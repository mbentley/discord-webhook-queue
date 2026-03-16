# discord-webhook-queue

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

The image runs as UID/GID `523:523` by default. Before starting the container, create the data directory on the host and set ownership to match:

```bash
mkdir -p /path/to/data
chown 523:523 /path/to/data
```

Then start the container with a bind mount:

```bash
docker run -d \
  --name discord-webhook-queue \
  -p 8080:8080 \
  -v /path/to/data:/data \
  mbentley/discord-webhook-queue
```

### Running as a different user

Override the default user at runtime with `--user`. Set the host directory ownership to match:

```bash
mkdir -p /path/to/data
chown 1000:1000 /path/to/data

docker run -d \
  --name discord-webhook-queue \
  -p 8080:8080 \
  -v /path/to/data:/data \
  --user 1000:1000 \
  mbentley/discord-webhook-queue
```

### Using a named volume

Named volumes are also supported. Docker creates the volume directory as root, so you will need to set ownership before the daemon starts or the database write will fail. One way to do this is with a one-off container:

```bash
docker volume create discord-webhook-queue-data
docker run --rm \
  -v discord-webhook-queue-data:/data \
  --user root \
  mbentley/discord-webhook-queue \
  chown 523:523 /data

docker run -d \
  --name discord-webhook-queue \
  -p 8080:8080 \
  -v discord-webhook-queue-data:/data \
  mbentley/discord-webhook-queue
```

## Configuration

All configuration is via environment variables. All are optional.

| Variable | Default | Description |
|---|---|---|
| `LISTEN_ADDR` | `:8080` | Address and port to listen on |
| `DB_PATH` | `/data/queue.db` | Path to SQLite database file (must be on a persistent bind mount or volume) |
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

Create and chown the data directory before starting:

```bash
mkdir -p /path/to/data
chown 523:523 /path/to/data
```

```yaml
services:
  discord-webhook-queue:
    image: mbentley/discord-webhook-queue
    user: "523:523"
    ports:
      - "8080:8080"
    volumes:
      - /path/to/data:/data
    environment:
      SMTP_HOST: mail.example.com
      SMTP_FROM: discord-webhook-queue@example.com
      SMTP_TO: alerts@example.com
      ALERT_HOST_LABEL: my-server
    restart: unless-stopped
```

## Endpoints

| Endpoint | Description |
|---|---|
| `GET /` | Info page listing available endpoints. |
| `POST /webhooks/{id}/{token}` | Enqueue a Discord webhook message. Accepts `application/json` and `multipart/form-data`. Returns 204. |
| `GET /status` | Returns daemon state, queue depth, and last failure time as JSON. Always 200. |
| `GET /queue` | Returns all queued messages as a JSON array. Webhook tokens and payloads are omitted. Empty queue returns `[]`. |
| `GET /metrics` | Prometheus metrics. Scrape with Telegraf `inputs.prometheus` or any compatible collector. |
| `POST /alert/test` | Send a test alert email to verify SMTP configuration. Returns 200 on success, 503 if SMTP is not configured. |
| `DELETE /queue/{id}` | Remove a specific queued message by ID. Returns 204 on success, 404 if not found or currently in_flight. |
| `DELETE /queue` | Remove all queued messages that are not currently in_flight. Returns `{"deleted": N}`. |

### Queue response

`GET /queue` returns a JSON array of queued messages. Fields included:

| Field | Description |
|---|---|
| `id` | Message ID — use this with `DELETE /queue/{id}` |
| `received_at` | When the message was enqueued |
| `webhook_id` | The Discord channel identifier from the webhook URL |
| `status` | `pending` or `in_flight` |
| `retry_count` | Number of failed delivery attempts so far |
| `last_error` | Error from the most recent attempt (omitted if never attempted) |
| `last_attempt` | Timestamp of the most recent attempt (omitted if never attempted) |

Webhook tokens and message payloads are never included in this response.

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

## nginx reverse proxy

When running behind nginx for SSL termination, a few settings matter:

```nginx
server {
    listen 443 ssl;
    server_name discord-webhook-queue.example.com;

    # Grafana image attachments can be several MB
    client_max_body_size 20m;

    location / {
        proxy_pass http://127.0.0.1:8080;

        # Use HTTP/1.1 to upstream to enable keep-alive
        proxy_http_version 1.1;
        proxy_set_header Connection "";

        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;

        # Give large uploads room to complete
        proxy_read_timeout 120s;
    }
}
```

Note: nginx strips non-standard `Expect` headers before proxying, so the daemon's built-in workaround for discord.sh is redundant (but harmless) when behind nginx.

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
