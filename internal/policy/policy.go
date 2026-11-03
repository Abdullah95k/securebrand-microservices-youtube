// Package policy holds how often each thing is re-checked, per channel tier
// (plan §8.6). Tiers A–D are core channels by size and relevance; M is a
// mention channel (only its Iraq-relevant items are collected). The
// capacity planner may stretch the intervals of C, D and M when the proxy
// pool cannot keep up; A and B are never stretched.
package policy

import (
	"fmt"
	"time"
)

// Tier is a channel tier.
type Tier string

// Tiers.
const (
	A Tier = "A"
	B Tier = "B"
	C Tier = "C"
	D Tier = "D"
	M Tier = "M"
)

// Valid reports whether t is a known tier.
func (t Tier) Valid() bool {
	switch t {
	case A, B, C, D, M:
		return true
	}
	return false
}

// ParseTier accepts "A"… "M" (any case).
func ParseTier(s string) (Tier, error) {
	if len(s) == 1 && s[0] >= 'a' && s[0] <= 'z' {
		s = string(s[0] - 'a' + 'A')
	}
	t := Tier(s)
	if !t.Valid() {
		return "", fmt.Errorf("policy: tier %q is not one of A, B, C, D, M", s)
	}
	return t, nil
}

const (
	day     = 24 * time.Hour
	week    = 7 * day
	month   = 30 * day
	quarter = 90 * day
)

type row struct{ a, b, c, d, m time.Duration }

func (r row) of(t Tier) time.Duration {
	switch t {
	case A:
		return r.a
	case B:
		return r.b
	case C:
		return r.c
	case D:
		return r.d
	default:
		return r.m
	}
}

// Stretch multiplies an interval for tiers C, D and M by s (≥ 1).
func Stretch(d time.Duration, t Tier, s float64) time.Duration {
	if s <= 1 || t == A || t == B || d == 0 {
		return d
	}
	return time.Duration(float64(d) * s)
}

// ChannelSnapshot: channel page, About panel and a stats snapshot.
func ChannelSnapshot(t Tier) time.Duration {
	return row{6 * time.Hour, day, 3 * day, week, week}.of(t)
}

// RSS: the channel's feed, for new uploads (WebSub push comes on top).
func RSS(t Tier) time.Duration {
	return row{30 * time.Minute, 2 * time.Hour, 6 * time.Hour, day, day}.of(t)
}

// TabCheck: the Videos, Shorts and Live tabs, which catch what the feed
// misses (premieres, videos made public later). Mention channels have none:
// their history comes from channel-scoped keyword searches.
func TabCheck(t Tier) time.Duration {
	return row{day, day, week, month, 0}.of(t)
}

// Posts: the community Posts tab.
func Posts(t Tier) time.Duration {
	return row{day, 3 * day, week, month, week}.of(t)
}

// VideoStats: views, likes and the comment count, by the video's age.
func VideoStats(t Tier, age time.Duration) time.Duration {
	switch {
	case age < 2*day:
		return row{30 * time.Minute, 2 * time.Hour, 6 * time.Hour, 6 * time.Hour, 2 * time.Hour}.of(t)
	case age < month:
		return row{6 * time.Hour, 12 * time.Hour, day, day, 12 * time.Hour}.of(t)
	case age < 365*day:
		return row{week, week, month, month, week}.of(t)
	default:
		return row{month, month, quarter, quarter, month}.of(t)
	}
}

// CommentScan: the newest-first scan for new comments, by the video's age.
// Zero means stop: the plan scans for late comments up to a year after the
// upload.
func CommentScan(t Tier, age time.Duration) time.Duration {
	switch {
	case age < 2*day:
		return row{time.Hour, 3 * time.Hour, 6 * time.Hour, 6 * time.Hour, 3 * time.Hour}.of(t)
	case age < week:
		return row{6 * time.Hour, 12 * time.Hour, day, day, 12 * time.Hour}.of(t)
	case age < month:
		return row{day, 2 * day, week, week, 2 * day}.of(t)
	case age < 365*day:
		return row{month, month, quarter, quarter, month}.of(t)
	default:
		return 0
	}
}

// Sweeps are the ages at which a video's comments are read in full, to
// catch edits and deletions.
func Sweeps(t Tier) []time.Duration {
	if t == C || t == D {
		return []time.Duration{month}
	}
	return []time.Duration{week, month}
}

// NextSweep is the next full-sweep age after age, or 0 when none is left.
func NextSweep(t Tier, age time.Duration) time.Duration {
	for _, s := range Sweeps(t) {
		if s > age {
			return s
		}
	}
	return 0
}

// LiveViewers: how often the viewer count of a running live stream is read.
func LiveViewers(t Tier) time.Duration {
	if t == A || t == B {
		return 5 * time.Minute
	}
	return 15 * time.Minute
}

// AuthorRefresh: how often an enriched commenter profile is read again.
func AuthorRefresh(active bool) time.Duration {
	if active {
		return 6 * month
	}
	return 365 * day
}
