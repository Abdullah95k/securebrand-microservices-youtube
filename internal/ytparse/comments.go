package ytparse

import (
	"strings"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/jsonx"
)

// Comment is one comment or reply as shown publicly.
type Comment struct {
	ID              string
	ParentID        string // set for replies when known
	IsReply         bool
	Text            string
	AuthorChannelID string
	AuthorName      string // handle or display name
	AuthorAvatar    string
	AuthorIsCreator bool
	AuthorVerified  bool
	AuthorBadge     string // e.g. channel-member badge tooltip
	Likes           Count
	ReplyCount      int64
	PublishedText   string
	Published       TimeEstimate
	Edited          bool
	Pinned          bool
	Hearted         bool
}

// CommentsPage is one page of comments.
type CommentsPage struct {
	Comments         []Comment
	NextToken        string            // next page of this list
	NewestFirstToken string            // re-sorts the section newest first
	ReplyTokens      map[string]string // comment ID → token for its replies
	TotalText        string            // e.g. "1,234 Comments"
	Total            Count
}

// ParseComments reads a comments continuation response (top-level comments
// or replies). It understands the newer entity layout (commentViewModel +
// commentEntityPayload) and the older commentRenderer layout.
func ParseComments(data any, now time.Time) CommentsPage {
	page := CommentsPage{ReplyTokens: map[string]string{}}

	// Newer layout: data lives in framework entity mutations.
	entities := map[string]map[string]any{}
	toolbarStates := map[string]map[string]any{}
	for _, e := range jsonx.FindMaps(data, "commentEntityPayload") {
		if id := jsonx.String(e, "properties", "commentId"); id != "" {
			entities[id] = e
		}
	}
	for _, s := range jsonx.FindMaps(data, "engagementToolbarStateEntityPayload") {
		if k := jsonx.String(s, "key"); k != "" {
			toolbarStates[k] = s
		}
	}

	seen := map[string]bool{}
	add := func(c Comment) {
		if c.ID == "" || seen[c.ID] {
			return
		}
		seen[c.ID] = true
		c.Published = ParseRelativeTime(c.PublishedText, now)
		c.Edited = c.Edited || c.Published.Edited
		page.Comments = append(page.Comments, c)
	}

	for _, vm := range jsonx.FindMaps(data, "commentViewModel") {
		id := jsonx.String(vm, "commentId")
		e := entities[id]
		if e == nil {
			continue
		}
		c := Comment{
			ID:              id,
			IsReply:         jsonx.String(e, "properties", "replyLevel") == "1" || jsonx.String(e, "properties", "replyLevel") == "REPLY_LEVEL_1",
			Text:            jsonx.String(e, "properties", "content", "content"),
			AuthorChannelID: jsonx.String(e, "author", "channelId"),
			AuthorName:      jsonx.String(e, "author", "displayName"),
			AuthorAvatar:    jsonx.String(e, "author", "avatarThumbnailUrl"),
			AuthorIsCreator: jsonx.Bool(e, "author", "isCreator"),
			AuthorVerified:  jsonx.Bool(e, "author", "isVerified"),
			AuthorBadge:     jsonx.String(e, "author", "sponsorBadgeA11y"),
			PublishedText:   jsonx.String(e, "properties", "publishedTime"),
			Pinned:          jsonx.Get(vm, "pinnedText") != nil,
		}
		if n, ok := jsonx.Int(e, "properties", "replyLevel"); ok && n > 0 {
			c.IsReply = true
		}
		c.Likes = ParseCount(firstNonEmpty(jsonx.String(e, "toolbar", "likeCountNotliked"), "0"))
		if n, ok := FirstNumber(jsonx.String(e, "toolbar", "replyCount")); ok {
			c.ReplyCount = n
		}
		if st := toolbarStates[jsonx.String(vm, "toolbarStateKey")]; st != nil {
			c.Hearted = jsonx.String(st, "heartState") == "TOOLBAR_HEART_STATE_HEARTED"
		}
		// Not toolbar.heartActiveTooltip: every comment carries that tooltip
		// text, hearted or not (live recordings, 1 November 2026).
		add(c)
	}

	// Older layout.
	for _, r := range jsonx.FindMaps(data, "commentRenderer") {
		c := Comment{
			ID:              jsonx.String(r, "commentId"),
			Text:            jsonx.TextAt(r, "contentText"),
			AuthorChannelID: jsonx.String(jsonx.First(r, "authorEndpoint"), "browseEndpoint", "browseId"),
			AuthorName:      jsonx.TextAt(r, "authorText"),
			AuthorAvatar:    largestThumb(jsonx.Get(r, "authorThumbnail")),
			AuthorIsCreator: jsonx.Bool(r, "authorIsChannelOwner"),
			AuthorVerified:  jsonx.Get(r, "authorCommentBadge") != nil,
			PublishedText:   jsonx.TextAt(r, "publishedTimeText"),
			Pinned:          jsonx.Get(r, "pinnedCommentBadge") != nil,
			Hearted:         jsonx.Bool(jsonx.First(r, "creatorHeartRenderer"), "isHearted"),
			Likes:           ParseCount(firstNonEmpty(jsonx.TextAt(r, "voteCount"), "0")),
		}
		if n, ok := jsonx.Int(r, "replyCount"); ok {
			c.ReplyCount = n
		}
		c.IsReply = strings.Contains(c.ID, ".")
		add(c)
	}

	// Reply tokens per thread; the parent is the thread's top comment.
	for _, th := range jsonx.FindMaps(data, "commentThreadRenderer") {
		parent := threadCommentID(th)
		if rep := jsonx.First(th, "commentRepliesRenderer"); rep != nil && parent != "" {
			if t := firstToken(rep); t != "" {
				page.ReplyTokens[parent] = t
			}
		}
	}
	// Reply IDs look like "<parentID>.<replyID>" in both layouts.
	for i := range page.Comments {
		if c := &page.Comments[i]; c.IsReply {
			if j := strings.Index(c.ID, "."); j > 0 {
				c.ParentID = c.ID[:j]
			}
		}
	}

	page.NextToken = pageContinuation(data)
	if h := jsonx.First(data, "commentsHeaderRenderer"); h != nil {
		// Newer headers carry the bare number in commentsCount next to the
		// sentence in countText (Phase 0: 30% of videos had only the
		// former readable).
		for _, key := range []string{"commentsCount", "countText"} {
			if t := jsonx.TextAt(h, key); t != "" {
				page.TotalText, page.Total = t, ParseCount(t)
				if page.Total.OK {
					break
				}
			}
		}
	}
	page.NewestFirstToken = newestFirstToken(data)
	return page
}

// threadCommentID returns the ID of a thread's top comment. The newer
// layout wraps it twice ("commentViewModel": {"commentViewModel": {…}}), so
// the first object that actually carries a commentId wins.
func threadCommentID(th map[string]any) string {
	for _, key := range []string{"commentViewModel", "commentRenderer"} {
		for _, m := range jsonx.FindMaps(th, key) {
			if id := jsonx.String(m, "commentId"); id != "" {
				return id
			}
		}
	}
	return ""
}

// pageContinuation finds the "load more" token that is a direct item of a
// continuationItems list, ignoring tokens nested inside threads (replies)
// and the sort menu.
func pageContinuation(data any) string {
	token := ""
	for _, arr := range jsonx.FindAll(data, "continuationItems") {
		items, _ := arr.([]any)
		for _, it := range items {
			cir := jsonx.Map(it, "continuationItemRenderer")
			if cir == nil {
				continue
			}
			if t := firstToken(cir); t != "" {
				token = t
			}
		}
	}
	return token
}

// newestFirstToken picks the "Newest first" entry of the sort menu.
func newestFirstToken(data any) string {
	for _, menu := range jsonx.FindMaps(data, "sortFilterSubMenuRenderer") {
		items := jsonx.Slice(menu, "subMenuItems")
		for _, it := range items {
			title := strings.ToLower(jsonx.String(it, "title"))
			if strings.Contains(title, "newest") || strings.Contains(title, "الأحدث") || strings.Contains(title, "الاحدث") {
				if t := firstToken(it); t != "" {
					return t
				}
			}
		}
		if len(items) > 1 { // YouTube lists "Top" first and "Newest" second
			if t := firstToken(items[1]); t != "" {
				return t
			}
		}
	}
	return ""
}

// Post is a community post.
type Post struct {
	ID            string
	ChannelID     string
	Text          string
	PublishedText string
	Published     TimeEstimate
	Likes         Count
	CommentsText  string
	HasImage      bool
	Images        []string // the attached images, the largest size of each, in order (fix 21)
	HasPoll       bool
	HasVideo      bool
	Comments      Count  // from the reply button
	DetailParams  string // opens the post's page (and its comments)
}

// postImages lists a post attachment's images (one, or several in a
// postMultiImageRenderer), the largest size of each, in order, once each.
func postImages(att any) []string {
	var out []string
	seen := map[string]bool{}
	for _, r := range jsonx.FindMaps(att, "backstageImageRenderer") {
		u := largestThumb(jsonx.Get(r, "image"))
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	return out
}

// ParsePosts reads a Posts/Community tab or its continuation.
func ParsePosts(data any, now time.Time) ([]Post, string) {
	var out []Post
	seen := map[string]bool{}
	for _, key := range []string{"backstagePostRenderer", "postRenderer", "sharedPostRenderer"} {
		for _, r := range jsonx.FindMaps(data, key) {
			id := jsonx.String(r, "postId")
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			p := Post{
				ID:            id,
				ChannelID:     jsonx.String(jsonx.First(r, "authorEndpoint"), "browseEndpoint", "browseId"),
				Text:          jsonx.TextAt(r, "contentText"),
				PublishedText: jsonx.TextAt(r, "publishedTimeText"),
				Likes:         ParseCount(firstNonEmpty(jsonx.TextAt(r, "voteCount"), "0")),
				CommentsText:  jsonx.TextAt(jsonx.First(r, "replyButton"), "text"),
			}
			att := jsonx.Get(r, "backstageAttachment")
			p.HasImage = jsonx.First(att, "backstageImageRenderer") != nil || jsonx.First(att, "postMultiImageRenderer") != nil
			p.Images = postImages(att)
			p.HasPoll = jsonx.First(att, "pollRenderer") != nil
			p.HasVideo = jsonx.First(att, "videoRenderer") != nil
			p.Published = ParseRelativeTime(p.PublishedText, now)
			p.Comments = ParseCount(firstNonEmpty(p.CommentsText, jsonx.String(jsonx.First(r, "replyButton"), "buttonRenderer", "text", "simpleText")))
			p.DetailParams = PostDetailParams(r)
			out = append(out, p)
		}
	}
	return out, lastContinuation(data)
}
