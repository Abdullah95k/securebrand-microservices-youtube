# yts: adopting the unified contract (REQ-021, fix 12)

Written by the yts session on 6 November 2026 (Iraq time), from `main` `9a0e248`. A plan: the outbox and relay (fix 12) and the
`content.*` mapping (REQ-021) are built from it in small PRs, in the order of section 7. **Updated 7 November:** the owner's two
answers, and PR 1 (the outbox) built; section 4 describes it as built. PR 2 (the mapping and the checks) built: sections 2 and 3. PR 3 (the `yt.*` messages) built: section 5. PR 4 (the relay) built: section 4. PR 5 (the chart) built: section 7. PR 6 (candidates and coverage) built: section 7. PR 7 (the media hand-off) built: section 7. All seven PRs are merged (#60–#66): fix 12 is complete.

**Contract version:** v1.2.0 (`github.com/abdullah95k/securebrand-contracts`). The owner approved v1.0.0 (D-016); v1.1.0 and v1.2.0 only
add fields and checks, and the master agreed to v1.2.0 (6 November). Every message goes out with `sb-contract: 1.2.0`.

## 1. What yts publishes

All through one relay, from a transactional outbox (section 4). yts creates no topics and registers no subjects: the platform
created every topic below (the `yt.*` ones on 6 November, in its commit `74f1abf`), and registers only the contract's subjects.

| Topic | Message | Key (partitions) | What yts puts there |
|---|---|---|---|
| `content.account.v1` | `content.v1.Account` | account ID | Monitored channels; commenters (enriched or not) as `NOT_MONITORED` accounts |
| `content.account_snapshot.v1` | `content.v1.AccountSnapshot` | account ID | Subscribers, videos and views at each channel read |
| `content.post.v1` | `content.v1.Post` | post ID (6) | Videos (`VIDEO`, `SHORT_VIDEO`, `LIVE`) and community posts (`COMMUNITY`) |
| `content.comment.v1` | `content.v1.Comment` | post ID (6) | Every comment and reply version; deletions as partial events |
| `content.metrics.v1` | `content.v1.Metrics` | post ID (6) | Views, likes, comment counts, live viewers: when a count changed, and at least daily per refreshed item |
| `content.media.v1` | `content.v1.Media` | media ID | Thumbnails and community-post images (the record, kept forever) |
| `content.media.ready.v1` | `media.v1.MediaReady` | media ID | Each stored image for `sb-analysis`, `delete_after` 72 hours |
| `content.media.deleted.v1` | `media.v1.MediaDeleted` | media ID | Each image yts removes: once every required consumer has acknowledged it, or at its `delete_after` (the owner's rule: kept until analysed) |
| `content.playlist.v1` | `content.v1.Playlist` | playlist ID | Channel playlists and their items |
| `discovery.candidate.v1` | `discovery.v1.Candidate` | target platform + account | Every channel discovery finds; official-API hits only here, never in `content.*` |
| `collection.coverage.v1` | `coverage.v1.JobCoverage` | job ID | Hourly roll-ups per job kind (`job_count`), with the reasons |
| `yt.channel.v1`, `yt.channel_snapshot.v1`, `yt.video.v1` (6), `yt.video_metrics.v1` (6), `yt.comment.v1` (12), `yt.post.v1`, `yt.post_metrics.v1`, `yt.playlist.v1`, `yt.author_profile.v1`, `yt.media.v1` | `securebrand.yt.v1.*` (yts's own, section 5) | as the matching `content.*` | The platform events (standard E2), every field yts has |

Partitions not shown are 3. **yts also reads** `content.media.processed.v1`: `sb-analysis`'s acknowledgements, filtered to
`platform = yt`, which decide when an image may go (section 4).

**Retention:** 7 days on Kafka for every topic (the contract's rule). The platform's writer reads every contract topic into
ClickHouse and the event archive; the relay writes Kafka only. The `yt.*` topics are yts's working topics and aren't archived: the
raw responses in object storage are yts's record of what YouTube returned.

**Volume** (an estimate from the plan's §6.5, until the pilot measures it): at 1,000 channels about 20,000 new comments a day, plus
metrics for the items still refreshed (about 30,000 a day) and the rest, so roughly 50,000–100,000 `content.*` messages a day, and as
many `yt.*`; about ten times that at 10,000 channels.

## 2. The mapping

From the README's rules (section "The rules collectors follow") and the field matrix, for yts's tables:

| yts | Contract | Notes |
|---|---|---|
| `channels` | `Account` `yt:account:UC…`, type channel | `scope` from the registry: core → `FULL`, mention → `RELEVANT_ONLY`; links from the About panel and descriptions; `extras`: `yt.tier`, `yt.family_safe`, `yt.added_by`, `yt.added_reason` |
| `channel_snapshots` | `AccountSnapshot` | `followers` = subscribers (`Count` with `exactness ROUNDED` when YouTube abbreviated it, `displayed` kept) |
| `videos` | `Post` `yt:post:<id>` | type by kind (video, short, live); `description` (not `text`); `live_started_at`, `live_ended_at`; `relevance` with `KeywordID` from the stored spelling; `extras`: `yt.source`, `yt.player_unavailable`, `yt.playability_status` |
| `video_snapshots` | `Metrics` (post) | only when a count changed, and at least daily |
| `comments` and `comment_versions` | `Comment` `yt:comment:<id>` | depth 1 for replies, parent = root = the thread; author as an `AccountRef`; `url`; times by D-013 (a relative time at the start of its unit, 00:00 Iraq time for days and longer); deletion as a partial event, `DELETED` only after a complete re-read misses it |
| `posts` and `post_snapshots` | `Post` (`COMMUNITY`) and `Metrics` | an attached video is a link, a re-shared post a `Reshare` |
| `playlists`, `playlist_items` | `Playlist` `yt:playlist:<id>` | |
| `authors`, `author_profiles` | `Account` (`NOT_MONITORED`) | enriched commenters carry their About links |
| `media` | `Media`, `MediaReady` and `MediaDeleted` | `owner_kind` post (a thumbnail) or account (an avatar or banner); the SHA-256 (no perceptual hash yet: the field is optional, and yts doesn't decode images); `required_consumers: [sb-analysis]`; the file is removed once acknowledged (or at `delete_after`), the `Media` record stays |
| `candidates` | `Candidate` | source keyword search, official API search, commenter, link…; outcome added, dormant, not relevant, second list; never `CAPPED` |
| the usage tables (fix 5) and the queue | `JobCoverage` | hourly roll-up per kind, with `job_count` and the reasons from the dead and failing jobs |

**Provenance on every message:** collector `yts` and its version (`internal/version`), `parser_version` (`ytparse.Version`), the
source operation (`browse`, `next`, `player`…), the job ID, `raw_ref` (fix 8) and `exit_country` (the proxy's `country=`).

**Built (PR 2, `internal/contract`):** every outbox kind maps to its message, with these choices where the contract leaves room:
- **IDs and keys:** `yt:account:`, `yt:post:`, `yt:comment:`, `yt:playlist:` plus the YouTube ID; Kafka keys are those IDs (the post's
  for comments and counts).
- **Version and event ID:** the version is the read time in microseconds; the event ID is a SHA-256 of the ID, the version, the
  message kind and the content hash, so a message sent twice has the same event ID. The content hash leaves out counts and coverage.
- **Times (D-013):** minutes and hours at the start of the minute or hour; days and longer at 00:00 Iraq time; `displayed` keeps the
  text ("3 days ago").
- **Counts:** a count YouTube abbreviated ("1.2K") is `ROUNDED`, a plain number `EXACT`; a video's views are exact from its own check
  and a lower bound from a listing; channel video and view totals are lower bounds.
- **Partial events:** a removal (two misses) is the keys and `UNAVAILABLE` (logged out, YouTube's deleted, private and removed look
  alike); a comment missing from two complete re-reads is `DELETED`. An item whose publish time isn't known yet (a Short seen in its
  tab) is partial until its own check.
- **Keyword IDs:** `contracts.KeywordID(group, language, the stored spelling that matched)`. The seed list gives each keyword its
  languages, not each spelling's, so the spelling's script decides: Arabic script with a Sorani letter is `ckb`, else `ar`; Latin with
  a Kurmanji letter (ê î û ç ş) is `kmr`, else `en`. A best effort: "Bexda" counts as English. The Kurdish-language rule goes in
  `matched_rules` as `lang-ckb`. Keywords you add without a group use their kind as the group.
- **Commenters:** `NOT_MONITORED` accounts; one who is also a listed channel adds no account message, so the channel's own scope
  stands.
- **Playlists:** item positions from 0, as in the contract's example (yts counts from 1); a video that left the list stays, as
  `UNAVAILABLE`, `removed_from_playlist`.

## 3. Conformance

Every message the relay builds goes through the contract's `conformance` checks with `Options{Production: true, Canary: true}`
while the canary runs, then `Canary: false`. A message that fails is not published: it stays in the outbox with its reason, counted
and alerted, and a test case is added. CI runs the checks on events built from the fake YouTube, end to end.

**Built (PR 2):** `contract.Mapper.Map` checks every message before returning it; a refusal is a `contract.Rejected` with the
reason. The end-to-end test collects a fake channel (videos, a Short, comments and a reply, a community post, a playlist,
commenters) through the real worker, and every one of its events passes in production mode. During the pilot, with no Kafka,
`ytcollect outbox check` runs the same checks on the real events and lists any refused, with the reason; `ytcollect outbox` shows
what is waiting.

## 4. The outbox and the relay

- **Migration `0012` (built, PR 1):** two new tables, nothing else changed.
  - `event_outbox`: id, kind, the entity's YouTube ID, the ID its Kafka key is built from (the post, for comments and counts),
    when it was read, the payload, the provenance, created_at, attempts, published_at, failure.
  - `outbox_state`: per entity, the hash of the last content sent, and the hash and time of the last counts sent.

  **A change from the first draft:** no `version` columns. The contract sets the version to the time of the read in microseconds,
  which needs no stored counter, so yts's tables are untouched.
- **Writes (built, PR 1):** one commit, so an event exists exactly when its data does. These write their events in the same
  transaction as the data:
  - the writer: channels, videos (a new upload goes out at once from the listing), removals, community posts, playlists;
  - the comment engine: comments, edits, deletions, commenters;
  - enrichment: commenters' profiles.

  Discovery and the coverage roll-ups were added in PR 6; stored images and the hand-off in PR 7.
- **When an event is written (built):**
  - a content event (channel, video, post, comment, playlist with its items, commenter with their profile) when what is stored
    changed, counts and bookkeeping aside. The first read, an edit, a removal and a deletion are changes; a read that changes
    nothing writes nothing.
  - a counts event (metrics, channel snapshots) when a count changed (a comment's likes or replies too), and at least once a day
    for every item still read: the contract's volume rule.
- **Provenance (built):**
  - each job carries a trail: the job ID (kind, entity, start time), the operations read, each answer's `raw_ref` (fix 8) and the
    proxies' `country=` tags;
  - each write takes what was read since the write before;
  - with the collector's and parser's versions.

  This is bookkeeping only: how yts reaches YouTube is unchanged.
- **The relay** (`ytcollect relay`, its own Deployment in the chart): leases outbox rows in order, builds the `yt.*` message and its
  `content.*` mapping, checks them, and publishes both in **one Kafka transaction** with an idempotent producer (franz-go). Keys as in
  section 1, so a post's comments and counts stay in order. At-least-once; consumers drop repeats by `event_id`.

  **Built (PR 4, `internal/relay`):**
  - One relay sends at a time (a Postgres advisory lock), so the outbox's order holds. It reads batches of unsent events in order
    (500 by default), maps each to its `content.*` messages and its `yt.*` message, and publishes the batch in one Kafka transaction
    (transactional ID `yts-relay`, all in-sync replicas, keys hashed as Kafka's own clients do). It marks the events sent after the
    commit; a stop in between sends the batch again, with the same event IDs.
  - Headers: `sb-type` on every message, `sb-contract: 1.2.0` on `content.*`.
  - An event the contract refuses, or that Kafka can't take (too large), is set aside with its reason (`event_outbox.failure`), so it
    can't block the rest; it is tried again when the relay restarts, after a fix. Anything else (Kafka down, a topic missing) aborts
    the batch, which goes again with back-off.
  - It never creates a topic and registers nothing.
  - Kafka settings from the platform's files (`KAFKA_BROKERS`, `KAFKA_USER`, `KAFKA_PASSWORD`, `KAFKA_SASL_MECHANISM`,
    `KAFKA_TLS`, `KAFKA_CA_CERT`; folder `YT_KAFKA_DIR`, else the platform's credentials); credentials are refused without TLS to
    another machine.
  - Ops port 9103: `/healthz`, `/readyz` (the database and Kafka), `/metrics`: `yt_relay_published_total` by topic,
    `yt_relay_refused_total` by kind, `yt_relay_transactions_total` by result, `yt_relay_last_commit_timestamp_seconds`,
    `yt_relay_up`. The scheduler adds `yt_outbox_failed`.
  - Alerts `OutboxBacklog` (events waiting over 30 minutes, once a relay has run in the last day, so the pilot doesn't alert) and
    `OutboxRefused`, with a runbook section.
  - **A change from the plan:** the end-to-end tests run against franz-go's in-memory Kafka (`kfake`, with transactions) inside
    `go test`, not a Redpanda container in CI: the same checks, no extra service.
- **Credentials:** `KAFKA_BROKERS`, `KAFKA_USER`, `KAFKA_PASSWORD`, `KAFKA_SASL_MECHANISM` (SCRAM-SHA-256), `KAFKA_TLS` and
  `KAFKA_CA_CERT` from `platform-credentials`, as files. The schema registry at `SCHEMA_REGISTRY_URL`, read only.
- **Metrics and alerts:** outbox depth by kind and the oldest unsent row (`yt_outbox_unsent`, `yt_outbox_oldest_unsent_seconds`,
  built in PR 1). Published per topic, failures by reason, conformance rejections and the alerts come with the relay.
- **Sent rows:** deleted 7 days after they were sent, hourly by the scheduler (**the owner, 7 November**). They are copies made for
  sending; the data stays in Postgres, and Kafka's copy reaches ClickHouse. Unsent rows are never deleted.
- **Until the relay runs:** the pilot runs without Kafka (the owner), so events wait in the outbox and go out when the relay arrives.
  At 1,000 channels that is roughly 100,000 rows and 100 MB a day; the pilot's `report` shows the table's size. `outbox_state` adds
  about 150 bytes per item ever stored, most of them comments.
- **Data from before the outbox:** each item goes out at its next read. A one-off command could send everything stored before it, if
  the platform wants that history in ClickHouse; I can propose it with the relay.
- **Erasure (off, kept working):** erasing an item also deletes its outbox events and state, so an unsent copy is never published.
  Copies already sent need the contract's erasure message, later.

## 5. The `yt.*` platform events

A proto package `securebrand.yt.v1` in this repository, with every field yts holds (the field inventory). The Go code is generated
from it with `buf generate` and committed; CI checks that the committed code matches the `.proto` files, and runs `buf breaking`
against the last release so the topics stay compatible (the master: they stay unregistered, read only by yts; if another service
ever needs one, it moves into the contract).

**Tooling:** the owner allowed `buf generate` in this session (7 November), so the Go code is generated here and committed, and CI
checks it matches.

**Built (PR 3):**
- `proto/securebrand/yt/v1/yt.proto`, package `securebrand.yt.v1`: `Channel`, `ChannelSnapshot`, `Video`, `VideoMetrics`, `Post`,
  `PostMetrics`, `Comment`, `Playlist` (with its items) and `AuthorProfile` (with the latest profile), one per `yt.*` topic. Each
  mirrors its outbox payload field for field, named as the stored column, so `internal/ytevents` reads a payload straight into its
  message (Protobuf's JSON reader). Field 1 of each is an `Event` header: event ID, outbox kind, read time and version, the
  provenance, the outbox row's ID.
- Keys: the same contract IDs as the `content.*` message from the same row, so the two stay on matching partitions.
- A comment's counts alone have no `yt.*` topic (none was created for them): `content.metrics.v1` carries them, and the next
  `yt.comment.v1` message has them too. `yt.media.v1` (PR 7) carries a stored image's row when it is stored, announced and deleted.
- Go code in `gen/go/securebrand/yt/v1`, generated with buf 1.50.0 and protoc-gen-go 1.35.2 (`buf.yaml`, `buf.gen.yaml`; run
  `buf generate` after changing the `.proto` file).
- CI job `proto`: `buf lint`; the committed code must equal what `buf generate` writes; `buf breaking` against `main` (rule set
  `FILE`), so no field is renumbered, retyped or removed.
- A strict test maps every event of a fake run and fails if a stored column has no field: adding a column means adding its field.

## 6. The master's answers (6 November)

1. **Topics:** the contract topics exist; the ten `yt.*` topics were added to production Kafka with 7 days' retention, partitions as
   in section 1. yts's Kafka user covers `yt.*`, `content.*`, `discovery.*`, `collection.*` and `registry.*`. yts creates none.
2. **Registration:** the platform registers only the contract's subjects. `yt.*` stay unregistered; yts keeps them compatible with
   `buf breaking` in its own CI.
3. **The archive:** the platform feeds it from Kafka. The relay writes Kafka only; `yt.*` aren't archived.
4. **The media hand-off has three parts:** `content.media.ready.v1` (yts publishes), `content.media.processed.v1` (yts consumes,
   filtered to `platform = yt`) and `content.media.deleted.v1` (yts publishes when it removes a file, after every required consumer
   has acknowledged it or at its `delete_after`). Checked with `conformance.MediaReady` and `MediaDeleted`.

## 7. Build order (draft PRs with tests)

| # | PR | Effort |
|---|---|---|
| 1 | Migration `0012` and the outbox writer; events written in the same transaction (no Kafka yet). **Built** | 3 days |
| 2 | The `content.*` mapping from outbox rows, with the conformance checks and tests from the fake YouTube. **Built** | 5 days |
| 3 | The `yt.*` protos and their mapping. **Built** | 4 days |
| 4 | The relay: franz-go transactions, leases, metrics, alerts; end-to-end tests against an in-memory Kafka. **Built** | 4 days |
| 5 | The chart: the relay Deployment, its credentials, the network policy to Kafka and the registry. **Built:** `yts-relay` (off by default and in `values-production.yaml`, for the pilot without Kafka; `relay.enabled=true` turns it on, `relay.canary=true` while the canary runs), one replica, `Recreate`, the platform's credentials mounted for the `KAFKA_*` files, ops port 9103 scraped and allowed from monitoring; Kafka's ports come from `KAFKA_BROKERS` at install, as before. The registry isn't needed: the relay reads nothing from it | 1 day |
| 6 | Discovery candidates and the hourly coverage roll-ups. **Built:** see below | 2 days |
| 7 | The media hand-off: media ready; a consumer of `content.media.processed.v1` (yts's own consumer group); deleting each file once acknowledged or at its deadline, with `content.media.deleted.v1`; metrics and an alert for files past their deadline. **Built:** see below | 3 days |

**PR 6, as built:**
- **Candidates** (`discovery.candidate.v1`, key the account ID, checked by the contract's `Candidate` rule): an event when discovery
  finds a channel, when one gains a source, and when the check decides; more sightings alone are not a change. Source from how it
  was first found (website search, official-API search, commenter); outcome pending, added (or added for relevant items only, for a
  mention channel), already listed, not relevant, or unavailable ("gone"); `decided_at`; the evidence: the keyword, and for website
  hits the video and when it was made (day precision). **Official-API hits give their keyword only, never the API's video IDs or
  fields**, and a candidate only the API found carries `terms_expire_at` (30 days after it was found). The daily API expiry also
  deletes the unsent outbox copies of the candidates it removes, so nothing from the API outlives 30 days.
- **Coverage** (`collection.coverage.v1`, key the job ID `yts:<kind>@<hour>`): the scheduler adds, hourly, one `JobCoverage` per job
  kind and finished hour from the usage tables (fix 5), once each: `job_count` jobs, counters `requests`, `bytes`, `blocked` and
  `yt.jobs`, `yt.failures`, `yt.run_ms`; status completed, partial or failed; reasons `rate_limited` (blocks) and `failed`.
- Neither has a `yt.*` topic: the contract's message carries everything.
- **A fix to PR 1:** a video first seen through an official-API search hit is stored with the API's title and time, and PR 1 sent it
  at once. It now waits for its first website read, which sends it with the website's data; both mappings also refuse such a video
  as a guard. The API's data never reaches the platform's events.

**PR 7, as built:**
- **The record** (`content.media.v1`, key the media ID `yt:media:<kind>:<video or channel>:<first 16 hex of the SHA-256>`): one
  event when an image is first stored (a new picture of the same thing is a new image), for every kind yts stores: thumbnails
  (owner the video, role `THUMBNAIL`), channel avatars and banners and commenters' avatars (owner the account, `PROFILE_PICTURE`,
  `BANNER`). Its provenance says `image` and the proxy's country, like any other read.
- **The hand-off** runs in the relay, so only once Kafka is there: without it (the pilot) nothing is announced or deleted.
  - Every minute, each stored thumbnail not yet announced gets its deadline (72 hours, `-media-keep`, at least 24 hours) and a
    `content.media.ready.v1` (bucket, key with the store's prefix, SHA-256, size, type; `required_consumers: [sb-analysis]`;
    `attempt` 1).
  - The acknowledgements are read from `content.media.processed.v1` in consumer group `yts-media` (committed records only; offsets
    committed once recorded), keeping those with `platform = yt` from a required consumer, one row per file and consumer
    (`media_acks`). After `FAILED_RETRYABLE` the file is announced again, at most 4 announcements in all; the deadline stays.
  - Every 10 minutes, each file every required consumer acknowledged (processed, skipped, or failed for good), or whose deadline
    passed, is deleted from storage, recorded (`deleted_at`, reason processed or expired) and announced on
    `content.media.deleted.v1`. A file storage won't delete stays and is tried next round.
  - Avatars and banners are not handed off (the owner's decision names thumbnails and post images); they stay as before.
- `yt.media.v1` carries the row each time (stored, announced, deleted).
- Migration `0013_media_handoff.sql` (additions only): five columns on `media`, the table `media_acks`.
- Erasure deletes an image's events and acknowledgements with it; a file already deleted after the hand-off isn't counted again.
- Metrics `yt_media_handoff{kind,state}`, `yt_media_past_deadline`, `yt_media_announced_total`, `yt_media_acks_total`,
  `yt_media_deleted_total`, `yt_media_delete_failures_total`; `yt_media_bytes` now counts only files still in storage. Alert
  `MediaPastDeadline`; runbook section 17. Chart: `relay.media.handoff`, `keep`, `group`, `consumers`.

About 4–5 weeks in all, matching the contract README's estimate for yts (4–5 weeks with the new fields of fixes 19–21). The owner
answered both open points on 7 November: sent rows go after 7 days, and `buf generate` may run here.
