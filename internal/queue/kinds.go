package queue

// Job kinds. The entity ID is what the job works on.
const (
	KindResolve      = "resolve"       // a channel input (@handle, URL) → channel ID, then listed
	KindChannel      = "channel"       // channel page, About panel, snapshot
	KindTabs         = "tabs"          // Videos, Shorts and Live tabs: uploads the feed missed
	KindBackfill     = "backfill"      // the last 365 days of uploads
	KindRSS          = "rss"           // the channel's feed: new uploads
	KindWebSub       = "websub"        // (re)subscribe to the channel's push notifications
	KindPosts        = "posts"         // the community Posts tab
	KindPlaylists    = "playlists"     // the Playlists tab and the videos in each playlist
	KindVideo        = "video"         // video details and stats
	KindComments     = "comments"      // newest-first scan for new comments
	KindSweep        = "sweep"         // full comment read: edits and deletions
	KindLive         = "live"          // viewers of a running live stream
	KindPostComments = "post_comments" // comments of a community post
	KindEnrich       = "enrich"        // a commenter's public channel page
	KindCandidate    = "candidate"     // a channel discovery found: is it Iraq-relevant?
	KindSearch       = "search"        // one search term on the website (and the official API for priority keywords)
)
