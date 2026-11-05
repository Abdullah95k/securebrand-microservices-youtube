# yts: contract and event-stream notes

Written by the yts session on 6 November 2026 (Iraq time), from `main` `3db1415`. Plans only: nothing here is built.

## 1. How yts would publish to Kafka

**Today:**
- yts writes everything to its own Postgres, inside transactions. Change detection and history depend on that:
  - diffs logged in `channel_changes` and `video_changes`;
  - comment versions by text hash;
  - deletions after missed sweeps;
  - completeness;
  - "is this comment new".
- yts **already has a transactional outbox**. Database triggers add `webhook_events` rows in the same transaction as the content change.
  A dispatcher in the scheduler leases them, delivers them with signed HTTP, retries with backoff and marks dead letters.

| | Option 1: platform events plus `content.*` straight from the writer | Option 2: transactional outbox from Postgres | Option 3: a bridge |
|---|---|---|---|
| What it means for yts | The writer calls Kafka next to Postgres | Each write also inserts an event row in the same transaction; a relay publishes the rows in order and marks them sent | Something outside yts reads its tables (or the Postgres log, through Debezium) and publishes |
| Consistency | **Two writes**: if Kafka fails after Postgres commits, or the other way round, they disagree. That breaks E1 unless yts stops keeping content in Postgres, which its change detection needs | One commit: the event exists exactly when the data does. At-least-once, made idempotent by `event_id` | Depends on the bridge. Table CDC sees row changes, not meaning: an edit, a deletion and a re-read look alike |
| Ordering | Per process only | In outbox order, per key | Log order |
| Change to yts | Large: every writer path, plus failure handling | Medium: reuses the outbox pattern yts already runs | Small in yts, large outside. The bridge is coupled to yts's internal tables, which then can't change freely |
| Replay | From Kafka only | From Kafka, or by re-queueing outbox rows | From Kafka |

**Recommendation: Option 2,** the outbox. It's the pattern already in yts, it satisfies E1 (one commit, one publish) and it keeps the change
detection that needs Postgres.

**What changes in yts.** Every change is an addition: no table, column or endpoint that anyone uses is renamed or removed.

1. **A new migration** (`0010`):
   - an `event_outbox` table: id, topic, key, payload (protobuf bytes), created_at, published_at;
   - `version` columns on `channels`, `videos`, `posts` and `playlists` (`comments` already has one).
2. **Event rows written in the same transaction as each write:**
   - channel and channel snapshot;
   - video and video snapshot;
   - comment, new version and deletion;
   - community post and post snapshot;
   - playlist;
   - commenter profile;
   - media;
   - discovery candidate.

   The comment engine already batches its writes, so this adds one row per event.
3. **Protobuf schemas** for the platform events `yt.channel.v1`, `yt.channel_snapshot.v1`, `yt.video.v1`, `yt.video_metrics.v1`,
   `yt.comment.v1`, `yt.post.v1`, `yt.post_metrics.v1`, `yt.playlist.v1`, `yt.author_profile.v1` and `yt.media.v1`, in yts's repository.
   `content.*` comes from the shared contract repository, once the master publishes v1. **Nothing is registered** in a shared Schema
   Registry before that.
4. **A relay** (`ytcollect relay`, its own deployable):
   - **What it does:** it leases outbox rows in order and publishes them with an idempotent producer. It publishes the platform event,
     and the `content.*` event mapped from it, in one Kafka transaction, so no separate normalizer hop is needed. The mapping code
     lives in yts's repository, as Q2 recommends.
   - **Bounded:** retries with backoff, metrics for lag and failures, and an alert when the outbox grows.
   - **Sent rows:** deleted after 7 days. They are transport, not content; the content stays in Postgres and ClickHouse.
5. **Kafka keys** (E5):

   | Event | Key |
   |---|---|
   | Comments | The video or post ID, so a thread stays in order |
   | Video, its metrics, its media | The video ID |
   | Channel, its snapshots | The channel ID |
   | Community post, its metrics | The post ID |

6. **Envelope fields:**
   - `event_id`: a ULID;
   - `observed_at`;
   - `version` (as in step 1);
   - `collector` = `yts` and `collector_version`;
   - `parser_version`: a new constant;
   - `content_hash`: comments already have one; added for posts and videos;
   - `raw_ref`: empty until raw capture exists (gap E8).
7. **Configuration:** Kafka and Schema Registry addresses and credentials as files (`*_FILE`), like every other secret.
8. **Webhooks stay** as they are. Nobody uses them yet. They could later be fed from Kafka instead; that's the master's call.

**Effort** (my estimate, in draft PRs with tests): about 2–3 weeks.
- **Outbox and version columns:** 2–3 days.
- **yt protos and the writers:** 4–5 days.
- **The relay:** 3 days.
- **`content.*` mapping and conformance tests:** 3–4 days, after v1 exists.
- **An end-to-end test** against the fake YouTube and a test Kafka (Redpanda): 2 days.

## 2. The archive (D27) and the shared ClickHouse

**D27:** the yts archiver moves Postgres monthly partitions older than 12 months to gzip JSON-lines files in object storage. These are
the snapshot, version and profile tables. The files are searchable, and a month can be restored for a while. **Nothing has been
archived yet**: production was never deployed.

**The platform:** content and history in a shared ClickHouse, fed from Kafka, with old partitions on object storage (tiered storage).

**How they fit:** once yts publishes, ClickHouse does what D27 was for, and better: columnar, compressed, queryable at once, and shared
with analysis. So:

1. **Phase 1:** yts publishes, and keeps everything in its Postgres as today. Postgres is a second copy while the pipeline is proven.
   The archiver stays off: yts doesn't configure the storage it needs.
2. **Phase 2:** once ClickHouse has held yts's data for some weeks and the counts match (a reconciliation report per table), **D27 is
   retired**, and yts slims its Postgres to working state.
   - This would remove history older than a window from yts's Postgres. It is kept in ClickHouse and replayable from Kafka and
     backups.
   - Because it is a deletion from yts's store, it needs the owner's approval at that time.

**What yts keeps in its own Postgres** (the working state):

| Kept | Why |
|---|---|
| `schedule`, `settings`, `proxy_health`, `quota_ledger`, `capacity_reports`, `field_fill_rates`, `parse_failures` | Running the collector |
| `channels` (identity, tier, kind, status and the latest values), `channel_inputs` | The registry, and change detection on the next check |
| `videos` (the latest row per video) | Scheduling by age, change detection, relevance, comment completeness |
| `comments`: ID, parent, hash, version, reply count, last seen, missed sweeps, deleted at | Detecting new, edited and deleted comments. The text itself isn't needed after publishing; keeping it is the owner's choice |
| `posts`, `playlists`, `playlist_items` (latest rows) | The same, for posts and playlists |
| Discovery tables | `keywords`, `search_terms`, `search_hits` (API hits 30 days), `candidates`, `channel_links_graph` |
| `authors` | Ordering and refreshing enrichment |
| `event_outbox`, `webhook_*` | Delivery |
| `suppression_list`, `erasure_*` | Dormant, kept (Q6) |

**Moved to ClickHouse only, after Phase 2:**
- `channel_snapshots`, `video_snapshots`, `post_snapshots`;
- `comment_versions`, `channel_changes`, `video_changes`;
- `author_profiles`;
- older comment text, if the owner agrees.

## 3. On `05-unified-schema-plan.md`, section 2

**I agree with:**
- separate entity messages with one envelope. yts's tables map to them almost one to one:

  | yts | Shared contract |
  |---|---|
  | `channels` | `content.account` |
  | `channel_snapshots` | `content.account_snapshot` |
  | `videos` and `posts` | `content.post` |
  | `comments` | `content.comment` |
  | `video_snapshots` and `post_snapshots` | `content.metrics` |
  | `media` | `content.media` |
  | `candidates` | `discovery.candidate` |

- the text ID scheme with `yt`;
- the transition adapter for analysis;
- one contract repository with a conformance kit.

**What I would change or add:**

1. **The kind in the ID must be the entity kind** (`yt:post:<id>`), never the subtype (`yt:short:…`, `yt:live:…`). A YouTube video's
   subtype changes: upcoming → live → was_live, and Shorts classification can change. The subtype goes in `kind` or `extras`, not in
   the key.
2. **Times need a precision.** YouTube gives exact times only for videos (player, RSS, Data API). Comments and community posts are
   only "3 days ago". 61% of Phase 0 comment times were year-precision, 28% month-precision. Add `published_at_precision_s` (or an
   enum: exact, minute, hour, day, week, month, year) next to every `published_at`, and let consumers refine it when a better reading
   arrives (yts already does).
3. **Approximate counts carry their text.** Add the displayed text ("1.2K") next to the number and the `approximate` flag, so a later
   parser can re-read it.
4. **On YouTube every author is a channel.** The comment's author reference should be able to point at an account ID (`yt:account:UC…`).
   Then the commenter-profile reads (`author_profiles`) map to `content.account` and `content.account_snapshot`, and cross-platform
   linking works on commenters too.
5. **Replies have one level on YouTube.** For a reply, `parent_comment_id` = `root_comment_id` = the thread. A reply to a reply is
   identified only by an @mention in its text. Say so in the contract, so depth means the same on every platform.
6. **Collector-side relevance.** yts matches keywords as it collects (`relevant`, `matched_keywords`). For mention channels it keeps
   only the relevant items. Add an optional `relevance` block (matched keyword IDs, rule IDs such as `lang-ckb`), or document it as
   `extras.yt.matched_keywords`, so analysis can see why an item was collected.
7. **The discovery outcome needs "mention".** yts lists Iraq channels as core (everything collected) and channels that only sometimes
   mention Iraq as mention (only relevant items collected). Add that to the outcomes. "Second list (private)" doesn't apply to
   YouTube: channels are public, and private videos are invisible.
8. **Live metrics.** Add `concurrent_viewers` to `content.metrics` (live streams), and `scheduled_start_at` for upcoming streams and
   premieres.

**YouTube fields with no place in section 4.** For each I give where it could go.

| Field | Proposal |
|---|---|
| Uploader tags (`videos.tags`; not hashtags) | `extras.yt.tags`. Promote to a shared field if another platform has uploader keywords |
| Category (YouTube's category name) | `extras.yt.category` |
| Chapters | `extras.yt.chapters` |
| Live status (none, upcoming, live, was_live), scheduled start | Point 8 above, plus `extras.yt.live_status` |
| Caption languages | `extras.yt.caption_langs` |
| Family-safe flag (the nearest to "age-restricted") | `extras.yt.family_safe`; `age_restricted` when it is parsed |
| The channel's own keywords | `extras.yt.channel_keywords` on the account |
| Join date as text (day precision) | `account.created_at` with a precision |
| Commenter membership badge (per channel, "Member (2 years)") | `extras.yt.member_badge` on the comment |
| Playlists and their items | No entity. Keep them as the platform event `yt.playlist.v1` for now; a shared "collection" entity later if Instagram highlights or TikTok playlists need one |
| Community post attachments (image, poll, linked video) | Media references for images; `extras.yt.poll` and `extras.yt.linked_video_id` (yts stores only yes/no today: gap) |
| Comment completeness and the "partial" flag | `collection.coverage` |
| Total channel views | Already in the account snapshot (good) |

## 4. YouTube API terms

**What the rule is.** YouTube's Developer Policies have a section on refreshing, storing and displaying API data. As recorded in the yts
plan (§4.2, D26), data obtained through the API may be stored for at most **30 calendar days**. After that it must be refreshed through
the API or deleted. The policy also expects data that is no longer available on YouTube to be removed.

> **Not verified today:** this session can't reach `developers.google.com`. The rule above is from the plan's earlier research, whose
> exact wording was already marked "verify". Two points matter and should be confirmed by the master, or a lawyer, before v1:
> - the exact current wording;
> - whether IDs (video and channel IDs) are exempt.
>
> yts assumes no exemption.

**Which of our data comes from the API:**
- **From the Data API:** only search results. Per hit: video ID, channel ID, title and publish time. They land in three places
  (corrected on 6 November; see REQ-004 below):
  - `search_hits` with engine `api`, deleted after 30 days;
  - for an upload of a listed channel, a `videos` row with source `api_search`, which starts with the API's title and time until the
    scraped read replaces them;
  - for an unknown channel, a `candidates` row: the channel ID, with `api_search` among its sources.

  Also the Phase 0 accuracy-check records, deleted after 30 days.
- **From scraping:** everything else: channels, About panels, videos, comments, community posts, playlists, commenter profiles, images
  and website search hits. No API rule applies to it. The YouTube Terms of Service question about automated access is covered by the
  owner's legal sign-off (D29, R10).
- **When a hit leads somewhere:** an API hit only points at something. When it leads to a candidate or a relevant video, yts reads that
  channel or video **by scraping**, and the scraped copy is what's kept.

**What this means for "keep all data":**
1. Everything scraped is kept forever, as the owner wants.
2. The only thing that can't be kept forever is the API copy of search results. Losing it costs nothing, because the same items are
   scraped and kept.
3. **For the platform:**
   - **Don't publish API search hits as `content.*`.** Publish a `discovery.candidate` with `source = api_search` and the video and
     channel IDs as evidence, without the API's title.
   - **Retention:** any topic or ClickHouse table that does carry API fields gets a 30-day retention or TTL.
   - **Alternative for the candidate:** carry the scraped title, which yts reads anyway at the candidate check.
4. The plan's other warning still stands: if Google links the API key to the scraping system, the key could be cut off. Discovery would
   then continue on website search and the channel graph. Phase 0: the website search alone found 60% of new channels, the API alone
   33%, and both 7%.

## REQ-004: answers to the master session (6 November 2026)

### 1. Data API storage

You're right, and my earlier summary was incomplete. Data that first came from the API is stored in three places:

| Where | What | How long |
|---|---|---|
| `search_hits` (engine `api`) | Video ID, channel ID, title, publish time | **Deleted after 30 days** (daily, `internal/discovery`) |
| `videos`, source `api_search` | When the hit is an upload of a listed channel: a row with the API's title and publish time. A scraped read of the video is queued at once (priority P1) and replaces the title, description, times and counts. The API time is written with 1-day precision on purpose, so the scraped exact time always replaces it. **`source` stays `api_search` for good:** it records provenance | Kept |
| `candidates` | When the channel is unknown: its channel ID, with `api_search` among `sources`. A candidate check (P3) is queued at once and reads the channel page by scraping, which returns the same ID | Kept |

**How this fits the rule in plan §4.2** (API data kept at most 30 days unless refreshed):
- **After the scraped read or check succeeds:** what yts keeps is scraped data. The API title and time are replaced, and the channel ID
  has been read again from the channel's own page. `source = api_search` and `sources` only record how the item was first found. That
  is yts's reading of the rule, and **it needs the policy text to confirm**: whether a stored ID counts as API data at all, and whether
  "refreshed" may mean "re-read from the website".
- **Two holes that don't fit:**
  1. A `videos` row whose scraped read never succeeds (the video was removed or made private, or the job keeps failing) keeps the API
     title and time past 30 days.
  2. A candidate that hasn't been checked within 30 days (a backlogged queue) holds an ID that only the API gave us.
- **Proposed fix** (not built; it waits for the owner's approval): a daily job. It clears the API title and time of `api_search` videos
  never read by scraping after 30 days (or deletes the row), and removes candidates whose only source is `api_search` that are unchecked
  after 30 days. Effort: about half a day.
- **Corrected in the inventory:** `videos.source` is `web`, `tab`, `rss`, `websub`, `web_search` or `api_search`, not only the first three.

### 2. AI inside yts

- **Only `ytprobe label -ai`,** the Phase 0 keyword-quality test (E11). It last ran on 4 November and is finished.
- **What was sent to the AI provider's API:** the labelling instructions and 300 public video titles, each with a local number (1, 2,
  3…). No video or channel IDs, channel names, descriptions, comments or commenters.
- **What came back:** one label per number (relevant, not relevant, or can't tell), stored in the Phase 0 data folder.
- **Key:** read from `YT_ANTHROPIC_KEY_FILE`, by `ytprobe` only.
- **Production path:** `ytcollect` never calls it and never reads the key. **But the `ytcollect` binary links the SDK:** it imports
  `internal/phase0` for the shared page-fetching code, and that package also holds the labelling command.
- **Proposed fix:** move the labelling code into a package only `ytprobe` imports, so the SDK leaves `ytcollect`'s dependency tree.
  Effort: about half a day, not built.

### 3. WebSub (push notifications)

| Item | What the receiver needs |
|---|---|
| Hostname | Any public hostname the platform's ingress gives it. It is set as the full callback URL in `YT_WEBSUB_CALLBACK`, for example `https://push.example.com/websub` |
| Path | Any path: the receiver answers on every path. The hub calls exactly the callback URL, with `?channel=<channel ID>` added by yts |
| TLS | Ends at the ingress. The receiver speaks plain HTTP on `:8080` (the `-addr` flag). The callback must be reachable from the public internet: Google's hub has no fixed address list known to me |
| Requests | **GET:** the hub's verification. yts checks the topic and channel, then echoes `hub.challenge`. **POST:** an Atom notification, read up to 1 MB, signed with `X-Hub-Signature` (HMAC-SHA1). Each channel's secret is derived from the server secret in `YT_WEBSUB_SECRET_FILE`. Unsigned or wrongly signed posts are dropped (still answered with 2xx, as the hub expects) |
| State | None. It writes to the collector's Postgres, so it needs the same database credentials. It can run several copies |
| Subscriptions per 1,000 channels | **1,000:** one per listed channel, core and mention. Each lease is 10 days and is renewed a day early, so about 111 renewals a day go to `pubsubhubbub.appspot.com`, each followed by a verification GET |
| Notifications per 1,000 channels | About 300–1,000 a day for new uploads (Phase 0 census: median 3 and p90 about 65 uploads per channel per month), plus notifications for updated videos. That rate is unknown: estimate |

### 4. Fields the platform expects: does YouTube show them logged out?

These answers are from how YouTube's public pages work. **They have not been checked against a live response in this session.** Before
building anything, I propose a read-only check: `ytprobe skeleton` on 2–3 channels and videos of each kind, saving structure only. It
is a handful of requests, but it runs on the shared server, so it needs the owner's approval.

| Field | Shown logged out? | Where | Confidence |
|---|---|---|---|
| **Channel verification badge** | **Yes** | The channel page header, the owner line on watch pages, and channel results in search. yts already reads the same badge for commenters (`isVerified`); it isn't parsed for channels | High |
| **Members-only flag** | **The flag yes, the content no** | Members-only videos appear in a channel's listings with a "Members only" badge; the video doesn't play logged out (the player refuses). Members-only community posts aren't shown at all | Medium: not recorded in Phase 0 |
| **Age restriction** (beyond family-safe) | **Yes, as a refusal** | For an age-restricted video, the logged-out player answers "login required" with an age reason. The watch page still gives the title, description and counts. **It must be told apart from the bot check,** which also answers "login required" ("confirm you're not a bot") and which yts treats as a block. The reason text is in the interface language (Arabic). The player is the most limited endpoint, so the flag would be known only for videos whose player is asked | Medium |
| **The channel's joined date** | **Yes** | The About panel shows "Joined …" logged out: present for **97%** of channels in Phase 0, stored today as text in Arabic (`joined_text`). Filling `joined_at` only needs that text parsed (Arabic month names, day precision) | High (measured) |

### 5. `ytprobe-detect` memory

**Not yet.** The owner hasn't sent the `docker stats` lines. I'll add them here when they arrive.
