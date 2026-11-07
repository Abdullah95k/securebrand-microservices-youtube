// Package kwstore is the keyword service (plan §5.1, §8.2): keywords and
// their spellings live in the database, editable at any time; workers hold
// a matcher that is rebuilt when the keywords change, so an admin change is
// applied within a minute without a restart.
//
// The built-in seed list (public place names) is imported from the repo.
// Your own brand and person keywords are entered with the admin command and
// exist only in the database (§8.10): they reveal who you monitor.
package kwstore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	contracts "github.com/abdullah95k/securebrand-contracts"
	"github.com/jackc/pgx/v5"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/keywords"
)

// Kinds of keywords.
var Kinds = []string{"place", "person", "org", "topic", "brand", "hashtag", "term"}

func validKind(k string) bool {
	for _, x := range Kinds {
		if k == x {
			return true
		}
	}
	return false
}

// Languages the seed list covers.
var seedLanguages = map[string]string{"ar": "Arabic", "ckb": "Kurdish (Sorani)", "kmr": "Kurdish (Kurmanji)", "en": "English", "tr": "Turkish / Turkmen"}

// ImportSeed adds the built-in seed keywords, or updates them to the repo's
// current version (their variants are replaced). Keywords you added are not
// touched. It returns how many seed keywords there are.
func ImportSeed(ctx context.Context, q db.Querier) (int, error) {
	seed, err := keywords.Seed()
	if err != nil {
		return 0, err
	}
	for code, name := range seedLanguages {
		if _, err := q.Exec(ctx, `INSERT INTO languages (code, name) VALUES ($1, $2) ON CONFLICT (code) DO NOTHING`, code, name); err != nil {
			return 0, err
		}
	}
	for _, k := range seed {
		var id int64
		// Iraq and Iraqi themselves are the top-priority group: the API
		// sweeps them every 3 hours.
		prio := 0
		if k.Group == "iraq" {
			prio = 1
		}
		err := q.QueryRow(ctx, `INSERT INTO keywords (label, kind, ambiguous, source, seed_id, grp, priority)
			VALUES ($1, $2, $3, 'seed', $1, $4, $5)
			ON CONFLICT (seed_id) DO UPDATE SET kind = EXCLUDED.kind, ambiguous = EXCLUDED.ambiguous, grp = EXCLUDED.grp,
				updated_at = now()
			RETURNING id`, k.ID, k.Kind, k.Ambiguous, k.Group, prio).Scan(&id)
		if err != nil {
			return 0, fmt.Errorf("kwstore: seed %s: %w", k.ID, err)
		}
		if err := setVariants(ctx, q, id, k.Variants, k.Lang); err != nil {
			return 0, err
		}
	}
	return len(seed), nil
}

func setVariants(ctx context.Context, q db.Querier, id int64, variants []string, lang string) error {
	if _, err := q.Exec(ctx, `DELETE FROM keyword_variants WHERE keyword_id = $1`, id); err != nil {
		return err
	}
	n := 0
	for _, v := range variants {
		v = strings.TrimSpace(v)
		norm := keywords.Normalize(v)
		if norm == "" {
			continue
		}
		if _, err := q.Exec(ctx, `INSERT INTO keyword_variants (keyword_id, variant, normalized, lang)
			VALUES ($1, $2, $3, NULLIF($4, '')) ON CONFLICT DO NOTHING`, id, v, norm, lang); err != nil {
			return err
		}
		n++
	}
	if n == 0 {
		return errors.New("kwstore: a keyword needs at least one spelling with letters or digits")
	}
	_, err := q.Exec(ctx, `UPDATE keywords SET updated_at = now() WHERE id = $1`, id)
	return err
}

// Keyword is one row with its spellings.
type Keyword struct {
	ID        int64
	Label     string
	Kind      string
	Ambiguous bool
	Active    bool
	Searched  bool
	Source    string
	Group     string
	Note      string
	Variants  []string
	// VariantLangs are the languages each variant covers ("ar,ckb,en" for a
	// seed keyword), in Variants' order; "" when not stated.
	VariantLangs []string
	ExpiresAt    *time.Time
}

// New is a keyword you add.
type New struct {
	Label     string
	Kind      string
	Variants  []string // spellings; the label is used when empty
	Lang      string
	Ambiguous bool
	NoSearch  bool // match only, never searched
	Note      string
	Source    string     // "admin" (default) or "suggestion"
	ExpiresAt *time.Time // trending suggestions expire after 7 days
}

// Add stores a new keyword and returns its ID.
func Add(ctx context.Context, q db.Querier, k New) (int64, error) {
	k.Label = strings.TrimSpace(k.Label)
	if k.Label == "" {
		return 0, errors.New("kwstore: a keyword needs a label")
	}
	if !validKind(k.Kind) {
		return 0, fmt.Errorf("kwstore: kind %q is not one of %s", k.Kind, strings.Join(Kinds, ", "))
	}
	if len(k.Variants) == 0 {
		k.Variants = []string{k.Label}
	}
	var id int64
	err := pgx.BeginFunc(ctx, txStarter{q}, func(tx pgx.Tx) error {
		src := k.Source
		if src == "" {
			src = "admin"
		}
		if err := tx.QueryRow(ctx, `INSERT INTO keywords (label, kind, ambiguous, searched, source, note, expires_at)
			VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7) RETURNING id`,
			k.Label, k.Kind, k.Ambiguous, !k.NoSearch, src, k.Note, k.ExpiresAt).Scan(&id); err != nil {
			return err
		}
		return setVariants(ctx, tx, id, k.Variants, k.Lang)
	})
	return id, err
}

// txStarter lets Add run in a transaction whether q is a pool or already one.
type txStarter struct{ q db.Querier }

func (t txStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	if b, ok := t.q.(interface {
		Begin(context.Context) (pgx.Tx, error)
	}); ok {
		return b.Begin(ctx)
	}
	return nil, errors.New("kwstore: needs a pool or a transaction")
}

// Change is an edit; nil fields stay as they are.
type Change struct {
	Active    *bool
	Ambiguous *bool
	Searched  *bool
	Variants  []string // replaces all spellings when set
	Lang      string
}

// Update edits a keyword.
func Update(ctx context.Context, q db.Querier, id int64, c Change) error {
	tag, err := q.Exec(ctx, `UPDATE keywords SET active = COALESCE($2, active), ambiguous = COALESCE($3, ambiguous),
		searched = COALESCE($4, searched), updated_at = now() WHERE id = $1`, id, c.Active, c.Ambiguous, c.Searched)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("kwstore: no keyword %d", id)
	}
	if c.Variants != nil {
		return setVariants(ctx, q, id, c.Variants, c.Lang)
	}
	return nil
}

// Remove deletes a keyword you added (seed keywords are switched off with
// Update instead, so a re-import does not bring them back by surprise).
func Remove(ctx context.Context, q db.Querier, id int64) error {
	tag, err := q.Exec(ctx, `DELETE FROM keywords WHERE id = $1 AND source <> 'seed'`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("kwstore: no keyword %d you added (seed keywords are switched off instead)", id)
	}
	return nil
}

// List returns every keyword with its spellings.
func List(ctx context.Context, q db.Querier) ([]Keyword, error) {
	rows, err := q.Query(ctx, `SELECT k.id, k.label, k.kind, k.ambiguous, k.active, k.searched, k.source,
			COALESCE(k.grp, ''), COALESCE(k.note, ''),
			COALESCE(array_agg(v.variant ORDER BY v.variant) FILTER (WHERE v.variant IS NOT NULL), '{}'),
			COALESCE(array_agg(COALESCE(v.lang, '') ORDER BY v.variant) FILTER (WHERE v.variant IS NOT NULL), '{}'), k.expires_at
		FROM keywords k LEFT JOIN keyword_variants v ON v.keyword_id = k.id
		GROUP BY k.id ORDER BY k.source DESC, k.kind, k.label`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Keyword, error) {
		var k Keyword
		err := r.Scan(&k.ID, &k.Label, &k.Kind, &k.Ambiguous, &k.Active, &k.Searched, &k.Source, &k.Group, &k.Note, &k.Variants,
			&k.VariantLangs, &k.ExpiresAt)
		return k, err
	})
}

// MatchID is the keyword ID the matcher reports: the seed ID for seed
// keywords (stable across databases), "kw-<id>" for yours.
func MatchID(k Keyword) string {
	if k.Source == "seed" {
		return k.Label
	}
	return "kw-" + strconv.FormatInt(k.ID, 10)
}

// Matcher is a keyword matcher with the seed list's rule for ambiguous
// keywords: a match on an ambiguous keyword needs a second, different
// keyword before it counts. Text written in Kurdish counts on its own (D28).
type Matcher struct {
	m         *keywords.Matcher
	ambiguous map[string]bool
	labels    map[string]string
	contract  map[string]contractInfo // by match ID
}

// contractInfo is what the platform contract's keyword ID needs.
type contractInfo struct {
	group string
	langs map[string]string // variant → the languages it covers
}

// NewMatcher builds a matcher from active keywords.
func NewMatcher(kws []Keyword) *Matcher {
	var in []keywords.Keyword
	mt := &Matcher{ambiguous: map[string]bool{}, labels: map[string]string{}, contract: map[string]contractInfo{}}
	for _, k := range kws {
		if !k.Active || k.ExpiresAt != nil && k.ExpiresAt.Before(time.Now()) {
			continue
		}
		id := MatchID(k)
		in = append(in, keywords.Keyword{ID: id, Kind: k.Kind, Variants: k.Variants, Ambiguous: k.Ambiguous})
		mt.ambiguous[id] = k.Ambiguous
		mt.labels[id] = k.Label
		ci := contractInfo{group: contractGroup(k.Group, k.Kind), langs: map[string]string{}}
		for i, v := range k.Variants {
			if i < len(k.VariantLangs) {
				ci.langs[v] = k.VariantLangs[i]
			}
		}
		mt.contract[id] = ci
	}
	mt.m = keywords.NewMatcher(in)
	return mt
}

// Match reports whether text is relevant and which keywords it matched
// (sorted, each once).
func (mt *Matcher) Match(text string) (relevant bool, ids []string) {
	if mt == nil {
		return false, nil
	}
	seen := map[string]bool{}
	for _, h := range mt.m.Match(text) {
		if !seen[h.KeywordID] {
			seen[h.KeywordID] = true
			ids = append(ids, h.KeywordID)
		}
	}
	// Kurdish-language text (Sorani, Badini) is relevant on its own (D28).
	if keywords.Kurdish(text) {
		ids = append(ids, keywords.KurdishID)
		sort.Strings(ids)
		return true, ids
	}
	sort.Strings(ids)
	for _, id := range ids {
		if !mt.ambiguous[id] {
			return true, ids
		}
	}
	return len(ids) >= 2, ids
}

// ContractIDs returns the platform contract's keyword IDs (REQ-021,
// contracts.KeywordID) for the keywords in matched that text contains: the
// keyword's group, the language of the spelling that matched, and that
// spelling as the keyword list stores it, never the text. Rules such as
// the Kurdish-language one are not keywords and are left out.
func (mt *Matcher) ContractIDs(text string, matched []string) []string {
	if mt == nil || len(matched) == 0 {
		return nil
	}
	want := map[string]bool{}
	for _, id := range matched {
		want[id] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, h := range mt.m.Match(text) {
		ci, ok := mt.contract[h.KeywordID]
		if !ok || !want[h.KeywordID] {
			continue
		}
		id := contracts.KeywordID(ci.group, VariantLanguage(h.Variant, ci.langs[h.Variant]), h.Variant)
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// contractGroup is a keyword's group as the contract's ID wants it: lower
// case ASCII letters, digits, "-" and "_". A keyword without a group (one
// you added) uses its kind.
func contractGroup(group, kind string) string {
	g := strings.ToLower(strings.TrimSpace(group))
	if g == "" {
		g = kind
	}
	var b strings.Builder
	for _, r := range g {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "keyword"
	}
	return b.String()
}

// kurdishLetters are Sorani letters Arabic doesn't use; kurmanjiLetters
// are Latin letters Kurmanji uses and English doesn't.
const (
	kurdishLetters  = "ڕڵۆێەڤ"
	kurmanjiLetters = "êîûçşÊÎÛÇŞ"
)

// VariantLanguage picks a spelling's language from the languages its
// keyword covers ("ar,ckb,en"), by its script and letters: Arabic script
// with a Sorani letter is Sorani, otherwise Arabic; Latin script with a
// Kurmanji letter (ê î û ç ş) is Kurmanji, otherwise English. Each only
// when listed, else the first listed language of that script. With no list
// it goes by the script alone; "und" when it can't tell. A best effort:
// Kurmanji spelled without those letters ("Bexda") counts as English.
func VariantLanguage(variant, langs string) string {
	var list []string
	for _, l := range strings.Split(langs, ",") {
		if l = strings.ToLower(strings.TrimSpace(l)); l != "" {
			list = append(list, l)
		}
	}
	has := func(l string) bool { return slices.Contains(list, l) }
	arabicScript, latin := false, false
	for _, r := range variant {
		switch {
		case unicode.Is(unicode.Arabic, r):
			arabicScript = true
		case unicode.Is(unicode.Latin, r):
			latin = true
		}
	}
	switch {
	case arabicScript:
		if strings.ContainsAny(variant, kurdishLetters) && (has("ckb") || len(list) == 0) {
			return "ckb"
		}
		if has("ar") || len(list) == 0 {
			return "ar"
		}
		for _, l := range list {
			if l == "ckb" || l == "fa" {
				return l
			}
		}
	case latin:
		if strings.ContainsAny(variant, kurmanjiLetters) && (has("kmr") || len(list) == 0) {
			return "kmr"
		}
		if has("en") || len(list) == 0 {
			return "en"
		}
		for _, l := range list {
			if l == "kmr" || l == "tr" {
				return l
			}
		}
	}
	if len(list) > 0 {
		return list[0]
	}
	return "und"
}

// Live keeps a Matcher in step with the database.
type Live struct {
	mu      sync.RWMutex
	m       *Matcher
	version string
}

// Matcher returns the current matcher (never nil after a successful Refresh).
func (l *Live) Matcher() *Matcher {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.m
}

// Refresh rebuilds the matcher if the keywords changed since the last call.
// It reports whether it rebuilt.
func (l *Live) Refresh(ctx context.Context, q db.Querier) (bool, error) {
	var count int64
	var last *time.Time
	if err := q.QueryRow(ctx, `SELECT count(*), max(updated_at) FROM keywords`).Scan(&count, &last); err != nil {
		return false, err
	}
	version := strconv.FormatInt(count, 10)
	if last != nil {
		version += "@" + last.UTC().Format(time.RFC3339Nano)
	}
	l.mu.RLock()
	same := l.m != nil && l.version == version
	l.mu.RUnlock()
	if same {
		return false, nil
	}
	kws, err := List(ctx, q)
	if err != nil {
		return false, err
	}
	m := NewMatcher(kws)
	l.mu.Lock()
	l.m, l.version = m, version
	l.mu.Unlock()
	return true, nil
}

// Run refreshes every interval until ctx ends.
func (l *Live) Run(ctx context.Context, q db.Querier, every time.Duration, onErr func(error)) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := l.Refresh(ctx, q); err != nil && onErr != nil && ctx.Err() == nil {
				onErr(err)
			}
		}
	}
}

// SearchTerm is one term the search runner searches.
type SearchTerm struct {
	KeywordID  int64
	Term       string // as searched
	Normalized string
	Priority   int
}

// SearchTerms lists the spellings of active, searched keywords, each
// normalized form once; the search runner rotates through them. Ambiguous
// keywords are left out: alone they would mostly find unrelated videos.
func SearchTerms(ctx context.Context, q db.Querier) ([]SearchTerm, error) {
	rows, err := q.Query(ctx, `SELECT DISTINCT ON (v.normalized) k.id, v.variant, v.normalized, k.priority
		FROM keywords k JOIN keyword_variants v ON v.keyword_id = k.id
		WHERE k.active AND k.searched AND NOT k.ambiguous AND (k.expires_at IS NULL OR k.expires_at > now())
		ORDER BY v.normalized, k.priority DESC, k.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (SearchTerm, error) {
		var t SearchTerm
		err := r.Scan(&t.KeywordID, &t.Term, &t.Normalized, &t.Priority)
		return t, err
	})
}
