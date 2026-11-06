# yts on the platform's Kubernetes cluster

The Helm chart for the YouTube collector (REQ-010 fix 14). Docker Compose on one server stays documented in
[docs/production-runbook.md](../../../docs/production-runbook.md); this is the cluster. Times are Iraq time.

## What runs

| Object | What | Replicas |
|---|---|---|
| `yts-worker` Deployment | The collector: leases jobs, reads YouTube through the proxies | **1**, replaced on an update (`Recreate`): each worker paces every proxy IP itself, so two would double the rate (fix 15 lifts this) |
| `yts-scheduler` Deployment | Partitions, missing jobs, metrics, capacity, archive, webhooks | 1 (no leader election yet) |
| `yts-websub` Deployment | Push notifications | Off until a public HTTPS address exists (D23) |
| `yts-migrate` Job | The migrations, a hook before every install and upgrade, then the grants for the app and read-only roles | — |
| ServiceMonitor | Scrapes `/metrics` on the worker (`:9101`) and the scheduler (`:9102`) | — |
| PrometheusRule | The 19 alerts of `deploy/prometheus/alerts.yml`, each with a runbook link | — |
| NetworkPolicy | In: the monitoring namespace on the ops ports. Out: DNS; HTTPS; the managed PostgreSQL, Kafka and Valkey in the VPC (`10.20.0.0/16`) on the ports read from `platform-credentials` at install; the schema registry (namespace `platform`, `app: schema-registry`, port 8080); the proxy exits on the ports read from `proxies.yaml` at install | — |

Every pod runs as user and group 65532, never root, with a read-only root filesystem, no capabilities and no service-account
token. Probes: `/healthz` (alive) and `/readyz` (the database answers; for the worker, at least one proxy isn't quarantined).

## Secrets (files, never values in the chart)

| Secret | Keys | From |
|---|---|---|
| `platform-credentials` | `POSTGRES_HOST`, `POSTGRES_PORT`, `POSTGRES_DATABASE`, `POSTGRES_SSLMODE`, `POSTGRES_CA_CERT`, `POSTGRES_USER`/`_PASSWORD` (owner), `POSTGRES_APP_USER`/`_PASSWORD`, `POSTGRES_RO_USER`/`_PASSWORD`; `S3_ENDPOINT`, `S3_REGION`, `S3_ACCESS_KEY`, `S3_SECRET_KEY`, `S3_BUCKET_RAW`, `S3_BUCKET_MEDIA` | The platform |
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
helm upgrade --install yts deploy/helm/yts -n yts -f deploy/helm/yts/values-production.yaml --set image.repository=REGISTRY/yts --set image.digest=sha256:DIGEST
```

The migrations run first; the services start only if they succeed.

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

## Values

`values.yaml` lists every setting with its default; `values-production.yaml` overrides them for production.

| Value | Production | Meaning |
|---|---|---|
| `worker.interval` | `2s` | Gap between requests on one proxy IP |
| `worker.workers` | `10` | Jobs at the same time |
| `worker.backfillDays` | `7` (the pilot) | Days of each channel's past uploads; 365 after the pilot |
| `worker.drain` | `20s` | On shutdown, running jobs get this long; the grace period is 40 s |
| `worker.rawSample`, `worker.raw30d` | `0.01`, `true` | Raw responses kept forever, and the 30-day rest |
| `worker.commentCap`, `worker.player` | `20000`, `true` | Comments per first read; the player endpoint |
| `storage.media`, `storage.raw` | on, `fromPlatformCredentials` | The endpoint, region, buckets and keys from `platform-credentials`; media under `yts/` |
| `database.platformCredentials` | `true` | The platform's `POSTGRES_*` files, each command with its least role, over TLS |
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
