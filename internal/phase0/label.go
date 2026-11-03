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

// Keyword-matching quality (E11): a sample of found videos is labelled
// relevant or not without seeing what the matcher decided, either by the AI
// (LabelAI: only the public titles are sent to the AI provider) or by you,
// one title at a time in the terminal (Label). Half the sample is videos the
// matcher accepted, half videos it rejected, so the report can estimate both
// precision (how many accepted videos are really relevant) and recall (how
// many relevant videos it catches). When the AI labelled, you can answer a
// few titles yourself as a spot check; the report then shows how often you
// and the AI agree, and your answer counts where you gave one. The sample
// stays in the data folder and the report shows only rates and seed keyword
// names.

// LabelItem is one video in the sample.
type LabelItem struct {
	Index     int      `json:"index"`
	VideoID   string   `json:"video_id"`
	Title     string   `json:"title"`
	Qualifies bool     `json:"qualifies"` // the matcher's decision, hidden while labelling
	Keywords  []string `json:"keywords"`  // matched keyword IDs
}

// LabelAnswer is one answer for one sample item, yours or the AI's.
type LabelAnswer struct {
	At       time.Time `json:"at"`
	Index    int       `json:"index"`
	Relevant bool      `json:"relevant"`
	Skipped  bool      `json:"skipped"`          // can't tell
	Source   string    `json:"source,omitempty"` // SourceAI, or empty for your own answer
	Model    string    `json:"model,omitempty"`  // AI: the model that answered
	Reason   string    `json:"reason,omitempty"` // AI: what decided it
}

// SourceAI marks answers given by the AI.
const SourceAI = "ai"

// LabelOptions configures the labelling session.
type LabelOptions struct {
	Size  int      // sample size (half accepted, half rejected by the matcher)
	Terms []string // your own keywords, matched like the seed list
	Reset bool     // draw a new sample and drop earlier answers
	Limit int      // by hand: answer at most this many titles this time (0 = all)
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

// Label runs the interactive session: it shows each title you have not
// answered and reads y (relevant), n (not relevant), s (skip) or q (quit)
// from in. Every answer is saved at once, so you can stop and continue
// later. When the AI has labelled the sample, this is the spot check: the
// AI's answers stay hidden until the end, then the titles where you and the
// AI disagree are listed with the AI's reason.
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
	ai := map[int]LabelAnswer{}
	for _, a := range answers {
		if a.Source == SourceAI {
			ai[a.Index] = a
		} else {
			done[a.Index] = true
		}
	}
	if len(ai) > 0 {
		fmt.Fprintf(out, "Spot check of the AI's labels: %d videos, %d answered by you so far. The AI's answers stay hidden until the end.\n", len(sample), len(done))
	} else {
		fmt.Fprintf(out, "Keyword-matching check: %d videos, %d already answered.\n", len(sample), len(done))
	}
	fmt.Fprintln(out, "For each title: is this video about Iraq, or relevant to Iraqi audiences, so that we should collect it?")
	fmt.Fprintln(out, "Type y (yes), n (no), s (skip, can't tell) or q (quit; you can continue later), then Enter.")
	sc := bufio.NewScanner(in)
	session := 0
	var mine []LabelAnswer // this session's answers, for the comparison
	finish := func() {
		reportDisagreements(out, sample, ai, mine)
	}
	for _, it := range sample {
		if done[it.Index] || it.Title == "" { // a blank title was official-API data, removed after 30 days
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if o.Limit > 0 && session >= o.Limit {
			fmt.Fprintf(out, "\nDone: %d answered this time (%d of %d in total). Run the same command to answer more.\n", session, len(done), len(sample))
			finish()
			return nil
		}
		for {
			if o.Limit > 0 {
				fmt.Fprintf(out, "\n[%d/%d] %s\n> ", session+1, o.Limit, it.Title)
			} else {
				fmt.Fprintf(out, "\n[%d/%d] %s\n> ", len(done)+1, len(sample), it.Title)
			}
			if !sc.Scan() {
				fmt.Fprintf(out, "\nStopped. %d of %d answered; run the same command to continue.\n", len(done), len(sample))
				finish()
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
				finish()
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
			session++
			mine = append(mine, a)
			break
		}
	}
	fmt.Fprintf(out, "\nAll %d answered. Thank you! The report now includes the keyword-matching results.\n", len(sample))
	finish()
	return nil
}

// reportDisagreements lists the titles you answered this session where the
// AI said otherwise (both gave a yes or no), with the AI's reason.
func reportDisagreements(out io.Writer, sample []LabelItem, ai map[int]LabelAnswer, mine []LabelAnswer) {
	if len(ai) == 0 || len(mine) == 0 {
		return
	}
	title := map[int]string{}
	for _, it := range sample {
		title[it.Index] = it.Title
	}
	yn := map[bool]string{true: "yes", false: "no"}
	both, differ := 0, 0
	var lines []string
	for _, a := range mine {
		b, ok := ai[a.Index]
		if !ok || a.Skipped || b.Skipped {
			continue
		}
		both++
		if a.Relevant != b.Relevant {
			differ++
			lines = append(lines, fmt.Sprintf("- %s\n  you: %s, AI: %s (%s)", title[a.Index], yn[a.Relevant], yn[b.Relevant], b.Reason))
		}
	}
	if both == 0 {
		return
	}
	fmt.Fprintf(out, "\nThis time you and the AI agreed on %d of %d titles you both answered.\n", both-differ, both)
	if differ > 0 {
		fmt.Fprintln(out, "Where you disagreed (your answer counts in the report):")
		for _, l := range lines {
			fmt.Fprintln(out, l)
		}
	}
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
	// The latest answer per item and source counts; yours wins over the AI's.
	mine, ai := map[int]LabelAnswer{}, map[int]LabelAnswer{}
	models := map[string]bool{}
	for _, a := range answers {
		if a.Source == SourceAI {
			ai[a.Index] = a
			if a.Model != "" {
				models[a.Model] = true
			}
		} else {
			mine[a.Index] = a
		}
	}
	last := map[int]LabelAnswer{}
	for i, a := range ai {
		last[i] = a
	}
	for i, a := range mine {
		last[i] = a
	}
	byAI, byYou, both, agree := 0, 0, 0, 0
	for i, a := range last {
		if a.Source == SourceAI {
			byAI++
		} else {
			byYou++
		}
		if b, ok := ai[i]; ok && a.Source != SourceAI && !a.Skipped && !b.Skipped {
			both++
			if a.Relevant == b.Relevant {
				agree++
			}
		}
	}
	who := "you marked"
	switch {
	case byAI > 0 && byYou == 0:
		who = "the AI marked"
	case byAI > 0:
		who = "were marked"
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
	if byAI > 0 {
		names := make([]string, 0, len(models))
		for m := range models {
			names = append(names, m)
		}
		sort.Strings(names)
		p("| Labelled by the AI / by you | %d / %d (AI model: %s) |", byAI, byYou, strings.Join(names, ", "))
		if both > 0 {
			p("| You and the AI agree (titles you both answered) | %s (%d of %d) |", pctStr(share(agree, both)), agree, both)
		}
	}
	p("| Videos answered / skipped / sample size | %d / %d / %d |", tp+fp+fn+tn, skipped, len(sample))
	p("| Precision: accepted videos that %s relevant | %s (%d of %d) |", who, pctStr(precision), tp, tp+fp)
	p("| Rejected videos that %s relevant | %s (%d of %d) |", who, pctStr(missRate), fn, fn+tn)
	p("| Share of all found videos the matcher accepts | %s |", pctStr(pAcc))
	p("| Estimated recall: relevant videos the matcher catches | %s |", pctStr(recall))
	p("")
	if byAI > 0 {
		p("The AI judged each title alone, against the same question you would answer. Its labels measure agreement with its judgment, not the truth: the spot-check row shows how far to trust them. Where you answered, your answer counts.")
		p("")
	}
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
