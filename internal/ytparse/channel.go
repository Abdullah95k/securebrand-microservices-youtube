package ytparse

import (
	"strings"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/jsonx"
)

// Tab is a channel tab (videos, shorts, streams, posts, playlists…).
type Tab struct {
	Key    string // last URL segment: "videos", "shorts", "streams", "posts", "community", "playlists"…
	Title  string
	Params string // browse params that open the tab
}

// Channel is what a channel's main page tells us.
type Channel struct {
	ID          string
	Title       string
	Handle      string // "@name" when known
	Description string
	AvatarURL   string
	BannerURL   string
	Keywords    string
	FamilySafe  bool
	Subscribers Count
	VideoCount  Count
	Tabs        map[string]Tab
	AboutToken  string // continuation that opens the About panel
}

// ParseChannel reads a browse response for a channel ID.
func ParseChannel(data any) Channel {
	var c Channel
	md := jsonx.Map(data, "metadata", "channelMetadataRenderer")
	c.ID = jsonx.String(md, "externalId")
	c.Title = jsonx.String(md, "title")
	c.Description = jsonx.String(md, "description")
	c.Keywords = jsonx.String(md, "keywords")
	c.FamilySafe = jsonx.Bool(md, "isFamilySafe")
	c.AvatarURL = largestThumb(jsonx.Get(md, "avatar"))
	if v := jsonx.String(md, "vanityChannelUrl"); v != "" {
		if i := strings.LastIndex(v, "/@"); i >= 0 {
			c.Handle = v[i+1:]
		}
	}

	header := jsonx.Map(data, "header")
	if old := jsonx.Map(header, "c4TabbedHeaderRenderer"); old != nil {
		if c.ID == "" {
			c.ID = jsonx.String(old, "channelId")
		}
		c.BannerURL = largestThumb(jsonx.Get(old, "banner"))
		c.Subscribers = ParseCount(jsonx.TextAt(old, "subscriberCountText"))
		c.VideoCount = ParseCount(jsonx.TextAt(old, "videosCountText"))
		if h := jsonx.TextAt(old, "channelHandleText"); strings.HasPrefix(h, "@") && c.Handle == "" {
			c.Handle = h
		}
	}
	if vm := jsonx.First(header, "pageHeaderViewModel"); vm != nil {
		if c.BannerURL == "" {
			c.BannerURL = largestSource(jsonx.First(jsonx.First(vm, "imageBannerViewModel"), "sources"))
		}
		for _, parts := range jsonx.FindAll(jsonx.First(vm, "contentMetadataViewModel"), "metadataParts") {
			arr, _ := parts.([]any)
			for _, p := range arr {
				txt := jsonx.String(p, "text", "content")
				switch {
				case strings.HasPrefix(txt, "@"):
					if c.Handle == "" {
						c.Handle = txt
					}
				case isSubscriberText(txt):
					c.Subscribers = ParseCount(txt)
				case isVideoCountText(txt):
					c.VideoCount = ParseCount(txt)
				}
			}
		}
		// The About panel opens from the description preview.
		if desc := jsonx.First(vm, "descriptionPreviewViewModel"); desc != nil {
			c.AboutToken = firstToken(desc)
		}
		if c.AboutToken == "" {
			c.AboutToken = firstToken(vm)
		}
	}

	c.Tabs = map[string]Tab{}
	for _, tr := range jsonx.FindMaps(data, "tabRenderer") {
		url := jsonx.String(tr, "endpoint", "commandMetadata", "webCommandMetadata", "url")
		key := url[strings.LastIndex(url, "/")+1:]
		if key == "" || strings.HasPrefix(key, "@") {
			key = "home"
		}
		c.Tabs[key] = Tab{Key: key, Title: jsonx.String(tr, "title"), Params: jsonx.String(tr, "endpoint", "browseEndpoint", "params")}
	}
	return c
}

func isSubscriberText(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "subscriber") || strings.Contains(s, "مشترك")
}

func isVideoCountText(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "video") || strings.Contains(s, "فيديو")
}

// Link is one link on a channel's About panel.
type Link struct {
	Title string
	URL   string
}

// About is the channel's About panel.
type About struct {
	ChannelID   string
	Description string
	Country     string
	JoinedText  string // e.g. "Joined Jan 1, 2015"; kept as text
	Views       Count
	Videos      Count
	Subscribers Count
	Links       []Link
	URL         string
}

// ParseAbout reads the response to the About continuation.
func ParseAbout(data any) (About, bool) {
	vm := jsonx.First(data, "aboutChannelViewModel")
	if vm == nil {
		return About{}, false
	}
	a := About{
		ChannelID:   jsonx.String(vm, "channelId"),
		Description: jsonx.String(vm, "description"),
		Country:     jsonx.String(vm, "country"),
		JoinedText:  firstNonEmpty(jsonx.String(vm, "joinedDateText", "content"), jsonx.TextAt(vm, "joinedDateText")),
		Views:       ParseCount(jsonx.TextAt(vm, "viewCountText")),
		Videos:      ParseCount(jsonx.TextAt(vm, "videoCountText")),
		Subscribers: ParseCount(jsonx.TextAt(vm, "subscriberCountText")),
		URL:         jsonx.String(vm, "canonicalChannelUrl"),
	}
	for _, l := range jsonx.FindMaps(vm, "channelExternalLinkViewModel") {
		a.Links = append(a.Links, Link{
			Title: firstNonEmpty(jsonx.String(l, "title", "content"), jsonx.TextAt(l, "title")),
			URL:   firstNonEmpty(jsonx.String(l, "link", "content"), jsonx.TextAt(l, "link")),
		})
	}
	return a, true
}

// Kind is the type of an upload.
type Kind string

// Upload kinds.
const (
	KindVideo    Kind = "video"
	KindShort    Kind = "short"
	KindLive     Kind = "live"     // live now
	KindUpcoming Kind = "upcoming" // scheduled stream or premiere
)

// VideoItem is one entry of a list (channel tab, search results).
type VideoItem struct {
	VideoID       string
	Title         string
	ChannelID     string // empty on channel tabs (the channel is implied)
	ChannelName   string
	Views         Count
	PublishedText string
	Published     TimeEstimate
	DurationSec   int
	Kind          Kind
}

// List is a page of items plus the token for the next page.
type List struct {
	Videos    []VideoItem
	Channels  []ChannelItem
	NextToken string
}

// ChannelItem is a channel found in search results.
type ChannelItem struct {
	ChannelID   string
	Title       string
	Subscribers Count
}

// ParseList reads a channel tab, a search response or one of their
// continuations. now anchors relative dates. Duplicates are removed.
func ParseList(data any, now time.Time) List {
	var out List
	seen := map[string]bool{}
	add := func(v VideoItem) {
		if v.VideoID == "" || seen[v.VideoID] {
			return
		}
		seen[v.VideoID] = true
		if v.PublishedText != "" {
			v.Published = ParseRelativeTime(v.PublishedText, now)
		}
		out.Videos = append(out.Videos, v)
	}
	for _, key := range []string{"videoRenderer", "gridVideoRenderer", "compactVideoRenderer"} {
		for _, r := range jsonx.FindMaps(data, key) {
			add(fromVideoRenderer(r))
		}
	}
	for _, r := range jsonx.FindMaps(data, "lockupViewModel") {
		if t := jsonx.String(r, "contentType"); t != "" && !strings.Contains(t, "VIDEO") {
			continue
		}
		add(fromLockup(r))
	}
	for _, r := range jsonx.FindMaps(data, "shortsLockupViewModel") {
		add(VideoItem{
			VideoID: firstNonEmpty(jsonx.String(jsonx.First(r, "reelWatchEndpoint"), "videoId"), strings.TrimPrefix(jsonx.String(r, "entityId"), "shorts-shelf-item-")),
			Title:   jsonx.String(r, "overlayMetadata", "primaryText", "content"),
			Views:   ParseCount(jsonx.String(r, "overlayMetadata", "secondaryText", "content")),
			Kind:    KindShort,
		})
	}
	for _, r := range jsonx.FindMaps(data, "reelItemRenderer") {
		add(VideoItem{VideoID: jsonx.String(r, "videoId"), Title: jsonx.TextAt(r, "headline"), Views: ParseCount(jsonx.TextAt(r, "viewCountText")), Kind: KindShort})
	}
	for _, r := range jsonx.FindMaps(data, "channelRenderer") {
		out.Channels = append(out.Channels, ChannelItem{
			ChannelID:   jsonx.String(r, "channelId"),
			Title:       jsonx.TextAt(r, "title"),
			Subscribers: ParseCount(firstNonEmpty(jsonx.TextAt(r, "subscriberCountText"), jsonx.TextAt(r, "videoCountText"))),
		})
	}
	out.NextToken = lastContinuation(data)
	return out
}

func fromVideoRenderer(r map[string]any) VideoItem {
	v := VideoItem{
		VideoID:       jsonx.String(r, "videoId"),
		Title:         jsonx.TextAt(r, "title"),
		ChannelID:     jsonx.String(jsonx.First(jsonx.Get(r, "ownerText"), "browseEndpoint"), "browseId"),
		ChannelName:   jsonx.TextAt(r, "ownerText"),
		Views:         ParseCount(firstNonEmpty(jsonx.TextAt(r, "viewCountText"), jsonx.TextAt(r, "shortViewCountText"))),
		PublishedText: jsonx.TextAt(r, "publishedTimeText"),
		Kind:          KindVideo,
	}
	if d, ok := ParseClock(jsonx.TextAt(r, "lengthText")); ok {
		v.DurationSec = d
	}
	if jsonx.Get(r, "upcomingEventData") != nil {
		v.Kind = KindUpcoming
	}
	for _, o := range jsonx.FindMaps(r, "thumbnailOverlayTimeStatusRenderer") {
		switch jsonx.String(o, "style") {
		case "LIVE":
			v.Kind = KindLive
		case "SHORTS":
			v.Kind = KindShort
		case "UPCOMING":
			v.Kind = KindUpcoming
		}
	}
	return v
}

func fromLockup(r map[string]any) VideoItem {
	v := VideoItem{
		VideoID: jsonx.String(r, "contentId"),
		Title:   jsonx.String(jsonx.First(r, "lockupMetadataViewModel"), "title", "content"),
		Kind:    KindVideo,
	}
	// Metadata rows hold channel name, views and date in display text.
	for _, parts := range jsonx.FindAll(jsonx.First(r, "lockupMetadataViewModel"), "metadataParts") {
		arr, _ := parts.([]any)
		for _, p := range arr {
			txt := jsonx.String(p, "text", "content")
			switch {
			case isViewsText(txt):
				v.Views = ParseCount(txt)
			case ParseRelativeTime(txt, time.Now()).OK:
				v.PublishedText = txt
			case v.ChannelName == "" && txt != "":
				v.ChannelName = txt
			}
		}
	}
	if ep := jsonx.First(jsonx.First(r, "lockupMetadataViewModel"), "browseEndpoint"); ep != nil {
		v.ChannelID = jsonx.String(ep, "browseId")
	}
	for _, b := range jsonx.FindMaps(r, "thumbnailBadgeViewModel") {
		txt := jsonx.String(b, "text")
		if d, ok := ParseClock(txt); ok {
			v.DurationSec = d
		}
		switch jsonx.String(b, "badgeStyle") {
		case "THUMBNAIL_OVERLAY_BADGE_STYLE_LIVE":
			v.Kind = KindLive
		}
		if strings.EqualFold(txt, "LIVE") || txt == "مباشر" {
			v.Kind = KindLive
		}
	}
	return v
}

func isViewsText(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "view") || strings.Contains(s, "مشاهد") || strings.Contains(l, "watching") || strings.Contains(s, "يشاهد")
}

// firstToken returns the first continuation token under v.
func firstToken(v any) string {
	for _, cc := range jsonx.FindMaps(v, "continuationCommand") {
		if t := jsonx.String(cc, "token"); t != "" {
			return t
		}
	}
	return ""
}

// lastContinuation returns the token of the last "load more" item in a list
// response, skipping tokens nested inside comment threads (reply tokens).
func lastContinuation(data any) string {
	token := ""
	for _, arr := range append(jsonx.FindAll(data, "continuationItems"), append(jsonx.FindAll(data, "contents"), jsonx.FindAll(data, "items")...)...) {
		items, _ := arr.([]any)
		for _, it := range items {
			if cir := jsonx.Map(it, "continuationItemRenderer"); cir != nil {
				if t := firstToken(cir); t != "" {
					token = t
				}
			}
		}
	}
	return token
}

func largestThumb(v any) string {
	best, bestW := "", int64(-1)
	for _, th := range jsonx.FindAll(v, "thumbnails") {
		arr, _ := th.([]any)
		for _, t := range arr {
			w, _ := jsonx.Int(t, "width")
			if u := jsonx.String(t, "url"); u != "" && w > bestW {
				best, bestW = u, w
			}
		}
	}
	return best
}

func largestSource(v any) string {
	arr, _ := v.([]any)
	best, bestW := "", int64(-1)
	for _, s := range arr {
		w, _ := jsonx.Int(s, "width")
		if u := jsonx.String(s, "url"); u != "" && w > bestW {
			best, bestW = u, w
		}
	}
	return best
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
