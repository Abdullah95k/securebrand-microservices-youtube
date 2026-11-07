package contract

import (
	"net/url"
	"strings"
	"time"

	contentv1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/content/v1"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytparse"
)

// Platform is YouTube's code in the platform registry.
const Platform = "yt"

// IDs: "<platform>:<kind>:<native id>".
func accountID(id string) string  { return Platform + ":account:" + id }
func postID(id string) string     { return Platform + ":post:" + id }
func commentID(id string) string  { return Platform + ":comment:" + id }
func playlistID(id string) string { return Platform + ":playlist:" + id }

// URLs of YouTube's own pages.
func channelURL(id string) string { return "https://www.youtube.com/channel/" + url.PathEscape(id) }
func videoURL(id, kind string) string {
	if kind == "short" {
		return "https://www.youtube.com/shorts/" + url.PathEscape(id)
	}
	return "https://www.youtube.com/watch?v=" + url.QueryEscape(id)
}
func communityURL(id string) string { return "https://www.youtube.com/post/" + url.PathEscape(id) }
func playlistURL(id string) string {
	return "https://www.youtube.com/playlist?list=" + url.QueryEscape(id)
}
func commentURL(parentKind, parentID, id string) string {
	if parentKind == "post" {
		return communityURL(parentID) + "?lc=" + url.QueryEscape(id)
	}
	return videoURL(parentID, "video") + "&lc=" + url.QueryEscape(id)
}

// iraq is Iraq time (UTC+3, no daylight saving): estimates of a day or
// longer start at 00:00 Iraq time (owner D-013).
var iraq = time.FixedZone("Asia/Baghdad", 3*60*60)

// precision maps yts's ± precision (published_precision_s) to the
// contract's, and sets the time at the start of its unit (D-013): minutes
// and hours at the start of the minute or hour, days and longer at 00:00
// Iraq time.
func precision(at time.Time, seconds *int) (time.Time, contentv1.TimePrecision) {
	if seconds == nil {
		return at, contentv1.TimePrecision_TIME_PRECISION_UNSPECIFIED
	}
	d := time.Duration(*seconds) * time.Second
	day := func() time.Time {
		l := at.In(iraq)
		return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, iraq).UTC()
	}
	switch {
	case d <= 0:
		return at, contentv1.TimePrecision_TIME_PRECISION_EXACT
	case d <= time.Minute:
		return at.Truncate(time.Minute), contentv1.TimePrecision_TIME_PRECISION_MINUTE
	case d <= time.Hour:
		return at.Truncate(time.Hour), contentv1.TimePrecision_TIME_PRECISION_HOUR
	case d <= 24*time.Hour:
		return day(), contentv1.TimePrecision_TIME_PRECISION_DAY
	case d <= 7*24*time.Hour:
		return day(), contentv1.TimePrecision_TIME_PRECISION_WEEK
	case d <= 31*24*time.Hour:
		return day(), contentv1.TimePrecision_TIME_PRECISION_MONTH
	default:
		return day(), contentv1.TimePrecision_TIME_PRECISION_YEAR
	}
}

// estimate is a published time: when yts knows it, how precisely, and the
// text shown. Nil when the time is unknown.
func estimate(at *time.Time, seconds *int, displayed *string) *contentv1.Time {
	if at == nil {
		return nil
	}
	t, p := precision(*at, seconds)
	if p == contentv1.TimePrecision_TIME_PRECISION_UNSPECIFIED {
		p = contentv1.TimePrecision_TIME_PRECISION_EXACT
	}
	return &contentv1.Time{At: timestamppb.New(t), Precision: p, Displayed: deref(displayed)}
}

// exact is a time yts recorded itself.
func exact(at *time.Time) *contentv1.Time {
	if at == nil {
		return nil
	}
	return &contentv1.Time{At: timestamppb.New(*at), Precision: contentv1.TimePrecision_TIME_PRECISION_EXACT}
}

// date is a calendar date ("Joined 2 Jan 2016") as a day at 00:00 Iraq
// time.
func date(s *string) *contentv1.Time {
	if s == nil {
		return nil
	}
	d, err := time.ParseInLocation("2006-01-02", *s, iraq)
	if err != nil {
		return nil
	}
	return &contentv1.Time{At: timestamppb.New(d.UTC()), Precision: contentv1.TimePrecision_TIME_PRECISION_DAY}
}

// count is a number YouTube showed as text ("1.2K", "12"): rounded when the
// text abbreviated it, exact otherwise. Nil when not read.
func count(v *int64, text *string) *contentv1.Count {
	if v == nil {
		return nil
	}
	c := &contentv1.Count{Value: v, Exactness: contentv1.CountExactness_COUNT_EXACTNESS_EXACT}
	if text != nil && *text != "" {
		c.Displayed = *text
		if pc := ytparse.ParseCount(*text); pc.OK && !pc.Exact {
			c.Exactness = contentv1.CountExactness_COUNT_EXACTNESS_ROUNDED
		}
	}
	return c
}

// exactCount is a number YouTube gave as an integer.
func exactCount(v *int64) *contentv1.Count {
	if v == nil {
		return nil
	}
	return &contentv1.Count{Value: v, Exactness: contentv1.CountExactness_COUNT_EXACTNESS_EXACT}
}

// atLeast is a number yts knows only as a lower bound.
func atLeast(v *int64) *contentv1.Count {
	if v == nil {
		return nil
	}
	return &contentv1.Count{Value: v, Exactness: contentv1.CountExactness_COUNT_EXACTNESS_LOWER_BOUND}
}

func i64(v *int) *int64 {
	if v == nil {
		return nil
	}
	n := int64(*v)
	return &n
}

func text(s *string) *contentv1.Text {
	if s == nil || *s == "" {
		return nil
	}
	return &contentv1.Text{Text: *s}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// links are links read from a channel's About panel or a description.
func links(ls []storedLink, src contentv1.LinkSource) []*contentv1.Link {
	var out []*contentv1.Link
	for _, l := range ls {
		if l.URL == "" {
			continue
		}
		u := l.URL
		if !strings.Contains(u, "://") {
			u = "https://" + u // YouTube shows links without the scheme
		}
		link := &contentv1.Link{Url: u, Title: l.Title, DisplayUrl: l.URL, Source: src}
		if p, err := url.Parse(u); err == nil {
			link.Domain = strings.TrimPrefix(strings.ToLower(p.Hostname()), "www.")
		}
		out = append(out, link)
	}
	return out
}

// crawlMode is the contract's crawl mode of a yts job kind.
func crawlMode(jobKind string) contentv1.CrawlMode {
	switch jobKind {
	case "":
		return contentv1.CrawlMode_CRAWL_MODE_UNSPECIFIED
	case "backfill":
		return contentv1.CrawlMode_CRAWL_MODE_BACKFILL
	case "sweep":
		return contentv1.CrawlMode_CRAWL_MODE_FULL
	case "comments", "post_comments", "tabs", "rss", "websub", "posts":
		return contentv1.CrawlMode_CRAWL_MODE_INCREMENTAL
	default: // channel, video, live, playlists, enrich, resolve…
		return contentv1.CrawlMode_CRAWL_MODE_REFRESH
	}
}
