# yts: fixes from REQ-010 and REQ-021

One row per fix, in the owner's order (REQ-010, 6 November 2026). Times are Iraq time. The owner approved fixes 13, 14, 16 and 17
before the pilot (11), because the cluster can't run yts without them.

| # | Fix | PR | Status | Evidence |
|---|---|---|---|---|
| 1 | `/healthz` and `/readyz` on the ops port (B1) | this PR | In review | `internal/ops` and its tests; the worker, scheduler and push receiver serve them; checked by hand against a local database |
| 2 | Memory limits and small-server settings in Compose (B2) | | To do | |
| 3 | Bounded job retries: `dead` state, metric, alert, `requeue` (C1, C3) | | To do | |
| 4 | `yt_proxy_bytes_total` (F1) | | To do | |
| 5 | `ytcollect report` (D5) | | To do | |
| 6 | Hand back leases on SIGTERM, within the grace period (B4) | this PR | In review | `worker.Drain`, `queue.ReleaseOwner`; tests `TestWorkerDrainLetsRunningJobsFinish`, `TestWorkerDrainInterruptsAndHandsBack`, `TestReleaseOwner` |
| 7 | Alert: too few healthy IPs (C5) | | To do | |
| 8 | Raw responses: failures and 1% kept forever, the rest 30 days (E8) | | To do | |
| 9 | Move the AI labelling code out of `ytcollect` | | To do | |
| 10 | The official-API 30-day job | | To do | |
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

The contract adoption (REQ-021) is tracked in `yts-contract-adoption.md`, which starts with fix 12.
