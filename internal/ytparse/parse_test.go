package ytparse

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/jsonx"
)

// Fixtures are generated here from the response shapes the parsers expect
// (older "Renderer" and newer "ViewModel" layouts). They contain only fake
// IDs and invented text; nothing is captured from YouTube. Phase 0 checks
// these shapes against live responses (experiment E13).

type M = map[string]any
type A = []any

const (
	fakeCh     = "UCfakeCHANNELCHANNEL0001"
	fakeAuthor = "UCfakeAUTHORAUTHORAUTHOR"
)

var now = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func doc(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	d, err := jsonx.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func token(tok string) M {
	return M{"continuationItemRenderer": M{"continuationEndpoint": M{"continuationCommand": M{"token": tok}}}}
}

func tab(title, url, params string) M {
	return M{"tabRenderer": M{"title": title, "endpoint": M{
		"browseEndpoint":  M{"browseId": fakeCh, "params": params},
		"commandMetadata": M{"webCommandMetadata": M{"url": url}},
	}}}
}

func TestParseChannelNewLayout(t *testing.T) {
	d := doc(t, M{
		"metadata": M{"channelMetadataRenderer": M{
			"externalId": fakeCh, "title": "قناة تجريبية", "description": "وصف", "isFamilySafe": true,
			"vanityChannelUrl": "http://www.youtube.com/@fakechan",
			"avatar":           M{"thumbnails": A{M{"url": "https://img.example/a88", "width": 88}, M{"url": "https://img.example/a900", "width": 900}}},
		}},
		"header": M{"pageHeaderRenderer": M{"content": M{"pageHeaderViewModel": M{
			"banner": M{"imageBannerViewModel": M{"image": M{"sources": A{M{"url": "https://img.example/b1060", "width": 1060}, M{"url": "https://img.example/b2560", "width": 2560}}}}},
			"metadata": M{"contentMetadataViewModel": M{"metadataRows": A{
				M{"metadataParts": A{M{"text": M{"content": "@fakechan"}}}},
				M{"metadataParts": A{M{"text": M{"content": "1.2M subscribers"}}, M{"text": M{"content": "345 videos"}}}},
			}}},
			"description": M{"descriptionPreviewViewModel": M{"rendererContext": M{"commandContext": M{"onTap": M{"innertubeCommand": M{
				"showEngagementPanelEndpoint": M{"engagementPanel": M{"engagementPanelSectionListRenderer": M{"content": M{"sectionListRenderer": M{"contents": A{
					M{"itemSectionRenderer": M{"contents": A{token("ABOUT_TOKEN")}}},
				}}}}}},
			}}}}}},
		}}}},
		"contents": M{"twoColumnBrowseResultsRenderer": M{"tabs": A{
			tab("Home", "/@fakechan", "HOMEP"), tab("Videos", "/@fakechan/videos", "VIDP"),
			tab("Shorts", "/@fakechan/shorts", "SHP"), tab("Posts", "/@fakechan/posts", "POSTP"),
		}}},
	})
	c := ParseChannel(d)
	if c.ID != fakeCh || c.Title != "قناة تجريبية" || c.Handle != "@fakechan" || !c.FamilySafe {
		t.Errorf("basic fields: %+v", c)
	}
	if c.AvatarURL != "https://img.example/a900" || c.BannerURL != "https://img.example/b2560" {
		t.Errorf("images: avatar %q banner %q", c.AvatarURL, c.BannerURL)
	}
	if c.Subscribers.Min != 1200000 || c.VideoCount.Min != 345 {
		t.Errorf("counts: subs %+v videos %+v", c.Subscribers, c.VideoCount)
	}
	if c.AboutToken != "ABOUT_TOKEN" {
		t.Errorf("about token = %q", c.AboutToken)
	}
	if c.Tabs["videos"].Params != "VIDP" || c.Tabs["shorts"].Params != "SHP" || c.Tabs["posts"].Params != "POSTP" || c.Tabs["home"].Params != "HOMEP" {
		t.Errorf("tabs = %+v", c.Tabs)
	}
}

func TestParseChannelOldLayout(t *testing.T) {
	d := doc(t, M{"header": M{"c4TabbedHeaderRenderer": M{
		"channelId":           fakeCh,
		"banner":              M{"thumbnails": A{M{"url": "https://img.example/old", "width": 1060}}},
		"subscriberCountText": M{"simpleText": "١٢٬٣٤٥ مشترك"},
		"videosCountText":     M{"runs": A{M{"text": "12"}, M{"text": " videos"}}},
		"channelHandleText":   M{"runs": A{M{"text": "@oldchan"}}},
	}}})
	c := ParseChannel(d)
	if c.ID != fakeCh || c.Handle != "@oldchan" || c.Subscribers.Min != 12345 || !c.Subscribers.Exact || c.VideoCount.Min != 12 || c.BannerURL == "" {
		t.Errorf("old layout: %+v", c)
	}
}

func TestParseAbout(t *testing.T) {
	d := doc(t, M{"onResponseReceivedEndpoints": A{M{"appendContinuationItemsAction": M{"continuationItems": A{
		M{"aboutChannelRenderer": M{"metadata": M{"aboutChannelViewModel": M{
			"channelId": fakeCh, "description": "about text", "country": "Iraq",
			"joinedDateText": M{"content": "Joined Jan 1, 2015"}, "viewCountText": "123,456,789 views",
			"videoCountText": "345 videos", "subscriberCountText": "1.2M subscribers",
			"canonicalChannelUrl": "http://www.youtube.com/@fakechan",
			"links":               A{M{"channelExternalLinkViewModel": M{"title": M{"content": "Website"}, "link": M{"content": "example.com/fake"}}}},
		}}}},
	}}}}})
	a, ok := ParseAbout(d)
	if !ok {
		t.Fatal("about not found")
	}
	if a.Country != "Iraq" || a.JoinedText != "Joined Jan 1, 2015" || a.Views.Min != 123456789 || !a.Views.Exact || a.Videos.Min != 345 {
		t.Errorf("about: %+v", a)
	}
	if len(a.Links) != 1 || a.Links[0].Title != "Website" || a.Links[0].URL != "example.com/fake" {
		t.Errorf("links: %+v", a.Links)
	}
	if _, ok := ParseAbout(doc(t, M{})); ok {
		t.Error("empty response has no about panel")
	}
}

func TestParseListBothLayouts(t *testing.T) {
	d := doc(t, M{"contents": M{"richGridRenderer": M{"contents": A{
		M{"richItemRenderer": M{"content": M{"lockupViewModel": M{
			"contentId": "fakevid0001", "contentType": "LOCKUP_CONTENT_TYPE_VIDEO",
			"contentImage": M{"thumbnailViewModel": M{"overlays": A{M{"thumbnailOverlayBadgeViewModel": M{"thumbnailBadges": A{M{"thumbnailBadgeViewModel": M{"text": "12:34"}}}}}}}},
			"metadata": M{"lockupMetadataViewModel": M{"title": M{"content": "Title one"}, "metadata": M{"contentMetadataViewModel": M{"metadataRows": A{
				M{"metadataParts": A{M{"text": M{"content": "12K views"}}, M{"text": M{"content": "3 days ago"}}}},
			}}}}},
		}}}},
		M{"richItemRenderer": M{"content": M{"lockupViewModel": M{"contentId": "fakeplaylst", "contentType": "LOCKUP_CONTENT_TYPE_PLAYLIST"}}}},
		M{"richItemRenderer": M{"content": M{"shortsLockupViewModel": M{
			"entityId":        "shorts-shelf-item-fakeshort1",
			"onTap":           M{"innertubeCommand": M{"reelWatchEndpoint": M{"videoId": "fakeshort1"}}},
			"overlayMetadata": M{"primaryText": M{"content": "Short"}, "secondaryText": M{"content": "1.2M views"}},
		}}}},
		M{"richItemRenderer": M{"content": M{"videoRenderer": M{
			"videoId": "fakevid0002", "title": M{"runs": A{M{"text": "Old "}, M{"text": "layout"}}},
			"viewCountText": M{"simpleText": "1,234 views"}, "publishedTimeText": M{"simpleText": "منذ يومين"},
			"lengthText":        M{"simpleText": "1:02:03"},
			"ownerText":         M{"runs": A{M{"text": "Owner", "navigationEndpoint": M{"browseEndpoint": M{"browseId": fakeCh}}}}},
			"thumbnailOverlays": A{M{"thumbnailOverlayTimeStatusRenderer": M{"style": "LIVE"}}},
		}}}},
		M{"channelRenderer": M{"channelId": fakeAuthor, "title": M{"simpleText": "Found channel"}, "subscriberCountText": M{"simpleText": "3.4K subscribers"}}},
		token("NEXT1"),
	}}}})
	l := ParseList(d, now)
	if len(l.Videos) != 3 {
		t.Fatalf("videos = %+v", l.Videos)
	}
	byID := map[string]VideoItem{}
	for _, v := range l.Videos {
		byID[v.VideoID] = v
	}
	v1 := byID["fakevid0001"]
	if v1.Title != "Title one" || v1.Views.Min != 12000 || v1.DurationSec != 754 || v1.Kind != KindVideo || now.Sub(v1.Published.At) != 3*day {
		t.Errorf("lockup item: %+v", v1)
	}
	if s := byID["fakeshort1"]; s.Kind != KindShort || s.Title != "Short" || s.Views.Min != 1200000 {
		t.Errorf("short: %+v", s)
	}
	v2 := byID["fakevid0002"]
	if v2.Title != "Old layout" || v2.Views.Min != 1234 || v2.DurationSec != 3723 || v2.Kind != KindLive || v2.ChannelID != fakeCh || now.Sub(v2.Published.At) != 2*day {
		t.Errorf("old item: %+v", v2)
	}
	if len(l.Channels) != 1 || l.Channels[0].ChannelID != fakeAuthor || l.Channels[0].Subscribers.Min != 3400 {
		t.Errorf("channels: %+v", l.Channels)
	}
	if l.NextToken != "NEXT1" {
		t.Errorf("next token = %q", l.NextToken)
	}
}

func watchDoc(t *testing.T, live bool) any {
	vc := M{"viewCount": M{"simpleText": "12,345 views"}, "originalViewCount": "12345"}
	if live {
		vc = M{"viewCount": M{"runs": A{M{"text": "1,234"}, M{"text": " watching now"}}}, "isLive": true}
	}
	return doc(t, M{
		"currentVideoEndpoint": M{"watchEndpoint": M{"videoId": "fakevid0001"}},
		"contents": M{"twoColumnWatchNextResults": M{"results": M{"results": M{"contents": A{
			M{"videoPrimaryInfoRenderer": M{"title": M{"runs": A{M{"text": "عنوان"}}}, "viewCount": M{"videoViewCountRenderer": vc}, "dateText": M{"simpleText": "Oct 3, 2026"},
				"videoActions": M{"menuRenderer": M{"topLevelButtons": A{M{"segmentedLikeDislikeButtonViewModel": M{"likeButtonViewModel": M{"likeButtonViewModel": M{"toggleButtonViewModel": M{"toggleButtonViewModel": M{"defaultButtonViewModel": M{"buttonViewModel": M{
					"title": "1.2K", "accessibilityText": "like this video along with 1,234 other people",
				}}}}}}}}}}}}},
			M{"videoSecondaryInfoRenderer": M{"owner": M{"videoOwnerRenderer": M{"title": M{"runs": A{M{"text": "Channel"}}}, "navigationEndpoint": M{"browseEndpoint": M{"browseId": fakeCh}}}}, "attributedDescription": M{"content": "0:00 Intro\n1:05 Part 2"}}},
			M{"itemSectionRenderer": M{"sectionIdentifier": "comment-item-section", "contents": A{token("COMMENTS_TOKEN")}}},
		}}}}},
		"engagementPanels": A{M{"engagementPanelSectionListRenderer": M{"panelIdentifier": "engagement-panel-comments-section",
			"header": M{"engagementPanelTitleHeaderRenderer": M{"contextualInfo": M{"runs": A{M{"text": "89"}}}}}}}},
		"playerOverlays": M{"playerOverlayRenderer": M{"decoratedPlayerBarRenderer": M{"decoratedPlayerBarRenderer": M{"playerBar": M{"multiMarkersPlayerBarRenderer": M{"markersMap": A{M{"value": M{"chapters": A{
			M{"chapterRenderer": M{"title": M{"simpleText": "Intro"}, "timeRangeStartMillis": 0}},
			M{"chapterRenderer": M{"title": M{"simpleText": "Part 2"}, "timeRangeStartMillis": 65000}},
		}}}}}}}}}},
	})
}

func TestParseWatch(t *testing.T) {
	w := ParseWatch(watchDoc(t, false))
	if w.VideoID != "fakevid0001" || w.Title != "عنوان" || w.ChannelID != fakeCh || w.ChannelName != "Channel" || w.DateText != "Oct 3, 2026" {
		t.Errorf("basic: %+v", w)
	}
	if !w.Views.Exact || w.Views.Min != 12345 {
		t.Errorf("views: %+v", w.Views)
	}
	if !w.Likes.Exact || w.Likes.Min != 1234 {
		t.Errorf("likes should come from the exact accessibility label: %+v", w.Likes)
	}
	if len(w.Chapters) != 2 || w.Chapters[1].Title != "Part 2" || w.Chapters[1].StartSec != 65 {
		t.Errorf("chapters: %+v", w.Chapters)
	}
	if w.CommentsToken != "COMMENTS_TOKEN" || w.CommentCount.Min != 89 {
		t.Errorf("comments: token %q count %+v", w.CommentsToken, w.CommentCount)
	}
	live := ParseWatch(watchDoc(t, true))
	if live.LiveViewers.Min != 1234 || live.Views.OK {
		t.Errorf("live viewers: %+v views %+v", live.LiveViewers, live.Views)
	}
}

func TestParsePlayer(t *testing.T) {
	p := ParsePlayer(doc(t, M{
		"playabilityStatus": M{"status": "OK"},
		"videoDetails": M{"videoId": "fakevid0001", "title": "t", "lengthSeconds": "754", "keywords": A{"بغداد", "news"},
			"channelId": fakeCh, "shortDescription": "d", "viewCount": "98765", "isLiveContent": true},
		"microformat": M{"playerMicroformatRenderer": M{"category": "News & Politics", "publishDate": "2026-10-03T08:00:12-07:00",
			"uploadDate": "2026-10-03T08:00:12-07:00", "isFamilySafe": true, "liveBroadcastDetails": M{"isLiveNow": false}}},
		"captions":      M{"playerCaptionsTracklistRenderer": M{"captionTracks": A{M{"languageCode": "ar", "kind": "asr"}, M{"languageCode": "en"}}}},
		"streamingData": M{"formats": A{M{"url": "must-not-be-parsed"}}},
	}))
	if p.VideoID != "fakevid0001" || p.LengthSec != 754 || len(p.Keywords) != 2 || p.Views != 98765 || !p.WasLive || p.IsLive {
		t.Errorf("details: %+v", p)
	}
	if p.Category != "News & Politics" || p.PublishDate == "" || !p.FamilySafe || p.Playability != "OK" {
		t.Errorf("microformat: %+v", p)
	}
	if len(p.CaptionLangs) != 2 || p.CaptionLangs[0] != "ar (auto)" {
		t.Errorf("captions: %v", p.CaptionLangs)
	}
}

func TestParseCommentsNewLayout(t *testing.T) {
	d := doc(t, M{
		"onResponseReceivedEndpoints": A{
			M{"reloadContinuationItemsCommand": M{"continuationItems": A{M{"commentsHeaderRenderer": M{
				"countText": M{"runs": A{M{"text": "1,234"}, M{"text": " Comments"}}},
				"sortMenu": M{"sortFilterSubMenuRenderer": M{"subMenuItems": A{
					M{"title": "Top comments", "serviceEndpoint": M{"continuationCommand": M{"token": "TOP"}}},
					M{"title": "Newest first", "serviceEndpoint": M{"continuationCommand": M{"token": "NEWEST"}}},
				}}},
			}}}}},
			M{"reloadContinuationItemsCommand": M{"continuationItems": A{
				M{"commentThreadRenderer": M{
					"commentViewModel": M{"commentViewModel": M{"commentId": "UgxFAKE1", "toolbarStateKey": "ts1", "pinnedText": "Pinned by @fakechan"}},
					"replies":          M{"commentRepliesRenderer": M{"contents": A{token("REPLIES1")}}},
				}},
				M{"commentThreadRenderer": M{"commentViewModel": M{"commentViewModel": M{"commentId": "UgxFAKE2", "toolbarStateKey": "ts2"}}}},
				token("NEXTPAGE"),
			}}},
		},
		"frameworkUpdates": M{"entityBatchUpdate": M{"mutations": A{
			M{"payload": M{"commentEntityPayload": M{
				"properties": M{"commentId": "UgxFAKE1", "content": M{"content": "تعليق أول"}, "publishedTime": "منذ ساعتين (تم التعديل)", "replyLevel": 0},
				"author":     M{"channelId": fakeAuthor, "displayName": "@viewer1", "avatarThumbnailUrl": "https://img.example/v1", "isVerified": false, "isCreator": false},
				"toolbar":    M{"likeCountNotliked": "1.2K", "replyCount": "3", "heartActiveTooltip": "liked by creator"},
			}}},
			M{"payload": M{"commentEntityPayload": M{
				"properties": M{"commentId": "UgxFAKE2", "content": M{"content": "second"}, "publishedTime": "5 days ago", "replyLevel": 0},
				"author":     M{"channelId": fakeCh, "displayName": "@fakechan", "isCreator": true, "isVerified": true},
				"toolbar":    M{"likeCountNotliked": "", "replyCount": "", "heartActiveTooltip": "liked by creator"},
			}}},
			M{"payload": M{"engagementToolbarStateEntityPayload": M{"key": "ts1", "heartState": "TOOLBAR_HEART_STATE_HEARTED"}}},
			M{"payload": M{"engagementToolbarStateEntityPayload": M{"key": "ts2", "heartState": "TOOLBAR_HEART_STATE_UNHEARTED"}}},
		}}},
	})
	p := ParseComments(d, now)
	if len(p.Comments) != 2 {
		t.Fatalf("comments: %+v", p.Comments)
	}
	c1, c2 := p.Comments[0], p.Comments[1]
	if c1.ID != "UgxFAKE1" || c1.Text != "تعليق أول" || c1.AuthorChannelID != fakeAuthor || c1.AuthorName != "@viewer1" {
		t.Errorf("c1 basic: %+v", c1)
	}
	if !c1.Pinned || !c1.Hearted || !c1.Edited || c1.Likes.Min != 1200 || c1.Likes.Exact || c1.ReplyCount != 3 {
		t.Errorf("c1 flags: %+v", c1)
	}
	if now.Sub(c1.Published.At) != 2*time.Hour || c1.Published.Precision != time.Hour {
		t.Errorf("c1 time: %+v", c1.Published)
	}
	if c2.Pinned || c2.Hearted || !c2.AuthorIsCreator || !c2.AuthorVerified || c2.Likes.Min != 0 || c2.IsReply {
		t.Errorf("c2: %+v", c2)
	}
	if p.NextToken != "NEXTPAGE" || p.NewestFirstToken != "NEWEST" || p.ReplyTokens["UgxFAKE1"] != "REPLIES1" || p.Total.Min != 1234 {
		t.Errorf("tokens/total: next %q newest %q replies %v total %+v", p.NextToken, p.NewestFirstToken, p.ReplyTokens, p.Total)
	}
}

func TestParseCommentsRepliesPage(t *testing.T) {
	d := doc(t, M{
		"onResponseReceivedEndpoints": A{M{"appendContinuationItemsAction": M{"continuationItems": A{
			M{"commentViewModel": M{"commentId": "UgxFAKE1.reply1", "toolbarStateKey": "tr1"}},
			token("MOREREPLIES"),
		}}}},
		"frameworkUpdates": M{"entityBatchUpdate": M{"mutations": A{M{"payload": M{"commentEntityPayload": M{
			"properties": M{"commentId": "UgxFAKE1.reply1", "content": M{"content": "reply"}, "publishedTime": "1 hour ago", "replyLevel": 1},
			"author":     M{"channelId": fakeAuthor, "displayName": "@viewer2"},
			"toolbar":    M{"likeCountNotliked": "7"},
		}}}}}},
	})
	p := ParseComments(d, now)
	if len(p.Comments) != 1 || !p.Comments[0].IsReply || p.Comments[0].ParentID != "UgxFAKE1" || p.Comments[0].Likes.Min != 7 || !p.Comments[0].Likes.Exact {
		t.Fatalf("reply: %+v", p.Comments)
	}
	if p.NextToken != "MOREREPLIES" {
		t.Errorf("next = %q", p.NextToken)
	}
}

func TestParseCommentsOldLayout(t *testing.T) {
	d := doc(t, M{"onResponseReceivedEndpoints": A{M{"appendContinuationItemsAction": M{"continuationItems": A{
		M{"commentThreadRenderer": M{"comment": M{"commentRenderer": M{
			"commentId": "UgzOLD1", "contentText": M{"runs": A{M{"text": "old "}, M{"text": "comment"}}},
			"authorText": M{"simpleText": "@old"}, "authorEndpoint": M{"browseEndpoint": M{"browseId": fakeAuthor}},
			"publishedTimeText": M{"runs": A{M{"text": "3 weeks ago (edited)"}}}, "voteCount": M{"simpleText": "12"},
			"replyCount": 2, "pinnedCommentBadge": M{}, "authorIsChannelOwner": false,
			"actionButtons": M{"commentActionButtonsRenderer": M{"creatorHeart": M{"creatorHeartRenderer": M{"isHearted": true}}}},
		}}}},
		token("OLDNEXT"),
	}}}}})
	p := ParseComments(d, now)
	if len(p.Comments) != 1 {
		t.Fatalf("comments: %+v", p.Comments)
	}
	c := p.Comments[0]
	if c.Text != "old comment" || c.AuthorChannelID != fakeAuthor || !c.Pinned || !c.Hearted || !c.Edited || c.Likes.Min != 12 || c.ReplyCount != 2 || now.Sub(c.Published.At) != 21*day {
		t.Errorf("old comment: %+v", c)
	}
	if p.NextToken != "OLDNEXT" {
		t.Errorf("next = %q", p.NextToken)
	}
}

func TestParsePosts(t *testing.T) {
	d := doc(t, M{"contents": A{
		M{"backstagePostThreadRenderer": M{"post": M{"backstagePostRenderer": M{
			"postId": "UgkxFAKEPOST", "authorEndpoint": M{"browseEndpoint": M{"browseId": fakeCh}},
			"contentText": M{"runs": A{M{"text": "post text"}}}, "publishedTimeText": M{"runs": A{M{"text": "منذ 3 أيام"}}},
			"voteCount": M{"simpleText": "45"}, "backstageAttachment": M{"pollRenderer": M{}},
		}}}},
		token("POSTNEXT"),
	}})
	posts, next := ParsePosts(d, now)
	if len(posts) != 1 || posts[0].ID != "UgkxFAKEPOST" || posts[0].ChannelID != fakeCh || posts[0].Likes.Min != 45 || !posts[0].HasPoll || now.Sub(posts[0].Published.At) != 3*day {
		t.Errorf("posts: %+v", posts)
	}
	if next != "POSTNEXT" {
		t.Errorf("next = %q", next)
	}
}

// aboutHeader is a channel header whose description opens the About panel
// with a "load more" item of its own, as every tab's first page carries.
func aboutHeader() M {
	return M{"pageHeaderRenderer": M{"content": M{"pageHeaderViewModel": M{
		"description": M{"descriptionPreviewViewModel": M{"rendererContext": M{"commandContext": M{"onTap": M{"innertubeCommand": M{"showEngagementPanelEndpoint": M{
			"engagementPanel": M{"engagementPanelSectionListRenderer": M{"content": M{"sectionListRenderer": M{"contents": A{
				M{"itemSectionRenderer": M{"contents": A{token("ABOUT_PANEL")}}},
			}}}}},
		}}}}}}},
	}}}}
}

func gridItem(id string) M {
	return M{"richItemRenderer": M{"content": M{"lockupViewModel": M{"contentId": id, "contentType": "LOCKUP_CONTENT_TYPE_VIDEO"}}}}
}

func TestListTokenIgnoresTheAboutPanel(t *testing.T) {
	grid := func(items ...any) M {
		return M{"twoColumnBrowseResultsRenderer": M{"tabs": A{M{"tabRenderer": M{"selected": true,
			"content": M{"richGridRenderer": M{"contents": A(items)}}}}}}}
	}
	cases := []struct {
		name string
		resp M
		want string
	}{
		{"first page with more to come", M{"header": aboutHeader(), "contents": grid(gridItem("fakevid0001"), gridItem("fakevid0002"), token("GRID_NEXT"))}, "GRID_NEXT"},
		{"first page that is also the last", M{"header": aboutHeader(), "contents": grid(gridItem("fakevid0001"))}, ""},
		{"continuation page", M{"onResponseReceivedActions": A{M{"appendContinuationItemsAction": M{"continuationItems": A{gridItem("fakevid0003"), token("PAGE_3")}}}}}, "PAGE_3"},
		{"a token alone in its own list is not the list's", M{"contents": grid(gridItem("fakevid0001"),
			M{"richItemRenderer": M{"content": M{"commentRepliesRenderer": M{"contents": A{token("REPLIES")}}}}})}, ""},
	}
	for _, c := range cases {
		l := ParseList(doc(t, c.resp), now)
		if l.NextToken != c.want {
			t.Errorf("%s: next token = %q, want %q", c.name, l.NextToken, c.want)
		}
	}
}

// TestLockupReadsLabelsAndShortForms uses the grid layout of the live site
// (1 November 2026): short text on screen, spelled-out screen-reader labels.
func TestLockupReadsLabelsAndShortForms(t *testing.T) {
	part := func(text, label string) M {
		p := M{"text": M{"content": text}}
		if label != "" {
			p["accessibilityLabel"] = label
		}
		return p
	}
	lockup := func(id string, parts ...any) M {
		return M{"richItemRenderer": M{"content": M{"lockupViewModel": M{"contentId": id, "contentType": "LOCKUP_CONTENT_TYPE_VIDEO",
			"metadata": M{"lockupMetadataViewModel": M{"title": M{"content": "t"}, "metadata": M{"contentMetadataViewModel": M{
				"metadataRows": A{M{"metadataParts": A(parts)}}}}}}}}}}
	}
	d := doc(t, M{"contents": M{"richGridRenderer": M{"contents": A{
		lockup("fakevid0001", part("12K", "12 thousand views"), part("3d ago", "3 days ago")),
		lockup("fakevid0002", part("1.2K", ""), part("2w ago", "")), // no labels: short forms alone
		lockup("fakevid0003", part("1.2K", "1.2 ألف مشاهدة"), part("قبل 5 أيام", "منذ 5 أيام")),
		lockup("fakevid0004", part("345", "345 views"), part("Streamed 5mo ago", "Streamed 5 months ago")),
	}}}})
	want := map[string]struct {
		views int64
		ago   time.Duration
	}{
		"fakevid0001": {12000, 3 * day}, "fakevid0002": {1200, 14 * day},
		"fakevid0003": {1200, 5 * day}, "fakevid0004": {345, 150 * day},
	}
	l := ParseList(d, now)
	if len(l.Videos) != len(want) {
		t.Fatalf("videos: %+v", l.Videos)
	}
	for _, v := range l.Videos {
		w := want[v.VideoID]
		if v.Views.Min != w.views || !v.Published.OK || now.Sub(v.Published.At) != w.ago || v.ChannelName != "" {
			t.Errorf("%s: views %+v published %+v channel %q; want %d views, %v ago, no channel name", v.VideoID, v.Views, v.Published, v.ChannelName, w.views, w.ago)
		}
	}
}

func TestCaptionsAndTranscriptHelpers(t *testing.T) {
	p := ParsePlayer(doc(t, M{"captions": M{"playerCaptionsTracklistRenderer": M{"captionTracks": A{
		M{"baseUrl": "https://www.youtube.com/api/timedtext?v=x&lang=ar", "languageCode": "ar", "kind": "asr"},
		M{"baseUrl": "https://www.youtube.com/api/timedtext?v=x&lang=en", "languageCode": "en"},
	}}}}))
	if len(p.CaptionTracks) != 2 || !p.CaptionTracks[0].Auto || p.CaptionTracks[1].Auto || p.CaptionTracks[1].URL == "" ||
		len(p.CaptionLangs) != 2 || p.CaptionLangs[0] != "ar (auto)" {
		t.Errorf("caption tracks %+v, langs %v", p.CaptionTracks, p.CaptionLangs)
	}
	if got := TranscriptParams(doc(t, M{"engagementPanels": A{M{"x": M{"getTranscriptEndpoint": M{"params": "P1"}}}}})); got != "P1" {
		t.Errorf("TranscriptParams = %q", got)
	}
	if got := TranscriptParams(doc(t, M{"contents": M{}})); got != "" {
		t.Errorf("no panel: TranscriptParams = %q", got)
	}
	if n := TranscriptSegments(doc(t, A{M{"transcriptSegmentRenderer": M{}}, M{"transcriptSegmentRenderer": M{}}})); n != 2 {
		t.Errorf("TranscriptSegments = %d", n)
	}
	for body, want := range map[string]int{
		`{"events":[{"segs":[{"utf8":"a"}]},{"tStartMs":1},{"segs":[{"utf8":"b"}]}]}`: 2,
		`<transcript><text start="0">a</text><text start="1">b</text></transcript>`:   2,
		`<timedtext><body><p t="0">a</p></body></timedtext>`:                          1,
		``: 0,
	} {
		if got := CaptionCues([]byte(body)); got != want {
			t.Errorf("CaptionCues(%q) = %d, want %d", body, got, want)
		}
	}
}
