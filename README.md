# Cairn

> A lightweight container image management platform — single process, single binary, with a built-in console. Implements the CNCF Distribution (Docker Registry HTTP API V2) protocol.

<p align="left"><img src="./docs/logo.svg" alt="Cairn logo" width="64" /></p>

**[English](README.md)** · [中文](./README_ZH.md)

Cairn is a **self-contained container image registry**: it implements the full Docker Registry V2 protocol and ships with a built-in management console. It is **one process, one binary, one registry** — start it where your images should live, and the same port serves the admin UI in a browser.

> 📘 **Want the full product tour?** See the [“What Is This?”](#what-is-this) section below — it covers “What it is / Design goals / Capability map / Single-binary architecture / Quick start / Configuration / Boundaries / Where to start next” in 8 sections. A running instance also ships with [`/cairn-intro.html`](./web/public/cairn-intro.html), reachable from the UI footer’s “Product Intro” link.

## What Is This?

Cairn is written from scratch in Go — a single binary with an embedded console, and zero external services (no database, no message queue, no Redis). It covers the full `docker push` / `docker pull` / `skopeo copy` roundtrip; day-to-day operations run through 6 built-in pages (Images / Pull / Credentials / Proxy / Settings).

## Design Goals

Four hard constraints:

1. **Single process, single binary**
   No runtime dependency on databases, message queues, or Redis. Only the Go standard library + chi + `golang.org/x/sync` are used, producing a ~10 MB binary.
2. **Frontend and backend share an origin**
   The React frontend is bundled in via `//go:embed` and embedded in the binary. The same port serves `/v2/*`, `/api/*`, and the UI — no CORS, no second deployment.
3. **Library-style backend**
   `cmd/server` only wires things up. All capability lives in `internal/` packages (registry / pull / stats / credentials / proxies / webhook), so behavioral changes have exactly one place to land.
4. **Observable**
   `/healthz` for liveness, `/readyz` for readiness, structured `slog` logs. The probe endpoints Compose and Kubernetes expect are built in.

## Capability Map

| Module | Status | Implementation notes |
|---|---|---|
| `/v2/*` Docker Registry V2 protocol | ✅ | `_catalog`, `tags/list`, manifests (4 `Accept` types), blobs and uploads (4 MiB chunked PATCH / PUT); full roundtrip verified with `docker push` / `docker pull` and direct `skopeo` connections |
| Local FS storage | ✅ | Registry content on the filesystem: `repos` / `blobs` / `uploads`; digest-verified with atomic writes; container-internal path is fixed at `/app/data/registry` — add a bind mount to put blobs on a dedicated disk |
| Catalog browsing & deletion | ✅ | Browse repos and tags with digest, layer count, size, and build time; deletion is digest-based and lists every tag pointing to that digest before proceeding |
| Pull queue | ✅ | Pull images from upstream registries: FIFO queue, single concurrent executor, cooperative cancellation; in-memory progress is bounded by a ring buffer |
| Credential vault (AES-256-GCM) | ✅ | Credentials are encrypted at rest (key from `REGISTRY_CREDENTIAL_KEY`); supports Bearer token flows for Docker Hub / ghcr.io / quay.io (including scope-less tokens) |
| Proxy library | ✅ | Separate table; reachability uses pure TCP probe + latency |
| Stats DB (SQLite) | ✅ | `modernc.org/sqlite` (pure Go, no cgo); aggregations by image and time window; data lives under `/app/data` |
| Webhook notifications | ✅ | Outbound POST events with HMAC-SHA256 signatures; receivers validate against a whitelist |
| Console (React + AntD) | ✅ | Images / Pull / Stats / Credentials / Proxy / Settings — 6 pages cover day-to-day operations |

## Single-Binary Architecture

```
                ┌───────────────────────────────────────────────┐
  docker /      │                  :8787                       │
  skopeo /      │  chi router + slog structured logging          │
  curl / browser│  /v2/* data plane + /api/* admin plane + UI   │
                │  (//go:embed bundled into the binary)         │
                └───────────────┬───────────────────────────────┘
                                │
                ┌───────────────▼───────────────────────────────┐
                │  internal/                                    │
                │  registry (protocol & storage) · pull (queue) │
                │  stats · credentials · proxies · webhook      │
                └───────────────┬───────────────────────────────┘
                                │
                ┌───────────────▼───────────────────────────────┐
                │  /app/data                                    │
                │  credentials.json · proxies.json · cairn.db · │
                │  registry/(repos + blobs + uploads)           │
                └───────────────────────────────────────────────┘
```

The container-internal listen port is a compile-time constant: `8787`. The externally reachable port is determined by Compose’s `HOST_PORT` (default `8787`).

## Quick Start

```bash
# 1. Prepare the host data directory (holds all state — credentials, SQLite, registry content)
mkdir -p /data/cairn

# 2. Copy .env.example and fill in the required values
cp .env.example .env
# Then edit .env, at minimum set:
#   REGISTRY_CREDENTIAL_KEY=<output of `openssl rand -hex 32`>
#   HOST_PORT=80           # host-side external port; falls back to 8787 if unset

# 3. Start
docker compose up -d --build

# 4. Verify
curl -s http://127.0.0.1:80/healthz
# Expected: HTTP 200 + "OK"

# 5. Open http://<host>:<HOST_PORT> in a browser
```

After startup, the container-internal listen port stays at `8787`; `HOST_PORT` only controls the host-side port mapping.

## Where Configuration Lives

**Infrastructure goes in `.env`; business configuration goes in Settings.** The two don’t collide — business env vars are not even read at runtime, so `docker inspect` won’t show them.

| Setting | Where | Notes |
|---|---|---|
| `REGISTRY_CREDENTIAL_KEY` | `.env` | **Required.** AES-256-GCM master key for the credential vault; **losing it makes stored credentials permanently unrecoverable** |
| `HOST_DATA_DIR` | `.env` | Host data directory; default `/data/cairn` |
| `HOST_PORT` | `.env` | Host-side port mapping; default `8787` |
| `CAIRN_ENV` | `.env` | `prod` (default) / `dev`; only affects log verbosity |
| `IMAGE` | `.env` | Running image tag; default `cairn:X.Y.Z` |
| `NODE_IMAGE` / `NPM_REGISTRY` / `GOPROXY` / `BUILD_HTTP_PROXY` / `BUILD_HTTPS_PROXY` / `BUILD_NO_PROXY` | `.env` | Only used during `docker compose build`; ignored at runtime |
| Registry address / proxy / auth / display name | Settings | Stored in SQLite; takes effect immediately |
| Feature switches (`allow.delete` / `allow.pull` / `allow.registry_events`) | Settings | Stored in SQLite; takes effect immediately |
| Notification token / retention days / ignored user agents | Settings | Stored in SQLite; takes effect immediately |

### Changing the Listen Port (`HOST_PORT`)

Cairn’s port is the **host mapping port** + **container-internal listen port** combined.

- **Host → container mapping**: `HOST_PORT` (default `8787`). This is Docker Compose’s job; cairn has no control over it.
- **Container-internal cairn listener**: `PORT` env var → `cfg.Port` (default `8787`, `Dockerfile` `EXPOSE 8787`). Fixed at boot; changes require restarting the process and updating the env.

**Steps to change**:

1. Edit `.env`: `HOST_PORT=8888` (replace `8787` with the host port you want)
2. `docker compose up -d` to recreate the container (if you only change `HOST_PORT`, cairn’s internal listen port stays at `8787`; if you also change the internal port, rebuild the image)
3. Browse to `http://<host>:8888`, `docker pull <host>:8888/...`

**Why no UI control?** UI changes to `cfg.Port` only change the container-internal port — they can’t change Docker’s port mapping, so browsers and the Docker daemon would still hit `80`/`8787`, making the UI change a no-op. Settings only displays the current port; the actual change lives in `.env` + `docker-compose.yml`.

## Boundaries

The following are **intentionally not done**, not “TBD”:

- ❌ Login / user accounts / RBAC
- ❌ Image scanning / CVE detection
- ❌ Image signing / cosign integration
- ❌ Multi-registry federation
- ❌ Quotas / rate limits
- ❌ Helm chart / OCI artifact browsing (Docker images only)

Data plane (`/v2/*`) auth status: `/v2/*` is currently anonymously readable; authentication is still on the roadmap — for production deployments, put it behind an internal network or reverse proxy, never expose it to the public Internet directly.

## Where to Start

1. Browse to `http://<host>:<HOST_PORT>` — defaults to the “Images” page
2. If the registry is empty, head to “Pull” first to add an upstream (Docker Hub / ghcr.io / quay.io, etc.) and fetch an image
3. Credentials, proxies, retention days, notification tokens, and similar items are saved in Settings and take effect immediately — no env editing required

`docs/design/*.html` carries the design and brand assets, `docs/ROADMAP.md` is the planning placeholder, and `CHANGELOG.md` records every release — all live in the source tree, not inside the running instance.

## Project Layout

```
.
├── cmd/server/                 # main entrypoint (main + wiring)
├── internal/
│   ├── config/                 # env loading
│   ├── registry/               # V2 protocol client (browse / delete / pull)
│   ├── registryd/              # built-in registry server (/v2/* routing + uploads)
│   ├── pull/                   # pull queue
│   ├── credentials/            # AES-256-GCM credential vault
│   ├── proxies/                # proxy library
│   ├── events/                 # webhook + stats
│   ├── api/                    # HTTP handlers (/api/*)
│   ├── storage/                # filesystem atomic writes + digest verification
│   ├── db/                     # SQLite wrapper (modernc, pure Go)
│   ├── server/                 # Build(cfg) → *http.Server
│   └── webui/                  # //go:embed frontend dist
├── web/                        # React + AntD + Vite frontend source
├── docs/
│   ├── design/                 # brand / UI / page prototypes
│   └── ROADMAP.md              # planning placeholder
├── Dockerfile                  # multi-stage build, scratch base
├── docker-compose.yml
├── Makefile                    # from-zero deployment + acceptance pipeline
└── .env.example
```

## Development

```bash
# Pull dependencies (first run / after go.mod changes)
go mod download

# Run (requires .env or environment variables)
go run ./cmd/server

# Build
go build -o cairn ./cmd/server

# Tests
go test ./...

# Containerize
docker build -t cairn:dev .
```

### Building in Restricted Networks (Go Proxy)

`go build` / `go mod download` default to `proxy.golang.org`, which times out in restricted networks. Two ways to override:

```bash
# 1) Override locally for the current shell
GOPROXY=https://goproxy.cn,direct go mod download
GOPROXY=https://goproxy.cn,direct go build -o cairn ./cmd/server

# 2) Override at build time via build-arg (affects `go mod download` inside the Dockerfile)
docker build --build-arg GOPROXY=https://goproxy.cn,direct -t cairn:dev .
# Or: set GOPROXY=https://goproxy.cn,direct in .env and `docker compose build --no-cache`
```

Common proxies: `https://goproxy.cn,direct` (China — Qiniu), `https://goproxy.io,direct` (China — official-recommended), `https://mirrors.aliyun.com/goproxy/,direct` (Aliyun).

## Putting Registry Content on Its Own Disk (Optional)

Not split by default. To put blobs/manifests on a separate volume, add a second bind mount under `volumes:` in `docker-compose.yml` (**no new variables**):

```yaml
volumes:
  - ${HOST_DATA_DIR:-/data/cairn}:/app/data
  - /data2/cairn-registry:/app/data/registry
```

## Versioning

The `MAJOR.MINOR.PATCH` rules live in [AGENTS.md §Versioning](./AGENTS.md#版本号规则).

The version must be updated in 5 places (missing any one causes drift):

1. The `Version` constant in `internal/version/version.go`
2. `image: ${IMAGE:-cairn:X.Y.Z}` in `docker-compose.yml`
3. `IMAGE=` in `.env.example`
4. Every `docker build/tag/push` example in `README.md`
5. A new section at the top of `CHANGELOG.md`

Current version: `0.7.52` (from `internal/version.Version`; exposed in runtime logs and `/api/config`).

## Documentation Index

- [`CHANGELOG.md`](./CHANGELOG.md) — notable changes for every release
- [`AGENTS.md`](./AGENTS.md) — project conventions (versioning rules / env convention / data directory / V2 protocol facts)
- [`CONTRIBUTING.md`](./CONTRIBUTING.md) — contribution guide (PR flow / `make gates` / commit message conventions)
- [`CODE_OF_CONDUCT.md`](./CODE_OF_CONDUCT.md) — code of conduct (Contributor Covenant 2.1)
- [`docs/ROADMAP.md`](./docs/ROADMAP.md) — planning placeholder
- [`docs/resilience.md`](./docs/resilience.md) — v0.5.x mid-range concurrency / deadlock / UI freeze survey (v0.5.18 resilience round planning doc)
- [`.github/workflows/ci.yml`](./.github/workflows/ci.yml) — GitHub Actions: `make gates` on push / PR
- [`.github/pull_request_template.md`](./.github/pull_request_template.md) — PR description template
- [`.github/ISSUE_TEMPLATE/`](./.github/ISSUE_TEMPLATE/) — bug report / feature request templates
- [`docs/design/cairn-brand.html`](./docs/design/cairn-brand.html) — brand assets (logo / palette)
- [`docs/design/cairn-ui-design.html`](./docs/design/cairn-ui-design.html) — UI design specs (tokens / components / states)
- [`docs/design/demo-A-*.html`](./docs/design/demo-A-overview.html) — 6 page prototypes
- [`web/public/cairn-intro.html`](./web/public/cairn-intro.html) — product intro page (shipped with the binary; reachable at `/cairn-intro.html` in a running instance)

## License

This project is licensed under the [Apache License 2.0](https://www.apache.org/licenses/LICENSE-2.0) — full terms in [`LICENSE`](./LICENSE); third-party attributions in [`NOTICE`](./NOTICE).

![License: Apache 2.0](https://img.shields.io/badge/license-Apache%202.0-blue.svg)

Key points:

- ✅ Commercial use, modification, and distribution allowed
- ✅ Companies may embed the Cairn binary in proprietary products
- ✅ Includes an explicit patent grant, reducing patent litigation risk
- ❌ No trademark license (the “Cairn” name requires separate authorization)
- ❌ No warranty provided (evaluate at your own risk)