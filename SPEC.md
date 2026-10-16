# SPEC.md — smtpify

> **Single source of truth.** This document defines the authoritative behavior,
> architecture, and design of smtpify. All code, tests, and documentation must
> conform to it. When it changes, code changes with it. If you find a
> contradiction between this file and the code, treat this file as correct and
> update the code (or raise it with the user).

_Status: evolving. Expand each section as features are agreed._

## 1. Overview

smtpify is a lightweight proxy that:

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

smtpify is configured via one or more YAML files. Configuration is loaded,
merged, defaulted, then validated before the service starts.

### Structure

The on-disk YAML has two top-level blocks:

- `server` — global service settings: `host`, `port`, `smtp_host`,
  `smtp_port`, `db_path`, `scheduler`, `retry`, `retry_delay`, `log_level`,
  `log_file`.
- `domains` — a map of sender domain to a list of `providers`. Each provider
  has `provider` (adapter name), `api_key`, `limit_daily`, `limit_monthly`.

All fallback defaults are centralized in `cmd/smtpify/config.go` (the `Defaults`
struct). Default `host` and `smtp_host` are `127.0.0.1` (localhost binding).
See `cmd/smtpify/config.go` for the authoritative default values.

### Architecture

smtpify is a single-binary application. All code — configuration, logging, and
the future SMTP listener, HTTP server, and provider adapters — lives directly
under `cmd/smtpify/` as `package main`. There is no `internal/` package
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

> Note: the configuration code existed before this section was written.
> This section retroactively documents its implemented behavior to restore SDD
> compliance; the code already conforms to these rules. The config logic now
> lives in `cmd/smtpify/config.go`.

## 3. Logging

smtpify emits operational logs covering service lifecycle and email delivery.
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
service startup banner (e.g. `starting smtpify`, `configuration loaded`,
`started smtpify`) is emitted at `info` level. See `doc/logging.md` for the
full set of entry formats.

### Sensitive Data

Logs must never contain:

- Provider API keys (masked as `***`).
- Email bodies.
- Passwords or authentication credentials.
- Sensitive HTTP headers.

Email addresses may appear in logs (e.g. sender/recipient in delivery events).

## 4. SMTP Server

smtpify accepts email from local applications and containers over SMTP, parses
each message, and hands a normalized representation to the queue for delivery.
Implemented in `cmd/smtpify/smtp.go` (package main).

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

On accept, the server emits an `SMTP` log event at `info` level using the
format from `doc/logging.md`, e.g.
`SMTP  id:8f21a4 (alerts@example.com -> user@example.net)`, with addresses but
no body.

## 5. Queue (SQLite)

smtpify persists received messages in a SQLite database used as a durable
message queue and service-state store. Implemented in `cmd/smtpify/queue.go`
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

smtpify delivers queued messages through provider REST APIs. V1 implements a
single adapter: **Resend**. Adapters implement the `Provider` interface
(`cmd/smtpify/provider.go`).

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
`cmd/smtpify/scheduler.go`.

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
- smtpify must never silently change the sender domain to use another provider
  configuration. If a domain has no configured providers, selection returns an
  error.
