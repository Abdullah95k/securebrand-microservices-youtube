# yts status

The current state of the YouTube collector, for the owner and the master session. Updated **14 November 2026, 06:10 Iraq time** (03:10Z).

## Where it stands

- **Collection stopped from 9 November 19:30 to 11 November 00:09 Iraq time** (16:30Z to 21:09Z). The cause was yts's own
  quarantine rule: one block of any kind on 3 different UTC days within a week set an IP aside for good. The pilot ran across
  7, 8 and 9 November, so its 47 player blocks in about 393,000 requests took all 10 IPs. The exits themselves were fine.
  The master restarted the worker at 21:09Z on 10 November, which cleared it.
- **Live since 14 November, about 05:33 Iraq time (02:33Z): `beb6636`** (the scheduler keeps its tasks' last runs), on
  40 exits; `3ce6d20` (incident switches, lighter commenter graph) is being deployed in platform PR 169 with three daily
  tasks paused. Before them: `cb85201` from 04:56 Iraq time (01:56Z, platform PR 164, suggested videos in discovery).
  Before it: `f23914d` from 03:34 Iraq time (00:34Z, platform PR 158), the first on 40 exits, after `7b6c997`
  (the player-length fill rate, PR 155). Before them: **`8762275` from 13 November, 19:03 Iraq time** (platform PR 148), on the
  10 IPs, with two overrides until
  the new IPs come: `worker.workers=10` and `worker.mediaHold={thumb,author_avatar}`. So on 10 IPs it adds Facebook discovery
  (off until the collector's address and key exist), post images (spare time) and migrations 0020–0021. Listed channels'
  new or changed avatars and banners run at P2 (a few a day). The master's go-live check passed 8 of 9 lines; "media
  acknowledged" waits for thumbnails, which are held. Before it: `6dfa4c7` from 12 November 01:05Z, the thumbnail hold
  (D-052: "Not now for thumbnails"); `624de33` from 11 November 11:17Z, the owner's
  queue order (D-052, below), `d0b93cd` from 07:32Z (nameless channels as partial accounts), `34ff031` from 06:54Z (health
  by tier) and `f87e02e` from 05:46Z (Go 1.27.4).
- **The owner's decision of 13 November (D-060): from 10 to 44 IPs.** 34 new exits go after the 10 in the proxy list,
  shared with Instagram and TikTok as the 10 are. A worker restart picks them up. What to change and what to read is in
  [`master/yts-ips-discovery-media.md`](master/yts-ips-discovery-media.md), which also answers the owner's questions on
  discovery and media, and the client's on count readings and comment picking (CLIENT-REQ-020). Built for the same
  restart, with the master's yes: 30 jobs at once instead of 10 (`worker.workers: 30`), and commenters' avatars held
  (`worker.mediaHold: [author_avatar]`) until the owner decides.
- **The owner's decision of 13 November (D-061): "youtube thumbnails turned on"**, replacing D-052's hold; of avatars
  and banners, "yes i need them" (listed channels only). The thumbnail hold is lifted in the production values. Images
  still run only in spare time, so none download while the 10 IPs are full. **Built with the master's yes** ("that is how
  'youtube thumbnails turned on' is meant to work"): a new video's first thumbnail at P1, with its channel so tiers A and B
  go first; a backfilled video's at P3; the recheck a week on at P3; a listed channel's new or changed avatar or banner at
  P2; older images in spare time. With 44 IPs that is about 1,400 requests an hour at P1, 3–4% of the IPs; a thumbnail
  arrives within about an hour of the video's first reading.
- **The owner's decision of 13 November (D-065): "activate Facebook discovery".** Built: the scheduler sends the Facebook
  links on listed channels' About panels and descriptions to the Facebook collector, each once, at most 200 a day; off
  until the master sets the address and the key's Secret (`scheduler.fbDiscovery`). No request to YouTube. Runbook §19.
- **40 exits since 14 November, 03:34 Iraq time.** On 13 November the owner said "for the time being keep running things
  and make sure its all smooth"; on 14 November the proxy list was replaced with 40 exits (`yt_proxies_total` 40). The
  two overrides (`worker.workers=10`, `worker.mediaHold={thumb,author_avatar}`) stay until PostgreSQL has at least
  100 GB free; they go together.
- **The capacity plan counts 40 exits again** (fixed by `beb6636`, PR 105). After each restart the scheduler had re-run
  its daily tasks before the hourly plan, so the plan stayed at 10 exits after the move to 40 (the worst monitor gap was
  28 minutes). Since `beb6636` the scheduler keeps each task's last run across restarts and runs due tasks with the
  shortest period first. Its first plan, at about 05:33 Iraq time, counted 37,333 requests an hour; the stretch then
  came down to 5.33 and keeps falling by up to 1.5 times an hour. New alert: CapacityPlanStale (plan over 2 hours old).
- **Incident, 14 November from about 05:20 Iraq time (02:20Z): the shared PostgreSQL restarted** (about 02:37Z and
  02:51Z), and pods of all four collectors were unready. The cause isn't known yet: only the owner's DigitalOcean console
  shows PostgreSQL's log. The master's prime suspect is tks's catch-up job, so the master stopped tks's scheduler.
  Data on disk is 45.1 GB of 200 GB (yts 6.8 GB), so disk space for data isn't the cause. yts's part:
  - three deploys in two hours each re-ran yts's daily tasks; the heaviest, the commenter graph, rewrote its whole
    table (0.21 GB) each time. Since `beb6636` deploys no longer re-run them.
  - `3ce6d20` (PR 106) adds incident switches: `scheduler.pauseTasks` and `scheduler.replicas: 0` (runbook §7). It also
    makes the commenter graph write only changed pairs. The master is deploying it with the commenter graph, privacy
    audit and enrichment order paused until PostgreSQL is stable.
- **For the owner (not urgent): the outbox is yts's biggest table**, 4.6 GB of yts's 6.8 GB. The relay is off (no Kafka
  yet), so every content event waits there, and with 40 exits it grows about 2–4 times faster. Options:
  - (a) turn the relay on when Kafka is ready, so the outbox drains;
  - (b) while the relay is off, keep only the newest unsent event per entity. Every reading stays in the snapshot
    tables, so no data is lost, but Kafka would get the latest state rather than every step;
  - (c) keep it and plan the disk for it.
  yts suggests (b) until Kafka is there; it changes what the relay will publish, so it is the owner's decision.
- **The owner, 14 November: "add the suggested videos to the discovery as well".** Built: channels of suggested videos
  whose titles match the Iraq keywords become discovery candidates, at most 500 a day. Runbook §20.
- **40 exits:** the owner's new list, two logins per port; still with `worker.workers=10` and
  `worker.mediaHold={thumb,author_avatar}` until the database shows 100 GB free.
- **Community post images (fix 21) are built**, under the owner's "all platforms media" (the master's yes, 13 November).
  One request per image, in spare time like other images. Poll options and the linked video are still to do.
- **Waiting:** from the master, ClickHouse bytes per yt event and PostgreSQL CPU and IOPS, to add analysis storage and
  database load to [`master/yts-coverage-sizing.md`](master/yts-coverage-sizing.md).
- **11 November:**
  - **tiers A and B were falling behind, and feed checks ran hours late in every tier** (health at 06:54Z and 07:56Z): priority
    2 (feed checks and refreshes) got almost no time, and priority 3 none, because priorities 0 and 1 took it all. The cause was
    in the code: a job kept the priority it was first queued with, so every later reading of a video first found as new
    stayed at priority 0, and its comment rechecks at 1. [`master/yts-queue-order.md`](master/yts-queue-order.md) has the
    analysis. **The owner approved all three fixes (D-052), and they are built:**
    - Fix 1 (PR 89): feed and tab checks at priority 0.
    - Fix 2 (PR 90): tiers A and B first within a priority.
    - Fix 3 (PR 91): a video's later readings, and comment checks once the video or post is two days old, at priority 2.

    **Live since 11:17Z (`624de33`).** At 12:23Z all three were doing what they should:
    - Feed and tab checks were all on time (0 due), so new uploads are found at the set intervals again.
    - P0 held only 8 new videos, and the rest of the video jobs were P2 refreshes (50,071). P1 comment scans fell from
      12,745 to 4,257.
    - Within P2, tier A and B video refreshes were at most an hour late, against tier C's 5h42m.

    Still to come: P2's oldest work (tiers C, D and M and searches, 11 hours late) and tiers A and B's P3 backlog (older
    videos' comments). The next readings show whether P2 catches up. The P3 backlog also needs the IP decision.

    **By 14:20Z the 10 IPs were full.** They made 12,325 requests an hour, and the busiest hour reached 96% of what 10
    IPs can make. That is not the 10-jobs-at-once limit: only 7 jobs were running.
    - New uploads were found in 25 minutes at the median and 90 at the 90th percentile.
    - No job failed, and there were 0.2 blocks per 1,000 requests.
    - Tier A stayed on time.
    - Tier B fell behind by about an hour each hour. Tiers C, D and M, older videos' comments, images and commenter
      profiles don't keep up.

    About 16–18 IPs keep everything running at today's slowing, and about 20 also clear the backlog. The owner decides
    after the 24-hour capacity plan tonight. The database grows about 1.3 GB a day at this pace.

    **The 24-hour plan (to 03:00 Iraq time on 12 November)** confirmed it:
    - 286,733 requests a day, with 0 failed jobs and 0.3 blocks per 1,000 requests;
    - 198,446 new comments;
    - work that is never slowed comes to 9,733 requests an hour, already above the 9,333 the planner allows for 10 IPs.

    For today's list:
    - 17 IPs run everything at today's slowing;
    - about 30 also run commenter profiles and images, which are the main route to the channels Iraqi audiences interact
      with, and clear the backlog;
    - 62 run everything at the set intervals.

    The owner's coverage target (D-054: every Iraqi channel, plus those Iraqi audiences engage with) is sized in
    [`master/yts-coverage-sizing.md`](master/yts-coverage-sizing.md). It estimates about 30,000 channels (low) to
    150,000 (high), all estimates. At today's slowing, with profiles and images, that needs 83–178 and 321–686 IPs.
  - **3 channel events refused** by the contract checks since about 85 hours ago (retried at each relay restart): each
    channel's page gave neither a title nor a handle, and an account needs one or the other. Fixed (PR 87, live since
    07:32Z): such a channel goes out as a partial account (what is known; consumers keep the name they have), and its page
    is kept as a parse failure. The 3 went out, nothing is refused, and `OutboxRefused` cleared.
- **The owner's decisions of 11 November** (live since `cf43f57`; deployed 10 Nov 22:48Z at `519c220`, then `834ea64` and
  `cf43f57`):
  - **the quarantine fix:** only 3 or more page blocks a day count, on 3 days in a row. Quarantines are saved and lifted
    with `ytcollect proxies release`. A database-backed alert fires if every IP is out, and the worker waits instead of
    failing jobs. Since the deploy: no page blocks, a few player blocks at each restart, nothing quarantined.
  - **the slower refresh:** the capacity planner fits the work to the 10 IPs at 70% busy. It refreshes tiers C, D and M less
    often (up to 8 times), never A and B, and never new-upload detection. Images and commenter enrichment run only in spare
    time.
  - **avatars and banners:** handed off to analysis and deleted; the hand-off is on. Thumbnails were held from 12 November
    (D-052, "Not now for thumbnails"). **The owner released them on 13 November (D-061): "youtube thumbnails turned on"**;
    of avatars and banners: "yes i need them". `worker.mediaHold` is now empty.
    Images run only in spare time, so none have run yet.
  - **the report's IP counts:** they now allow for each answer's time.
- **Capacity, first plans after the deploy** (supply 9,333 requests an hour):
  - the work that is never slowed is already about 10,800 an hour, and the stretch is climbing to its cap of 8;
  - comment scans are about 80% of the demand (video comments 16,312 an hour, post comments 6,417), because most videos
    are under a week old and on the frequent rungs;
  - there is no spare time yet, so images (and the hand-off to analysis) and commenter enrichment wait. Part of the
    demand is the backlog of the 28-hour stop.

  The plan 24 hours on (about 23:56Z on 11 November) gives the steady figure. Then the owner decides between more IPs and
  scanning young videos' comments less often.
- **Built and merged:** every production step of the plan (1–20). That is the collector, comments, posts and playlists, discovery,
  capacity planning, privacy tools (dormant), images, the archive, webhooks, and the Docker Compose deployment, all with tests. See
  [plan.md](plan.md) §10.
- **The 48-hour pilot (fix 11) ran** from 18:29 Iraq time on 7 November, run by the master's ops workflow. Its IPs went into
  quarantine one by one on 9 November UTC, the last at 16:30Z, an hour after its window ended (above). The report will show
  how much its last day lost. The proxies are
  igs's 10 US ISP IPs, shared until yts's own are bought (the owner, D-042). It uses a 2 s gap per IP, 10 jobs at once and a 7-day
  backfill. The 46 Iraq place names are loaded, and the owner's 329 channels were imported at 18:52. An hour later, 374 channels
  were listed (45 more found by discovery), 5 of the owner's inputs were unresolved, and no jobs were failing or dead. The first
  day goes to the new videos and their comments (the top priorities); the rest drains after. The report covers 18:29 on
  7 November to 18:29 on 9 November: [`master/yts-pilot-report.md`](master/yts-pilot-report.md). In short: it works, comment
  sections were complete and YouTube barely pushed back, but the 10 IPs were full. The list grew to 2,646 channels, which need
  at least 19 IPs; the lowest-priority work (backfill, enrichment, images) never got a turn.
- **Deployed to production (7 November, the master):** on the platform's cluster through Argo CD, at `9a4700d`, then pinned to
  `145e68c` (no overrides left but the image and the worker count). The migrations ran (PreSync hook) and the scheduler is
  running and ready. The worker was held at 0 until the pilot (above) started, on igs's shared IPs until yts's own are bought.
  Push stays off. The two fixes the deploy needed are merged: the London VPC (`10.30.0.0/16`, ports 5432 and 25073) and the
  registry's pull secret in `values-production.yaml`.
- **The relay is live** (the master, 20:24Z on 7 November): `yts-relay` running since 20:17Z, canary checks on, the media hand-off
  off until the owner decides. Kafka granted the transactional ID `yts-relay`, so batches go in transactions. In its first 8
  minutes the platform's writer took 273,387 yt events into ClickHouse (posts, comments, metrics, accounts, media, discovery
  candidates, coverage) with 0 dead letters and no insert errors. Should Kafka ever refuse the transactional ID,
  `relay.transactions: false` sends without transactions (runbook §16).
- **Phase 0:** has ended (the owner, 6 November). The census of 5 November was not run: there are no new long runs on the shared
  server (D-026). The final numbers come from the 4 November report and the 5 November discovery, into `docs/phase0-results.md`.
- **Now building:** the owner's fix list (REQ-010) and the contract adoption (REQ-021), approved on 6 November. Progress, one row per
  fix: [`master/yts-fixes.md`](master/yts-fixes.md).
  - **Done:** every fix before the pilot: 1 to 10, 13 (non-root image), 14 (Helm chart), 16 (pinned bases, image scan, releases)
    and 17 (database TLS and roles), plus the proxy `country=` tag and PostgreSQL 18; and fix 12 (below).
  - **Fix 12, the outbox and relay, is done:** all 7 PRs merged on 7 November, as planned in
    [`master/yts-contract-adoption.md`](master/yts-contract-adoption.md). The owner answered on 7 November: sent outbox rows go
    after 7 days, and the code generator may run here. **PR 1, the outbox, is merged**
    ([#60](https://github.com/Abdullah95k/securebrand-microservices-youtube/pull/60)): every write adds its events in the same
    transaction as the data; no Kafka yet. **PR 2, the contract mapping, is merged** ([#61](https://github.com/Abdullah95k/securebrand-microservices-youtube/pull/61)): every event becomes a contract v1.2.0
    message checked by the contract's conformance package; `ytcollect outbox check` runs the checks on real events. **PR 3,
    the `yt.*` messages, is merged** ([#62](https://github.com/Abdullah95k/securebrand-microservices-youtube/pull/62)): the platform events with every field yts holds, generated with buf, checked in CI. **PR 4, the relay, is
    merged** ([#63](https://github.com/Abdullah95k/securebrand-microservices-youtube/pull/63)): `ytcollect relay` sends the outbox to Kafka, both message sets in one transaction. **PR 5, the relay in the
    chart, is merged** ([#64](https://github.com/Abdullah95k/securebrand-microservices-youtube/pull/64)): off until Kafka is there; `relay.enabled=true` turns it on. **PR 6, discovery candidates
    and hourly coverage, is merged** ([#65](https://github.com/Abdullah95k/securebrand-microservices-youtube/pull/65)); it also stops a video first seen through the official API from going out before its
    website read. **PR 7, the media hand-off, is merged**
    ([#66](https://github.com/Abdullah95k/securebrand-microservices-youtube/pull/66)): thumbnails are announced to analysis, and each file is deleted once
    analysis acknowledges it, or at its 72-hour deadline; the record stays. It runs in the relay; the
    master keeps it off until the owner decides, so every image is kept.
  - **REQ-023 and REQ-024 (the owner, 7 November): done**, merged in
    [#68](https://github.com/Abdullah95k/securebrand-microservices-youtube/pull/68) (merge commit `7f9677ca5fe853160821b2b9226f6e4deea4396c`,
    sent to the master on 7 November with the test method and the idle numbers). yts now fits the platform's PostgreSQL behind PgBouncer in
    transaction mode: no session locks (the relay holds a lease row; the migrations and new partitions use locks that end with
    their transaction), no statements prepared on the server, no startup parameters, at most 4 connections per process, and a
    wait for a free connection when the database is full (45 minutes for the migrations). While idle it touches the database far
    less: readiness checked at most once a minute, liveness without the database, polling that backs off to every 30 seconds.
    Idle queries per minute through PgBouncer, before and after: worker 60 → 12, scheduler 45 → 36 (one burst a minute, the
    gauges), relay 67 → 16 (12 of them the lease renewal every 5 seconds the platform asks for). The test suite runs through
    PgBouncer in transaction mode with prepared statements off, here and in CI.
  - **Next:** the pilot report (fix 11) after 18:29 on 9 November. The relay sends the outbox as it fills (above).
- **Legal sign-off:** received (D29). Comments and all their public data are collected.

## Approved by the owner (11 November): the quarantine rule

What stays the same: the 2 s pace, the rests after a block (30 min, 2 h, 12 h, 24 h), trying another IP after a block, parking a
request blocked on 3 IPs, the pool brake and the WEB client.

What changes:

1. **A day counts towards quarantine only if it was a bad day:** at least 3 blocks on page requests that day (player blocks don't
   count, as they already don't for the brake and for the healthy-IP count), and on 3 days in a row, as the plan says.
2. **A quarantine is saved and restored at start,** so a restart no longer undoes one, and a command lifts it
   (`ytcollect proxies release`).
3. **An alert for "every IP quarantined" from the database.** It works even when the worker isn't Ready or isn't scraped.
4. **The worker stops taking jobs while no IP is usable,** instead of failing each one and pushing it hours back.

The block counts the master read (47, all on the player, in about 393,000 requests) show the pace and the exits are fine.

**Decided (the owner, 11 November, through the master): refresh channels less often for now; more proxies come later.** Done
in the capacity planner: it now fits the work to the 10 IPs at 70% busy and refreshes tiers C, D and M less often as needed,
never A and B and never new-upload detection (runbook §7).

## Ready for the pilot (fix 11)

yts's side is ready on `main` at `edab872` (6 November):

| Item | Value |
|---|---|
| Chart | `deploy/helm/yts`, values `deploy/helm/yts/values-production.yaml` ([README](../deploy/helm/yts/README.md)) |
| Image | Built from the repository's `Dockerfile` (bases pinned by digest) with `--build-arg VERSION=<tag> --build-arg REVISION=<commit>`, or published by pushing a `v*` tag (`ghcr.io/<owner>/yts:<tag>`) |
| Helm image keys | `image.repository`, and `image.digest` (preferred) or `image.tag` |
| Secrets it mounts | `platform-credentials` (`POSTGRES_*`), `proxies` (`proxies.yaml`), `yts` (`privacy_key`, made by the owner from the README) |
| Pilot settings | One worker, 2 s per proxy IP, 10 jobs at once, a 7-day backfill; the cost report with `ytcollect report` |

**Still needed before it starts:** the cluster and namespace; the proxies (bought, and in the `proxies` secret); the owner's `yts`
secret. The owner starts and stops the pilot; I give the commands, one per step, and write `docs/master/yts-pilot-report.md`.

## What runs where

| Where | What | Since |
|---|---|---|
| Shared pilot server (`201.79.36.209`) | `ytprobe-detect` (Phase 0 new-upload detection, RSS every 15 minutes for 327 channels). No memory limit. **To be stopped by the owner** (its data files are kept) | 2 November; restarted 4 November |
| Shared pilot server | Owner-run `ytprobe` jobs (`discover`, `census`), about an hour in total | Last run 5 November |
| Proxies | The 10 ISP IPs of fbs's pilot, shared with fbs | 2 November |
| Anything else | Nothing | |

## Decisions (owner, 6 November)

1. **Production and the 48-hour pilot run on the platform's Kubernetes cluster** (D-012, D-026), once the owner confirms it is
   ready. Nothing is bought for yts; no new long runs on the shared server.
2. **Order:** fixes 13, 14, 16 and 17 (non-root image, Helm chart, releases, Postgres TLS) come before the pilot. The pilot began
   without Kafka; the relay joined it at 23:17 Iraq time on 7 November. Discovery runs during it at low priority.
3. **Proxies:** the owner confirms with the purchase whether yts gets its own IPs, with a country for each.
4. **Media for analysis:** thumbnails and community-post images, kept until analysed. No audio, video or transcripts.
5. **The contract v1.0.0 is published** (D-016). The `content.*` mapping follows the outbox (fix 12).

**Answered by the master (6 November):**
- the database `yts`, with the roles `yts`, `yts_app` and `yts_ro`;
- credentials arrive as files from the secret `platform-credentials`;
- proxies arrive in the secret `proxies`, key `proxies.yaml`, one line per proxy with `country=`;
- raw responses go under `yts/keep/` and `yts/30d/` in the raw bucket. The platform expires `yts/30d/`; yts never deletes.

**Answered by the master (6 November, fixes 14 and 17):**
- `platform-credentials` carries `POSTGRES_CA_CERT` (the PEM): yts now verifies the server (`verify-full`).
- `POSTGRES_USER` owns the `yts` database and its `public` schema; `migrate` grants `yts_app` and `yts_ro`.
- Object storage comes from the same secret (`S3_ENDPOINT`, `S3_REGION`, `S3_ACCESS_KEY`, `S3_SECRET_KEY`, `S3_BUCKET_RAW`,
  `S3_BUCKET_MEDIA`): the chart now reads them, and storage is on in the production values.
- The contract is at v1.2.0 (additions and checks only). The master wants, when REQ-010 is done: the commit, the image details,
  the chart path and values file, the Helm image keys, and the outbox topics with their retention.

- Network: the managed services sit in the production VPC (`10.30.0.0/16`, London since 7 November: D-030) on PgBouncer's 5432 and Kafka's 25073; the schema registry in
  namespace `platform`; HTTPS anywhere; the proxy exits once bought. The chart's network policy now follows this.
- Prometheus picks up every ServiceMonitor and PrometheusRule; no labels needed. Alerts go to the owner's Telegram.

**Still open with the platform:** the proxy exits' addresses, once they are bought.

**Changes to report (additions only):**
- Incident switches for the scheduler (14 November, the master's ask during the database restarts): chart values
  `scheduler.pauseTasks` (flag `-pause-tasks`) and `scheduler.replicas` (0 pauses it; the chart refuses more than 1).
  Production values unchanged. Runbook §7.
- The commenter graph (same day): the daily task now writes only new pairs and pairs whose count or last sighting
  changed, instead of rewriting every row of `channel_links_graph`. Same contents; far less WAL and fewer dead rows.
- The scheduler's last runs (14 November, with the master's yes): new settings rows `scheduler/last/<task>` (yts's own) for
  the tasks an hour or more apart; a restart no longer re-runs the daily and weekly tasks (so the tiers are recomputed
  weekly as planned, not at every deploy). New metric `yt_capacity_report_timestamp_seconds`, new alert
  `CapacityPlanStale` (24 alerts now).
- Suggested videos in discovery (the owner, 14 November): the watch page's side list is parsed (no extra request); a
  suggestion matching the Iraq keywords from an unknown channel becomes a candidate with the new source value `suggested`
  (`candidates.sources`, no schema change), at most 500 a day (`worker.suggestedDaily`, `-suggested-daily`). New metric
  `yt_discovery_suggested_24h`, new fill-rate field `watch.suggestions`.
- The proxy list (14 November): plain `HOST:PORT:USER:PASS` lines are accepted, and an exit is its login and address, so
  two logins on one host and port are two exits (before, they were refused as duplicates and the worker would not start).
  Exit IDs (`px-…`, in metrics and `ytcollect proxies`) include the login now, so they changed once.
- Facebook discovery (D-065): migration `0021` adds the table `fb_candidates` (yts's own: the links sent and the
  collector's answers); the metric `yt_fb_candidates{result}`; the scheduler flags `-fb-url`, `-fb-key-file`, `-fb-daily`
  and the chart's `scheduler.fbDiscovery` and `networkPolicy.egress.fbDiscovery`.
- Community post images (fix 21, 13 November; the owner's "all platforms media"):
  - migration `0020` adds the column `posts.image_urls` and the media kind `post_image` (the `media` table's kind check);
  - `yt.post.v1` gains the field `image_urls` (24), an addition `buf breaking` accepts;
  - post images go out as `content.media.v1` records (role `MEDIA_ROLE_CONTENT`, owner the post) and are handed to analysis
    like thumbnails (`relay.media.kinds` now lists `post_image`), then deleted once analysed. They are erased with the post or
    its channel.
- The queue's order (D-052), on yts's own `schedule` table only:
  - Migration `0017` moves feed and tab checks to P0.
  - Migration `0018` adds the column `schedule.tier_rank`, the function `queue_tier_rank` and two triggers that keep it in
    step with the channel's tier, and rebuilds the index `schedule_due` on (priority, tier_rank, due_at).
  - Migration `0019` moves the rows already queued, by the rule the workers now follow: a job's priority follows what it
    does each run.

  The capacity planner counts C, D and M's later video readings as refreshes it can slow, as the handler already did.
- `content.account.v1`: a monitored channel whose page gave neither a title nor a handle is sent as a partial account
  (`envelope.partial`, as for commenters without a name) instead of being refused. A channel page without a title is now
  recorded as a parse failure even when it has tabs.
- `ytcollect health` adds the overdue work by tier, kind and priority, and how much of each tier's the 10 busiest channels hold
  (counts only). `yt_outbox_oldest_unsent_seconds` no longer counts refused events (they are `yt_outbox_failed`): 3 refused
  events kept `OutboxBacklog` firing.
- Go 1.27.4 (11 November): `go.mod` and the build image (`golang:1.27.4-trixie`, pinned by digest), for GO-2026-6859 (a race
  in `net/http` when a request body hits `MaxBytesReader`'s limit). Nothing else changes.
- `ytcollect proxies` splits blocks into page and player blocks: migration `0016_proxy_player_blocks.sql` adds
  `proxy_health.player_blocks`. The capacity plan adds `demand_by_kind` (its log line: `top_kinds` and `spare_debt_hours`), and
  the scheduler exports `yt_capacity_fixed_per_hour`, `yt_capacity_stretchable_per_hour`, `yt_capacity_spare_time_per_hour` and
  `yt_capacity_spare_debt_hours`.
- The capacity planner (11 November, after the first plan on `519c220`): a stopping worker marks its announcement stopped
  (new field `stopped` in `capacity/<worker>`), so a restart no longer counts its IPs twice for 15 minutes. The P4 backlog
  (images, enrichment) is no longer freshness debt: `capacity_reports.debt` and `yt_freshness_debt_hours` leave it out, and
  `spare_debt_hours` reports it apart.
- The quarantine rule (11 November, the owner: "ok fix the quarantine rule"): a day counts only after 3 page blocks (player
  blocks never count), and an IP is quarantined after 3 such days in a row. Migration `0015_proxy_quarantine.sql` adds two
  columns to `proxy_health` (`quarantined_since`, `released_at`): a quarantine outlives a restart, and the new command
  `ytcollect proxies` (list, `release ID`, `release -all`) lifts it. New gauge `yt_proxies_reporting` and alert
  `AllProxiesQuarantined` (from the database, through the scheduler). While no IP is usable, the worker takes no jobs.
  Runbook §7.
- The capacity planner leaves the lowest-priority work (P4: images and commenter enrichment) out of its demand: it runs in
  the time left and never slows a refresh; `spare_time_per_hour` in the log and `capacity_reports.detail`.
- The media hand-off (11 November, the owner: "yes", and avatars and banners "collect them for analysis then delete them as
  well"): channel avatars and banners are handed off and deleted like thumbnails, by default. New relay flag `-media-kinds` and
  chart value `relay.media.kinds` (default `[thumb, avatar, banner]`; `author_avatar` adds commenters' avatars). Turning the
  hand-off on in production is `relay.media.handoff: true`. Runbook §17.
- The capacity planner (11 November, the owner's decision to refresh less often): supply counts each answer's time and 70% busy
  (about 933 requests an hour per IP at 2 s, was 1,440); demand uses each job kind's cost measured over the last day; the stretch
  is what fits the IPs, moving at most 1.5 times an hour. New-upload detection (the feeds and tab checks) is no longer stretched
  at any tier. New: the scheduler's `-min-stretch` flag and chart value `scheduler.minStretch` (default 1), and `fixed_per_hour`,
  `stretchable_per_hour`, `min_stretch` and `measured_cost` in `capacity_reports.detail`. `yt_capacity_demand_per_hour` now
  means the demand without any stretch. Runbook §7.
- `ytcollect report` (11 November): the IP counts now allow 0.7 s for each answer on top of the gap (an IP makes about 1,333
  requests an hour at 2 s, not 1,800), so they come out about a third higher; new fields `proxies` and `peak_use` (the busiest
  hour against what the worker's IPs can make), and a note when that is 90% or more, saying the counts are a floor.
- The relay's `-transactions` flag and the chart value `relay.transactions` (default `true`, as before). `false` sends each
  batch without a Kafka transaction (idempotent sends), for a Kafka that can't grant the transactional ID; runbook §16.
- REQ-023 and REQ-024: migration `0014_leases.sql` (a new table `leases`); the migrate command's `-wait` flag (45 minutes) and the
  chart's `migrate.waitForSlot` and `migrate.activeDeadlineSeconds` 3600 (give `helm upgrade` `--timeout 60m`); the relay's
  `-idle` now means the longest idle wait (30 seconds); the metric `yt_relay_up` now means "holds the lease"; runbook §18. The
  database tests now make a database per test instead of a schema, so the test role must be allowed to create databases.
- Fix 12, PR 7: migration `0013_media_handoff.sql` (five new columns on `media`, a new table `media_acks`); the relay publishes
  `content.media.v1`, `content.media.ready.v1`, `content.media.deleted.v1` and `yt.media.v1`, and **reads**
  `content.media.processed.v1` in consumer group `yts-media`; relay flags `-media-handoff`, `-media-keep`, `-media-group`,
  `-media-consumers` and chart values `relay.media.*`; metrics `yt_media_handoff`, `yt_media_past_deadline`,
  `yt_media_announced_total`, `yt_media_acks_total`, `yt_media_deleted_total`, `yt_media_delete_failures_total`; alert
  `MediaPastDeadline`; runbook §17. One change of meaning: `yt_media_bytes` now counts only files still in storage. Image
  downloads now appear in the provenance trail (operation `image`).
- Fix 12, PR 6: the scheduler's hourly "coverage roll-up" task; candidates and coverage go to `discovery.candidate.v1` and
  `collection.coverage.v1`.
- Fix 12, PR 5: the chart's `yts-relay` Deployment and Service (off by default and in production values), values `relay.*`, the
  ops port 9103 in the ServiceMonitor and NetworkPolicy.
- Fix 12, PR 4: the command `ytcollect relay` (a new long-running service: ops port 9103; the chart adds it in PR 5), the
  `YT_KAFKA_DIR` setting, the metrics `yt_relay_*` and `yt_outbox_failed`, the alerts `OutboxBacklog` and `OutboxRefused`, the
  runbook's §16, and the Go dependency franz-go (the Kafka client).
- Fix 12, PR 3: the `yt.*` message definitions (`proto/securebrand/yt/v1/yt.proto`, package `securebrand.yt.v1`), their generated
  Go code (`gen/go`) and a CI job `proto`. The topics already exist; nothing is registered.
- Fix 12, PR 2: the command `ytcollect outbox` (and `outbox check`), read-only; the Go dependency
  `github.com/abdullah95k/securebrand-contracts` v1.2.0 (the platform contract, with the Protobuf library it needs).
- Fix 12, PR 1 ([#60](https://github.com/Abdullah95k/securebrand-microservices-youtube/pull/60), merged): two tables
  (`event_outbox`, `outbox_state`) in migration `0012`; no existing table changed. The scheduler deletes sent outbox rows 7 days
  after sending (the owner's answer); unsent rows stay. Metrics `yt_outbox_unsent` and `yt_outbox_oldest_unsent_seconds`. Erasure
  also deletes an item's outbox rows.
- Fix 16: `ytcollect version`, the metric `yt_build_info`, OCI labels on the image, and a release workflow that publishes
  `ghcr.io/<owner>/yts` only when a `v*` tag is pushed.
- Fix 14: the Helm chart; a storage `prefix` setting (media under `yts/`); a `runbook_url` on every alert.
- Fix 17: `YT_POSTGRES_DIR` (the platform's credential files). Each command connects as the owner, app or read-only role; `migrate`
  grants the app and read-only roles. Without TLS, a connection to another machine is refused. Compose is unchanged.
- Fix 13: the image runs as user 65532, not root. Secret files must be readable by 65532: owned by it on a server, or through the
  pod's fsGroup on Kubernetes. The Compose runbook has a new `chown` step. Phase 0 images already built are unaffected.
- Fix 8 adds the worker settings `YT_RAW_STORAGE_FILE`, `-raw-sample` and `-raw-30d`, and the metrics `yt_raw_objects_total` and
  `yt_raw_bytes_total`. `parse_failures.raw_ref` is now filled. Raw files hold what the page shows, personal data included, and
  erasure doesn't reach them.
- Fix 5 adds three tables (`usage_hourly`, `usage_channel_daily`, `db_sizes`), the command
`ytcollect report`, the worker flag `-backfill-days`, and a `backfill_days` field in the workers' capacity record. Nothing was renamed
or removed. The outbox migration moves to `0012`.

## Reports for the master session (6 November)

In [`docs/master/`](master/):

| File | What |
|---|---|
| `yts-field-inventory.md`, `.yaml` | Every field yts collects |
| `yts-operations.md` | Components, deployment, proxies, resources, reliability |
| `yts-contract-notes.md` | The Kafka outbox plan, the archive and ClickHouse, the API terms. **REQ-004** answers at the end |
| `yts-gaps.md` | Gaps against the service standard, and the plans |
| `yts-contract-review.md` | **REQ-013**: the review of the contract draft v0.1 |
| `yts-contract-adoption.md` | **REQ-021 and fix 12**: the plan for the outbox, the relay and the `content.*` mapping on contract v1.2.0, with the topics, keys and retention, the master's answers (6 November) and the build order (seven PRs, about 4–5 weeks) |
| `yts-fixes.md` | REQ-010 progress, one row per fix |
| `yts-ips-discovery-media.md` | **D-060**: what 44 IPs change and what to read; how discovery finds channels; media for analysis (why videos can't be handed over within the rules); **CLIENT-REQ-020**: count readings and comment picking |

Waiting: `ytprobe-detect`'s memory (REQ-004 §5), from the owner's `docker stats` readings.

**Open questions (7 November):**
- For the master: may yts's Kafka user use consumer group `yts-media` to read `content.media.processed.v1` (the media
  acknowledgements)? The relay reads it only once Kafka is there.
- For the owner: yts also downloads channel avatars, banners and commenters' avatars, and keeps them. Only thumbnails are handed
  to analysis and deleted after it (and post images once fix 21 collects them). Keep downloading the others, or stop?
  **Answered 11 November (D-050)** for listed channels' avatars and banners: handed off to analysis and deleted, like
  thumbnails. Commenters' avatars are still downloaded and kept; the relay's `-media-kinds` can hand them off too if the
  owner wants.

## Standing rules (from the owner, 5 November)

- Never rename or remove tables, fields, schemas or endpoints others use; additions are fine, and each is reported.
- Nothing heavy on the shared pilot server without the owner's approval. Don't restart or rebuild yts containers there without telling
  the owner.
- Propose, then build.
