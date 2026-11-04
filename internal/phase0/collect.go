package phase0

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/jsonx"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/proxypool"
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
	Log    *slog.Logger // optional

	playerMu       sync.Mutex
	playerOffUntil time.Time // player calls are skipped until then
}

// The player endpoint is the one YouTube guards hardest, and everything the
// crawl asks it for is optional (a Short's date, a video's tags). A player
// call waits at most playerWait for a proxy and an answer, and after a block
// every player call is skipped for playerRest, so the endpoint can never hold up the
// listing. On 2 November 2026 two blocked Shorts date checks rested the whole
// proxy group and a 10-minute test took 4 hours.
var (
	playerWait = 20 * time.Second
	playerRest = 30 * time.Minute
)

// errPlayerResting is reported instead of calling the player while it rests.
var errPlayerResting = errors.New("player endpoint resting after a block; skipped")

// playerBestEffort calls the player endpoint as described above.
func (c *Collector) playerBestEffort(ctx context.Context, cost *Cost, label, id, prefer string) (*innertube.Response, error) {
	c.playerMu.Lock()
	resting := c.Now().Before(c.playerOffUntil)
	c.playerMu.Unlock()
	if resting {
		cost.add(label, nil) // makes sure the map exists
		cost.ByEndpoint[label+":skipped"]++
		return nil, errPlayerResting
	}
	pctx, cancel := context.WithTimeout(ctx, playerWait)
	defer cancel()
	r, err := c.callVia(pctx, cost, innertube.Player, label, map[string]any{"videoId": id}, prefer)
	// Rest only after a block. A timeout means the pool was busy (or the
	// answer slow): this call is skipped, the next one tries again.
	if err != nil && ctx.Err() == nil && (errors.Is(err, innertube.ErrBlocked) || errors.Is(err, proxypool.ErrParked)) {
		c.playerMu.Lock()
		c.playerOffUntil = c.Now().Add(playerRest)
		c.playerMu.Unlock()
		c.warn("player endpoint blocked or busy; skipping player calls for a while", "rest", playerRest.String())
	}
	return r, err
}

func (c *Collector) call(ctx context.Context, cost *Cost, ep innertube.Endpoint, label string, payload map[string]any) (*innertube.Response, error) {
	return c.callVia(ctx, cost, ep, label, payload, "")
}

// callVia sends the request through the preferred proxy when it is healthy.
// Chains of continuation requests pass the previous response's proxy, so
// every page of a list is read by the same IP and visitor ID.
func (c *Collector) callVia(ctx context.Context, cost *Cost, ep innertube.Endpoint, label string, payload map[string]any, prefer string) (*innertube.Response, error) {
	r, err := c.Client.CallPrefer(ctx, ep, payload, prefer)
	cost.add(label, r)
	return r, err
}

func (c *Collector) warn(msg string, args ...any) {
	if c.Log != nil {
		c.Log.Warn(msg, args...)
	}
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
	// A handle can point to the channel's older custom URL: on 2 November
	// 2026 /@TRTArabi answered resolve_url with a urlEndpoint to /trtarabi
	// and its page with a 303 there. Such a youtube.com pointer is followed
	// once. why collects each step's outcome, so a failure says what YouTube
	// answered (only endpoint kinds, page types, statuses and paths).
	var why []string
	var firstErr error
	for hop := 0; hop < 2; hop++ {
		id, next, w, err := c.resolveOnce(ctx, url, in, cost)
		if id != "" {
			return id, nil
		}
		why = append(why, w...)
		if firstErr == nil {
			firstErr = err
		}
		if next == "" || sameYouTubePath(next, url) {
			break
		}
		why = append(why, "following to "+youtubePath(next))
		url = next
	}
	if firstErr != nil {
		return "", fmt.Errorf("could not resolve %q to a channel: %s: %w", input, strings.Join(why, "; "), firstErr)
	}
	return "", fmt.Errorf("could not resolve %q to a channel: %s", input, strings.Join(why, "; "))
}

// resolveOnce tries resolve_url, then the page itself. When neither names a
// channel, next is a youtube.com URL that either of them pointed to.
func (c *Collector) resolveOnce(ctx context.Context, url, in string, cost *Cost) (id, next string, why []string, err error) {
	r, err := c.call(ctx, cost, innertube.ResolveURL, "resolve", map[string]any{"url": url})
	if err == nil {
		if id := channelIDIn(r.Data); id != "" {
			return id, "", nil, nil
		}
		c.Store.SaveSkeleton("resolve-"+shortHash(in), r.Body)
		why = append(why, "resolve_url answered "+endpointKind(r.Data))
		if u := jsonx.String(r.Data, "endpoint", "urlEndpoint", "url"); youtubePath(u) != "" && !sameYouTubePath(u, url) {
			return "", u, why, nil // go straight there: this page would only redirect
		}
	} else {
		why = append(why, "resolve_url: "+err.Error())
	}
	// Fallback: the channel's own page names its channel ID.
	if path := youtubePath(url); path != "" {
		pr, perr := c.Client.Get(ctx, proxypool.ClassWatch, path, nil)
		cost.add("page:channel", pr)
		var re *innertube.RedirectError
		switch {
		case errors.As(perr, &re):
			why = append(why, "channel page: "+perr.Error())
			if loc := re.Location.String(); youtubePath(loc) != "" {
				next = loc
			}
		case perr != nil:
			why = append(why, "channel page: "+perr.Error())
		default:
			if id := channelIDInHTML(pr.Body); id != "" {
				return id, "", nil, nil
			}
			why = append(why, fmt.Sprintf("channel page (HTTP %d, %d bytes) names no channel ID", pr.Status, len(pr.Body)))
		}
	}
	return "", next, why, err
}

func sameYouTubePath(a, b string) bool {
	return strings.EqualFold(strings.TrimSuffix(youtubePath(a), "/"), strings.TrimSuffix(youtubePath(b), "/"))
}

// endpointKind describes a resolve_url answer without IDs, for example
// "urlEndpoint to www.youtube.com/@name (WEB_PAGE_TYPE_UNKNOWN)".
func endpointKind(data any) string {
	ep := jsonx.Map(data, "endpoint")
	kind := "no endpoint"
	keys := make([]string, 0, len(ep))
	for k := range ep {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.HasSuffix(k, "Endpoint") {
			kind = k
			break
		}
	}
	if u, err := url.Parse(jsonx.String(ep, "urlEndpoint", "url")); err == nil && u.Host != "" {
		kind += " to " + u.Host + u.Path
	}
	if t := jsonx.String(ep, "commandMetadata", "webCommandMetadata", "webPageType"); t != "" {
		kind += " (" + t + ")"
	}
	return kind
}

var channelIDRe = regexp.MustCompile(`^UC[A-Za-z0-9_-]{22}$`)

// channelIDIn returns the first channel ID found under a "browseId" key.
func channelIDIn(data any) string {
	for _, v := range jsonx.FindAll(data, "browseId") {
		if s, ok := v.(string); ok && channelIDRe.MatchString(s) {
			return s
		}
	}
	return ""
}

var htmlChannelIDRes = []*regexp.Regexp{
	regexp.MustCompile(`<link rel="canonical" href="https://www\.youtube\.com/channel/(UC[A-Za-z0-9_-]{22})"`),
	regexp.MustCompile(`"externalId"\s*:\s*"(UC[A-Za-z0-9_-]{22})"`),
	regexp.MustCompile(`"channelId"\s*:\s*"(UC[A-Za-z0-9_-]{22})"`),
}

// channelIDInHTML reads a channel page's own channel ID.
func channelIDInHTML(body []byte) string {
	for _, re := range htmlChannelIDRes {
		if m := re.FindSubmatch(body); m != nil {
			return string(m[1])
		}
	}
	return ""
}

// youtubePath returns the path (and query) of a youtube.com URL, or "".
func youtubePath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Path == "" {
		return ""
	}
	if h := strings.ToLower(u.Hostname()); h != "youtube.com" && !strings.HasSuffix(h, ".youtube.com") {
		return ""
	}
	return u.RequestURI()
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
		ar, err := c.callVia(ctx, cost, innertube.Browse, "browse:about", map[string]any{"continuation": res.Channel.AboutToken}, r.Proxy)
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
	Undated      int  // regular videos whose date could not be read
	DateProbes   int  // player requests made to date an undated page
}

// Tab lists a channel tab, newest first, until items are older than since
// (when their age is known), the list ends, or maxPages pages were read.
func (c *Collector) Tab(ctx context.Context, channelID, params string, since time.Time, maxPages int, cost *Cost) (TabResult, error) {
	var res TabResult
	if params == "" {
		return res, errors.New("tab not found on channel page")
	}
	payload := map[string]any{"browseId": channelID, "params": params}
	prefer := ""
	for res.Pages < maxPages {
		r, err := c.callVia(ctx, cost, innertube.Browse, "browse:tab", payload, prefer)
		if err != nil {
			return res, err
		}
		res.Pages++
		prefer = r.Proxy
		l := ytparse.ParseList(r.Data, c.Now())
		if len(l.Videos) == 0 && l.NextToken == "" {
			if res.Pages == 1 {
				c.Store.SaveSkeleton("tab-"+shortHash(channelID+params), r.Body)
			} else {
				// A "next page" that holds nothing is suspicious: keep its
				// structure so the cause can be found.
				c.Store.SaveSkeleton("tabnext-"+shortHash(channelID+params), r.Body)
				c.warn("next page of a channel tab was empty", "page", res.Pages, "bytes", len(r.Body))
			}
		}
		old, dated := false, 0
		for _, v := range l.Videos {
			if v.Published.OK {
				dated++
			} else if v.Kind == ytparse.KindVideo {
				res.Undated++
			}
			if v.Published.OK && v.Published.At.Before(since) {
				old = true
				continue
			}
			res.Items = append(res.Items, v)
		}
		// Shorts carry no date on the tab. When a page with more to come
		// holds no dated item, the player gives its last item's publish
		// date (one request per page), so the window still ends the list.
		// The page that crosses the window is kept whole. No probe on the
		// last page allowed (the player is the endpoint guarded hardest).
		if !old && dated == 0 && len(l.Videos) > 0 && l.NextToken != "" && res.Pages < maxPages {
			if at, ok := c.publishedAt(ctx, l.Videos[len(l.Videos)-1].VideoID, cost); ok {
				res.DateProbes++
				old = at.Before(since)
			}
		}
		if old || l.NextToken == "" {
			return res, nil
		}
		payload = map[string]any{"continuation": l.NextToken}
	}
	res.ReachedLimit = true
	return res, nil
}

// publishedAt reads a video's publish date from the player endpoint.
func (c *Collector) publishedAt(ctx context.Context, id string, cost *Cost) (time.Time, bool) {
	r, err := c.playerBestEffort(ctx, cost, "player:date", id, "")
	if err != nil {
		return time.Time{}, false
	}
	return parsePublishDate(ytparse.ParsePlayer(r.Data).PublishDate)
}

// parsePublishDate reads the player's publishDate: a date-time with offset,
// or a plain date on older responses.
func parsePublishDate(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// VideoResult is a video's watch page and player data.
type VideoResult struct {
	Watch     ytparse.WatchInfo
	Player    ytparse.PlayerInfo
	PlayerErr string
	Proxy     string // proxy that read the watch page; comment chains start there
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
	res.Proxy = r.Proxy
	if res.Watch.Title == "" {
		c.Store.SaveSkeleton("watch-"+shortHash(id), r.Body)
	}
	if withPlayer {
		pr, err := c.playerBestEffort(ctx, cost, "player", id, r.Proxy)
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
// from the creator even when YouTube's creator flag is missing. prefer is
// the proxy that read the watch page: the whole sweep stays on it.
//
// limit caps the comments read (top-level plus replies); 0 reads them all.
func (c *Collector) Sweep(ctx context.Context, videoID, channelID, commentsToken, prefer string, limit int, cost *Cost) (SweepResult, error) {
	res := SweepResult{PrecisionBuckets: map[string]int{}, channelID: channelID}
	room := func() bool { return limit <= 0 || res.TopLevel+res.Replies < limit }
	if commentsToken == "" {
		res.NoCommentsToken = true
		return res, nil
	}
	r, err := c.callVia(ctx, cost, innertube.Next, "next:comments", map[string]any{"continuation": commentsToken}, prefer)
	if err != nil {
		return res, err
	}
	prefer = r.Proxy
	first := ytparse.ParseComments(r.Data, c.Now())
	res.Displayed = first.Total
	token := first.NewestFirstToken
	if token == "" {
		// No sort menu: read the default order instead.
		c.Store.SaveSkeleton("commentsheader-"+shortHash(videoID), r.Body)
		res.absorb(first, c.Now())
		token = first.NextToken
	} else if !first.Total.OK {
		c.Store.SaveSkeleton("commentscount-"+shortHash(videoID), r.Body)
	}
	replyTokens := map[string]string{}
	for k, v := range first.ReplyTokens {
		replyTokens[k] = v
	}
	for token != "" && room() {
		r, err := c.callVia(ctx, cost, innertube.Next, "next:comments", map[string]any{"continuation": token}, prefer)
		if err != nil {
			return res, err
		}
		res.Pages++
		prefer = r.Proxy
		p := ytparse.ParseComments(r.Data, c.Now())
		if res.Pages == 1 && len(p.Comments) == 0 && p.NextToken == "" {
			c.Store.SaveSkeleton("commentspage-"+shortHash(videoID), r.Body)
			if res.Displayed.Min > 0 {
				c.warn("first comment page was empty", "displayed", res.Displayed.Min, "bytes", len(r.Body))
			}
		}
		res.absorb(p, c.Now())
		for k, v := range p.ReplyTokens {
			replyTokens[k] = v
		}
		token = p.NextToken
	}
	for _, tok := range replyTokens {
		for tok != "" && room() {
			r, err := c.callVia(ctx, cost, innertube.Next, "next:replies", map[string]any{"continuation": tok}, prefer)
			if err != nil {
				return res, err
			}
			res.ReplyPages++
			p := ytparse.ParseComments(r.Data, c.Now())
			res.absorb(p, c.Now())
			tok = p.NextToken
		}
	}
	res.Capped = limit > 0 && res.TopLevel+res.Replies >= limit
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
