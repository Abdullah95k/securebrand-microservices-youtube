# Production runbook

How to set up and run the collector on a production server, and the everyday tasks: channels, keywords, erasure requests,
backups, the archive, webhooks and updates. Phase 0 has its own runbook ([phase0-runbook.md](phase0-runbook.md)). On the platform's
Kubernetes cluster, the Helm chart replaces Docker Compose: see [deploy/helm/yts/README.md](../deploy/helm/yts/README.md).

Times on the server are UTC. Iraq time is UTC+3.

## What runs

Everything is in one Docker Compose stack ([deploy/compose/docker-compose.yml](../deploy/compose/docker-compose.yml)):

| Service | What it does |
|---|---|
| `postgres` | The database |
| `migrate` | Creates or updates the tables, then stops. The other services wait for it |
| `scheduler` | Maintenance: partitions, missing jobs, the daily privacy audit, the archive, capacity, keyword suggestions, metrics, outgoing webhooks, the hourly copy of the suppression list |
| `worker` | The collector: reads YouTube through the proxies |
| `backup` | A database dump every 24 hours; dumps older than 30 days are deleted from the server |
| `prometheus`, `alertmanager` | Metrics and alerts. They listen on 127.0.0.1 only |
| `websub` | Push notifications. Off until there is a public subdomain (D23) |

Files on the server:

| Path | What |
|---|---|
| `/etc/securebrand-youtube/` | Secret files and settings, each `chmod 600`. Mounted read-only into the containers |
| `/var/lib/securebrand-youtube/backups/` | Database dumps and the suppression-list copy |
| `/var/lib/securebrand-youtube/debug/` | Scrubbed page skeletons from parse failures (no personal data) |
| `/var/lib/securebrand-youtube/objects/` | Images and archive files, if you use a local folder instead of object storage |

## 1. What you need first

- A server. The Compose file is sized for a small machine: 4 vCPU and 16 GB of memory. Step 5 says what to change for a bigger one.
  (Production itself runs on the platform's Kubernetes cluster; this Compose stack is for a single server.)
- Ubuntu 24.04 with root access over SSH.
- The proxy list (plan §7.3), in the same format as in Phase 0.
- Optional: the YouTube Data API key, object storage (bucket and access keys), and webhook receivers.
- Phase 0 results: the safe request gap per IP (E1) goes into the worker's `-interval` (step 5).

## 2. Install Docker and get the code

1. Log in to the server:

```bash
ssh root@SERVER_IP
```

2. Install Docker:

```bash
curl -fsSL https://get.docker.com | sh
```

3. Get the code:

```bash
git clone https://github.com/Abdullah95k/securebrand-microservices-youtube.git ~/securebrand-microservices-youtube
```

## 3. Create the secret files

Nothing secret goes into the repository, the image or a chat. Each file is readable only by its owner.

**On the platform's cluster** (REQ-010 fix 17) the database settings come instead from the platform's credentials, one file per
value (`POSTGRES_HOST`, `POSTGRES_PORT`, `POSTGRES_DATABASE`, `POSTGRES_SSLMODE`, then the user and password of each role). Point
`YT_POSTGRES_DIR` at the folder they are mounted in. Each command then uses the least role it needs:

| Role | Commands |
|---|---|
| Owner (`POSTGRES_USER`) | `migrate`, `scheduler`, `archive`: they change the tables (monthly partitions, the archive) |
| App (`POSTGRES_APP_USER`) | the worker, the push receiver, and the admin commands that change data |
| Read-only (`POSTGRES_RO_USER`) | `report`, `health`, `erasures`, `export` (the app role if there is no read-only one) |

`migrate` grants the app role row access (never the schema) and the read-only role `SELECT`, also for tables made later. The
connection must use TLS (`require`, `verify-ca` or `verify-full`). With the server's CA (`POSTGRES_CA_CERT`, the platform's key),
`require` becomes `verify-full`: the certificate is checked against the CA and the name against `POSTGRES_HOST`. A plain connection
to another machine is refused. The steps below are for one server with Docker Compose, which keeps
the single `database_url` file.

1. Create the folders:

```bash
install -d -m 700 /etc/securebrand-youtube /var/lib/securebrand-youtube /var/lib/securebrand-youtube/backups
```

2. Create the database password. The Postgres container reads it as user 999, so that user owns the file:

```bash
openssl rand -hex 24 > /etc/securebrand-youtube/postgres_password
```

```bash
chown 999:999 /etc/securebrand-youtube/postgres_password && chmod 600 /etc/securebrand-youtube/postgres_password
```

3. Create the database address the services use. It contains the password, so it's made on the server and never typed out:

```bash
printf 'host=postgres port=5432 user=ytcollect dbname=ytcollect sslmode=disable password=%s\n' "$(cat /etc/securebrand-youtube/postgres_password)" > /etc/securebrand-youtube/database_url
```

```bash
chmod 600 /etc/securebrand-youtube/database_url
```

4. Create the privacy key. It makes the keyed hashes of the suppression list:

```bash
(umask 077; openssl rand -hex 32 > /etc/securebrand-youtube/privacy_key)
```

> **Back up the privacy key now, somewhere off this server** (a password manager). Without it, the suppression list can't be
> checked, so erased people and channels could be collected again. Never paste it into a chat or ticket.

5. Add the proxies, one per line: `http://user:password@IP:PORT type=isp subnet=label country=XX`. `type` is `dc`, `isp` or `res`;
   `subnet` is optional (proxies with the same label rest together); `country` is the exit's two-letter ISO code (`IQ`, `DE`…), used
   for the platform's `exit_country`. A provider's plain `HOST:PORT:USER:PASS` line works too (read as http). An exit is its login
   and address, so two logins on one host and port are two exits, each its own subnet group unless labelled. Since 14 November an
   exit's ID includes its login, so the IDs of exits listed before changed once, and quarantines saved before then no longer apply:

```bash
nano /etc/securebrand-youtube/proxies.txt
```

```bash
chmod 600 /etc/securebrand-youtube/proxies.txt
```

6. Optional: add the YouTube Data API key (discovery uses it for priority keywords, within the daily quota):

```bash
nano /etc/securebrand-youtube/youtube_api_key
```

```bash
chmod 600 /etc/securebrand-youtube/youtube_api_key
```

## 4. Storage for images and the archive

Thumbnails, avatars, banners and the archive (history older than 12 months) need storage. Without a settings file, both are off
and everything else works.

**Option A: object storage** (recommended: Hetzner Object Storage or DigitalOcean Spaces). Create a private bucket and an access key
pair in the provider's console. Then:

1. Save the two keys, each in its own file:

```bash
nano /etc/securebrand-youtube/s3_access_key
```

```bash
nano /etc/securebrand-youtube/s3_secret_key
```

```bash
chmod 600 /etc/securebrand-youtube/s3_access_key /etc/securebrand-youtube/s3_secret_key
```

2. Create the settings file, with your endpoint, region and bucket (this example is Hetzner's Falkenstein region):

```bash
nano /etc/securebrand-youtube/storage.json
```

```json
{"type": "s3", "endpoint": "https://fsn1.your-objectstorage.com", "region": "fsn1", "bucket": "securebrand-youtube",
 "access_key_file": "/etc/securebrand-youtube/s3_access_key", "secret_key_file": "/etc/securebrand-youtube/s3_secret_key"}
```

For DigitalOcean Spaces the endpoint is like `https://fra1.digitaloceanspaces.com` and the region is `fra1`.

**Option B: a local folder** (fine to start with; the files grow with the data):

```bash
echo '{"type": "dir", "dir": "/var/lib/securebrand-youtube/objects"}' > /etc/securebrand-youtube/storage.json
```

**Raw responses** (REQ-010 fix 8, the owner's decision 3). With a second settings file, the worker keeps every page and API answer
it reads, gzipped, with the parser version. Images are left out: they are media.

- Under `yts/keep/`, kept forever: every failure (a block, an error page, an answer that isn't JSON, a page the parsers couldn't
  read) and a random 1% of the rest (`-raw-sample`).
- Under `yts/30d/`: everything else. Give the bucket a rule that deletes objects under `yts/30d/` after 30 days; yts itself never
  deletes raw files. `-raw-30d=false` writes only the `keep/` part.
- Each answer's reference (`raw_ref`, such as `s3://BUCKET/yts/keep/2026/11/06/browse/….json.gz`) goes into `parse_failures`, and
  later into each event's provenance.
- The traffic is about the same as the proxy traffic (`yt_raw_bytes_total` counts it).

1. Create a private bucket for raw responses, separate from the media bucket, and give it the 30-day rule above.
2. Create its settings file, in the same format as `storage.json` (your endpoint, region and bucket):

```bash
nano /etc/securebrand-youtube/raw_storage.json
```

```json
{"type": "s3", "endpoint": "https://fsn1.your-objectstorage.com", "region": "fsn1", "bucket": "securebrand-youtube-raw",
 "access_key_file": "/etc/securebrand-youtube/s3_access_key", "secret_key_file": "/etc/securebrand-youtube/s3_secret_key"}
```

3. Restart the worker. Its start-up log says `raw responses kept` with the bucket. Without the file it says
   `no raw bucket configured`, and nothing raw is kept.

```bash
docker compose restart worker
```

## 5. Start the stack

1. Check the worker's request gap. The Compose file starts at `-interval "2s"` under `worker`: Phase 0 (E1) found every page type
   clean down to 1 request per second per IP, except the player, which the collector asks only when it adds something. If the
   alerts show blocks, raise it (for example to `"3s"`).

   **Sizes.** Every container has a memory limit (`mem_limit`, with `memswap_limit` the same, so no swap), a CPU limit and a process
   limit. A runaway process is stopped alone and never takes the machine down. For 4 vCPU and 16 GB:

   | Container | Memory | CPU |
   |---|---|---|
   | postgres | 8 GB | 2 |
   | worker | 1 GB (`GOMEMLIMIT` 850 MiB) | 1.5 |
   | scheduler | 1 GB (`GOMEMLIMIT` 850 MiB) | 1 |
   | prometheus | 1 GB | 0.5 |
   | backup | 512 MB | 0.5 |
   | migrate, websub | 256 MB each | 0.5 |
   | alertmanager | 128 MB | 0.25 |

   That is about 12 GB in all, leaving 4 GB to the system. **On a bigger machine**, raise Postgres first:
   - `mem_limit` and `memswap_limit` to about half the machine's memory;
   - `shared_buffers` to a quarter of that limit;
   - `effective_cache_size` to three quarters of it.

   For example, on 64 GB: a 32 GB limit, `shared_buffers=8GB`, `effective_cache_size=24GB`, `maintenance_work_mem=2GB` and
   `max_wal_size=8GB`.

   If a worker is killed for memory (`docker compose ps` shows it restarting, and `docker inspect` shows `OOMKilled`), raise its
   limit and `GOMEMLIMIT` together, keeping `GOMEMLIMIT` about 15% below the limit:

```bash
nano ~/securebrand-microservices-youtube/deploy/compose/docker-compose.yml
```

2. Give the collector its files. Its containers run as user 65532, never as root (REQ-010 fix 13), so that user owns the secret
   files and the data folder. Postgres keeps its own password file (user 999). Run this again after adding a file to
   `/etc/securebrand-youtube`:

```bash
chown -R 65532:65532 /etc/securebrand-youtube /var/lib/securebrand-youtube && chown 999:999 /etc/securebrand-youtube/postgres_password
```

3. Go to the Compose folder:

```bash
cd ~/securebrand-microservices-youtube/deploy/compose
```

4. Build the image:

```bash
docker compose build
```

5. Start everything:

```bash
docker compose up -d
```

6. Check that the services are running. `migrate` shows `Exited (0)`, which is correct:

```bash
docker compose ps -a
```

7. Look at the latest log lines:

```bash
docker compose logs --tail 50 scheduler worker
```

8. Add a shortcut for admin commands. `ytc` runs one `ytcollect` command in a fresh container:

```bash
echo "alias ytc='docker compose -f $HOME/securebrand-microservices-youtube/deploy/compose/docker-compose.yml run --rm --no-deps scheduler'" >> ~/.bashrc
```

```bash
source ~/.bashrc
```

9. Check the queue:

```bash
ytc health
```

## 6. Keywords and channels

1. Load the built-in Iraq place names:

```bash
ytc keywords import-seed
```

2. Add your own keywords, with their other spellings (Arabic, Kurdish or English). `-ambiguous` means a second keyword must also
   match. They're stored only in the database, never in the repository:

```bash
ytc keywords add -kind brand -variants "مثال|example" "Example brand"
```

3. Put the channel list in a file, one `@handle`, channel URL or `UC…` ID per line:

```bash
nano /etc/securebrand-youtube/channels.txt
```

4. Import it. Core channels are collected in full; `-kind mention` is for channels that only sometimes mention Iraq:

```bash
ytc channels import /etc/securebrand-youtube/channels.txt
```

5. See the channels and their status:

```bash
ytc channels list
```

To move the Phase 0 channel list over, copy `/etc/securebrand-youtube/phase0-channels.txt` from the droplet and import it the same
way.

## 7. Watching it

Prometheus and Alertmanager listen on the server's 127.0.0.1 only. Reach them through an SSH tunnel.

1. On your own computer, open the tunnel and keep the window open:

```bash
ssh -N -L 9090:127.0.0.1:9090 -L 9093:127.0.0.1:9093 root@SERVER_IP
```

2. Open http://localhost:9090/alerts for the alert list and http://localhost:9093 for Alertmanager.

What to look at (plan §8.13):

- `yt_pool_speed` below 1: the pool brake is on (many IPs blocked).
- `yt_freshness_debt_hours`: work running late, per tier. If it grows, add proxy IPs (`yt_capacity_recommended_ips`).
- `yt_capacity_stretch`: how many times less often tiers C, D and M are refreshed (1 = as planned). See below.
- `yt_capacity_report_timestamp_seconds`: when the newest capacity plan was made; the `yt_capacity_*` values are from it.
  The alert CapacityPlanStale fires when it is over 2 hours old.
- `yt_fill_rate` against `yt_fill_rate_baseline`: a big drop means YouTube changed a page.
- `yt_erasure_oldest_pending_seconds` and `yt_suppression_violations`: must stay low and 0.

**Refreshing less often when the IPs are full.** Every hour the scheduler's capacity planner compares what the IPs can do with what
the schedule asks for:

- **What the IPs can do:** each IP makes a request every gap plus about 0.7 s for the answer: about 1,333 an hour at 2 s. That is
  counted at 70% busy, which leaves 30% for retries, rests and the lowest-priority work (backfill, commenter enrichment, images).
- **What the schedule asks for,** from what each kind of job really cost in the last day. It comes in two parts:
  - work that is never slowed: new uploads, the feeds and tab checks of every channel, tiers A and B, backfill and discovery;
  - refreshes that can be: tiers C, D and M's channel snapshots, posts, playlists, video stats and comment scans.

  Images and commenter enrichment, the lowest priority, are left out: they run in whatever time is left and never slow a
  refresh (the log's `spare_time_per_hour`). Their backlog is not freshness debt either: the log shows it as
  `spare_debt_hours`, apart from the tiers. Debt under "-" is work of no listed channel: discovery's searches and candidate
  checks, and videos of channels not on the list.

A worker that shuts down marks itself stopped, so the pod that replaces it isn't counted twice.

**The scheduler's tasks after a restart.** The scheduler runs its tasks one after another. Each task that runs an hour or more
apart (the capacity plan, the archive, the privacy audit, the commenter graph, the tiers and the rest) keeps its last run in
the settings table (`scheduler/last/<task>`). A restart, such as a deploy, therefore doesn't re-run the daily and weekly tasks,
and the hourly ones, such as the capacity plan, don't wait behind them: they run when they are due. When several are due at
once, the one with the shortest period goes first. The metrics, every minute, run at start as before. A task the restart cut
short is run again by the next scheduler. Before 14 November every restart re-ran them all, the hourly plan last, so two
deploys an hour apart could leave the plan hours old.

**The order of the queue** (the owner, D-052, 11 November). Workers take due jobs by priority. Within a priority, tier A's jobs
go first, then tier B's, then the rest (tiers C, D and M, and jobs of no listed channel). Within a tier, the oldest due job
goes first.

- **P0:** new uploads' first reading, and the feed and tab checks that find them. A live stream stays at P0 while it is live or
  about to start.
- **P1:** comment scans of videos and posts under two days old, and a new video's first thumbnail (D-061).
- **P2:** refreshes: later readings of a video, comment scans once it is two days old, channel snapshots, posts, sweeps and
  searches; and a listed channel's new or changed avatar or banner (D-061).
- **P3:** backfill, the first comment scan and the first thumbnail of a backfilled video or post, a thumbnail's recheck a
  week on, playlists and candidate checks.
- **P4:** commenter profiles and the other images (older ones queued before D-061, post images, commenters' avatars), in
  spare time only.

A job's priority follows what it does each time it runs: after its first reading, a video's next run is a P2 refresh. The
queue table in `ytcollect health` shows this as most video jobs at P2 and only new ones at P0. If P0 or P1 holds thousands
of due jobs, the IPs can't keep up even with new work: that is the time to add IPs.

When that is more than the IPs can do, the planner refreshes C, D and M less often (the "stretch", at most 8 times), by up to 1.5
times per hour. New uploads are found as soon as before: the feeds and tab checks are never slowed. The scheduler's log line
"capacity plan" shows both parts, the stretch and the IPs that would make it unnecessary (`recommended_ips`).

To slow refreshes down on purpose, set a floor (1 to 8) in the chart. The planner never goes below it:

```yaml
scheduler:
  minStretch: 2
```

When the stretch sits at 8 and `yt_freshness_debt_hours` still grows, the work that is never slowed fills the IPs alone: add IPs, or
find fewer channels.

**Quarantined IPs.** An IP blocked on page requests at least 3 times in a day (UTC), 3 days in a row, is quarantined: no job
uses it. Player blocks never count; the player is blocked often by design. A quarantine outlives a restart. See them, with
each IP's page and player blocks since the worker started:

```bash
ytcollect proxies
```

Release one once its provider has replaced it, or once you are sure the blocks have passed (the worker picks it up within a
minute):

```bash
ytcollect proxies release px-0a1b2c
```

Or every quarantined IP:

```bash
ytcollect proxies release -all
```

**Alert `AllProxiesQuarantined`**: every IP is quarantined, so nothing is collected. The worker takes no jobs meanwhile, so none
fail or are pushed back. The alert comes from the database, so it fires even when the worker isn't Ready. Replace the IPs, or
release them when the blocks have passed.

**Health endpoints.** The worker (`:9101`) and the scheduler (`:9102`) answer on their ops port. The push receiver answers on its own
port (`:8080`).

| Endpoint | Answers |
|---|---|
| `/healthz` | 200 while the process runs, also while it shuts down |
| `/readyz` | 200 when the database answers (and, for the worker, at least one proxy isn't quarantined). Otherwise 503, with the names of the failed checks. 503 `draining` once a shutdown has started |
| `/metrics` | Prometheus (worker and scheduler) |

**Dead jobs.** A job that fails ten times in a row for reasons of its own is marked dead and isn't retried. Examples: a page that
won't parse, or an error YouTube gives for that one item. Image downloads and commenter profiles die after five failures,
discovery checks after six. Blocks, a pool with no free IP, timeouts and a database that can't be reached never count, so an outage
can't kill jobs. The alert `DeadJobs` fires while any are dead.

1. See how many, by kind and reason (no IDs are shown):

```bash
docker compose exec worker ytcollect jobs dead
```

2. Once the cause is fixed (usually a parser update), bring them back. Replace `KIND` with the kind from step 1:

```bash
docker compose exec worker ytcollect jobs requeue -kind KIND
```

**Proxies.** `yt_proxy_bytes_total` counts the traffic each proxy IP carries. `yt_proxies_healthy` against `yt_proxies_total` shows
how many IPs can serve pages now. The alert `ProxiesHealthyLow` fires when fewer than 3 (or under half) are usable for 15 minutes.
Player rests don't count against an IP.

**What it costs.** Every job records what it used: requests, bytes on the wire and blocks, per hour and job kind, and per day and
channel. The scheduler also records the database size every hour. `ytcollect report` turns this into the cost per channel per day,
by tier, and in total. It projects the cost to 1,000 and 10,000 channels, with the proxy IPs needed. It shows counts only, never
names, IDs or text.

1. The last 48 hours, on the screen:

```bash
docker compose exec scheduler ytcollect report
```

2. Whole days, Iraq time, saved as a file in the data folder, where the server sees it at the same path. Change the dates to the
   ones you want:

```bash
docker compose exec scheduler ytcollect report -from 2026-11-10 -to 2026-11-12 -o /var/lib/securebrand-youtube/report.md
```

`-channels 1000,10000,50000` projects to other counts; `-json` gives the same report as JSON. Memory and CPU come from the
metrics (`process_resident_memory_bytes`, `process_cpu_seconds_total`), not from the report. The worker's `-backfill-days`
(default 365) sets how far back a new channel's uploads are read; a pilot can start with `-backfill-days 7`.

**Stopping.** On a stop, the worker takes no new jobs, gives running jobs 20 seconds (`-drain`) to finish, then interrupts the rest
and hands them back. They run again at once on another worker, and the attempt doesn't count. The Compose file gives the worker
30 seconds before Docker kills it. On Kubernetes, keep the pod's termination grace period above `-drain` plus about 10 seconds.

To send alerts to your system, add a receiver in [deploy/alertmanager/alertmanager.yml](../deploy/alertmanager/alertmanager.yml)
(it has an example), then restart Alertmanager:

```bash
docker compose restart alertmanager
```

## 8. Erasure requests

1. Record the request (an author, channel, video, comment or post, by ID, URL or `@handle`). A worker carries it out within a
   minute:

```bash
ytc erase -kind author -by "ticket 17" @someone
```

2. Check its status:

```bash
ytc erasures
```

What an erasure does:

- it goes on the suppression list first, so nothing about the target is collected again;
- it deletes the stored data, images and pending webhook events;
- it removes the data from the archive files within an hour;
- it tells webhook receivers that take `erasure` events.

The daily privacy audit erases again anything that comes back.

**Not covered:** raw responses in the raw bucket (section 4). A raw file is the page as YouTube sent it, so it can hold the erased
person's data. Erasure doesn't edit or delete raw files. Under the owner's decisions the privacy tools stay off; if erasure is ever
switched on, raw files need their own step first.

## 9. Backups

The `backup` service dumps the database every 24 hours (the first one when the stack starts) into
`/var/lib/securebrand-youtube/backups`. It keeps 30 days. The scheduler writes `suppression-list.jsonl` there every hour.

**Copy the folder off the server every day** (plan §7.1 item 8). For example, to a Hetzner Storage Box over SSH:

1. Make an SSH key for the copy, without a passphrase:

```bash
ssh-keygen -t ed25519 -N "" -f /root/.ssh/backup_box
```

2. Install it on the Storage Box. In this command and the next, replace `BOXUSER` with the box's user name (like `u123456`) and
   `BOXHOST` with its host name (like `u123456.your-storagebox.de`). It asks for the box password once:

```bash
ssh-copy-id -p 23 -s -i /root/.ssh/backup_box.pub BOXUSER@BOXHOST
```

3. Add a daily copy at 04:00 Iraq time (01:00 UTC):

```bash
(crontab -l 2>/dev/null; echo '0 1 * * * rsync -a --delete -e "ssh -p 23 -i /root/.ssh/backup_box" /var/lib/securebrand-youtube/backups/ BOXUSER@BOXHOST:ytcollect-backups/') | crontab -
```

Also keep a copy of `/etc/securebrand-youtube` somewhere safe and off the server. It holds the secrets, so keep it encrypted, for
example in a password manager.

**Restoring a backup** (stop collection, restore, put back newer erasures, run the audit):

1. Stop the services that write:

```bash
docker compose stop scheduler worker
```

2. Restore the dump you choose (list them with `ls /var/lib/securebrand-youtube/backups`):

```bash
docker compose exec -T postgres pg_restore -U ytcollect -d ytcollect --clean --if-exists < /var/lib/securebrand-youtube/backups/ytcollect-YYYYMMDDTHHMMZ.dump
```

3. Put back the erasures made after that dump:

```bash
ytc suppression import /var/lib/securebrand-youtube/backups/suppression-list.jsonl
```

4. Erase again what the old dump brought back:

```bash
ytc privacy-audit
```

5. Start the services:

```bash
docker compose start scheduler worker
```

## 10. The archive

Every day the scheduler moves monthly history older than 12 months (snapshots, comment versions, commenter profiles) into
compressed files in storage (D27). Each file is checked before Postgres drops that month. Nothing is deleted.

1. See what is archived:

```bash
ytc archive list
```

2. Find archived rows, for example the comment versions on one channel in 2025. Replace `CHANNEL_ID` with the channel's `UC…` ID:

```bash
ytc archive search -table comment_versions -channel CHANNEL_ID -from 2025-01 -to 2025-12 > /root/found.jsonl
```

3. Put a month back into Postgres for 30 days, for reports that need it in SQL:

```bash
ytc archive restore -table video_snapshots -month 2025-03 -days 30
```

## 11. Webhooks

Webhooks send events to your other systems (D20): `video.relevant`, `post.relevant`, `channel.listed` and `erasure`. Each request is
signed. A failed delivery is retried for about a day and a half, then marked dead. They're off until you add endpoints.

1. Create a shared secret for the receiver. Give it to the receiving side through a secure channel, never a chat:

```bash
(umask 077; openssl rand -hex 32 > /etc/securebrand-youtube/webhook_fbs_secret)
```

2. Create the endpoints file:

```bash
nano /etc/securebrand-youtube/webhooks.json
```

```json
{"endpoints": [{"name": "fbs", "url": "https://hooks.example.com/youtube",
  "secret_file": "/etc/securebrand-youtube/webhook_fbs_secret",
  "events": ["video.relevant", "post.relevant", "channel.listed", "erasure"]}]}
```

3. Restart the scheduler and the worker so they read it:

```bash
docker compose restart scheduler worker
```

4. Send a signed test ping:

```bash
ytc webhooks test -endpoint fbs
```

5. See the deliveries, and queue the dead ones again once the receiver is fixed:

```bash
ytc webhooks list
```

```bash
ytc webhooks retry -endpoint fbs
```

**For the receiving side.** Each request is a JSON `POST` with this body:

```json
{"id": 123, "type": "video.relevant", "created_at": "2026-11-03T12:00:00Z", "data": {"video_id": "...", "channel_id": "...", "title": "...", "url": "..."}}
```

It carries these headers:

- `X-SecureBrand-Event`: the event type.
- `X-SecureBrand-Delivery`: the event ID. It's the same on every retry, so use it to drop duplicates.
- `X-SecureBrand-Timestamp`: Unix seconds.
- `X-SecureBrand-Signature`: `v1=` followed by the hex HMAC-SHA256 of `timestamp + "." + body`, with the shared secret.

Check the signature and that the timestamp is within 5 minutes, then answer with any 2xx status. In Python:

```python
import hashlib, hmac, time

def verify(secret: bytes, headers, body: bytes) -> bool:
    ts = headers["X-SecureBrand-Timestamp"]
    if abs(time.time() - int(ts)) > 300:
        return False
    want = "v1=" + hmac.new(secret, ts.encode() + b"." + body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(want, headers["X-SecureBrand-Signature"])
```

An `erasure` event (`{"kind": "author", "id": "UC…"}`) means: delete what you stored about that item. After delivery, the ID is
removed from the sending side.

## 12. Push notifications (later, D23)

When a public subdomain exists (for example `push.example.com`):

1. Point the subdomain at the server and put HTTPS in front of `127.0.0.1:8080`, for example with Caddy.
2. Create the push secret:

```bash
(umask 077; openssl rand -hex 32 > /etc/securebrand-youtube/websub_secret)
```

3. Save the public address where Compose reads it. The worker uses it to subscribe each channel, the receiver to answer:

```bash
echo 'YT_WEBSUB_CALLBACK=https://push.example.com/websub' > ~/securebrand-microservices-youtube/deploy/compose/.env
```

4. Start the receiver and restart the others with the new setting:

```bash
docker compose --profile websub up -d
```

## 13. Updating

1. Get the new code:

```bash
cd ~/securebrand-microservices-youtube && git pull
```

2. Rebuild:

```bash
cd deploy/compose && docker compose build
```

3. Restart with the new image. `migrate` runs first and updates the tables:

```bash
docker compose up -d
```

4. Free the disk space old images use:

```bash
docker image prune -f
```

5. Check which build is running:

```bash
docker compose exec scheduler ytcollect version
```

**Releases** (REQ-010 fix 16). Pushing a tag such as `v0.1.0` builds the image with the release and commit stamped in, scans it
and publishes it as `ghcr.io/<owner>/yts:v0.1.0`; the run's summary gives the digest and the Helm values. The base images are
pinned by digest in the `Dockerfile`; to move to newer ones, change each tag and its digest together, and CI scans the result.

## 14. Turning things off

Any job kind can be switched off at once, without a restart. For example, to pause comment collection:

```bash
ytc switch comments off
```

Turn it back on:

```bash
ytc switch comments on
```

`ytc switch all off` pauses all collection.

## 15. Moving the Phase 0 data

Phase 0 data is kept (D26). Before the Phase 0 droplet is shut down, copy its folder to the production server. Run this on the
production server; it asks for the droplet's root password or uses your SSH key:

```bash
rsync -a root@DROPLET_IP:/var/lib/securebrand-youtube/phase0/ /var/lib/securebrand-youtube/phase0/
```

The files stay as they are (JSON lines, readable with any tool), next to the production data.

## 16. The outbox and the relay

Every change yts stores also goes into an outbox table, in the same save (REQ-010 fix 12). The relay (`ytcollect relay`, its own
service on the cluster) sends the outbox to the platform's Kafka: each event as the contract's message (`content.*`, checked first)
and as yts's own (`yt.*`), in one Kafka transaction per batch. Sent events are deleted after 7 days; unsent ones stay until sent.
While the relay is off or Kafka is down, events wait in the outbox and go when it is back.

What is waiting, by kind:

```bash
ytcollect outbox
```

Check the waiting events against the contract, without sending anything. It lists any the contract would refuse, with the reason:

```bash
ytcollect outbox check
```

**Alert `OutboxBacklog`** (events have waited more than 30 minutes, once a relay has run): the relay is behind, failing or
stopped. Its log says why ("sending a batch failed"); `yt_relay_transactions_total` shows aborted or failed transactions. Kafka down
or a topic missing (the platform creates them; the relay never does) keeps events waiting; they go when it is fixed.

One relay sends at a time: it holds a lease (a row in the `leases` table, renewed every 5 seconds). A second relay stands by and
tries every 15 seconds; it takes over within about 15 seconds of the first one stopping or losing the lease. While nothing waits,
the relay looks for new events less often, from every second up to every 30 seconds.

**Alert `OutboxRefused`**: some events will not be sent until something changes: the contract refused them (a bug in yts's mapping,
or data the contract can't express), or Kafka can't take them (too large). `ytcollect outbox check` shows each with its reason.
After the fix is deployed, the relay's restart tries them again.

**Without Kafka transactions.** The relay uses the transactional ID `yts-relay`. If Kafka's access rules can't grant it (the
relay's log then says "beginning a transaction" or "committing" failed, with an authorization error such as
`TRANSACTIONAL_ID_AUTHORIZATION_FAILED`), set `relay.transactions: false` in the chart (the flag `-transactions=false`). The relay
then sends each batch without a transaction; the producer stays idempotent, so a retry inside Kafka doesn't write a message twice.
What changes: when part of a batch fails, the events up to the first failure are marked sent and the rest go again, so a consumer
may see a message twice (with the same event ID, which it drops) but never an older message after a newer one for the same post.
`yt_relay_transactions_total` stays at zero in this mode; `yt_relay_published_total` and the `OutboxBacklog` alert work as before.
The start-up log line "relay starting" says `transactions=false`.

## 17. The media hand-off

yts downloads video thumbnails, channel avatars and banners, community post images (`post_image`, fix 21) and commenters'
avatars. The contract's rule for media files (R5) is that a file is kept until analysis is done with it, and at least 24 hours.
The relay does this, once storage and Kafka are both there, for the kinds in `relay.media.kinds`: thumbnails, channel avatars
and banners (the owner, 11 November) and post images (13 November, "all platforms media") by default. Add `author_avatar` to
hand off commenters' avatars too; kinds not listed are kept.

A post's images are one job: the Posts tab gives their addresses (the largest size of each), and the job downloads each one not
stored yet. A post that shows new images is queued again; a page that shows none keeps the stored addresses.

1. Every minute it announces new images of those kinds on `content.media.ready.v1`, each with a deadline 72 hours later
   (`relay.media.keep` in the chart).
2. It reads analysis's acknowledgements from `content.media.processed.v1`, in consumer group `yts-media`. After a retryable
   failure it announces the file again, at most 3 more times.
3. Every 10 minutes it deletes each file that analysis has acknowledged (processed, skipped, or failed for good), and each file
   whose deadline has passed, and says so on `content.media.deleted.v1`.

**Held kinds.** `worker.mediaHold` lists image kinds that are not downloaded for now. Their jobs stay queued and come back
every 7 days, without a request, until the kind is released.
- Production holds `author_avatar`, commenters' avatars: D-061 covers listed channels only, and keeping commenters' pictures
  was never decided. Thumbnails were held from 12 to 13 November (D-052, "Not now for thumbnails") and released by the
  owner's D-061 ("youtube thumbnails turned on"). Listed channels' avatars and banners are collected and handed off (D-050).
- To release a held kind, remove it from `worker.mediaHold`. Its queued jobs then download at their own priority (§7): a
  new video's first thumbnail at P1 (the video's first reading moves it up, with the channel, so tiers A and B go first), a
  backfilled video's at P3, the recheck a week on at P3, a listed channel's new or changed avatar or banner at P2, and the
  rest in spare time (P4).
- To stop all image downloads at once, without a deploy, run `ytcollect switch media off`; `on` restarts them.

The record of every image (its row, and `content.media.v1`) stays forever; only the file goes. With the hand-off off
(`relay.media.handoff: false`, or without Kafka) nothing is announced and nothing is deleted. A deleted avatar or banner is not
downloaded again: yts knows it by its address and its content, so only a new picture is fetched (and handed off).

What the relay has done, by kind and state (`stored`, `announced`, `deleted_processed`, `deleted_expired`), is the metric
`yt_media_handoff`. Files deleted without analysis (`deleted_expired`) mean analysis did not answer within the deadline.

**Alert `MediaPastDeadline`**: files are still in storage an hour after their deadline. The relay could not delete them (its log
says "deleting an analysed file failed", with the storage's answer), or the relay is stopped. When storage answers again, the
next round deletes them.

## 18. The database through PgBouncer

On the platform, PostgreSQL is reached only through PgBouncer in transaction mode, with 50 connections shared by every service
(REQ-023, REQ-024). yts is built for that:

- **Connections:** each process opens at most 4. It sends no startup parameters and prepares no statements on the server, and no
  setting outlives a transaction. One-at-a-time work uses a lease row (the relay) or a lock that ends with its transaction (the
  migrations, new partitions), never a session lock.
- **When the database is full** (no free connection: PostgreSQL's "too many connections", or PgBouncer's `query_wait_timeout`,
  "server login has been failing" or "no more connections allowed"), yts waits and tries again, logging each wait ("the database
  has no free connection; waiting"): the migrations for up to 45 minutes (`migrate.waitForSlot` in the chart; give `helm upgrade` a
  `--timeout` of 60 minutes), the services for up to 10 minutes at start-up, a command run by hand for a minute. A wrong password,
  or any other error, fails at once.
- **While idle:** `/readyz` checks the database at most once a minute and answers from the last result in between; `/healthz`
  doesn't touch it. The worker, the relay and the webhooks look for work less often while they find none, from every second up
  to every 30 seconds, and at once again when work turns up. An idle process touches PostgreSQL about once every few seconds at
  most, so PgBouncer can give its server connection back.

If the database is full for longer than the wait, the migrate Job fails and the deploy stops; run it again once there is room.

## 19. Facebook discovery

The owner, 13 November (D-065): "activate Facebook discovery". When a listed channel's About panel or description has a
Facebook link (facebook.com and its subdomains, fb.com, fb.me), yts sends it to the Facebook collector, which checks the page
and may add it to its own schedule. It uses what yts already reads at each channel refresh: no request is made to YouTube for
it. Commenters are never read.

- **Off by default.** The ops action creates the Secret with the collector's key, then sets `scheduler.fbDiscovery.enabled`,
  `.url` (the collector's API address) and `.secret.name` (and `.secret.key` if not `api_key`). The key is read from the
  mounted file and never logged. If the collector runs in the cluster, also set `networkPolicy.egress.fbDiscovery`.
- **Every 10 minutes** the scheduler records the new links (table `fb_candidates`) and sends the unsent ones, oldest first:
  `POST <url>/v1/discovery/candidates` with `ref` (the link as found), `source` `yts` and `evidence_url` (the channel's page).
- **Each link once**, by the link as found. A channel's new links are sent when its page shows them.
- **At most `dailyLimit` a day** (UTC; 200 to start).
- **503 or 429** (the collector's mentions are off, or it is busy), **401 or 403** (the key): yts waits an hour and tries
  again; the links stay unsent. Another 4xx is recorded as `rejected` and not sent again.
- **What it did:** `yt_fb_candidates{result}`, the links by the collector's answer (`queued`, `on_list`, `added`,
  `public_waiting`, `checked_recently`, `ignored`, or `rejected`), and `unsent`. The scheduler logs "facebook links" with the
  new and sent counts, and "facebook discovery paused" when it waits.
- **Erasure:** erasing a channel deletes its rows here.

## 20. Suggested videos in discovery

The owner, 14 November: "for youtube, we need to add the suggested videos to the discovery as well, as it can lead to new
channels that we didnt discover in other ways".

- **Where they come from:** the side list of the watch page yts already reads at each video's first full reading (core channels'
  videos, and mention channels' Iraq-relevant ones). No extra request.
- **Which count:** a suggestion whose title matches the Iraq keywords, from a channel yts does not know yet. It becomes a
  candidate with the source `suggested` and gets the usual candidate check (3 requests, P3), which lists it as Iraqi or
  mention, or not at all, by the same rules as search finds. A suggestion of a candidate already waiting counts it again.
- **The limit:** at most `worker.suggestedDaily` new candidates a day (UTC; 500, `-suggested-daily`; 0 turns it off).
- **What to watch:** `yt_discovery_suggested_24h` (new candidates from suggestions in the last day) and the fill rate
  `watch.suggestions`, which falls if YouTube's side list stops matching the parser.
