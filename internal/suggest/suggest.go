// Package suggest proposes new keywords and measures how precise the
// existing ones are (plan §5.1):
//
//   - co-occurring terms: hashtags, tags and frequent title words that keep
//     appearing in Iraq-relevant items, and rarely elsewhere, become
//     suggestions;
//   - trending terms: a term spiking in the last 24 hours against its usual
//     level becomes a temporary keyword with a 7-day expiry;
//   - suggestions above the quality threshold are activated automatically,
//     at most 20 a day (a setting switches this off); you can reject any of
//     them, and a rejected term is never suggested again;
//   - precision: for every keyword, how many of the items it matched were
//     confirmed by another Iraq signal (a second keyword, or a core
//     channel); keywords with low precision are flagged for review.
package suggest

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/keywords"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/kwstore"
)

// Defaults (plan §5.1).
const (
	MinSupport    = 10   // relevant items a term must appear in
	MinPrecision  = 0.6  // share of its items that are relevant
	AutoThreshold = 0.75 // score above which a suggestion is activated
	MaxAutoPerDay = 20
	TrendRatio    = 3.0 // last 24 h against the previous days' average
	TrendMin      = 5   // items in the last 24 h
	AutoKey       = "suggestions_auto"
)

// Term is one candidate with its evidence.
type Term struct {
	Term       string  `json:"term"`
	Normalized string  `json:"normalized"`
	Relevant   int     `json:"relevant"` // relevant items (7 days)
	All        int     `json:"all"`      // all items (7 days)
	Last24     int     `json:"last_24h"` // relevant items, last 24 hours
	Precision  float64 `json:"precision"`
	Trending   bool    `json:"trending"`
	Score      float64 `json:"score"`
}

// item is one video or post's text, for mining.
type item struct {
	relevant bool
	recent   bool // within 24 hours
	tokens   map[string]string
}

var stop = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`the and for with from this that your you are was were have has will new video live news full
		official channel episode part watch youtube shorts short العاجل عاجل مباشر الحلقة حلقة فيديو قناة اخبار أخبار
		في من على الى إلى عن مع هذا هذه التي الذي اليوم بث`) {
		stop[keywords.Normalize(w)] = true
	}
}

// tokens returns a text's candidate terms: hashtags, and words of at least
// 4 letters; the key is the normalized form, the value the first spelling.
func tokens(text string, tags []string) map[string]string {
	out := map[string]string{}
	add := func(raw string, hashtag bool) {
		raw = strings.TrimFunc(raw, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '#' })
		n := keywords.Normalize(strings.TrimPrefix(raw, "#"))
		if n == "" || stop[n] || len([]rune(n)) < 4 && !hashtag {
			return
		}
		if hashtag {
			n = "#" + n
		}
		if _, ok := out[n]; !ok {
			out[n] = raw
		}
	}
	for _, w := range strings.Fields(text) {
		add(w, strings.HasPrefix(w, "#"))
	}
	for _, t := range tags {
		add(t, false)
	}
	return out
}

// Mine scores candidate terms from the last 7 days of videos and posts.
func Mine(ctx context.Context, pool *pgxpool.Pool, now time.Time) ([]Term, error) {
	rows, err := pool.Query(ctx, `SELECT relevant, first_seen >= $2::timestamptz, COALESCE(title, ''), COALESCE(tags, '{}')
		FROM videos WHERE first_seen >= $1
		UNION ALL
		SELECT relevant, first_seen >= $2::timestamptz, COALESCE(text, ''), '{}' FROM posts WHERE first_seen >= $1`,
		now.Add(-7*24*time.Hour), now.Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	var items []item
	for rows.Next() {
		var it item
		var text string
		var tags []string
		if err := rows.Scan(&it.relevant, &it.recent, &text, &tags); err != nil {
			rows.Close()
			return nil, err
		}
		it.tokens = tokens(text, tags)
		items = append(items, it)
	}
	rows.Close()
	known, err := knownTerms(ctx, pool)
	if err != nil {
		return nil, err
	}
	type acc struct {
		spelling              string
		rel, all, last24, old int
	}
	by := map[string]*acc{}
	for _, it := range items {
		for n, raw := range it.tokens {
			a := by[n]
			if a == nil {
				a = &acc{spelling: raw}
				by[n] = a
			}
			a.all++
			if it.relevant {
				a.rel++
				if it.recent {
					a.last24++
				} else {
					a.old++
				}
			}
		}
	}
	var out []Term
	for n, a := range by {
		if known[strings.TrimPrefix(n, "#")] || a.rel < MinSupport && a.last24 < TrendMin {
			continue
		}
		t := Term{Term: a.spelling, Normalized: n, Relevant: a.rel, All: a.all, Last24: a.last24}
		t.Precision = float64(a.rel) / float64(a.all)
		if t.Precision < MinPrecision {
			continue
		}
		avg := float64(a.old) / 6
		t.Trending = a.last24 >= TrendMin && float64(a.last24) >= TrendRatio*math.Max(avg, 1)
		// Score: precision, raised with support (saturating at 50 items),
		// and a lift for trending terms.
		t.Score = t.Precision * math.Min(1, 0.5+float64(a.rel)/100)
		if t.Trending {
			t.Score = math.Min(1, t.Score+0.15)
		}
		t.Score = math.Round(t.Score*1000) / 1000
		if a.rel < MinSupport && !t.Trending {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Normalized < out[j].Normalized
	})
	return out, nil
}

func knownTerms(ctx context.Context, pool *pgxpool.Pool) (map[string]bool, error) {
	rows, err := pool.Query(ctx, `SELECT normalized FROM keyword_variants UNION SELECT normalized FROM keyword_suggestions WHERE status = 'rejected'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[strings.TrimPrefix(n, "#")] = true
	}
	return out, rows.Err()
}

// Run mines terms, stores them as suggestions, and activates the best
// ones (at most MaxAutoPerDay a day, unless switched off). It returns the
// suggestions stored and the keywords activated.
func Run(ctx context.Context, pool *pgxpool.Pool, now time.Time) (suggested, activated int, err error) {
	terms, err := Mine(ctx, pool, now)
	if err != nil {
		return 0, 0, err
	}
	auto := true
	if ok, err := db.GetSetting(ctx, pool, AutoKey, &auto); err != nil {
		return 0, 0, err
	} else if !ok {
		auto = true
	}
	var today int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM keywords WHERE source = 'suggestion' AND created_at >= $1`,
		now.Add(-24*time.Hour)).Scan(&today); err != nil {
		return 0, 0, err
	}
	for _, t := range terms {
		evidence, _ := json.Marshal(t)
		var status string
		err := pool.QueryRow(ctx, `INSERT INTO keyword_suggestions (term, normalized, score, evidence) VALUES ($1, $2, $3, $4)
			ON CONFLICT (normalized) DO UPDATE SET score = EXCLUDED.score, evidence = EXCLUDED.evidence
			RETURNING status`, t.Term, t.Normalized, t.Score, evidence).Scan(&status)
		if err != nil {
			return suggested, activated, err
		}
		suggested++
		if !auto || status != "new" || t.Score < AutoThreshold || today+activated >= MaxAutoPerDay {
			continue
		}
		kind := "term"
		label := t.Term
		if strings.HasPrefix(t.Normalized, "#") {
			kind = "hashtag"
			label = strings.TrimPrefix(t.Term, "#")
		}
		n := kwstore.New{Label: label, Kind: kind, Source: "suggestion", Note: "suggested automatically"}
		if t.Trending {
			exp := now.Add(7 * 24 * time.Hour)
			n.ExpiresAt = &exp
		}
		if _, err := kwstore.Add(ctx, pool, n); err != nil {
			return suggested, activated, err
		}
		if _, err := pool.Exec(ctx, `UPDATE keyword_suggestions SET status = 'accepted', decided_at = now() WHERE normalized = $1`,
			t.Normalized); err != nil {
			return suggested, activated, err
		}
		activated++
	}
	return suggested, activated, nil
}

// Reject marks a suggestion rejected (it is never suggested again) and
// switches off the keyword it became, if any.
func Reject(ctx context.Context, pool *pgxpool.Pool, id int64) error {
	var term string
	if err := pool.QueryRow(ctx, `UPDATE keyword_suggestions SET status = 'rejected', decided_at = now() WHERE id = $1 RETURNING term`,
		id).Scan(&term); err != nil {
		return err
	}
	_, err := pool.Exec(ctx, `UPDATE keywords SET active = false, updated_at = now()
		WHERE source = 'suggestion' AND (label = $1 OR '#' || label = $1)`, term)
	return err
}

// Accept turns a suggestion into a keyword by hand.
func Accept(ctx context.Context, pool *pgxpool.Pool, id int64) error {
	var term, norm string
	if err := pool.QueryRow(ctx, `SELECT term, normalized FROM keyword_suggestions WHERE id = $1 AND status = 'new'`, id).Scan(&term, &norm); err != nil {
		return err
	}
	kind, label := "term", term
	if strings.HasPrefix(norm, "#") {
		kind, label = "hashtag", strings.TrimPrefix(term, "#")
	}
	if _, err := kwstore.Add(ctx, pool, kwstore.New{Label: label, Kind: kind, Source: "suggestion", Note: "accepted by hand"}); err != nil {
		return err
	}
	_, err := pool.Exec(ctx, `UPDATE keyword_suggestions SET status = 'accepted', decided_at = now() WHERE id = $1`, id)
	return err
}

// Precision is one keyword's measured precision.
type Precision struct {
	KeywordID int64
	Label     string
	Matches   int
	Confirmed int
	Share     float64
	Low       bool // flagged for review
}

// MeasurePrecision counts, for every keyword, the last 30 days of matched
// videos and posts and how many another Iraq signal confirmed; it stores
// today's numbers in keyword_stats. Keywords with at least 20 matches and
// under 30% confirmed are flagged.
func MeasurePrecision(ctx context.Context, pool *pgxpool.Pool, now time.Time) ([]Precision, error) {
	kws, err := kwstore.List(ctx, pool)
	if err != nil {
		return nil, err
	}
	var out []Precision
	for _, k := range kws {
		id := kwstore.MatchID(k)
		var matches, confirmed int
		err := pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE confirmed) FROM (
				SELECT cardinality(v.matched_keywords) > 1 OR c.kind = 'core' AS confirmed
				FROM videos v JOIN channels c ON c.id = v.channel_id
				WHERE $1 = ANY(v.matched_keywords) AND v.first_seen >= $2
				UNION ALL
				SELECT cardinality(p.matched_keywords) > 1 OR c.kind = 'core'
				FROM posts p JOIN channels c ON c.id = p.channel_id
				WHERE $1 = ANY(p.matched_keywords) AND p.first_seen >= $2) x`, id, now.Add(-30*24*time.Hour)).Scan(&matches, &confirmed)
		if err != nil {
			return nil, err
		}
		if matches == 0 {
			continue
		}
		p := Precision{KeywordID: k.ID, Label: k.Label, Matches: matches, Confirmed: confirmed,
			Share: math.Round(float64(confirmed)/float64(matches)*1000) / 1000}
		p.Low = matches >= 20 && p.Share < 0.3
		out = append(out, p)
		if _, err := pool.Exec(ctx, `INSERT INTO keyword_stats (keyword_id, day, matches, confirmed) VALUES ($1, $2, $3, $4)
			ON CONFLICT (keyword_id, day) DO UPDATE SET matches = EXCLUDED.matches, confirmed = EXCLUDED.confirmed`,
			k.ID, now.UTC().Format("2006-01-02"), matches, confirmed); err != nil {
			return nil, err
		}
	}
	return out, nil
}
