package ytparse

import (
	"strings"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/jsonx"
)

// Chapter is one video chapter (creator-written or automatic).
type Chapter struct {
	Title    string
	StartSec int
}

// WatchInfo is what the "next" (watch page) response tells us.
type WatchInfo struct {
	VideoID       string
	Title         string
	Description   string
	ChannelID     string
	ChannelName   string
	Views         Count
	Likes         Count
	DateText      string // "Oct 3, 2026" / "Streamed live on …"; kept as text
	Chapters      []Chapter
	CommentsToken string // opens the comment section
	CommentCount  Count
	LiveViewers   Count // "1,234 watching now" while live
}

// ParseWatch reads a "next" response for a video.
func ParseWatch(data any) WatchInfo {
	var w WatchInfo
	w.VideoID = jsonx.String(data, "currentVideoEndpoint", "watchEndpoint", "videoId")

	if p := jsonx.First(data, "videoPrimaryInfoRenderer"); p != nil {
		w.Title = jsonx.TextAt(p, "title")
		w.DateText = jsonx.TextAt(p, "dateText")
		vc := jsonx.First(p, "videoViewCountRenderer")
		if n, ok := jsonx.Int(vc, "originalViewCount"); ok && n > 0 {
			w.Views = Count{Min: n, Max: n + 1, Exact: true, OK: true, Text: jsonx.String(vc, "originalViewCount")}
		} else {
			txt := jsonx.TextAt(vc, "viewCount")
			if jsonx.Bool(vc, "isLive") || strings.Contains(strings.ToLower(txt), "watching") || strings.Contains(txt, "يشاهد") {
				w.LiveViewers = ParseCount(txt)
			} else {
				w.Views = ParseCount(txt)
			}
		}
	}
	if s := jsonx.First(data, "videoSecondaryInfoRenderer"); s != nil {
		owner := jsonx.First(s, "videoOwnerRenderer")
		w.ChannelID = jsonx.String(jsonx.First(owner, "browseEndpoint"), "browseId")
		w.ChannelName = jsonx.TextAt(owner, "title")
		w.Description = firstNonEmpty(jsonx.String(s, "attributedDescription", "content"), jsonx.TextAt(s, "description"))
	}

	w.Likes = parseLikes(data)
	w.Chapters = parseChapters(data)

	// Comment section: an itemSectionRenderer marked "comment-item-section".
	for _, sec := range jsonx.FindMaps(data, "itemSectionRenderer") {
		if jsonx.String(sec, "sectionIdentifier") == "comment-item-section" {
			if t := firstToken(sec); t != "" {
				w.CommentsToken = t
				break
			}
		}
	}
	if h := jsonx.First(data, "commentsEntryPointHeaderRenderer"); h != nil {
		w.CommentCount = ParseCount(jsonx.TextAt(h, "commentCount"))
	}
	if !w.CommentCount.OK {
		for _, ep := range jsonx.FindMaps(data, "engagementPanelSectionListRenderer") {
			if jsonx.String(ep, "panelIdentifier") == "engagement-panel-comments-section" {
				w.CommentCount = ParseCount(jsonx.TextAt(jsonx.First(ep, "engagementPanelTitleHeaderRenderer"), "contextualInfo"))
			}
		}
	}
	return w
}

// parseLikes prefers exact numbers (structured-description factoids and the
// like button's accessibility label) over the abbreviated button title.
func parseLikes(data any) Count {
	for _, f := range jsonx.FindMaps(data, "factoidRenderer") {
		label := strings.ToLower(jsonx.TextAt(f, "label"))
		if strings.Contains(label, "like") || strings.Contains(label, "إعجاب") || strings.Contains(label, "اعجاب") {
			if c := ParseCount(jsonx.TextAt(f, "value")); c.OK {
				return c
			}
		}
	}
	for _, lb := range jsonx.FindMaps(data, "likeButtonViewModel") {
		bvm := jsonx.First(lb, "buttonViewModel")
		if n, ok := FirstNumber(jsonx.String(bvm, "accessibilityText")); ok {
			return Count{Min: n, Max: n + 1, Exact: true, OK: true, Text: jsonx.String(bvm, "accessibilityText")}
		}
		if c := ParseCount(jsonx.String(bvm, "title")); c.OK {
			return c
		}
	}
	return Count{}
}

func parseChapters(data any) []Chapter {
	var out []Chapter
	for _, c := range jsonx.FindMaps(data, "chapterRenderer") {
		ms, _ := jsonx.Int(c, "timeRangeStartMillis")
		out = append(out, Chapter{Title: jsonx.TextAt(c, "title"), StartSec: int(ms / 1000)})
	}
	if len(out) > 0 {
		return out
	}
	seen := map[int]bool{}
	for _, m := range jsonx.FindMaps(data, "macroMarkersListItemRenderer") {
		sec, ok := ParseClock(jsonx.TextAt(m, "timeDescription"))
		if !ok || seen[sec] {
			continue
		}
		seen[sec] = true
		out = append(out, Chapter{Title: jsonx.TextAt(m, "title"), StartSec: sec})
	}
	return out
}

// PlayerInfo is what the "player" response tells us (without any stream
// URLs: the collector never downloads media, plan §2).
type PlayerInfo struct {
	VideoID      string
	Title        string
	ChannelID    string
	Description  string
	LengthSec    int
	Keywords     []string // tags
	Views        int64
	IsLive       bool // live now
	WasLive      bool // a live stream (now or in the past)
	Category     string
	PublishDate  string // as given (date or date-time)
	UploadDate   string
	FamilySafe   bool
	CaptionLangs []string
	Playability  string // "OK", "LOGIN_REQUIRED", "UNPLAYABLE", "ERROR"…
	// CaptionTracks are the caption files the player offers. Their URLs
	// carry no anti-bot token; the website's player adds one, and we never
	// generate one (plan §3), so whether they answer without it is what
	// Phase 0 experiment E12 measures.
	CaptionTracks []CaptionTrack
}

// CaptionTrack is one caption file the player offers.
type CaptionTrack struct {
	Lang string
	Auto bool   // automatic speech recognition
	URL  string // timedtext URL
}

// ParsePlayer reads a "player" response.
func ParsePlayer(data any) PlayerInfo {
	vd := jsonx.Map(data, "videoDetails")
	mf := jsonx.Map(data, "microformat", "playerMicroformatRenderer")
	p := PlayerInfo{
		VideoID:     jsonx.String(vd, "videoId"),
		Title:       jsonx.String(vd, "title"),
		ChannelID:   jsonx.String(vd, "channelId"),
		Description: jsonx.String(vd, "shortDescription"),
		IsLive:      jsonx.Bool(vd, "isLive") || jsonx.Bool(mf, "liveBroadcastDetails", "isLiveNow"),
		WasLive:     jsonx.Bool(vd, "isLiveContent"),
		Category:    jsonx.String(mf, "category"),
		PublishDate: jsonx.String(mf, "publishDate"),
		UploadDate:  jsonx.String(mf, "uploadDate"),
		FamilySafe:  jsonx.Bool(mf, "isFamilySafe"),
		Playability: jsonx.String(data, "playabilityStatus", "status"),
	}
	if n, ok := jsonx.Int(vd, "lengthSeconds"); ok {
		p.LengthSec = int(n)
	}
	if n, ok := jsonx.Int(vd, "viewCount"); ok {
		p.Views = n
	}
	for _, k := range jsonx.Slice(vd, "keywords") {
		if s, ok := k.(string); ok {
			p.Keywords = append(p.Keywords, s)
		}
	}
	for _, tr := range jsonx.Slice(data, "captions", "playerCaptionsTracklistRenderer", "captionTracks") {
		lang := jsonx.String(tr, "languageCode")
		auto := jsonx.String(tr, "kind") == "asr"
		p.CaptionTracks = append(p.CaptionTracks, CaptionTrack{Lang: lang, Auto: auto, URL: jsonx.String(tr, "baseUrl")})
		if auto {
			lang += " (auto)"
		}
		p.CaptionLangs = append(p.CaptionLangs, lang)
	}
	return p
}

// TranscriptParams returns the parameters of the watch page's "show
// transcript" request, or "" when the page offers none.
func TranscriptParams(watch any) string {
	for _, ep := range jsonx.FindMaps(watch, "getTranscriptEndpoint") {
		if p := jsonx.String(ep, "params"); p != "" {
			return p
		}
	}
	return ""
}

// TranscriptSegments counts the lines in a get_transcript response (older
// and newer layouts).
func TranscriptSegments(data any) int {
	n := len(jsonx.FindMaps(data, "transcriptSegmentRenderer"))
	if n == 0 {
		n = len(jsonx.FindMaps(data, "transcriptCueGroupRenderer"))
	}
	return n
}

// CaptionCues counts the cues in a timedtext response: JSON ("json3",
// events with text segments) or XML (<text> or <p> elements).
func CaptionCues(body []byte) int {
	if d, err := jsonx.Decode(body); err == nil {
		n := 0
		for _, ev := range jsonx.Slice(d, "events") {
			if len(jsonx.Slice(ev, "segs")) > 0 {
				n++
			}
		}
		return n
	}
	s := string(body)
	if n := strings.Count(s, "<text"); n > 0 {
		return n
	}
	return strings.Count(s, "<p ")
}
