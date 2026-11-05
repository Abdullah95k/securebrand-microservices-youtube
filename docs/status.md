# yts status

The current state of the YouTube collector, for the owner and the master session. Updated **6 November 2026, 02:15 (Iraq time)**.

## Where it stands

- **Built and merged:** every production step of the plan (1–20). That is the collector, comments, posts and playlists, discovery,
  capacity planning, privacy tools (dormant), images, the archive, webhooks, and the Docker Compose deployment, all with tests. See
  [plan.md](plan.md) §10.
- **Not running anywhere:** the production collector (`ytcollect`). It has only run against the fake YouTube in tests.
- **Phase 0:** ends with the 5 November discovery and census (D30). The results are in the Phase 0 report of 4 November. The final
  numbers go into `docs/phase0-results.md` after the census.
- **Legal sign-off:** received (D29). Comments and all their public data are collected.

## What runs where

| Where | What | Since |
|---|---|---|
| Shared pilot server (`201.79.36.209`) | `ytprobe-detect` (Phase 0 new-upload detection, RSS every 15 minutes for 327 channels). No memory limit | 2 November; restarted 4 November |
| Shared pilot server | Owner-run `ytprobe` jobs (`discover`, `census`), about an hour in total | Last run 5 November |
| Proxies | The 10 ISP IPs of fbs's pilot, shared with fbs | 2 November |
| Anything else | Nothing | |

## Open decisions (owner and master)

1. **Where yts production and its 48-hour pilot run:** the platform's Kubernetes (hosting, Q4) or the shared pilot server.
   **No server should be bought for yts before this.**
2. **Proxies for yts production:** keep sharing fbs's, or buy its own.
3. **Kafka output:** the outbox plan in the reports, waiting for the shared contract v1. Nothing is registered in a shared Schema
   Registry before the master publishes v1.
4. **Media for analysis:** thumbnails (and community post images), kept until analysed. No audio, video or transcripts: they need
   anti-bot tokens.

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
