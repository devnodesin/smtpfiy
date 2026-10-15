# AGENTS3.md — smtpify

`gstdesk`-style guidance for AI agents working on `smtpify`.

## What this project is

- **Language:** Go (modules). Target the version pinned in `go.mod`.
- **Name:** `smtpify`
- **Purpose:** A lightweight proxy that intercepts local, unauthenticated SMTP traffic
  from legacy software and translates it into modern REST API payloads for
  transactional email providers (Brevo, Resend, Mailgun).

## Source of truth

Authority, in order:

1. **User/developer instructions** — highest authority.
2. **`SPEC.md`** — the evolving specification and single source of truth for
   behavior, architecture, and design. Update it BEFORE implementation when
   behavior or design changes. It always wins over this file and over memory.
3. **`AGENTS.md`** — this file (process rules only).
4. **Existing codebase** — follow established patterns.

If `SPEC.md` is silent on a requirement, do not invent it. Update `SPEC.md`
(or ask the developer) before implementing. Software design and architecture
live in `SPEC.md`, not here.

## Process rules (MANDATORY)

### Specification-First, then TDD

Every non-trivial change MUST follow this order:

```
Analyze → Plan → Update/Verify SPEC.md → Write failing test
→ Prove it fails → Implement → Run targeted tests → Run full suite
→ Verify against SPEC.md → Final review
```

- **No behavior in code before it is specified in `SPEC.md`.**
- **No production code before its failing test exists** (trivial scaffolding excepted).
- Prove the test fails for the right reason, then implement the minimum to pass.
- No change is complete or production-ready without passing tests; PRs/code
  review must reject changes that lack coverage for the affected behavior.

### Modularity & reuse

- Strictly modular. One responsibility per package/file.
- DRY — extract a helper/interface before a pattern appears a third time.
- `cmd/smtpify` is wiring only; business logic lives in `internal/`.
- **Centralize all fallback defaults** in `internal/config` (a `Defaults` struct);
  never hardcode default literals in business logic, and reference them via the
  centralized source in both code and tests. See `SPEC.md` → Architecture.
- Detailed design and directory layout live in `SPEC.md`, not here.

### Work directly, keep scope tight

- Work on the current working tree; do not create branches/commits unless explicitly requested.
- Keep changes small and focused; avoid unrelated refactoring.
- Preserve existing behavior unless the specification changes.
- Do not weaken, skip, or delete tests to make code pass.
- Do not add speculative features, infrastructure, or dependencies.
- Report assumptions, limitations, and validation failures honestly; never claim
  a test/check passed unless it was actually run.

## Go best practices

- Prefer the standard library; minimize dependencies. Check `go.mod` before
  adding or using a package.
- Keep packages cohesive; avoid premature abstraction; inspect existing code
  before introducing new abstractions.
- Use `context.Context` for I/O and long-running operations; respect cancellation
  and apply network/request timeouts.
- Wrap errors with useful context; use `errors.Is`/`errors.As`.
- Avoid panics for normal runtime errors; avoid global mutable state and data races.
- Bound concurrency, queues, retries, memory, and input sizes; close resources correctly.

## Security

Treat all external input as untrusted.

- SMTP listener binds to **localhost by default**; never expose an open relay
  (never `0.0.0.0` unless explicitly specified).
- Use TLS/HTTPS for outbound provider communication; never disable certificate verification.
- Apply connection, request, message, attachment, and recipient size limits.
- Prevent SMTP header/CRLF injection; validate/canonicalize email addresses at the boundary.
- Never hardcode, log, or commit secrets (API keys, passwords, authorization headers,
  email bodies). Credentials come only from configured environment/secret mechanisms.
- Do not expose secrets, stack traces, or internal details through APIs or health endpoints.
- Run containers as non-root with minimal privileges.

## Docker

- Build images on demand; no required CI/CD or staging environment.
- Prefer minimal, reproducible, multi-stage images; run as a non-root user.
- Do not include secrets or unnecessary build/runtime files.
- Handle graceful shutdown; persist queue/state only in the configured location.

## Git & commit discipline

- AI agents MUST NOT commit, amend, push, branch, tag, or release. These are developer-only.
- Final code-review and commit decisions are developer-only.

## Validation (run before declaring done)

```bash
gofmt -w .
go test ./...
go test -race ./...   # for concurrent code
go vet ./...
```

Run additional security/static-analysis checks when relevant.

## Definition of done

- Requirement exists in `SPEC.md`; tests written first and proven failing.
- Implementation satisfies `SPEC.md`; targeted and full test suites pass (incl. `-race` where concurrent).
- `go vet ./...` clean; `gofmt` clean; no unrelated scope expansion; no weakened tests.
- Security and modularity rules respected; `SPEC.md` in sync with code.
- No secrets or unrelated changes introduced.

## Anti-hallucination

- Don't invent features/config keys not in `SPEC.md` or user instructions.
- Don't assume a package or dependency exists — check `go.mod` first.
- Don't create documentation files unless explicitly requested.
- Never commit unless explicitly asked.
