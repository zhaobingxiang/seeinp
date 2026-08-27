# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project overview

seeinp (时遇) is a remote-access / reverse-proxy platform (frp-style) for exposing internal services. It is a **three-tier system** with a shared web frontend:

- **seeinpm** (`cmd/seeinpm`) — central management platform. Deploys to Linux, listens on `:999`. Serves the B-end web UI, REST API, WebSocket, and the seeinps control channel (TLS). Owns the SQLite DB, port pool, users/groups, and version management.
- **seeinps** (`cmd/seeinps`) — server-side proxy agent. Deploys to Linux, listens on `:65443` for its own B-end/API. **Actively dials out** to seeinpm:999 to establish a control channel, then accepts forwarded external connections to reach internal targets. No inbound port / public IP needed.
- **seeinpc** (`cmd/seeinpc`) — Windows client (VPN). Serves a local HTTP API on `127.0.0.1:65444` and a SOCKS5 proxy on `127.0.0.1:1080`; tunnels traffic through the seeinpm ops proxy.
- **seeinps-deployer** (`cmd/seeinps-deployer`) — Windows tool that deploys seeinps to a remote server over SSH/SFTP.

The web frontend (`web/`) is Vue 3 + TypeScript + Vite + Element Plus. It is built once and embedded into all three Go binaries via `go:embed` (`internal/embed/dist`), so the frontend is a single shared codebase with per-endpoint views (`web/src/views/pm/`, `web/src/views/ps/`).

**Authoritative docs live in `docs/`** (Chinese): `01-需求文档.md` (requirements), `02-通信协议规范.md` (control protocol), `03-API规范.md` (REST API), `04-数据存储规范.md` (SQLite schema), `05-project.config.md` (tech stack/conventions). When changing protocol or API behavior, read and update the corresponding doc — the project treats doc/implementation drift as a defect.

## Commands (see `Makefile`)

Go is required. All common tasks go through `make`:

```bash
make build            # build all four binaries for current platform → dist/<name>/
make build-pm / build-ps / build-pc / build-deployer   # single binary
make run-pm / run-ps / run-pc   # run a binary in dev (default conf/ templates)

make test             # go test ./...
make test-unit        # internal/... + test/protocol/...
make test-e2e         # test/e2e/...  (currently empty)
make vet
make lint             # golangci-lint (skips if not installed)

make web-dev          # Vite dev server
make web-build        # npm install + build → copies web/dist into internal/embed/dist for go:embed

make release          # full cross-compiled release → release/packages/*.zip
make clean
```

Run a single Go test directly: `go test ./internal/protocol/ -run TestName -v`.

Notes:
- Version info is injected via `-ldflags` (`internal/version`); `VERSION`/`COMMIT`/`BUILD_TIME` come from git, so set them explicitly when building outside a git checkout.
- The web frontend must be built (`make web-build`) before the backend binaries will serve real UI; otherwise `internal/embed/dist` is empty and the UI shows "Frontend not built".
- Cross-compilation uses `CGO_ENABLED=0` (pure-Go `modernc.org/sqlite`). seeinpm/seeinps target `linux/amd64`; seeinpc/deployer target `windows/amd64`.

## Architecture

### Control channel (seeinps ↔ seeinpm)

The core design is a single TLS long connection (seeinpm:999) multiplexed with **yamux** (`internal/mux`, wraps `hashicorp/yamux`) into logical streams. Control signaling and proxy data share the connection.

- **Protocol** (`internal/protocol`): messages are `4-byte big-endian length prefix + JSON payload`, with a common envelope `{type, id, version?, ts, code?, data?}`. Message types, error codes, and payload structs are all defined here. **This is the single source of truth — never copy protocol structs into other packages** (prevents protocol drift).
- **Handshake flow**: seeinps dials seeinpm → `HELLO` (version negotiation, major version must match) → `REGISTER` (auth code verification) → establish mux session → first stream is the control-signaling stream. After that, seeinpm forwards external connections as new mux streams; seeinps reads a stream header (`stype` byte + proxyID) and routes to the matching local proxy.
- **seeinpm side** (`cmd/seeinpm/control.go` + `listener.go`): a `sniffListener` wraps the TLS listener and distinguishes HTTP from control connections by peeking the first byte (ASCII uppercase method letter = HTTP, otherwise control protocol). `ControlServer` handles handshake, register, heartbeat, and message dispatch (alloc/release port, report, conn events).
- **seeinps side** (`cmd/seeinps/control.go`): `ControlClient` maintains the connection with exponential-backoff reconnect (1s→60s cap), a 10s heartbeat (35s timeout), and a request/response router keyed by message ID (`pending` map) so async responses (e.g. `ALLOC_PORT_RESP`) don't race the heartbeat reader.

### Data flow for a proxied connection

1. seeinps requests a port via `ALLOC_PORT` over the control stream.
2. seeinpm allocates from its port pool, records a proxy in SQLite, and replies with the public port.
3. An external client connects to seeinpm's public port → seeinpm opens a new yamux stream to seeinps carrying a stream header → seeinps finds the local proxy by proxyID and bidirectionally forwards to the internal target.
4. seeinps reports traffic/connection events back over the control stream; seeinpm aggregates them into SQLite.

### Backend structure

- Each binary is a `package main` under `cmd/`, with `main.go` (config load + signal handling) and `server.go`/`client.go` (the `Server`/`Client` struct holding all dependencies, registering routes via Go 1.22+ `http.ServeMux` method+path patterns like `"GET /api/v1/users/{id}"`).
- `internal/` shared libraries:
  - `config` — TOML config structs + load/validate per endpoint (seeinpm/seeinps/seeinpc). Defaults returned when file missing.
  - `store` — `modernc.org/sqlite` wrapper (`internal/store/store.go`) with PRAGMAs (WAL, foreign_keys), a versioned migration runner (`Migrate`), `WithTx`, online `Backup` (VACUUM INTO). PM and PS each have their own schema/migrations (`pm.go`/`pmigrate.go`, `ps.go`/`psmigrate.go`).
  - `auth` — JWT manager, bcrypt auth-code verification, DPAPI (Windows) for local config encryption.
  - `httpx` — shared HTTP middleware (`AuthMiddleware`, `Chain`).
  - `logx` — `log/slog`-based rotating JSON logger.
  - `mux`, `protocol`, `tlsutil`, `version`, `embed` (static frontend + gzip).

### Frontend

`web/` is a single Vue 3 app serving all three endpoints. `web/src/api/` has per-endpoint SDKs (`pm.ts`, `ps.ts`, `pc.ts`); `web/src/router/` has per-endpoint route trees (`pm-routes.ts`, `ps-routes.ts`). The `embed.StaticHandler(endpoint)` injects a `<meta name="seeinp-endpoint">` tag into `index.html` so the SPA redirects to the right login (`/pm/login` vs `/ps/login`). Build scripts run `vue-tsc --noEmit` type-check as part of `npm run build`.

## Conventions

- **Protocol changes must be mirrored in `docs/02-通信协议规范.md`** (and `docs/03-API规范.md` if the API changes). New/unknown message types must return error code `2001` (`CodeUnsupportedType`) rather than dropping the connection.
- Go code: `gofmt` + `golangci-lint` (goimports, errcheck, govet, staticcheck, etc.) must pass; propagate errors with `%w` context; all I/O takes a `context.Context`; never log passwords/auth codes/tokens.
- Commits use Conventional Commits (`feat:`, `fix:`, `docs:`, ...).
- The `scripts/` directory contains ad-hoc Python deploy/verify/fix scripts used during server deployment — these are operational tooling, not part of the Go build.
- Config files live in `conf/` (`seeinpm.toml`, `seeinps.toml`, `seeinpc.toml`); `*-test.toml` / `*-local.toml` variants are used for testing. Auth codes are read from files like `conf/authcode*`. Runtime data (SQLite DBs, logs, uploaded releases) is gitignored under `data/` and `logs/`.
