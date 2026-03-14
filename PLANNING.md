# discord-queue: Design & Planning

## Overview

A local Discord message queue daemon that acts as a drop-in replacement for Discord webhook URLs. Senders (bash scripts, Grafana, etc.) point at this daemon instead of Discord directly. The daemon queues messages durably and forwards them to the real Discord webhook URLs, handling rate limiting and retries transparently.

---

## Section 1: Use Case & Senders

- **Senders:** Bash scripts (e.g. using `discord.sh`), Grafana alerting, and other homelab tools
- **Network scope:** Both local and remote senders — daemon must be network-accessible
- **Concurrency:** Multiple senders may enqueue messages simultaneously
- **Message types:**
  - Plain text and basic embeds (most messages)
  - Multipart file uploads with `payload_json` (Grafana image renderer alerts)
- **Volume:** Dozens per day; hard ceiling of ~100/day; bursty around events
- **Discord targets:** Multiple channels, primarily one server; multi-server support desired
- **Auth method:** Discord webhook URLs only (no bot token)
- **Design intent:** Act as a transparent proxy — existing tools require no reconfiguration beyond changing the hostname in their webhook URL

---

## Section 2: Delivery Requirements

### Ordering
- Single global queue ordered by receipt timestamp (FIFO)
- All messages across all channels/webhooks share one ordered queue

### Deduplication
- None — every received message is delivered exactly once, no duplicate detection

### Retry & Health State

The daemon operates in one of two states:

**Healthy mode:**
- Deliver messages as fast as possible within Discord's rate limits
- Full concurrency/throughput

**Probing mode (entered on delivery failure):**
- Send one message at a time
- Wait for a successful delivery before resuming healthy mode
- Prevents hammering Discord during an outage

**Retry policy:**
- Retry forever — messages are never dropped
- Configurable interval between retry attempts (with enforced min/max bounds to prevent misconfiguration)
- A sensible default retry interval is provided out of the box

### Failure Alerting (SMTP)
- After a configurable duration of sustained failure, send a plain text email alert
- Not triggered on every retry — only after the failure threshold duration is exceeded
- Email content:
  - Subject includes a configurable identifier (tool name + host label) so it's clear where the alert originates
  - Body includes: how long the failure has been occurring, current pending queue depth, and instructions to check `/status`
- SMTP configuration:
  - Host, port, from address, to address — all configurable
  - STARTTLS: optional (configurable on/off)
  - Auth: optional username/password (omit env vars = no auth)

### Logging
- All send attempts logged (success and failure)
- Each log entry includes running retry count for the message
- Log to stdout only — no log files
- Format: human-readable (Go `log/slog` text handler)

### Durability
- Hard requirement: no messages lost on daemon restart
- Queue must be backed by durable persistent storage at all times

---

## Section 3: Technology Stack

| Concern | Decision |
|---|---|
| **Language** | Go |
| **Binary** | Single statically-linked binary |
| **Production target** | Linux amd64 |
| **Development/test target** | Linux arm64 (Mac M-series running Docker) |
| **Build toolchain** | Multi-stage Docker build with `docker buildx` — no native Go installation required on host |
| **Runtime** | Docker container |
| **Deployment scope** | Single host on home network |

### Rationale
- Go produces self-contained static binaries with no runtime dependencies
- Strong standard library for HTTP server/client and concurrency primitives
- Multi-stage Dockerfile: `golang` builder image → minimal final image containing only the binary
- Go cross-compilation is native and trivial — same Dockerfile for both architectures via `--platform` flag:
  - Local dev/test: `docker buildx build --platform linux/arm64 .`
  - Production: `docker buildx build --platform linux/amd64 .`

---

## Section 4: Storage & Persistence

| Concern | Decision |
|---|---|
| **Backend** | Embedded SQLite |
| **Driver** | `modernc.org/sqlite` (pure Go, no CGo, statically linkable) |
| **Durability mode** | WAL (Write-Ahead Logging) for crash-safe writes |
| **File path** | Configurable via env var; mounted as a Docker volume for persistence across restarts |
| **External dependencies** | None — fully self-contained |

### Rationale
- No external service dependency (Valkey/MariaDB ruled out for this reason)
- WAL mode provides crash-safe durability
- SQLite file is directly inspectable with the standard `sqlite3` CLI tool
- Trivially sufficient for the expected volume (dozens/day)
- Status endpoint queries SQLite directly

### Queue Schema (conceptual)
```
messages:
  id            INTEGER PRIMARY KEY AUTOINCREMENT
  received_at   DATETIME NOT NULL          -- ordering key
  webhook_id    TEXT NOT NULL              -- from URL path ({id})
  webhook_token TEXT NOT NULL             -- from URL path ({token})
  content_type  TEXT NOT NULL             -- application/json or multipart/form-data
  payload       BLOB NOT NULL             -- raw request body
  retry_count   INTEGER DEFAULT 0
  last_error    TEXT
  last_attempt  DATETIME
```

---

## Section 5: API Surface

### Ingestion Endpoint
```
POST /webhooks/{id}/{token}
```
- Mirrors the Discord webhook URL format exactly
- Senders change only the hostname — all other tooling is unchanged
- Accepts both:
  - `application/json` — standard Discord webhook payload
  - `multipart/form-data` — Grafana image uploads (`file` + `payload_json` fields)
- Daemon constructs the real Discord URL as `https://discord.com/api/webhooks/{id}/{token}`

### Status Endpoint
```
GET /status
```
Returns:
- Daemon health state (`healthy` or `probing`)
- Current queue depth (total pending messages)
- Last failure time/date
- (Optional) per-webhook pending counts

### Metrics Endpoint
```
GET /metrics
```
- Prometheus exposition format
- Scraped by Telegraf `inputs.prometheus` → pushed to VictoriaMetrics via `outputs.influxdb`
- Exposed metrics (at minimum):
  - `discord_queue_depth` — current number of pending messages
  - `discord_queue_messages_sent_total` — counter of successfully delivered messages
  - `discord_queue_messages_failed_total` — counter of failed delivery attempts
  - `discord_queue_retry_total` — counter of retries
  - `discord_queue_healthy` — gauge: 1 = healthy, 0 = probing

### Authentication
- Optional static token auth on all endpoints
- Configured via env var: if set, requests must include the token in a configurable HTTP header
- If env var is not set, all endpoints are open (trusted-network assumption)

---

## Section 6: Operational Concerns

### Logging
- Go standard library `log/slog` with text handler
- Output: stdout only
- Log every send attempt with outcome and retry count
- Log state transitions (healthy → probing, probing → healthy)
- Log SMTP alert sends

### Docker
- No `HEALTHCHECK` instruction in the Dockerfile
- Binary runs as the container entrypoint
- SQLite file path mounted as a volume

### Metrics
- Prometheus-compatible `/metrics` endpoint (see Section 5)
- Telegraf config snippet (for reference):
  ```toml
  [[inputs.prometheus]]
    urls = ["http://discord-queue:8080/metrics"]

  [[outputs.influxdb]]
    urls = ["http://victoriametrics:8428"]
  ```

### Configuration (Environment Variables)

| Variable | Required | Default | Description |
|---|---|---|---|
| `LISTEN_ADDR` | No | `:8080` | Address and port to listen on |
| `DB_PATH` | No | `/data/queue.db` | Path to SQLite database file |
| `RETRY_INTERVAL_SECONDS` | No | `30` | Seconds between delivery attempts (min: 5, max: 300) |
| `FAILURE_ALERT_AFTER_MINUTES` | No | `30` | Minutes of sustained failure before SMTP alert is sent |
| `AUTH_TOKEN` | No | _(unset = disabled)_ | Static token for endpoint authentication |
| `AUTH_HEADER` | No | `X-Auth-Token` | HTTP header name to check for auth token |
| `SMTP_HOST` | No* | — | SMTP server hostname (*required if alerting desired) |
| `SMTP_PORT` | No | `25` | SMTP server port |
| `SMTP_FROM` | No* | — | Sender email address |
| `SMTP_TO` | No* | — | Recipient email address |
| `SMTP_STARTTLS` | No | `false` | Enable STARTTLS (`true`/`false`) |
| `SMTP_USERNAME` | No | _(unset = no auth)_ | SMTP username (optional) |
| `SMTP_PASSWORD` | No | _(unset = no auth)_ | SMTP password (optional) |
| `ALERT_HOST_LABEL` | No | system hostname | Label used in email subject to identify this instance |

---

## Key Design Decisions Summary

1. **Drop-in webhook proxy** — no sender changes required beyond hostname
2. **Single global FIFO queue** — ordered by receipt time, backed by durable SQLite
3. **Two-state delivery engine** — healthy (full speed, rate-limit-aware) vs. probing (one at a time until recovery)
4. **Never drop messages** — retry forever, SMTP alert after sustained failure
5. **Self-contained binary** — no external runtime dependencies; SQLite embedded via pure-Go driver
6. **Docker container** — multi-stage build, SQLite on a named volume
7. **Prometheus metrics** — natural fit for Go; scraped by Telegraf into VictoriaMetrics
8. **Environment variable configuration** — simple, container-friendly
