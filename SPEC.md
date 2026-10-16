# SPEC.md — smtpfiy

> **Single source of truth.** This document defines the authoritative behavior,
> architecture, and design of smtpfiy. All code, tests, and documentation must
> conform to it. When it changes, code changes with it. If you find a
> contradiction between this file and the code, treat this file as correct and
> update the code (or raise it with the user).

_Status: evolving. Expand each section as features are agreed._

## 1. Overview

smtpfiy is a lightweight proxy that:

1. Listens for **local, unauthenticated SMTP** traffic from legacy software.
2. Receives and parses SMTP messages (HELO/EHLO, MAIL FROM, RCPT TO, DATA).
3. Translates each message into a normalized internal representation.
4. Sends it as a REST API call to a configured transactional email provider
   (Brevo, Resend, Mailgun).

Goal: let old applications send email through modern providers without code changes.

### Development methodology

- **Specification-Driven Development (SDD):** every feature MUST have its
  requirement and acceptance criteria in this document before implementation.
- **Test-Driven Development (TDD):** every feature MUST start with a failing
  test. No exceptions.

## 2. Configuration

smtpfiy is configured via one or more YAML files. Configuration is loaded,
merged, defaulted, then validated before the service starts.

### Structure

The on-disk YAML has two top-level blocks:

- `server` — global service settings: `host`, `port`, `smtp_host`,
  `smtp_port`, `db_path`, `scheduler`, `retry`, `retry_delay`, `log_level`,
  `log_file`.
- `server.smtp_auth` — optional SMTP AUTH block (PLAIN/LOGIN). It has two
  fields: `required` (bool) and `users` (list of `{username, password}`). When
  `users` is empty/absent, AUTH is not advertised and no authentication is
  required (fully backward compatible). The number of failed AUTH attempts
  before the connection is closed is controlled by the centralized default
  `MaxAuthAttempts` (5).
- `server.smtp_tls` — optional STARTTLS block. It has three fields: `enabled`
  (bool, default `false`), `cert_file` (PEM certificate path), and `key_file`
  (PEM private-key path). When `enabled: true` and both file paths are empty, an
  in-memory self-signed certificate is generated at startup (logged at `warn`,
  regenerated on every start); when both paths are set they are loaded, and a
  load failure fails fast at startup. When `enabled: false` (or absent) the
  listener stays plaintext and fully backward compatible.
- `domains` — a map of sender domain to a list of `providers`. Each provider
  has `provider` (adapter name), `api_key`, `limit_daily`, `limit_monthly`.

When AUTH is configured (at least one `users` entry), the listener advertises
`AUTH PLAIN LOGIN` in its EHLO (ESMTP) response and accepts the `AUTH` command.
`HELO` clients receive no AUTH advertisement. Credentials are compared using
constant-time comparison for the password. On success the server replies
`235 Authentication successful`; on failure `535 Authentication failed`. After
`MaxAuthAttempts` failed attempts the connection is closed with `421`. When
`required: true` and the session has not authenticated, `MAIL FROM` is rejected
with `530 Authentication required`. Authentication events are logged at `info`
(success) and `warn` (failure) without leaking the password; credentials are
masked in diagnostics.

**STARTTLS and AUTH ordering.** When `smtp_tls.enabled: true`, the listener
advertises `STARTTLS` in its EHLO multi-line response and accepts the `STARTTLS`
command (`220 2.0.0 Ready to start TLS`, then upgrades the connection; SMTP
state is reset and the client must re-`EHLO`). **AUTH requires STARTTLS when TLS
is enabled:** an `AUTH` command or a `MAIL FROM` (when `smtp_auth.required:
true`) issued before the connection is encrypted is rejected with `523 5.7.10
Encryption required`. TLS handshakes are logged at `info` (success) and `warn`
(failure); certificate private keys and credentials are never logged.

When `smtp_tls.enabled: false`, no STARTTLS is advertised or accepted and AUTH
(if configured) proceeds in cleartext over the internal backend network only
(the listener binds to `smtp_host`, default `127.0.0.1`). This is acceptable
given the network isolation; credentials are not secrets in this topology. The
backend SMTP port is never published to a host port.

All fallback defaults are centralized in `cmd/smtpfiy/config.go` (the `Defaults`
struct). Default `host` and `smtp_host` are `127.0.0.1` (localhost binding).
See `cmd/smtpfiy/config.go` for the authoritative default values.

### Architecture

smtpfiy is a single-binary application. All code — configuration, logging, and
the future SMTP listener, HTTP server, and provider adapters — lives directly
under `cmd/smtpfiy/` as `package main`. There is no `internal/` package
boundary; the binary's packages are split by responsibility into separate files
(`config.go`, `logging.go`, `main.go`, etc.) rather than by directory.

### Loading

- A `-c` CLI flag accepts a comma-separated list of YAML file paths, overriding
  the default search paths.
- When no `-c` flag is given, the default search paths are used.
- Multiple files are merged into a single configuration. The `server` block may
  be defined at most once across all files; a second definition is rejected.
- A domain may appear at most once across all files; a duplicate domain
  definition is rejected.
- At least one readable configuration file must be found, otherwise loading
  fails.
- Unset `server` fields are filled from the centralized defaults.

### Validation

Before use, the merged configuration is validated:

- `scheduler` must be one of `order` or `rr`.
- At least one sender domain must be configured.
- Each domain must have at least one provider.
- Only supported providers are accepted (V1: `resend`).
- A provider's `api_key` must be non-empty.
- `log_level` must be one of `debug`, `info`, `warn`, `error` (see Logging).
- API keys are masked (`***`) wherever configuration is rendered for diagnostics;
  keys are never logged in cleartext.

### Reference

`server` fields and their defaults:

| Field | Default | Description |
|-------|---------|-------------|
| `host` | `127.0.0.1` | HTTP bind address (localhost by default; never `0.0.0.0` unless explicitly set) |
| `port` | `7001` | HTTP port |
| `smtp_host` | `127.0.0.1` | SMTP bind address (localhost by default) |
| `smtp_port` | `2525` | SMTP port |
| `db_path` | `/smtpfiy/data/smtpfiy.db` | SQLite database path |
| `scheduler` | `order` | Provider selection: `order` \| `rr` |
| `retry` | `1` | Max delivery attempts; `0` disables retries |
| `retry_delay` | `60` | Seconds between retries |
| `log_level` | `info` | `debug` \| `info` \| `warn` \| `error` |
| `log_file` | `/smtpfiy/log/smtpfiy.log` | Log file path (dual write with stdio) |

`domains` maps each sender domain to a list of providers; each provider has
`provider` (adapter name), `api_key`, `limit_daily`, `limit_monthly`. Provider
fallback is restricted to providers of the same sender domain.

> Note: the configuration code existed before this section was written.
> This section retroactively documents its implemented behavior to restore SDD
> compliance; the code already conforms to these rules. The config logic now
> lives in `cmd/smtpfiy/config.go`.

## 3. Logging

smtpfiy emits operational logs covering service lifecycle and email delivery.
Logging uses the Go standard library only (`log`, `io`, `os`).

### Output

- Logs are always written to standard output (stdio).
- When a `log_file` path is configured, logs are additionally written to that
  file (dual write to both stdio and the file).
- If `log_file` creation or writing fails, logging continues to stdio and the
  error is handled gracefully (no crash, no panic).

### Levels & Format

Log levels, in increasing severity, are: `debug`, `info`, `warn`, `error`.
A level threshold filters output: messages below the configured `log_level`
threshold are suppressed (e.g. `debug` is suppressed when level is `info`).

Each log entry uses the format:

```text
timestamp LEVEL message
```

with an RFC3339-style timestamp, an upper-case level, and the message. The
service startup banner (e.g. `starting smtpfiy`, `configuration loaded`,
`started smtpfiy`) is emitted at `info` level. The full set of entry formats
is shown in the Logging section above.

### Sensitive Data

Logs must never contain:

- Provider API keys (masked as `***`).
- Email bodies.
- Passwords or authentication credentials.
- Sensitive HTTP headers.

Email addresses may appear in logs (e.g. sender/recipient in delivery events).

## 4. SMTP Server

smtpfiy accepts email from local applications and containers over SMTP, parses
each message, and hands a normalized representation to the queue for delivery.
Implemented in `cmd/smtpfiy/smtp.go` (package main).

### Listening & binding

- Binds to `server.smtp_host` / `server.smtp_port` (defaults `127.0.0.1:2525`).
- The SMTP listener MUST bind to localhost by default and MUST NOT become an
  open relay (never `0.0.0.0` unless explicitly configured by the user).

### Protocol handling

The server implements a minimal SMTP transaction:

- Greets with `220`, handles `HELO`/`EHLO` (replies `250`).
- `MAIL FROM:<addr>` records the envelope sender; replies `250`.
- `RCPT TO:<addr>` records each envelope recipient; replies `250` per recipient.
- `DATA` reads the message body until the terminating `\r\n.\r\n`, replies
  `250` (message accepted).
- `RSET` resets the transaction; `QUIT` replies `221` and closes; unknown
  commands reply `500`/`502`.

### Parsing & normalization

- The `From` header is parsed to determine the **sender domain** used for
  provider selection. The envelope `MAIL FROM` domain is validated against the
  configured `domains` (only configured sender domains are accepted).
- The message is parsed into a normalized internal representation:
  `Message{ID, From, To[], Cc[], Bcc[], Subject, TextBody, HTMLBody,
  Headers, RawSize, Domain, CreatedAt}`.
- A unique message ID is assigned (short random hex, e.g. `8f21a4`).

### Limits & security (untrusted input)

- Connection, message, and recipient-count limits are applied and enforced.
- Email addresses are canonicalized/validated at the boundary (single address
  per MAIL FROM / RCPT TO command).
- SMTP header and CRLF injection are prevented: header values and addresses
  are validated; raw header lines containing embedded CRLF are rejected.

### Handoff

On a completed `DATA` transaction the parsed `Message` is passed to the queue
store (see `## 5. Queue`); on success the server replies `250` to the client.
Delivery (provider selection/send) happens asynchronously via the queue, not
during the SMTP transaction.

### Logging

On accept, the server emits an `SMTP` log event at `info` level, e.g.
`SMTP  id:8f21a4 (alerts@example.com -> user@example.net)`, with addresses but
no body.

## 5. Queue (SQLite)

smtpfiy persists received messages in a SQLite database used as a durable
message queue and service-state store. Implemented in `cmd/smtpfiy/queue.go`
(package main) using `modernc.org/sqlite` (pure-Go, no cgo).

### Persistence

- The database file is at `server.db_path` (default `/smtpfiy/data/smtpfiy.db`).
- Messages survive container/service restarts.
- The schema is created automatically on open if it does not exist.

### Schema

A `messages` table tracks each message and its delivery state:

- `id` TEXT PRIMARY KEY (short message id)
- `from_addr` TEXT
- `to_addrs` TEXT (comma-separated; envelope recipients)
- `subject` TEXT
- `text_body` TEXT
- `html_body` TEXT
- `domain` TEXT (sender domain used for provider selection)
- `status` TEXT — one of `pending`, `processing`, `sent`, `failed`
- `provider` TEXT — last/selected provider name
- `attempts` INTEGER
- `last_error` TEXT
- `created_at` TEXT (RFC3339)
- `sent_at` TEXT (RFC3339, nullable)
- `failed_at` TEXT (RFC3339, nullable)

### Operations

- `Store(Message)` inserts a new row with status `pending`.
- `Pending()` returns messages with status `pending`/`processing` eligible for
  delivery (used by the delivery worker).
- `MarkSent(id, provider)` sets status `sent`, records provider and `sent_at`.
- `MarkFailed(id, err)` increments `attempts`, records `last_error` and
  `failed_at`, and sets status `failed` only after retries are exhausted
  (otherwise keeps it `pending` for retry — see `## 7. Retry`).
- `Stats()` returns counts by status and recent items for `/stats`
  (NO email body is returned by `Stats()`).

### Security

- Email bodies are stored but never returned by `Stats()` or any HTTP endpoint.
- API keys are never written to the database.

## 6. Provider Adapters (Resend, V1)

smtpfiy delivers queued messages through provider REST APIs. V1 implements a
single adapter: **Resend**. Adapters implement the `Provider` interface
(`cmd/smtpfiy/provider.go`).

### Interface

```go
type Provider interface {
    Name() string
    Send(ctx context.Context, msg Message) (providerID string, err error)
}
```

### Resend adapter

- Endpoint: `POST https://api.resend.com/emails`.
- Auth: `Authorization: Bearer <api_key>`; `Content-Type: application/json`.
- Request body (JSON): `from`, `to` (array), `subject`, `html`, `text`, `cc`,
  `bcc`, `reply_to`, `headers`, `attachments`, `tags`.
- Success response: `{ "id": "<uuid>" }` → returns that id.
- Errors are classified as permanent (`401`, `403`, `4xx` other than `429`)
  vs retryable (`429`, `5xx`, network/timeout). The adapter returns a typed
  error so the retry engine can decide.
- Transport uses TLS/HTTPS; certificate verification is never disabled.
- A request/network timeout is applied via `context.Context` (caller-supplied).
- API keys and email bodies are never written to logs (use `MaskAndInfo`).

## 7. Provider Selection / Scheduler

Multiple providers may be configured for the same sender domain. Provider
selection is controlled by `server.scheduler` and implemented in
`cmd/smtpfiy/scheduler.go`.

### Strategy

- `order` — always start with the first configured provider for the domain;
  fall back to the next in configuration order after a failure.
- `rr` — round-robin: maintain per-domain rotating state so successive messages
  for the same domain spread across its providers.

### Contract

- `Scheduler.Select(domain, providers) []Provider` returns the providers for a
  domain in the order they should be attempted (primary first, then fallback).
  For `order` this is the configuration order; for `rr` the list is rotated so
  the next primary is first.
- Fallback is restricted to providers configured for the **same sender domain**.
- smtpfiy must never silently change the sender domain to use another provider
  configuration. If a domain has no configured providers, selection returns an
  error.

## 8. Retry Engine

The delivery worker (`cmd/smtpfiy/delivery.go`) picks pending messages from the
queue and attempts delivery through the selected provider(s), applying retries.

### Behavior

- For each pending message, the scheduler returns the ordered providers for its
  domain. The worker tries them in order (primary, then fallback) until one
  succeeds.
- A **retryable** failure (`429`, `5xx`, network/timeout) triggers a retry after
  `server.retry_delay` seconds, up to `server.retry` total attempts. `retry: 0`
  disables retries (message fails after the first attempt).
- A **permanent** failure (`401`, `403`, other `4xx`) is NOT retried; the
  message is marked failed immediately.
- After all attempts/providers are exhausted without success, the message is
  marked `failed` (terminal).
- On success, the message is marked `sent` with the provider name and id.
- The worker runs as a background goroutine with bounded concurrency. It backs
  off and respects `context` cancellation for graceful shutdown.

### Queue interaction

- `MarkFailed(id, err, forceFailed=true)` on terminal failure.
- `MarkFailed(id, err, forceFailed=false)` is NOT used here; instead attempts
  accumulate via re-queueing (status remains `pending`) until exhausted.
- `MarkSent(id, provider)` on success.

## 9. HTTP Server

smtpfiy exposes a minimal HTTP API for health and statistics
(`cmd/smtpfiy/http.go`). It binds to `server.host` / `server.port` (default
`127.0.0.1:7001`).

### Endpoints

- `GET /health` — returns `200 {"status":"ok"}` when the service is operating
  normally, or a `5xx {"status":"error"}` when it cannot (lightweight; intended
  for Docker health checks).
- `GET /stats` — returns a JSON object with `uptime`, `database` (path, size),
  `messages` (total/pending/processing/sent/failed counts) and `items` (recent
  messages: id, from, to, subject, provider, status, attempts, timestamps,
  last_error). The email body is **never** included, and no secrets are exposed.

### Operation

- The server runs as a background goroutine; supports graceful shutdown via
  `http.Server.Shutdown` on context cancellation.
- Concurrency is bounded; request timeouts are applied.

### `/stats` response shape

```json
{
  "uptime": "1h23m",
  "database": { "path": "/smtpfiy/data/smtpfiy.db", "size": "2.4 MB" },
  "messages": {
    "total": 120, "pending": 4, "processing": 1, "sent": 112, "failed": 3
  },
  "items": [
    {
      "id": "8f21a4", "from": "alerts@example.com", "to": ["user@example.net"],
      "subject": "Server Alert", "provider": "resend", "status": "sent",
      "attempts": 1, "created_at": "2026-10-16T10:30:00Z",
      "sent_at": "2026-10-16T10:30:02Z"
    },
    {
      "id": "b73d11", "from": "alerts@example.com", "to": ["admin@example.net"],
      "subject": "Daily Report", "provider": "brevo", "status": "failed",
      "attempts": 2, "created_at": "2026-10-16T10:40:00Z",
      "last_error": "connection timeout"
    }
  ]
}
```

Each item may include `id`, `from`, `to`, `subject`, `provider`, `status`,
`attempts`, `created_at`, `sent_at`, `failed_at`, `last_error`. The email body
is **never** returned. A Docker healthcheck calls `GET /health` (e.g.
`curl -f http://localhost:7001/health`).

## 10. Message Lifecycle Wiring

The `main` entrypoint (`cmd/smtpfiy/main.go`) wires the components together and
runs them as coordinated background services with graceful shutdown:

1. Load and validate configuration (incl. `ValidateLog`).
2. Initialize the logger from config (`log_level`, `log_file`).
3. Open the SQLite queue at `db_path`.
4. Build the `Scheduler` and a `ProviderResolver` that resolves each domain's
   configured providers (V1: `resend`).
5. Start the SMTP server (binds `smtp_host`/`smtp_port`); on `DATA` it stores
   the parsed message via the queue and emits an `SMTP` log event.
6. Start the HTTP server (binds `host`/`port`) for `/health` and `/stats`.
7. Start the delivery worker loop: repeatedly take pending messages and attempt
   delivery (see `## 8. Retry Engine`), emitting `SENT`/`FAIL` log events.
8. On `SIGINT`/`SIGTERM`, stop accepting new SMTP/HTTP connections and drain in
   flight work before exiting non-zero on startup error or zero on clean stop.

### Logging events

- `SMTP  id:... (from -> to)` on receive.
- `SENT  id:... via <provider> (from -> to)` on success.
- `FAIL  id:... (<reason>)` on terminal failure.

Queue activity is NOT logged as routine events; inspect via `/stats`.

## 11. Deployment

smtpfiy is distributed as a small Docker image and runs as a non-root user.

- Multi-stage build: compile `cmd/smtpfiy` with `CGO_ENABLED=0` (the pure-Go
  SQLite driver needs no C toolchain), then run on a minimal Alpine image as an
  unprivileged `smtpfiy` user. The build base image `golang:<version>-alpine`
  MUST track (or exceed) the `go` directive in `go.mod` — a too-old builder
  fails at `go mod download` with `go.mod requires go >= ...`.
- Health: a Docker `HEALTHCHECK` calls `GET /health` (see `## 9. HTTP Server`);
  the image uses `wget -q -O- http://localhost:7001/health` since Alpine ships
  BusyBox `wget` (not `curl`) by default.
- Exposed ports: `2525` (SMTP) and `7001` (HTTP).
- Volumes: `/smtpfiy/data` (queue DB), `/smtpfiy/log` (logs), `/smtpfiy/config`
  (configuration).
- Health: a Docker healthcheck calls `GET /health` (see `## 9. HTTP Server`).
- SMTP binds to localhost by default; only set `smtp_host: 0.0.0.0` when the
  container is deliberately exposed on a network (never as a default).
- No secrets in the image: API keys come from the mounted config only.
