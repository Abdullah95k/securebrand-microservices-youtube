# yts on the platform's Kubernetes cluster

The Helm chart for the YouTube collector (REQ-010 fix 14). Docker Compose on one server stays documented in
[docs/production-runbook.md](../../../docs/production-runbook.md); this is the cluster. Times are Iraq time.

## What runs

| Object | What | Replicas |
|---|---|---|
| `yts-worker` Deployment | The collector: leases jobs, reads YouTube through the proxies | **1**, replaced on an update (`Recreate`): each worker paces every proxy IP itself, so two would double the rate (fix 15 lifts this) |
| `yts-scheduler` Deployment | Partitions, missing jobs, metrics, capacity, archive, webhooks | 1 (no leader election yet) |
| `yts-websub` Deployment | Push notifications | Off until a public HTTPS address exists (D23) |
| `yts-relay` Deployment | Publishes the outbox to the platform's Kafka: the contract's `content.*` messages (checked) and yts's `yt.*` messages, one transaction per batch (fix 12) | Off until Kafka is there (the pilot runs without it); then **1**, `Recreate` (one sends at a time) |
| `yts-migrate` Job | The migrations, a hook before every install and upgrade, then the grants for the app and read-only roles | — |
| ServiceMonitor | Scrapes `/metrics` on the worker (`:9101`), the scheduler (`:9102`) and the relay (`:9103`) | — |
| PrometheusRule | The 23 alerts of `deploy/prometheus/alerts.yml`, each with a runbook link | — |
| NetworkPolicy | In: the monitoring namespace on the ops ports. Out: DNS; HTTPS; the managed PostgreSQL and Kafka in the VPC (`10.30.0.0/16`, London) on the ports read from `platform-credentials` at install, or given in the values (`5432`, `25073`) when the chart is rendered without the cluster (Argo CD); the schema registry (namespace `platform`, `app: schema-registry`, port 8080); the proxy exits on the ports read from `proxies.yaml` at install | — |

Every pod runs as user and group 65532, never root, with a read-only root filesystem, no capabilities and no service-account
token. Probes: `/healthz` (alive) and `/readyz` (the database answers; for the worker, at least one proxy isn't quarantined; for the
relay, Kafka answers too).

## Secrets (files, never values in the chart)

| Secret | Keys | From |
|---|---|---|
| `platform-credentials` | `POSTGRES_HOST`, `POSTGRES_PORT`, `POSTGRES_DATABASE`, `POSTGRES_SSLMODE`, `POSTGRES_CA_CERT`, `POSTGRES_USER`/`_PASSWORD` (owner), `POSTGRES_APP_USER`/`_PASSWORD`, `POSTGRES_RO_USER`/`_PASSWORD`; `S3_ENDPOINT`, `S3_REGION`, `S3_ACCESS_KEY`, `S3_SECRET_KEY`, `S3_BUCKET_RAW`, `S3_BUCKET_MEDIA`; for the relay `KAFKA_BROKERS`, `KAFKA_USER`, `KAFKA_PASSWORD`, `KAFKA_SASL_MECHANISM`, `KAFKA_TLS`, `KAFKA_CA_CERT` | The platform |
| `proxies` | `proxies.yaml`: one proxy per line, `scheme://user:password@host:port type=isp subnet=label country=XX` | The platform |
| `yts` | `privacy_key` (required), `youtube_api_key` (optional), `websub_secret` (only with push) | The owner, below |

All are mounted as files, mode `0440` with the pod's group (`fsGroup: 65532`), which the secrets loader accepts.

### Creating the `yts` secret (the owner, once)

From a new terminal on your computer, with `kubectl` pointed at the cluster. The key is made on your computer and goes straight
into the cluster; it is never typed or pasted anywhere.

1. Make the privacy key:

```bash
(umask 077; openssl rand -hex 32 > privacy_key)
```

2. Put it in the cluster:

```bash
kubectl -n yts create secret generic yts --from-file=privacy_key=privacy_key
```

3. Keep a copy off the cluster (a password manager), then delete the local file:

```bash
rm privacy_key
```

To add the YouTube Data API key later, save it in a file named `youtube_api_key` (mode 600), add it to the secret, then delete
the file and restart the worker:

```bash
kubectl -n yts patch secret yts --type merge -p "{\"data\":{\"youtube_api_key\":\"$(base64 -w0 youtube_api_key)\"}}"
```

```bash
rm youtube_api_key
```

```bash
kubectl -n yts rollout restart deploy/yts-worker
```

## Installing and updating

The platform's pipeline builds the image and runs (the image keys are `image.repository`, and `image.digest` or `image.tag`):

```bash
helm upgrade --install yts deploy/helm/yts -n yts -f deploy/helm/yts/values-production.yaml --set image.repository=REGISTRY/yts --set image.digest=sha256:DIGEST --timeout 60m
```

The migrations run first; the services start only if they succeed. When the shared database is full, the migrations wait for a free
connection for up to 45 minutes (`migrate.waitForSlot`), logging each wait, so the `--timeout` is 60 minutes. A wrong password, or any
other error, fails at once.

## Admin commands

They run in the scheduler's container. Each command picks its own database role. For example, the cost report:

```bash
kubectl -n yts exec deploy/yts-scheduler -- ytcollect report
```

The queue, and the dead jobs:

```bash
kubectl -n yts exec deploy/yts-scheduler -- ytcollect health
```

```bash
kubectl -n yts exec deploy/yts-scheduler -- ytcollect jobs dead
```

The events waiting for Kafka, and a check of them against the platform contract (it sends nothing):

```bash
kubectl -n yts exec deploy/yts-scheduler -- ytcollect outbox
```

```bash
kubectl -n yts exec deploy/yts-scheduler -- ytcollect outbox check
```

## Values

`values.yaml` lists every setting with its default; `values-production.yaml` overrides them for production.

| Value | Production | Meaning |
|---|---|---|
| `worker.interval` | `2s` | Gap between requests on one proxy IP |
| `worker.workers` | `10` | Jobs at the same time |
| `worker.backfillDays` | `7` (the pilot) | Days of each channel's past uploads; 365 after the pilot |
| `worker.drain` | `20s` | On shutdown, running jobs get this long; the grace period is 40 s |
| `worker.rawSample`, `worker.raw30d` | `0.01`, `true` | Raw responses kept forever, and the 30-day rest |
| `worker.mediaHold` | `[]` (production: `[author_avatar]`) | Image kinds not downloaded for now; their jobs stay queued |
| `worker.commentCap`, `worker.player` | `20000`, `true` | Comments per first read; the player endpoint |
| `scheduler.minStretch` | `1` | Refresh tiers C, D and M at least this many times less often (1 to 8); 1: only as much as the capacity planner needs to fit the IPs. New-upload detection and tiers A and B are never slowed |
| `scheduler.fbDiscovery.enabled`, `.url`, `.dailyLimit`, `.secret.name`, `.secret.key` | `false`, `""`, `200`, `""`, `api_key` | Facebook discovery (D-065): every 10 minutes the scheduler sends the Facebook links on listed channels' About panels and descriptions to the Facebook collector at `url`, each once, at most `dailyLimit` a day; the key is mounted from the Secret `secret.name` (key `secret.key`) |
| `networkPolicy.egress.fbDiscovery` | off | The Facebook collector's API when it runs in the cluster: `namespace`, `podLabels`, `port` (an https address outside it needs nothing) |
| `storage.media`, `storage.raw` | on, `fromPlatformCredentials` | The endpoint, region, buckets and keys from `platform-credentials`; media under `yts/` |
| `database.platformCredentials` | `true` | The platform's `POSTGRES_*` files, each command with its least role, over TLS |
| `relay.media.handoff`, `relay.media.kinds` | `false`, `[thumb, avatar, banner, post_image]` | Hand stored images of these kinds to analysis and delete them once analysed or after `relay.media.keep` (72h); `author_avatar` adds commenters' avatars |
| `relay.enabled`, `relay.canary` | `false`, `false` | The relay to Kafka: on once Kafka is there, with `canary` while the canary runs (D-012); `relay.batch` events per transaction (500) |
| `networkPolicy.egress` | the master's ranges | The VPC for the managed services, the schema registry, and the proxy exits (any address until they are bought; their ports from `proxies.yaml`) |
| `*.resources`, `*.goMemLimit` | as in `values.yaml` | Requests and limits; `GOMEMLIMIT` a little under the limit |

The chart refuses to render with more than one worker, a store switched on with a value missing, push switched on without its
address, or no image.

The database connection verifies the server: with `POSTGRES_CA_CERT`, the platform's `require` becomes `verify-full` (the
certificate against the CA, and the name against `POSTGRES_HOST`).

The platform's Prometheus picks up every ServiceMonitor and PrometheusRule, so no selector labels are needed; alerts go to the
owner's Telegram.

## Waiting for the platform

- The proxy exits' addresses, once they are bought, to narrow the proxy rule from any address to theirs.
