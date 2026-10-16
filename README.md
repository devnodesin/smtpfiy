# smtpify

A lightweight SMTP-to-API relay. smtpify receives email over local SMTP from
legacy applications and containers, queues it in a persistent SQLite store, and
delivers it through a transactional email provider's REST API (Resend in V1).

## Features

- Local, unauthenticated SMTP listener (binds localhost by default).
- Persistent SQLite message queue (survives restarts).
- Pluggable provider adapters — Resend is the V1 reference.
- Per-domain provider selection: `order` or round-robin (`rr`) with same-domain
  fallback.
- Retry with backoff for retryable failures; permanent failures fail fast.
- Minimal HTTP API: `/health` and `/stats`.
- Simple operational logging (`SMTP` / `SENT` / `FAIL`); no email bodies or
  API keys are ever logged.
- Small, non-root Docker image.

See `SPEC.md` for the authoritative behavior, configuration reference, HTTP API,
and logging rules.

## Build

```bash
go build -o smtpify ./cmd/smtpify
```

## Configure

Configuration is one or more YAML files. Examples live in `samples/`:

- `samples/single.yaml` — everything in one file.
- `samples/multi-main.yaml` + `samples/multi-provider.yaml` — split into server
  settings and domains/providers.

Run with a single file:

```bash
./smtpify -c samples/single.yaml
```

Or combine multiple files (comma-separated):

```bash
./smtpify -c samples/multi-main.yaml,samples/multi-provider.yaml
```

If `-c` is omitted, smtpify searches the default paths
(`/etc/smtpfiy.conf`, `/etc/smtpfiy/smtpfiy.yaml`).

### Key settings

```yaml
server:
  smtp_host: "127.0.0.1"   # SMTP bind address (localhost by default — never an open relay)
  smtp_port: 2525
  host: "127.0.0.1"        # HTTP bind address
  port: 7001
  db_path: "/smtpfiy/data/smtpfiy.db"
  scheduler: order         # order | rr
  retry: 3                 # max delivery attempts (0 disables retries)
  retry_delay: 30          # seconds between retries
  log_level: info          # debug | info | warn | error
  log_file: "/smtpfiy/log/smtpfiy.log"

domains:
  example.com:
    providers:
      - provider: resend
        api_key: "re_your_api_key_here"
        limit_daily: 100
        limit_monthly: 3000
```

## Run (Docker)

```bash
docker build -t smtpify .
docker run --rm -p 2525:2525 -p 7001:7001 \
  -v $(pwd)/data:/smtpfiy/data \
  -v $(pwd)/config:/smtpfiy/config \
  smtpify -c /smtpfiy/config/single.yaml
```

The image runs as a non-root user. A healthcheck calls `GET /health`.

## HTTP API

- `GET /health` → `200 {"status":"ok"}` (or `5xx` when unhealthy).
- `GET /stats` → uptime, database info, message counts, and recent items.
  Email bodies and secrets are never returned.

## Development

This project follows Specification-Driven Development (SDD) and Test-Driven
Development (TDD): every feature is spec'd in `SPEC.md` before implementation
and starts from a failing test. Run the suite with:

```bash
gofmt -w .
go vet ./...
go test -race ./...
```
