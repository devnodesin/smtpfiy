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

## 2. Architecture & Design

Strictly modular; provider-agnostic; one responsibility per package.

```
smtpify/
├── AGENTS.md                # Process rules (lean)
├── SPEC.md                  # This document: behavior + design
├── Dockerfile               # Container build (minimal base, non-root)
├── go.mod
├── cmd/
│   └── smtpify/
│       └── main.go          # Wiring only: config -> smtp server -> provider
├── internal/
│   ├── config/
│   │   ├── config.go        # Load/validate config (env, flags, file)
│   │   └── config_test.go
│   ├── smtp/
│   │   ├── server.go        # Listen on localhost, accept unauth SMTP
│   │   ├── server_test.go
│   │   ├── session.go       # Per-connection SMTP state machine
│   │   └── session_test.go
│   ├── translate/
│   │   ├── translator.go    # SMTP session -> normalized EmailMessage
│   │   └── translator_test.go
│   ├── provider/
│   │   ├── provider.go      # Provider interface + shared models
│   │   ├── brevo.go         # Brevo client (implements Provider)
│   │   ├── brevo_test.go
│   │   ├── resend.go
│   │   ├── resend_test.go
│   │   ├── mailgun.go
│   │   └── mailgun_test.go
│   └── logging/
│       ├── logger.go        # Structured, leveled logging
│       └── logger_test.go
└── pkg/                     # Only if a package must be importable externally
```

### Design rationale

- `cmd/smtpify` contains **only** composition/wiring — no business logic.
- `internal/` keeps implementation private; providers and translation are isolated
  so adding a new provider (e.g. Postmark) is a localized change.
- The `Provider` interface decouples SMTP handling from any single vendor,
  enabling mocking in unit tests and reuse.
- `translate` is the single place that maps SMTP fields to the normalized
  `EmailMessage` model, so provider clients stay thin and consistent.

### Core interfaces & models (contract)

- `EmailMessage`: normalized message shape (from, to, cc, subject, text, html,
  attachments, headers).
- `Provider` interface: `Send(ctx context.Context, msg EmailMessage) error`.
- Each provider package implements `Provider` by mapping `EmailMessage` to its
  REST payload and performing the outbound call over TLS.

### Centralized default values

All hardcoded fallback defaults MUST be centralized in one place (a `Defaults`
struct in `internal/config`). This keeps behavior consistent and tests in sync.

- Never use hardcoded default literals directly in business logic.
- All code and tests reference defaults via the centralized source (e.g.
  `config.Defaults.Port`), never a repeated literal.
- Adding or changing a default updates the centralized struct and its tests only.

### TDD enforcement

- No Go change is considered complete or production-ready without passing tests.
- Code review/PRs MUST reject changes that lack the corresponding `*_test.go`
  coverage for the affected behavior.

## 3. Configuration

_Define keys, sources (env/file/flags), defaults, and validation rules here._

- Listening address — default `127.0.0.1:<port>`.
- Selected provider and its credentials (environment only).
- Logging level.

## 4. SMTP server behavior

_Define supported SMTP commands, flow, timeouts, and limits here._

- Commands: `HELO`/`EHLO`, `MAIL`, `RCPT`, `DATA`, `RSET`, `QUIT`.
- No AUTH required (local proxy).
- Message size limits and rejection behavior.

## 5. Translation rules

_Define how SMTP fields map to the `EmailMessage` model (from, to, cc, subject,
body text/html, attachments, headers)._

## 6. Provider behavior

- Per-provider mapping to REST payloads (Brevo, Resend, Mailgun).
- Error handling, retries, rate limits, and status semantics.

## 7. Security requirements

- Bind to localhost by default; never expose the listener to untrusted networks.
- Secrets from environment only; never logged.
- Redact email bodies and headers with secrets in logs.
- Input validation/canonicalization to prevent header injection.
- TLS for all outbound provider API calls.
- Size-limit inbound messages to prevent memory exhaustion/abuse.

## 8. Deployment (Docker)

- `Dockerfile` at repo root; minimal base image (e.g. distroless/alpine).
- Run as non-root; drop capabilities.
- Config and secrets supplied at runtime via environment, never baked in.

## 9. Out of scope

_List explicitly what smtpify will NOT do (e.g. relay to the public internet,
store mail, support authenticated clients)._
