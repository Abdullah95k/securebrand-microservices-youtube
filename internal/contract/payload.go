package contract

import "time"

// The outbox payloads: each table's row as Postgres's to_jsonb writes it
// (internal/outbox), and the counts events' payloads.

type storedLink struct {
	Title string
	URL   string
}

type channelRow struct {
	ID              string       `json:"id"`
	Handle          *string      `json:"handle"`
	Title           *string      `json:"title"`
	Description     *string      `json:"description"`
	Keywords        *string      `json:"keywords"`
	Country         *string      `json:"country"`
	JoinedAt        *string      `json:"joined_at"` // a date
	JoinedText      *string      `json:"joined_text"`
	Links           []storedLink `json:"links"`
	SubscribersMin  *int64       `json:"subscribers_min"`
	SubscribersText *string      `json:"subscribers_text"`
	VideosMin       *int64       `json:"videos_min"`
	ViewsMin        *int64       `json:"views_min"`
	Kind            string       `json:"kind"`
	Tier            *string      `json:"tier"`
	Status          string       `json:"status"`
	AddedBy         *string      `json:"added_by"`
	AddedReason     *string      `json:"added_reason"`
	FamilySafe      *bool        `json:"family_safe"`
	RemovedAt       *time.Time   `json:"removed_at"`
}

type channelCounts struct {
	SubscribersMin  *int64  `json:"subscribers_min"`
	SubscribersText *string `json:"subscribers_text"`
	VideosMin       *int64  `json:"videos_min"`
	ViewsMin        *int64  `json:"views_min"`
}

type channelSnapshot struct {
	Counts    channelCounts  `json:"counts"`
	Observed  *channelCounts `json:"observed"`
	Handle    *string        `json:"handle"`
	Title     *string        `json:"title"`
	Status    string         `json:"status"`
	RemovedAt *time.Time     `json:"removed_at"`
}

type chapter struct {
	Title    string
	StartSec int
}

type videoRow struct {
	ID                  string     `json:"id"`
	ChannelID           string     `json:"channel_id"`
	Kind                string     `json:"kind"`
	Title               *string    `json:"title"`
	Description         *string    `json:"description"`
	PublishedAt         *time.Time `json:"published_at"`
	PublishedPrecisionS *int       `json:"published_precision_s"`
	PublishedText       *string    `json:"published_text"`
	DurationS           *int       `json:"duration_s"`
	Views               *int64     `json:"views"`
	LikesMin            *int64     `json:"likes_min"`
	LikesText           *string    `json:"likes_text"`
	CommentsMin         *int64     `json:"comments_min"`
	CommentsText        *string    `json:"comments_text"`
	CommentsDisabled    bool       `json:"comments_disabled"`
	Tags                []string   `json:"tags"`
	Category            *string    `json:"category"`
	PlayerUnavailable   bool       `json:"player_unavailable"`
	LiveStatus          string     `json:"live_status"`
	ScheduledStart      *time.Time `json:"scheduled_start"`
	Relevant            *bool      `json:"relevant"`
	MatchedKeywords     []string   `json:"matched_keywords"`
	CommentsPartial     bool       `json:"comments_partial"`
	CommentsCompletePct *float64   `json:"comments_complete_pct"`
	LastChecked         *time.Time `json:"last_checked"`
	RemovedAt           *time.Time `json:"removed_at"`
	Source              *string    `json:"source"`
	FamilySafe          *bool      `json:"family_safe"`
	CaptionLangs        []string   `json:"caption_langs"`
	Chapters            []chapter  `json:"chapters"`
}

type videoMetrics struct {
	Counts struct {
		Views        *int64  `json:"views"`
		LikesMin     *int64  `json:"likes_min"`
		LikesText    *string `json:"likes_text"`
		CommentsMin  *int64  `json:"comments_min"`
		CommentsText *string `json:"comments_text"`
	} `json:"counts"`
	Observed *struct {
		Views       *int64 `json:"views"`
		LiveViewers *int64 `json:"live_viewers"`
	} `json:"observed"`
	ChannelID  string `json:"channel_id"`
	LiveStatus string `json:"live_status"`
}

type postRow struct {
	ID                  string     `json:"id"`
	ChannelID           string     `json:"channel_id"`
	Text                *string    `json:"text"`
	PublishedAt         *time.Time `json:"published_at"`
	PublishedPrecisionS *int       `json:"published_precision_s"`
	PublishedText       *string    `json:"published_text"`
	LikesMin            *int64     `json:"likes_min"`
	LikesText           *string    `json:"likes_text"`
	CommentsMin         *int64     `json:"comments_min"`
	CommentsText        *string    `json:"comments_text"`
	Relevant            *bool      `json:"relevant"`
	MatchedKeywords     []string   `json:"matched_keywords"`
	RemovedAt           *time.Time `json:"removed_at"`
	Source              *string    `json:"source"`
}

type postMetrics struct {
	Counts struct {
		LikesMin     *int64  `json:"likes_min"`
		LikesText    *string `json:"likes_text"`
		CommentsMin  *int64  `json:"comments_min"`
		CommentsText *string `json:"comments_text"`
	} `json:"counts"`
	ChannelID string `json:"channel_id"`
}

type commentRow struct {
	ID                  string     `json:"id"`
	ParentKind          string     `json:"parent_kind"`
	ParentID            string     `json:"parent_id"`
	ChannelID           string     `json:"channel_id"`
	ThreadID            *string    `json:"thread_id"`
	AuthorID            *string    `json:"author_id"`
	AuthorHandle        *string    `json:"author_handle"`
	AuthorName          *string    `json:"author_name"`
	AuthorAvatarURL     *string    `json:"author_avatar_url"`
	AuthorIsCreator     bool       `json:"author_is_creator"`
	AuthorBadges        *badges    `json:"author_badges"`
	Text                string     `json:"text"`
	PublishedAt         *time.Time `json:"published_at"`
	PublishedPrecisionS *int       `json:"published_precision_s"`
	LikesMin            *int64     `json:"likes_min"`
	LikesText           *string    `json:"likes_text"`
	ReplyCount          *int64     `json:"reply_count"`
	Pinned              bool       `json:"pinned"`
	Hearted             bool       `json:"hearted"`
	Edited              bool       `json:"edited"`
	DeletedAt           *time.Time `json:"deleted_at"`
}

type badges struct {
	Verified bool   `json:"verified"`
	Member   string `json:"member"`
}

type commentMetrics struct {
	Counts struct {
		LikesMin   *int64  `json:"likes_min"`
		LikesText  *string `json:"likes_text"`
		ReplyCount *int64  `json:"reply_count"`
	} `json:"counts"`
	ParentKind string  `json:"parent_kind"`
	ParentID   string  `json:"parent_id"`
	ThreadID   *string `json:"thread_id"`
}

type playlistRow struct {
	ID          string     `json:"id"`
	ChannelID   string     `json:"channel_id"`
	Title       *string    `json:"title"`
	Description *string    `json:"description"`
	VideoCount  *int64     `json:"video_count"`
	RemovedAt   *time.Time `json:"removed_at"`
	Items       []struct {
		VideoID   string     `json:"video_id"`
		Position  int32      `json:"position"`
		RemovedAt *time.Time `json:"removed_at"`
	} `json:"items"`
}

type authorRow struct {
	ID          string  `json:"id"`
	Handle      *string `json:"handle"`
	DisplayName *string `json:"display_name"`
	AvatarURL   *string `json:"avatar_url"`
	Monitored   bool    `json:"monitored"`
	Profile     *struct {
		Handle         *string      `json:"handle"`
		DisplayName    *string      `json:"display_name"`
		Description    *string      `json:"description"`
		Country        *string      `json:"country"`
		JoinedAt       *string      `json:"joined_at"`
		Links          []storedLink `json:"links"`
		SubscribersMin *int64       `json:"subscribers_min"`
		VideosMin      *int64       `json:"videos_min"`
		AvatarURL      *string      `json:"avatar_url"`
	} `json:"profile"`
}

// sourceTrail is the outbox's provenance (outbox.Provenance).
type sourceTrail struct {
	JobID            string   `json:"job_id"`
	JobKind          string   `json:"job_kind"`
	Operations       []string `json:"operations"`
	RawRefs          []string `json:"raw_refs"`
	ExitCountries    []string `json:"exit_countries"`
	Collector        string   `json:"collector"`
	CollectorVersion string   `json:"collector_version"`
	ParserVersion    string   `json:"parser_version"`
}
