package ytparse

import (
	"testing"
	"time"
)

func TestParseCount(t *testing.T) {
	tests := []struct {
		in       string
		min, max int64
		exact    bool
	}{
		{"12,345 views", 12345, 12346, true},
		{"1 view", 1, 2, true},
		{"No views", 0, 1, true},
		{"1.2K", 1200, 1300, false},
		{"12K views", 12000, 13000, false},
		{"123K", 123000, 124000, false},
		{"1M subscribers", 1000000, 1100000, false},
		{"3.4M", 3400000, 3500000, false},
		{"2.1B", 2100000000, 2200000000, false},
		{"١٢٬٣٤٥ مشاهدة", 12345, 12346, true},
		{"1.2 ألف", 1200, 1300, false},
		{"‏3.4 مليون مشترك", 3400000, 3500000, false},
		{"١٫٢ ألف", 1200, 1300, false},
		{"لا توجد مشاهدات", 0, 1, true},
		{"1 member", 1, 2, true},
		{"12 months", 12, 13, true},
		{"1.2K views", 1200, 1300, false},
		{"5M", 5000000, 5100000, false},
		// Screen-reader labels spell the unit out.
		{"12 thousand views", 12000, 13000, false},
		{"1.2 thousand views", 1200, 1300, false},
		{"3.4 million views", 3400000, 3500000, false},
		{"2 billion views", 2000000000, 2100000000, false},
		{"345 views", 345, 346, true},
		// Arabic says 1 and 2 in words (hl=ar).
		{"تعليق واحد", 1, 2, true},
		{"تعليقان", 2, 3, true},
		{"مشاهدة واحدة", 1, 2, true},
		{"مشتركان", 2, 3, true},
		{"٢ تعليقات", 2, 3, true},
	}
	for _, tc := range tests {
		c := ParseCount(tc.in)
		if !c.OK || c.Min != tc.min || c.Max != tc.max || c.Exact != tc.exact {
			t.Errorf("ParseCount(%q) = %+v, want [%d,%d) exact=%v", tc.in, c, tc.min, tc.max, tc.exact)
		}
	}
	if ParseCount("views").OK {
		t.Error("text without a number must not parse")
	}
}

func TestFirstNumber(t *testing.T) {
	if n, ok := FirstNumber("like this video along with 12,345 other people"); !ok || n != 12345 {
		t.Errorf("got %d %v", n, ok)
	}
	if n, ok := FirstNumber("أبدِ إعجابك بهذا الفيديو مع ١٬٢٣٤ من الأشخاص الآخرين"); !ok || n != 1234 {
		t.Errorf("arabic: got %d %v", n, ok)
	}
	if _, ok := FirstNumber("no digits"); ok {
		t.Error("want false")
	}
}

func TestParseRelativeTime(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		in     string
		ago    time.Duration
		prec   time.Duration
		edited bool
	}{
		{"3 days ago", 3 * day, day, false},
		{"1 hour ago (edited)", time.Hour, time.Hour, true},
		{"Streamed 2 weeks ago", 14 * day, 7 * day, false},
		{"5 years ago", 5 * 365 * day, 365 * day, false},
		{"منذ 3 أيام", 3 * day, day, false},
		{"منذ يومين", 2 * day, day, false},
		{"منذ ساعة", time.Hour, time.Hour, false},
		{"منذ ساعتين (تم التعديل)", 2 * time.Hour, time.Hour, true},
		{"منذ ١١ شهرًا", 11 * 30 * day, 30 * day, false},
		{"منذ سنتين", 2 * 365 * day, 365 * day, false},
		{"منذ 25 دقيقة", 25 * time.Minute, time.Minute, false},
		{"منذ أسبوع", 7 * day, 7 * day, false},
		{"قبل 3 أيام", 3 * day, day, false},
		{"قبل يومين", 2 * day, day, false},
		{"بُث قبل ٥ ساعات", 5 * time.Hour, time.Hour, false},
		{"just now", 0, time.Minute, false},
		// The video grid's short form (live recordings, 1 November 2026).
		{"3d ago", 3 * day, day, false},
		{"12h ago", 12 * time.Hour, time.Hour, false},
		{"2w ago", 14 * day, 7 * day, false},
		{"Streamed 5mo ago", 5 * 30 * day, 30 * day, false},
		{"1y ago", 365 * day, 365 * day, false},
		{"45m ago", 45 * time.Minute, time.Minute, false},
		{"Streamed 3 months ago", 3 * 30 * day, 30 * day, false},
	}
	for _, tc := range tests {
		te := ParseRelativeTime(tc.in, now)
		if !te.OK {
			t.Errorf("ParseRelativeTime(%q) failed", tc.in)
			continue
		}
		if got := now.Sub(te.At); got != tc.ago || te.Precision != tc.prec || te.Edited != tc.edited {
			t.Errorf("ParseRelativeTime(%q) = ago %v ±%v edited=%v; want %v ±%v edited=%v", tc.in, got, te.Precision, te.Edited, tc.ago, tc.prec, tc.edited)
		}
	}
	if ParseRelativeTime("Oct 3, 2026", now).OK {
		t.Error("absolute dates are not relative times")
	}
}

func TestParseClock(t *testing.T) {
	for in, want := range map[string]int{"0:59": 59, "12:34": 754, "1:02:03": 3723, "١٢:٣٤": 754} {
		if got, ok := ParseClock(in); !ok || got != want {
			t.Errorf("ParseClock(%q) = %d %v, want %d", in, got, ok, want)
		}
	}
	for _, bad := range []string{"LIVE", "12", "1:2:3:4", ""} {
		if _, ok := ParseClock(bad); ok {
			t.Errorf("ParseClock(%q) should fail", bad)
		}
	}
}
