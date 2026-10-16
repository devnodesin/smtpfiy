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
