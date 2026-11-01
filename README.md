# SecureBrand YouTube collector

Collects public YouTube data about Iraq-relevant channels for SecureBrand brand monitoring: channels, videos, Shorts, live streams,
comments, community posts and playlists. It discovers new channels through keyword search.

- **Plan and decisions:** [docs/plan.md](docs/plan.md) (approved 1 November 2026).
- **Status:** Phase 0 measurement kit ready to run. Follow [docs/phase0-runbook.md](docs/phase0-runbook.md).

## Rules for everyone working in this repo

This repository is **public**:

- **Never commit secrets.** API keys, proxy lists and passwords live in files on the server. Programs read them from paths given in
  environment variables (`*_FILE`). Never log or paste them.
- **Never commit real data.** That includes captured responses, real channel or video IDs, comments, names, and your own monitoring
  keywords.
- **Test fixtures are generated.** Fake channel IDs start with `UCfake`. Real response *shapes* may be committed only as scrubbed skeletons
  (every value replaced by a type placeholder) after a person has reviewed them.
- **The scraper never logs in,** never solves CAPTCHAs, and never generates YouTube's anti-bot tokens (plan §3).

CI enforces the first three with two checks: `fixturescan` (this repo's scanner) and `gitleaks` (secrets in the full git history).

## Layout

| Path | What |
|---|---|
| `docs/plan.md` | The approved plan |
| `cmd/` | Programs (one folder per binary) |
| `internal/` | Shared packages, not importable from outside this module |
| `cmd/ytprobe/`, `internal/phase0/` | The Phase 0 measurement kit (`Dockerfile` builds it) |
| `internal/proxypool/` | Proxy IPs: pacing, cooldowns, failover, subnet health, pool brake (plan §3) |
| `internal/innertube/`, `internal/feeds/` | Logged-out YouTube website client and channel feeds |
| `internal/ytparse/` | Parsers for YouTube responses (English and Arabic display text) |
| `internal/keywords/` | Arabic/Kurdish-aware keyword matching and the seed list of Iraq place names |
| `internal/ytapi/` | Official Data API client (search, accuracy check) with a quota ledger |
| `internal/fakeyt/` | Fake YouTube for tests (generated data only) |
| `.github/workflows/ci.yml` | CI: format, vet, tests with the race detector, fixture scan, Docker build, gitleaks |

## Development

Requires Go 1.24.

Run all checks, the same ones CI runs except gitleaks:

```bash
make check
```

Format the code:

```bash
make fmt
```

Pull requests are small, opened as drafts, and merged when CI is green.
