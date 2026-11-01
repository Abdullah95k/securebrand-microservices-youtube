package ytparse

import (
	"regexp"
	"sort"
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
	// Metadata rows hold channel name, views and date. The grid shows them
	// in short form ("12K", "3d ago"); each part's screen-reader label spells
	// them out ("12 thousand views", "3 days ago"), so the label is read
	// first and the short text is the fallback.
	for _, parts := range jsonx.FindAll(jsonx.First(r, "lockupMetadataViewModel"), "metadataParts") {
		arr, _ := parts.([]any)
		for _, p := range arr {
			txt := jsonx.String(p, "text", "content")
			label := jsonx.String(p, "accessibilityLabel")
			switch {
			case isViewsText(label) || isViewsText(txt) || countOnlyRe.MatchString(foldDigits(txt)):
				v.Views = ParseCount(firstNonEmpty(label, txt))
			case ParseRelativeTime(label, time.Now()).OK:
				v.PublishedText = label
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

// countOnlyRe matches a bare count such as "12K", "1.2M" or "345": the
// grid's short form of a view count.
var countOnlyRe = regexp.MustCompile(`^\s*\d[\d,]*(?:\.\d+)?\s*(?:[KMBkmb]|ألف|مليون|مليار)?\s*$`)

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

// notTheList names subtrees whose "load more" items do not continue the list
// being read: the channel header (its description opens the About panel
// with one), engagement panels, the top bar and entity updates.
var notTheList = map[string]bool{
	"header": true, "engagementPanels": true, "showEngagementPanelEndpoint": true,
	"topbar": true, "frameworkUpdates": true,
}

// lastContinuation returns the "load more" token of the list a channel tab,
// search or continuation response holds. The token must sit in a list next
// to at least one item, outside the subtrees in notTheList; when several
// lists qualify, the one with the most items wins. Tokens that open a panel
// or a comment's replies sit alone in their list and never win.
//
// The live crawl of 1 November 2026 shows why: every tab's first page also
// carries the channel header, whose About-panel token came last in the
// response, so the "next page" fetched was the About panel and held nothing.
func lastContinuation(data any) string {
	token, best := "", 0
	var walk func(any)
	walk = func(n any) {
		switch t := n.(type) {
		case map[string]any:
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if !notTheList[k] {
					walk(t[k])
				}
			}
		case []any:
			tok, items := "", 0
			for _, e := range t {
				if cir := jsonx.Map(e, "continuationItemRenderer"); cir != nil {
					if x := firstToken(cir); x != "" {
						tok = x
					}
					continue
				}
				items++
				walk(e)
			}
			if tok != "" && items > best {
				token, best = tok, items
			}
		}
	}
	walk(data)
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
