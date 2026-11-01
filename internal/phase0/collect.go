package phase0

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytparse"
)

// Cost adds up what a piece of work took.
type Cost struct {
	Requests   int            `json:"requests"`
	WireBytes  int64          `json:"wire_bytes"`
	ByEndpoint map[string]int `json:"by_endpoint"`
	Blocks     int            `json:"blocks"`  // block signals met (then failed over)
	Retries    int            `json:"retries"` // transient retries and proxy failures
}

func (c *Cost) add(ep string, r *innertube.Response) {
	if c.ByEndpoint == nil {
		c.ByEndpoint = map[string]int{}
	}
	if r == nil {
		return
	}
	c.Requests += r.Attempt.Attempts
	c.WireBytes += r.WireBytes
	c.ByEndpoint[ep] += r.Attempt.Attempts
	c.Blocks += len(r.Attempt.BlockedOn)
	c.Retries += r.Attempt.Transient + r.Attempt.ProxyFailures
}

// Merge adds o into c.
func (c *Cost) Merge(o Cost) {
	if c.ByEndpoint == nil {
		c.ByEndpoint = map[string]int{}
	}
	c.Requests += o.Requests
	c.WireBytes += o.WireBytes
	c.Blocks += o.Blocks
	c.Retries += o.Retries
	for k, v := range o.ByEndpoint {
		c.ByEndpoint[k] += v
	}
}

// Collector wraps the website client with the calls Phase 0 needs.
type Collector struct {
	Client *innertube.Client
	Store  *Store
	Now    func() time.Time
}

func (c *Collector) call(ctx context.Context, cost *Cost, ep innertube.Endpoint, label string, payload map[string]any) (*innertube.Response, error) {
	r, err := c.Client.Call(ctx, ep, payload)
	cost.add(label, r)
	return r, err
}

// Resolve turns an input line (channel ID, @handle or channel URL) into a
// channel ID.
func (c *Collector) Resolve(ctx context.Context, input string, cost *Cost) (string, error) {
	in := strings.TrimSpace(input)
	if strings.HasPrefix(in, "UC") && len(in) == 24 {
		return in, nil
	}
	if i := strings.Index(in, "/channel/"); i >= 0 {
		id := in[i+len("/channel/"):]
		if j := strings.IndexAny(id, "/?#"); j >= 0 {
			id = id[:j]
		}
		return id, nil
	}
	url := in
	if strings.HasPrefix(in, "@") {
		url = "https://www.youtube.com/" + in
	}
	r, err := c.call(ctx, cost, innertube.ResolveURL, "resolve", map[string]any{"url": url})
	if err != nil {
		return "", err
	}
	id := firstString(r.Data, "browseId")
	if !strings.HasPrefix(id, "UC") {
		return "", fmt.Errorf("could not resolve %q to a channel", input)
	}
	return id, nil
}

// ChannelResult is a channel page plus its About panel.
type ChannelResult struct {
	Channel  ytparse.Channel
	About    ytparse.About
	HasAbout bool
}

// Channel fetches a channel's page and About panel.
func (c *Collector) Channel(ctx context.Context, id string, cost *Cost) (ChannelResult, error) {
	var res ChannelResult
	r, err := c.call(ctx, cost, innertube.Browse, "browse:channel", map[string]any{"browseId": id})
	if err != nil {
		return res, err
	}
	res.Channel = ytparse.ParseChannel(r.Data)
	if res.Channel.ID == "" || len(res.Channel.Tabs) == 0 {
		c.Store.SaveSkeleton("channel-"+shortHash(id), r.Body)
	}
	if res.Channel.AboutToken != "" {
		ar, err := c.call(ctx, cost, innertube.Browse, "browse:about", map[string]any{"continuation": res.Channel.AboutToken})
		if err == nil {
			res.About, res.HasAbout = ytparse.ParseAbout(ar.Data)
			if !res.HasAbout {
				c.Store.SaveSkeleton("about-"+shortHash(id), ar.Body)
			}
		}
	}
	return res, nil
}

// TabResult is the list of items read from one channel tab.
type TabResult struct {
	Items        []ytparse.VideoItem
	Pages        int
	ReachedLimit bool // stopped at maxPages before the window ended
}

// Tab lists a channel tab, newest first, until items are older than since
// (when their age is known), the list ends, or maxPages pages were read.
func (c *Collector) Tab(ctx context.Context, channelID, params string, since time.Time, maxPages int, cost *Cost) (TabResult, error) {
	var res TabResult
	if params == "" {
		return res, errors.New("tab not found on channel page")
	}
	payload := map[string]any{"browseId": channelID, "params": params}
	for res.Pages < maxPages {
		r, err := c.call(ctx, cost, innertube.Browse, "browse:tab", payload)
		if err != nil {
			return res, err
		}
		res.Pages++
		l := ytparse.ParseList(r.Data, c.Now())
		if res.Pages == 1 && len(l.Videos) == 0 && l.NextToken == "" {
			c.Store.SaveSkeleton("tab-"+shortHash(channelID+params), r.Body)
		}
		old := false
		for _, v := range l.Videos {
			if v.Published.OK && v.Published.At.Before(since) {
				old = true
				continue
			}
			res.Items = append(res.Items, v)
		}
		if old || l.NextToken == "" {
			return res, nil
		}
		payload = map[string]any{"continuation": l.NextToken}
	}
	res.ReachedLimit = true
	return res, nil
}

// VideoResult is a video's watch page and player data.
type VideoResult struct {
	Watch     ytparse.WatchInfo
	Player    ytparse.PlayerInfo
	PlayerErr string
}

// Video fetches the watch page ("next") and the player data. A blocked
// player endpoint is reported, not retried beyond the pool's failover.
func (c *Collector) Video(ctx context.Context, id string, withPlayer bool, cost *Cost) (VideoResult, error) {
	var res VideoResult
	r, err := c.call(ctx, cost, innertube.Next, "next:watch", map[string]any{"videoId": id})
	if err != nil {
		return res, err
	}
	res.Watch = ytparse.ParseWatch(r.Data)
	if res.Watch.Title == "" {
		c.Store.SaveSkeleton("watch-"+shortHash(id), r.Body)
	}
	if withPlayer {
		pr, err := c.call(ctx, cost, innertube.Player, "player", map[string]any{"videoId": id})
		if err != nil {
			res.PlayerErr = err.Error()
		} else {
			res.Player = ytparse.ParsePlayer(pr.Data)
			if res.Player.VideoID == "" {
				c.Store.SaveSkeleton("player-"+shortHash(id), pr.Body)
			}
		}
	}
	return res, nil
}

// SnapComment is what Phase 0 keeps about a comment: no text, no author.
type SnapComment struct {
	ID         string `json:"id"`
	TextHash   string `json:"text_hash"`
	Likes      string `json:"likes"`
	ReplyCount int64  `json:"reply_count"`
	Edited     bool   `json:"edited"`
	Pinned     bool   `json:"pinned"`
	Hearted    bool   `json:"hearted"`
	IsReply    bool   `json:"is_reply"`
}

// SweepResult describes a full comment sweep of one video.
type SweepResult struct {
	Displayed        ytparse.Count  `json:"displayed"`
	TopLevel         int            `json:"top_level"`
	Replies          int            `json:"replies"`
	Pages            int            `json:"pages"`
	ReplyPages       int            `json:"reply_pages"`
	Capped           bool           `json:"capped"`
	Pinned           int            `json:"pinned"`
	Hearted          int            `json:"hearted"`
	CreatorReplies   int            `json:"creator_replies"`
	Edited           int            `json:"edited"`
	LikesAbbreviated int            `json:"likes_abbreviated"`
	PrecisionBuckets map[string]int `json:"precision_buckets"` // "minute", "hour", "day", "week", "month", "year", "unknown"
	NoCommentsToken  bool           `json:"no_comments_token"`
	Snapshot         []SnapComment  `json:"-"`

	seen      map[string]bool
	channelID string
}

// Sweep reads all comments (newest first) and their replies, up to limit
// comments in total. channelID (the video's channel) identifies replies
// from the creator even when YouTube's creator flag is missing.
func (c *Collector) Sweep(ctx context.Context, videoID, channelID, commentsToken string, limit int, cost *Cost) (SweepResult, error) {
	res := SweepResult{PrecisionBuckets: map[string]int{}, channelID: channelID}
	if commentsToken == "" {
		res.NoCommentsToken = true
		return res, nil
	}
	r, err := c.call(ctx, cost, innertube.Next, "next:comments", map[string]any{"continuation": commentsToken})
	if err != nil {
		return res, err
	}
	first := ytparse.ParseComments(r.Data, c.Now())
	res.Displayed = first.Total
	token := first.NewestFirstToken
	if token == "" {
		// No sort menu: read the default order instead.
		c.Store.SaveSkeleton("comments-header-"+shortHash(videoID), r.Body)
		res.absorb(first, c.Now())
		token = first.NextToken
	}
	replyTokens := map[string]string{}
	for k, v := range first.ReplyTokens {
		replyTokens[k] = v
	}
	for token != "" && res.TopLevel+res.Replies < limit {
		r, err := c.call(ctx, cost, innertube.Next, "next:comments", map[string]any{"continuation": token})
		if err != nil {
			return res, err
		}
		res.Pages++
		p := ytparse.ParseComments(r.Data, c.Now())
		if res.Pages == 1 && len(p.Comments) == 0 && p.NextToken == "" && res.Displayed.Min > 0 {
			c.Store.SaveSkeleton("comments-page-"+shortHash(videoID), r.Body)
		}
		res.absorb(p, c.Now())
		for k, v := range p.ReplyTokens {
			replyTokens[k] = v
		}
		token = p.NextToken
	}
	for _, tok := range replyTokens {
		for tok != "" && res.TopLevel+res.Replies < limit {
			r, err := c.call(ctx, cost, innertube.Next, "next:replies", map[string]any{"continuation": tok})
			if err != nil {
				return res, err
			}
			res.ReplyPages++
			p := ytparse.ParseComments(r.Data, c.Now())
			res.absorb(p, c.Now())
			tok = p.NextToken
		}
	}
	res.Capped = res.TopLevel+res.Replies >= limit
	return res, nil
}

func (s *SweepResult) absorb(p ytparse.CommentsPage, now time.Time) {
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	for _, cm := range p.Comments {
		if s.seen[cm.ID] {
			continue
		}
		s.seen[cm.ID] = true
		if cm.IsReply {
			s.Replies++
		} else {
			s.TopLevel++
		}
		if cm.Pinned {
			s.Pinned++
		}
		if cm.Hearted {
			s.Hearted++
		}
		if cm.IsReply && (cm.AuthorIsCreator || (s.channelID != "" && cm.AuthorChannelID == s.channelID)) {
			s.CreatorReplies++
		}
		if cm.Edited {
			s.Edited++
		}
		if cm.Likes.OK && !cm.Likes.Exact {
			s.LikesAbbreviated++
		}
		s.PrecisionBuckets[precisionBucket(cm.Published)]++
		s.Snapshot = append(s.Snapshot, SnapComment{
			ID: cm.ID, TextHash: shortHash(cm.Text), Likes: cm.Likes.Text, ReplyCount: cm.ReplyCount,
			Edited: cm.Edited, Pinned: cm.Pinned, Hearted: cm.Hearted, IsReply: cm.IsReply,
		})
	}
}

func precisionBucket(t ytparse.TimeEstimate) string {
	if !t.OK {
		return "unknown"
	}
	switch {
	case t.Precision <= time.Minute:
		return "minute"
	case t.Precision <= time.Hour:
		return "hour"
	case t.Precision <= 24*time.Hour:
		return "day"
	case t.Precision <= 7*24*time.Hour:
		return "week"
	case t.Precision <= 31*24*time.Hour:
		return "month"
	}
	return "year"
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// firstString finds the first string stored under key anywhere in v.
func firstString(v any, key string) string {
	switch t := v.(type) {
	case map[string]any:
		if s, ok := t[key].(string); ok {
			return s
		}
		for _, x := range t {
			if s := firstString(x, key); s != "" {
				return s
			}
		}
	case []any:
		for _, x := range t {
			if s := firstString(x, key); s != "" {
				return s
			}
		}
	}
	return ""
}
