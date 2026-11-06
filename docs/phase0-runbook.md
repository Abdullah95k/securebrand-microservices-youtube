# Phase 0 runbook

This is how to run the Phase 0 measurements (plan §9) on your DigitalOcean droplet with `ytprobe`. All times are **Iraq time (UTC+3)**.

## What ytprobe does, and what it never does

- It reads public YouTube pages **logged out**, through your proxies, at a slow pace: by default one request every 3 seconds per proxy IP.
- **When an IP gets a block signal**, that IP rests (30 min, then 2 h, 12 h, 24 h) and the work moves to another IP (D7). If the same request is blocked on 3 different IPs, ytprobe gives up on it for 6 hours.
- It **never** logs in, never solves CAPTCHAs, and never generates YouTube's anti-bot tokens.
- **Phase 0 keeps counts, not people.** It records numbers, sizes, timings and yes/no flags. For the 7-day re-check it keeps comment IDs and a fingerprint (hash) of each comment's text, **never the text or the author**.
- Everything stays in `/var/lib/securebrand-youtube/phase0` on the droplet and is **kept** (D26). Only data that came from the official API (the accuracy check's official numbers and the API's search results) is removed after 30 days, because YouTube's API rules allow no longer; the same videos seen on the website are kept.
- **The report contains aggregates only**: no channel IDs, no names, no comment text. It's safe to paste into our chat.

## What this version covers

| Experiment | Command | When |
|---|---|---|
| Setup check | `check` | Day 0 |
| Channel list check | `resolve` | Whenever the list changes |
| New-upload detection (E8) | `detect` (runs in the background for 14 days) | Day 0 → Day 14 |
| Channel cost, fields, comments, parser check (E3, E5, E6, E13) | `crawl` | Day 0 |
| Accuracy against the official API (E4) | `accuracy` | After the crawl |
| Edits and deletions (E7) | `resweep` | Day 7 |
| Safe pace per endpoint and failover (E1, E2) | `pace` (about an hour; when no crawl is running) | After the crawl |
| Discovery: website search vs official API (E9) | `discover` (repeat every few days) | After the crawl |
| Channel census (E10) | `census` | After the first `discover` |
| Keyword-matching quality (E11) | `label -ai` (the AI labels 300 titles, about 10 minutes), then optionally `label -limit 30` (you check 30 of them, about 5 minutes) | After `discover` |
| Transcripts without anti-bot tokens (E12) | `transcripts` (a few minutes) | Any time |
| Report | `report` | Any time; final on Day 14 |

All 13 experiments are in this version. The order is: Day 0 (setup, detection, crawl), then "After the crawl" (accuracy and the remaining experiments), then Day 7 and Day 14.

## Before you start

You already have these from plan §9.3:

- `/etc/securebrand-youtube/youtube_api_key` (mode 600);
- `/etc/securebrand-youtube/proxies.txt` (mode 600).

Step 18 (E11) also needs an Anthropic API key in `/etc/securebrand-youtube/anthropic_api_key` (mode 600); step 18 shows how to create it.

Add a type to each proxy line so the report can compare proxy kinds: `type=dc` for datacenter, `type=isp`, or `type=res` for residential. The format is one proxy per line, for example:

```
http://user:password@203.0.113.10:8080 type=dc
http://user:password@198.51.100.20:8000 type=isp
```

The two lines above are placeholders. Use your real proxies. Optionally add `subnet=<label>` when several proxies share one provider gateway hostname. Without it, each gateway port counts as its own subnet.

If your account isn't in the `docker` group, put `sudo` in front of the `docker` commands below.

## Day 0

1. Get the code:

```bash
git clone https://github.com/Abdullah95k/securebrand-microservices-youtube.git
```

2. Go into the folder:

```bash
cd securebrand-microservices-youtube
```

3. Build the image. This takes a minute or two:

```bash
docker build -t ytprobe .
```

> **Images built after 6 November 2026 run as user 65532, not root** (REQ-010 fix 13). The Phase 0 files on the shared server
> belong to root, so such an image can't read them. Phase 0 has ended and the image already on the server still runs as root, so
> nothing changes there. If you ever rebuild it for a Phase 0 command, add `--user 0:0` right after `docker run`.

4. Create the channel list:

```bash
sudo nano /etc/securebrand-youtube/phase0-channels.txt
```

   Put one channel per line. Any of these forms works: `@handle`, `https://www.youtube.com/@handle`, `https://www.youtube.com/channel/UC…`, or the channel ID `UC…`. Lines starting with `#` are ignored. Use the 10–20 example channels from plan §9.1: different sizes, some Shorts-heavy, one that streams live, Arabic and Kurdish.

5. Create the data folder:

```bash
sudo install -d -m 700 /var/lib/securebrand-youtube/phase0
```

6. Run the setup check. Every proxy fetches YouTube's homepage once, and the API key answers one 1-unit call:

```bash
docker run --rm -v /etc/securebrand-youtube:/etc/securebrand-youtube:ro -v /var/lib/securebrand-youtube:/var/lib/securebrand-youtube ytprobe check
```

   You want every proxy to say `OK` and the last line to say `Result: ready for Phase 0`. If a line says `FAILED`:

   | Message contains | Meaning | What to do |
   |---|---|---|
   | `consent page` | The proxy is in the EU, and YouTube shows its cookie-consent page | Replace it with a proxy outside the EU |
   | `block signal` | That IP is already blocked by YouTube | Replace it, or try it again tomorrow |
   | `proxy` / `connection refused` / `407` | The proxy itself failed, or the user/password is wrong | Check that line in `proxies.txt` |
   | `mode 0644` or similar | A secret file is readable by others | Run `sudo chmod 600` on that file |
   | `API key: FAILED` | The key is wrong, or not enabled for YouTube Data API v3 | Re-check plan §9.3 steps 2–7 |

7. Start new-upload detection in the background. It checks each channel's feed every 15 minutes and its tabs once a day, and restarts automatically if the droplet reboots:

```bash
docker run -d --name ytprobe-detect --restart unless-stopped -v /etc/securebrand-youtube:/etc/securebrand-youtube:ro -v /var/lib/securebrand-youtube:/var/lib/securebrand-youtube ytprobe detect
```

8. Start the crawl in the background. It reads each channel's last 365 days of uploads and checks 8 videos per channel in depth, including all their comments and replies. Expect a few hours for 20 channels, longer when a video has tens of thousands of comments:

```bash
docker run -d --name ytprobe-crawl -v /etc/securebrand-youtube:/etc/securebrand-youtube:ro -v /var/lib/securebrand-youtube:/var/lib/securebrand-youtube ytprobe crawl
```

9. Watch the crawl's progress. Ctrl+C stops *watching*, not the crawl:

```bash
docker logs -f ytprobe-crawl
```

   Each `channel crawled` line counts the uploads found in the window. It also shows:

   - `pages`: tab pages read;
   - `capped`: whether a tab hit the page limit (`-max-pages`, 400);
   - `undated`: videos whose date couldn't be read (should be 0);
   - `date_probes`: extra requests used to date Shorts, which show no date on the Shorts tab.

   If a channel can't be found, the `channel not resolved` line says what YouTube answered at each step. Listing that channel by its channel ID (`UC…`) avoids the lookup.

10. Check whether the crawl has finished. `Exited (0)` means done:

```bash
docker ps -a --filter name=ytprobe-crawl
```

Detection and the crawl share your proxies. Detection's load is tiny (a few feed requests a minute for 20 channels), so running both at once is fine.

If the logs show many block signals, run the crawl again with a slower pace, for example one request every 6 seconds per IP. Add `-interval 6s` after `crawl` in step 8, after removing the old container with `docker rm ytprobe-crawl`.

## After the crawl (Day 0 or Day 1)

11. Compare scraped numbers with the official API for up to 200 of the crawled videos. This costs about 4 quota units (1 per 50 videos):

```bash
docker run --rm -v /etc/securebrand-youtube:/etc/securebrand-youtube:ro -v /var/lib/securebrand-youtube:/var/lib/securebrand-youtube ytprobe accuracy
```

12. Print a first report and send it to me in our chat:

```bash
docker run --rm -v /var/lib/securebrand-youtube:/var/lib/securebrand-youtube ytprobe report
```

13. See whether any responses couldn't be parsed:

```bash
sudo ls /var/lib/securebrand-youtube/phase0/skeletons
```

   If the folder has files, show me one. Skeletons contain only the *structure* of a response: every value is replaced by a placeholder like `<string>`. Look through it before sharing anyway. This command prints the first file:

```bash
sudo sh -c 'cat "$(ls -1 /var/lib/securebrand-youtube/phase0/skeletons/* | head -n 1)"'
```

## The remaining experiments (after the crawl has finished)

Run these one at a time, while detection keeps running. Every command can be stopped with Ctrl+C and run again.

14. Optional: put your own search terms (brands, people, topics) in a keywords file, one per line. They stay on the server, are stored only as hashes, and never appear in the report:

```bash
sudo nano /etc/securebrand-youtube/phase0-keywords.txt
```

15. Safe pace and failover (E1, E2), about an hour. On 3 proxies it steps the pace up per endpoint and stops each endpoint at its first block signal; nothing goes faster after a block:

```bash
docker run --rm -v /etc/securebrand-youtube:/etc/securebrand-youtube:ro -v /var/lib/securebrand-youtube:/var/lib/securebrand-youtube ytprobe pace
```

16. Discovery (E9), about 30 minutes. Your keywords plus 20 seed keywords, each on the website's search (10 pages) and the official API (one page, at most 2,000 of the 10,000 daily units). Repeat it every 2–3 days until Day 14; the report then shows how many new channels each run finds:

```bash
docker run --rm -v /etc/securebrand-youtube:/etc/securebrand-youtube:ro -v /var/lib/securebrand-youtube:/var/lib/securebrand-youtube ytprobe discover
```

17. Channel census (E10), about 30 minutes: profiles up to 500 channels discovery found that are not in your list. Running it again after a later `discover` profiles only the new ones:

```bash
docker run --rm -v /etc/securebrand-youtube:/etc/securebrand-youtube:ro -v /var/lib/securebrand-youtube:/var/lib/securebrand-youtube ytprobe census
```

18. Keyword-matching quality (E11), about 10 minutes. The AI labels the 300-title sample: is each video about Iraq or relevant to Iraqi audiences? Only the public video titles are sent to Anthropic's API, 25 per request: no video or channel IDs, no comments, no names, and not what the keyword matcher decided (the AI labels blind).

a. Once: put your Anthropic API key in a file. Create the key in the Claude Console (https://platform.claude.com, "API keys"). The editor keeps the key out of your shell history: paste the key, then save with Ctrl+O, Enter, and leave with Ctrl+X:

```bash
sudo nano /etc/securebrand-youtube/anthropic_api_key
```

```bash
sudo chmod 600 /etc/securebrand-youtube/anthropic_api_key
```

b. Run the labelling. Replace `MODEL_ID` with the Claude model ID to use (I give you the current one in chat; the list is at https://platform.claude.com/docs/en/about-claude/models/overview). If it stops (Ctrl+C, a network error, a rate limit), run the same command again: it continues where it stopped. The last log line shows the requests and tokens used:

```bash
docker run --rm -v /etc/securebrand-youtube:/etc/securebrand-youtube:ro -v /var/lib/securebrand-youtube:/var/lib/securebrand-youtube ytprobe label -ai -model MODEL_ID
```

c. Optional, recommended, about 5 minutes: check the AI on 30 titles yourself. You answer y (relevant), n (not relevant), s (can't tell) or q (stop) without seeing the AI's answers; at the end it lists the titles where you disagreed, with the AI's reason. The report then shows how often you and the AI agree, and your answer counts where you gave one. Note the `-it`: it needs your keyboard:

```bash
docker run -it --rm -v /etc/securebrand-youtube:/etc/securebrand-youtube:ro -v /var/lib/securebrand-youtube:/var/lib/securebrand-youtube ytprobe label -limit 30
```

Labelling all 300 titles by hand still works: run `label` with `-it` and without `-ai` or `-limit`.

19. Transcripts (E12), a few minutes: checks whether transcripts come back without anti-bot tokens. The report gives the go or no-go:

```bash
docker run --rm -v /etc/securebrand-youtube:/etc/securebrand-youtube:ro -v /var/lib/securebrand-youtube:/var/lib/securebrand-youtube ytprobe transcripts
```

20. Print the report and send it to me (the commands above each add a section):

```bash
docker run --rm -v /var/lib/securebrand-youtube:/var/lib/securebrand-youtube ytprobe report
```

> **Phase 0 ended early, with the 5 November 2026 discovery and census (D30).** The Day 7 resweep and the later discovery and
> census runs were dropped: production measures the same trends. Steps 22–24 (the final report and stopping detection) are
> done after that census.

## Day 7

21. Re-read the comments of the crawled videos to measure edits and deletions. If the crawl ran on 2 November 2026, run this on 9 November 2026:

```bash
docker run --rm -v /etc/securebrand-youtube:/etc/securebrand-youtube:ro -v /var/lib/securebrand-youtube:/var/lib/securebrand-youtube ytprobe resweep
```

## Day 14

22. Print the final report and send it to me:

```bash
docker run --rm -v /var/lib/securebrand-youtube:/var/lib/securebrand-youtube ytprobe report
```

23. Stop detection:

```bash
docker stop ytprobe-detect
```

24. Remove the stopped container:

```bash
docker rm ytprobe-detect
```

Phase 0 data is kept (D26). Before the droplet is shut down, copy the folder to the production server or to backup storage; I'll give you the commands then. Only official-API data is removed after 30 days, automatically when `accuracy`, `discover` or `label` runs, or by hand:

```bash
docker run --rm -v /var/lib/securebrand-youtube:/var/lib/securebrand-youtube ytprobe cleanup
```

## Checking the channel list

`resolve` looks up every channel in the list and prints its channel ID. When a channel can't be found, it says what YouTube answered. It sends one or two requests per channel and changes nothing:

```bash
docker run --rm -v /etc/securebrand-youtube:/etc/securebrand-youtube:ro -v /var/lib/securebrand-youtube:/var/lib/securebrand-youtube ytprobe resolve
```

To check only some channels, put them after `resolve`, for example `ytprobe resolve @TRTArabi https://www.youtube.com/@bbcnewsarabic`.

Some handles redirect to the channel's older address (for example `@TRTArabi` → `youtube.com/trtarabi`); ytprobe follows that one step. If a channel still isn't found, put its channel ID (`UC…`) in the list instead.

## Re-running the crawl

To start a crawl again, for example after an update, first remove the old container and the earlier crawl's results, so the two runs don't mix.

1. Stop the crawl if it is still running:

```bash
docker stop ytprobe-crawl
```

2. Remove its container:

```bash
docker rm ytprobe-crawl
```

3. Delete the earlier crawl's results (detection data is kept):

```bash
rm -rf /var/lib/securebrand-youtube/phase0/crawl_channels.jsonl /var/lib/securebrand-youtube/phase0/crawl_videos.jsonl /var/lib/securebrand-youtube/phase0/snapshots /var/lib/securebrand-youtube/phase0/skeletons
```

4. Run a quick test on the first 5 channels. It finishes on its own; expect up to an hour, depending on how much those channels upload and how many comments their videos have:

```bash
docker run --rm -v /etc/securebrand-youtube:/etc/securebrand-youtube:ro -v /var/lib/securebrand-youtube:/var/lib/securebrand-youtube ytprobe crawl -limit 5 -interval 4s -videos 4
```

5. Check the report before starting the full crawl again:

```bash
docker run --rm -v /var/lib/securebrand-youtube:/var/lib/securebrand-youtube ytprobe report
```

## Updating to a newer version

When I tell you a fix or the next experiments are merged:

1. Get the new code:

```bash
git pull
```

2. Rebuild the image:

```bash
docker build -t ytprobe .
```

3. Remove the running detection container:

```bash
docker rm -f ytprobe-detect
```

4. Start detection again with the same command as step 7. Its progress is kept in the data folder.

## Settings you can change

Add these after the command name, for example `ytprobe crawl -videos 12`:

| Command | Flag | Default | Meaning |
|---|---|---|---|
| any | `-interval` | `3s` | Minimum gap between requests on one proxy IP |
| `crawl` | `-videos` | `8` | Videos per channel checked in depth |
| `crawl` | `-comments` | `0` (all) | Maximum comments read per video; 0 reads every comment and reply |
| `crawl` | `-workers` | `4` | Channels processed at the same time |
| `crawl` | `-limit` | `0` (all) | Crawl only the first N channels of the list; use it for a quick test |
| `crawl` | `-window-days` | `365` | Backfill window |
| `crawl` | `-max-pages` | `400` | Safety limit of pages per channel tab (30 videos or 48 Shorts per page). The one-year window normally ends a tab first; `capped=true` in the log means it didn't |
| `crawl` | `-no-player` | off | Skip the player endpoint (tags, category) |
| `detect` | `-rss-every` | `15m` | Feed check interval per channel |
| `resweep` | `-comments` | `0` (all) | Use the same value as the crawl |
| `accuracy` | `-max` | `200` | Videos to compare |
| `pace` | `-proxies` / `-steps` / `-per-step` | `3` / `10s,6s,4s,3s,2s,1.5s,1s` / `20` | Proxies tested, gaps tried (slowest first), requests per proxy per step |
| `pace` | `-classes` | all | Endpoints to test: `browse,next,player,search,rss,watch,cdn` |
| `discover` | `-seed` / `-web-pages` | `20` / `10` | Seed keywords searched besides your file; website pages per term |
| `discover` | `-api-units` / `-api-pages` / `-api-days` | `2000` / `1` / `30` | API budget per run (0 = website only); API pages per term; only videos from the last N days |
| `census` | `-max` | `500` | Channels to profile |
| `label` | `-size` / `-reset` | `300` / off | Sample size; draw a new sample (drops earlier answers, yours and the AI's) |
| `label` | `-ai` / `-model` | off / none | Let the AI label the sample; the Claude model ID (required with `-ai`) |
| `label` | `-effort` / `-batch` / `-fallback` | `high` / `25` / on | With `-ai`: how hard the model thinks (`low`, `medium`, `high`, `xhigh`, `max`); titles per request; if the model declines a request, the API answers with a fallback model |
| `label` | `-limit` | `0` (all) | By hand: answer at most this many titles this time |
| `transcripts` | `-videos` | `30` | Videos to check |
