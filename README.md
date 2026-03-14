# discord-queue

A local Discord webhook queue daemon. It acts as a drop-in replacement for Discord webhook URLs, accepting messages from any sender (scripts, Grafana, monitoring tools, etc.), storing them durably, and forwarding them to Discord with automatic retry and rate limit handling.

The problem it solves: when multiple tools send Discord notifications concurrently or during a Discord outage, messages get dropped or rate-limited with no recovery. This daemon queues everything and handles delivery transparently.

## How it works

Point your existing tools at the daemon instead of Discord. The URL format is identical:

```
# Before
https://discord.com/api/webhooks/{id}/{token}

# After
http://your-host:8080/webhooks/{id}/{token}
```

No changes to your senders are required beyond swapping the hostname. The daemon accepts both `application/json` and `multipart/form-data` payloads (Grafana image attachments work out of the box).

Messages are stored in an embedded SQLite database and survive daemon restarts. Delivery is fast when Discord is healthy. If Discord becomes unreachable, the daemon switches to probe mode — sending one message at a time until delivery succeeds — then resumes normal operation.

## Quick start

```bash
docker run -d \
  --name discord-queue \
  -p 8080:8080 \
  -v discord-queue-data:/data \
  mbentley/discord-queue
```

## Configuration

All configuration is via environment variables. All are optional.

| Variable | Default | Description |
|---|---|---|
| `LISTEN_ADDR` | `:8080` | Address and port to listen on |
| `DB_PATH` | `/data/queue.db` | Path to SQLite database file (should be on a named volume) |
| `RETRY_INTERVAL_SECONDS` | `30` | Seconds between delivery attempts in probe mode (min: 5, max: 300) |
| `AUTH_TOKEN` | _(disabled)_ | Static token required on `/status` and `/metrics` requests |
| `AUTH_HEADER` | `X-Auth-Token` | Header name checked for the auth token |
| `SMTP_HOST` | _(disabled)_ | SMTP server hostname; leave unset to disable email alerts |
| `SMTP_PORT` | `25` | SMTP server port |
| `SMTP_FROM` | | Sender address for alert emails |
| `SMTP_TO` | | Recipient address for alert emails |
| `SMTP_STARTTLS` | `false` | Enable STARTTLS (`true`/`false`) |
| `SMTP_USERNAME` | _(none)_ | SMTP username (omit for unauthenticated relay) |
| `SMTP_PASSWORD` | _(none)_ | SMTP password |
| `ALERT_HOST_LABEL` | system hostname | Identifier shown in alert email subjects |

Email alerts fire after 15 minutes of sustained delivery failure, then every 24 hours if the failure continues. The alert resets when delivery recovers.

### Docker Compose example

```yaml
services:
  discord-queue:
    image: mbentley/discord-queue
    ports:
      - "8080:8080"
    volumes:
      - queue-data:/data
    environment:
      SMTP_HOST: mail.example.com
      SMTP_FROM: discord-queue@example.com
      SMTP_TO: alerts@example.com
      ALERT_HOST_LABEL: my-server
    restart: unless-stopped

volumes:
  queue-data:
```

## Endpoints

| Endpoint | Description |
|---|---|
| `POST /webhooks/{id}/{token}` | Enqueue a Discord webhook message. Accepts `application/json` and `multipart/form-data`. Returns 204. |
| `GET /status` | Returns daemon state, queue depth, and last failure time as JSON. Always 200. |
| `GET /metrics` | Prometheus metrics. Scrape with Telegraf `inputs.prometheus` or any compatible collector. |

### Status response

```json
{
  "state": "healthy",
  "queue_depth": 0,
  "last_failure_at": null
}
```

`state` is either `healthy` (normal delivery) or `probing` (Discord unreachable, sending one message at a time).

### Metrics

| Metric | Type | Description |
|---|---|---|
| `discord_queue_depth` | gauge | Messages currently waiting in the queue |
| `discord_queue_messages_sent_total` | counter | Messages successfully delivered |
| `discord_queue_messages_failed_total` | counter | Failed delivery attempts |
| `discord_queue_retry_total` | counter | Total retries including rate-limit retries |
| `discord_queue_healthy` | gauge | `1` = healthy, `0` = probing |

## Building from source

Requires Docker with buildx. No local Go installation needed.

```bash
# Generate go.sum (first time only)
make mod-tidy

# Build and run locally (linux/arm64)
make run

# Build production image (linux/amd64)
make build-prod
```