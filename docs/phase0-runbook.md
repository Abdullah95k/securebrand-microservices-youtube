# Phase 0 runbook

This is how to run the Phase 0 measurements (plan §9) on your DigitalOcean droplet with `ytprobe`. All times are **Iraq time (UTC+3)**.

## What ytprobe does, and what it never does

- It reads public YouTube pages **logged out**, through your proxies, at a slow pace: by default one request every 3 seconds per proxy IP.
- **When an IP gets a block signal**, that IP rests (30 min, then 2 h, 12 h, 24 h) and the work moves to another IP (D7). If the same request is blocked on 3 different IPs, ytprobe gives up on it for 6 hours.
- It **never** logs in, never solves CAPTCHAs, and never generates YouTube's anti-bot tokens.
- **Phase 0 keeps counts, not people.** It records numbers, sizes, timings and yes/no flags. For the 7-day re-check it keeps comment IDs and a fingerprint (hash) of each comment's text, **never the text or the author**.
- Everything stays in `/var/lib/securebrand-youtube/phase0` on the droplet and is deleted automatically after 14 days.
- **The report contains aggregates only**: no channel IDs, no names, no comment text. It's safe to paste into our chat.

## What this version covers

| Experiment | Command | When |
|---|---|---|
| Setup check | `check` | Day 0 |
| New-upload detection (E8) | `detect` (runs in the background for 14 days) | Day 0 → Day 14 |
| Channel cost, fields, comments, parser check (E3, E5, E6, E13) | `crawl` | Day 0 |
| Accuracy against the official API (E4) | `accuracy` | After the crawl |
| Edits and deletions (E7) | `resweep` | Day 7 |
| Report | `report` | Any time; final on Day 14 |

The remaining experiments come in the next update. You can start the long-running parts now:

- the safe-pace and failover curve (E1, E2);
- keyword search and the channel census (E9, E10, E11);
- the transcript check (E12).

## Before you start

You already have these from plan §9.3:

- `/etc/securebrand-youtube/youtube_api_key` (mode 600);
- `/etc/securebrand-youtube/proxies.txt` (mode 600).

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

8. Start the crawl in the background. It reads each channel's last 365 days of uploads and checks 8 videos per channel in depth, including their comments (up to 2,000 per video). Expect a few hours for 20 channels:

```bash
docker run -d --name ytprobe-crawl -v /etc/securebrand-youtube:/etc/securebrand-youtube:ro -v /var/lib/securebrand-youtube:/var/lib/securebrand-youtube ytprobe crawl
```

9. Watch the crawl's progress. Ctrl+C stops *watching*, not the crawl:

```bash
docker logs -f ytprobe-crawl
```

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

## Day 7

14. Re-read the comments of the crawled videos to measure edits and deletions. If the crawl ran on 2 November 2026, run this on 9 November 2026:

```bash
docker run --rm -v /etc/securebrand-youtube:/etc/securebrand-youtube:ro -v /var/lib/securebrand-youtube:/var/lib/securebrand-youtube ytprobe resweep
```

## Day 14

15. Print the final report and send it to me:

```bash
docker run --rm -v /var/lib/securebrand-youtube:/var/lib/securebrand-youtube ytprobe report
```

16. Stop detection:

```bash
docker stop ytprobe-detect
```

17. Remove the stopped container:

```bash
docker rm ytprobe-detect
```

Phase 0 data deletes itself after 14 days. To delete it all immediately:

```bash
sudo rm -rf /var/lib/securebrand-youtube/phase0
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
| `crawl` | `-comments` | `2000` | Maximum comments read per video |
| `crawl` | `-workers` | `4` | Channels processed at the same time |
| `crawl` | `-window-days` | `365` | Backfill window |
| `crawl` | `-no-player` | off | Skip the player endpoint (tags, category) |
| `detect` | `-rss-every` | `15m` | Feed check interval per channel |
| `resweep` | `-comments` | `2000` | Use the same value as the crawl |
| `accuracy` | `-max` | `200` | Videos to compare |
