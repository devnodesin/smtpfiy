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
