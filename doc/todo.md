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

## 3. SMTP Server (PENDING)
Spec: `doc/design.md` → SMTP Server; `## SMTP Server` in SPEC.md (to add).
- [ ] Bind to `smtp_host`/`smtp_port` (default 127.0.0.1:2525, localhost only)
- [ ] Accept connections; handle HELO/EHLO, MAIL FROM, RCPT TO, DATA
- [ ] Parse incoming MIME message (headers + body; plain + html)
- [ ] Determine sender domain from `From`; reject/validate untrusted input
- [ ] Assign unique message ID; apply connection/size/recipient limits
- [ ] Prevent CRLF/header injection; canonicalize addresses at boundary
- [ ] Hand parsed message to the queue (store) stage

## 4. SQLite Queue (PENDING)
Spec: `doc/design.md` → SQLite Queue; `## Queue` in SPEC.md (to add).
- [ ] Open/create SQLite DB at configured `db_path` (persistent across restarts)
- [ ] Schema: messages table (id, from, to, subject, body, provider state,
      status, attempts, created_at, sent_at, failed_at, last_error, domain)
- [ ] Store received message; track status: pending/processing/sent/failed
- [ ] Persist retry state and per-provider delivery state
- [ ] Expose query API for `/stats` (no email body returned there)

## 5. Provider Adapters — Resend (PENDING, FIRST PROVIDER)
Spec: `doc/design.md` → Provider Adapters; `## Provider Adapters` in SPEC.md (to add).
Resend REST API (verified): `POST https://api.resend.com/emails`,
`Authorization: Bearer <api_key>`, `Content-Type: application/json`.
Request body fields: `from` (string), `to` (string|string[]), `subject`,
`html`, `text`, `cc`, `bcc`, `reply_to`, `headers`, `attachments`, `tags`.
Success response: `{ "id": "<uuid>" }`. Use TLS/HTTPS; never disable cert check.
- [ ] `resend` adapter: translate internal message → Resend JSON request
- [ ] Send via `POST https://api.resend.com/emails` with Bearer auth
- [ ] Handle response: success → message id; error → classify permanent vs retryable
- [ ] Apply request/network timeout via `context.Context`
- [ ] Never log API key or email body (mask via logger)
- [ ] Unit-test adapter with a mocked HTTP server (failing test first)

## 6. Provider Selection / Scheduler (PENDING)
Spec: `doc/design.md` → Provider Selection.
- [ ] `order` scheduler: try providers in config order for the sender domain
- [ ] `rr` scheduler: round-robin selection per sender domain
- [ ] Fallback restricted to providers of the **same sender domain**
- [ ] Never change sender domain to use another domain's provider config

## 7. Retry Engine (PENDING)
Spec: `doc/design.md` → Retry; config `retry`, `retry_delay`.
- [ ] Honor max attempts (`retry`); 0 disables retry
- [ ] Delay between attempts (`retry_delay` seconds)
- [ ] Respect provider availability + selection strategy on fallback
- [ ] Update attempts/last_error/failed_at in queue; terminal FAIL after exhaustion

## 8. HTTP Server (PENDING)
Spec: `doc/design.md` → HTTP Server; `doc/api.md`.
- [ ] Bind to `host`/`port` (default 127.0.0.1:7001)
- [ ] `GET /health` → `{ "status": "ok" }` (200) / `5xx` when unhealthy; lightweight
- [ ] `GET /stats` → uptime, database path/size, message counts, recent items
      (id, from, to, subject, provider, status, attempts, timestamps, last_error)
- [ ] `/stats` MUST NOT return email body; no secrets exposed
- [ ] Graceful shutdown; bound concurrency

## 9. Message Lifecycle Wiring (PENDING)
Spec: `doc/design.md` → Message Lifecycle.
- [ ] End-to-end flow: SMTP receive → validate/parse → store → select provider
      → send → success(SENT) / failure(retry→next provider→FAIL)
- [ ] Emit `SMTP`, `SENT`, `FAIL` log events per `doc/logging.md`
- [ ] Queue activity NOT logged as normal events (inspect via `/stats`)

## 10. Operational / Deployment Hardening (PENDING)
- [ ] Dockerfile: minimal multi-stage, non-root, volume mounts (data/log/config)
- [ ] Docker healthcheck using `GET /health`
- [ ] Non-root runtime; minimal privileges
- [ ] Final pass: `gofmt`, `go vet`, `go test -race ./...`, no secrets committed

---

## Notes
- Only `## Configuration` and `## Logging` currently exist in `SPEC.md`; each
  pending section above needs a corresponding `SPEC.md` feature section written
  before implementation begins.
- Resend is the V1 reference provider; Brevo/Mailgun/SMTP2GO adapters are
  out of scope for V1 but the adapter interface should accommodate them.
