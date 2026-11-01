// Package ytparse turns YouTube website responses into plain Go values.
//
// Parsers find data by object type anywhere in a response (plan §8.11) and
// understand both the older "…Renderer" layout and the newer "…ViewModel"
// layout. Display text is parsed in English and Arabic, because requests use
// hl=ar by default.
//
// Many website values are approximate: comment times are relative ("3 days
// ago") and large counts are abbreviated ("1.2K"). Count and TimeEstimate
// carry that precision explicitly so it is never lost (plan §2).
package ytparse

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Count is a number read from display text.
type Count struct {
	Min   int64 // lower bound (the value itself when Exact)
	Max   int64 // exclusive upper bound; Min+1 when Exact
	Exact bool
	OK    bool   // false when the text held no number
	Text  string // original text, kept for audits
}

// Value returns the best single value (the lower bound).
func (c Count) Value() int64 { return c.Min }

var (
	digitFolder = strings.NewReplacer(
		"٠", "0", "١", "1", "٢", "2", "٣", "3", "٤", "4", "٥", "5", "٦", "6", "٧", "7", "٨", "8", "٩", "9",
		"۰", "0", "۱", "1", "۲", "2", "۳", "3", "۴", "4", "۵", "5", "۶", "6", "۷", "7", "۸", "8", "۹", "9",
		"٫", ".", "٬", ",", " ", " ", " ", " ", "‎", "", "‏", "", "؜", "",
	)
	// A K/M/B suffix must not be the start of a word ("1 member" is not 1M).
	numberRe = regexp.MustCompile(`(\d[\d,]*(?:\.\d+)?)\s*((?:[KMBkmb](?:[^A-Za-z]|$))|ألف|آلاف|الف|مليون|ملايين|مليار|مليارات)?`)
	noneRe   = regexp.MustCompile(`(?i)\bno\b|لا توجد|لا يوجد|بلا`)
)

func foldDigits(s string) string { return digitFolder.Replace(s) }

// ParseCount reads counts such as "12,345 views", "1.2K", "3M subscribers",
// "No views", "١٢٬٣٤٥ مشاهدة" or "1.2 ألف".
//
// YouTube abbreviates without rounding up: values under 10 of a unit show
// one decimal ("1.2K" = 1,200–1,299) and larger ones none ("12K" =
// 12,000–12,999). [verify in Phase 0, experiment E5]
func ParseCount(text string) Count {
	c := Count{Text: text}
	s := foldDigits(text)
	m := numberRe.FindStringSubmatch(s)
	if m == nil {
		if noneRe.MatchString(s) {
			return Count{Min: 0, Max: 1, Exact: true, OK: true, Text: text}
		}
		return c
	}
	num, suffix := strings.ReplaceAll(m[1], ",", ""), strings.TrimSpace(m[2])
	if len(suffix) > 1 && strings.ContainsAny(suffix[:1], "KMBkmb") {
		suffix = suffix[:1] // drop the character after K/M/B captured by the regexp
	}
	var unit float64
	switch strings.ToLower(suffix) {
	case "":
		unit = 1
	case "k", "ألف", "آلاف", "الف":
		unit = 1e3
	case "m", "مليون", "ملايين":
		unit = 1e6
	case "b", "مليار", "مليارات":
		unit = 1e9
	}
	if unit == 1 {
		n, err := strconv.ParseInt(strings.Split(num, ".")[0], 10, 64)
		if err != nil {
			return c
		}
		return Count{Min: n, Max: n + 1, Exact: true, OK: true, Text: text}
	}
	mant, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return c
	}
	step := unit
	if mant < 10 {
		step = unit / 10
	}
	lo := int64(mant*unit + 0.5)
	return Count{Min: lo, Max: lo + int64(step), Exact: false, OK: true, Text: text}
}

// FirstNumber returns the first exact integer in text, for accessibility
// labels such as "like this video along with 12,345 other people".
func FirstNumber(text string) (int64, bool) {
	s := foldDigits(text)
	m := regexp.MustCompile(`\d[\d,]*`).FindString(s)
	if m == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.ReplaceAll(m, ",", ""), 10, 64)
	return n, err == nil
}

// TimeEstimate is a point in time read from relative display text.
type TimeEstimate struct {
	At        time.Time     // best estimate
	Precision time.Duration // the true time is within ±Precision
	Edited    bool          // text carried "(edited)" / "(تم التعديل)"
	OK        bool
	Text      string
}

type unitInfo struct {
	d       time.Duration
	implied int // count implied by the word itself (1, or 2 for Arabic duals)
}

const day = 24 * time.Hour

var englishUnits = map[string]time.Duration{
	"second": time.Second, "minute": time.Minute, "hour": time.Hour,
	"day": day, "week": 7 * day, "month": 30 * day, "year": 365 * day,
}

// Arabic unit words, including dual forms that carry the number themselves
// ("يومين" = two days). Keys are normalized by normArabic.
var arabicUnits = map[string]unitInfo{
	"ثانيه": {time.Second, 1}, "ثانيتين": {time.Second, 2}, "ثانيتان": {time.Second, 2}, "ثوان": {time.Second, 1}, "ثواني": {time.Second, 1},
	"دقيقه": {time.Minute, 1}, "دقيقتين": {time.Minute, 2}, "دقيقتان": {time.Minute, 2}, "دقائق": {time.Minute, 1},
	"ساعه": {time.Hour, 1}, "ساعتين": {time.Hour, 2}, "ساعتان": {time.Hour, 2}, "ساعات": {time.Hour, 1},
	"يوم": {day, 1}, "يوما": {day, 1}, "يومين": {day, 2}, "يومان": {day, 2}, "ايام": {day, 1},
	"اسبوع": {7 * day, 1}, "اسبوعا": {7 * day, 1}, "اسبوعين": {7 * day, 2}, "اسبوعان": {7 * day, 2}, "اسابيع": {7 * day, 1},
	"شهر": {30 * day, 1}, "شهرا": {30 * day, 1}, "شهرين": {30 * day, 2}, "شهران": {30 * day, 2}, "اشهر": {30 * day, 1}, "شهور": {30 * day, 1},
	"سنه": {365 * day, 1}, "سنتين": {365 * day, 2}, "سنتان": {365 * day, 2}, "سنوات": {365 * day, 1}, "سنين": {365 * day, 1},
	"عام": {365 * day, 1}, "عاما": {365 * day, 1}, "عامين": {365 * day, 2}, "عامان": {365 * day, 2}, "اعوام": {365 * day, 1},
}

var (
	englishAgoRe = regexp.MustCompile(`(?i)(\d+)\s+(second|minute|hour|day|week|month|year)s?\s+ago`)
	arabicAgoRe  = regexp.MustCompile(`(?:منذ|قبل)\s+(?:(\d+)\s+)?(\S+)`)
	editedRe     = regexp.MustCompile(`(?i)\(edited\)|\(تم التعديل\)|\(معدّل\)|\(معدل\)`)
	justNowRe    = regexp.MustCompile(`(?i)just now|الآن|للتو`)
)

// normArabic folds the letter variants that appear in YouTube's Arabic UI
// strings, so dictionary lookups are simple.
func normArabic(s string) string {
	r := strings.NewReplacer("أ", "ا", "إ", "ا", "آ", "ا", "ة", "ه", "ى", "ي", "ً", "", "ٌ", "", "ٍ", "", "َ", "", "ُ", "", "ِ", "", "ّ", "", "ْ", "", "ـ", "")
	return r.Replace(s)
}

// ParseRelativeTime reads "3 days ago", "Streamed 2 weeks ago",
// "منذ 3 أيام", "قبل 3 أيام", "منذ يومين", "منذ ساعة (تم التعديل)" relative to now.
func ParseRelativeTime(text string, now time.Time) TimeEstimate {
	te := TimeEstimate{Text: text, Edited: editedRe.MatchString(text)}
	s := foldDigits(text)
	if m := englishAgoRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		d := englishUnits[strings.ToLower(m[2])]
		te.At, te.Precision, te.OK = now.Add(-time.Duration(n)*d), d, true
		return te
	}
	if m := arabicAgoRe.FindStringSubmatch(normArabic(s)); m != nil {
		word := strings.Trim(m[2], "()،,.")
		if u, ok := arabicUnits[word]; ok {
			n := u.implied
			if m[1] != "" {
				n, _ = strconv.Atoi(m[1])
			}
			te.At, te.Precision, te.OK = now.Add(-time.Duration(n)*u.d), u.d, true
			return te
		}
	}
	if justNowRe.MatchString(s) {
		te.At, te.Precision, te.OK = now, time.Minute, true
	}
	return te
}

// ParseClock reads a duration shown as "12:34" or "1:02:03" into seconds.
func ParseClock(text string) (int, bool) {
	s := strings.TrimSpace(foldDigits(text))
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	total := 0
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return 0, false
		}
		total = total*60 + n
	}
	return total, true
}
