# yts status

The current state of the YouTube collector, for the owner and the master session. Updated **6 November 2026, 19:20 (Iraq time)**.

## Where it stands

- **Built and merged:** every production step of the plan (1–20). That is the collector, comments, posts and playlists, discovery,
  capacity planning, privacy tools (dormant), images, the archive, webhooks, and the Docker Compose deployment, all with tests. See
  [plan.md](plan.md) §10.
- **Not running anywhere:** the production collector (`ytcollect`). It has only run against the fake YouTube in tests.
- **Phase 0:** has ended (the owner, 6 November). The census of 5 November was not run: there are no new long runs on the shared
  server (D-026). The final numbers come from the 4 November report and the 5 November discovery, into `docs/phase0-results.md`.
- **Now building:** the owner's fix list (REQ-010) and the contract adoption (REQ-021), approved on 6 November. Progress, one row per
  fix: [`master/yts-fixes.md`](master/yts-fixes.md).
  - **Done:** every fix before the pilot: 1 to 10, 13 (non-root image), 14 (Helm chart), 16 (pinned bases, image scan, releases)
    and 17 (database TLS and roles), plus the proxy `country=` tag and PostgreSQL 18.
  - **Next:** the pilot (fix 11) when the platform is ready (below); the outbox and relay (fix 12) alongside it.
- **Legal sign-off:** received (D29). Comments and all their public data are collected.

## Ready for the pilot (fix 11)

yts's side is ready on `main` at `edab872` (6 November):

| Item | Value |
|---|---|
| Chart | `deploy/helm/yts`, values `deploy/helm/yts/values-production.yaml` ([README](../deploy/helm/yts/README.md)) |
| Image | Built from the repository's `Dockerfile` (bases pinned by digest) with `--build-arg VERSION=<tag> --build-arg REVISION=<commit>`, or published by pushing a `v*` tag (`ghcr.io/<owner>/yts:<tag>`) |
| Helm image keys | `image.repository`, and `image.digest` (preferred) or `image.tag` |
| Secrets it mounts | `platform-credentials` (`POSTGRES_*`), `proxies` (`proxies.yaml`), `yts` (`privacy_key`, made by the owner from the README) |
| Pilot settings | One worker, 2 s per proxy IP, 10 jobs at once, a 7-day backfill; the cost report with `ytcollect report` |

**Still needed before it starts:** the cluster and namespace; the storage values (endpoint, region, bucket names, credential keys);
the proxies secret; the owner's `yts` secret; optionally the network ranges and the Prometheus selector labels. The owner starts and
stops the pilot; I give the commands, one per step, and write `docs/master/yts-pilot-report.md`.

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

**Answered by the master (6 November):**
- the database `yts`, with the roles `yts`, `yts_app` and `yts_ro`;
- credentials arrive as files from the secret `platform-credentials`;
- proxies arrive in the secret `proxies`, key `proxies.yaml`, one line per proxy with `country=`;
- raw responses go under `yts/keep/` and `yts/30d/` in the raw bucket. The platform expires `yts/30d/`; yts never deletes.

**Questions for the master (fix 14, 6 November):** the values `values-production.yaml` still needs.
- Object storage: the endpoint, the region, the media and raw bucket names, and the keys of the access and secret key in
  `platform-credentials` (the chart assumes `S3_ACCESS_KEY_ID` and `S3_SECRET_ACCESS_KEY`). Storage stays off until then.
- The address ranges of the database and the proxy exits, to narrow the network policy (open ranges until then).
- The Prometheus Operator's selector labels for the ServiceMonitor and PrometheusRule, if any.
- The namespace (the chart's docs use `yts`) and who creates yts's own secret `yts` (the privacy key): the owner, by the chart's
  README.

**Questions for the master (fix 17, 6 November):**
- Does `platform-credentials` carry the database server's CA? Under which key? yts reads `POSTGRES_SSLROOTCERT`; with it, yts
  verifies the server (`verify-full`), without it, it still encrypts (`require`).
- Does `POSTGRES_USER` own the `yts` database's `public` schema? `migrate` creates the tables as that role.

**Changes to report (additions only):**
- Fix 16: `ytcollect version`, the metric `yt_build_info`, OCI labels on the image, and a release workflow that publishes
  `ghcr.io/<owner>/yts` only when a `v*` tag is pushed.
- Fix 14: the Helm chart; a storage `prefix` setting (media under `yts/`); a `runbook_url` on every alert.
- Fix 17: `YT_POSTGRES_DIR` (the platform's credential files). Each command connects as the owner, app or read-only role; `migrate`
  grants the app and read-only roles. Without TLS, a connection to another machine is refused. Compose is unchanged.
- Fix 13: the image runs as user 65532, not root. Secret files must be readable by 65532: owned by it on a server, or through the
  pod's fsGroup on Kubernetes. The Compose runbook has a new `chown` step. Phase 0 images already built are unaffected.
- Fix 8 adds the worker settings `YT_RAW_STORAGE_FILE`, `-raw-sample` and `-raw-30d`, and the metrics `yt_raw_objects_total` and
  `yt_raw_bytes_total`. `parse_failures.raw_ref` is now filled. Raw files hold what the page shows, personal data included, and
  erasure doesn't reach them.
- Fix 5 adds three tables (`usage_hourly`, `usage_channel_daily`, `db_sizes`), the command
`ytcollect report`, the worker flag `-backfill-days`, and a `backfill_days` field in the workers' capacity record. Nothing was renamed
or removed. The outbox migration moves to `0012`.

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
