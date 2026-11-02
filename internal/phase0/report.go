package phase0

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/proxypool"
)

// PoolSnapshot records the proxy pool's counters at the end of a command.
type PoolSnapshot struct {
	At      time.Time       `json:"at"`
	Command string          `json:"command"`
	Run     string          `json:"run,omitempty"` // one process run; its snapshots are cumulative
	Stats   proxypool.Stats `json:"stats"`
}

// latestPerRun keeps the last snapshot of each run: a run's counters are
// cumulative, so adding its earlier snapshots would count requests twice.
// Snapshots written before runs were labelled each count as their own run.
func latestPerRun(pools []PoolSnapshot) []PoolSnapshot {
	last := map[string]int{}
	var order []string
	for i, ps := range pools {
		key := ps.Run
		if key == "" {
			key = fmt.Sprintf("old-%d", i)
		}
		if _, ok := last[key]; !ok {
			order = append(order, key)
		}
		if j, ok := last[key]; !ok || !pools[j].At.After(ps.At) {
			last[key] = i
		}
	}
	out := make([]PoolSnapshot, 0, len(order))
	for _, k := range order {
		out = append(out, pools[last[k]])
	}
	return out
}

var iraq = time.FixedZone("Iraq (UTC+3)", 3*3600)

// Report writes the Phase 0 report: aggregates only, no IDs, no text. It is
// safe to share and to commit as docs/phase0-results.md.
func Report(s *Store, w io.Writer, now time.Time) error {
	chans, err := ReadAll[ChannelRecord](s, "crawl_channels")
	if err != nil {
		return err
	}
	vids, err := ReadAll[VideoRecord](s, "crawl_videos")
	if err != nil {
		return err
	}
	res, err := ReadAll[ResweepRecord](s, "resweep")
	if err != nil {
		return err
	}
	feedsRecs, err := ReadAll[FeedRecord](s, "detect_feeds")
	if err != nil {
		return err
	}
	sights, err := ReadAll[SightingRecord](s, "detect_sightings")
	if err != nil {
		return err
	}
	acc, err := ReadAll[AccuracyRecord](s, "accuracy")
	if err != nil {
		return err
	}
	pools, err := ReadAll[PoolSnapshot](s, "pool_stats")
	if err != nil {
		return err
	}
	paceRecs, err := ReadAll[PaceRecord](s, "pace_steps")
	if err != nil {
		return err
	}
	discRecs, err := ReadAll[DiscoverRecord](s, "discover")
	if err != nil {
		return err
	}
	discItems, err := ReadAll[DiscoverItem](s, "discover_items")
	if err != nil {
		return err
	}
	census, err := ReadAll[CensusRecord](s, "census")
	if err != nil {
		return err
	}
	labelSampleRecs, err := ReadAll[LabelItem](s, "label_sample")
	if err != nil {
		return err
	}
	labelAnswers, err := ReadAll[LabelAnswer](s, "labels")
	if err != nil {
		return err
	}
	transcripts, err := ReadAll[TranscriptRecord](s, "transcripts")
	if err != nil {
		return err
	}
	skels, _ := s.List("skeletons")

	p := func(format string, a ...any) { fmt.Fprintf(w, format+"\n", a...) }
	p("# Phase 0 results")
	p("")
	p("Generated %s. Aggregates only: no channel or video IDs, no names, no comment text.", now.In(iraq).Format("2 January 2006 15:04 (Iraq time)"))
	p("")

	// Channels and listing cost (E5).
	ok := 0
	var listReq, listBytes, perHundred []float64
	classes := map[string][]float64{}
	for _, c := range chans {
		if c.Error != "" {
			continue
		}
		ok++
		listReq = append(listReq, float64(c.ListCost.Requests))
		if c.ListCost.Requests > 0 {
			listBytes = append(listBytes, float64(c.ListCost.WireBytes)/float64(c.ListCost.Requests))
		}
		n := c.VideosInWindow + c.ShortsInWindow + c.LiveInWindow
		if n > 0 {
			perHundred = append(perHundred, float64(c.ListCost.Requests)*100/float64(n))
		}
		classes[sizeClass(n)] = append(classes[sizeClass(n)], float64(c.ListCost.Requests))
	}
	p("## Channels crawled (E5)")
	p("")
	p("| | Value |")
	p("|---|---|")
	p("| Channels attempted / succeeded | %d / %d |", len(chans), ok)
	p("| Listing requests per channel (median / p90) | %s / %s |", f0(pct(listReq, 50)), f0(pct(listReq, 90)))
	p("| Listing requests per 100 uploads in the window (median) | %s |", f1(pct(perHundred, 50)))
	p("| Compressed bytes per request (median) | %s KB |", f1(pct(listBytes, 50)/1024))
	for _, k := range []string{"small (<20 uploads/yr)", "medium (20–200)", "large (>200)"} {
		if v := classes[k]; len(v) > 0 {
			p("| Listing requests, %s channels (n=%d, median) | %s |", k, len(v), f0(pct(v, 50)))
		}
	}
	p("")

	p("## Channel fields found (E3)")
	p("")
	p("| Field | Share of channels |")
	p("|---|---|")
	for _, row := range fieldShares(chans, func(c ChannelRecord) map[string]bool { return c.Fields }) {
		p("| %s | %s |", row.name, pctStr(row.share))
	}
	p("")

	// Videos (E3, E5, E6).
	var vReq, cReq, cPer100, ratio []float64
	var pinned, hearted, abbrev, total, capped, noTok int
	buckets := map[string]int{}
	for _, v := range vids {
		if v.Error != "" && v.VideoCost.Requests == 0 {
			continue
		}
		vReq = append(vReq, float64(v.VideoCost.Requests))
		cReq = append(cReq, float64(v.CommentCost.Requests))
		n := v.Comments.TopLevel + v.Comments.Replies
		total += n
		if n > 0 {
			cPer100 = append(cPer100, float64(v.CommentCost.Requests)*100/float64(n))
		}
		if v.Comments.Capped {
			capped++
		} else if v.Comments.Displayed.OK && v.Comments.Displayed.Min > 0 {
			ratio = append(ratio, float64(n)/float64(v.Comments.Displayed.Min))
		}
		if v.Comments.NoCommentsToken {
			noTok++
		}
		pinned += v.Comments.Pinned
		hearted += v.Comments.Hearted
		abbrev += v.Comments.LikesAbbreviated
		for k, c := range v.Comments.PrecisionBuckets {
			buckets[k] += c
		}
	}
	p("## Videos checked in depth (E3, E6)")
	p("")
	p("| | Value |")
	p("|---|---|")
	p("| Videos checked | %d |", len(vids))
	p("| Requests per video for watch page + player (median) | %s |", f1(pct(vReq, 50)))
	p("| Comments collected in total | %d |", total)
	p("| Requests per 100 comments (median) | %s |", f1(pct(cPer100, 50)))
	p("| Completeness: collected ÷ displayed count, uncapped videos (median / p10) | %s / %s |", pctStr(pct(ratio, 50)), pctStr(pct(ratio, 10)))
	p("| Videos that hit the comment cap | %d |", capped)
	p("| Videos without a comment section token | %d |", noTok)
	p("| Pinned / hearted comments seen | %d / %d |", pinned, hearted)
	p("| Comments whose likes were abbreviated | %s |", pctStr(share(abbrev, total)))
	p("")
	p("Comment time precision (how exact the relative time was):")
	p("")
	p("| Precision | Share |")
	p("|---|---|")
	for _, k := range []string{"minute", "hour", "day", "week", "month", "year", "unknown"} {
		if buckets[k] > 0 {
			p("| %s | %s |", k, pctStr(share(buckets[k], total)))
		}
	}
	p("")
	p("| Video field | Share of videos |")
	p("|---|---|")
	for _, row := range fieldShares(vids, func(v VideoRecord) map[string]bool { return v.Fields }) {
		p("| %s | %s |", row.name, pctStr(row.share))
	}
	p("")

	// Re-sweep (E7).
	if len(res) > 0 {
		var days, stopPages []float64
		var before, newC, missing, edited int
		for _, r := range res {
			if r.Error != "" {
				continue
			}
			days = append(days, r.DaysSince)
			stopPages = append(stopPages, float64(r.StopEarlyReqs))
			before += r.Before
			newC += r.New
			missing += r.Missing
			edited += r.Edited
		}
		p("## Edits and deletions after a re-sweep (E7)")
		p("")
		p("| | Value |")
		p("|---|---|")
		p("| Videos re-swept / median days since first sweep | %d / %s |", len(res), f1(pct(days, 50)))
		p("| New comments per 100 earlier comments | %s |", f1(per100(newC, before)))
		p("| Missing (deleted or hidden) per 100 earlier comments | %s |", f1(per100(missing, before)))
		p("| Edited per 100 earlier comments | %s |", f1(per100(edited, before)))
		p("| Pages needed by the stop-early scan (median / p90) | %s / %s |", f1(pct(stopPages, 50)), f1(pct(stopPages, 90)))
		p("")
	}

	// Detection (E8).
	if len(feedsRecs) > 0 {
		outcomes := map[string]int{}
		for _, f := range feedsRecs {
			outcomes[f.Outcome]++
		}
		var delays []float64
		layers := map[string]int{}
		for _, sgt := range sights {
			if sgt.Layer == "tab" && sgt.Published.IsZero() {
				continue // recorded before tab sightings needed a recent publish date
			}
			layers[sgt.Layer]++
			if sgt.Layer == "rss" && sgt.DelaySec > 0 {
				delays = append(delays, sgt.DelaySec/60)
			}
		}
		p("## New-upload detection (E8, RSS and tab checks; WebSub later, D23)")
		p("")
		p("| | Value |")
		p("|---|---|")
		p("| Feed checks | %d |", len(feedsRecs))
		for _, k := range []string{"ok", "not_modified", "not_found", "error"} {
			p("| Feed checks: %s | %d (%s) |", k, outcomes[k], pctStr(share(outcomes[k], len(feedsRecs))))
		}
		p("| New uploads first seen by RSS / by tab check | %d / %d |", layers["rss"], layers["tab"])
		p("| RSS detection delay after publishing, minutes (median / p90) | %s / %s |", f0(pct(delays, 50)), f0(pct(delays, 90)))
		p("")
	}

	// Accuracy (E4).
	if len(acc) > 0 {
		var vd, ld []float64
		inRange, exact, found := 0, 0, 0
		for _, a := range acc {
			if !a.Found {
				continue
			}
			found++
			vd = append(vd, a.ViewsRelDiff)
			ld = append(ld, a.LikesRelDiff)
			if a.LikesInRange || a.LikesScraped == a.LikesOfficial {
				inRange++
			}
			if a.LikesExact {
				exact++
			}
		}
		p("## Accuracy against the official API (E4)")
		p("")
		p("| | Value |")
		p("|---|---|")
		p("| Videos compared | %d |", found)
		p("| Views: relative difference (median / p90) | %s / %s |", pctStr(pct(vd, 50)), pctStr(pct(vd, 90)))
		p("| Likes: relative difference (median / p90) | %s / %s |", pctStr(pct(ld, 50)), pctStr(pct(ld, 90)))
		p("| Likes read as exact numbers | %s |", pctStr(share(exact, found)))
		p("| Likes matching the official value or inside the scraped range | %s |", pctStr(share(inRange, found)))
		p("")
	}

	// Blocks and proxies (E1/E2 come later; these are the crawl's counters).
	// The pace experiment blocks on purpose; its pool is reported in E1,
	// not in the crawl's block counts or the failover figures.
	var work []PoolSnapshot
	for _, ps := range pools {
		if ps.Command != "pace" {
			work = append(work, ps)
		}
	}
	if pools = latestPerRun(work); len(pools) > 0 {
		byKind := map[proxypool.Kind][2]int64{}
		byClass := map[proxypool.Class]int64{}
		pauses, subnetCools := 0, 0
		var paused time.Duration
		for _, ps := range pools {
			for _, px := range ps.Stats.Proxies {
				v := byKind[px.Kind]
				v[0] += px.Requests
				v[1] += px.Blocks
				byKind[px.Kind] = v
				for c, n := range px.BlocksByClass {
					byClass[c] += n
				}
			}
			pauses += ps.Stats.Pauses
			paused += ps.Stats.PausedTotal
			subnetCools += ps.Stats.SubnetCooldowns
		}
		p("## Requests and block signals by proxy type")
		p("")
		p("| Proxy type | Requests | Block signals | Blocks per 1,000 requests |")
		p("|---|---|---|---|")
		kinds := make([]string, 0, len(byKind))
		for k := range byKind {
			kinds = append(kinds, string(k))
		}
		sort.Strings(kinds)
		for _, k := range kinds {
			v := byKind[proxypool.Kind(k)]
			p("| %s | %d | %d | %s |", k, v[0], v[1], f1(per1000(v[1], v[0])))
		}
		p("")
		if len(byClass) > 0 {
			classes := make([]string, 0, len(byClass))
			for c := range byClass {
				classes = append(classes, string(c))
			}
			sort.Strings(classes)
			parts := make([]string, 0, len(classes))
			for _, c := range classes {
				parts = append(parts, fmt.Sprintf("%s %d", c, byClass[proxypool.Class(c)]))
			}
			p("Block signals by request kind: %s.", strings.Join(parts, ", "))
			p("")
		}
		p("Safety pauses of all requests: %d (%s in total). Subnet rests: %d.", pauses, paused.Round(time.Minute), subnetCools)
		p("")
	}

	reportPace(p, paceRecs)
	reportFailover(p, pools)
	reportDiscovery(p, discRecs, discItems, knownChannelHashes(s))
	reportCensus(p, census)
	reportLabels(p, labelSampleRecs, labelAnswers, discItems)
	reportTranscripts(p, transcripts)

	p("## Parser problems (E13)")
	p("")
	if len(skels) == 0 {
		p("No unreadable responses.")
	} else {
		count := map[string]int{}
		for _, n := range skels {
			count[strings.SplitN(n, "-", 2)[0]]++
		}
		p("Skeletons saved (structure only, in the data folder under skeletons/):")
		p("")
		for _, k := range sortedKeys(count) {
			p("- %s: %d", k, count[k])
		}
	}
	return nil
}

func sizeClass(n int) string {
	switch {
	case n < 20:
		return "small (<20 uploads/yr)"
	case n <= 200:
		return "medium (20–200)"
	}
	return "large (>200)"
}

type fieldRow struct {
	name  string
	share float64
}

func fieldShares[T any](recs []T, fields func(T) map[string]bool) []fieldRow {
	count, n := map[string]int{}, 0
	for _, r := range recs {
		f := fields(r)
		if len(f) == 0 {
			continue
		}
		n++
		for k, v := range f {
			if v {
				count[k] += 1
			} else if _, ok := count[k]; !ok {
				count[k] = 0
			}
		}
	}
	var out []fieldRow
	for _, k := range sortedKeys(count) {
		out = append(out, fieldRow{k, share(count[k], n)})
	}
	return out
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// pct returns the q-th percentile (nearest rank), or -1 for no data.
func pct(v []float64, q float64) float64 {
	if len(v) == 0 {
		return -1
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	i := int(q/100*float64(len(s)-1) + 0.5)
	return s[i]
}

func share(a, b int) float64 {
	if b == 0 {
		return -1
	}
	return float64(a) / float64(b)
}

func per100(a, b int) float64 {
	if b == 0 {
		return -1
	}
	return float64(a) * 100 / float64(b)
}

func per1000(a, b int64) float64 {
	if b == 0 {
		return -1
	}
	return float64(a) * 1000 / float64(b)
}

func f0(x float64) string {
	if x < 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.0f", x)
}

func f1(x float64) string {
	if x < 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.1f", x)
}

func pctStr(x float64) string {
	if x < 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", x*100)
}

// reportPace writes E1: for each endpoint, the fastest gap per IP that drew
// no block signal and the gap that drew the first one. When an endpoint was
// tested in several runs, its latest run counts.
func reportPace(p func(string, ...any), recs []PaceRecord) {
	if len(recs) == 0 {
		return
	}
	latest := map[string]string{} // class → latest run
	for _, r := range recs {
		if r.Run > latest[r.Class] {
			latest[r.Class] = r.Run
		}
	}
	var classes []string
	for _, c := range DefaultPaceOptions().Classes {
		if _, ok := latest[string(c)]; ok {
			classes = append(classes, string(c))
		}
	}
	for c := range latest {
		known := false
		for _, k := range classes {
			known = known || k == c
		}
		if !known {
			classes = append(classes, c)
		}
	}
	p("## Safe pace per endpoint (E1)")
	p("")
	p("Gap = time between two requests on one IP. Each endpoint was tested from the slowest gap down and stopped at its first block signal.")
	p("")
	p("| Endpoint | Proxy type | Fastest gap with no block | First block at | Requests / blocks | Median response at fastest clean gap |")
	p("|---|---|---|---|---|---|")
	for _, c := range classes {
		var steps []PaceRecord
		for _, r := range recs {
			if r.Class == c && r.Run == latest[c] {
				steps = append(steps, r)
			}
		}
		sort.Slice(steps, func(i, j int) bool { return steps[i].GapSec > steps[j].GapSec })
		clean, firstBlock, kinds := "none", "none", ""
		var req, blocks int
		var cleanMS int64
		for _, st := range steps {
			req += st.Requests
			blocks += st.Blocks
			kinds = st.Kinds
			if st.Blocks == 0 && st.Requests > 0 {
				clean, cleanMS = gapStr(st.GapSec), st.MedianMS
			} else if st.Blocks > 0 && firstBlock == "none" {
				firstBlock = gapStr(st.GapSec)
			}
		}
		if firstBlock == "none" && len(steps) > 0 {
			firstBlock = "none up to " + gapStr(steps[len(steps)-1].GapSec)
		}
		p("| %s | %s | %s | %s | %d / %d | %d ms |", c, kinds, clean, firstBlock, req, blocks, cleanMS)
	}
	p("")
}

func gapStr(sec float64) string {
	return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.2f", sec), "0"), ".0") + " s"
}

// reportFailover writes E2 from the crawl's and detection's pools: what
// happened to requests that met a block, and whether rested IPs came back.
func reportFailover(p func(string, ...any), pools []PoolSnapshot) {
	byClass := map[proxypool.Class]*proxypool.ClassStats{}
	for _, ps := range pools {
		for c, cs := range ps.Stats.Classes {
			a := byClass[c]
			if a == nil {
				a = &proxypool.ClassStats{}
				byClass[c] = a
			}
			a.BlockedRequests += cs.BlockedRequests
			a.FailoverOK += cs.FailoverOK
			a.Parked += cs.Parked
			a.Recovered += cs.Recovered
			a.Reblocked += cs.Reblocked
			a.RecoverySeconds = append(a.RecoverySeconds, cs.RecoverySeconds...)
		}
	}
	if len(byClass) == 0 {
		return
	}
	classes := make([]string, 0, len(byClass))
	for c := range byClass {
		classes = append(classes, string(c))
	}
	sort.Strings(classes)
	p("## Failover and recovery (E2)")
	p("")
	p("| Endpoint | Requests that met a block | Finished on another IP | Given up (3 IPs blocked) | IPs working after their rest / blocked again | Median minutes from block to working again |")
	p("|---|---|---|---|---|---|")
	for _, c := range classes {
		a := byClass[proxypool.Class(c)]
		mins := make([]float64, len(a.RecoverySeconds))
		for i, s := range a.RecoverySeconds {
			mins[i] = s / 60
		}
		p("| %s | %d | %d (%s) | %d | %d / %d | %s |", c, a.BlockedRequests, a.FailoverOK, pctStr(share(int(a.FailoverOK), int(a.BlockedRequests))),
			a.Parked, a.Recovered, a.Reblocked, f0(pct(mins, 50)))
	}
	p("")
}

// reportDiscovery writes E9: yield and depth per engine, channels not in
// the list, which engine found them, and new channels per run.
func reportDiscovery(p func(string, ...any), recs []DiscoverRecord, items []DiscoverItem, known map[string]bool) {
	if len(recs) == 0 {
		return
	}
	engines := []string{"web", "api"}
	type agg struct {
		terms         map[string]bool
		perPage       []float64
		pages         []float64
		ranOut, total int
		units         int
		errors        int
	}
	a := map[string]*agg{}
	for _, e := range engines {
		a[e] = &agg{terms: map[string]bool{}}
	}
	for _, r := range recs {
		g := a[r.Engine]
		if g == nil {
			continue
		}
		g.terms[r.TermHash] = true
		g.total++
		g.units += r.Units
		if r.Error != "" {
			g.errors++
		}
		if r.Pages > 0 {
			g.perPage = append(g.perPage, float64(r.Results)/float64(r.Pages))
			g.pages = append(g.pages, float64(r.Pages))
		}
		if r.RanOut {
			g.ranOut++
		}
	}
	// Channels per engine, from the found videos.
	chans := map[string]map[string]bool{"web": {}, "api": {}}
	qualifying := map[string]bool{}
	perTerm := map[string]map[string]map[string]bool{} // term → engine → channels
	for _, it := range items {
		if it.ChannelID == "" || chans[it.Engine] == nil {
			continue
		}
		chans[it.Engine][it.ChannelID] = true
		if it.Qualifies {
			qualifying[it.ChannelID] = true
		}
		if perTerm[it.TermHash] == nil {
			perTerm[it.TermHash] = map[string]map[string]bool{"web": {}, "api": {}}
		}
		perTerm[it.TermHash][it.Engine][it.ChannelID] = true
	}
	newOf := func(set map[string]bool) (n, q int) {
		for ch := range set {
			if !known[shortHash(ch)] {
				n++
				if qualifying[ch] {
					q++
				}
			}
		}
		return
	}
	p("## Discovery: website search vs official API (E9)")
	p("")
	p("| | Website search | Official API |")
	p("|---|---|---|")
	p("| Terms searched | %d | %d |", len(a["web"].terms), len(a["api"].terms))
	p("| Results per page (median) | %s | %s |", f1(pct(a["web"].perPage, 50)), f1(pct(a["api"].perPage, 50)))
	p("| Pages read per term (median) | %s | %s |", f1(pct(a["web"].pages, 50)), f1(pct(a["api"].pages, 50)))
	p("| Terms whose results ran out before the page limit | %s | %s |", pctStr(share(a["web"].ranOut, a["web"].total)), pctStr(share(a["api"].ranOut, a["api"].total)))
	wn, wq := newOf(chans["web"])
	an, aq := newOf(chans["api"])
	p("| Channels found (unique) | %d | %d |", len(chans["web"]), len(chans["api"]))
	p("| ... not in the list | %d | %d |", wn, an)
	p("| ... not in the list, with a video whose title matches the keywords | %d | %d |", wq, aq)
	p("| Searches that failed | %d | %d |", a["web"].errors, a["api"].errors)
	p("| Quota units used | n/a | %d |", a["api"].units)
	p("")
	// Which engine found the new channels.
	onlyWeb, onlyAPI, both := 0, 0, 0
	for ch := range chans["web"] {
		if known[shortHash(ch)] {
			continue
		}
		if chans["api"][ch] {
			both++
		} else {
			onlyWeb++
		}
	}
	for ch := range chans["api"] {
		if !known[shortHash(ch)] && !chans["web"][ch] {
			onlyAPI++
		}
	}
	if total := onlyWeb + onlyAPI + both; total > 0 && len(a["api"].terms) > 0 {
		var jac []float64
		for _, e := range perTerm {
			w, ap := e["web"], e["api"]
			if len(w) == 0 || len(ap) == 0 {
				continue
			}
			inter := 0
			for ch := range w {
				if ap[ch] {
					inter++
				}
			}
			jac = append(jac, 100*float64(inter)/float64(len(w)+len(ap)-inter))
		}
		p("New channels found only by the website: %s; only by the API: %s; by both: %s. Median overlap of the two engines' channels for the same term: %s%%.",
			pctStr(share(onlyWeb, total)), pctStr(share(onlyAPI, total)), pctStr(share(both, total)), f0(pct(jac, 50)))
		p("")
	}
	// New qualifying channels first seen in each run.
	runs := map[string]bool{}
	for _, it := range items {
		runs[it.Run] = true
	}
	if len(runs) > 1 {
		order := make([]string, 0, len(runs))
		for r := range runs {
			order = append(order, r)
		}
		sort.Strings(order)
		seen := map[string]bool{}
		var parts []string
		for _, r := range order {
			n := 0
			for _, it := range items {
				if it.Run == r && it.Qualifies && !known[shortHash(it.ChannelID)] && !seen[it.ChannelID] {
					seen[it.ChannelID] = true
					n++
				}
			}
			t, _ := time.Parse(time.RFC3339, r)
			parts = append(parts, fmt.Sprintf("%s: %d", t.In(iraq).Format("2 Jan 15:04"), n))
		}
		p("New keyword-matching channels first seen per run: %s.", strings.Join(parts, "; "))
		p("")
	}
}

// reportCensus writes E10: core vs mention, size mix, upload rate and
// comments per video of the channels discovery found.
func reportCensus(p func(string, ...any), recs []CensusRecord) {
	if len(recs) == 0 {
		return
	}
	var ok []CensusRecord
	errs := 0
	for _, r := range recs {
		if r.Error != "" {
			errs++
			continue
		}
		ok = append(ok, r)
	}
	core, byCountry, byMeta := 0, 0, 0
	found := map[string]int{}
	var reqs []float64
	for _, r := range recs {
		reqs = append(reqs, float64(r.Requests))
	}
	for _, r := range ok {
		if r.Core {
			core++
		}
		if r.CountryIraq {
			byCountry++
		}
		if r.MetaMatch {
			byMeta++
		}
		found[r.FoundBy]++
	}
	p("## Channel census (E10)")
	p("")
	p("| | Value |")
	p("|---|---|")
	p("| Channels profiled (failed) | %d (%d) |", len(ok), errs)
	p("| Iraq channels (core) / mention only | %s / %s |", pctStr(share(core, len(ok))), pctStr(share(len(ok)-core, len(ok))))
	p("| Core signals: About country is Iraq / own title, description or keywords match | %s / %s |", pctStr(share(byCountry, len(ok))), pctStr(share(byMeta, len(ok))))
	p("| Found by: website only / API only / both | %s / %s / %s |", pctStr(share(found["web"], len(ok))), pctStr(share(found["api"], len(ok))), pctStr(share(found["both"], len(ok))))
	p("| Requests per channel profiled (median) | %s |", f1(pct(reqs, 50)))
	p("")
	buckets := []struct {
		name string
		max  int64
	}{{"< 1K", 1e3}, {"1K–10K", 1e4}, {"10K–100K", 1e5}, {"100K–1M", 1e6}, {"≥ 1M", 1 << 62}}
	p("| Subscribers | Core | Mention only |")
	p("|---|---|---|")
	for i, b := range buckets {
		var lo int64
		if i > 0 {
			lo = buckets[i-1].max
		}
		nc, nm, tc, tm := 0, 0, 0, 0
		for _, r := range ok {
			if !r.Subscribers.OK {
				continue
			}
			in := r.Subscribers.Min >= lo && r.Subscribers.Min < b.max
			if r.Core {
				tc++
				if in {
					nc++
				}
			} else {
				tm++
				if in {
					nm++
				}
			}
		}
		p("| %s | %s | %s |", b.name, pctStr(share(nc, tc)), pctStr(share(nm, tm)))
	}
	p("")
	var upC, upM, comC, comM []float64
	for _, r := range ok {
		if r.UploadsKnown {
			if r.Core {
				upC = append(upC, r.UploadsPerMonth)
			} else {
				upM = append(upM, r.UploadsPerMonth)
			}
		}
		if r.NewestComments.OK {
			if r.Core {
				comC = append(comC, float64(r.NewestComments.Min))
			} else {
				comM = append(comM, float64(r.NewestComments.Min))
			}
		}
	}
	p("| | Core | Mention only |")
	p("|---|---|---|")
	p("| Uploads per month, videos tab (median / p90) | %s / %s | %s / %s |", f1(pct(upC, 50)), f1(pct(upC, 90)), f1(pct(upM, 50)), f1(pct(upM, 90)))
	p("| Comments on the newest video (median / p90) | %s / %s | %s / %s |", f0(pct(comC, 50)), f0(pct(comC, 90)), f0(pct(comM, 50)), f0(pct(comM, 90)))
	p("")
}
