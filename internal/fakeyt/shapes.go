package fakeyt

import (
	"fmt"
	"strings"
)

// Builders for generated responses in the shapes the parsers read (see
// internal/ytparse). Everything is invented: fake IDs, invented text.

// M and A shorten nested literals.
type (
	M = map[string]any
	A = []any
)

// Token builds a "load more" item.
func Token(tok string) M {
	return M{"continuationItemRenderer": M{"continuationEndpoint": M{"continuationCommand": M{"token": tok}}}}
}

// FakeChannel describes a generated channel.
type FakeChannel struct {
	ID          string // must start with UCfake
	Handle      string // "@name"
	Title       string
	Country     string
	Subscribers string // display text, e.g. "1.2K subscribers"
	Videos      []FakeVideo
	Posts       []FakePost
	Playlists   []FakePlaylist
}

// FakePost is a community post; its comments are served like a video's.
type FakePost struct {
	ID          string
	Text        string
	Ago         string
	Likes       string
	Comments    []FakeComment
	CommentsTxt string
}

// FakePlaylist is a playlist and the IDs of its videos.
type FakePlaylist struct {
	ID       string
	Title    string
	VideoIDs []string
}

// FakeVideo describes a generated upload.
type FakeVideo struct {
	ID          string
	Title       string
	Views       int64
	Likes       int64
	Ago         string // "3 days ago"
	LengthSec   int
	Short       bool
	Comments    []FakeComment
	CommentsTxt string // displayed count, e.g. "12"
	PublishDate string // player publishDate (RFC 3339); a fixed date when empty
}

// FakeComment is a generated comment.
type FakeComment struct {
	ID      string
	Text    string
	Author  string // UCfake… ID
	Ago     string
	Likes   string
	Pinned  bool
	Hearted bool
	Replies []FakeComment
}

// ChannelBrowse is the browse response for a channel ID.
func ChannelBrowse(c FakeChannel) M {
	tab := func(title, key string) M {
		return M{"tabRenderer": M{"title": title, "endpoint": M{
			"browseEndpoint":  M{"browseId": c.ID, "params": "TAB_" + key},
			"commandMetadata": M{"webCommandMetadata": M{"url": "/" + c.Handle + "/" + key}},
		}}}
	}
	return M{
		"metadata": M{"channelMetadataRenderer": M{"externalId": c.ID, "title": c.Title, "description": "Generated channel",
			"vanityChannelUrl": "http://www.youtube.com/" + c.Handle,
			"avatar":           M{"thumbnails": A{M{"url": "https://img.example/" + c.ID, "width": 900}}}}},
		"header": channelHeader(c),
		"contents": M{"twoColumnBrowseResultsRenderer": M{"tabs": A{tab("Videos", "videos"), tab("Shorts", "shorts"), tab("Posts", "posts"),
			tab("Playlists", "playlists")}}},
	}
}

// PostsTab is the Posts tab, in the 2026 layout (the reply button opens the
// post's page through browseId FEpost_detail).
func PostsTab(c FakeChannel) M {
	var items A
	for _, p := range c.Posts {
		items = append(items, M{"backstagePostThreadRenderer": M{"post": M{"backstagePostRenderer": M{
			"postId":            p.ID,
			"authorEndpoint":    M{"browseEndpoint": M{"browseId": c.ID}},
			"contentText":       M{"runs": A{M{"text": p.Text}}},
			"publishedTimeText": M{"runs": A{M{"text": p.Ago}}},
			"voteCount":         M{"simpleText": p.Likes},
			"replyButton": M{"buttonRenderer": M{"text": M{"simpleText": p.CommentsTxt},
				"navigationEndpoint": M{"browseEndpoint": M{"browseId": "FEpost_detail", "params": "POST_" + p.ID}}}},
		}}}})
	}
	return M{"header": channelHeader(c), "contents": M{"twoColumnBrowseResultsRenderer": M{"tabs": A{M{"tabRenderer": M{
		"content": M{"sectionListRenderer": M{"contents": A{M{"itemSectionRenderer": M{"contents": items}}}}}}}}}}}
}

// PostPage is a post's own page: the post and its comment section.
func PostPage(p FakePost) M {
	return M{"contents": M{"sectionListRenderer": M{"contents": A{
		M{"itemSectionRenderer": M{"sectionIdentifier": "backstage-item-section", "contents": A{}}},
		M{"itemSectionRenderer": M{"sectionIdentifier": "comment-item-section", "contents": A{Token("COMMENTS_" + p.ID)}}},
	}}}}
}

// PlaylistsTab lists playlist cards (lockupViewModel, 2026 layout).
func PlaylistsTab(c FakeChannel) M {
	var items A
	for _, pl := range c.Playlists {
		items = append(items, M{"lockupViewModel": M{"contentId": pl.ID, "contentType": "LOCKUP_CONTENT_TYPE_PLAYLIST",
			"contentImage": M{"collectionThumbnailViewModel": M{"primaryThumbnail": M{"thumbnailViewModel": M{"overlays": A{
				M{"thumbnailOverlayBadgeViewModel": M{"thumbnailBadges": A{M{"thumbnailBadgeViewModel": M{"text": fmt.Sprintf("%d videos", len(pl.VideoIDs))}}}}}}}}}},
			"metadata": M{"lockupMetadataViewModel": M{"title": M{"content": pl.Title}}}}})
	}
	return M{"header": channelHeader(c), "contents": M{"twoColumnBrowseResultsRenderer": M{"tabs": A{M{"tabRenderer": M{
		"content": M{"richGridRenderer": M{"contents": items}}}}}}}}
}

// PlaylistPage is a playlist's page (browse "VL" + ID).
func PlaylistPage(pl FakePlaylist) M {
	var items A
	for i, id := range pl.VideoIDs {
		items = append(items, M{"playlistVideoRenderer": M{"videoId": id, "title": M{"runs": A{M{"text": "video " + id}}},
			"index": M{"simpleText": fmt.Sprint(i + 1)}}})
	}
	return M{"contents": M{"playlistVideoListRenderer": M{"contents": items}}}
}

// channelHeader is the page header YouTube sends with the channel page and
// with the first page of every tab. Its description opens the About panel
// through a "load more" item of its own, nested in lists as on the live site,
// so a parser that takes any "load more" item as the next page of a tab
// follows it by mistake.
func channelHeader(c FakeChannel) M {
	return M{"pageHeaderRenderer": M{"content": M{"pageHeaderViewModel": M{
		"metadata": M{"contentMetadataViewModel": M{"metadataRows": A{
			M{"metadataParts": A{M{"text": M{"content": c.Handle}}}},
			M{"metadataParts": A{M{"text": M{"content": c.Subscribers}}, M{"text": M{"content": fmt.Sprintf("%d videos", len(c.Videos))}}}},
		}}},
		"description": M{"descriptionPreviewViewModel": M{"rendererContext": M{"commandContext": M{"onTap": M{"innertubeCommand": M{"showEngagementPanelEndpoint": M{
			"engagementPanel": M{"engagementPanelSectionListRenderer": M{"content": M{"sectionListRenderer": M{"contents": A{
				M{"itemSectionRenderer": M{"contents": A{Token("ABOUT_" + c.ID)}}},
			}}}}},
		}}}}}}},
	}}}}
}

// About is the About-panel continuation response.
func About(c FakeChannel) M {
	return M{"onResponseReceivedEndpoints": A{M{"appendContinuationItemsAction": M{"continuationItems": A{
		M{"aboutChannelRenderer": M{"metadata": M{"aboutChannelViewModel": M{
			"channelId": c.ID, "description": "Generated channel", "country": c.Country,
			"joinedDateText": M{"content": "Joined Jan 1, 2020"}, "viewCountText": "1,000 views",
			"videoCountText": fmt.Sprintf("%d videos", len(c.Videos)), "subscriberCountText": c.Subscribers,
			"links": A{M{"channelExternalLinkViewModel": M{"title": M{"content": "Site"}, "link": M{"content": "example.com/" + c.Handle[1:]}}}},
		}}}},
	}}}}}
}

// Tab lists a channel's videos or shorts; pageSize items per page, page
// tokens "TABPAGE_<key>_<n>".
func Tab(c FakeChannel, shorts bool, page, pageSize int) M {
	var items A
	var list []FakeVideo
	for _, v := range c.Videos {
		if v.Short == shorts {
			list = append(list, v)
		}
	}
	start, end := page*pageSize, (page+1)*pageSize
	if end > len(list) {
		end = len(list)
	}
	for i := start; i < end; i++ {
		v := list[i]
		if shorts {
			items = append(items, M{"richItemRenderer": M{"content": M{"shortsLockupViewModel": M{
				"entityId": "shorts-shelf-item-" + v.ID, "onTap": M{"innertubeCommand": M{"reelWatchEndpoint": M{"videoId": v.ID}}},
				"overlayMetadata": M{"primaryText": M{"content": v.Title}, "secondaryText": M{"content": fmt.Sprintf("%d views", v.Views)}},
			}}}})
			continue
		}
		items = append(items, M{"richItemRenderer": M{"content": M{"lockupViewModel": M{
			"contentId": v.ID, "contentType": "LOCKUP_CONTENT_TYPE_VIDEO",
			"contentImage": M{"thumbnailViewModel": M{"overlays": A{M{"thumbnailOverlayBadgeViewModel": M{"thumbnailBadges": A{
				M{"thumbnailBadgeViewModel": M{"text": fmt.Sprintf("%d:%02d", v.LengthSec/60, v.LengthSec%60)}}}}}}}},
			"metadata": M{"lockupMetadataViewModel": M{"title": M{"content": v.Title}, "metadata": M{"contentMetadataViewModel": M{"metadataRows": A{
				M{"metadataParts": A{viewsPart(v.Views), M{"text": M{"content": shortAgo(v.Ago)}, "accessibilityLabel": v.Ago}}},
			}}}}},
		}}}})
	}
	key := "videos"
	if shorts {
		key = "shorts"
	}
	if end < len(list) {
		items = append(items, Token(fmt.Sprintf("TABPAGE_%s_%s_%d", c.ID, key, page+1)))
	}
	if page == 0 {
		return M{
			"header":   channelHeader(c),
			"contents": M{"twoColumnBrowseResultsRenderer": M{"tabs": A{M{"tabRenderer": M{"selected": true, "content": M{"richGridRenderer": M{"contents": items}}}}}}},
		}
	}
	return M{"onResponseReceivedActions": A{M{"appendContinuationItemsAction": M{"continuationItems": items}}}}
}

// viewsPart is a grid view count as the live site sends it: short text
// ("1.2K") with a spelled-out screen-reader label ("1.2 thousand views").
func viewsPart(n int64) M {
	text, label := fmt.Sprint(n), fmt.Sprintf("%d views", n)
	switch {
	case n >= 10000:
		text, label = fmt.Sprintf("%dK", n/1000), fmt.Sprintf("%d thousand views", n/1000)
	case n >= 1000:
		text, label = fmt.Sprintf("%.1fK", float64(n/100)/10), fmt.Sprintf("%.1f thousand views", float64(n/100)/10)
	}
	return M{"text": M{"content": text}, "accessibilityLabel": label}
}

// shortAgo turns "3 days ago" into the grid's short form "3d ago".
func shortAgo(s string) string {
	f := strings.Fields(s)
	if len(f) == 3 && f[2] == "ago" {
		ab := map[string]string{"second": "s", "minute": "m", "hour": "h", "day": "d", "week": "w", "month": "mo", "year": "y"}[strings.TrimSuffix(f[1], "s")]
		if ab != "" {
			return f[0] + ab + " ago"
		}
	}
	return s
}

// Watch is the "next" response for a video.
func Watch(c FakeChannel, v FakeVideo) M {
	return M{
		"currentVideoEndpoint": M{"watchEndpoint": M{"videoId": v.ID}},
		"contents": M{"twoColumnWatchNextResults": M{"results": M{"results": M{"contents": A{
			M{"videoPrimaryInfoRenderer": M{"title": M{"runs": A{M{"text": v.Title}}}, "dateText": M{"simpleText": "Oct 3, 2026"},
				"viewCount": M{"videoViewCountRenderer": M{"viewCount": M{"simpleText": fmt.Sprintf("%d views", v.Views)}, "originalViewCount": fmt.Sprint(v.Views)}},
				"videoActions": M{"menuRenderer": M{"topLevelButtons": A{M{"segmentedLikeDislikeButtonViewModel": M{"likeButtonViewModel": M{"likeButtonViewModel": M{"toggleButtonViewModel": M{"toggleButtonViewModel": M{"defaultButtonViewModel": M{"buttonViewModel": M{
					"title": fmt.Sprint(v.Likes), "accessibilityText": fmt.Sprintf("like this video along with %d other people", v.Likes),
				}}}}}}}}}}}}},
			M{"videoSecondaryInfoRenderer": M{"owner": M{"videoOwnerRenderer": M{"title": M{"runs": A{M{"text": c.Title}}}, "navigationEndpoint": M{"browseEndpoint": M{"browseId": c.ID}}}},
				"attributedDescription": M{"content": "Description of " + v.Title}}},
			M{"itemSectionRenderer": M{"sectionIdentifier": "comment-item-section", "contents": A{Token("COMMENTS_" + v.ID)}}},
		}}}}},
		"engagementPanels": A{M{"engagementPanelSectionListRenderer": M{"panelIdentifier": "engagement-panel-comments-section",
			"header": M{"engagementPanelTitleHeaderRenderer": M{"contextualInfo": M{"runs": A{M{"text": v.CommentsTxt}}}}}}}},
	}
}

func publishDate(v FakeVideo) string {
	if v.PublishDate != "" {
		return v.PublishDate
	}
	return "2026-10-03T08:00:12-07:00"
}

// Player is the "player" response for a video (no stream data).
func Player(c FakeChannel, v FakeVideo) M {
	return M{
		"playabilityStatus": M{"status": "OK"},
		"videoDetails": M{"videoId": v.ID, "title": v.Title, "lengthSeconds": fmt.Sprint(v.LengthSec), "keywords": A{"fake", "tag"},
			"channelId": c.ID, "shortDescription": "Description of " + v.Title, "viewCount": fmt.Sprint(v.Views)},
		"microformat": M{"playerMicroformatRenderer": M{"category": "News & Politics", "publishDate": publishDate(v), "uploadDate": publishDate(v), "isFamilySafe": true}},
	}
}

// CommentsHeader answers the first comments token: header with the sort
// menu, plus the first page in "top" order (which callers re-sort).
func CommentsHeader(v FakeVideo) M {
	return M{"onResponseReceivedEndpoints": A{M{"reloadContinuationItemsCommand": M{"continuationItems": A{M{"commentsHeaderRenderer": M{
		"countText": M{"runs": A{M{"text": v.CommentsTxt}, M{"text": " Comments"}}},
		"sortMenu": M{"sortFilterSubMenuRenderer": M{"subMenuItems": A{
			M{"title": "Top comments", "serviceEndpoint": M{"continuationCommand": M{"token": "TOP_" + v.ID}}},
			M{"title": "Newest first", "serviceEndpoint": M{"continuationCommand": M{"token": "NEWEST_" + v.ID + "_0"}}},
		}}},
	}}}}}}}
}

// CommentsPage returns page n (newest first) of top-level comments.
func CommentsPage(v FakeVideo, page, pageSize int) M {
	start, end := page*pageSize, (page+1)*pageSize
	if end > len(v.Comments) {
		end = len(v.Comments)
	}
	var items A
	var mutations A
	for i := start; i < end; i++ {
		cm := v.Comments[i]
		th := M{"commentViewModel": M{"commentViewModel": commentVM(cm)}}
		if len(cm.Replies) > 0 {
			th["replies"] = M{"commentRepliesRenderer": M{"contents": A{Token("REPLIES_" + v.ID + "_" + cm.ID)}}}
		}
		items = append(items, M{"commentThreadRenderer": th})
		mutations = append(mutations, commentMutations(cm, 0, len(cm.Replies))...)
	}
	if end < len(v.Comments) {
		items = append(items, Token(fmt.Sprintf("NEWEST_%s_%d", v.ID, page+1)))
	}
	return M{
		"onResponseReceivedEndpoints": A{M{"appendContinuationItemsAction": M{"continuationItems": items}}},
		"frameworkUpdates":            M{"entityBatchUpdate": M{"mutations": mutations}},
	}
}

// Replies returns all replies of one comment.
func Replies(v FakeVideo, parentID string) M {
	var items, mutations A
	for _, cm := range v.Comments {
		if cm.ID != parentID {
			continue
		}
		for _, r := range cm.Replies {
			items = append(items, M{"commentViewModel": commentVM(r)})
			mutations = append(mutations, commentMutations(r, 1, 0)...)
		}
	}
	return M{
		"onResponseReceivedEndpoints": A{M{"appendContinuationItemsAction": M{"continuationItems": items}}},
		"frameworkUpdates":            M{"entityBatchUpdate": M{"mutations": mutations}},
	}
}

func commentVM(cm FakeComment) M {
	vm := M{"commentId": cm.ID, "toolbarStateKey": "ts_" + cm.ID}
	if cm.Pinned {
		vm["pinnedText"] = "Pinned by creator"
	}
	return vm
}

func commentMutations(cm FakeComment, level, replies int) A {
	heart := "TOOLBAR_HEART_STATE_UNHEARTED"
	if cm.Hearted {
		heart = "TOOLBAR_HEART_STATE_HEARTED"
	}
	rc := ""
	if replies > 0 {
		rc = fmt.Sprint(replies)
	}
	return A{
		M{"payload": M{"commentEntityPayload": M{
			"properties": M{"commentId": cm.ID, "content": M{"content": cm.Text}, "publishedTime": cm.Ago, "replyLevel": level},
			"author":     M{"channelId": cm.Author, "displayName": "@" + strings.ToLower(cm.Author[len(cm.Author)-6:])},
			"toolbar":    M{"likeCountNotliked": cm.Likes, "replyCount": rc, "heartActiveTooltip": "liked by the creator"},
		}}},
		M{"payload": M{"engagementToolbarStateEntityPayload": M{"key": "ts_" + cm.ID, "heartState": heart}}},
	}
}

// ServeChannels wires browse/next/player handlers on s for the given
// channels, with tab pages of tabPageSize and comment pages of
// commentPageSize.
func (s *Server) ServeChannels(chans []FakeChannel, tabPageSize, commentPageSize int) {
	byID := map[string]FakeChannel{}
	byHandle := map[string]FakeChannel{}
	videos := map[string]struct {
		c FakeChannel
		v FakeVideo
	}{}
	posts := map[string]FakePost{}
	playlists := map[string]FakePlaylist{}
	for _, c := range chans {
		byID[c.ID] = c
		byHandle[c.Handle] = c
		for _, p := range c.Posts {
			posts[p.ID] = p
		}
		for _, pl := range c.Playlists {
			playlists[pl.ID] = pl
		}
		// The channel page names its own ID, as YouTube's does.
		s.SetPage("/"+c.Handle, fmt.Sprintf(`<html><head><link rel="canonical" href="https://www.youtube.com/channel/%s"></head>`+
			`<body><script>var ytInitialData = {"metadata":{"channelMetadataRenderer":{"externalId":"%s"}}};</script></body></html>`, c.ID, c.ID))
		for _, v := range c.Videos {
			videos[v.ID] = struct {
				c FakeChannel
				v FakeVideo
			}{c, v}
		}
		for _, p := range c.Posts { // a post's comments are served like a video's
			videos[p.ID] = struct {
				c FakeChannel
				v FakeVideo
			}{c, FakeVideo{ID: p.ID, Comments: p.Comments, CommentsTxt: p.CommentsTxt}}
		}
	}
	notFound := func() (int, any) { return 404, M{"error": M{"code": 404}} }
	s.Handle("navigation/resolve_url", func(body map[string]any) (int, any) {
		u, _ := body["url"].(string)
		h := u[strings.LastIndex(u, "/")+1:]
		c, ok := byHandle[h]
		if !ok {
			return notFound()
		}
		return 200, M{"endpoint": M{"browseEndpoint": M{"browseId": c.ID}}}
	})
	s.Handle("browse", func(body map[string]any) (int, any) {
		if tok, _ := body["continuation"].(string); tok != "" {
			switch {
			case strings.HasPrefix(tok, "ABOUT_"):
				if c, ok := byID[strings.TrimPrefix(tok, "ABOUT_")]; ok {
					return 200, About(c)
				}
			case strings.HasPrefix(tok, "TABPAGE_"):
				var id, key string
				var page int
				parts := strings.Split(strings.TrimPrefix(tok, "TABPAGE_"), "_")
				if len(parts) == 3 {
					id, key = parts[0], parts[1]
					fmt.Sscan(parts[2], &page)
					if c, ok := byID[id]; ok {
						return 200, Tab(c, key == "shorts", page, tabPageSize)
					}
				}
			}
			return notFound()
		}
		id, _ := body["browseId"].(string)
		if id == "FEpost_detail" {
			params, _ := body["params"].(string)
			if p, ok := posts[strings.TrimPrefix(params, "POST_")]; ok {
				return 200, PostPage(p)
			}
			return notFound()
		}
		if pl, ok := playlists[strings.TrimPrefix(id, "VL")]; ok && strings.HasPrefix(id, "VL") {
			return 200, PlaylistPage(pl)
		}
		c, ok := byID[id]
		if !ok {
			return notFound()
		}
		switch params, _ := body["params"].(string); params {
		case "TAB_videos":
			return 200, Tab(c, false, 0, tabPageSize)
		case "TAB_shorts":
			return 200, Tab(c, true, 0, tabPageSize)
		case "TAB_posts":
			return 200, PostsTab(c)
		case "TAB_playlists":
			return 200, PlaylistsTab(c)
		}
		return 200, ChannelBrowse(c)
	})
	s.Handle("next", func(body map[string]any) (int, any) {
		if tok, _ := body["continuation"].(string); tok != "" {
			parts := strings.Split(tok, "_")
			switch parts[0] {
			case "COMMENTS", "TOP":
				if cv, ok := videos[parts[1]]; ok {
					return 200, CommentsHeader(cv.v)
				}
			case "NEWEST":
				if cv, ok := videos[parts[1]]; ok && len(parts) == 3 {
					var page int
					fmt.Sscan(parts[2], &page)
					return 200, CommentsPage(cv.v, page, commentPageSize)
				}
			case "REPLIES":
				if cv, ok := videos[parts[1]]; ok && len(parts) == 3 {
					return 200, Replies(cv.v, parts[2])
				}
			}
			return notFound()
		}
		id, _ := body["videoId"].(string)
		cv, ok := videos[id]
		if !ok {
			return notFound()
		}
		return 200, Watch(cv.c, cv.v)
	})
	s.Handle("player", func(body map[string]any) (int, any) {
		id, _ := body["videoId"].(string)
		cv, ok := videos[id]
		if !ok {
			return notFound()
		}
		return 200, Player(cv.c, cv.v)
	})
}
