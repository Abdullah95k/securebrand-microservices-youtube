// Package keywords finds Iraq-relevant keywords in public YouTube text:
// video titles, descriptions, tags, hashtags and community posts (plan §5.1).
//
// Text and keyword variants are both passed through Normalize, which folds
// Arabic, Kurdish (Sorani and Kurmanji), Turkish and other Latin spellings to
// one matching form. A Matcher then looks for whole-word matches, including
// multi-word variants such as "إقليم كوردستان" or "Dhi Qar", and Arabic words
// that carry a one-letter or article prefix such as "والعراق" ("and Iraq").
//
// The built-in seed list (Seed) holds public place and national names only.
// Brand, person and topic keywords live in the database, never in this repo,
// because they reveal who is being monitored (plan §8.10).
package keywords

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Keyword is one thing to look for, with all of its spellings.
type Keyword struct {
	// ID is unique across all keywords.
	ID string
	// Group collects related keywords. In the seed list it is the
	// governorate a place belongs to.
	Group string
	// Kind says what the keyword names (plan §5.1: place, person, brand,
	// topic, hashtag).
	Kind string
	// Lang lists the languages the variants cover, comma-separated
	// (for example "ar,ckb,kmr,en,tr").
	Lang string
	// Variants are the spellings to match. They are normalized before use,
	// so they may be written in their natural form.
	Variants []string
	// Ambiguous marks keywords whose spellings are also common words,
	// people's names, brands or other places. A match on such a keyword
	// needs another Iraq signal before it is trusted.
	Ambiguous bool
}

// Hit is one keyword found in a text.
//
// Start and End are token positions (End is exclusive) in the normalized
// text, that is, indexes into strings.Fields(Normalize(text)).
type Hit struct {
	KeywordID string
	// Variant is the spelling that matched, as written in the keyword.
	Variant string
	Start   int
	End     int
}

// Matcher finds keywords in text. Build it with NewMatcher. It is never
// changed after construction, so Match is safe to call from many goroutines
// at once.
type Matcher struct {
	// byFirst maps the first normalized token of every variant to the
	// variants that start with it. A text token is looked up directly (plus
	// its prefix-stripped forms), so matching costs a few map lookups per
	// token, however many keywords there are.
	byFirst map[string][]*entry
}

// entry is one normalized variant of one keyword.
type entry struct {
	id      string
	variant string   // original spelling, reported in Hit.Variant
	tokens  []string // normalized tokens
	runes   int      // length of the normalized variant, for tie-breaks
	seq     int      // insertion order, the final tie-break
}

// NewMatcher builds a Matcher for the given keywords. Variants that
// normalize to nothing are skipped, and variants of the same keyword that
// normalize to the same text are kept once (the first one listed). Keywords
// that share an ID are treated as one keyword.
func NewMatcher(kws []Keyword) *Matcher {
	m := &Matcher{byFirst: make(map[string][]*entry)}
	seq := 0
	for _, kw := range kws {
		seen := make(map[string]bool, len(kw.Variants))
		for _, v := range kw.Variants {
			norm := Normalize(v)
			if norm == "" || seen[norm] {
				continue
			}
			seen[norm] = true
			toks := strings.Fields(norm)
			e := &entry{id: kw.ID, variant: v, tokens: toks, runes: utf8.RuneCountInString(norm), seq: seq}
			seq++
			m.byFirst[toks[0]] = append(m.byFirst[toks[0]], e)
		}
	}
	return m
}

// Match returns every keyword found in text, as whole words.
//
// Rules:
//   - A variant matches a run of whole tokens; it never matches part of a
//     longer word ("Iraq" does not match "Iraqiya").
//   - The first token of a variant also matches an Arabic-script token that
//     carries one proclitic prefix (see arabicProclitics): "والعراق",
//     "بالعراق" and "للعراق" all match both "العراق" and "عراق".
//   - A keyword is reported at most once per start position, using its
//     longest matching variant. A later match of the same keyword that lies
//     wholly inside one already reported is dropped.
//
// Hits are ordered by Start, then longest first (End descending), then by
// KeywordID, so the output is deterministic.
func (m *Matcher) Match(text string) []Hit {
	if m == nil || len(m.byFirst) == 0 {
		return nil
	}
	toks := strings.Fields(Normalize(text))
	var (
		hits    []Hit
		lastEnd map[string]int // keyword ID -> End of its last reported hit
		forms   []string       // reused buffer for token forms
		cands   []*entry       // best entry per keyword at the current position
	)
	for i, tok := range toks {
		cands = cands[:0]
		forms = appendTokenForms(forms[:0], tok)
		for _, form := range forms {
			for _, e := range m.byFirst[form] {
				if !restMatches(toks, i, e.tokens) {
					continue
				}
				cands = keepBest(cands, e)
			}
		}
		if len(cands) == 0 {
			continue
		}
		sort.Slice(cands, func(a, b int) bool {
			if len(cands[a].tokens) != len(cands[b].tokens) {
				return len(cands[a].tokens) > len(cands[b].tokens)
			}
			return cands[a].id < cands[b].id
		})
		for _, e := range cands {
			end := i + len(e.tokens)
			if lastEnd == nil {
				lastEnd = make(map[string]int)
			}
			if prev, ok := lastEnd[e.id]; ok && end <= prev {
				continue // inside a longer hit of the same keyword
			}
			lastEnd[e.id] = end
			hits = append(hits, Hit{KeywordID: e.id, Variant: e.variant, Start: i, End: end})
		}
	}
	return hits
}

// restMatches reports whether the tokens after the first one of a variant
// follow position i exactly. The first token was already checked.
func restMatches(toks []string, i int, variant []string) bool {
	if i+len(variant) > len(toks) {
		return false
	}
	for j := 1; j < len(variant); j++ {
		if toks[i+j] != variant[j] {
			return false
		}
	}
	return true
}

// keepBest adds e to cands, or replaces the entry for the same keyword if e
// is a better match. Better means more tokens, then a longer normalized
// variant, then listed earlier.
func keepBest(cands []*entry, e *entry) []*entry {
	for k, c := range cands {
		if c.id != e.id {
			continue
		}
		if betterEntry(e, c) {
			cands[k] = e
		}
		return cands
	}
	return append(cands, e)
}

func betterEntry(a, b *entry) bool {
	if len(a.tokens) != len(b.tokens) {
		return len(a.tokens) > len(b.tokens)
	}
	if a.runes != b.runes {
		return a.runes > b.runes
	}
	return a.seq < b.seq
}

// arabicProclitics are prefixes that Arabic attaches to the next word:
// "and", "so", "with/in", "like", "for/to", optionally followed by the
// article "ال". They are written in normalized form. Only one is stripped.
var arabicProclitics = []string{"وال", "بال", "كال", "فال", "لل", "ال", "و", "ف", "ب", "ك", "ل"}

// minStemRunes is the shortest remainder kept after stripping a prefix. It
// stops tokens like "ول" from turning into a lone letter.
const minStemRunes = 2

// appendTokenForms appends the forms of a text token that may equal the
// first token of a variant: the token itself and, for Arabic-script tokens,
// the token with one proclitic removed. Because "لل" is "ل" plus the article
// with its alef dropped ("للعراق" = "ل" + "العراق"), stripping it yields both
// the bare stem and the stem with "ال" restored. Latin tokens are never
// stripped.
func appendTokenForms(forms []string, tok string) []string {
	forms = append(forms, tok)
	first, _ := utf8.DecodeRuneInString(tok)
	if !unicode.Is(unicode.Arabic, first) {
		return forms
	}
	for _, p := range arabicProclitics {
		rest, ok := strings.CutPrefix(tok, p)
		if !ok || utf8.RuneCountInString(rest) < minStemRunes {
			continue
		}
		forms = append(forms, rest)
		if p == "لل" {
			forms = append(forms, "ال"+rest)
		}
	}
	return forms
}
