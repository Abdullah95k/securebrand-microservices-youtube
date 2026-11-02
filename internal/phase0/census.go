package phase0

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"sort"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytparse"
)

// The channel census (E10) profiles channels that discovery found and the
// list does not have yet: About panel, the first page of the videos tab and
// the newest video's watch page (about 4 requests each). It measures how
// many are Iraq channels ("core": the About country is Iraq, or the
// channel's own title, description or keywords match the seed keywords) and
// how many only mentioned a keyword ("mention"), their size, how often they
// upload and how many comments a video draws. Records keep hashes and
// numbers only.

// CensusOptions configures one census run.
type CensusOptions struct {
	Max int // channels to profile (the most often found first)
}

// CensusRecord is one profiled channel (no IDs, names or text).
type CensusRecord struct {
	At              time.Time     `json:"at"`
	ChannelHash     string        `json:"channel_hash"`
	Core            bool          `json:"core"`
	CountryIraq     bool          `json:"country_iraq"`
	MetaMatch       bool          `json:"meta_match"`
	FoundBy         string        `json:"found_by"` // "web", "api" or "both"
	Subscribers     ytparse.Count `json:"subscribers"`
	TotalVideos     ytparse.Count `json:"total_videos"`
	UploadsPerMonth float64       `json:"uploads_per_month"` // videos tab
	UploadsKnown    bool          `json:"uploads_known"`
	NewestComments  ytparse.Count `json:"newest_comments"` // displayed count on the newest video
	Requests        int           `json:"requests"`
	Error           string        `json:"error,omitempty"`
}

// censusCandidate is a found channel and how often discovery saw it.
type censusCandidate struct {
	id      string
	terms   map[string]bool
	items   int
	engines map[string]bool
}

// censusCandidates ranks the channels discovery found that are neither in
// the crawled list nor profiled already: most distinct terms first, then
// most videos.
func censusCandidates(s *Store) ([]censusCandidate, error) {
	items, err := ReadAll[DiscoverItem](s, "discover_items")
	if err != nil {
		return nil, err
	}
	known := knownChannelHashes(s)
	done, err := ReadAll[CensusRecord](s, "census")
	if err != nil {
		return nil, err
	}
	for _, r := range done {
		known[r.ChannelHash] = true
	}
	by := map[string]*censusCandidate{}
	for _, it := range items {
		if it.ChannelID == "" || known[shortHash(it.ChannelID)] {
			continue
		}
		c := by[it.ChannelID]
		if c == nil {
			c = &censusCandidate{id: it.ChannelID, terms: map[string]bool{}, engines: map[string]bool{}}
			by[it.ChannelID] = c
		}
		c.terms[it.TermHash] = true
		c.engines[it.Engine] = true
		c.items++
	}
	out := make([]censusCandidate, 0, len(by))
	for _, c := range by {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].terms) != len(out[j].terms) {
			return len(out[i].terms) > len(out[j].terms)
		}
		if out[i].items != out[j].items {
			return out[i].items > out[j].items
		}
		return out[i].id < out[j].id
	})
	return out, nil
}

// Census profiles up to o.Max candidates. Re-running continues with the
// channels not profiled yet.
func Census(ctx context.Context, col *Collector, o CensusOptions, log *slog.Logger) error {
	cands, err := censusCandidates(col.Store)
	if err != nil {
		return err
	}
	if len(cands) == 0 {
		return errors.New("census: no new channels to profile; run discover first (and the crawl, so list channels are left out)")
	}
	if o.Max > 0 && len(cands) > o.Max {
		cands = cands[:o.Max]
	}
	match := newDiscoveryMatcher(nil) // seed keywords only: "core" means an Iraq channel
	for i, c := range cands {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rec := col.profileChannel(ctx, c, match)
		if err := col.Store.Append("census", rec); err != nil {
			return err
		}
		if (i+1)%25 == 0 || i+1 == len(cands) {
			log.Info("census progress", "profiled", i+1, "of", len(cands))
		}
	}
	return nil
}

func (c *Collector) profileChannel(ctx context.Context, cand censusCandidate, match *discoveryMatcher) CensusRecord {
	rec := CensusRecord{At: c.Now(), ChannelHash: shortHash(cand.id)}
	switch {
	case cand.engines["web"] && cand.engines["api"]:
		rec.FoundBy = "both"
	case cand.engines["api"]:
		rec.FoundBy = "api"
	default:
		rec.FoundBy = "web"
	}
	var cost Cost
	defer func() { rec.Requests = cost.Requests }()
	ch, err := c.Channel(ctx, cand.id, &cost)
	if err != nil {
		rec.Error = err.Error()
		return rec
	}
	meta := ch.Channel.Title + " \n " + ch.Channel.Description + " \n " + ch.Channel.Keywords + " \n " + ch.About.Description
	rec.MetaMatch = match.qualifies(meta)
	rec.CountryIraq = containsIraq(ch.About.Country)
	rec.Core = rec.MetaMatch || rec.CountryIraq
	rec.Subscribers = ch.About.Subscribers
	if !rec.Subscribers.OK {
		rec.Subscribers = ch.Channel.Subscribers
	}
	rec.TotalVideos = ch.About.Videos
	if !rec.TotalVideos.OK {
		rec.TotalVideos = ch.Channel.VideoCount
	}
	tab, ok := ch.Channel.Tabs["videos"]
	if !ok {
		rec.UploadsKnown = true // no videos tab: no long-form uploads
		return rec
	}
	tr, err := c.Tab(ctx, cand.id, tab.Params, time.Time{}, 1, &cost)
	if err != nil {
		rec.Error = err.Error()
		return rec
	}
	rec.UploadsPerMonth, rec.UploadsKnown = uploadsPerMonth(tr.Items, tr.ReachedLimit, c.Now())
	if len(tr.Items) > 0 {
		if vr, err := c.Video(ctx, tr.Items[0].VideoID, false, &cost); err == nil {
			rec.NewestComments = vr.Watch.CommentCount
		}
	}
	return rec
}

// uploadsPerMonth estimates uploads in 30 days from one page of a videos
// tab (newest first): the dated videos of the last 30 days, or, when the
// tab has more pages and even this page's oldest video is younger than 30
// days, the page's rate scaled to 30 days. A tab that fits on one page is
// the whole history, so it is counted, not scaled (a new channel's two
// videos are two uploads, not a monthly rate).
func uploadsPerMonth(items []ytparse.VideoItem, more bool, now time.Time) (float64, bool) {
	if len(items) == 0 {
		return 0, true
	}
	month := 30 * 24 * time.Hour
	recent, dated := 0, 0
	var oldest time.Duration
	for _, v := range items {
		if !v.Published.OK {
			continue
		}
		dated++
		age := now.Sub(v.Published.At)
		if age <= month {
			recent++
		}
		if age > oldest {
			oldest = age
		}
	}
	if dated == 0 {
		return 0, false
	}
	if more && recent == dated && oldest > 0 && oldest < month {
		days := oldest.Hours() / 24
		if days < 1 {
			days = 1
		}
		return math.Round(float64(dated)*30/days*10) / 10, true
	}
	return float64(recent), true
}
