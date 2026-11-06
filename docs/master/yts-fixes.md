# yts: fixes from REQ-010 and REQ-021

One row per fix, in the owner's order (REQ-010, 6 November 2026). Times are Iraq time. The owner approved fixes 13, 14, 16 and 17
before the pilot (11), because the cluster can't run yts without them.

| # | Fix | PR | Status | Evidence |
|---|---|---|---|---|
| 1 | `/healthz` and `/readyz` on the ops port (B1) | [#43](https://github.com/Abdullah95k/securebrand-microservices-youtube/pull/43) | Done, 6 Nov | `internal/ops` and its tests; the worker, scheduler and push receiver serve them; checked by hand against a local database |
| 2 | Memory limits and small-server settings in Compose (B2) | [#46](https://github.com/Abdullah95k/securebrand-microservices-youtube/pull/46) | Done, 6 Nov | Every container has `mem_limit` (no swap), `cpus` and `pids_limit`; about 12 GB in all for 4 vCPU / 16 GB; Postgres tuned to an 8 GB limit; `GOMEMLIMIT` on the Go services; a CI step fails if any service lacks a limit |
| 3 | Bounded job retries: `dead` state, metric, alert, `requeue` (C1, C3) | [#45](https://github.com/Abdullah95k/securebrand-microservices-youtube/pull/45) | Done, 6 Nov | Migration `0010_dead_jobs.sql`; `queue.Fail` counts item failures only (`worker.Systemic`), dead at `queue.MaxFailures`; `ytcollect jobs dead / requeue`; `yt_queue_dead`, `yt_jobs_dead_total`, alert `DeadJobs`; tests `TestDeadJobsAndRequeue`, `TestSystemic`, `deploy/prometheus/alerts_test.yml` |
| 4 | `yt_proxy_bytes_total` (F1) | [#45](https://github.com/Abdullah95k/securebrand-microservices-youtube/pull/45) | Done, 6 Nov | `Proxy.AddBytes` on every request (wire bytes, both ways); test `TestProxyCountsBytes` |
| 5 | `ytcollect report` (D5) | | To do | |
| 6 | Hand back leases on SIGTERM, within the grace period (B4) | [#43](https://github.com/Abdullah95k/securebrand-microservices-youtube/pull/43) | Done, 6 Nov | `worker.Drain`, `queue.ReleaseOwner`; tests `TestWorkerDrainLetsRunningJobsFinish`, `TestWorkerDrainInterruptsAndHandsBack`, `TestReleaseOwner` |
| 7 | Alert: too few healthy IPs (C5) | [#45](https://github.com/Abdullah95k/securebrand-microservices-youtube/pull/45) | Done, 6 Nov | `yt_proxies_healthy`, `yt_proxies_total`; alert `ProxiesHealthyLow` with a promtool unit test |
| 8 | Raw responses: failures and 1% kept forever, the rest 30 days (E8) | | To do | |
| 9 | Move the AI labelling code out of `ytcollect` | [#47](https://github.com/Abdullah95k/securebrand-microservices-youtube/pull/47) | Done, 6 Nov | The API client moved to `internal/ailabel/claude`, imported only by `ytprobe`; `go list -deps ./cmd/ytcollect` lists no AI SDK package (it listed 25), checked in CI |
| 10 | The official-API 30-day job | this PR | In review | `discovery.ExpireAPIData`, daily in the scheduler: API hits deleted after 30 days; the API title and time cleared from `api_search` videos never read by scraping; API-only candidate checks moved to the front at 20 days, removed (with their job) if still unchecked at 30. Test `TestExpireAPIData` |
| 11 | The 48-hour pilot on the production cluster | | Waiting for 13, 14, 16, 17 and the cluster | |
| 12 | Outbox and relay (E1–E5) | | To do | |
| 13 | Non-root image, read-only root (A2) | | To do (before the pilot) | |
| 14 | Helm chart (A3, F3, I2) | | To do (before the pilot) | |
| 15 | Scaling: scheduler leader election, pacing in Valkey (B6) | | To do | |
| 16 | Releases on GHCR, base pinned by digest, image scan (A1, I3) | | To do (before the pilot) | |
| 17 | Postgres over TLS, separate roles (I1) | | To do (before the pilot) | |
| 18 | Validate every flag and setting at start-up (A4) | | To do | |
| 19 | Fields: verified badge, members-only, age restriction, joined date, scheduled start, playability status | | To do | |
| 20 | History and links: post text and About links versioned, comment metrics, description links | | To do | |
| 21 | Post attachments: images, poll options, linked video | | To do | |
| 22 | Operations: runbooks, restore test, dashboard, migration check, visitor ID per IP, daily budget | | To do | |
| 23 | 24-hour soak against the fake YouTube (G3) | | To do | |

**Platform conventions** (from the master, 6 November):

| Item | PR | Status | Evidence |
|---|---|---|---|
| `country=XX` on proxy lines, for `exit_country` (REQ-021 rule 7) | [#44](https://github.com/Abdullah95k/securebrand-microservices-youtube/pull/44) | Done, 6 Nov | `proxypool.ParseList`, `Proxy.Country`; test `TestParseListCountry` |
| PostgreSQL 18 in CI and Compose (the cluster runs 18) | [#44](https://github.com/Abdullah95k/securebrand-microservices-youtube/pull/44) | Done, 6 Nov | `.github/workflows/ci.yml` and the Compose file use `postgres:18`; the whole test suite runs against it in CI |

**Migration numbers:** fix 3 took `0010` (`0010_dead_jobs.sql`), so the outbox of fix 12 will be `0011`. The content is as planned;
only the number moved.

The contract adoption (REQ-021) is tracked in `yts-contract-adoption.md`, which starts with fix 12.
