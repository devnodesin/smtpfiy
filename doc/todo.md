# smtpify — Implementation TODO

Tracking document for pending and completed work derived from `doc/design.md`,
`doc/api.md`, `doc/config.md`, and `doc/logging.md`. Completed items reflect the
current codebase state (config + logging implemented per `SPEC.md` `## Configuration`
and `## Logging`).

> Process rule: per `AGENTS.md`, every feature below MUST be spec'd in `SPEC.md`
> (with acceptance criteria) **before** code, and MUST start from a failing test
> (TDD). Mark a task `done` only after its `SPEC.md` section exists, tests pass,
> and `go test -race ./...` is green.

Legend: `[ ]` pending · `[x]` done

---

## 1. Configuration (DONE)
- [x] `server` + `domains` YAML structure, defaults, merge, duplicate rejection
- [x] `-c` flag, default search paths, validation (scheduler, providers, api_key)
- [x] `ValidateLog()` enforcing `log_level ∈ {debug,info,warn,error}`

## 2. Logging (DONE)
- [x] Leveled logger (debug/info/warn/error) with timestamp+level+message format
- [x] Dual write to stdio + `log_file`; graceful file-open failure
- [x] Sensitive-data masking helper (`MaskAndInfo`, `***` for API keys)

---

## 3. SMTP Server (DONE)
Spec: `doc/design.md` → SMTP Server; `## 4. SMTP Server` in SPEC.md (added).
- [x] Bind to `smtp_host`/`smtp_port` (default 127.0.0.1:2525, localhost only)
- [x] Accept connections; handle HELO/EHLO, MAIL FROM, RCPT TO, DATA
- [x] Parse incoming MIME message (headers + body; plain + html)
- [x] Determine sender domain from `From`; reject/validate untrusted input
- [x] Assign unique message ID; apply connection/size/recipient limits
- [x] Prevent CRLF/header injection; canonicalize addresses at boundary
- [x] Hand parsed message to the queue (store) stage (end-to-end test w/ real client)

## 4. SQLite Queue (DONE)
Spec: `doc/design.md` → SQLite Queue; `## 5. Queue` in SPEC.md (added).
- [x] Open/create SQLite DB at `db_path` (modernc.org/sqlite, pure-Go/no cgo)
- [x] Schema: messages table (id, from, to, subject, bodies, domain, status,
      provider, attempts, last_error, created_at, sent_at, failed_at)
- [x] Store received message (status pending); satisfies SMTP `MessageStore`
- [x] Pending() for delivery worker; MarkSent / MarkFailed(force)
- [x] Stats() for /stats — counts + items, NO body returned
- [x] Persistence across restarts; parent dir creation
- [x] Failing-first TDD; `go test -race` green

## 5. Provider Adapters — Resend (DONE)
Spec: `doc/design.md` → Provider Adapters; `## 6. Provider Adapters` in SPEC.md (added).
- [x] `Provider` interface (`Name`, `Send(ctx, msg)`)
- [x] Resend adapter: `POST https://api.resend.com/emails`, Bearer auth, JSON
- [x] Translate Message → Resend request (from/to/subject/text/html/cc/bcc)
- [x] Success → provider id; error classification (permanent vs retryable)
- [x] Timeout via context; TLS/HTTPS enforced
- [x] No API key / body leakage; failing-first TDD w/ httptest mock

## 6. Provider Selection / Scheduler (DONE)
Spec: `doc/design.md` → Provider Selection; `## 7. Provider Selection` in SPEC.md.
- [x] `order` scheduler: configuration order, fallback in order
- [x] `rr` scheduler: per-domain round-robin rotation
- [x] Fallback restricted to same-domain providers; error if none configured
- [x] Failing-first TDD

## 7. Retry Engine (DONE)
Spec: `doc/design.md` → Retry; `## 8. Retry Engine` in SPEC.md (added).
- [x] Delivery worker: pick pending, try providers in order, fallback within domain
- [x] Retryable (`429`/`5xx`/network) retried up to `retry` attempts w/ `retry_delay`
- [x] Permanent (`401`/`403`/other 4xx) fails immediately (no retry)
- [x] `retry: 0` disables retries; terminal FAIL after exhaustion; SENT on success
- [x] `MarkSent`/`MarkFailed(force)` queue updates; context-aware backoff
- [x] Failing-first TDD (fake provider, retry/delay/exhaustion/permanent)

## 8. HTTP Server (DONE)
Spec: `doc/design.md` → HTTP Server; `## 9. HTTP Server` in SPEC.md (added).
- [x] Bind to `host`/`port` (default 127.0.0.1:7001)
- [x] `GET /health` → `200 {"status":"ok"}` (5xx when unhealthy); Docker-friendly
- [x] `GET /stats` → uptime, database(path/size), message counts, recent items
- [x] `/stats` omits email body and secrets; graceful shutdown; timeouts
- [x] Failing-first TDD (httptest for both endpoints)

## 9. Message Lifecycle Wiring (DONE)
Spec: `doc/design.md` → Message Lifecycle; `## 10. Message Lifecycle Wiring` in SPEC.md (added).
- [x] `main.go` wires config → logger → queue → SMTP → HTTP → delivery worker
- [x] SMTP stores to queue; delivery worker processes pending with retry
- [x] `configResolver` resolves per-domain providers (same-domain fallback)
- [x] Graceful shutdown on SIGINT/SIGTERM; startup errors exit non-zero
- [x] Log events: SMTP / SENT / FAIL (queue activity not logged)
- [x] Integration test: real SMTP client → queue → delivery → SENT

## 10. Operational / Deployment Hardening (DONE)
- [x] Dockerfile: multi-stage, CGO_ENABLED=0, non-root Alpine user, volumes (data/log/config)
- [x] Exposed ports 2525 (SMTP) / 7001 (HTTP); healthcheck via GET /health
- [x] Non-root runtime; minimal privileges; no secrets in image
- [x] SPEC `## 11. Deployment`; runtime smoke test passes (/health, /stats, graceful shutdown)
- [x] Final pass: gofmt, vet, `go test -race ./...` all green; no secrets committed

## Notes
- All design-doc features (SMTP, Queue, Resend adapter, Scheduler, Retry,
  HTTP, Lifecycle, Deployment) are now implemented and spec'd in SPEC.md.
- Resend is the V1 reference provider; Brevo/Mailgun/SMTP2GO adapters are out
  of scope for V1 but the Provider interface accommodates them.
