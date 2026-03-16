# CLAUDE.md — discord-webhook-queue

## What this is

A Discord webhook queue daemon written in Go. It acts as a drop-in proxy: senders swap the Discord hostname for this daemon's hostname, and the daemon queues messages in SQLite and forwards them to Discord with automatic retry and rate-limit handling. Primary use case: homelab tools (bash scripts via `discord.sh`, Grafana alerts) that can't tolerate dropped messages during Discord outages or concurrent rate limiting.

## Build and run workflow

**Never run `go build`, `go test`, or raw `docker build` commands directly.** Always use the Makefile:

```bash
make build-dev   # local dev (linux/arm64 on ARM Mac)
make build-prod  # production cross-compile (linux/amd64)
make run         # build + run locally on port 8080 with a named volume
make mod-tidy    # regenerate go.sum (first time after clone only)
make clean       # remove images and data volume
```

`make run` expects a `.env` file in the project root for environment variables.

The build uses Docker multi-stage builds. No local Go installation is required.

## Architecture

```
main.go                          # wires everything together, graceful shutdown
internal/
  config/config.go               # env var loading and validation
  server/server.go               # HTTP server, routes, logging middleware
  store/store.go                 # SQLite abstraction (WAL mode)
  delivery/delivery.go           # delivery engine goroutine, rate-limit state machine
  alert/alert.go                 # SMTP alerting on sustained failure
  metrics/metrics.go             # Prometheus metrics registration
```

Startup sequence in `main.go`:
1. Load config from env vars
2. Open SQLite, reset any `in_flight` rows to `pending` (crash recovery)
3. Create metrics, alerter, delivery engine, HTTP server
4. Start delivery engine and HTTP server as goroutines
5. Block on SIGTERM/SIGINT
6. Graceful shutdown: stop HTTP first (15s timeout), then cancel delivery engine context

## HTTP endpoints

| Method | Path | Auth-gated | Description |
|--------|------|------------|-------------|
| `GET` | `/` | No | Plain-text endpoint listing |
| `POST` | `/webhooks/{id}/{token}` | **Never** | Ingest — accepts `application/json` and `multipart/form-data` |
| `GET` | `/status` | Optional | JSON: state, queue_depth, last_failure_at |
| `GET` | `/queue` | Optional | JSON array of queued messages; token and payload omitted; empty queue returns `[]` |
| `GET` | `/metrics` | Optional | Prometheus exposition format |
| `POST` | `/alert/test` | Optional | Send a test SMTP alert |
| `DELETE` | `/queue/{id}` | Optional | Remove a specific message by ID; `404` if not found or in_flight |
| `DELETE` | `/queue` | Optional | Remove all non-in_flight messages; returns `{"deleted": N}` |

Auth is a static token in a configurable header (`X-Auth-Token` by default). The ingest endpoint is **never** auth-gated — senders like `discord.sh` and Grafana cannot inject custom headers.

## Delivery state machine

Two states in `delivery/delivery.go`:
- **healthy**: deliver messages at full speed, honour `X-RateLimit-*` and `Retry-After` headers
- **probing**: send one message at a time; return to healthy on first success

State transitions are logged as `Warn`. The engine retries forever — messages are never dropped.

## HTTP request logging

All requests are logged by `loggingMiddleware` in `server/server.go`, which wraps the entire `ServeMux`. Two log lines per request:

```
INFO http request  method=POST path=/webhooks/... remote_addr=1.2.3.4:port
INFO http response status=204 duration=9ms
```

Handler-level logs (e.g., `message enqueued`) appear between these two lines in the natural execution order. The response line deliberately omits method/path to avoid looking like a second request.

## Key quirk: Expect header stripping

`discord.sh` sends `Expect: application/json`, which Go's HTTP server would reject with 417. The `expectStrippingListener` in `server/server.go` wraps the TCP listener and rewrites each connection's header stream before Go parses it, dropping any `Expect` value that isn't `100-continue`. This is invisible to handlers. When behind nginx, nginx strips these headers first so the workaround is redundant but harmless.

## Logging conventions

- Package: `log/slog` text handler, stdout only
- All log fields are structured key=value pairs
- Levels: `Error` for failures, `Warn` for state changes and startup anomalies, `Info` for normal operations
- No log files, no log rotation

## Configuration (environment variables)

| Variable | Default | Notes |
|----------|---------|-------|
| `LISTEN_ADDR` | `:8080` | |
| `DB_PATH` | `/data/queue.db` | Must be on a persistent volume |
| `RETRY_INTERVAL_SECONDS` | `30` | Enforced range: 5–300 |
| `AUTH_TOKEN` | _(disabled)_ | |
| `AUTH_HEADER` | `X-Auth-Token` | |
| `SMTP_HOST` | _(disabled)_ | All three SMTP_HOST/FROM/TO required to enable alerting |
| `SMTP_PORT` | `25` | |
| `SMTP_FROM` | | |
| `SMTP_TO` | | |
| `SMTP_STARTTLS` | `false` | |
| `SMTP_USERNAME` | _(none)_ | |
| `SMTP_PASSWORD` | _(none)_ | |
| `FAILURE_ALERT_AFTER_MINUTES` | `15` | Minutes of sustained failure before first alert; range 1–1440 |
| `ALERT_HOST_LABEL` | system hostname | Appears in alert email subjects |

Alert repeat interval is hardcoded at 24 hours and is not configurable.

## Deployment

- Runs as UID/GID `523:523` by default
- SQLite on a Docker named volume or bind mount at `/data`
- When behind nginx: set `client_max_body_size 20m` (Grafana image attachments), `proxy_read_timeout 120s`
- Production image: `mbentley/discord-webhook-queue` (linux/amd64)
- No `HEALTHCHECK` in Dockerfile by design

## Known gaps vs PLANNING.md

- PLANNING.md describes healthy-mode concurrency ("messages may be processed with concurrency for throughput") — the delivery engine currently processes sequentially. Given the expected volume (dozens/day), this is not a problem in practice.
