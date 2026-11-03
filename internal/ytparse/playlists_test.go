package ytparse

import (
	"encoding/json"
	"testing"
	"time"
)

func mustJSON(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// The shapes follow the browser recordings of 2 November 2026 (keys only;
// every value is made up).
func TestParsePlaylists(t *testing.T) {
	data := mustJSON(t, `{"contents":{"items":[
		{"lockupViewModel":{"contentId":"PLfakePLAYLIST01","contentType":"LOCKUP_CONTENT_TYPE_PLAYLIST",
			"contentImage":{"collectionThumbnailViewModel":{"primaryThumbnail":{"thumbnailViewModel":{"overlays":[
				{"thumbnailOverlayBadgeViewModel":{"thumbnailBadges":[{"thumbnailBadgeViewModel":{"text":"6 videos"}}]}}]}}}},
			"metadata":{"lockupMetadataViewModel":{"title":{"content":"أخبار بغداد"}}}}},
		{"lockupViewModel":{"contentId":"fakeVIDEO01","contentType":"LOCKUP_CONTENT_TYPE_VIDEO"}},
		{"gridPlaylistRenderer":{"playlistId":"PLfakePLAYLIST02","title":{"runs":[{"text":"Old style"}]},"videoCountShortText":{"simpleText":"43"}}},
		{"continuationItemRenderer":{"continuationEndpoint":{"continuationCommand":{"token":"NEXTPL"}}}}
	]}}`)
	pls, next := ParsePlaylists(data)
	if len(pls) != 2 || pls[0].ID != "PLfakePLAYLIST01" || pls[0].Title != "أخبار بغداد" || pls[0].VideoCount.Min != 6 ||
		pls[1].VideoCount.Min != 43 || next != "NEXTPL" {
		t.Errorf("playlists %+v, next %q", pls, next)
	}
}

func TestParsePlaylistItems(t *testing.T) {
	data := mustJSON(t, `{"contents":[
		{"playlistVideoRenderer":{"videoId":"fakeV1","title":{"runs":[{"text":"one"}]},"index":{"simpleText":"1"}}},
		{"playlistVideoRenderer":{"videoId":"fakeV2","title":{"simpleText":"two"},"index":{"simpleText":"2"}}},
		{"lockupViewModel":{"contentId":"fakeV3","contentType":"LOCKUP_CONTENT_TYPE_VIDEO","metadata":{"lockupMetadataViewModel":{"title":{"content":"three"}}}}},
		{"continuationItemRenderer":{"continuationEndpoint":{"continuationCommand":{"token":"MORE"}}}}]}`)
	items, next := ParsePlaylistItems(data)
	if len(items) != 3 || items[1].Position != 2 || items[2].Title != "three" || next != "MORE" {
		t.Errorf("items %+v, next %q", items, next)
	}
}

func TestPostDetailAndCommentSection(t *testing.T) {
	posts, _ := ParsePosts(mustJSON(t, `{"contents":[{"backstagePostThreadRenderer":{"post":{"backstagePostRenderer":{
		"postId":"UgkxFAKEPOST01","contentText":{"runs":[{"text":"post text"}]},"publishedTimeText":{"runs":[{"text":"2 days ago"}]},
		"voteCount":{"simpleText":"1.2K"},
		"replyButton":{"buttonRenderer":{"text":{"simpleText":"37"},"navigationEndpoint":{"browseEndpoint":{"browseId":"FEpost_detail","params":"FAKEPARAMS"}}}}}}}}]}`), time.Now())
	if len(posts) != 1 || posts[0].DetailParams != "FAKEPARAMS" || posts[0].Comments.Min != 37 || posts[0].Likes.Min != 1200 {
		t.Fatalf("posts %+v", posts)
	}
	page := mustJSON(t, `{"contents":{"sectionListRenderer":{"contents":[
		{"itemSectionRenderer":{"sectionIdentifier":"backstage-item-section","contents":[]}},
		{"itemSectionRenderer":{"sectionIdentifier":"comment-item-section","contents":[
			{"continuationItemRenderer":{"continuationEndpoint":{"continuationCommand":{"token":"POSTCOMMENTS"}}}}]}}]}}}`)
	if tok := CommentSectionToken(page); tok != "POSTCOMMENTS" {
		t.Errorf("comment section token %q", tok)
	}
}
