# yts: review of the contract draft v0.1 (REQ-013)

**Written by:** the yts session, 6 November 2026 (Iraq time).
**What I reviewed:** `Abdullah95k/securebrand-contracts`, branch `claude/securebrand-platform-integration-4iiwlr`, commit `3a75c8b`.
**Checked against:** yts `main` `b6df42c`.
**Changes:** none, to code or to either repository. Synthetic examples only.

**Overall:** the draft fits yts well. Every yts entity has a home, and the five things I asked for (entity kind in IDs, time
precision, counts with their text, commenters as accounts, relevance) are in. The changes I'd ask for are small:
- map a video's description to `description`, not `text`;
- add a scope for "accounts known only from content";
- add live start and end times;
- correct about ten availability marks;
- don't publish a `content.post` from an official-API hit before the website read.

## 1. Mapping

**Agree** means the contract field takes the yts value as it is. **Change** gives a reason. Each row says where the value comes from in
yts: the database (table or column) or the parser.

### Account (`content.account.v1`): monitored channels

| yts | Contract | Verdict |
|---|---|---|
| `channels.id` | `envelope.id` = `yt:account:<UC…>`, `native_id` | Agree |
| — | `type` = `ACCOUNT_TYPE_CHANNEL` | Agree |
| `channels.handle` | `handle` | Agree |
| `channels.title` | `display_name` | Agree |
| built from the ID | `url` = `https://www.youtube.com/channel/<id>` | Agree |
| `channels.description` | `bio.text` | Agree. `bio.spans` stays empty until links and mentions are extracted (fix below) |
| `channels.links` (About panel) | `links[]` with `source = LINK_SOURCE_ABOUT_PANEL`, `title`, `display_url`, plus `domain`, `target_platform` and `target_id` worked out by the mapper | Agree. The panel shows the link as host and path; yts keeps that as `display_url` and builds `url` by adding `https://` |
| links typed in the description | `links[]` with `source = LINK_SOURCE_DESCRIPTION` | Agree. Not extracted yet (fix) |
| `channels.country` (Arabic name) | `stated_country`; `stated_country_code` from a name → ISO table | Agree. The table is a small addition |
| `channels.joined_text` | `created_at` (`DAY`, `displayed` = the text) | Agree. Needs the Arabic date parsed (fix) |
| `channels.keywords` | `keywords[]` (split as YouTube writes them: quoted phrases and words) | Agree |
| `channels.avatar_url`, `banner_url` | `picture`, `cover` (`MediaRef`, roles `PROFILE_PICTURE`, `BANNER`) | Agree |
| `channels.family_safe` | `extras["yt.family_safe"]` | **Change:** a new extras key. No account field fits |
| verified badge | `verified` | Agree. Not parsed yet (fix) |
| `channels.status` (`listed`, `dormant`, `removed`…), `removed_at` | `status` (`VISIBLE`, or `DELETED` with `since`) | Agree. A removed channel can be terminated, deleted or hidden: logged out we can't tell which, so `reason = "unavailable_logged_out"` |
| `channels.kind` core / mention | `scope` = `FULL` / `RELEVANT_ONLY` | Agree |
| `channels.tier` (A–D, M) | `extras["yt.tier"]` | **Change:** a new extras key. The platform's hot, warm and long-tail tiers are a different scale |
| `channels.added_by`, `added_reason` | `extras["yt.added_by"]`, `extras["yt.added_reason"]` | **Change:** new extras keys, or leave them out (`Candidate` carries the discovery story) |
| subscribers, videos, views | `counts.followers`, `counts.posts`, `counts.views` | Agree |
| channel matched keywords | `relevance` | Agree (mapping in question 6) |

### Account (`content.account.v1`): commenters' own channels (`author_profiles`)

The same mapping as above, from `author_profiles`: handle, name, description, country, links, counts, avatar, banner. There is no
tier, kind or added-by. The scope is in question 3. The join date isn't stored for commenters today: it would be added.

### AccountSnapshot (`content.account_snapshot.v1`)

| yts | Contract | Verdict |
|---|---|---|
| `channel_snapshots.taken_at` | `envelope.observed_at` | Agree |
| `subscribers_min`, `subscribers_text` | `counts.followers` (`ROUNDED` when abbreviated; `hidden` when the channel hides it) | Agree. yts doesn't yet tell "hidden" apart from "not shown": it stores NULL for both. A hidden count shows no number on the page, so `hidden = true` needs a small parser change |
| `videos_min` | `counts.posts` | Agree |
| `views_min` | `counts.views` | Agree |
| handle, title at that time | `handle`, `display_name` | Agree |

### Post (`content.post.v1`): video, Short, live stream

| yts | Contract | Verdict |
|---|---|---|
| `videos.id` | `yt:post:<id>` | Agree |
| `videos.channel_id` | `account_id` | Agree |
| `videos.kind` video / short / live | `type` `VIDEO` / `SHORT_VIDEO` / `LIVE` | Agree |
| `videos.live_status` | `video.live_status`: none → `NONE`, upcoming → `UPCOMING`, live → `LIVE`, was_live → `ENDED` | Agree. `extras["yt.live_status_raw"]` adds nothing once this mapping is fixed: drop it |
| `videos.published_text` "Premiered …" | `subtype = "premiere"` | **Change:** derive the subtype from the date line; yts doesn't today |
| built | `url` = `https://www.youtube.com/watch?v=<id>` (Shorts: `/shorts/<id>`) | Agree |
| `videos.published_at`, `published_precision_s`, `published_text` | `published_at` (Time) | Agree (question 1) |
| `videos.title` | `title.text` | Agree |
| `videos.description` | **`description.text`**, with `text` empty | **Change:** the field matrix puts the description under `text`. `title` + `description` is how YouTube shapes a video; `text` fits community posts |
| `videos.tags` | `uploader_tags` | Agree |
| hashtags in the title or description | `hashtags` | **Change in the matrix:** they exist on YouTube (X, not —). Not extracted yet |
| @mentions in the description | `mentions`, `description.spans` | **Change in the matrix:** they exist (X, not —) |
| links in the description | `links` (`LINK_SOURCE_DESCRIPTION`), `description.spans` | Agree (X) |
| `videos.category` | `category` | Agree |
| `videos.duration_s` | `video.duration_ms` (× 1000) | Agree |
| `videos.chapters` | `video.chapters` (start × 1000) | Agree |
| `videos.caption_langs` | `video.caption_languages` | Agree. Logged out the list is empty today: see the matrix |
| `scheduled_start` (not filled) | `video.scheduled_start` | Agree (fix) |
| stream start and end | — | **Question:** add `video.live_started_at` and `live_ended_at` (Time)? The player gives both for streams when it answers. Or the extras keys `yt.live_started_at` and `yt.live_ended_at` |
| thumbnail | `media[0]` (role `THUMBNAIL`) | Agree |
| `videos.family_safe` | `labels.family_safe` | Agree |
| age restriction, members-only | `labels.age_restricted`, `labels.members_only` | Agree (fixes; see REQ-004 §4) |
| `videos.comments_disabled` | `flags.comments_disabled` | Agree |
| `playlist_items` | `playlists[]` | Agree |
| views, likes, comment count | `counts` (and `content.metrics`) | Agree |
| `videos.removed_at` | `status` `DELETED` (or `UNAVAILABLE`), `since` | Agree. "Private" and "deleted" look the same logged out: map both to `DELETED`, with YouTube's reason text in `status.reason` |
| `videos.relevant`, `matched_keywords` | `relevance` | Agree (question 6) |
| `comments_complete_pct`, `comments_partial`, `comments_min` | `coverage`: `completeness`, `comments_state` (`PARTIAL` when capped, `DISABLED` when off), `comments_reported`, `comments_collected` | Agree |
| `videos.player_unavailable` | `extras["yt.player_unavailable"]` | Agree |
| player status | `extras["yt.playability_status"]` | Agree. It's dropped today (fix) |
| `videos.source` | `extras["yt.source"]` | Agree |

### Post (`content.post.v1`): community post

| yts | Contract | Verdict |
|---|---|---|
| `posts.id` | `yt:post:<id>`, `type = POST_TYPE_COMMUNITY` | Agree |
| `posts.text` | `text.text` | Agree |
| `posts.published_at`, `published_precision_s`, `published_text` | `published_at` | Agree |
| `posts.likes_min`, `likes_text`, `comments_min`, `comments_text` | `counts.likes`, `counts.comments` | Agree |
| `has_image` | `media[]` (role `CONTENT`) | **Gap:** yts stores only yes/no today; images to be added |
| `has_poll` | `poll` | **Gap:** options to be added (question 4) |
| `has_video` (attached video) | `links[]` to the video | Question 4 |
| a re-shared post (`sharedPostRenderer`) | `reshare` (`SHARE`) | **Gap:** yts reads these posts but not what they share |

### Comment (`content.comment.v1`)

| yts | Contract | Verdict |
|---|---|---|
| `comments.id` | `yt:comment:<id>` | Agree |
| `parent_kind`, `parent_id` | `post_id` = `yt:post:<id>` (videos and community posts share the ID space) | Agree |
| the post's channel | `account_id` | Agree |
| `thread_id` | `root_comment_id` = `parent_comment_id`, `depth = 1` (0 and empty for top level) | Agree |
| `author_id`, `author_name`/`author_handle`, `author_avatar_url` | `author.account` (`id`, `handle`, `picture_url`, `type = CHANNEL`) | Agree. `display_name`: YouTube shows only the @handle on comments today |
| `author_badges` (verified, member) | `author.account.verified`, and `author.badges[]` (`member` with `detail`; `verified` too) | Agree. **Drop `extras["yt.member_badge"]`:** `Badge` already holds it |
| `author_is_creator` | `flags.by_post_owner` | Agree |
| `text` | `text.text`; spans when extracted | Agree |
| `published_at`, `published_precision_s` | `published_at`, with `displayed` = "3 days ago" | Agree. yts drops that text today; the mapper takes it from the parser at write time |
| `pinned`, `hearted`, `edited` | `flags.pinned`, `liked_by_creator`, `edited` | Agree. `edited_at` stays unset: YouTube doesn't say when |
| `likes_min`, `reply_count` | `counts.likes`, `counts.replies` | Agree (question 2) |
| `deleted_at` | a partial event: `status.availability = DELETED`, `since` | Agree |
| `comment_versions` | a new version on each text change (`version` = observed_at µs) | Agree |
| `parent_kind = post` comments | the same, with `post_id` = the community post | Agree |

### Metrics (`content.metrics.v1`)

| yts | Contract | Verdict |
|---|---|---|
| `video_snapshots` (views, likes, comments, live viewers) | `target_kind = post`, `parent_id` = the channel; `views`, `likes`, `comments`, `concurrent_viewers` | Agree |
| `post_snapshots` | the same for community posts | Agree |
| comment likes and replies | `target_kind = comment`, `parent_id` = the post | Agree. yts keeps the latest only today; publishing a Metrics event per changed read gives the series |
| The volume rule (publish when a count changed, at least daily while refreshed) | | Agree. yts has the previous reading at hand when it writes |

### Media (`content.media.v1`) and the hand-off

| yts | Contract | Verdict |
|---|---|---|
| `media` (`kind`, `entity_id`) | `owner_id`, `owner_kind`, `role` (thumb → `THUMBNAIL`, avatar → `PROFILE_PICTURE`, banner → `BANNER`, author_avatar → `PROFILE_PICTURE` on the commenter's account) | Agree |
| `media.source_url` | `source_url`; `source_expires_at` unset (no expiry in YouTube image URLs) | Agree |
| `media.sha256`, `bytes`, `content_type` | `MediaReady` (object, SHA-256, size, MIME) | Agree |
| media ID | a UUIDv5 of (owner, role, variant), stable across reads; a new image (new SHA-256) is a new version | **Change in yts:** yts keys images by (kind, entity, SHA-256) today. The mapper derives the stable ID |
| 7-day thumbnail re-check | a new `MediaReady` when the image changed | Agree |

### Playlist (`content.playlist.v1`)

| yts | Contract | Verdict |
|---|---|---|
| `playlists.id`, `channel_id`, `title`, `video_count` | `yt:collection:<id>`? (see below), `account_id`, `title`, `item_count` | **Question:** the README's ID examples don't name the playlist kind. `common.proto` lists `collection` among entity kinds. Please confirm `yt:collection:PL…` |
| `playlist_items` (video, position, first and last seen, removed) | `items[]` (`post_id`, `position`, `status` `DELETED` with `since` when removed); `added_at` unset (not shown logged out) | Agree |
| `type` | `PLAYLIST_TYPE_PLAYLIST` | Agree |

### Candidate (`discovery.candidate.v1`)

| yts | Contract | Verdict |
|---|---|---|
| `candidates.channel_id` | `account` (`id`, `native_id`), `target_platform = yt` | Agree |
| `first_found` | `found_at`; `found_by = yts` | Agree |
| `sources`: web_search, api_search, commenter, graph | `source`: `KEYWORD_SEARCH`, `OFFICIAL_API_SEARCH`, `COMMENTER`, `GRAPH` | Agree |
| `search_hits` (keyword, video) | `evidence[]` (`keyword_id`, `content_id` = `yt:post:<video>`, `via` = `web_search` / `api_search`). For API hits: no title (question 5) | Agree |
| `status`, `decision` (core, mention, no_match) | `outcome` `ADDED` / `ADDED_RELEVANT_ONLY` / `NOT_RELEVANT` / `ALREADY_LISTED`; `scope`; `outcome_reason`; `decided_at` | Agree |
| About-panel links to other platforms | `Candidate` for that platform, `source = PROFILE_LINK` (the sample `candidate.yt-to-fb.json` is right) | Agree. Not built yet |

### JobCoverage (`collection.coverage.v1`)

| yts | Contract | Verdict |
|---|---|---|
| `schedule` jobs (one per channel check, video stats, comment scan, tab check…) | one `JobCoverage` per finished job | **Change:** yts's jobs are small and many: tens of thousands a day per 1,000 channels. Publish one per channel-level job (channel check, tab check, a video's first read with its comment backfill, full sweeps), and an hourly roll-up per kind for the rest. Otherwise coverage outweighs content |

## 2. The mapping rules in `README.md` that apply to yts

| Rule | Verdict |
|---|---|
| YouTube video → `POST_TYPE_VIDEO` | Right |
| YouTube Shorts → `POST_TYPE_SHORT_VIDEO` | Right |
| Live streams → `POST_TYPE_LIVE` with `video.live_status` | Right. A finished stream stays `LIVE` with `ENDED`, so its ID and type don't change |
| YouTube community posts → `POST_TYPE_COMMUNITY` | Right |
| Replies on YouTube: depth 1, parent = root = the thread | Right |
| IDs use the entity kind, never the subtype | Right, and it matters most for YouTube (upcoming → live → ended) |
| Absent is not zero; Count with exactness and text | Right |
| Every time with its precision | Right |
| Official-API results as IDs only, in `Candidate` | Right (question 5) |
| YouTube subscribers → `followers` | Right |
| Retention 7 days on Kafka | Right. Topics carrying official-API fields never exist under this design |

## 3. Corrections to the YouTube column of the field matrix

| Row | Matrix says | Should be | Why |
|---|---|---|---|
| Account: contact | — | **N** | YouTube has a business e-mail, but showing it needs a sign-in and a CAPTCHA. Never collected (D2) |
| Account: verified | X | X | Right. Shown logged out (REQ-004 §4) |
| Post: text | description **O** | `description` **O** (87%); `text` = community post text **O** | See the mapping |
| Post: hashtags | — | **X** | Videos have hashtags in the title and description, and YouTube links them |
| Post: mentions | — | **X** | @mentions in descriptions are linked to channels |
| Post: coauthors | not collected | **unknown** | YouTube's multi-channel "collaborations" credit exists; whether a logged-out page shows it isn't checked |
| Post: platform_language | X | **unknown** | Not checked whether the logged-out player states it |
| Post: location | — | **unknown** | Videos can carry a recording location; the watch page doesn't show it as far as I know |
| Post: reshare | — | **X** (community posts) | A community post can re-share another channel's post |
| Post: audio | — | **unknown** (Shorts) | Shorts name their sound; not checked logged out |
| Post: caption_languages | R (0%) | **N** (in practice) | The player answered for 85% of videos but listed no caption track at all: tracks seem withheld logged out in 2026 |
| Post: labels.ai_generated, paid_partnership | — | **X** | YouTube shows "altered or synthetic content" and "includes paid promotion" labels; neither is parsed. Logged-out visibility to be checked |
| Post: poll | X | X | Right. Options and total votes are shown; per-option counts only after voting: **N** |
| Counts: comments | O (70%) | O (70% on 4 Nov) | Measured before the parser fix of 4 November; to re-measure |
| Comment: text spans | — | **X** | Comments carry linked @mentions, timestamps, hashtags and URLs |
| Comment: author.account.id | O | O (expected A; not measured) | |
| Account snapshot: followers | can be hidden | can be hidden (yts stores NULL; `hidden` not yet set) | |

## 4. Fields with no place

| yts field | Proposal |
|---|---|
| Channel family-safe flag | extras `yt.family_safe` (account) |
| Channel tier A–D, M | extras `yt.tier` (account) |
| How a channel was added (seed, admin, discovery, enrichment), and why | extras `yt.added_by`, `yt.added_reason` (account), or leave out |
| Stream start and end times | a new field `video.live_started_at` and `live_ended_at` (Time), or extras `yt.live_started_at` and `yt.live_ended_at` (post) |
| Total poll votes | `counts.other["poll_votes"]` (no new key needed) |
| Live viewers at their peak | Not collected; derived from the snapshot series |
| The comment's displayed time text | `published_at.displayed` (no new key) |

Already in the registry and right: `yt.player_unavailable`, `yt.playability_status`, `yt.source`. Drop `yt.member_badge`, which
duplicates `Author.badges`, and `yt.live_status_raw`, which duplicates `video.live_status`.

## 5. What yts can't fill, and what would change how it collects

**Can't fill** (not shown logged out, or not on YouTube):
- account contact, categories, privacy, language, location, and handle or name change times;
- post location, country, captions and transcripts;
- per-option poll votes;
- a comment's `edited_at`;
- the direct parent of a reply to a reply (YouTube gives only the thread).

**Would change how yts collects** (small parser and mapper additions, no new requests, except one):
- text spans: links, hashtags, @mentions and timestamps, from the description's and comment's `commandRuns`;
- the verified badge, the joined date, the age-gate and members-only flags, the playability status;
- the scheduled start and stream start and end;
- community post images, poll options and re-shares;
- the country name → ISO table;
- `hidden` on subscribers;
- comment likes as a series (Metrics);
- `parser_version`.

The exception is `provenance.raw_ref`: it needs raw-response capture (standard E8).

**`provenance.exit_country`:** yts doesn't know its proxies' countries. A `country=XX` tag on each line of the proxy list would give it.
That's one small change, with the owner providing the countries.

**Zero likes on comments** (question 2) needs one parser fix, so a renamed field can't turn into silent zeros.

**Coverage volume:** see JobCoverage above.

**One rule for the relay:** no `content.post` from an official-API search hit (question 5).

## 6. Effort to adopt it

My earlier estimate was 2–3 weeks for the whole Kafka path; the README's "3–4 days after the outbox" is the mapping part only. With
the draft in hand:

| Work | Estimate |
|---|---|
| Outbox table and `version` handling | 2–3 days |
| yt platform protos (`yt.*.v1`) and the writer changes | 4–5 days |
| The relay, publishing `yt.*` and `content.*` in one Kafka transaction | 3 days |
| `content.*` mapping: Time, Count, Relevance split, Link and AccountRef building, coverage roll-ups, media IDs | 4–5 days |
| Conformance in CI, against the samples and the fake YouTube | 1 day |
| End to end with a test Kafka | 2 days |
| The missing fields (spans, badge, joined date, age and members flags, scheduled start, live times, poll and re-share, images in posts, country codes, `hidden`, comment-likes series, zero-likes fix) | 5–6 days |

**Total:** about 4–5 weeks of draft PRs, of which about 1 week is new fields. It can start with the outbox and yt protos before v1.0.0.
The `content.*` mapping waits for v1.0.0, and nothing is registered in a shared Schema Registry.

## 7. Answers to the yts questions

1. **Time precision.** Right. yts stores the precision in seconds:

   | `published_precision_s` | `TimePrecision` |
   |---|---|
   | 0 | `EXACT` |
   | 1–60 ("seconds ago", "just now") | `MINUTE` |
   | 3,600 | `HOUR` |
   | 43,200 (a date only, ± 12 h) and 86,400 | `DAY` |
   | 604,800 | `WEEK` |
   | 2,592,000 (30 days) | `MONTH` |
   | 31,536,000 (365 days) | `YEAR` |

   Anything between two of these goes to the coarser unit. `displayed` holds the text shown ("3 days ago", "Premiered 5 Nov 2026"):
   - **Videos:** yts keeps it in `published_text`.
   - **Comments and community posts:** yts drops it today; the mapper takes it from the parser at write time.

   The API's exact time is written with 1-day precision on purpose, so the scraped exact time replaces it. That row is never
   published (question 5).
2. **Zero likes.** `value = 0`, `EXACT`, `displayed` empty is right for what YouTube means. A comment's or community post's like count
   can't be hidden: YouTube simply shows no number at zero, so a hidden count doesn't read that way.
   - **The real risk is different:** yts writes 0 whenever the like field is missing. If YouTube renamed that field, every comment
     would silently read 0.
   - **The fix** (small, not built): write 0 only when the like button is present without a number; leave it unset otherwise; and
     watch the share of zeros in the fill-rate alerts.
   - **Video likes can be hidden by the uploader.** yts already leaves them unset: the button then carries no number.
3. **Commenters' `content.account` events.** `PROFILE_ONLY` is the nearest, but its comment ties it to "private accounts: the second
   list". Mixing the two would stop downstream from telling a private account apart from a commenter we merely know about.
   - **I propose a new value:** `COLLECTION_SCOPE_NOT_MONITORED` ("known from content: a commenter, mention or link target; its profile
     is read now and then, its posts aren't collected").
   - **The alternative:** keep `PROFILE_ONLY` and widen its comment to cover both.
4. **Community posts:**
   - **Polls:** `Poll` (question and options), total votes in `counts.other["poll_votes"]`, per-option votes unset.
   - **An attached video:** a **link**, not a `Reshare`: `links[]` with `target_platform = yt`, `target_id = yt:post:<video>` and
     `source = LINK_SOURCE_ATTACHMENT`. Channels attach their own videos as often as others', and nothing is re-published.
   - **A re-shared community post** (another channel's post shown inside this one) is a real `Reshare`, type `RESHARE_TYPE_SHARE`, with
     the original post's ID and owner.
5. **Official-API search hits.** Right, with one rule for yts's relay. Today an API hit for a listed channel's upload inserts a `videos`
   row (source `api_search`) and queues the website read. The relay must **not** publish `content.post` (or `yt.video`) from that
   insert. The first `content.post` comes from the website read, with `ACQUISITION_SCRAPED_LOGGED_OUT`, and `extras["yt.source"] =
   "api_search"` only as provenance. A `Candidate` from an API hit has `acquisition = ACQUISITION_OFFICIAL_API`, IDs only in
   `evidence`, and `terms_expire_at` 30 days on. Its outcome event after the website check is a scraped observation, without the
   deadline.
6. **Relevance and scope.** Right for the scope: core → `FULL`, mention → `RELEVANT_ONLY`. For `Relevance`: yts's `matched_keywords`
   mixes three kinds of ID, so the mapper splits them.

   | ID | Example | Goes to |
   |---|---|---|
   | Seed keyword IDs | `sulaymaniyah-ambiguous` | `matched_keyword_ids` |
   | The owner's own keywords | `kw-12` | `matched_keyword_ids` |
   | Rules | `lang-ckb` | `matched_rules` |

   Once the shared keyword list exists (open point 1), yts maps its IDs to the shared list's, or imports that list and uses its IDs
   directly. I'd recommend the import. `score` stays unset: yts's check is yes or no.
7. **Live streams.** `live_viewers` → `concurrent_viewers` and `scheduled_start` → `video.scheduled_start`: right. Also:
   - the stream's actual start and end times (section 4);
   - `subtype = "premiere"` for premieres;
   - a finished stream keeps `POST_TYPE_LIVE` with `ENDED`.

   Live chat stays out (D24). Peak viewers are derived from the series, not collected.
8. **Playlists and the single Kafka transaction.** Nothing in the contract makes it hard:
   - One transaction can write to several topics. The relay batches outbox rows (a few hundred per transaction); a 20,000-comment
     backfill becomes several transactions, in order per key.
   - `version` = observed_at µs needs no state.
   - `event_id` as SHA-256 of `id|observed_at µs|content_hash` makes replays safe.
   - Keys: comments and metrics by their post, which matches yts's outbox key.

   Two small points:
   - **The playlist ID's kind** (`collection`?), as asked above.
   - **Large playlists:** `Playlist.items` is repeated in one message, so a 5,000-video playlist is a message of roughly 150–200 KB.
     That's fine under Kafka's default 1 MB, but a single moved item republishes the whole list. That's acceptable at yts's weekly to
     monthly playlist schedule. If another platform has far bigger lists, an item-level message may be worth it later.
