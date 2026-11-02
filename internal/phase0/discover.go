package phase0

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/keywords"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytapi"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytparse"
)

// Discovery (E9) runs each search term through the official API's search
// (D4, quota-limited) and through the website's search (D8), and records
// per term and engine how many results a call or page gives, how deep a
// query goes before results run out, and which channels each engine finds
// that are not in the list yet. The found videos (ID, channel, public title)
// are kept for the channel census (E10) and the keyword-matching check
// (E11), in the data folder only, and deleted with the rest after 14 days.
// Search terms are stored as hashes: your own keywords may be sensitive.

// DiscoverOptions configures one discovery run.
type DiscoverOptions struct {
	Terms     []string
	WebPages  int           // website search pages per term
	APIPages  int           // API pages (50 results, 100 units each) per term
	APIUnits  int           // quota units this run may use at most
	APIWithin time.Duration // API: only videos published within this window
	WebParams string        // website search filter params; "" = relevance
}

// DefaultDiscoverOptions: 10 website pages and 1 API page per term, at most
// 2,000 API units (20 searches) per run, API limited to the last 30 days.
func DefaultDiscoverOptions() DiscoverOptions {
	return DiscoverOptions{WebPages: 10, APIPages: 1, APIUnits: 2000, APIWithin: 30 * 24 * time.Hour}
}

// DiscoverRecord is one term on one engine (aggregates only).
type DiscoverRecord struct {
	At            time.Time `json:"at"`
	Run           string    `json:"run"`
	Engine        string    `json:"engine"` // "api" or "web"
	TermHash      string    `json:"term_hash"`
	Pages         int       `json:"pages"`
	Results       int       `json:"results"` // videos returned, before de-duplication
	Videos        int       `json:"videos"`
	Channels      int       `json:"channels"`
	NewChannels   int       `json:"new_channels"`   // not in the crawled list
	NewQualifying int       `json:"new_qualifying"` // ... with a video whose title matches the keywords
	RanOut        bool      `json:"ran_out"`        // results ended before the page limit
	Units         int       `json:"units"`
	Requests      int       `json:"requests"`
	Error         string    `json:"error,omitempty"`
}

// DiscoverItem is one found video, kept for the census and E11.
type DiscoverItem struct {
	At        time.Time `json:"at"`
	Run       string    `json:"run"`
	Engine    string    `json:"engine"`
	TermHash  string    `json:"term_hash"`
	VideoID   string    `json:"video_id"`
	ChannelID string    `json:"channel_id"`
	Title     string    `json:"title"`
	Qualifies bool      `json:"qualifies"`
}

// SeedTerms returns up to n search terms from the seed keywords: the first
// spelling of each keyword not marked ambiguous.
func SeedTerms(n int) []string { return seedQueries(n) }

// discoveryMatcher matches the seed keywords plus the run's own terms.
type discoveryMatcher struct {
	m         *keywords.Matcher
	ambiguous map[string]bool
}

func newDiscoveryMatcher(terms []string) *discoveryMatcher {
	terms = uniqueTerms(terms)
	kws, _ := keywords.Seed()
	for i, t := range terms {
		kws = append(kws, keywords.Keyword{ID: fmt.Sprintf("term-%d", i), Kind: "term", Variants: []string{t}})
	}
	dm := &discoveryMatcher{m: keywords.NewMatcher(kws), ambiguous: map[string]bool{}}
	for _, k := range kws {
		dm.ambiguous[k.ID] = k.Ambiguous
	}
	return dm
}

// qualifies applies the seed list's rule: a match on an ambiguous keyword
// needs a second, different keyword before it counts.
func (d *discoveryMatcher) qualifies(text string) bool {
	hits := d.m.Match(text)
	ids := map[string]bool{}
	for _, h := range hits {
		if !d.ambiguous[h.KeywordID] {
			return true
		}
		ids[h.KeywordID] = true
	}
	return len(ids) >= 2
}

// knownChannelHashes are the channels already in your list: the crawled
// ones, plus every channel the list resolved to (detection's lookup cache),
// so a listed channel whose crawl failed is not counted as a new find.
func knownChannelHashes(s *Store) map[string]bool {
	recs, _ := ReadAll[ChannelRecord](s, "crawl_channels")
	known := map[string]bool{}
	for _, r := range recs {
		if r.Error == "" {
			known[r.ChannelHash] = true
		}
	}
	resolved := map[string]string{}
	if ok, _ := s.ReadJSON("resolved.json", &resolved); ok {
		for _, id := range resolved {
			if id != "" {
				known[shortHash(id)] = true
			}
		}
	}
	return known
}

// Discover runs every term on both engines. api may be nil (no key): then
// only the website is searched.
func Discover(ctx context.Context, col *Collector, api *ytapi.Client, o DiscoverOptions, log *slog.Logger) error {
	o.Terms = uniqueTerms(o.Terms)
	if len(o.Terms) == 0 {
		return errors.New("discover: no search terms")
	}
	run := col.Now().UTC().Format(time.RFC3339)
	known := knownChannelHashes(col.Store)
	match := newDiscoveryMatcher(o.Terms)
	unitsLeft := o.APIUnits
	for _, term := range o.Terms {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		th := shortHash(term)
		web, webItems := col.webSearch(ctx, term, o)
		finishDiscover(col, run, th, "web", &web, webItems, known, match, log)
		if api == nil {
			continue
		}
		if unitsLeft < ytapi.CostSearch {
			log.Warn("discover: API unit budget for this run used up; website search continues", "budget", o.APIUnits)
			api = nil
			continue
		}
		rec, apiItems := apiSearch(ctx, api, term, o, &unitsLeft, col.Now())
		finishDiscover(col, run, th, "api", &rec, apiItems, known, match, log)
		if errors.Is(rec.err, ytapi.ErrQuota) {
			log.Warn("discover: daily API quota reached; website search continues")
			api = nil
		}
	}
	return nil
}

// discoverPage is what one engine found for one term before bookkeeping.
type discoverPage struct {
	DiscoverRecord
	channels        map[string]bool // every channel seen, including channel results
	err             error
	warnedNoChannel bool
}

func finishDiscover(col *Collector, run, th, engine string, rec *discoverPage, items []DiscoverItem, known map[string]bool, m *discoveryMatcher, log *slog.Logger) {
	now := col.Now()
	videos := map[string]bool{}
	qualifyingCh := map[string]bool{}
	for i := range items {
		it := &items[i]
		it.At, it.Run, it.Engine, it.TermHash = now, run, engine, th
		it.Qualifies = m.qualifies(it.Title)
		if it.Qualifies {
			qualifyingCh[it.ChannelID] = true
		}
		if !videos[it.VideoID] {
			videos[it.VideoID] = true
			_ = col.Store.Append("discover_items", *it)
		}
	}
	rec.At, rec.Run, rec.Engine, rec.TermHash = now, run, engine, th
	rec.Videos, rec.Channels = len(videos), len(rec.channels)
	for ch := range rec.channels {
		if !known[shortHash(ch)] {
			rec.NewChannels++
			if qualifyingCh[ch] {
				rec.NewQualifying++
			}
		}
	}
	if rec.err != nil {
		rec.Error = rec.err.Error()
	}
	_ = col.Store.Append("discover", rec.DiscoverRecord)
	log.Info("search term done", "engine", engine, "term", th, "pages", rec.Pages, "results", rec.Results, "channels", rec.Channels, "new", rec.NewChannels, "ran_out", rec.RanOut)
}

// webSearch reads up to o.WebPages pages of the website's search for term.
// The pages of one query stay on one proxy, like a tab's pages.
func (c *Collector) webSearch(ctx context.Context, term string, o DiscoverOptions) (discoverPage, []DiscoverItem) {
	rec := discoverPage{channels: map[string]bool{}}
	var items []DiscoverItem
	payload := map[string]any{"query": term}
	if o.WebParams != "" {
		payload["params"] = o.WebParams
	}
	var cost Cost
	prefer := ""
	for rec.Pages < o.WebPages {
		r, err := c.callVia(ctx, &cost, innertube.Search, "search:web", payload, prefer)
		if err != nil {
			rec.err = err
			break
		}
		prefer = r.Proxy
		rec.Pages++
		l := ytparse.ParseList(r.Data, c.Now())
		if rec.Pages == 1 && len(l.Videos) == 0 && len(l.Channels) == 0 {
			c.Store.SaveSkeleton("search-"+shortHash(term), r.Body)
		}
		rec.Results += len(l.Videos) // videos only, like the API's results
		noChannel := 0
		for _, v := range l.Videos {
			if v.ChannelID == "" {
				noChannel++
			}
		}
		if len(l.Videos) > 0 && noChannel == len(l.Videos) && !rec.warnedNoChannel {
			// Videos without channel IDs would silently vanish from the
			// channel counts: keep the page's structure so the parser can
			// be fixed.
			rec.warnedNoChannel = true
			c.Store.SaveSkeleton("searchnochannel-"+shortHash(term), r.Body)
			c.warn("search results carry no channel IDs; saved a skeleton", "videos", len(l.Videos))
		}
		for _, v := range l.Videos {
			if v.ChannelID != "" {
				rec.channels[v.ChannelID] = true
			}
			items = append(items, DiscoverItem{VideoID: v.VideoID, ChannelID: v.ChannelID, Title: v.Title})
		}
		for _, ch := range l.Channels {
			if ch.ChannelID != "" {
				rec.channels[ch.ChannelID] = true
			}
		}
		if l.NextToken == "" || len(l.Videos)+len(l.Channels) == 0 {
			rec.RanOut = true
			break
		}
		payload = map[string]any{"continuation": l.NextToken}
	}
	rec.Requests = cost.Requests
	return rec, items
}

// apiSearch reads up to o.APIPages pages of the official search for term,
// newest first, within the unit budget.
func apiSearch(ctx context.Context, api *ytapi.Client, term string, o DiscoverOptions, unitsLeft *int, now time.Time) (discoverPage, []DiscoverItem) {
	rec := discoverPage{channels: map[string]bool{}}
	var items []DiscoverItem
	opts := ytapi.SearchOptions{RegionCode: "IQ", RelevanceLanguage: "ar", MaxResults: 50}
	if o.APIWithin > 0 {
		opts.PublishedAfter = now.Add(-o.APIWithin)
	}
	for rec.Pages < o.APIPages && *unitsLeft >= ytapi.CostSearch {
		res, err := api.Search(ctx, term, opts)
		if err != nil {
			rec.err = err
			break
		}
		*unitsLeft -= ytapi.CostSearch
		rec.Units += ytapi.CostSearch
		rec.Requests++
		rec.Pages++
		rec.Results += len(res.Hits)
		for _, h := range res.Hits {
			rec.channels[h.ChannelID] = true
			items = append(items, DiscoverItem{VideoID: h.VideoID, ChannelID: h.ChannelID, Title: h.Title})
		}
		if res.NextPageToken == "" || len(res.Hits) == 0 {
			rec.RanOut = true
			break
		}
		opts.PageToken = res.NextPageToken
	}
	return rec, items
}

// uniqueTerms drops empty terms and repeats (compared after normalization,
// so "العراق" from your file and from the seed list is searched once).
func uniqueTerms(terms []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range terms {
		k := keywords.Normalize(t)
		if strings.TrimSpace(k) == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, t)
	}
	return out
}

// containsIraq says whether an About-panel country names Iraq.
func containsIraq(country string) bool {
	n := keywords.Normalize(country)
	for _, w := range []string{"iraq", "irak", "عراق", "عيراق", "عێراق"} {
		if strings.Contains(n, keywords.Normalize(w)) {
			return true
		}
	}
	return false
}
