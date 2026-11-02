package phase0

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestLabelSessionAndScore(t *testing.T) {
	_, col, api := discoveryRig(t)
	o := DefaultDiscoverOptions()
	o.Terms = []string{"بغداد", "zqbrandx"}
	if err := Discover(context.Background(), col, api, o, quietLog); err != nil {
		t.Fatal(err)
	}
	// Labelling matches the seed list only (no keywords file): the two
	// Baghdad titles are accepted, the other two rejected.
	lo := LabelOptions{Size: 4}
	sample, err := labelSample(col.Store, lo)
	if err != nil {
		t.Fatal(err)
	}
	if len(sample) != 4 {
		t.Fatalf("sample %+v; want 2 accepted and 2 rejected videos", sample)
	}
	// Answer like a careful labeller: accepted ones are relevant, and so is
	// the brand video the matcher missed; "nothing relevant" is not.
	var answers []string
	for _, it := range sample {
		if it.Qualifies || strings.Contains(it.Title, "zqbrandx") {
			answers = append(answers, "y")
		} else {
			answers = append(answers, "n")
		}
	}
	// First session: a typo, one answer, then quit.
	var out strings.Builder
	if err := Label(context.Background(), col.Store, strings.NewReader("maybe\n"+answers[0]+"\nq\n"), &out, lo); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Please type y, n, s or q.") || !strings.Contains(out.String(), "1 of 4 answered") {
		t.Errorf("first session output:\n%s", out.String())
	}
	if strings.Contains(out.String(), "accepted") || strings.Contains(out.String(), "Qualifies") {
		t.Error("the matcher's decision must stay hidden while labelling")
	}
	// Second session continues with the rest.
	out.Reset()
	if err := Label(context.Background(), col.Store, strings.NewReader(strings.Join(answers[1:], "\n")+"\n"), &out, lo); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "All 4 answered") {
		t.Errorf("second session output:\n%s", out.String())
	}

	var rep strings.Builder
	if err := Report(col.Store, &rep, time.Now()); err != nil {
		t.Fatal(err)
	}
	r := rep.String()
	// Precision 2/2; misses 1 of 2 rejected; the matcher accepted 3 of the 4
	// found videos during discovery (your term counted there), so recall is
	// 0.75 / (0.75 + 0.25 × 0.5) = 86%.
	for _, want := range []string{
		"## Keyword-matching quality (E11)",
		"| Videos answered / skipped / sample size | 4 / 0 / 4 |",
		"| Precision: accepted videos that you marked relevant | 100% (2 of 2) |",
		"| Rejected videos that you marked relevant | 50% (1 of 2) |",
		"| Estimated recall: relevant videos the matcher catches | 86% |",
	} {
		if !strings.Contains(r, want) {
			t.Errorf("report misses %q:\n%s", want, r)
		}
	}
	if strings.Contains(r, "zqbrandx") || strings.Contains(r, "بغداد اليوم") {
		t.Error("the report must not show titles")
	}
}
