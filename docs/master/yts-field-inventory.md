# yts: field inventory

Written by the yts session on 6 November 2026 (Iraq time), from the code at `main` `3db1415`. Every example value is synthetic.
Percentages marked "Phase 0" were measured on 4 November 2026 on 37 channels and 228 videos, with the same parsers (`internal/ytparse`).

**How to read this:** yts has two programs.
- **`ytcollect`** is the production collector. It is built and tested but **not running anywhere yet**. Everything below describes it.
- **`ytprobe`** is the Phase 0 measurement kit, running on the shared pilot server. It keeps counts, hashes and public titles only, not
  content (section 4).

## 1. Overview

- **Service:** yts, [Abdullah95k/securebrand-microservices-youtube](https://github.com/Abdullah95k/securebrand-microservices-youtube),
  commit `3db1415`.
- **Access method:** a mix, always **logged out**, never a browser.
  - **YouTube's own web API ("InnerTube"), over plain HTTP:** `youtubei/v1/browse` (channel page, About panel, tabs, community posts,
    playlists), `next` (watch data, comments, replies), `player` (tags, category, exact publish time) and `search` (website search).
  - **Plain web requests:** the channel RSS feed (`/feeds/videos.xml`), the home page once per IP (for the anonymous visitor ID every
    logged-out browser gets), handle and URL resolution, and the image CDNs (`i.ytimg.com`, `yt3.ggpht.com`).
  - **The official Data API v3:** `search.list` only, for discovery. `videos.list` was used once, for the Phase 0 accuracy check.
  - Requests ask for the Arabic interface in Iraq (`hl=ar`, `gl=IQ`), so texts YouTube writes itself (country names, "Joined …",
    "3 days ago", badges) are in Arabic.
- **Entities, with their names in the code:**
  - channel = account;
  - video = post (kind `video`, `short` or `live`);
  - community post = post (`posts`);
  - comment and reply = comment (one table, replies have `thread_id`);
  - author = comment author;
  - author profile = the commenter's own channel page;
  - playlist;
  - search hit and candidate = discovery output;
  - media = saved images;
  - snapshots = account and post metrics.
- **What can't be collected logged out:** members-only content; the player data of age-restricted videos; transcripts and captions;
  video and audio streams; who liked what; exact comment times.
- **How often:** by channel tier (A–D for Iraq channels by size and relevance, M for "mention" channels where only Iraq-relevant items
  are collected). The `fetched` line under each table gives the schedule. The full table is in `docs/plan.md` §8.6
  (`internal/policy/policy.go`).
- **Nothing is published to Kafka today.** "Published at" names the webhook payload field where one exists. The webhook outbox is
  `webhook_events`, filled by database triggers. Events: `video.relevant`, `post.relevant`, `channel.listed` and `erasure`.

## 2. Tables

The **Origin** column says where each field comes from. **InnerTube** means YouTube's scraped web API. **Data API** means the official
API. "Collector" means yts works it out.

### channel (unified: account)

**Fetched:** Channel page + About panel: tier A every 6 h, B daily, C every 3 days, D and M (mention) weekly. Discovery candidates once, at the candidate check.

| Field | Meaning | Type | Synthetic example | Present | Origin | Source | Stored at | Published at | History | Personal data | Notes |
|---|---|---|---|---|---|---|---|---|---|---|---|
| `channels.id` | The channel's ID | text | UCfake0000000000000001 | always (100%) | InnerTube | browse (channel page): metadata.channelMetadataRenderer.externalId | channels.id | webhook channel.listed .channel_id; video.relevant .channel_id | key | sometimes | 24 characters, starts with UC. Stable for the channel's life. Personal data when the channel is a person's. |
| `handle` | The @handle | text | @fake_channel_1 | always (100%) | InnerTube | browse: metadata.vanityChannelUrl, else header metadata parts | channels.handle (unique, case-insensitive) | webhook channel.listed .handle | versions | sometimes | Can change. Each check compares it; a change is logged in channel_changes. |
| `title` | Display name | text | Example Channel | always (100%) | InnerTube | browse: metadata.title | channels.title | webhook channel.listed .title; video.relevant .channel_title | versions | sometimes | Changes logged in channel_changes. |
| `description` | The channel description (full) | text | example text | often (92%) | InnerTube | About panel: aboutChannelViewModel.description; else metadata.description | channels.description | not published | versions | sometimes | Stored verbatim, so links typed into it are kept as text. They are not extracted into a list. |
| `keywords` | The channel's own keyword field | text | fake keyword list | unknown | InnerTube | browse: metadata.keywords | channels.keywords | not published | versions | no | Not measured in Phase 0. |
| `country` | The country the channel states | text | Iraq (in Arabic script) | often (95%) | InnerTube | About panel: country | channels.country | not published | versions | no | A country name in the interface language (we request hl=ar, gl=IQ), not an ISO code. |
| `joined_text` | Creation date, as shown | text | Joined 1 Jan 2015 (in Arabic) | often (97%) | InnerTube | About panel: joinedDateText | channels.joined_text | not published | latest | no | Day precision, text in the interface language. The column channels.joined_at (date) exists but is never filled. |
| `links` | The About panel's external links | list of {title, url} | [{"title":"Website","url":"example.com/fake"}] | often (89%) | InnerTube | About panel: channelExternalLinkViewModel (title, link) | channels.links (jsonb) | not published | latest | sometimes | The link as the panel displays it (host and path), not YouTube's redirect URL. Replaced when a new non-empty list is read; old lists are not kept (gap). |
| `subscribers` | Subscriber count | int64 (lower bound) + text | 12000 / '12K subscribers' | always (100%) | InnerTube | browse header metadata parts; About panel subscriberCountText | channels.subscribers_min, subscribers_text; channel_snapshots | not published | snapshots | no | Abbreviated (approximate): stored as the lower bound plus the text. Hidden counts are left empty. |
| `videos` | Video count | int64 (lower bound) | 345 | unknown | InnerTube | browse header; About panel videoCountText | channels.videos_min; channel_snapshots.videos_min | not published | snapshots | no | Can be abbreviated. |
| `views` | Total channel views | int64 | 12345678 | often (97%) | InnerTube | About panel: viewCountText | channels.views_min; channel_snapshots.views_min | not published | snapshots | no | Normally exact. |
| `avatar_url` | Profile picture URL | text (URL) | https://yt3.ggpht.example/fake-avatar | always (100%) | InnerTube | browse: metadata.avatar (largest) | channels.avatar_url; the image in object storage (media kind avatar) | not published | versions | sometimes | No expiry in the URL; it changes when the picture changes. Images are saved only when storage is configured (off on the pilot). |
| `banner_url` | Banner URL | text (URL) | https://yt3.ggpht.example/fake-banner | often (95%) | InnerTube | browse header (banner) | channels.banner_url; media kind banner | not published | versions | no |  |
| `family_safe` | YouTube's family-safe flag | bool | true | unknown | InnerTube | browse: metadata.isFamilySafe | channels.family_safe | not published | latest | no |  |
| `tabs` | Which tabs exist (Videos, Shorts, Live, Posts, Playlists) and how to open them | map | {videos, shorts, streams} | always (100%) | InnerTube | browse: tabRenderer | not stored (kept in memory for 24 h) | not published | latest | no | Dropped: used only to read the tabs. |
| `canonical_url` | The channel's canonical URL | text | https://www.youtube.com/@fake_channel_1 | often | InnerTube | About panel: canonicalChannelUrl | not stored | not published | latest | no | Dropped: rebuilt from the ID or handle. |
| `verified` | Verified badge on the channel | bool | true | unknown | InnerTube | channel header badge | not collected | not published | latest | no | Gap: not parsed for channels (it is for commenters). |

### channel_snapshot (unified: account_snapshot)

**Fetched:** Every channel check (see channel).

| Field | Meaning | Type | Synthetic example | Present | Origin | Source | Stored at | Published at | History | Personal data | Notes |
|---|---|---|---|---|---|---|---|---|---|---|---|
| `channel_snapshots` | One row per check: subscribers_min, subscribers_text, videos_min, views_min, taken_at | row | UCfake…, 2026-11-05T10:00:00Z, 12000, '12K', 345, 12345678 | always | InnerTube | as for channel | channel_snapshots (partitioned by month) | not published | snapshots | no | taken_at is UTC. |
| `channel_changes` | One row per changed field: field, old_value, new_value, changed_at | row | title: 'Old Name' → 'Example Channel' | sometimes | InnerTube | comparison on each check | channel_changes | not published | versions | sometimes | Fields tracked: handle, title, description, keywords, country, avatar_url, banner_url. Not links (gap). |

### video (unified: post)

**Fetched:** Seen first in a listing (RSS feed, Videos/Shorts/Live tabs, search). Details (next + player) on the first check. Stats by age, e.g. tier A every 30 min under 2 days, 6 h under 30 days, weekly under a year, monthly after.

| Field | Meaning | Type | Synthetic example | Present | Origin | Source | Stored at | Published at | History | Personal data | Notes |
|---|---|---|---|---|---|---|---|---|---|---|---|
| `id` | Video ID | text | fakeVid0001 | always (100%) | InnerTube | every listing; next; player | videos.id | webhook video.relevant .video_id | key | no | 11 characters. Stable. Shorts and live streams use the same ID space. |
| `channel_id` | Owner channel | text | UCfake0000000000000001 | always (100%) | InnerTube | next (owner); listing | videos.channel_id | webhook video.relevant .channel_id | key | sometimes |  |
| `kind` | video, short or live | enum: video \| short \| live | short | always (100%) | InnerTube | listing (tab, overlay badge) + player isLive / isLiveContent + 'watching now' | videos.kind | webhook video.relevant .kind | latest | no |  |
| `live_status` | Live state | enum: none \| upcoming \| live \| was_live | was_live | always (100%) | InnerTube | listing badges, player isLive / isLiveContent, watch 'watching now' | videos.live_status | not published | latest | no | A finished stream becomes was_live. |
| `title` | Title | text | example title | always (100%) | InnerTube | next: videoPrimaryInfoRenderer.title (else player videoDetails.title; listings and RSS on first sight) | videos.title | webhook video.relevant .title | versions | no | Edits logged in video_changes. |
| `description` | Description (full) | text | example text | often (87%) | InnerTube | next: attributedDescription.content (else player shortDescription) | videos.description | not published | versions | no | Full text, not the 'more' preview. Edits logged in video_changes. Links in it are kept as text, not extracted. |
| `tags` | The uploader's tags | list of text | ["fake tag 1","fake tag 2"] | sometimes (63%) | InnerTube | player: videoDetails.keywords | videos.tags | not published | versions | no | Only when the player answers (85% in Phase 0); some videos have none. Edits logged in video_changes. |
| `category` | YouTube category | text | News & Politics | often (85%) | InnerTube | player: microformat.category | videos.category | not published | versions | no | Only from the player. |
| `language` | The video's stated language | — | — | unknown | InnerTube | player (defaultAudioLanguage) not parsed | not collected | not published | — | no | Not collected. Analysis detects language from the text. |
| `duration_s` | Length in seconds | int | 754 | always (94%) | InnerTube | player lengthSeconds (exact); listing lengthText | videos.duration_s | not published | latest | no | Seconds. |
| `published_at` | When it was published | timestamp + precision | 2026-11-05T07:00:00Z, precision 0 | often (85%) | InnerTube | player: microformat.publishDate (date-time, exact); else the listing's relative time ('3 hours ago') | videos.published_at, published_precision_s | webhook video.relevant .published_at | latest | no | precision_s 0 = exact; 43200 = date only; larger = a relative-time estimate. Only replaced by a more precise value. UTC. |
| `published_text` | The date line as shown | text | 'Premiered 5 Nov 2026' (in Arabic) | always (100%) | InnerTube | next: dateText | videos.published_text | not published | latest | no | Interface language (hl=ar). |
| `upload_date` | Upload date (as opposed to publish date) | text | 2026-11-05T06:00:00-08:00 | often | InnerTube | player: microformat.uploadDate | not stored | not published | — | no | Dropped: publish date is used. |
| `views` | View count | int64 | 12345 | always (100%) | InnerTube | player viewCount (exact); else next originalViewCount (exact); listing (abbreviated) | videos.views; video_snapshots.views | not published | snapshots | no | Exact in 100% of Phase 0 checks; matched the official API (median difference 0%). |
| `likes` | Like count | int64 (lower bound) + text | 321 / '321 likes' | always (100%) | InnerTube | next: like button accessibility text (exact); else its title | videos.likes_min, likes_text; video_snapshots.likes_min | not published | snapshots | no | Exact in 100% of Phase 0 checks. Hidden likes are left empty. |
| `comment_count` | Comment count | int64 (lower bound) + text | 1200 / '1.2K' | often (70%) | InnerTube | next: comments header (commentsCount, then countText) | videos.comments_min, comments_text; video_snapshots.comments_min | not published | snapshots | no | 70% on 4 Nov, before the parser fix of 4 Nov (PR #39); not re-measured since. Can be abbreviated. |
| `comments_disabled` | Comments are off | bool | false | always | InnerTube | derived: no comment-section token and no count | videos.comments_disabled | not published | latest | no | 10 of 228 Phase 0 videos (4%) had no comment section. |
| `family_safe` | YouTube's family-safe flag | bool | true | often (85%) | InnerTube | player: microformat.isFamilySafe | videos.family_safe | not published | latest | no | The nearest signal to an age restriction. An age-gated video's player answers 'login required': we can't read it, and the status itself is dropped (gap). |
| `members_only` | Members-only video | — | — | logged_in_only | InnerTube | listing badge not parsed | not collected | not published | — | no | Members-only content is not viewable logged out. The badge in listings isn't parsed (gap). |
| `player_unavailable` | Tags and category could not be read | bool | false | always | InnerTube | collector state | videos.player_unavailable | not published | latest | no | The player is asked only on the first check, while live, and for a week while category is missing. |
| `chapters` | Chapters | list of {title, start_sec} | [{"title":"Intro","start_sec":0}] | rare (1%) | InnerTube | next: chapterRenderer / macroMarkersListItemRenderer | videos.chapters (jsonb) | not published | latest | no | Seconds. |
| `caption_langs` | Caption languages listed | list of text | ["ar","ar (auto)"] | rare (0%) | InnerTube | player: captionTracks | videos.caption_langs | not published | latest | no | 0% in Phase 0 (the player listed none). Caption file URLs are dropped: fetching them needs anti-bot tokens. |
| `transcript` | Transcript text | — | — | unknown (0%) | InnerTube | get_transcript | not collected | not published | — | no | Phase 0 E12: 0 of 23 transcript requests returned text without an anti-bot token. Dropped (no-go). |
| `thumbnail` | Thumbnail image | image (JPEG) | https://i.ytimg.example/vi/fakeVid0001/hqdefault.jpg | always | InnerTube | built from the ID: i.ytimg.com/vi/<id>/hqdefault.jpg (480×360) | media (kind thumb) + object storage | not published | versions | no | Saved only when storage is configured (off on the pilot). Checked again after 7 days; a new image is a new row (by SHA-256). |
| `live_viewers` | Viewers now, while live | int64 | 1500 | sometimes | InnerTube | next: 'N watching now' | video_snapshots.live_viewers | not published | snapshots | no | Every 5 min (tiers A, B) or 15 min while live. |
| `scheduled_start` | Start time of an upcoming stream or premiere | timestamp | 2026-11-06T18:00:00Z | unknown | InnerTube | not parsed | videos.scheduled_start (never filled) | not published | — | no | Gap: column exists, not filled. |
| `rss_extras` | RSS entry description, thumbnail, views, likes, updated time | various | — | always | InnerTube | feeds/videos.xml | not stored | not published | — | no | Dropped: the watch page gives the same, fresher. The RSS published time IS used (exact). |
| `removed_at` | When the video disappeared | timestamp | 2026-11-05T12:00:00Z | rare | InnerTube | derived: two missed checks in a row | videos.removed_at, missed_checks | not published | latest | no | Cleared if it comes back. |
| `source` | How we first saw it (provenance) | enum: web \| tab \| rss \| websub \| web_search \| api_search | rss | always | collector | collector | videos.source | not published | key | no | Set on first sight and never changed. An 'api_search' row starts with the Data API's title and publish time; the scraped read queued at once replaces them. If that read never succeeds, the API values stay (a gap against the API's 30-day rule). |
| `relevant, matched_keywords` | Iraq relevance and why | bool, list of text | true, ["place-baghdad"] | always | InnerTube | collector (keyword matcher on title, description, tags) | videos.relevant, matched_keywords | webhook video.relevant .matched_keywords | latest | no | Kurdish-language text counts on its own (lang-ckb, D28). |

### video_snapshot (unified: post_metrics)

**Fetched:** Every video check (see video).

| Field | Meaning | Type | Synthetic example | Present | Origin | Source | Stored at | Published at | History | Personal data | Notes |
|---|---|---|---|---|---|---|---|---|---|---|---|
| `video_snapshots` | One row per check: views, likes_min, comments_min, live_viewers, taken_at | row | fakeVid0001, 2026-11-05T10:00:00Z, 12345, 321, 1200, null | always | InnerTube | as for video | video_snapshots (partitioned by month) | not published | snapshots | no | Absent counts are NULL, never 0. |
| `video_changes` | One row per changed field: field, old_value, new_value, changed_at | row | title: 'old title' → 'example title' | sometimes | InnerTube | comparison on each check | video_changes | not published | versions | no | Fields tracked: title, description, category, tags. |

### comment (unified: comment)

**Fetched:** All comments on the first check (newest first, up to 20,000 per video); then newest-first scans by age (tier A hourly under 2 days … monthly up to a year); replies again when a reply count grows; full sweeps at 7 and 30 days (A, B, M) or 30 days (C, D) to catch edits and deletions.

| Field | Meaning | Type | Synthetic example | Present | Origin | Source | Stored at | Published at | History | Personal data | Notes |
|---|---|---|---|---|---|---|---|---|---|---|---|
| `id` | Comment ID | text | UgzFAKE00000000000001 (reply: UgzFAKE00000000000001.FAKEreply01) | always (100%) | InnerTube | next (comments continuation): commentEntityPayload.properties.commentId | comments.id | not published | key | no | Stable. |
| `parent_kind, parent_id` | The video or community post it is on | enum video \| post, text | video, fakeVid0001 | always (100%) | InnerTube | the request | comments.parent_kind, parent_id | not published | key | no | Always has a post (video or community post). |
| `thread_id` | For a reply: the top-level comment | text | UgzFAKE00000000000001 | always | InnerTube | replyLevel and the thread the replies were read from | comments.thread_id (NULL for top level) | not published | key | no | YouTube has one reply level; a reply to a reply is a reply to the thread, with an @mention in its text. |
| `text` | Comment text | text | example text | always | InnerTube | properties.content.content | comments.text; every version in comment_versions | not published | versions | yes | Full text. A changed text is a new version; all versions are kept. |
| `author_id` | The commenter's channel ID | text | UCfakeAuthor000000000001 | often | InnerTube | author.channelId | comments.author_id; authors.id | not published | key | yes |  |
| `author_name` | The commenter's name (today the @handle) | text | @fake_user_1 | often | InnerTube | author.displayName | comments.author_name and author_handle (same value); authors.handle, display_name | not published | latest | yes | A rename shows up on the next read (authors row updated; old comments keep the old name). |
| `author_avatar_url` | Commenter picture URL | text (URL) | https://yt3.ggpht.example/fake-user | often | InnerTube | author.avatarThumbnailUrl | comments.author_avatar_url; authors.avatar_url; media kind author_avatar | not published | latest | yes | Image saved only when storage is configured. |
| `author_is_creator` | Written by the video's channel | bool | false | always | InnerTube | author.isCreator, or the author is the video's channel | comments.author_is_creator | not published | latest | no |  |
| `author_verified` | Commenter has the verified badge | bool | false | sometimes | InnerTube | author.isVerified | comments.author_badges (jsonb); author_badges row 'verified' | not published | latest | yes |  |
| `author_member_badge` | Channel-membership badge | text | Member (2 years) | rare | InnerTube | author.sponsorBadgeA11y | comments.author_badges; author_badges row 'member' (per channel) | not published | latest | yes | Interface language. |
| `likes` | Comment likes | int64 (lower bound) + text | 15 / '15' | always | InnerTube | toolbar.likeCountNotliked | comments.likes_min, likes_text | not published | latest | no | YouTube shows no number at zero; we store 0 then. Overwritten on each read: no comment metrics history (gap). 0% abbreviated in Phase 0. |
| `reply_count` | Number of replies | int | 3 | always | InnerTube | toolbar.replyCount | comments.reply_count | not published | latest | no | Overwritten on each read. |
| `published_at` | When it was posted (estimated) | timestamp + precision | 2026-11-02T10:00:00Z, precision 86400 | always | InnerTube | properties.publishedTime ('3 days ago') | comments.published_at, published_precision_s | not published | latest | no | Relative time only. Phase 0 precision: hour 4%, day 5%, week 1%, month 28%, year 61% (backfill of old videos). A later read with better precision replaces it. The text itself is dropped. |
| `edited` | Marked '(edited)' | bool | false | sometimes | InnerTube | publishedTime suffix | comments.edited; comment_versions.edited | not published | versions | no | Once true, stays true. |
| `pinned` | Pinned by the creator | bool | false | rare | InnerTube | pinnedText / pinnedCommentBadge | comments.pinned | not published | latest | no | 6 of 12,683 in Phase 0. |
| `hearted` | Hearted by the creator | bool | false | rare | InnerTube | toolbar heartState / creatorHeartRenderer | comments.hearted | not published | latest | no | 74 of 12,683 in Phase 0. |
| `deleted_at` | When it disappeared | timestamp | 2026-11-05T12:00:00Z | rare | InnerTube | derived: missing from two complete sweeps in a row | comments.deleted_at, missed_sweeps | not published | latest | no | Text and author are kept (D18). |
| `version, first_seen, last_seen` | Collector bookkeeping | int, timestamps | 2, … | always | InnerTube | collector | comments.version, first_seen, last_seen | not published | latest | no |  |

### comment_version (unified: comment)

**Fetched:** Whenever a comment's text changes.

| Field | Meaning | Type | Synthetic example | Present | Origin | Source | Stored at | Published at | History | Personal data | Notes |
|---|---|---|---|---|---|---|---|---|---|---|---|
| `comment_versions` | Every text we saw: comment_id, version, seen_at, text, edited | row | UgzFAKE…, 2, 2026-11-05T10:00:00Z, 'example text (edited)', true | sometimes | InnerTube | comment reads | comment_versions (partitioned by month) | not published | versions | yes |  |

### author (unified: author)

**Fetched:** Updated from every comment read.

| Field | Meaning | Type | Synthetic example | Present | Origin | Source | Stored at | Published at | History | Personal data | Notes |
|---|---|---|---|---|---|---|---|---|---|---|---|
| `authors` | One row per commenter: id, handle, display_name, avatar_url, first_seen, last_seen, comments_seen, relevant_comments, enriched_at | row | UCfakeAuthor…, @fake_user_1, …, 12, 3 | always | InnerTube | comments | authors | not published | latest | yes | relevant_comments orders enrichment. |
| `author_badges` | Badges: author_id, badge (verified \| member), detail, channel_id, first_seen, last_seen | row | UCfakeAuthor…, member, 'Member (2 years)', UCfake… | sometimes | InnerTube | comments | author_badges | not published | latest | yes | Membership is per channel. |

### author_profile (unified: account)

**Fetched:** Commenters' own channel pages, with spare capacity only (lowest priority), most active commenters first; again every 6 months (active) or yearly.

| Field | Meaning | Type | Synthetic example | Present | Origin | Source | Stored at | Published at | History | Personal data | Notes |
|---|---|---|---|---|---|---|---|---|---|---|---|
| `author_profiles` | One row per read: handle, display_name, description, country, links, subscribers_min, videos_min, avatar_url, banner_url, fetched_at | row | UCfakeAuthor…, @fake_user_1, 'example text', … | always | InnerTube | browse (channel page) + About panel, as for channels | author_profiles (partitioned by month) | not published | snapshots | yes | Join date, total views and keywords are read but dropped here. A commenter with uploads of their own becomes a discovery candidate. |

### community_post (unified: post)

**Fetched:** Posts tab: tier A daily, B every 3 days, C weekly, D monthly, M weekly; newest first until a page brings nothing new (at most 5 pages; the first read up to 50). Comments of new posts are read too.

| Field | Meaning | Type | Synthetic example | Present | Origin | Source | Stored at | Published at | History | Personal data | Notes |
|---|---|---|---|---|---|---|---|---|---|---|---|
| `id` | Post ID | text | UgkxFAKE000000000000001 | always (100%) | InnerTube | browse (Posts tab): backstagePostRenderer.postId | posts.id | webhook post.relevant .post_id | key | no |  |
| `channel_id` | Owner channel | text | UCfake0000000000000001 | always (100%) | InnerTube | the tab | posts.channel_id | webhook post.relevant .channel_id | key | sometimes |  |
| `text` | Post text | text | example text | often | InnerTube | contentText | posts.text | webhook post.relevant .text (first 2,000 characters) | latest | no | Overwritten: edits are not versioned (gap). Unknown whether very long posts are cut on the tab. |
| `published_at` | When posted (estimated) | timestamp + precision + text | 2026-11-03T00:00:00Z, 86400, '2 days ago' | always | InnerTube | publishedTimeText | posts.published_at, published_precision_s, published_text | webhook post.relevant .published_at | latest | no | Relative time only. |
| `likes` | Likes | int64 (lower bound) + text | 1200 / '1.2K' | often | InnerTube | voteCount | posts.likes_min, likes_text; post_snapshots.likes_min | not published | snapshots | no | Abbreviated. A missing number is stored as 0 (YouTube shows none at zero). |
| `comments` | Comment count | int64 (lower bound) + text | 45 / '45' | often | InnerTube | reply button | posts.comments_min, comments_text; post_snapshots.comments_min | not published | snapshots | no |  |
| `has_image, has_poll, has_video` | What is attached | bool ×3 | true, false, false | always | InnerTube | backstageAttachment | posts.has_image, has_poll, has_video | not published | latest | no | Only yes/no. The images, poll options and the linked video ID are dropped; posts.attachments exists but is never filled (gap). |
| `image_urls` | The attached images (fix 21) | list of text | ["https://yt3.ggpht.example/fakepost=s1080"] | sometimes | InnerTube | backstageImageRenderer (one, or several in postMultiImageRenderer): the largest size of each | posts.image_urls; the files: media kind post_image + object storage | content.media (role CONTENT, owner the post) and the hand-off | latest | sometimes (a picture can show people) | A page that shows no images keeps the stored list. Poll options and the linked video ID are still dropped. |

### playlist (unified: other)

**Fetched:** Playlists tab and items: tiers A and B weekly, others monthly.

| Field | Meaning | Type | Synthetic example | Present | Origin | Source | Stored at | Published at | History | Personal data | Notes |
|---|---|---|---|---|---|---|---|---|---|---|---|
| `playlists` | id, channel_id, title, video_count, relevant, first_seen, last_checked, removed_at | row | PLfake000000000000000001, UCfake…, 'example title', 6 | always | InnerTube | browse (Playlists tab): lockupViewModel | playlists | not published | latest | no | playlists.description exists but is never filled. |
| `playlist_items` | playlist_id, video_id, position, first_seen, last_seen, removed_at | row | PLfake…, fakeVid0001, 3 | always | InnerTube | browse (playlist) | playlist_items | not published | latest | no | Removal is recorded, not deleted. |

### search_hit (unified: discovery_candidate)

**Fetched:** Website search: priority keywords hourly, others every 6 h. Data API: priority keywords every 3 h, within the daily cap.

| Field | Meaning | Type | Synthetic example | Present | Origin | Source | Stored at | Published at | History | Personal data | Notes |
|---|---|---|---|---|---|---|---|---|---|---|---|
| `api: id.videoId, snippet.channelId, snippet.title, snippet.publishedAt` | A video the official search found | text, text, text, timestamp | fakeVid0001, UCfake…, 'example title', 2026-11-05T07:00:00Z | always | **Data API** | search.list (part=snippet, type=video, order=date, regionCode=IQ, relevanceLanguage=ar, publishedAfter, maxResults=50) | search_hits (engine 'api') — deleted after 30 days; for a listed channel's upload also a videos row (source api_search); otherwise a candidates row | not published | latest | no | snippet.channelTitle is read and dropped. Exact publish time (written to videos with 1-day precision, so the scraped exact time replaces it). |
| `web: video ID, title, channel ID, views, relative time, duration, kind; channel results: ID, title, subscribers` | A video or channel the website search found | various | fakeVid0001, 'example title', UCfake… | always | InnerTube | search (sorted by upload date) | search_hits (engine 'web'), kept | not published | latest | no | Channel name is dropped. |
| `search_terms` | Per spelling: query, keyword_id, last_web, last_api, web_hits, api_hits | row | … | always | collector | collector | search_terms | not published | latest | no |  |

### candidate (unified: discovery_candidate)

**Fetched:** Created by a search hit, a commenter with uploads, or the channel graph; checked once (re-checked after 90 days if no match).

| Field | Meaning | Type | Synthetic example | Present | Origin | Source | Stored at | Published at | History | Personal data | Notes |
|---|---|---|---|---|---|---|---|---|---|---|---|
| `candidates` | channel_id, first_found, last_found, hits, sources (api_search \| web_search \| graph \| commenter), status (new \| checked \| listed \| no_match), checked_at, decision | row | UCfake…, …, 3, {web_search,api_search}, listed, 'core: country' | always | collector | derived from search hits, comments and the graph | candidates | not published | latest | sometimes | The check reads the channel by scraping and lists it as core (stated country Iraq, ≥ 30% of recent items match, or its own page matches) or mention. Listed channels get channels.added_by = 'discovery'. |
| `channel_links_graph` | from_channel, to_channel, relation (commenter today; featured, link, mention, collab reserved), weight, first_seen, last_seen | row | UCfakeAuthor…, UCfake…, commenter, 12 | often | collector | derived from stored comments | channel_links_graph | not published | latest | yes | Only 'commenter' edges are filled today. |

### media (unified: media)

**Fetched:** Thumbnail of each new video, checked again after 7 days; avatars and banners when their URL changes; commenter avatars with enrichment. Only when object storage is configured (not on the pilot).

| Field | Meaning | Type | Synthetic example | Present | Origin | Source | Stored at | Published at | History | Personal data | Notes |
|---|---|---|---|---|---|---|---|---|---|---|---|
| `media` | kind (thumb \| avatar \| banner \| author_avatar), entity_id, channel_id, sha256, object_key, source_url, content_type, bytes, first_seen, last_seen | row | thumb, fakeVid0001, UCfake…, 3f…, …, image/jpeg, 25000 | always | InnerTube | i.ytimg.com and yt3.ggpht.com | media + object storage (keyed by SHA-256) | not published | versions | sometimes | Largest image kept: 8 MB. Video and audio files are never downloaded (they need anti-bot tokens). |

### coverage (unified: coverage)

**Fetched:** Continuous.

| Field | Meaning | Type | Synthetic example | Present | Origin | Source | Stored at | Published at | History | Personal data | Notes |
|---|---|---|---|---|---|---|---|---|---|---|---|
| `comments_complete_pct, comments_partial` | Per video: collected ÷ displayed comments; capped at 20,000 | real, bool | 98.5, false | often | collector | comment engine | videos.comments_complete_pct, comments_partial | not published | latest | no | Phase 0: median 100%, p10 100% on uncapped videos. |
| `field_fill_rates` | Per endpoint and field, per 3-hour window: seen, present | row | watch, likes, 120, 120 | always | collector | collector | field_fill_rates | not published | snapshots | no | Alerts on a drop (parser drift). |
| `parse_failures` | endpoint, entity, error, raw_ref | row | watch, video, fakeVid0001, … | rare | collector | collector | parse_failures (raw_ref: the raw answers, kept under `yts/keep/`); a scrubbed structure skeleton in the debug folder | not published | latest | no | Since fix 8, the raw answers behind a failure are kept forever in the raw bucket; `raw_ref` points to them. |
| `schedule` | Per job: kind, entity, priority, due_at, attempts, last_ok, last_error | row | comments, fakeVid0001, 1, … | always | collector | collector | schedule | not published | latest | no | No per-job coverage event yet (gap). |

### Not collected

| What | Why |
|---|---|
| Live chat | Not collected (owner decision D24). |
| Transcripts, captions | Phase 0 E12: no transcript or caption text came back without an anti-bot token. Dropped. |
| Video and audio files | Need anti-bot tokens. Out of scope. |
| Members-only content | Needs a member's login. |
| Who liked a video or comment | Never public. |

### Received but dropped

| Field | Why |
|---|---|
| Player `microformat.uploadDate` | The publish date is used instead. |
| Player `playabilityStatus` (login required, unplayable…) | Only used to set `player_unavailable`. It should be kept (gap). |
| Caption track URLs | Fetching them needs anti-bot tokens. |
| Channel tabs and their parameters | Kept in memory for 24 h, only to read the tabs. |
| About-panel canonical URL | Rebuilt from the ID or handle. |
| RSS entry description, thumbnail, views, likes and "updated" time | The watch page is fresher. The RSS publish time **is** used: it is exact. |
| Channel names in search results and on watch pages | The channel page is the source. |
| Data API `snippet.channelTitle` | The channel page is the source. |
| A comment's "3 days ago" text | Converted into a time and its precision. |
| A commenter profile's join date, total views and keywords | Not needed for enrichment so far. |

## 3. Answers

1. **IDs.**
   - **Formats** (all text, none numeric):
     - channel: `UC` + 22 characters;
     - video: 11 characters;
     - comment: `Ugz…` for top level, `<thread id>.<reply id>` for a reply;
     - community post: `Ugkx…`;
     - playlist: `PL…`.
   - **Stability:** IDs are stable. A channel has one ID.
   - **Handles** (`@name`) can change. Each channel check compares the handle and logs a change in `channel_changes`.
   - **Inputs:** a channel can be registered as a handle, a URL or an ID (`channel_inputs`). It is resolved to its `UC…` ID, and only
     that ID is used as the key.
2. **Time.** All stored times are UTC.
   - **Exact:** a video's publish time from the player (date-time with offset) and from the RSS feed; the Data API's `publishedAt`.
   - **Date only:** the watch page's date line.
   - **Relative ("3 days ago"):** comments, community posts, and videos first seen in a listing. These are stored as an estimate with
     `published_precision_s`: 0 means exact, 43200 means date only, larger means relative.
   - **Refinement:** a later reading with better precision replaces a coarser one. A comment first seen "25 minutes ago" keeps that.
   - **Old comments are coarse:** Phase 0 backfills were 61% year-precision and 28% month-precision.
3. **Text.**
   - **Kept as received:** titles, descriptions, comment and post texts are stored in full, as received. Video descriptions come from
     the full description field, not the "more" preview.
   - **Not kept:** mention and hashtag positions; links inside text (they stay in the text but aren't extracted); any language field.
     YouTube gives no translations logged out.
   - **Lengths:** the longest texts seen weren't measured.
   - **Truncation:** unknown whether the Posts tab cuts very long community posts.
4. **Counts.**
   - **Exact:** video views and likes (100% exact in Phase 0, and matching the official API: median difference 0%); total channel
     views.
   - **Abbreviated ("1.2K"):** subscribers, often comment counts and post likes. Stored as a lower bound (`_min`) plus the text.
   - **Hidden by the owner** (likes, subscribers): left empty (NULL), never 0.
   - **Exception:** comment and post likes are stored as 0 when YouTube shows no number. YouTube shows none at zero, so this is
     correct, but a hidden count would also read 0.
5. **Edits and deletions.**
   - **Video:** title, description, category and tag changes are logged in `video_changes` at the next check. A video missing from two
     checks in a row gets `removed_at`.
   - **Channel:** changes to handle, title, description, keywords, country, avatar and banner are logged in `channel_changes`. Two
     missed checks mark it `removed`.
   - **Comment:**
     - a new text is a new row in `comment_versions`, and "(edited)" is recorded;
     - a comment missing from two complete sweeps (at 7 and 30 days for tiers A, B, M; at 30 days for C, D) gets `deleted_at`;
     - newer comments on old videos are found by monthly scans up to a year;
     - text and author are kept.
   - **Not versioned (gaps):** community post text and About-panel links.
6. **Relationships.**
   - **Comments:** a comment always has a parent (`parent_kind` video or post, `parent_id`). A reply has `thread_id`, the top-level
     comment. YouTube has one reply level: a reply to a reply is in the same thread, with an @mention in its text.
   - **Playlists:** `playlist_items` links playlists to videos.
   - **Not collected:** shares, quotes, co-authors and tagged accounts (YouTube's "collaborations").
   - **Channel graph:** `channel_links_graph` holds commenter → channel edges. Featured-channel and link edges are reserved, not
     filled.
   - **Cross-posts:** not detected.
7. **Authors.** The commenter's channel ID, name (today the @handle), picture URL, verified badge, membership badge and "is the
   creator".
   - **Every author is a channel.** On YouTube, a person and a brand both comment as channels, so the comment alone can't tell them apart.
   - **Enrichment** reads the commenter's own channel page (description, country, links, subscriber and video counts, avatar, banner). A
     commenter with uploads becomes a discovery candidate.
8. **Media.**
   - **What exists:** video thumbnails, channel avatars and banners, commenter avatars, post images.
   - **What we save:** the thumbnail (`hqdefault`, 480×360 JPEG), avatars and banners, and post images (fix 21, the largest size).
     Images are saved to object storage by SHA-256, only when storage is configured: **off on the pilot**. Video and audio are not
     saved.
   - **Sizes:** not measured. A 480×360 thumbnail is roughly 15–40 KB (estimate). The largest image kept is 8 MB.
   - **Expiry:** URLs carry no expiry time; they change when the image changes.
   - **What analysis would need:** see `yts-gaps.md`, Plans.
9. **Iraq signals.**
   - **Keyword match** on titles, descriptions, tags, post and channel text: precision 99% and estimated recall 92% (Phase 0 E11; AI
     labels checked by hand, 25 of 27 agreeing).
   - **Kurdish-language text** counts on its own (D28).
   - **The About panel's stated country:** 35% of census core channels said Iraq.
   - **Channel kind:** core (Iraq channel) or mention (only Iraq-relevant items collected).
   - **Not collected:** location tags and phone codes.
   - The matched keyword IDs are stored with every item (`matched_keywords`).
10. **Profile links.**
    - **Kept:** the full description and every About-panel link (title and displayed URL), from day one, for channels and for enriched
      commenters.
    - **Gaps:** links inside descriptions aren't extracted into a list, and old link lists aren't versioned.
11. **Raw responses** (updated 6 November, REQ-010 fix 8).
    - **Kept,** in the raw bucket (`internal/rawstore`): every page and API answer the worker reads, images aside, gzipped, with the
      parser version, endpoint, URL, status, block signal and the opaque proxy ID.
    - **Failures and a 1% sample** go under `yts/keep/`, kept forever. A parse failure keeps the job's latest answers there and
      records their `raw_ref` in `parse_failures`.
    - **The rest** goes under `yts/30d/`, which the bucket may expire after 30 days. yts never deletes raw files.
    - **Personal data:** a raw answer holds whatever the page shows, commenters' names and comments included. The bucket must be
      private; erasure doesn't reach raw files (the privacy tools are off by the owner's decision).
    - The scrubbed skeleton in the debug folder stays, for the parser developers.
12. **Volumes** (measured in Phase 0 unless marked):
    - **Uploads:** median 3 per channel per month, p90 64–69 (500-channel census, 4–5 Nov).
    - **Comments:** on the newest video, median 10–13 and p90 188–433. 12,683 comments over 228 crawled videos (about 56 per video).
    - **Requests:** 30 per 100 comments. 2 per video for watch and player.
    - **Bytes:** 57 KB compressed per request (median).
    - **Estimate from the plan (§6.2):** about 25 requests per core channel per day and 3.5 per mention channel, after a one-time
      backfill of about 875 requests per core channel.
13. **Privacy.**
    - **Personal data:** comment text, commenter IDs, names, pictures and badges, commenter profiles, and the channels of individuals.
    - **Pseudonymization:** keyed hashes (HMAC-SHA256) are used for the suppression list and, optionally, for exports and webhook payloads
      (`pseudonymize_exports`, off).
    - **Erasure:**
      - by author, channel, video, comment or post, with a suppression list;
      - built and tested, never used;
      - the owner's rule R6 says no deletion, so it stays dormant.

## 4. Phase 0 records (ytprobe), for completeness

They are JSON-lines files in `/var/lib/securebrand-youtube/phase0` on the pilot server, kept (D26):
- `crawl_channels`, `crawl_videos`: counts, sizes, timings and yes/no field flags;
- `detect_feeds`, `detect_sightings`: hashed channel and video IDs, outcomes and delays;
- `discover`, `discover_items`: run totals; per hit the video ID, channel ID and public title (official-API hits deleted after 30 days);
- `census`: per channel counts and signals;
- `accuracy`: official vs scraped numbers (official-API data deleted after 30 days);
- `resweep`: comment IDs and text hashes only;
- `label_sample`, `labels`: 300 public titles with their labels;
- `pace_steps`, `pool_stats`, `transcripts`: request counts and outcomes;
- `skeletons/`: structure only.

No comment text and no commenter identities.

## 5. YAML

`yts-field-inventory.yaml` has the same content. It adds one key, `origin`, per field: InnerTube, the Data API or the collector.
