package contract

import (
	"strings"

	contentv1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/content/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// channel: content.account, a monitored channel.
func (b *builder) channel() ([]Message, error) {
	var c channelRow
	if err := b.decode(&c); err != nil {
		return nil, err
	}
	id := accountID(c.ID)
	// A removal is partial: keys and status. So is a channel whose page
	// gave no name, neither title nor handle (11 November: 3 such events
	// were refused): what is set is known, and the name is not, so
	// consumers keep the name they have, as for commenters (author).
	nameless := deref(c.Title) == "" && deref(c.Handle) == ""
	a := &contentv1.Account{Envelope: b.envelope("account", id, c.ID, c.RemovedAt != nil || nameless)}
	if c.RemovedAt != nil {
		a.Status = gone(*c.RemovedAt, "not_found")
	} else {
		a.Type = contentv1.AccountType_ACCOUNT_TYPE_CHANNEL
		a.Handle, a.DisplayName, a.Url = deref(c.Handle), deref(c.Title), channelURL(c.ID)
		a.Bio = text(c.Description)
		a.Links = links(c.Links, contentv1.LinkSource_LINK_SOURCE_ABOUT_PANEL)
		a.StatedCountry = deref(c.Country)
		if a.CreatedAt = date(c.JoinedAt); a.CreatedAt != nil {
			a.CreatedAt.Displayed = deref(c.JoinedText)
		}
		a.Keywords = channelKeywords(deref(c.Keywords))
		a.Status = visible()
		a.Scope = scope(c.Kind, c.Status)
		a.Counts = &contentv1.AccountCounts{Followers: count(c.SubscribersMin, c.SubscribersText),
			Posts: atLeast(c.VideosMin), Views: atLeast(c.ViewsMin)}
		a.Extras = extras(map[string]any{"yt.tier": c.Tier, "yt.family_safe": c.FamilySafe,
			"yt.added_by": c.AddedBy, "yt.added_reason": c.AddedReason})
	}
	finish(a.Envelope, a, true)
	return []Message{{Topic: TopicAccount, Key: id, Msg: a}}, nil
}

// scope is the registry's decision for a channel (the contract's
// CollectionScope): core channels in full, mention channels for relevant
// items only; candidates and channels not kept are not monitored.
func scope(kind, status string) contentv1.CollectionScope {
	switch {
	case status != "listed" && status != "dormant":
		return contentv1.CollectionScope_COLLECTION_SCOPE_NOT_MONITORED
	case kind == "mention":
		return contentv1.CollectionScope_COLLECTION_SCOPE_RELEVANT_ONLY
	default:
		return contentv1.CollectionScope_COLLECTION_SCOPE_FULL
	}
}

// channelKeywords splits a channel's keyword field: words, and "quoted
// phrases".
func channelKeywords(s string) []string {
	var out []string
	var cur strings.Builder
	quoted := false
	flush := func() {
		if w := strings.TrimSpace(cur.String()); w != "" {
			out = append(out, w)
		}
		cur.Reset()
	}
	for _, r := range s {
		switch {
		case r == '"':
			flush()
			quoted = !quoted
		case (r == ' ' || r == ',') && !quoted:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// channelSnapshot: content.account_snapshot, the counts read.
func (b *builder) channelSnapshot() ([]Message, error) {
	var s channelSnapshot
	if err := b.decode(&s); err != nil {
		return nil, err
	}
	id := accountID(b.row.EntityID)
	c := s.Counts
	if s.Observed != nil {
		c = *s.Observed // what this read showed
	}
	m := &contentv1.AccountSnapshot{Envelope: b.envelope("account_snapshot", id, b.row.EntityID, false),
		Counts: &contentv1.AccountCounts{Followers: count(c.SubscribersMin, c.SubscribersText), Posts: atLeast(c.VideosMin),
			Views: atLeast(c.ViewsMin)},
		Handle: deref(s.Handle), DisplayName: deref(s.Title), Status: visible()}
	if s.RemovedAt != nil {
		m.Status = gone(*s.RemovedAt, "not_found")
	}
	finish(m.Envelope, m, false)
	return []Message{{Topic: TopicAccountSnapshot, Key: id, Msg: m}}, nil
}

func postType(kind string) contentv1.PostType {
	switch kind {
	case "short":
		return contentv1.PostType_POST_TYPE_SHORT_VIDEO
	case "live":
		return contentv1.PostType_POST_TYPE_LIVE
	default:
		return contentv1.PostType_POST_TYPE_VIDEO
	}
}

func liveStatus(s string) contentv1.LiveStatus {
	switch s {
	case "none":
		return contentv1.LiveStatus_LIVE_STATUS_NONE
	case "upcoming":
		return contentv1.LiveStatus_LIVE_STATUS_UPCOMING
	case "live":
		return contentv1.LiveStatus_LIVE_STATUS_LIVE
	case "was_live":
		return contentv1.LiveStatus_LIVE_STATUS_ENDED
	}
	return contentv1.LiveStatus_LIVE_STATUS_UNSPECIFIED
}

// video: content.post, a video, Short or live stream.
func (b *builder) video() ([]Message, error) {
	var v videoRow
	if err := b.decode(&v); err != nil {
		return nil, err
	}
	id := postID(v.ID)
	if fromAPI(v) && v.RemovedAt == nil {
		return nil, nil // the API's title and time: never sent; its website read sends it
	}
	// Removed: the keys and the status. Not yet dated (a Short seen in its
	// tab): what is known, as a partial event.
	removed := v.RemovedAt != nil
	p := &contentv1.Post{Envelope: b.envelope("post", id, v.ID, removed || v.PublishedAt == nil),
		AccountId: accountID(v.ChannelID), Type: postType(v.Kind)}
	if removed {
		p.Status = gone(*v.RemovedAt, "not_found")
	} else {
		p.Url = videoURL(v.ID, v.Kind)
		p.PublishedAt = estimate(v.PublishedAt, v.PublishedPrecisionS, v.PublishedText)
		p.Title, p.Description = text(v.Title), text(v.Description)
		p.UploaderTags, p.Category = v.Tags, deref(v.Category)
		info := &contentv1.VideoInfo{LiveStatus: liveStatus(v.LiveStatus), ScheduledStart: exact(v.ScheduledStart),
			CaptionLanguages: v.CaptionLangs}
		if v.DurationS != nil {
			ms := int64(*v.DurationS) * 1000
			info.DurationMs = &ms
		}
		for _, c := range v.Chapters {
			info.Chapters = append(info.Chapters, &contentv1.Chapter{Title: c.Title, StartMs: int64(c.StartSec) * 1000})
		}
		p.Video = info
		if v.FamilySafe != nil {
			p.Labels = &contentv1.ContentLabels{FamilySafe: v.FamilySafe}
		}
		disabled := v.CommentsDisabled
		p.Flags = &contentv1.PostFlags{CommentsDisabled: &disabled}
		p.Counts = &contentv1.Engagement{Views: views(v.Views, v.LastChecked != nil),
			Likes: count(v.LikesMin, v.LikesText), Comments: count(v.CommentsMin, v.CommentsText)}
		p.Status = visible()
		p.Relevance = b.relevance(v.Relevant, v.MatchedKeywords, deref(v.Title), deref(v.Description), strings.Join(v.Tags, " "))
		p.Coverage = coverage(v)
		p.Account = &contentv1.AccountRef{Id: accountID(v.ChannelID), Type: contentv1.AccountType_ACCOUNT_TYPE_CHANNEL}
		p.Extras = extras(map[string]any{"yt.source": v.Source, "yt.player_unavailable": &v.PlayerUnavailable})
	}
	finish(p.Envelope, p, true)
	return []Message{{Topic: TopicPost, Key: id, Msg: p}}, nil
}

// views: exact from a video's own check (the player's number), a lower
// bound from a listing ("1.2K views").
func views(v *int64, checked bool) *contentv1.Count {
	if checked {
		return exactCount(v)
	}
	return atLeast(v)
}

// coverage is how completely the video's comments were collected.
func coverage(v videoRow) *contentv1.ItemCoverage {
	c := &contentv1.ItemCoverage{CommentsReported: v.CommentsMin}
	switch {
	case v.CommentsDisabled:
		c.CommentsState = contentv1.CommentsState_COMMENTS_STATE_DISABLED
	case v.CommentsCompletePct == nil:
		c.CommentsState = contentv1.CommentsState_COMMENTS_STATE_NOT_READ
	case v.CommentsPartial:
		c.CommentsState, c.Reason = contentv1.CommentsState_COMMENTS_STATE_PARTIAL, "cap"
	default:
		c.CommentsState = contentv1.CommentsState_COMMENTS_STATE_COMPLETE
	}
	if v.CommentsCompletePct != nil {
		f := *v.CommentsCompletePct / 100
		c.Completeness = &f
	}
	return c
}

// videoMetrics: content.metrics for a video.
func (b *builder) videoMetrics() ([]Message, error) {
	var s videoMetrics
	if err := b.decode(&s); err != nil {
		return nil, err
	}
	id := postID(b.row.EntityID)
	e := &contentv1.Engagement{Views: views(s.Counts.Views, s.Observed != nil),
		Likes: count(s.Counts.LikesMin, s.Counts.LikesText), Comments: count(s.Counts.CommentsMin, s.Counts.CommentsText)}
	if s.Observed != nil {
		e.ConcurrentViewers = exactCount(s.Observed.LiveViewers)
	}
	m := &contentv1.Metrics{Envelope: b.envelope("metrics", id, b.row.EntityID, false), TargetKind: "post",
		ParentId: accountID(s.ChannelID), Counts: e}
	finish(m.Envelope, m, false)
	return []Message{{Topic: TopicMetrics, Key: id, Msg: m}}, nil
}

// post: content.post, a community post.
func (b *builder) post() ([]Message, error) {
	var v postRow
	if err := b.decode(&v); err != nil {
		return nil, err
	}
	id := postID(v.ID)
	removed := v.RemovedAt != nil
	p := &contentv1.Post{Envelope: b.envelope("post", id, v.ID, removed || v.PublishedAt == nil),
		AccountId: accountID(v.ChannelID), Type: contentv1.PostType_POST_TYPE_COMMUNITY}
	if removed {
		p.Status = gone(*v.RemovedAt, "not_found")
	} else {
		p.Url = communityURL(v.ID)
		p.PublishedAt = estimate(v.PublishedAt, v.PublishedPrecisionS, v.PublishedText)
		p.Text = text(v.Text)
		p.Counts = &contentv1.Engagement{Likes: count(v.LikesMin, v.LikesText), Comments: count(v.CommentsMin, v.CommentsText)}
		p.Status = visible()
		p.Relevance = b.relevance(v.Relevant, v.MatchedKeywords, deref(v.Text))
		p.Account = &contentv1.AccountRef{Id: accountID(v.ChannelID), Type: contentv1.AccountType_ACCOUNT_TYPE_CHANNEL}
		p.Extras = extras(map[string]any{"yt.source": v.Source})
	}
	finish(p.Envelope, p, true)
	return []Message{{Topic: TopicPost, Key: id, Msg: p}}, nil
}

// postMetrics: content.metrics for a community post.
func (b *builder) postMetrics() ([]Message, error) {
	var s postMetrics
	if err := b.decode(&s); err != nil {
		return nil, err
	}
	id := postID(b.row.EntityID)
	m := &contentv1.Metrics{Envelope: b.envelope("metrics", id, b.row.EntityID, false), TargetKind: "post",
		ParentId: accountID(s.ChannelID), Counts: &contentv1.Engagement{Likes: count(s.Counts.LikesMin, s.Counts.LikesText),
			Comments: count(s.Counts.CommentsMin, s.Counts.CommentsText)}}
	finish(m.Envelope, m, false)
	return []Message{{Topic: TopicMetrics, Key: id, Msg: m}}, nil
}

// comment: content.comment. YouTube has one reply level: a reply's parent
// and root are its thread, depth 1.
func (b *builder) comment() ([]Message, error) {
	var c commentRow
	if err := b.decode(&c); err != nil {
		return nil, err
	}
	key := postID(c.ParentID)
	deleted := c.DeletedAt != nil
	m := &contentv1.Comment{Envelope: b.envelope("comment", commentID(c.ID), c.ID, deleted || c.PublishedAt == nil),
		PostId: key, AccountId: accountID(c.ChannelID)}
	if c.ThreadID != nil && *c.ThreadID != "" {
		m.Depth, m.ParentCommentId, m.RootCommentId = 1, commentID(*c.ThreadID), commentID(*c.ThreadID)
	}
	if deleted {
		// Absent from two complete re-reads; the text stays stored (D18).
		m.Status = &contentv1.Status{Availability: contentv1.Availability_AVAILABILITY_DELETED, Since: timestamppb.New(*c.DeletedAt)}
	} else {
		m.Author = commentAuthor(c)
		m.Text = &contentv1.Text{Text: c.Text}
		m.PublishedAt = estimate(c.PublishedAt, c.PublishedPrecisionS, nil)
		byOwner, pinned, hearted, edited := c.AuthorIsCreator, c.Pinned, c.Hearted, c.Edited
		m.Flags = &contentv1.CommentFlags{ByPostOwner: &byOwner, Pinned: &pinned, LikedByCreator: &hearted, Edited: &edited}
		m.Counts = &contentv1.Engagement{Likes: count(c.LikesMin, c.LikesText)}
		if m.Depth == 0 {
			m.Counts.Replies = exactCount(c.ReplyCount)
		}
		m.Status = visible()
		m.Url = commentURL(c.ParentKind, c.ParentID, c.ID)
	}
	finish(m.Envelope, m, true)
	return []Message{{Topic: TopicComment, Key: key, Msg: m}}, nil
}

// commentAuthor is the commenter as the comment showed them.
func commentAuthor(c commentRow) *contentv1.Author {
	ref := &contentv1.AccountRef{Type: contentv1.AccountType_ACCOUNT_TYPE_CHANNEL, PictureUrl: deref(c.AuthorAvatarURL)}
	if c.AuthorID != nil && *c.AuthorID != "" {
		ref.Id, ref.NativeId, ref.Url = accountID(*c.AuthorID), *c.AuthorID, channelURL(*c.AuthorID)
	}
	name := deref(c.AuthorHandle)
	if name == "" {
		name = deref(c.AuthorName)
	}
	if strings.HasPrefix(name, "@") {
		ref.Handle = name
	} else {
		ref.DisplayName = name
	}
	if ref.Id == "" && ref.Handle == "" && ref.DisplayName == "" {
		return nil
	}
	a := &contentv1.Author{Account: ref}
	if b := c.AuthorBadges; b != nil {
		if b.Verified {
			a.Badges = append(a.Badges, &contentv1.Badge{Kind: "verified"})
		}
		if b.Member != "" {
			a.Badges = append(a.Badges, &contentv1.Badge{Kind: "member", Detail: b.Member})
		}
	}
	if c.AuthorIsCreator {
		a.Badges = append(a.Badges, &contentv1.Badge{Kind: "creator"})
	}
	return a
}

// commentMetrics: content.metrics for a comment, keyed by its post.
func (b *builder) commentMetrics() ([]Message, error) {
	var s commentMetrics
	if err := b.decode(&s); err != nil {
		return nil, err
	}
	key := postID(s.ParentID)
	e := &contentv1.Engagement{Likes: count(s.Counts.LikesMin, s.Counts.LikesText)}
	if s.ThreadID == nil || *s.ThreadID == "" {
		e.Replies = exactCount(s.Counts.ReplyCount)
	}
	m := &contentv1.Metrics{Envelope: b.envelope("metrics", commentID(b.row.EntityID), b.row.EntityID, false),
		TargetKind: "comment", ParentId: key, Counts: e}
	finish(m.Envelope, m, false)
	return []Message{{Topic: TopicMetrics, Key: key, Msg: m}}, nil
}

// playlist: content.playlist with its videos, removed ones recorded.
func (b *builder) playlist() ([]Message, error) {
	var v playlistRow
	if err := b.decode(&v); err != nil {
		return nil, err
	}
	id := playlistID(v.ID)
	removed := v.RemovedAt != nil
	p := &contentv1.Playlist{Envelope: b.envelope("playlist", id, v.ID, removed), AccountId: accountID(v.ChannelID)}
	if removed {
		p.Status = gone(*v.RemovedAt, "not_found")
	} else {
		p.Type = contentv1.PlaylistType_PLAYLIST_TYPE_PLAYLIST
		p.Title, p.Description = text(v.Title), text(v.Description)
		for _, it := range v.Items {
			pos := it.Position - 1 // yts counts from 1, the contract's examples from 0
			if pos < 0 {
				pos = 0
			}
			item := &contentv1.PlaylistItem{PostId: postID(it.VideoID), Position: pos}
			if it.RemovedAt != nil {
				item.Status = gone(*it.RemovedAt, "removed_from_playlist")
			}
			p.Items = append(p.Items, item)
		}
		p.ItemCount = exactCount(v.VideoCount)
		p.Status = visible()
		p.Url = playlistURL(v.ID)
	}
	finish(p.Envelope, p, true)
	return []Message{{Topic: TopicPlaylist, Key: id, Msg: p}}, nil
}

// author: content.account for a commenter yts doesn't monitor (known from
// their comments; profile read now and then). A listed channel's own
// events describe it instead.
func (b *builder) author() ([]Message, error) {
	var a authorRow
	if err := b.decode(&a); err != nil {
		return nil, err
	}
	if a.Monitored {
		return nil, nil
	}
	id := accountID(a.ID)
	m := &contentv1.Account{Type: contentv1.AccountType_ACCOUNT_TYPE_CHANNEL, Url: channelURL(a.ID),
		Handle: deref(a.Handle), DisplayName: deref(a.DisplayName), Status: visible(),
		Scope: contentv1.CollectionScope_COLLECTION_SCOPE_NOT_MONITORED}
	if !strings.HasPrefix(m.Handle, "@") {
		// Comments show the name; only "@…" is a handle.
		m.DisplayName, m.Handle = m.Handle, ""
	}
	if p := a.Profile; p != nil {
		if h := deref(p.Handle); h != "" {
			m.Handle = h
		}
		if n := deref(p.DisplayName); n != "" {
			m.DisplayName = n
		}
		m.Bio = text(p.Description)
		m.Links = links(p.Links, contentv1.LinkSource_LINK_SOURCE_ABOUT_PANEL)
		m.StatedCountry = deref(p.Country)
		m.CreatedAt = date(p.JoinedAt)
		m.Counts = &contentv1.AccountCounts{Followers: atLeast(p.SubscribersMin), Posts: atLeast(p.VideosMin)}
	}
	// Without a handle or a name there is little to say: what is known.
	m.Envelope = b.envelope("account", id, a.ID, m.Handle == "" && m.DisplayName == "")
	finish(m.Envelope, m, true)
	return []Message{{Topic: TopicAccount, Key: id, Msg: m}}, nil
}

// fromAPI: a video first stored from an official-API search hit and never
// read on the website, whose title and time are still the API's (the
// YouTube terms: the API's data never reaches the platform's events).
func fromAPI(v videoRow) bool {
	return v.Source != nil && *v.Source == "api_search" && v.LastChecked == nil
}
