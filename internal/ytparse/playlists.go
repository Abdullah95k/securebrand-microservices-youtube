package ytparse

import (
	"strconv"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/jsonx"
)

// Playlist is one playlist card on a channel's Playlists tab.
type Playlist struct {
	ID         string
	Title      string
	VideoCount Count // from the card's badge, e.g. "6 videos"
}

// ParsePlaylists reads a Playlists tab (or its continuation). The 2026
// layout is lockupViewModel cards with contentType
// LOCKUP_CONTENT_TYPE_PLAYLIST (checked against the browser recordings of
// 2 November 2026); the older gridPlaylistRenderer is read too.
func ParsePlaylists(data any) ([]Playlist, string) {
	var out []Playlist
	seen := map[string]bool{}
	add := func(p Playlist) {
		if p.ID == "" || seen[p.ID] {
			return
		}
		seen[p.ID] = true
		out = append(out, p)
	}
	for _, l := range jsonx.FindMaps(data, "lockupViewModel") {
		if jsonx.String(l, "contentType") != "LOCKUP_CONTENT_TYPE_PLAYLIST" {
			continue
		}
		p := Playlist{ID: jsonx.String(l, "contentId"),
			Title: jsonx.String(jsonx.First(l, "lockupMetadataViewModel"), "title", "content")}
		for _, b := range jsonx.FindMaps(l, "thumbnailBadgeViewModel") {
			if c := ParseCount(jsonx.String(b, "text")); c.OK {
				p.VideoCount = c
				break
			}
		}
		add(p)
	}
	for _, g := range jsonx.FindMaps(data, "gridPlaylistRenderer") {
		add(Playlist{ID: jsonx.String(g, "playlistId"), Title: jsonx.TextAt(g, "title"),
			VideoCount: ParseCount(firstNonEmpty(jsonx.TextAt(g, "videoCountText"), jsonx.TextAt(g, "videoCountShortText")))})
	}
	return out, lastContinuation(data)
}

// PlaylistEntry is one video in a playlist.
type PlaylistEntry struct {
	VideoID  string
	Title    string
	Position int // 1-based, 0 when not shown
}

// ParsePlaylistItems reads a playlist page (browse "VL" + playlist ID) or
// its continuation: playlistVideoRenderer rows, or video lockups.
func ParsePlaylistItems(data any) ([]PlaylistEntry, string) {
	var out []PlaylistEntry
	seen := map[string]bool{}
	for _, r := range jsonx.FindMaps(data, "playlistVideoRenderer") {
		id := jsonx.String(r, "videoId")
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		pos, _ := strconv.Atoi(jsonx.TextAt(r, "index"))
		out = append(out, PlaylistEntry{VideoID: id, Title: jsonx.TextAt(r, "title"), Position: pos})
	}
	for _, l := range jsonx.FindMaps(data, "lockupViewModel") {
		if jsonx.String(l, "contentType") != "LOCKUP_CONTENT_TYPE_VIDEO" {
			continue
		}
		id := jsonx.String(l, "contentId")
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, PlaylistEntry{VideoID: id, Title: jsonx.String(jsonx.First(l, "lockupMetadataViewModel"), "title", "content")})
	}
	return out, lastContinuation(data)
}

// PostDetailParams returns the browse params that open a post's own page,
// where its comments are: the reply button's (or any) browseEndpoint with
// browseId FEpost_detail inside the post.
func PostDetailParams(post any) string {
	for _, b := range jsonx.FindMaps(post, "browseEndpoint") {
		if jsonx.String(b, "browseId") == "FEpost_detail" {
			if p := jsonx.String(b, "params"); p != "" {
				return p
			}
		}
	}
	return ""
}

// CommentSectionToken returns the token that opens the comment section on
// a watch page or a post's page.
func CommentSectionToken(data any) string {
	for _, sec := range jsonx.FindMaps(data, "itemSectionRenderer") {
		if jsonx.String(sec, "sectionIdentifier") == "comment-item-section" {
			if t := firstToken(sec); t != "" {
				return t
			}
		}
	}
	return ""
}
