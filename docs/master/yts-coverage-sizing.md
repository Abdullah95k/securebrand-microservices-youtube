# yts: coverage sizing for D-054

For the master's production plan, from the yts session, written 12 November 2026. Counts only, no channel names.

**The owner's target (D-054):**
1. every Iraqi channel;
2. every channel, from anywhere, that Iraqi audiences follow, comment on or interact with.

**Data:**
- `ytcollect report` over 24 hours, 11 Nov 03:00 to 12 Nov 03:00 Iraq time;
- the capacity gauges and Prometheus at 03:11 Iraq time on 12 November;
- the pilot report and Phase 0 notes.

**Labels:** **(G)** marks a guess or an assumption. Everything else is measured. "Requests" are requests to YouTube through the proxy IPs.

## Summary

- **Today:** 2,898 channels are listed. The 10 IPs are full: 11,944 requests an hour on average, 97% of the most they can make in the busiest hour.
  - Tier A is on time. Tier B is a little behind.
  - Tiers C, D and M are refreshed 8 times less often than set (the maximum slowing), and are still behind.
  - Commenter profiles and images, the lowest priority, haven't run at all.
- **The scope (G):**
  - low about 30,000 channels: 20,000 Iraqi and 10,000 others with a real Iraqi audience;
  - high about 150,000: 100,000 Iraqi and 50,000 others.
  - The method is in §1. These are estimates to plan with, not counts. A week of discovery with spare IPs would firm them up.
- **Cost is driven by requests**, and requests by how many videos and comments a channel has. IP count, storage, events and analysis volume all scale with them.

**Scale by tier, at today's slowing** (C, D and M refreshed 8 times less often than set; A and B at their set intervals):

| | Channels | IPs | IPs with commenter profiles and images | IP cost a month at $3 each | Comments a day | PostgreSQL growth a month | Events a day to Kafka |
|---|---|---|---|---|---|---|---|
| Today's list | 2,898 | 17 | 29 | $51 / $87 | about 250,000 (198,446 measured) | about 22 GB | about 1.6 M (1.26 M measured) |
| Low (G) | 30,000 | 49–105 | 83–178 | $147–315 / $249–534 | 0.76–1.6 M | 66–142 GB | 4.8–10 M |
| High (G) | 150,000 | 189–404 | 321–686 | $567–1,212 / $963–2,058 | 2.9–6.3 M | 256–548 GB | 19–40 M |

At the owner's set intervals with no slowing, the IP counts are much higher: today 62, low 308–631, high 1,381–2,901. A range runs from the median channel (low end) to the mean channel (high end) of each tier; §3 explains. The price is the owner's O-5 figure of $3 per IP a month, unlimited data, still to be confirmed for yts's provider.

**What limits coverage first:**
1. IPs.
2. Above about 50 IPs, the single worker and its 4 database connections.
3. Then database size and write load, and the 30-day raw-response store.
4. Not, so far: blocks (0.3 per 1,000 requests), CAPTCHAs (none seen) or logins (none used, by rule).

**For today's list, I'd buy about 30 IPs ($90 a month at $3).** That runs everything at today's slowing. It includes commenter profiles and images, which are also the main route for finding the channels Iraqi audiences interact with. It also clears the backlog in a week or two. From there, IPs grow with the list (§4).

## 1. Size of the scope

**On the list now: 2,898 channels.**

| Kind | Tier | What it means | Channels |
|---|---|---|---|
| core | A | 1M subscribers or more | 81 |
| core | B | 100K or more | 188 |
| core | C | smaller, active | 659 |
| core | D | nothing uploaded in 180 days | 339 |
| mention | M | not Iraqi, but carries Iraq-relevant items | 1,631 |

- In all, 1,267 core channels (Iraqi) and 1,631 mention channels.
- The owner imported 329 on 7 November. Discovery found the rest, about 1,100 a day for the first two days.
- Since the 28-hour stop on 9–10 November, discovery has barely run, because the IPs are full. In the last day: 114 searches and 38 candidate checks, with 3,286 candidates waiting.
- So the list stopped growing because yts ran out of time, not because it ran out of channels.

**(1) Iraqi channels (G).**
- **Audience:** YouTube reaches about 22–25 million people in Iraq (DataReportal: 22.3 M in 2025, 24.7 M late 2025, advertising reach, not monthly users).
- **The big channels:** yts lists 81 Iraqi channels with 1M+ subscribers and 188 with 100K+. Big channels are the easiest to find. If yts has found 30–60% of them, there are about 140–270 and 300–600.
- **Smaller bands:** channel counts typically grow about tenfold per band. That gives roughly:
  - 3,000–6,000 with 10K+ subscribers;
  - 30,000–60,000 with 1K+;
  - 100,000 or more active channels below that.
- **The range:**
  - low about 20,000: active Iraqi channels with an audience of about 1K subscribers or more;
  - high about 100,000: all active Iraqi channels, small ones included.
  - The plan's §6 modelled 20,000, 100,000 and 300,000 lists. The Phase 0 census found 52% of discovered channels Iraqi and 48% mention-only, and 35% of the Iraqi ones state Iraq as their country.

**(2) Others with an Iraqi audience (G).**
- **What they are:** pan-Arab news, religious, sport, entertainment and gaming channels, and global ones where Iraqis comment.
- **What today's list shows:** keyword discovery alone already found 1.3 mention channels per Iraqi one.
- **Not yet measured:** the commenter route (§2), which finds where Iraqi commenters engage, hasn't run yet.
- **The range:** low about 10,000 and high about 50,000, counting only channels where Iraqi engagement is a real share. Below that the tail is unbounded: every big global channel has some Iraqi viewers.

**How to firm up the numbers:** give discovery spare IPs for one to two weeks and watch how quickly new qualifying channels level off. That was Phase 0's plan, dropped on 5 November (D30). yts's coverage events show the growth day by day.

## 2. How yts finds them

| Route | Status | What it finds | How fast |
|---|---|---|---|
| Website keyword search: 46 Iraq place names, seed and owner keywords, newest first, up to 5 pages | Built, starved now (P2) | Channels whose recent items match Iraq keywords | About 1,100 channels a day in the pilot's first two days, not yet levelling off. About 4 requests a search. |
| Official API search: region Iraq, Arabic, priority keywords | Built, starved now | The same, by YouTube's own ranking | At most about 95 searches a day (quota of 9,500 units). In Phase 0 it found 512 channels to the website's 838. |
| Search hit → candidate check: channel page, About and first Videos page | Built, starved now (P3) | Lists a channel as Iraqi if it states Iraq, if 30% of its items match, or if its own page matches. Otherwise as mention if any item matches. No size minimum. | About 3 requests a channel. 3,286 waiting. |
| Commenter route: every new commenter's channel, if it has videos, becomes a candidate | Built, never run (P4, spare time only) | Iraqi commenters' own channels, and where Iraqi audiences are | About 110,000 new commenters a day. About 385,000 waiting. Yield unknown: if 10–20% have channels with videos, and 20–40% of those qualify, that is 8,000–31,000 channels from the backlog alone (G). |
| Suggested videos: the side list of each video's watch page (the owner, 14 November) | Built 14 November | Channels YouTube links to what Iraqi channels post | No extra request: the page is already read at each video's first reading. A suggestion counts only if its title matches the Iraq keywords and its channel is new; then the usual candidate check (3 requests). At most 500 new candidates a day (`-suggested-daily`). |
| Channel links and featured channels on listed channels' pages | Not built (the table exists) | Networks of related Iraqi channels | Cheap: 1–2 requests per channel a month (G). Needs the owner's approval. |
| @mentions and collaborations in titles and descriptions | Not built | Collaborators | Free: the text is already collected. Needs approval. |
| YouTube's trending lists for Iraq | Not built | What Iraqis watch now, a direct signal for (2) | A few requests a day. Needs approval. |
| Links from Iraqi accounts on other platforms (Facebook, Instagram, TikTok bios) | Not built | Cross-platform creators | Depends on the other collectors' link data on the platform |
| Iraqi-dialect classifier on titles and comments | Not built | Better precision for (2): Iraqi audience versus Arabic in general | Analysis-side cost, no extra requests |
| Who Iraqi accounts follow | Not practical | | YouTube subscriptions are private by default; few are public |
| Location | Not available | | YouTube has no location search. The About panel's stated country is the only signal, already used. |

## 3. What one tracked channel costs

**Requests a day per channel, by tier.** The first two columns are measured over 24 hours. Because work waited, they are floors. The last two are what the channel needs, using the planner's demand: tiers A and B match the measurement, while C, D and M need about 2.4 times what they got.

| Tier | Measured: median | Measured: mean | Needs, at today's slowing (median–mean) | Needs, at set intervals (median–mean) | IP share at today's slowing |
|---|---|---|---|---|---|
| A | 809 | 1,613 | 830–1,660 | 830–1,660 | 0.04–0.07 IP |
| B | 138 | 450 | 140–460 | 140–460 | 0.006–0.02 |
| C | 32 | 52 | 76–123 | 610–990 | 0.003–0.006 |
| D | 3 | 10 | 7–24 | 57–190 | 0.0003–0.001 |
| M | 3 | 14 | 7–33 | 57–266 | 0.0003–0.0015 |

- One IP gives about 22,400 requests a day: 933 an hour at 70% busy, which is how the planner sizes. In practice the IPs run at about 97% with almost no blocks, so the 30% left over is the margin for retries and bad hours.
- **Where requests go:**
  - comment scans 58%;
  - video readings 29%;
  - full comment sweeps 7%;
  - feed checks 4%;
  - everything else 3%.
- **Commenter profiles and images (P4)** add about 2 requests per new commenter and about 1 per new video. Today that is about 254,000 requests a day, which scale with comments.

**CPU and memory, measured** (1-hour averages, at about 12,000 requests an hour):

| Pod | CPU | Memory |
|---|---|---|
| Worker | 0.086 cores | 79 MiB |
| Scheduler | 0.002 cores | 68 MiB |
| Relay | 0.004 cores | 18 MiB |

- CPU is about 0.007 cores per 1,000 requests an hour, and grows with requests.
- Memory grows with jobs at once, not channels: about 0.5–1 GB at 100+ IPs (G).
- PostgreSQL's CPU and IOPS aren't measured from yts; the platform has them.

**Storage per request, measured** (multiply by the requests a day above):

| Store | Per request | Per channel a month (tier A / B / C / D / M, at the mean, today's slowing) |
|---|---|---|
| PostgreSQL, lasting data (comments, versions, authors, videos, snapshots, queue) | 2.0 KB (566 MB a day today) | 100 / 28 / 7.5 / 1.5 / 2 MB |
| PostgreSQL, outbox: sent rows kept 7 days, so it levels off at 7 days' worth | 4.1 KB (1,185 MB a day today) | a standing 11 GB today, not growth |
| Events to Kafka (content.* and yt.*) | 4.4 messages | ClickHouse bytes per event: the platform's figure |
| Raw responses, 30-day store (`yts/30d/`, expired by the bucket rule) | 33 KB | a standing 0.37 TB today |
| Raw responses kept forever (failures and a 1% sample) | 0.33 KB | 4 GB a month today |
| Images: thumbnails, avatars, banners | 15–40 KB each (G), deleted within 72 hours of the hand-off to analysis | 1–4 GB standing today once images run (G) |

**Content volume, measured over 24 hours:**
- **Comments:** 198,419 content.comment.v1 messages, against 198,446 new comments stored. Edits and deletions are few.
- **Posts:** 57,904 content.post.v1 messages: videos and community posts, new and updated.
- **New videos:** 21,014 to 33,637, depending on the count. Both include one-off finds from search hits and newly listed channels, so steady new uploads are lower: the Phase 0 census found a median of 3 uploads a month per channel (G for the mean).
- **Community posts:** 2,103 yt.post.v1 messages.
- **Account events:** about 110,000, mostly new commenters.

## 4. Three tiers

The channel mix is an assumption: there are more large channels than yts has found, most new channels are small, and about half of small channels are dormant (plan §6) **(G)**.

| | Today | Low (G) | High (G) |
|---|---|---|---|
| Channels | 2,898 | 30,000 | 150,000 |
| A / B / C / D / M | 81 / 188 / 659 / 339 / 1,631 | 200 / 700 / 9,000 / 10,100 / 10,000 | 300 / 1,200 / 40,000 / 58,500 / 50,000 |
| Requests a day, at today's slowing | 365,000 | 1.1–2.3 M | 4.2–9.0 M |
| IPs, at today's slowing | 17 | 49–105 | 189–404 |
| Plus commenter profiles and images | +12 | +34–73 | +132–282 |
| IPs, at the set intervals, no slowing (without profiles and images) | 62 | 308–631 | 1,381–2,901 |
| IP cost a month at $3 (today's slowing, with profiles and images) | $87 | $249–534 | $963–2,058 |
| Pods | worker, scheduler, relay; about 0.1 core and 170 MB in use | the same three; worker 0.3–0.7 core (G) | 2–4 workers, each with its own share of the IPs (needs a change, §5); relay 0.5–1 core (G) |
| Nodes | a fraction of one | a share of one 2 vCPU / 4 GB node (G) | 1–2 nodes of 4 vCPU / 8 GB (G) |
| PostgreSQL growth a month | 22 GB | 66–142 GB | 256–548 GB |
| PostgreSQL outbox, standing | 11 GB | 32–68 GB | 122–262 GB |
| Raw 30-day store, standing | 0.37 TB | 1.1–2.3 TB | 4.2–9.1 TB |
| Raw kept forever, a month | 4 GB | 11–23 GB | 42–91 GB |
| Events a day to Kafka | 1.6 M | 4.8–10 M | 19–40 M |
| Comments a day | about 250,000 | 0.76–1.6 M | 2.9–6.3 M |
| Comments a day at D-053's 10% sample | about 25,000 | 76,000–160,000 | 290,000–630,000 |
| Post messages a day (videos and community posts, new and updated) (G) | about 74,000 (57,904 measured) | 220,000–470,000 | 0.85–1.8 M |

- **Requests:** a range runs from the median channel (low end) to the mean channel (high end).
- **Comments:** they depend on how much people comment, not on how often yts checks. So the comment counts are the same at any slowing; slowing only delays them.
- **Commenter profiles and images:** their extra IPs are a first-months figure. As the same commenters keep coming back, fewer are new.
- **A one-off backfill on top:** each new channel's 7-day backfill is about 5 requests. Older videos' and posts' comments waiting on today's list come to about 200,000 requests once.

## 5. What limits coverage first, and what lifts it

| Limit | Where it bites | What lifts it |
|---|---|---|
| **IPs** | Now: the IPs are full at 97%. Discovery, older comments, profiles and images get no time. | More IPs; cost scales linearly. Slowing C, D and M further, or reading small channels' older videos less often, are policy choices for the owner. |
| **One worker, 4 database connections** | The chart allows one worker. Its pace per IP is kept inside that process, and it has 4 connections through PgBouncer. Fine to about 50 IPs (G). | Several workers, each with its own share of the IPs, and more connections. A code and chart change, to propose before the low tier. |
| **Database** | Growth of 66–548 GB a month at the low and high tiers, and its write load. Everything is kept forever. | A larger managed plan (the platform's call). Fewer outbox days would trim the standing outbox; the owner set 7 days. |
| **Raw 30-day store** | 1–9 TB standing at the low and high tiers | Store a sample instead of everything (the owner's 6 November decision 3) |
| **Proxy quality at scale** | Today's ISP IPs are clean: 0.3 blocks per 1,000 requests, nearly all on the player, none on pages. A pool ten times larger from one provider is untried. Datacentre and residential IPs are untested at scale. | Several providers and many subnets. The pool already rests whole subnets after blocks. |
| **Pace per IP** | 2 s per request per IP. In Phase 0, pages were clean down to 1 s, but the player was blocked even at 1 per 10 s. | A shorter gap would double each IP's output, but it changes how yts reaches YouTube. Only with the owner's approval, and only after a bigger pool has run cleanly at 2 s. |
| **API quota** | About 95 official searches a day | Website search does the bulk and has no quota. |
| **Feed outages** | In Phase 0, 42% of feed checks answered "not found" during global outages of about 9 hours. The channel tabs stand in, at more requests. | Push (WebSub) for channels that send it: the owner's call. It saves little (feed checks are 4% of requests). |
| **Logins, CAPTCHAs** | Not a limit. yts never logs in and never solves CAPTCHAs (owner rules). Members-only videos and age-restricted player data are out of scope, a small share. | None needed |

## Sources

- yts measurements: `ytcollect report` over 24 hours (11–12 Nov), the capacity and Prometheus readings at 03:11 Iraq time on 12 Nov (the master's ops runs), `docs/master/yts-pilot-report.md`, `docs/master/yts-gaps.md` (Phase 0), `docs/plan.md` §6.
- Audience: [DataReportal, Digital 2026: Iraq](https://datareportal.com/reports/digital-2026-iraq), and [Shafaq News on DataReportal's 2025 figure](https://shafaq.com/en/Iraq/Top-3-in-Arab-world-Iraq-s-YouTube-user-count-revealed).
