# yts: the 48-hour pilot (fix 11)

For the owner and the master session. Written 11 November 2026 (Iraq time) from `ytcollect report` for **7 November 18:29 to
9 November 18:29 Iraq time** (15:29Z to 15:29Z), which the master ran on 10 November, and from the worker's metrics the master read
the same day. Counts only: no channel names, IDs or text.

## In short

- **The collector works at this size.** It ran 2 days on the production cluster with 10 shared IPs. It kept every comment
  section complete (100%), had no failed or dead jobs, and met almost no resistance from YouTube: 47 blocks in about
  393,000 requests, all on the video player, none on pages.
- **It then stopped itself, through a bug of mine.** The rule that sets a blocked IP aside ("quarantine") counted one player
  block on each of 3 days as a reason. By 16:30Z on 9 November, an hour after the window ended, all 10 IPs were set aside and
  collection stopped. The IPs were never the problem. The fix is proposed in [`../status.md`](../status.md); a restart clears
  it until then.
- **The list grew to 2,646 channels in two days** (from 329 imported), because discovery has no cap. That growth drives what
  everything else costs.
- **What 2,646 channels cost: at least** 191,000 requests and 6.5 GB of proxy traffic a day. "At least" because **the 10 IPs
  were full.** In the busiest hour each IP made about 1,320 requests, which is the most one can make at the 2 s pace: the gap
  starts after each answer, and an answer took about 0.7 s. The lowest-priority work never got a turn in the window: the
  backfill of the owner's channels' older videos, commenter enrichment and images show no cost at all.
- **IPs:** by the plan's rule (each IP at most 70% busy, plus 30% spare), today's list needs **at least 19 IPs**, 8 for 1,000
  channels and 69 for 10,000. The report's own column says 14, 6 and 51: it counts 1,800 requests an hour per IP, which the
  pace allows on paper but no IP reached. I'll correct the report's formula.

## What ran

| Setting | Value |
|---|---|
| Where | The platform's Kubernetes cluster (London), chart `deploy/helm/yts`, pinned at `145e68c` |
| Proxies | igs's 10 US ISP IPs, shared until yts's own are bought (D-042), on 9 subnets |
| Pace | 2 s between requests on one IP, 10 jobs at once, a 7-day backfill |
| Channels | The owner's 329, imported at 18:52 on 7 November; 374 an hour later; 2,646 measured over the window (discovery) |
| Relay | Off for the first 5 hours (no Kafka), on from 23:17 on 7 November; it sent the backlog and kept up |

## What it collected

| Measure | Result |
|---|---|
| New videos | 57,747 |
| New comments | 395,552 |
| Comment completeness | median 100.0%, mean 100.0%, over 10,839 videos |
| Failed jobs | 0.0%; no dead jobs |
| Blocks | 0.1 per 1,000 requests: 47 blocked requests, all on the player; no page blocks, no pool pauses, pool speed 1 throughout |
| Sent to the platform | About 1.59 million yt events into ClickHouse by 10 November (the master), with no dead letters |

**How soon a new video was seen** (from its publish time to yts's first sight of it): median 146 minutes, 90th percentile
591 minutes.

| Found by | Videos | Median delay |
|---|---|---|
| The channel's feed (RSS) | 3,157 | 134 min |
| The channel's video tab | 352 | 283 min |
| A web page | 98 | 170 min |
| Web search (discovery) | 164 | 85 min |

Most new videos come in through the feed. Push (WebSub) is off in production; turned on, it tells yts within minutes for the
channels that send it. Turning it on is the owner's call.

## What it costs

From the report's projections. "Measured" is the window itself, at 2,646 channels; the other rows scale the per-channel work.
Discovery is the same 6,677 requests a day in each row, because it doesn't depend on the channel count.

| Channels | Requests a day | Proxy traffic a day | a month | Busiest hour (requests) | IPs (report) | IPs (corrected, at least) | Database a year (see below) |
|---|---|---|---|---|---|---|---|
| 2,646 (measured) | 191,425 | 6.52 GB | 195.7 GB | 13,210 | 14 | 19 | 643.9 GB |
| 1,000 | 76,499 | 2.71 GB | 81.2 GB | 5,279 | 6 | 8 | 243.4 GB |
| 10,000 | 704,895 | 23.57 GB | 707.0 GB | 48,644 | 51 | 69 | 2,433.6 GB |

All of these are floors: the work that never got a turn isn't in them.

- **Requests:** at 2,646 channels, 184,749 a day are channel work (videos, comments, posts, playlists) and 6,677 discovery.
  Commenter enrichment, images and the backfill show 0: none of their jobs finished in the window, because higher-priority
  work filled the IPs. So a new channel's start-up cost (its backfill) isn't measured yet.
- **IPs, corrected:** an IP manages about 1,320 requests an hour at the 2 s pace (the busiest hour, measured). At 70% busy
  that is about 925 an hour; with 30% spare, the busiest hour's requests divided by 925, times 1.3. The 10 IPs being bought
  cover at most about 1,400 channels this way, and fewer once the work that never got a turn is counted.
- **Proxy traffic:** 6.5 GB a day, about 196 GB a month at this size. Check it against the provider's plan if it limits traffic.
- **Database:** 0.01 GB at the start and 3.50 GB at the end, about 1.76 GB a day. The yearly figures overstate it:
  - **the outbox is 2.36 GB of the 3.5 GB.** It holds each event until it has been sent and then 7 more days (the owner's
    rule), so it levels off at about 8 GB rather than growing;
  - **the rest**, the collected data itself (comments 283 MB, videos, posts, authors, the link graph and the rest), grew by
    about 0.57 GB a day. That is about 210 GB a year at 2,646 channels, and less in steady state, because the first two days
    include the backfill and every channel's first snapshot.

## What went wrong

**The quarantine rule stopped collection.** The pool sets an IP aside for good once it has had a block on 3 different UTC days
within 7 days, and a single block counts, of any kind. Each IP had one player block on 7, 8 and 9 November, so all 10 were set
aside during 9 November. The last one went at 16:30Z, after the window, and from then on every job failed with "no proxy
available". The plan meant "blocked on several days in a row"; the code was far stricter.

- **Effect on these numbers:** small. IPs went one by one on 9 November UTC, so the last hours of the window ran on fewer IPs;
  the second day's busiest hour and request counts may be a little low.
- **Effect after the window:** nothing was collected from 16:30Z on 9 November until the master restarted the worker on
  10 November. No job was lost and none died: the jobs only moved back by their retry delay (at most 6 hours).
- **Fix:** proposed to the owner ([`../status.md`](../status.md), "Proposed to the owner"). Until it is in, a worker restart
  clears the quarantine, and it would happen again about three days later.
- **Monitoring:** the alerts `ProxyQuarantined` and `ProxiesHealthyLow` should have reached the owner's Telegram on 8–9
  November. Whether they did, and whether the worker was still scraped while not Ready, is being checked by the master.

## Still to check

- **How far behind the worker was:** the capacity planner's own records for the window (`capacity_reports`: supply, demand,
  stretch, debt and recommended IPs, every few minutes) and the jobs waiting by kind at its end. These show how much work never
  got a turn. The meter is right: every finished job is counted, so a 0 means none finished.
- **The planner is a little optimistic too:** it assumes an IP does 1,440 requests an hour (80% of 1,800); the pilot measured
  about 1,320. I'll correct it with the report's formula.

## Decisions for the owner

1. **The quarantine fix:** count a day only after 3 page blocks, 3 days in a row; keep quarantine across restarts with a command
   to lift it; alert from the database; and the worker waits instead of failing jobs while no IP is usable.
2. **IPs:** at least 19 for today's 2,646 channels at the 2 s pace, 8 for 1,000 and 69 for 10,000; the 10 being bought were
   full in the pilot. Discovery added about 1,100 channels a day, with no cap, so the need keeps growing unless the list is
   capped or the pace changes.
3. **Push (WebSub):** turn it on to see new videos sooner (median 134 minutes through the feed today).
4. **The outbox's 7 days:** about 8 GB in the shared database at this size; fewer days means less.
5. Still open from before: the media hand-off, avatars and banners, the pace (2 s held up well), and the 5 unresolved inputs.
