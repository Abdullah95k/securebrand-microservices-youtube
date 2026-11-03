package policy

import (
	"testing"
	"time"
)

func TestIntervalsFollowThePlan(t *testing.T) {
	h := time.Hour
	cases := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"A snapshot", ChannelSnapshot(A), 6 * h},
		{"D snapshot", ChannelSnapshot(D), 7 * 24 * h},
		{"A feed", RSS(A), 30 * time.Minute},
		{"M feed", RSS(M), 24 * h},
		{"M tabs", TabCheck(M), 0},
		{"C tabs", TabCheck(C), 7 * 24 * h},
		{"B posts", Posts(B), 72 * h},
		{"A stats, new video", VideoStats(A, time.Hour), 30 * time.Minute},
		{"C stats, 10 days", VideoStats(C, 10*24*h), 24 * h},
		{"D stats, 2 years", VideoStats(D, 2*365*24*h), 90 * 24 * h},
		{"B comments, new video", CommentScan(B, time.Hour), 3 * h},
		{"A comments, 5 days", CommentScan(A, 5*24*h), 6 * h},
		{"C comments, 20 days", CommentScan(C, 20*24*h), 7 * 24 * h},
		{"M comments, 2 years", CommentScan(M, 2*365*24*h), 0},
		{"A live", LiveViewers(A), 5 * time.Minute},
		{"C live", LiveViewers(C), 15 * time.Minute},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: %v; want %v", c.name, c.got, c.want)
		}
	}
}

func TestSweepsAndStretch(t *testing.T) {
	day := 24 * time.Hour
	if NextSweep(A, day) != 7*day || NextSweep(A, 8*day) != 30*day || NextSweep(A, 31*day) != 0 {
		t.Error("A sweeps at 7 and 30 days")
	}
	if NextSweep(C, day) != 30*day {
		t.Error("C sweeps at 30 days only")
	}
	if Stretch(time.Hour, A, 3) != time.Hour || Stretch(time.Hour, C, 2) != 2*time.Hour || Stretch(time.Hour, M, 0.5) != time.Hour {
		t.Error("only C, D and M stretch, and never below their interval")
	}
	for _, s := range []string{"a", "M", "c"} {
		if _, err := ParseTier(s); err != nil {
			t.Errorf("ParseTier(%q): %v", s, err)
		}
	}
	if _, err := ParseTier("E"); err == nil {
		t.Error("E is not a tier")
	}
}
