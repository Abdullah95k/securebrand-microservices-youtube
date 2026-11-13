# yts: 44 IPs, discovery, media and the client's two questions

For the master and the owner, from the yts session. Written 13 November 2026 at 14:00 Iraq time, from the code and the
last readings (counts only). (G) marks an estimate.

## 1. From 10 to 44 IPs (D-060)

**A restart is enough.** The worker reads its proxy list once, at start. When the platform secret has the 44 lines, restart
the worker and it picks them up. Nothing else needs a restart. The capacity planner reads each worker's IP count from the
worker's own report, within 15 minutes.

**These adjust by themselves:**

- **Pace per exit:** still 2 seconds between requests on each exit (Phase 0). Each new exit starts with its own clean pace,
  rest and quarantine record.
- **The subnet rest rule:** with `subnet=` set to each line's own port, each exit is its own group, as the 10 are today.
  yts rests blocked exits one at a time, never a group. If the 34 share a few real address ranges, a block on a whole
  range shows as several exits resting at once. That is no different from today, so nothing to change.
- **The brake and the alert:** both use shares, so they scale with the pool. The brake slows down when more than 20% of
  exits are blocked in an hour and pauses above 50%. The alert fires when fewer than half the exits are healthy.
- **The capacity planner:** supply goes from about 9,300 to about 41,000 requests an hour (44 × 933, at 70% busy). The
  slowing of tiers C, D and M eases by itself, by at most 1.5 times an hour: from 8 to about 1.5 in about 5 hours. At 1.5,
  tiers C, D and M are read at 1.5 times their set intervals. Tiers A and B and new-upload detection stay at their set
  intervals, as now.
- **The thumbnail hold** stays. More IPs don't change it.

**One change I propose: 30 jobs at once instead of 10.**

- Today the worker runs at most 10 jobs at once. With 10 IPs that was never the limit: when the IPs were full, 7 jobs were
  running.
- With 44 IPs it will be the limit. A job reads one page after another, so 10 jobs make at most about 30,000–36,000
  requests an hour (G). That is below the 41,000 the planner will plan for, so the backlog would keep growing while the
  IPs sit partly idle.
- The change is `worker.workers: 30` in the production values: one line, no code. Database connections stay at 4 per
  process (REQ-023). A job holds a connection only while it writes.
- Best done in the same restart as the new IPs. I'll build it when you say yes.

**What grows, at 3 to 5 times today's requests (G, from the 24-hour plan):**

- **Requests:** from about 287,000 a day to 1.0–1.4 million.
- **PostgreSQL:** today it grows 1.75 GB a day. 1.2 GB of that is the outbox, and sent events are deleted after 7 days.
  With 44 IPs that becomes:
  - about 2–3 GB a day kept for good;
  - an outbox that levels off at 7 days of events, about 25–40 GB.

  **Please check the database's free disk before the change.**
- **Raw answers:** every answer is also written under `yts/30d/` (33 KB each). That is about 9.5 GB a day today, and
  33–46 GB a day with 44 IPs. If the bucket deletes them after 30 days, it holds about 1–1.4 TB. Please check that the bucket
  has that 30-day rule; without it the folder grows for ever. The 1% sample and the failures are kept forever either way.
- **Proxy traffic:** `yt_proxy_bytes_total` counts it. Watch it if the IP plan caps traffic, the more so because Instagram
  and TikTok share the exits.

**What spare time starts.** Nothing has run in spare time yet. With 44 IPs, after the backlog, it starts:

- **Commenter profiles:** about 385,000 waiting. This is discovery's commenter route (§2).
- **Avatars and banners of listed channels:** downloaded and handed to analysis (D-050). You say analysis skips them by
  its policy, so yts then deletes them. That costs IP time for nothing until analysis takes them. **Option:** hold them too
  (`mediaHold: [thumb, avatar, banner]`, one line) until analysis is ready.
- **Commenters' avatars:** every profile read queues its avatar, which is downloaded and kept, not handed off. That is up to
  385,000 images from the backlog, then one for each new commenter. The question of 7 November, keep or stop these, was
  never answered for commenters. They are public, but they are private people's pictures, and nothing uses them today.
  **I propose holding them** (add `author_avatar` to the hold) until the owner decides.

**What to read in the first hours** (all counts):

1. The worker's start log: `worker running` with `proxies` 44.
2. `ytcollect proxies`: 44 healthy and 0 quarantined. Blocks per 1,000 requests should stay near today's 0.2–0.3; new exits
   may start a little worse.
3. These metrics:
   - `yt_pool_speed` stays at 1 (no brake);
   - `yt_pool_subnet_cooldowns_total` stays flat;
   - `yt_proxies_healthy` is 44.
4. The scheduler's hourly `capacity plan` log:
   - `supply_per_hour` about 41,000;
   - `stretch` falls from 8 to about 1.5 over about 5 hours;
   - `recommended_ips`.
5. `ytcollect health`:
   - requests an hour, and the busiest hour's share of what the IPs can make;
   - the oldest waiting job per priority. Priority 2 should shrink first, then 3; then priority 4 (profiles, images) starts.
6. `yt_queue_running`: if it sits at 10 while health shows the IPs well under 70% busy, the 10-jobs limit is what holds
   it back (the change above).
7. After a day:
   - `yt_discovery_listed_24h` should rise;
   - `yt_enrichment_backlog` should fall;
   - `yt_db_size_bytes` shows the growth.

## 2. Discovery: how YouTube channels are found

**The routes that run today:**

| Route | Priority | What it does |
|---|---|---|
| Website keyword search | 2 | 46 Iraq place names plus the seed and owner keywords, newest first, up to 5 pages. About 4 requests a search. |
| Official API search | 2 | Region Iraq, Arabic, priority keywords. At most about 95 searches a day (the quota). |
| Candidate check | 3 | Each channel a search finds becomes a candidate. yts reads its page, About panel and first Videos page (about 3 requests) and lists it as Iraqi, as a mention channel, or not at all. |
| Commenter route | 4 (spare time) | Each new commenter with videos of their own becomes a candidate once their profile is read. Built, never run: there has been no spare time. |

Not built, each needs the owner's approval:

- links and featured channels on listed channels' pages;
- @mentions in titles and descriptions;
- YouTube's trending lists for Iraq;
- links from the same creators on other platforms.

[`yts-coverage-sizing.md`](yts-coverage-sizing.md) §2 has the detail.

**How it is doing** (the last readings, 12 November):

- **2,898 listed.** The owner imported 329 on 7 November. Discovery found the rest, about 1,100 a day in the first two days.
- **Since the stop of 9–10 November, almost nothing.** The IPs are full, so searches and candidate checks get little time:
  - in one day, 114 searches and 38 candidate checks;
  - 3,286 candidates waiting;
  - about 385,000 commenters waiting for the commenter route.
- **What holds it back is IP time,** not a shortage of channels. With 44 IPs it should start again within hours.

**How to read it yourself** (counts only):

| What | Where |
|---|---|
| Channels discovery listed in the last 24 hours, Iraqi or mention | `yt_discovery_listed_24h{kind}` |
| Candidates by status: `new` (waiting), `checked`, `listed`, `no_match` | `yt_discovery_candidates{status}` |
| The whole list by tier | `yt_channels_listed{kind,tier}` |
| Commenters waiting for the commenter route | `yt_enrichment_backlog` |
| Searches and candidate checks waiting, and how late | `yt_queue_due` and `yt_queue_oldest_due_seconds` for `kind` `search` and `candidate` |
| The last 7 days | No metric. A read-only count: `SELECT kind, count(*) FROM channels WHERE added_by = 'discovery' AND first_seen >= now() - interval '7 days' GROUP BY 1` |

## 3. Media for analysis

**Videos can't be handed over within the owner's rules.**

- In 2026, YouTube's video and audio servers answer only requests that carry an anti-bot token ("proof of origin"). Each
  file's address also has to be unlocked by running YouTube's own player script.
- Making those tokens, or running the player's challenge, is what the standing rule forbids ("Don't add CAPTCHA or
  challenge solving, or anything that makes anti-bot tokens").
- The owner also decided on 6 November: "No audio, video or transcripts."

So there is no option to price. Shorts only, the last 7 days' videos and everything new would each break the same rule.
For scale only (G):

- a Short of 30–60 seconds at low quality is 2–5 MB;
- a 10-minute video at 360p is about 50 MB;
- audio alone is about 1 MB a minute.

**Allowed, built or easy:**

| What | State | Cost |
|---|---|---|
| Thumbnails: one picture per video (480×360) | Built and handed off. Held since D-052 ("Not now for thumbnails"). Releasing the hold is one line. | One request per video, again after 7 days. About 20–40 KB each (G). The only picture of each video yts can give today. |
| Community post images | Fix 21, not built | One request per image |
| Frame previews: the small frames YouTube shows over the progress bar, a grid of low-resolution pictures every few seconds | Not built. They appear to need no token, but that must be checked first (a few minutes, like E12). A new collection method, so it needs the owner's approval. | One or a few requests per video (G) |

**The text yts already gives analysis for each video** (on `content.post`, with the Phase 0 share of videos that have it):

| Field | Share |
|---|---|
| Title | 100% |
| Description, in full | 87% |
| The uploader's tags | 63% (only when the player answers, about 85%) |
| Category | 85% |
| Length | 94% |
| Chapters | 1% |
| Caption languages | 0%: logged out, the player lists no caption tracks, and fetching caption files needs anti-bot tokens anyway |
| Transcripts | None: Phase 0 E12, 0 of 23 requests returned text without an anti-bot token |

On top of that come all the comments and replies (§4). So captions exist for 0% of videos as far as yts can see. yts
can't tell, logged out, whether a video has captions at all.

## 4. The client's two questions (CLIENT-REQ-020)

### How often yts re-reads views, likes and the comment count

These are the set intervals, by the video's age and the channel's tier:

| Video age | A | B | C | D | M |
|---|---|---|---|---|---|
| Under 2 days | 30 min | 2 h | 6 h | 6 h | 2 h |
| 2–30 days | 6 h | 12 h | 1 day | 1 day | 12 h |
| 30 days to 1 year | 1 week | 1 week | 1 month | 1 month | 1 week |
| Over 1 year | 1 month | 1 month | 3 months | 3 months | 1 month |

- **Live streams:** viewers every 5 minutes for A and B, every 15 for the others, while live.
- **The slowing:** C, D and M intervals are multiplied by the planner's slowing (8 today, about 1.5 with 44 IPs). A and B
  are never slowed.
- **Running late:** these are due times. While the IPs are full, readings run late. Tier A is on time, tier B up to about
  an hour behind, and C, D and M later.
- **Events:** a `content.metrics` event goes out when a count changes, or when the last one is a day old.

### Which comments yts takes

- **The first read** of a video or post:
  - takes every comment, newest first, with all the replies of each thread;
  - stops at 20,000 comments, replies counted. A video that reaches the cap is marked partial (`coverage`: state
    `PARTIAL`, reason `cap`), and its oldest comments beyond the 20,000 are not read.
  - If a video has no sort menu, yts reads YouTube's default order.
- **Replies:** YouTube has one level of replies; a reply to a reply sits in the same thread. yts reads every page of a
  thread's replies.
- **New-comment scans:** newest first. A scan stops after the first page whose top-level comments are all known, plus one
  more page. It re-reads replies only where a thread's reply count grew. Scans stop one year after the upload.

  | Video age | A | B | C | D | M |
  |---|---|---|---|---|---|
  | Under 2 days | 1 h | 3 h | 6 h | 6 h | 3 h |
  | 2–7 days | 6 h | 12 h | 1 day | 1 day | 12 h |
  | 7–30 days | 1 day | 2 days | 1 week | 1 week | 2 days |
  | 30 days to 1 year | 1 month | 1 month | 3 months | 3 months | 1 month |
  | Over 1 year | none | none | none | none | none |

  C, D and M are slowed as above.
- **Full sweeps** at 1 week and 1 month (tiers C and D: 1 month only):
  - every comment again, up to the cap, with all replies;
  - a comment missing from two sweeps is marked deleted, and its text is kept.
- **Community posts:** newest-first scans by the post's age, no sweeps.
- **"Top" order:** yts doesn't read it, so YouTube's top ranking isn't recorded. Each comment carries its likes and reply
  count, and whether it is pinned, hearted by the creator, written by the channel's owner, or edited.

**What each comment says about how it was read** (the envelope's `provenance`):

- **`source_operation`:** the YouTube operation the page came from.
  - Video comments: always `next` (the watch page and its comment pages).
  - Community post comments: the first batch of a run can say `browse` (the post's page, which opens the comment
    section), later ones `next`.
- **`mode`:**
  - `INCREMENTAL` for first reads and new-comment scans;
  - `FULL` for sweeps.

  So `mode` tells a sweep from a scan, but not a first read from a later scan.
- **`source_variant`:** yts sets none. Neither the contract fields yts fills nor its own `yt.*` events have one. yts's
  `yt.comment.v1` events carry `job_kind` (`comments`, `post_comments` or `sweep`) and every operation read.
- **Partial videos:** the post's `coverage` says whether its comments are complete, partial (the cap), not read yet or
  turned off, with the share collected.
