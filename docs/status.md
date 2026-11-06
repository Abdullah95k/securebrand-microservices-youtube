# yts status

The current state of the YouTube collector, for the owner and the master session. Updated **6 November 2026, 17:55 (Iraq time)**.

## Where it stands

- **Built and merged:** every production step of the plan (1–20). That is the collector, comments, posts and playlists, discovery,
  capacity planning, privacy tools (dormant), images, the archive, webhooks, and the Docker Compose deployment, all with tests. See
  [plan.md](plan.md) §10.
- **Not running anywhere:** the production collector (`ytcollect`). It has only run against the fake YouTube in tests.
- **Phase 0:** has ended (the owner, 6 November). The census of 5 November was not run: there are no new long runs on the shared
  server (D-026). The final numbers come from the 4 November report and the 5 November discovery, into `docs/phase0-results.md`.
- **Now building:** the owner's fix list (REQ-010) and the contract adoption (REQ-021), approved on 6 November. Progress, one row per
  fix: [`master/yts-fixes.md`](master/yts-fixes.md). Fixes 1 and 6 are in review.
- **Legal sign-off:** received (D29). Comments and all their public data are collected.

## What runs where

| Where | What | Since |
|---|---|---|
| Shared pilot server (`201.79.36.209`) | `ytprobe-detect` (Phase 0 new-upload detection, RSS every 15 minutes for 327 channels). No memory limit. **To be stopped by the owner** (its data files are kept) | 2 November; restarted 4 November |
| Shared pilot server | Owner-run `ytprobe` jobs (`discover`, `census`), about an hour in total | Last run 5 November |
| Proxies | The 10 ISP IPs of fbs's pilot, shared with fbs | 2 November |
| Anything else | Nothing | |

## Decisions (owner, 6 November)

1. **Production and the 48-hour pilot run on the platform's Kubernetes cluster** (D-012, D-026), once the owner confirms it is
   ready. Nothing is bought for yts; no new long runs on the shared server.
2. **Order:** fixes 13, 14, 16 and 17 (non-root image, Helm chart, releases, Postgres TLS) come before the pilot. The pilot runs
   without Kafka; discovery runs during it at low priority.
3. **Proxies:** the owner confirms with the purchase whether yts gets its own IPs, with a country for each.
4. **Media for analysis:** thumbnails and community-post images, kept until analysed. No audio, video or transcripts.
5. **The contract v1.0.0 is published** (D-016). The `content.*` mapping follows the outbox (fix 12).

**Waiting for the master:** the database yts gets in the cluster, who expires the 30-day raw files, the `proxies.yaml` layout, and
whether credentials arrive as environment variables or files (asked on 6 November).

## Reports for the master session (6 November)

In [`docs/master/`](master/):

| File | What |
|---|---|
| `yts-field-inventory.md`, `.yaml` | Every field yts collects |
| `yts-operations.md` | Components, deployment, proxies, resources, reliability |
| `yts-contract-notes.md` | The Kafka outbox plan, the archive and ClickHouse, the API terms. **REQ-004** answers at the end |
| `yts-gaps.md` | Gaps against the service standard, and the plans |
| `yts-contract-review.md` | **REQ-013**: the review of the contract draft v0.1 |

Waiting: `ytprobe-detect`'s memory (REQ-004 §5), from the owner's `docker stats` readings.

## Standing rules (from the owner, 5 November)

- Never rename or remove tables, fields, schemas or endpoints others use; additions are fine, and each is reported.
- Nothing heavy on the shared pilot server without the owner's approval. Don't restart or rebuild yts containers there without telling
  the owner.
- Propose, then build.
