# yts: why tiers A and B fall behind, and three fixes to approve

For the owner, from the yts session. Written 11 November 2026, from `ytcollect health` at 09:54 and 10:56 Iraq time
(counts only). Nothing here is built yet. All three fixes change the order in which yts collects, so they wait for your
answer.

## Update, 10:56 Iraq time: priority 2 gets almost no time

The second reading came an hour after the first:

- **Priority 2:** the oldest waiting job went from 5h51m to 6h54m, one for one with the clock. That holds for every kind at
  this level, searches included, and searches belong to no channel. So in that hour priorities 0 and 1 took almost all of
  the 10 IPs' time. Feed checks hardly ran, so new uploads are found hours late.
- **Priority 3:** the oldest waiting job went from 8h53m to 9h56m.
- **New comment scans (priority 1):** the oldest is now 3 hours late.

The reason is in the code, not only the IPs: see Fix 3 below. It is the main fix. Fixes 1 and 2 still help.

## What the numbers show

The collector has been back since 00:09 Iraq time today. Since then:

- **New videos are read at once.** When yts knows about a new video, it reads it within minutes (priority 0). New comment
  scans (priority 1) are almost all on time.
- **Finding new videos is late.** Feed checks are how yts notices a new upload, and they share priority 2 with ordinary
  refreshes: channel details, community posts, full comment sweeps. Priority 2 is running about **6 hours late in every
  tier**. Tier A's feeds are due every 30 minutes. At 09:54, 79 of the 81 tier A feeds were overdue, on average by 3 hours
  and at most by 6 hours. So a new tier A upload is found up to about 6 hours late, and only then read at once.
- **Older videos' comments are not checked at all.** Priority 3 covers the comment checks on videos older than two days and
  on older community posts (most of them found by the backfill), and the playlists. It runs only when nothing at
  priorities 0–2 is due, and that has not happened since the restart.

This is where tiers A and B stand:

| | Tier A (81 channels) | Tier B |
|---|---|---|
| Waiting jobs | 16,347 | 26,861 |
| Waiting time, in job-hours | 71,099 | 121,635 |
| Of that, priority 3 (older videos' and posts' comments, playlists, backfill) | 69,085 (97%) | 111,426 (92%) |
| Of that, priority 2 (feed checks and refreshes) | 2,014 | 9,935 |
| Of that, priorities 0 and 1 (new videos, new comments) | 0 | 274 |

The "falling behind" figure rises by about the number of waiting jobs every hour, because those jobs are not running. It
measures waiting, not lost data: nothing is dropped, everything waits its turn.

There are two reasons A and B get no head start:

1. **Inside a priority, the oldest job goes first, whatever the tier.** A tier A refresh waits exactly as long as a tier M
   one. The planner already slows only tiers C, D and M, but that does not move A and B up the line.
2. **One job per channel at a time.** The 10 busiest channels hold a quarter of tier A's waiting jobs. The busiest has 657, and
   they run one after another. This is not what holds A back today. It will matter once there is room to reach them.

The cause underneath is the one you already know: 10 IPs are about a third short of the work. These fixes do not add
capacity. They choose what goes first.

## Fix 1: find new uploads first (recommended)

Move the feed checks and the tab checks (Videos, Shorts, Live) from priority 2 to priority 0, next to reading new videos.

- **Effect:** new uploads in every tier are found on time again. For tier A that is every 30 minutes, for B every
  2 hours, as you set.
- **Cost:** at most about 500 requests an hour, about 5% of what the 10 IPs give. A feed check is one request. The time
  comes from the comment rechecks of tiers C, D and M, which the planner already slows.
- **Not changed:** how yts reaches YouTube (the same client, the same pace), the feed and tab intervals, and the data
  collected.
- **What changes in the database:** the priority on yts's own queue rows for these two kinds. No table, field or topic that
  others use changes.

## Fix 2: tiers A and B first within each priority (recommended)

Inside each priority, take tier A's jobs first, then B's, then everyone else's (C, D, M and searches) by due time, as now.
Priorities stay in their order: a new video of any tier still comes before a refresh of tier A.

- **Effect:** tier A and B refreshes and comment rechecks run before everyone else's. Their priority 2 backlog (about 2,400
  jobs, most of them full comment sweeps) goes first. Their older-video comment checks (priority 3, about 40,500 jobs)
  run as soon as priority 3 gets any time, ahead of C, D and M.
- **Cost:** no extra requests. Tiers C, D and M wait longer than today, until there are more IPs.
- **What changes in the database:** one new column on yts's own queue table, holding each row's tier order, so the queue
  can sort by it quickly. It is an addition. Nothing others use changes.

## Fix 3: a video's later readings are refreshes (recommended: the main fix)

The plan sets the priorities as: 0 for new uploads and new videos' first details, 1 for new comments, 2 for refreshes and
sweeps. In the code, though, a job keeps for good the priority it had when it was first queued.

So when a new video is read at priority 0, every later reading of its views and likes also runs at priority 0, for as
long as yts follows the video. For tier A that is every 30 minutes in its first two days, then every 6 hours, then weekly.
Its comment rechecks stay at priority 1 the same way. Every new video adds lasting work at the top of the line, and
everything below it gets less time each day: feed checks, channel refreshes, sweeps. That is what the 10:56 reading
shows.

The change: a job's priority follows what it does each time it runs.

- **A video:** its first reading stays at priority 0. Its later readings are refreshes, at priority 2. A live stream's
  viewer counts stay at priority 0 while it is live.
- **Comment checks:** priority 1 while the video or post is under two days old, then priority 2. A backfilled video's
  first comment check stays at priority 3, and its later checks are refreshes at priority 2.
- **The queue as it stands:** its rows are moved once, by the same rule.

What it means:

- **Size:** at 10:56 the queue held 41,175 video jobs at priority 0, of which only 77 were waiting. Almost all of them are
  later readings of videos already read once. It also held 12,745 comment checks and 4,201 video jobs at priority 1. Fix 3
  moves almost all of them to priority 2, where nothing was running at 10:56.
- **Effect:** priorities 0 and 1 hold only new work again, so new videos and new comments stay on time with room to spare.
  Refreshes of every kind share priority 2 by due time. With Fix 2, tier A's go first, then B's.
- **Cost:** no extra requests. Refreshes of older videos wait in line with the other refreshes instead of ahead of
  them. With the 10 IPs a third short, some refreshes still run late. With Fix 2, those are tiers C, D and M's.
- **Fix 1 is still needed:** without it, feed checks would wait behind all those refreshes at priority 2.
- **What changes in the database:** only the priority on yts's own queue rows. Nothing others use changes.

## Left for later

- **More than one job at a time for an A or B channel**, so a channel with hundreds of waiting jobs drains faster. Not
  needed until there is room to reach them. I will come back to it if the numbers show it.
- **Images (86,000 waiting, including the thumbnails, avatars and banners analysis is waiting for) and commenter profiles
  (339,000)** run only in spare time, and there is none. Only more IPs or fewer comment rechecks free it. That is the IP
  decision after tonight's 24-hour capacity plan (about 02:56 Iraq time on 12 November).

## A note on "failing" in the queue table

`ytcollect health` lists about 50,000 "failing" post-comment jobs, 2,600 playlist jobs and 316,000 image and profile
jobs. These are not failing now. Their last run was during the 28-hour stop, when there was no IP to use, and they have
not run since, so the old error is still shown. Jobs created since the restart show none. An error that comes from having no IP does not
count towards the limit that sets a job aside for good. Each job's error clears the next time it runs successfully.

## To answer

Fix 1: yes or no. Fix 2: yes or no. Fix 3: yes or no. I recommend all three. Each one is a small PR with tests, and
all three can go out in the same deploy.
