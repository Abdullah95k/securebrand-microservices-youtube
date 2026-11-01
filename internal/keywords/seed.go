package keywords

import (
	_ "embed"
	"fmt"
	"strings"
)

// seedIraqTSV is the built-in seed list. The file's header explains its
// columns and conventions.
//
//go:embed seed_iraq.tsv
var seedIraqTSV string

// Seed returns the built-in seed keywords: Iraq and Iraqi, the Kurdistan
// Region, the governorates and their main cities, in Arabic, Kurdish
// (Sorani and Kurmanji), English and Turkish/Turkmen spellings. Each call
// returns a fresh slice that the caller may change.
func Seed() ([]Keyword, error) {
	return parseTSV("seed_iraq.tsv", seedIraqTSV)
}

// seedColumns is the number of tab-separated columns in a seed row:
// id, group, kind, lang, ambiguous (0/1), variants (separated by "|").
const seedColumns = 6

// parseTSV reads keywords in the seed format. Blank lines and lines starting
// with "#" are skipped. name labels error messages.
func parseTSV(name, data string) ([]Keyword, error) {
	var out []Keyword
	seenID := make(map[string]int) // ID -> line it was first defined on
	for n, line := range strings.Split(data, "\n") {
		lineNo := n + 1
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) != seedColumns {
			return nil, fmt.Errorf("%s:%d: want %d tab-separated columns, got %d", name, lineNo, seedColumns, len(cols))
		}
		for i := range cols {
			cols[i] = strings.TrimSpace(cols[i])
		}
		kw := Keyword{ID: cols[0], Group: cols[1], Kind: cols[2], Lang: cols[3]}
		if kw.ID == "" || kw.Group == "" || kw.Kind == "" || kw.Lang == "" {
			return nil, fmt.Errorf("%s:%d: id, group, kind and lang must not be empty", name, lineNo)
		}
		if first, dup := seenID[kw.ID]; dup {
			return nil, fmt.Errorf("%s:%d: duplicate id %q (first on line %d)", name, lineNo, kw.ID, first)
		}
		switch cols[4] {
		case "0":
		case "1":
			kw.Ambiguous = true
		default:
			return nil, fmt.Errorf("%s:%d: ambiguous must be 0 or 1, got %q", name, lineNo, cols[4])
		}
		for _, v := range strings.Split(cols[5], "|") {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			if Normalize(v) == "" {
				return nil, fmt.Errorf("%s:%d: variant %q has no letters or digits", name, lineNo, v)
			}
			kw.Variants = append(kw.Variants, v)
		}
		if len(kw.Variants) == 0 {
			return nil, fmt.Errorf("%s:%d: keyword %q has no variants", name, lineNo, kw.ID)
		}
		seenID[kw.ID] = lineNo
		out = append(out, kw)
	}
	return out, nil
}
