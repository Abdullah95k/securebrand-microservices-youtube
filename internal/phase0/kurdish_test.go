package phase0

import "testing"

// Samples drawn before D28 are re-judged with the Kurdish-language rule.
func TestWithKurdishRule(t *testing.T) {
	sample := []LabelItem{
		{Index: 0, Title: "أخبار بغداد", Qualifies: true},
		{Index: 1, Title: "ڕۆژی 3 هێلکە بخۆی", Qualifies: false},
		{Index: 2, Title: "cooking show", Qualifies: false},
	}
	last := map[int]LabelAnswer{
		0: {Index: 0, Relevant: true},
		1: {Index: 1, Relevant: true},
		2: {Index: 2, Relevant: false},
	}
	items := []DiscoverItem{
		{VideoID: "a", Title: "أخبار بغداد", Qualifies: true},
		{VideoID: "b", Title: "ڕۆژی 3 هێلکە بخۆی"},
		{VideoID: "c", Title: "cooking show"},
	}
	precision, recall, changed := withKurdishRule(sample, last, items)
	if changed != 1 || precision != 1 || recall != 1 {
		t.Errorf("precision %v recall %v changed %d; want the Kurdish title accepted: 1, 1, 1", precision, recall, changed)
	}
	if _, _, changed := withKurdishRule(sample[:1], last, items[:1]); changed != 0 {
		t.Error("no Kurdish titles: nothing to report")
	}
}
