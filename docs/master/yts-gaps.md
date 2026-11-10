# yts: gaps against the production standard, and plans

Written by the yts session on 6 November 2026 (Iraq time), from `main` `3db1415`. Efforts are my estimates of build time, in draft PRs with
tests. **Plans only:** nothing is built until the owner approves.

## Gaps

| # | Status | Evidence | Fix | Effort |
|---|---|---|---|---|
| A1 Image per binary, versioned, on GHCR | **Missing** | One image with both binaries (`Dockerfile`). CI builds it but pushes nothing. No version tags; built by hand on the pilot server | A release workflow on tags: `ghcr.io/abdullah95k/yts:<version>` (one image, two entrypoints, or two images) | 0.5 day |
| A2 Non-root, read-only root, no capabilities | **Missing** | The image runs as root, to read owner-only secret files mounted from the host | Distroless `nonroot`; `readOnlyRootFilesystem`; drop all capabilities. The secrets loader refuses files readable by others than their owner: allow Kubernetes secret mounts (`defaultMode 0400` with the pod's `runAsUser`) | 1 day |
| A3 Helm chart or manifests, settings in values | **Missing** | Docker Compose only (`deploy/compose`), validated in CI | A Helm chart: worker, scheduler, relay and websub Deployments; migrate as a hook Job; every flag in values; ServiceMonitor and PrometheusRule | 2–3 days |
| A4 Config validated at start-up | **Partly** | Secret files are checked (present, owner-only, size), and the storage file (HTTPS required). Flags aren't range-checked | Validate every flag and setting at start, and refuse to start with the reason | 0.5 day |
| A5 Secrets from files or the environment | **Met** | Every secret is a file named by a `*_FILE` variable (`internal/secrets`); nothing is baked in or committed; gitleaks over the full history | — | — |
| A6 Versioned migrations, run by a job, compatible for one release | **Partly** | Embedded migrations 0001–0009, run by `ytcollect migrate` (advisory lock) before the services start. Every one so far is additive, but compatibility isn't tested | A CI check that runs the previous release against the new schema; a rule in the PR template | 1 day |
| B1 `/healthz`, `/readyz`, `/metrics` | **Partly** | `/metrics` on `:9101` (worker) and `:9102` (scheduler) only | Add `/healthz` (alive) and `/readyz` (database answers, proxies loaded) on the same port | 0.5 day |
| B2 Measured memory and CPU limits | **Missing** | Not measured. No limits in Compose. `ytprobe-detect` runs without a limit on the shared server | Measure (read-only `docker stats`). Cap the Phase 0 containers (Plans, 2). Set measured requests and limits in the chart, from the soak test (G3) | 0.5 day, plus G3 |
| B3 No browser | **Met** | Never: no Chromium, and endpoints that would need one are off | — | — |
| B4 Graceful shutdown | **Partly** | SIGTERM stops leasing; writes are transactional. Running jobs aren't handed back: their leases simply expire, then they run again | On SIGTERM, finish or release leases within the grace period (`lease_until = now()`) | 0.5 day |
| B5 Sessions and pacing survive restarts | **Partly** | No logins or sessions. Cooldowns and quarantine are held in memory and end with a restart; `proxy_health` is written for metrics and not read back (corrected 11 November); the queue and quota are in Postgres. The anonymous visitor ID per IP is fetched again at start (one home-page load per IP); pacing restarts slowly | Persist the visitor ID per IP. The restart cost is small: 10 page loads | 0.5 day |
| B6 Horizontal scaling, fleet-wide limits in Valkey | **Missing** | The queue is shared (`SKIP LOCKED`), but per-IP pacing is per process: two workers on the same proxies would double the rate. The scheduler has no leader election | Per-IP token buckets in Valkey; leader election for the scheduler (a Postgres advisory lock) | 2–3 days |
| B7 Internal times in UTC | **Met** | `timestamptz`, UTC logs; owner-facing docs use Iraq time | — | — |
| C1 Bounded retries, dead-letter queue | **Partly** | Backoff 2 min doubling to 6 h. **No attempt cap, no dead-letter queue.** One row per item, so retries never multiply. Webhooks have dead letters after 15 attempts | A maximum number of attempts per kind, then `dead`; a dead-letter view, an alert and a `requeue` command | 1 day |
| C2 Retention on every Kafka topic | **N/A today** | No Kafka. Applies to the relay's topics | Set in the Kafka plan (Plans, 1) | — |
| C3 Stuck jobs closed after a maximum age | **Partly** | A dead worker's lease expires and the job runs again. A job that always fails is retried forever (visible in `yt_queue_failing`) | With C1 | — |
| C4 Circuit breakers per IP and per query | **Met** | IP cooldown ladder (30 min → 24 h), quarantine, subnet rests, a request parked after blocks on 3 IPs, a pool brake that the player can't trigger (`internal/proxypool`, tested). Feed outages fall back to the tabs | — | — |
| C5 The pool can't run dry unnoticed | **Partly** | Metrics `yt_proxies_cooling`, `yt_proxies_quarantined`, `yt_pool_speed`; alerts PoolBrakeEngaged and ProxyQuarantined. No alert for "too few healthy IPs". Session-starting requests are one home-page load per IP per start | Add the alert | 1 hour |
| C6 Backpressure | **Met** | Pull-based: work is leased only as fast as the proxies allow. The capacity planner stretches tiers C, D and M under load. The relay will slow when Kafka does | — | — |
| C7 A daily budget cap | **Partly** | Speed multiplier, kill switches per job kind, the per-IP gap, and the API unit cap (9,500). No daily request or byte cap | A `daily_budget` setting (requests, bytes), enforced in the pool, with a metric and an alert | 1 day |
| D1 Logged out | **Met** | No accounts or account cookies, ever | — | — |
| D2 Don't circumvent bot checks | **Met** | No anti-bot tokens, no browser, no solving. A challenged request isn't answered; the IP rests. Transcripts and streams are dropped because they need tokens (E12) | — | — |
| D3 Rate limits set from measurement | **Met** | Phase 0 E1: clean down to 1 s per IP on every page type but the player; 2 s used; the player asked sparingly | — | — |
| D4 Proxy credentials never shown; exits not named by address | **Met** | Proxies print only an opaque ID (`internal/proxypool/proxy.go`), and subnets are opaque labels; URLs and auth headers are redacted from logs | — | — |
| D5 Pilot report with projections | **Partly** | The Phase 0 report has requests, bytes, blocks and completeness. `ytcollect` has no pilot report, and no projection to 1,000 or 10,000 channels with IPs and cost | A `ytcollect report` command (from the queue, snapshots, `capacity_reports` and the pool), run after the 48-hour pilot | 1–2 days |
| E1 Publishes once to Kafka | **Missing** | Postgres plus webhooks only | The outbox and relay (`yts-contract-notes.md` §1) | Part of 2–3 weeks |
| E2 Platform events and `content.*` | **Missing** | | yt protos; the `content.*` mapping after the master's v1 | In E1 |
| E3 Schemas registered, `BACKWARD_TRANSITIVE` | **Missing** | No protos yet. Nothing registered before the master publishes v1 | Register yt subjects at go-live | In E1 |
| E4 Envelope fields | **Partly** | Stable IDs, observed times and comment versions exist. No event ID, collector version or raw pointer | Added in the outbox work, with `version` columns | In E1 |
| E5 Kafka keys keep order | **N/A today** | | Comments keyed by their video or post (contract notes §1) | In E1 |
| E6 Absent is not zero | **Met** | Counts not shown are NULL. Only comment and post likes store 0 when YouTube shows no number, which is how YouTube shows zero | — | — |
| E7 Edits and deletions as new versions | **Partly** | Comment versions and deletions, video and channel change logs, removals: done. **Not versioned:** community post text, About-panel links. Comment likes are overwritten (no comment metrics) | Version post text and links; publish comment metrics at each read | 1–2 days |
| E8 Raw responses kept | **Missing** | Only scrubbed structure skeletons on parse failures (a privacy choice) | Keep the raw response of every failure, and a small sample of the rest: gzip, in a restricted bucket, with the parser version and a `raw_ref` | 1–2 days |
| E9 Bio, website and every profile link from day one | **Partly** | The full description and every About-panel link, for channels and enriched commenters. Links typed into descriptions are kept only as text; old link lists aren't kept | Extract URLs from descriptions into the links list; version the list | 0.5 day |
| E10 Coverage per job and item, with reasons | **Partly** | Per video: completeness, the "partial" flag, comments off, player unavailable, removed; fill rates per field. No per-job coverage record with reasons | A `collection.coverage` event per job from the relay | 1–2 days |
| F1 Metrics with a prefix | **Partly** | About 60 `yt_` families: queue, jobs by result, blocks per IP, quarantine, fill rates, capacity, quota. Missing: proxy bytes, items published (no Kafka yet) | Add `yt_proxy_bytes_total`, and the relay's published and lag metrics | 0.5 day |
| F2 Structured JSON logs, no content | **Met** | `ytcollect` logs JSON (`slog`), with IDs, counts and reasons; no text, names or credentials. `ytprobe` (Phase 0) logs text | — | — |
| F3 Alert rules with runbook links | **Partly** | 17 rules (`deploy/prometheus/alerts.yml`). No runbook links; no rules for no data for N minutes, or disk and memory near their limits | Add `runbook_url` annotations and those rules; ship them as a PrometheusRule | 1 day |
| F4 Grafana dashboard | **Missing** | | One dashboard: collection, blocks, freshness, comments, discovery, quota | 1 day |
| F5 Owner health check | **Met** | `docs/production-runbook.md` §7 (production). The Phase 0 checks are in `yts-operations.md` §9 | — | — |
| G1 Tests in CI, race detector | **Met** | `go test -race` with a Postgres service, `go vet` and `gofmt` on every change (`.github/workflows/ci.yml`) | — | — |
| G2 Fake platform | **Met** | `internal/fakeyt`, used by the end-to-end tests | — | — |
| G3 Load and soak tests (24 h or more) | **Missing** | | A 24-hour run against `fakeyt` in containers, with memory, disk and queue depth watched | 1–2 days, plus the run |
| G4 CI guards against secrets and real data | **Met** | gitleaks (full history) and the fixture scanner | — | — |
| G5 Contract conformance tests | **Missing** | No contract yet | Run the master's conformance kit in CI | 0.5 day, after v1 |
| H1 Runbooks | **Partly** | `production-runbook.md` covers setup, start and stop, update, backups, erasure, the archive, webhooks and turning features off. Missing: roll back, per-alert pages, block storm, a YouTube layout change, backfill, replay | Write the missing pages | 1–2 days |
| H2 Backups with a tested restore | **Partly** | Daily `pg_dump`, 30 days kept, offsite steps in the runbook. The restore is documented, not tested | A CI job that restores a dump into a fresh database. On the platform: managed Postgres with point-in-time recovery | 0.5 day |
| H3 Go-live checklist and canary | **Partly** | The runbook's setup steps; "start with the Phase 0 channel list". No written checklist or ramp | The 48-hour pilot plan (Plans, 5), then a written ramp | 0.5 day |
| H4 Cost report per 1,000 accounts | **Partly** | The capacity planner (supply, demand, recommended IPs, hourly); estimates in `yts-operations.md` §13. Nothing measured on the production collector yet | From the pilot report (D5) | In D5 |
| I1 Least privilege | **Partly** | One database role owns everything. The database isn't published to the internet in Compose. One API key | Separate roles: migrator, app, read-only | 0.5 day |
| I2 Network policies | **Missing** | No Kubernetes yet. The egress list is in `yts-operations.md` §6 | A NetworkPolicy in the chart | 0.5 day |
| I3 Dependencies current, images scanned | **Partly** | Go 1.27.3 and current modules. The base image is pinned by tag, not digest. No image scan | Pin by digest; a Trivy job in CI | 0.5 day |
| I4 API keys per client, scopes | **N/A** | yts has no client API. Its admin is a local command line. Webhook payloads can be pseudonymized | — | — |

**Before production:** about 2–3 weeks for the Kafka work (E1–E5), plus about 2–3 weeks for the rest.

**Found since, while answering the master session (REQ-004, 6 November):**

| Gap | Evidence | Fix | Effort |
|---|---|---|---|
| Data first found through the official API can outlive the API's 30-day rule | A `videos` row with source `api_search` keeps the API's title and time if its scraped read never succeeds. A candidate found only by the API keeps its channel ID if it isn't checked within 30 days | A daily job: clear (or delete) those values after 30 days | 0.5 day |
| The production binary links the AI SDK | `ytcollect` imports `internal/phase0`, which holds the Phase 0 labelling command. It is never called and never reads a key | Move the labelling code into a package only `ytprobe` imports | 0.5 day |

## Plans

### 1. Kafka output

The full plan is in `yts-contract-notes.md` §1.

- **The approach:** a transactional outbox written in the same transaction as every content write, and a relay (`ytcollect relay`)
  that publishes the yt platform events and the mapped `content.*` events in one Kafka transaction. It is additive only: a new outbox
  table and `version` columns.
- **Topics:**
  - retention 7 days for the platform and content topics, as fbs does, or what the master sets;
  - any topic carrying official-API fields at most 30 days;
  - partitions: by key (comments by video or post).
- **Order:**
  - yt protos and the outbox can start now;
  - the `content.*` mapping waits for the master's v1;
  - nothing is registered in a shared Schema Registry before v1.
- **Effort:** 2–3 weeks.

### 2. Memory limits on the shared server

**Today:** `ytprobe-detect` runs without a limit, and so do the owner's `ytprobe` jobs. Nothing yts runs there has a browser.

1. **Measure first (read-only).** While tonight's census runs, `docker stats --no-stream ytprobe-census`, and the same for
   `ytprobe-detect`. That gives real numbers instead of guesses.
2. **Stop `ytprobe-detect` once the final Phase 0 report is printed.** Its measurement (E8) is done. Stopping it frees the shared server
   and fbs's proxies. Then nothing of yts runs there for long.
3. **Cap any later one-off job.** Add to every `docker run` of `ytprobe`:
   - `--memory 512m --memory-swap 512m` (no swap), so a runaway process is killed alone;
   - `--cpus 1`;
   - `--pids-limit 256`.

   512 MB is a provisional cap. It will be set to twice the measured peak once step 1 has numbers.
4. **If the owner wants detection to keep running,** cap it live without a restart:
   `docker update --memory 256m --memory-swap 256m --cpus 0.5 ytprobe-detect`. This changes a container on the shared server, so it is
   only done with the owner's approval.

### 3. Media: what analysis really needs from YouTube

| Media | Possible within the rules? | Use for analysis | Size | Cost |
|---|---|---|---|---|
| **Thumbnails** (`hqdefault`, 480×360 JPEG) | Yes: public CDN, no tokens | Thumbnails often carry the headline as text: OCR in Arabic and Kurdish, image classification, brand logos | About 15–40 KB each (estimate, not measured); `maxresdefault` 1280×720 about 100–250 KB, if OCR needs more | 1,000 channels publish about 300–1,000 videos a day: 10–40 MB a day. Trivial, and deleted after analysis |
| **Community post images** | Yes: public CDN | OCR, image classification | About 50–200 KB each (estimate) | Small. Not saved today: needs a change to post parsing |
| **Avatars, banners** | Yes | Entity linking (the same picture on several platforms: an image hash) | About 10–100 KB | Small. Keep the hash, delete the image after analysis |
| **Captions, transcripts** | **No.** Phase 0 E12: no text without an anti-bot token. The Data API's `captions.download` needs the video owner's authorization | | | |
| **Audio** | **No.** Needs the video stream, which needs anti-bot tokens | | | |
| **Full video** | **No**, for the same reason. It would also cost too much: a 10-minute 360p video is about 30–50 MB, about 40 GB a day at 1,000 uploads a day | | | |

**Recommendation:** thumbnails for every new video, and community post images if analysis wants OCR on them. They are kept only until
analysis acknowledges them (R5). Titles, descriptions, tags and comments carry the rest of the text.

**What changes in yts:**
- the media job publishes `content.media` and `content.media.ready`;
- a janitor deletes the file after `content.media.processed`, as fbs does;
- post images are added;
- the 7-day thumbnail re-check stays, so a changed thumbnail is analysed again.

**Effort:** 1–2 days, after the contract.

### 4. Discovery: the shared keyword list and the hand-off

**What yts has:**
- **Keywords:** in its own tables, so the owner can change them at any time with live reload. Kinds: place, person, organisation,
  topic, brand, hashtag, term. Spellings per language. An "ambiguous" flag (needs a second keyword), a priority, and a "searched" flag.
- **Seeds:** Iraq, every governorate and key cities (`internal/keywords/seed_iraq.tsv`), plus the Kurdish-language rule (D28).
- **Searches:** on YouTube's website search (priority keywords hourly, the others every 6 hours, newest first) and on the Data API.
- **Data API:** only priority keywords, every 3 hours, with an exact `publishedAfter` window. The quota allows about 95 searches a day.

**How the shared list fits:**
1. **One source of truth.** A `ytcollect keywords sync -file iraq.yaml` command imports fbs's `configs/discovery/iraq.yaml`. Shared
   entries are marked `source = shared`; yts's own flags (ambiguous, priority, searched) stay as overrides. I haven't seen that file's
   format: I need its layout from the master or the fbs session. Effort: 1–2 days.
2. **"The last 6 hours" (R8).** The API searches use `publishedAfter = now − 6 h`, sorted by date, for the priority keywords. The
   website search, sorted by upload date, covers every spelling. The quota decides which keywords are priority. 95 searches a day is
   about 12 keywords every 3 hours, or 24 every 6 hours.
3. **Hand-off (`discovery.candidate.v1`):**
   - **yts publishes** one for every channel it finds: channel ID, when found, the source (API search, website search, commenter,
     graph), and as evidence the keyword ID and the video ID, never the API's title (the 30-day rule). Then the outcome: core, mention,
     no match or already listed.
   - **yts also publishes candidates for the other platforms,** from the links in YouTube About panels (facebook.com, instagram.com,
     tiktok.com links), with the source "youtube profile link". That's one answer to Q11, where new Facebook pages come from.
   - **yts consumes** candidates for `yt` from other platforms (YouTube links in Facebook, Instagram and TikTok bios). It adds them to
     `channel_inputs` and runs its candidate check.
   - Effort: 2–3 days, after the contract.

### 5. Phase 0 results, and the 48-hour pilot

**Phase 0 results.** From the report of 4 November, 12:08 (Iraq time). The 5 November census follows tonight, and its numbers will be
added to `docs/phase0-results.md`.

| Experiment | Result |
|---|---|
| E1 safe pace (ISP IPs) | Clean down to 1 request per second per IP on browse, next, search, RSS, watch and the image CDN (420 requests each, no blocks). **The player was blocked even at one per 10 s.** Production: 2 s, player sparingly |
| E2 failover | Only the player was ever blocked. 18% of blocked requests finished on another IP; IPs worked again after a median of 120 minutes |
| E3 fields | Channel fields 89–100%; video fields in `yts-field-inventory.md` |
| E4 accuracy against the official API | Views and likes: median difference 0% (159 videos); likes exact in 100% |
| E5 cost | A year of a large channel's listings: median 378 requests; 3.6 requests per 100 uploads; 57.3 KB compressed per request |
| E6 comments | 12,683 comments; 30 requests per 100 comments; completeness median 100%; no video hit the cap |
| E8 detection | RSS median 19 minutes after publishing (p90 311). **42% of feed checks answered "not found"**: global outages of about 9 hours. The tab fallback was built after this |
| E9 discovery | Website search found 838 channels, the API 512. New channels: 60% only by the website, 33% only by the API, 7% by both. 2,000 API units per run |
| E10 census (500 channels) | 52% Iraq channels, 48% mention only; median 3 uploads a month (p90 about 65); median 13 comments on the newest video |
| E11 keyword quality | Precision 99%, estimated recall 92%; the hand spot check agreed with the AI labels on 25 of 27 |
| E12 transcripts | No-go: dropped |
| Blocks overall | 1.2 per 1,000 requests, all on the player |

**When the production collector can run a 48-hour pilot:**

1. **Decisions first** (the owner, with the master):
   - **Where:**
     - **(a) The shared pilot server.** Its disk is 88% full (about 3.3 GB free on 5 November), and it also runs other services (fbs, igs). It would
       need the owner's approval, a small Postgres (`shared_buffers` 256 MB, not the 8 GB in the Compose file), memory caps on every
       container (about 1.5 GB in total), and disk freed first.
     - **(b) The platform's staging cluster,** once it exists. I recommend (b), or (a) only for a small set of channels.
   - **Which proxies:** fbs's 10 ISP IPs, shared and agreed with the fbs session, or yts's own.
   - **With or without Kafka:** the pilot can measure the collector alone (requests, blocks, completeness, freshness, database growth,
     memory), with Kafka following later. Or it can wait for the relay, to prove the pipeline end to end.
2. **Code before the pilot** (about 3–4 days, draft PRs):
   - health endpoints (B1);
   - memory limits and small-server settings in the Compose file (B2);
   - attempt caps and dead letters (C1);
   - proxy bytes metric (F1);
   - `ytcollect report` (D5).
3. **The pilot:**
   - **Channels:** 50–100 from the Phase 0 list, mixed tiers, with the backfill limited to 7 days (as fbs did) so it measures steady
     state.
   - **Watched:** requests and GB per channel per day, blocks, comment completeness, detection delay, database growth, memory and CPU.
   - **Output:** the pilot report, projected to 1,000 and 10,000 channels.

**Earliest start:** about one week after the owner approves the plan and the place. If the master also wants Kafka in the pilot: after
the relay, about 3–4 weeks.
