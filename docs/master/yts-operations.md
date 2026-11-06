# yts: operations and integration

Written by the yts session on 6 November 2026 (Iraq time), from `main` `3db1415`. No secret values, proxy addresses or real data.
Measured numbers say when and how they were measured; everything else is marked as an estimate or unknown.

## 1. Components

| Component | What it does | Long-running or job | Copies | Scales out? | State |
|---|---|---|---|---|---|
| `ytcollect worker` | The production collector. It takes due jobs from the Postgres queue (channel checks, tabs, RSS, video details and stats, comments, posts, playlists, search, candidate checks, commenter enrichment, image downloads) and runs up to `-workers` (10) at once through the proxy pool | Long-running | 1 | The queue can feed several workers (`FOR UPDATE SKIP LOCKED`, one job per channel at a time), but **per-IP pacing lives in each process**, so two workers on the same proxies would double the rate per IP. Run one per proxy set until pacing moves to a shared store | Postgres; pacing and cooldowns in memory (cooldowns also written to `proxy_health`) |
| `ytcollect scheduler` | Maintenance: monthly partitions, missing jobs, the daily privacy audit, the archive, the capacity planner, keyword suggestions, metrics (`:9102`), the webhook dispatcher (every 10 s) and the hourly copy of the suppression list | Long-running | 1 | **No**: no leader election, so run exactly one | Postgres |
| `ytcollect websub` | Receiver for YouTube push notifications | Long-running | 0 (off until a public subdomain exists, D23) | Yes (stateless) | Postgres |
| `ytcollect migrate` | Creates or updates the tables (embedded migrations 0001–0009), under an advisory lock | Job, before the others | 1 | n/a | |
| `ytcollect` admin commands | Channels, keywords, erasure, export, archive search and restore, webhooks, suppression list export and import | CLI | | | |
| `backup` | `pg_dump` every 24 hours; keeps 30 days on the server | Long-running loop | 1 | No | Dump files |
| `prometheus`, `alertmanager` | Metrics and alerts | Long-running | 1 each | | Their own volumes |
| `ytprobe` | The Phase 0 measurement kit: `crawl`, `detect`, `discover`, `census`, `accuracy`, `pace`, `label`, `resweep`, `transcripts`, `report` and others | Jobs, except `detect` | | No | JSON-lines files |
| `fakeyt` | A fake YouTube for tests (`internal/fakeyt`) | Tests only | | | |

**On the shared pilot server today, only `ytprobe` runs:**
- **`ytprobe-detect`:** long-running. It checks the RSS feeds of the 327 listed channels every 15 minutes, and their Videos, Shorts and
  Live tabs daily, to measure how fast new uploads are seen. It was started on 2 November and restarted on 4 November after two
  unresolvable channel entries were removed.
- **`ytprobe-crawl`:** a one-off job. It read 38 channels' last year, with their videos and comments. It ran from 2 November until
  04:47 on 3 November (Iraq time), when it stopped because the server stopped responding; the server's Docker service started fresh
  after that. That matches the tks Chrome incident in the service standard (B2): likely, not verified. Its data was enough for the
  measurements, so it was not restarted.
- **Owner-run jobs:** `discover` and `census` (about 15 and 35 minutes) are started by the owner. The last pair ran on 5 November;
  Phase 0 ends with it (D30).

The production stack (`ytcollect` and the rest) **is not running anywhere**.

## 2. Packaging and deployment

1. **Images:**
   - **What there is:** one image with both binaries (`Dockerfile`: a Go build, then distroless static).
   - **Built where:** CI builds it on every change to check that it builds, but publishes nothing. On the pilot server the owner builds
     it locally (`docker build -t ytprobe .`).
   - **Tags:** no version tags and no registry. The Compose file names it `securebrand-youtube:latest`.
   - **User:** it **runs as root**, so it can read owner-only secret files mounted from the host. Gaps A1 and A2. *Since REQ-010
     fix 13: user 65532, no capabilities, a read-only root filesystem; secrets readable through the pod's fsGroup on Kubernetes.*
2. **Deployment:**
   - **Compose:** `deploy/compose/docker-compose.yml` runs postgres, migrate, scheduler, worker, backup, prometheus and alertmanager,
     plus websub as an optional profile. CI validates it with `docker compose config`, and the Prometheus and Alertmanager configs with
     promtool and amtool. It has never been started as a whole.
   - **Not there:** no Helm chart and no Kubernetes manifests. The setup steps are in `docs/production-runbook.md`.
3. **Startup:** `migrate` runs first, and the other services wait for it to finish. There are no Kafka topics or schemas to create. The
   scheduler creates monthly partitions ahead of time.
4. **Where it runs today:** on the shared pilot server `201.79.36.209`, the `ytprobe-detect` container (`docker run -d --rm`, no memory
   limit) and the owner's occasional `ytprobe` jobs. Files:
   - secrets in `/etc/securebrand-youtube` (`chmod 600`);
   - data in `/var/lib/securebrand-youtube/phase0`.

## 3. Dependencies

| Dependency | Version | Used for | Shared with another service? | Size today, and growth |
|---|---|---|---|---|
| Postgres | 16 | Everything in production: content, history, the job queue, settings, the webhook outbox | No (its own container in Compose) | None yet (not deployed). Estimate from the plan: about 7.5 GB a year per 1,000 channels |
| Kafka or Redpanda (+ Schema Registry) | — | Not used | | |
| Valkey or Redis | — | Not used | | |
| Object storage (S3) | Any S3-compatible service (AWS SigV4), or a local folder | Images (thumbnails, avatars, banners) and the archive (partitions older than 12 months, as gzip JSON lines); a copy of the suppression list | No | Off until configured. Estimate: about 10 GB a year of images per 1,000 channels |
| ClickHouse or other | — | Not used. The plan left the door open for archive queries (D27) | | |
| Browser (Chromium) | — | **Never** | | |
| GPU or AI APIs | Anthropic API | Phase 0 only: labelling 300 public video titles for the keyword-quality test. Done. The `ytcollect` binary links the SDK (through `internal/phase0`) but never calls it or reads a key (see `yts-gaps.md`) | | |
| Other | YouTube Data API v3 | Discovery search (production); one-time accuracy check (Phase 0) | Unknown whether the key's Google Cloud project is used by anything else | |
| Other | Proxies | All YouTube traffic | **Yes: the Phase 0 pool is fbs's pilot proxy list** (see §6) | |

## 4. Kafka

Nothing today: yts publishes and consumes no Kafka topics, and has no Schema Registry subjects.

- **Outbound events today:** HTTP webhooks from a transactional outbox (`webhook_events`, filled by database triggers):
  - events `video.relevant`, `post.relevant`, `channel.listed` and `erasure`;
  - signed with HMAC-SHA256;
  - retried with backoff from 30 s up to 6 h, then marked dead after 15 attempts.
- **The plan for Kafka:** see `yts-contract-notes.md` and the plans in `yts-gaps.md`.
- **Expected message rate per 1,000 channels per day:** estimates, from the plan's volume model (§6.5) and the Phase 0 census:

| Event | Per 1,000 channels per day | How estimated |
|---|---|---|
| Comment (new, plus a new version for each edit) | about 19,000 | 140 M comments a year at 20,000 channels |
| Metrics (a video's counts at each check) | about 10,000–15,000 | Most of the 25 requests per core channel per day are stats checks |
| Video (new, plus changes) | about 300–1,000 | Census: median 3 and p90 about 65 uploads per channel per month |
| Channel snapshot | about 300–1,000 | Depends on the tier mix (A every 6 h … D weekly) |
| Commenter profile | Up to several thousand while catching up | Spare capacity only |

## 5. Storage

- **Database (Postgres):** no production database exists yet, so the rows today are 0. The tables:

| Group | Tables | Notes |
|---|---|---|
| Content | `channels`, `videos`, `comments`, `posts`, `playlists`, `playlist_items`, `authors`, `author_badges`, `author_profiles` | Upserts keyed by YouTube IDs |
| History | `channel_snapshots`, `video_snapshots`, `post_snapshots`, `comment_versions`, `channel_changes`, `video_changes` | The four snapshot and version tables, and `author_profiles`, are partitioned by month |
| Discovery | `keywords`, `keyword_variants`, `keyword_suggestions`, `keyword_stats`, `languages`, `search_terms`, `search_hits`, `candidates`, `channel_links_graph`, `channel_inputs` | Official-API search hits are deleted after 30 days |
| Operations | `schedule` (the job queue), `quota_ledger`, `proxy_health` (no credentials), `parse_failures`, `field_fill_rates`, `capacity_reports`, `settings` (kill switches, speed, retention) | |
| Privacy | `suppression_list` (keyed hashes only), `erasure_requests`, `erasure_audit` | Dormant (R6) |
| Media and archive | `media`, `archive_index`, `archive_months` | |
| Webhooks | `webhook_events` (the outbox), `webhook_endpoints`, `webhook_deliveries` | |

- **Growth and retention:** about 7.5 GB a year per 1,000 channels (plan estimate). Retention is none: everything is kept (D26). The one
  exception is official-API search hits, deleted after 30 days. The archiver moves whole monthly partitions older than 12 months to
  object storage and drops them from Postgres. They stay searchable, and a month can be restored for a while.
- **Valkey:** not used.
- **Object storage:**
  - **Keys:**
    - `media/<kind>/<id>/<hash>.<ext>`;
    - `archive/<table>/<YYYY-MM>/part-NNNN.jsonl.gz`;
    - the suppression-list copy.
  - **Lifecycle:** none; nothing is deleted. Not configured anywhere yet.
- **Rebuildable state:** content can't be rebuilt. Deleted comments and old versions are gone from YouTube. Postgres and the privacy key
  must be backed up. The job queue can be rebuilt: the scheduler recreates missing jobs.
- **Phase 0 files:** the size of `/var/lib/securebrand-youtube/phase0` hasn't been measured. A read-only command for the owner:

```bash
du -sh /var/lib/securebrand-youtube/phase0
```

## 6. Network

- **Inbound:**
  - `:9101` `/metrics` (worker) and `:9102` `/metrics` (scheduler);
  - `:8080` push receiver (off);
  - Prometheus `127.0.0.1:9090` and Alertmanager `127.0.0.1:9093`;
  - **no `/healthz` or `/readyz`** (gap B1);
  - `ytprobe` listens on nothing.
- **Outbound** (YouTube traffic goes through the proxies):

| Host | What | Through proxies? |
|---|---|---|
| `www.youtube.com` | InnerTube (`/youtubei/v1/browse`, `next`, `player`, `search`), RSS feeds, the home page, handle resolution | Yes |
| `i.ytimg.com`, `*.ggpht.com`, `*.googleusercontent.com` | Images | Yes |
| `www.googleapis.com` | Data API v3 (`search`; `videos` once in Phase 0) | No: direct from the server, with the API key in a header |
| `pubsubhubbub.appspot.com` | Push subscriptions (when on) | No |
| `api.anthropic.com` | Phase 0 title labelling only (done) | No |
| The webhook receivers and the S3 endpoint | As configured | No |

- **Proxies:**

| Item | Value |
|---|---|
| Tier | ISP. Datacentre tested only for its share; no residential |
| IPs | **10**, all ISP |
| Where they come from | **They are fbs's pilot proxies.** The owner chose to reuse them (2 November), so yts and fbs send traffic through the same IPs, each with its own pacing. They don't add to each other's rate on a platform: Facebook sees only fbs, YouTube only yts. But they share the provider's bandwidth and connection limits, and one bill |
| Providers | Names unknown to this session (ask the owner or the fbs session) |
| Safe rate per IP (Phase 0 E1, measured 3–4 November, ISP tier) | Clean down to **1 request per second per IP** on browse, next, search, RSS, watch and the image CDN (420 requests per endpoint, no blocks). **The player was blocked even at one request per 10 s**, so production asks it only when needed. Production uses a 2-second gap |
| Blocks (Phase 0) | 1.2 per 1,000 requests overall (83 in 70,869), all on the player. No pool-wide pauses and no subnet rests |
| Monthly cost | Paid as part of fbs's pilot. Unknown to this session |

## 7. Configuration and secrets

- **Configuration:**
  - **How it's set:** command flags and environment variables, plus runtime settings in the `settings` table (kill switches per job
    kind, the speed multiplier, retention: off). Discovery's daily API ceiling defaults to 9,500 units.
  - **Worker flags:**
    - `-interval` sets the gap between requests per IP (default 4 s, 2 s in Compose);
    - `-workers` sets how many jobs run at once (10);
    - `-comment-cap` sets the most comments the first read of a video takes (20,000);
    - `-no-player` turns the player off.
  - **Start-up validation:** missing or unreadable secret files, files readable by others than their owner, and a bad storage file
    stop the program with the reason. There is no full validation of every setting (gap A4).
- **Secrets** (all read from files; the environment variable gives the path; nothing is logged):

| Secret | Environment variable | Default file |
|---|---|---|
| Database address with its password | `YT_DATABASE_URL_FILE` | `/etc/securebrand-youtube/database_url` |
| Postgres password (Compose) | Compose `POSTGRES_PASSWORD_FILE` | `/etc/securebrand-youtube/postgres_password` |
| Privacy key (keyed hashes) | `YT_PRIVACY_KEY_FILE` | `/etc/securebrand-youtube/privacy_key` |
| Proxy list | `YT_PROXIES_FILE` | `/etc/securebrand-youtube/proxies.txt` |
| Data API key | `YT_API_KEY_FILE` | `/etc/securebrand-youtube/youtube_api_key` |
| Push signing secret | `YT_WEBSUB_SECRET_FILE` | `/etc/securebrand-youtube/websub_secret` |
| Object storage keys | Named inside `YT_STORAGE_FILE` (`storage.json`: `access_key_file`, `secret_key_file`) | |
| Alertmanager receiver token (optional) | `credentials_file` in `alertmanager.yml` | |
| AI key (Phase 0 only) | `YT_ANTHROPIC_KEY_FILE` | |

- **Never logged:** the logger redacts proxy URLs and authorization headers. The API key travels in a header, never in a URL. Errors
  never include secret values.

## 8. Resources

**Not measured.** No container has a memory or CPU limit.
- `ytprobe-detect` is one Go process doing network-paced work: about 33,000 small RSS requests a day, at most one request per IP at a
  time.
- `ytprobe-crawl` held a channel's video and comment IDs in memory while crawling.
- There has been no soak test.

To measure without changing anything, the owner can run these read-only commands while a job runs:

```bash
docker stats --no-stream ytprobe-detect
```

```bash
docker stats --no-stream ytprobe-census
```

The memory limits proposed for the shared server are in `yts-gaps.md`, Plans.

**Network** (measured in Phase 0):
- **Bytes:** a median of 57 KB compressed per page request; feed requests are far smaller.
- **Growth per 1,000 channels:** about 24,000–64,000 requests a day, and about 40–110 GB of proxy traffic a month after backfill. The
  backfill is a one-time 50 GB or so. The range is the fixed cost of discovery searches. These are estimates, in §13.

## 9. Observability

- **Health:** only `/metrics`. No `/healthz` or `/readyz`.
- **Metrics:** prefix `yt_`, about 60 families (`internal/monitor/monitor.go`). The key ones for "is it working":
  - **Queue:** `yt_queue_due`, `yt_queue_oldest_due_seconds`, `yt_queue_failing`;
  - **Fresh data:** `yt_videos_new_24h`, `yt_comments_new_24h`, `yt_detection_delay_seconds`;
  - **Comment coverage:** `yt_comments_complete_pct`;
  - **Proxies and blocks:** `yt_pool_speed`, `yt_pool_blocked_requests_total`, `yt_proxy_blocks_total`, `yt_proxies_quarantined`,
    `yt_proxies_cooling`;
  - **Parser drift:** `yt_fill_rate` against `yt_fill_rate_baseline`, `yt_parse_failures_24h`, `yt_canary_channels_ok`;
  - **Capacity:** `yt_capacity_supply_per_hour`, `yt_capacity_demand_per_hour`, `yt_freshness_debt_hours`;
  - **API quota:** `yt_api_quota_used_units`.
- **Logs:** structured (Go `slog`), with IDs, counts and reasons. Never proxy URLs, keys, comment text or author names. `ytprobe`
  writes hashed channel and video IDs in its detection records.
- **Alerts:** 17 rules in `deploy/prometheus/alerts.yml`, for example:
  - the pool brake engaged;
  - a proxy quarantined;
  - a fill-rate drop;
  - a parse-failure spike;
  - the canary failing;
  - stale metrics;
  - growing freshness debt;
  - low comment completeness;
  - the API quota exhausted;
  - webhook dead letters.

  They have no runbook links, and there is no Grafana dashboard (gaps F3, F4).
- **Health check for the owner:** `docs/production-runbook.md` §7 for production. For Phase 0 today, read-only:

```bash
docker ps --filter name=ytprobe-
```

```bash
docker logs --tail 5 ytprobe-detect
```

Good looks like `ytprobe-detect` shown as "Up", and recent "new upload seen" lines.

## 10. Reliability

- **Retries:**
  - **Backoff:** 2 minutes, doubling, capped at 6 hours.
  - **No cap on attempts** and no dead-letter queue: a job keeps failing every 6 h (gap C1).
  - **Why it can't explode:** there is one row per item, so retries never multiply.
- **Stuck jobs:**
  - A job's lease expires on its own if a worker dies, and the job runs again.
  - Long backfills renew their lease.
  - A job that fails forever is visible in `yt_queue_failing`, but isn't closed (gap C3).
- **Circuit breakers:**
  - **Per IP:** a blocked IP rests for 30 min, then 2 h, 12 h and 24 h on repeats.
  - **Quarantine:** an IP blocked on several days in a row is quarantined, with an alert.
  - **Per request:** one that meets blocks on 3 IPs is parked for 6 hours.
  - **Per subnet:** a subnet with several blocked IPs rests.
  - **Pool brake:** a block storm slows the whole pool. Player blocks don't count towards it.
  - **Feed outages:** when the RSS service answers "not found" (42% of Phase 0 feed checks, all channels at once), detection reads the
    first page of the tabs instead.
- **What survives a restart:**
  - the queue, cooldowns (`proxy_health`) and the API quota ledger, all in Postgres;
  - not the in-memory pacing (it restarts slowly);
  - not the cached tab parameters (re-read from the channel page).
- **Graceful shutdown:**
  - **Stops:** SIGTERM cancels the context, and the worker stops leasing.
  - **Unfinished jobs:** they keep their lease until it expires, then run again. Writes are transactional, so nothing is half written.
- **Backpressure:**
  - Work is pulled from the queue at the pace the proxies allow, so nothing is buffered.
  - When demand is above supply, the capacity planner stretches the intervals of tiers C, D and M; A and B are never stretched.
- **When YouTube blocks:** the request stops and is counted, and the IP rests. The job moves to another IP after a short random delay.
  **No challenge is ever answered** (D2).
- **When something is down:**
  - **Database down:** the worker and scheduler fail their jobs and retry.
  - **Kafka, Valkey:** not used.
  - **Object storage down:** image jobs retry; the archiver waits.

## 11. Platform access and policy

- **Login:** logged out. No Google or YouTube accounts and no account cookies, ever. Each IP keeps the anonymous visitor ID that every
  logged-out browser receives; IDs are never shared between IPs.
- **Bot checks:**
  - Nothing hides automation, solves or bypasses challenges, or generates anti-bot tokens. No BotGuard or proof-of-origin tokens, no
    headless browsers, no pretending to be a search engine.
  - Endpoints that need such tokens are switched off: transcripts, caption files, video and audio streams.
  - A challenged request is not answered; the IP rests.
- **Terms:**
  - **YouTube's Terms of Service** forbid automated access; the owner has the legal sign-off (D29, R10).
  - **Data API:** 10,000 units a day by default. `search.list` costs 100 units; yts caps itself at 9,500.
  - **The API storage rule** (data from the API kept at most 30 days unless refreshed) is in `yts-contract-notes.md`.
- **Rate limits:** set from measurement (Phase 0 E1: 1 s clean, 2 s used; the player is used sparingly because it was blocked even at
  10 s).
- **Data API quota use so far** (Phase 0):
  - **`discover`:** 20 searches per run, 2,000 units (measured in the 4 November report).
  - **Accuracy check:** a handful of `videos.list` units (159 videos at 1 unit per 50).
  - **Key check:** 1 unit per command start.
  - **Production:** priority keywords every 3 hours, up to 9,500 units (95 searches) a day.
  - **Exact use per day:** in the ledger file on the server. It holds counts only, so it is safe to print:

```bash
cat /var/lib/securebrand-youtube/phase0/api_quota.json
```

## 12. Data contract

- **Shared `content.*` contract:** not published. yts has no protos and no Kafka.
- **Platform topics:** none. The webhook events (`video.relevant`, `post.relevant`, `channel.listed`, `erasure`) have no configured
  receiver today.
- **To adopt the unified contract**, yts needs:
  - the `securebrand-contracts` repository at a tagged version, with Go code and the conformance kit;
  - the topic names, partitions and retention;
  - the Kafka and Schema Registry addresses and credentials, as files;
  - a decision on the Data API storage rule for anything that carries API data (`yts-contract-notes.md`).

## 13. Cost per month (estimates unless marked)

| | 1,000 channels | 10,000 channels |
|---|---|---|
| Requests a day after backfill | 24,000–64,000 (14,000 collection + 10,000–50,000 discovery) | about 170,000 |
| Proxy traffic (at the measured 57 KB a request) | 40–110 GB a month, plus about 50 GB once for the backfill | about 290 GB a month, plus about 500 GB once |
| Proxy IPs (2 s gap, 70% use, failover spare) | 5 (the minimum for failover across 3 subnets) | 8 after backfill; 10–15 to finish the backfill in about 30 days |
| Proxy cost | 5 ISP IPs at about $1.25–2.50 each (plan figure, not checked against current prices), unless the plan bills by GB | 10–15 IPs |
| Servers | Not measured. Likely 1 vCPU and 1–2 GB for the worker and scheduler, plus Postgres | Not measured |
| Database growth | about 7.5 GB a year | about 75 GB a year |
| Object storage (images, if on) | about 10 GB a year | about 100 GB a year |
| API fees | $0 (Data API within the free quota) | $0 |
| AI | $0 in production | $0 |

## 14. Status and risks

1. **Where the build stands:**
   - **Built:** every production step of the plan (1–20) and the Compose deployment are built and merged, with tests (unit and
     integration against a real Postgres, end to end against a fake YouTube).
   - **Phase 0:** ends with the 5 November census (D30).
   - **Proven against the real site** (Phase 0):
     - the parsers;
     - the safe pace;
     - comment completeness (median 100%);
     - accuracy against the official API (0% median difference);
     - feed-based detection (median 19 minutes after publishing).
   - **Not proven:** `ytcollect` has never run against the real site, only against the fake one in tests. The feed-outage fallback
     and the comment-count fix (4 November) are unverified on real data. There has been no soak test.
2. **Open issues:**
   - the player answers sparingly (85% in the crawl; blocked at one request per 10 s in the pace test);
   - the RSS service was "not found" 42% of the time in Phase 0 (fallback built);
   - the gaps in `yts-gaps.md`.
3. **Top three risks in production:**
   1. **YouTube tightens anti-bot checks** on more endpoints, as it did on the player and transcripts. That would stop collecting the
      affected fields; the rule is never to work around them.
   2. **The collector is untested at scale** on the real site, and the platform integration (Kafka, contract, ClickHouse) isn't built.
   3. **Shared proxies with fbs:** shared bandwidth, connection limits and IP reputation, and a cost and capacity plan that has to
      cover both.
4. **What it needs from the owner:**
   - **Hosting:** the decision (Q4) before any server is bought. Earlier today I suggested ordering a Hetzner server for a production
     test. **Please hold that:** the platform plan puts production on managed Kubernetes.
   - **Proxies:** whether yts keeps sharing fbs's or gets its own.
   - **The 48-hour pilot:** where and when.
   - **A bucket**, if images are wanted.
   - **A new Data API key** at go-live (the owner's rule).
   - **An off-server backup of the privacy key**, once production starts.
