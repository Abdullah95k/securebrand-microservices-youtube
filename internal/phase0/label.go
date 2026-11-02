package phase0

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Keyword-matching quality (E11): you label a sample of found videos as
// relevant or not, one title at a time in the terminal, without seeing
// what the matcher decided. Half the sample is videos the matcher accepted,
// half videos it rejected, so the report can estimate both precision (how
// many accepted videos are really relevant) and recall (how many relevant
// videos it catches). Titles are public video titles; the sample stays in
// the data folder and the report shows only rates and seed keyword names.

// LabelItem is one video in the sample.
type LabelItem struct {
	Index     int      `json:"index"`
	VideoID   string   `json:"video_id"`
	Title     string   `json:"title"`
	Qualifies bool     `json:"qualifies"` // the matcher's decision, hidden while labelling
	Keywords  []string `json:"keywords"`  // matched keyword IDs
}

// LabelAnswer is your answer for one sample item.
type LabelAnswer struct {
	At       time.Time `json:"at"`
	Index    int       `json:"index"`
	Relevant bool      `json:"relevant"`
	Skipped  bool      `json:"skipped"`
}

// LabelOptions configures the labelling session.
type LabelOptions struct {
	Size  int      // sample size (half accepted, half rejected by the matcher)
	Terms []string // your own keywords, matched like the seed list
	Reset bool     // draw a new sample and drop earlier answers
}

// labelSample loads the sample, or draws and saves it on first use.
func labelSample(s *Store, o LabelOptions) ([]LabelItem, error) {
	if !o.Reset {
		if got, err := ReadAll[LabelItem](s, "label_sample"); err != nil || len(got) > 0 {
			return got, err
		}
	}
	items, err := ReadAll[DiscoverItem](s, "discover_items")
	if err != nil {
		return nil, err
	}
	m := newDiscoveryMatcher(o.Terms)
	seen := map[string]bool{}
	var pos, neg []LabelItem
	for _, it := range items {
		if seen[it.VideoID] || strings.TrimSpace(it.Title) == "" {
			continue
		}
		seen[it.VideoID] = true
		ok, ids := m.matches(it.Title)
		li := LabelItem{VideoID: it.VideoID, Title: it.Title, Qualifies: ok, Keywords: ids}
		if ok {
			pos = append(pos, li)
		} else {
			neg = append(neg, li)
		}
	}
	if len(pos)+len(neg) == 0 {
		return nil, errors.New("label: nothing to label yet; run discover first")
	}
	rng := rand.New(rand.NewSource(1)) // the same data gives the same sample
	rng.Shuffle(len(pos), func(i, j int) { pos[i], pos[j] = pos[j], pos[i] })
	rng.Shuffle(len(neg), func(i, j int) { neg[i], neg[j] = neg[j], neg[i] })
	half := o.Size / 2
	if len(pos) > half {
		pos = pos[:half]
	}
	if len(neg) > o.Size-len(pos) {
		neg = neg[:o.Size-len(pos)]
	}
	sample := append(pos, neg...)
	rng.Shuffle(len(sample), func(i, j int) { sample[i], sample[j] = sample[j], sample[i] })
	for _, name := range []string{"label_sample.jsonl", "labels.jsonl"} {
		_ = os.Remove(filepath.Join(s.Dir(), name))
	}
	for i := range sample {
		sample[i].Index = i
		if err := s.Append("label_sample", sample[i]); err != nil {
			return nil, err
		}
	}
	return sample, nil
}

// Label runs the interactive session: it shows each unlabelled title and
// reads y (relevant), n (not relevant), s (skip) or q (quit) from in. Every
// answer is saved at once, so you can stop and continue later.
func Label(ctx context.Context, s *Store, in io.Reader, out io.Writer, o LabelOptions) error {
	if o.Size <= 0 {
		o.Size = 300
	}
	sample, err := labelSample(s, o)
	if err != nil {
		return err
	}
	answers, err := ReadAll[LabelAnswer](s, "labels")
	if err != nil {
		return err
	}
	done := map[int]bool{}
	for _, a := range answers {
		done[a.Index] = true
	}
	fmt.Fprintf(out, "Keyword-matching check: %d videos, %d already answered.\n", len(sample), len(done))
	fmt.Fprintln(out, "For each title: is this video about Iraq, or relevant to Iraqi audiences, so that we should collect it?")
	fmt.Fprintln(out, "Type y (yes), n (no), s (skip, can't tell) or q (quit; you can continue later), then Enter.")
	sc := bufio.NewScanner(in)
	for _, it := range sample {
		if done[it.Index] {
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		for {
			fmt.Fprintf(out, "\n[%d/%d] %s\n> ", len(done)+1, len(sample), it.Title)
			if !sc.Scan() {
				fmt.Fprintf(out, "\nStopped. %d of %d answered; run the same command to continue.\n", len(done), len(sample))
				return sc.Err()
			}
			var a LabelAnswer
			switch strings.ToLower(strings.TrimSpace(sc.Text())) {
			case "y", "yes", "نعم", "ن":
				a = LabelAnswer{Relevant: true}
			case "n", "no", "لا", "ل":
				a = LabelAnswer{}
			case "s", "skip":
				a = LabelAnswer{Skipped: true}
			case "q", "quit":
				fmt.Fprintf(out, "Saved. %d of %d answered; run the same command to continue.\n", len(done), len(sample))
				return nil
			default:
				fmt.Fprintln(out, "Please type y, n, s or q.")
				continue
			}
			a.At, a.Index = time.Now(), it.Index
			if err := s.Append("labels", a); err != nil {
				return err
			}
			done[it.Index] = true
			break
		}
	}
	fmt.Fprintf(out, "\nAll %d answered. Thank you! The report now includes the keyword-matching results.\n", len(sample))
	return nil
}

// matches is qualifies plus the matched keyword IDs.
func (d *discoveryMatcher) matches(text string) (bool, []string) {
	ids := map[string]bool{}
	for _, h := range d.m.Match(text) {
		ids[h.KeywordID] = true
	}
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return d.qualifies(text), out
}

// reportLabels writes E11 from the sample and your answers.
func reportLabels(p func(string, ...any), sample []LabelItem, answers []LabelAnswer, items []DiscoverItem) {
	if len(sample) == 0 || len(answers) == 0 {
		return
	}
	last := map[int]LabelAnswer{} // the latest answer per item counts
	for _, a := range answers {
		last[a.Index] = a
	}
	var tp, fp, fn, tn, skipped int
	falseByKw := map[string]int{}
	for _, it := range sample {
		a, ok := last[it.Index]
		if !ok {
			continue
		}
		if a.Skipped {
			skipped++
			continue
		}
		switch {
		case it.Qualifies && a.Relevant:
			tp++
		case it.Qualifies && !a.Relevant:
			fp++
			for _, k := range it.Keywords {
				if strings.HasPrefix(k, "term-") {
					k = "(one of your own keywords)"
				}
				falseByKw[k]++
			}
		case !it.Qualifies && a.Relevant:
			fn++
		default:
			tn++
		}
	}
	// The sample holds as many rejected as accepted videos, so recall is
	// estimated with the real share of accepted videos among all found ones.
	seen := map[string]bool{}
	acc, all := 0, 0
	for _, it := range items {
		if seen[it.VideoID] {
			continue
		}
		seen[it.VideoID] = true
		all++
		if it.Qualifies {
			acc++
		}
	}
	precision := share(tp, tp+fp)
	missRate := share(fn, fn+tn)
	pAcc := share(acc, all)
	recall := 0.0
	if d := pAcc*precision + (1-pAcc)*missRate; d > 0 {
		recall = pAcc * precision / d
	}
	p("## Keyword-matching quality (E11)")
	p("")
	p("| | Value |")
	p("|---|---|")
	p("| Videos answered / skipped / sample size | %d / %d / %d |", tp+fp+fn+tn, skipped, len(sample))
	p("| Precision: accepted videos that you marked relevant | %s (%d of %d) |", pctStr(precision), tp, tp+fp)
	p("| Rejected videos that you marked relevant | %s (%d of %d) |", pctStr(missRate), fn, fn+tn)
	p("| Share of all found videos the matcher accepts | %s |", pctStr(pAcc))
	p("| Estimated recall: relevant videos the matcher catches | %s |", pctStr(recall))
	p("")
	if len(falseByKw) > 0 {
		type kc struct {
			k string
			n int
		}
		var list []kc
		for k, n := range falseByKw {
			list = append(list, kc{k, n})
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].n != list[j].n {
				return list[i].n > list[j].n
			}
			return list[i].k < list[j].k
		})
		if len(list) > 10 {
			list = list[:10]
		}
		var parts []string
		for _, x := range list {
			parts = append(parts, fmt.Sprintf("%s (%d)", x.k, x.n))
		}
		p("Keywords behind wrongly accepted videos: %s.", strings.Join(parts, ", "))
		p("")
	}
}
