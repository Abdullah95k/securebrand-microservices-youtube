package phase0

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ailabel"
)

// fakeLabeler answers like a careful labeller: titles naming Baghdad or the
// brand are relevant, the rest not. hook may change a call's outcome.
type fakeLabeler struct {
	calls int
	seen  []string // every title sent
	hook  func(call int, titles []ailabel.Title) (ailabel.Result, error, bool)
}

func (f *fakeLabeler) Label(_ context.Context, titles []ailabel.Title) (ailabel.Result, error) {
	f.calls++
	for _, t := range titles {
		f.seen = append(f.seen, t.Text)
	}
	if f.hook != nil {
		if r, err, ok := f.hook(f.calls, titles); ok {
			return r, err
		}
	}
	return careful(titles), nil
}

func careful(titles []ailabel.Title) ailabel.Result {
	res := ailabel.Result{Model: "test-model", InputTokens: 100, OutputTokens: 20}
	for _, t := range titles {
		v := ailabel.Verdict{ID: t.ID, Answer: ailabel.No, Reason: "no Iraqi angle"}
		if strings.Contains(t.Text, "بغداد") || strings.Contains(t.Text, "zqbrandx") {
			v = ailabel.Verdict{ID: t.ID, Answer: ailabel.Yes, Reason: "names Baghdad or the brand"}
		}
		res.Verdicts = append(res.Verdicts, v)
	}
	return res
}

func discoveredStore(t *testing.T) *Store {
	t.Helper()
	_, col, api := discoveryRig(t)
	o := DefaultDiscoverOptions()
	o.Terms = []string{"بغداد", "zqbrandx"}
	if err := Discover(context.Background(), col, api, o, quietLog); err != nil {
		t.Fatal(err)
	}
	return col.Store
}

func TestLabelAIThenSpotCheck(t *testing.T) {
	s := discoveredStore(t)
	// The first reply leaves one title out and the second is unusable: the
	// three titles are asked again in the second pass (batches of 2 here).
	ai := &fakeLabeler{hook: func(call int, titles []ailabel.Title) (ailabel.Result, error, bool) {
		switch call {
		case 1:
			r := careful(titles)
			r.Verdicts = r.Verdicts[:1]
			return r, nil, true
		case 2:
			return ailabel.Result{}, ailabel.ErrAnswer, true
		}
		return ailabel.Result{}, nil, false
	}}
	o := AILabelOptions{LabelOptions: LabelOptions{Size: 4}, Batch: 2}
	if err := LabelAI(context.Background(), s, ai, o, quietLog); err != nil {
		t.Fatal(err)
	}
	answers, _ := ReadAll[LabelAnswer](s, "labels")
	if len(answers) != 4 || ai.calls != 4 {
		t.Fatalf("answers %+v after %d calls; want 4 answers after 2 + 2 calls", answers, ai.calls)
	}
	for _, a := range answers {
		if a.Source != SourceAI || a.Model != "test-model" || a.Reason == "" {
			t.Errorf("answer %+v; want the source, model and reason", a)
		}
	}
	// Only titles were sent: no video IDs.
	for _, title := range ai.seen {
		if strings.Contains(title, "fakeS") {
			t.Errorf("sent more than a title: %q", title)
		}
	}
	// A second run has nothing left to do.
	if err := LabelAI(context.Background(), s, ai, o, quietLog); err != nil || ai.calls != 4 {
		t.Errorf("a finished run asked again: %d calls, err %v", ai.calls, err)
	}

	var rep strings.Builder
	if err := Report(s, &rep, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"| Labelled by the AI / by you | 4 / 0 (AI model: test-model) |",
		"| Precision: accepted videos that the AI marked relevant | 100% (2 of 2) |",
		"| Rejected videos that the AI marked relevant | 50% (1 of 2) |",
		"Its labels measure agreement with its judgment, not the truth",
	} {
		if !strings.Contains(rep.String(), want) {
			t.Errorf("report misses %q:\n%s", want, rep.String())
		}
	}

	// Spot check: you answer 2 titles; the AI's answers stay hidden until
	// the end. You disagree on the first one.
	sample, _ := labelSample(s, o.LabelOptions)
	flip := func(it LabelItem) string {
		ai := strings.Contains(it.Title, "بغداد") || strings.Contains(it.Title, "zqbrandx")
		if ai {
			return "n"
		}
		return "y"
	}
	same := func(it LabelItem) string {
		if flip(it) == "y" {
			return "n"
		}
		return "y"
	}
	var out strings.Builder
	in := strings.NewReader(flip(sample[0]) + "\n" + same(sample[1]) + "\n")
	if err := Label(context.Background(), s, in, &out, LabelOptions{Size: 4, Limit: 2}); err != nil {
		t.Fatal(err)
	}
	o2 := out.String()
	before, after, _ := strings.Cut(o2, "Done: 2 answered this time")
	if strings.Contains(before, "names Baghdad") || strings.Contains(before, "no Iraqi angle") {
		t.Errorf("the AI's answers must stay hidden while you label:\n%s", before)
	}
	for _, want := range []string{"Spot check of the AI's labels", "[1/2]", "[2/2]", "agreed on 1 of 2", sample[0].Title} {
		if !strings.Contains(o2, want) {
			t.Errorf("spot check output misses %q:\n%s", want, o2)
		}
	}
	if !strings.Contains(after, "you: ") || !strings.Contains(after, "AI: ") {
		t.Errorf("want the disagreement listed with both answers:\n%s", after)
	}

	rep.Reset()
	if err := Report(s, &rep, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"| Labelled by the AI / by you | 2 / 2 (AI model: test-model) |",
		"| You and the AI agree (titles you both answered) | 50% (1 of 2) |",
		"accepted videos that were marked relevant",
	} {
		if !strings.Contains(rep.String(), want) {
			t.Errorf("report misses %q:\n%s", want, rep.String())
		}
	}
	for _, leak := range []string{"zqbrandx", "بغداد اليوم", "names Baghdad", "no Iraqi angle"} {
		if strings.Contains(rep.String(), leak) {
			t.Errorf("the report must not show titles or reasons: %q", leak)
		}
	}
}

// TestLabelAIStopsOnAPIErrorAndRecordsRefusals: a lasting API error stops
// the run with earlier answers kept; a batch declined twice counts as
// "can't tell" for its titles.
func TestLabelAIStopsOnAPIErrorAndRecordsRefusals(t *testing.T) {
	s := discoveredStore(t)
	o := AILabelOptions{LabelOptions: LabelOptions{Size: 4}, Batch: 2}
	stopErr := errors.New("ailabel: API error 401 authentication_error")
	ai := &fakeLabeler{hook: func(call int, _ []ailabel.Title) (ailabel.Result, error, bool) {
		if call == 2 {
			return ailabel.Result{}, stopErr, true
		}
		return ailabel.Result{}, nil, false
	}}
	if err := LabelAI(context.Background(), s, ai, o, quietLog); !errors.Is(err, stopErr) {
		t.Fatalf("err %v; want the API error", err)
	}
	if answers, _ := ReadAll[LabelAnswer](s, "labels"); len(answers) != 2 {
		t.Fatalf("answers %+v; want the first batch kept", answers)
	}

	// Continue: the remaining batch is declined in both passes.
	ai = &fakeLabeler{hook: func(int, []ailabel.Title) (ailabel.Result, error, bool) {
		return ailabel.Result{Model: "test-model", Refused: true}, nil, true
	}}
	if err := LabelAI(context.Background(), s, ai, o, quietLog); err != nil {
		t.Fatal(err)
	}
	answers, _ := ReadAll[LabelAnswer](s, "labels")
	declined := 0
	for _, a := range answers {
		if a.Skipped && strings.Contains(a.Reason, "declined") {
			declined++
		}
	}
	if len(answers) != 4 || declined != 2 || ai.calls != 2 {
		t.Errorf("answers %+v after %d calls; want 2 declined titles, asked once per pass", answers, ai.calls)
	}

	// Reset draws the sample again and drops every answer.
	if _, err := labelSample(s, LabelOptions{Size: 4, Reset: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), "labels.jsonl")); !os.IsNotExist(err) {
		t.Errorf("reset should drop the answers: %v", err)
	}
}
