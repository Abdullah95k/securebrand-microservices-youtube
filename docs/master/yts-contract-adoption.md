# yts: adopting the unified contract (REQ-021, fix 12)

Written by the yts session on 6 November 2026 (Iraq time), from `main` `9a0e248`. A plan: the outbox and relay (fix 12) and the
`content.*` mapping (REQ-021) are built from it in small PRs, in the order of section 7.

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
| `media` | `Media`, `MediaReady` and `MediaDeleted` | `owner_kind` post or account; SHA-256 and the perceptual hash; `required_consumers: [sb-analysis]`; the file is removed once acknowledged (or at `delete_after`), the `Media` record stays |
| `candidates` | `Candidate` | source keyword search, official API search, commenter, link…; outcome added, dormant, not relevant, second list; never `CAPPED` |
| the usage tables (fix 5) and the queue | `JobCoverage` | hourly roll-up per kind, with `job_count` and the reasons from the dead and failing jobs |

**Provenance on every message:** collector `yts` and its version (`internal/version`), `parser_version` (`ytparse.Version`), the
source operation (`browse`, `next`, `player`…), the job ID, `raw_ref` (fix 8) and `exit_country` (the proxy's `country=`).

## 3. Conformance

Every message the relay builds goes through the contract's `conformance` checks with `Options{Production: true, Canary: true}`
while the canary runs, then `Canary: false`. A message that fails is not published: it stays in the outbox with its reason, counted
and alerted, and a test case is added. CI runs the checks on events built from the fake YouTube, end to end.

## 4. The outbox and the relay

- **Migration `0012`:** `event_outbox` (id, topic, key, message type, payload bytes, headers, created_at, published_at, failure) and
  `version` columns on `channels`, `videos`, `posts` and `playlists` (`comments` has one). Additions only.
- **Writes:** the writer, comment engine, discovery, media and the scheduler's coverage roll-up insert their outbox rows in the same
  transaction as the data. One commit: an event exists exactly when its data does.
- **The relay** (`ytcollect relay`, its own Deployment in the chart): leases outbox rows in order, builds the `yt.*` message and its
  `content.*` mapping, checks them, and publishes both in **one Kafka transaction** with an idempotent producer (franz-go). Keys as in
  section 1, so a post's comments and counts stay in order. At-least-once; consumers drop repeats by `event_id` (a ULID).
- **Credentials:** `KAFKA_BROKERS`, `KAFKA_USER`, `KAFKA_PASSWORD`, `KAFKA_SASL_MECHANISM` (SCRAM-SHA-256), `KAFKA_TLS` and
  `KAFKA_CA_CERT` from `platform-credentials`, as files. The schema registry at `SCHEMA_REGISTRY_URL`, read only.
- **Metrics and alerts:** outbox depth and oldest unsent row, published per topic, failures by reason, conformance rejections.
- **Sent rows:** kept 7 days, then deleted. They are transport copies; the data stays in Postgres, and Kafka's copy reaches ClickHouse.
  **The owner's call** (decision 3 keeps processed data forever; this deletes only the copies made for sending).

## 5. The `yt.*` platform events

A proto package `securebrand.yt.v1` in this repository, with every field yts holds (the field inventory). The Go code is generated
from it with `buf generate` and committed; CI checks that the committed code matches the `.proto` files, and runs `buf breaking`
against the last release so the topics stay compatible (the master: they stay unregistered, read only by yts; if another service
ever needs one, it moves into the contract).

**Tooling question:** this session can't run code generators (`buf`, `protoc-gen-go`): its permissions block running downloaded tools.
Options: the owner allows `buf generate` here; or CI generates the code and the session commits CI's output; or the `yt.*` messages
are compiled at start-up from the embedded `.proto` files (`protocompile` and `dynamicpb`), which needs no generator but checks less
at build time. I recommend the first.

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
| 1 | Migration `0012`, the outbox writer and the version columns; events written in the same transaction (no Kafka yet) | 3 days |
| 2 | The `content.*` mapping from outbox rows, with the conformance checks and tests from the fake YouTube | 5 days |
| 3 | The `yt.*` protos and their mapping | 4 days |
| 4 | The relay: franz-go transactions, leases, metrics, alerts; an end-to-end test with Redpanda in CI | 4 days |
| 5 | The chart: the relay Deployment, its credentials, the network policy to Kafka and the registry | 1 day |
| 6 | Discovery candidates and the hourly coverage roll-ups | 2 days |
| 7 | The media hand-off: media ready; a consumer of `content.media.processed.v1` (yts's own consumer group); deleting each file once acknowledged or at its deadline, with `content.media.deleted.v1`; metrics and an alert for files past their deadline | 3 days |

About 4–5 weeks in all, matching the contract README's estimate for yts (4–5 weeks with the new fields of fixes 19–21). Section 4's
row deletion and section 5's tooling are the two points that need an answer before PR 1 and PR 3.
