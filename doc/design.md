# smtpfiy Design

`smtpfiy` is a small self-hosted email relay service that accepts email over SMTP and delivers it through provider REST APIs.

Applications and containers send email to `smtpfiy` using SMTP. `smtpfiy` receives the message, stores it in a persistent SQLite queue, selects an eligible provider, and delivers the message through the provider API.

## Architecture

```text
Application / Container
        |
        | SMTP
        v
   +-----------+
   |  smtpfiy  |
   +-----------+
        |
        v
   SQLite Queue
        |
        v
   Provider Scheduler
        |
        +---------> Brevo API
        |
        +---------> Resend API
        |
        +---------> Mailgun API
        |
        +---------> SMTP2GO API
```

## Main Components

### SMTP Server

Accepts email from applications and containers.

The SMTP server:

- Accepts SMTP connections.
- Parses incoming MIME messages.
- Determines the sender domain from the `From` address.
- Assigns a unique message ID.
- Stores the message in the persistent queue.

The SMTP listener is configurable through the service configuration.

### HTTP Server

Provides the service HTTP endpoints:

- `/health` — Docker/service health check.
- `/stats` — service statistics and detailed message inspection.

See [`api.md`](api.md) for the HTTP API definition.

### SQLite Queue

SQLite is used as the persistent message queue and service state database.

The queue provides:

- Persistence across container restarts.
- Message status tracking.
- Retry state.
- Provider delivery state.
- Message metadata.

The database path is configurable.

### Provider Adapters

`smtpfiy` uses provider adapters to deliver email through provider REST APIs.

Providers are configured per sender domain.

Example providers include:

- Brevo
- Resend
- Mailgun
- SMTP2GO

Each provider adapter is responsible for translating the internal email representation into the provider's API format and handling the provider response.

### Provider Selection

Multiple providers may be configured for the same sender domain.

Provider selection is controlled by the configured scheduler:

- `order` — try providers in configuration order.
  - MAIL 1, MAIL 2, MAIL  -> provider 1 (limits reach)
- `rr` — use round-robin selection.
  - MAIL 1 -> provider 1
  - MAIL 2 -> provider 2
  - MAIL 3 -> provider 3

Provider fallback is restricted to providers configured for the **same sender domain**.

`smtpfiy` must never silently change the sender domain to use another provider configuration.

## Message Lifecycle

```text
SMTP receive
     |
     v
Validate / parse
     |
     v
Store in SQLite
     |
     v
Select provider
     |
     v
Send through provider API
     |
     +------ success ------> SENT
     |
     +------ failure ------> retry / next provider
                                  |
                                  v
                                FAIL
```

The queue is persistent, so messages are not lost when the service restarts.

## Retry

Provider delivery failures may be retried according to the configured retry settings.

Retry behavior is controlled by:

- Maximum retry attempts.
- Retry delay.
- Provider availability.
- Configured provider selection strategy.

See [`config.md`](config.md) for the configuration settings.

## Configuration

`smtpfiy` uses one or more external YAML configuration files.

Configuration covers:

- HTTP server.
- SMTP server.
- SQLite database.
- Scheduler.
- Retry behavior.
- Logging.
- Sender domains.
- Provider API keys.
- Provider limits.

Configuration files may be combined or split into multiple files.

Duplicate configuration entries are rejected.

See [`config.md`](config.md) for the complete configuration specification.

## Logging

`smtpfiy` provides simple operational logging focused on:

```text
SMTP
SENT
FAIL
```

Queue activity is not logged as normal operational events. Queue and message state can be inspected through `/stats`.

See [`logging.md`](logging.md) for the logging format, events, levels, and sensitive-data rules.

## HTTP API

The HTTP API is intentionally small:

```text
GET /health
GET /stats
```

`/health` is intended for Docker health checks.

`/stats` provides service-level statistics and detailed information about queued and processed messages.

See [`api.md`](api.md) for the complete API specification.

## Security

Provider API keys are stored in the external configuration and must not be written to logs or exposed through HTTP endpoints.

Email bodies must not be exposed through `/stats`.

`smtpfiy` does not provide user login or authentication configuration in V1.

## Deployment

`smtpfiy` is designed to run as a small Docker container.

Typical deployment:

```text
Docker Network
     |
     +-- Application
     |
     +-- smtpfiy
            |
            +-- SQLite volume
            +-- Configuration volume
            +-- Log volume
```

The SMTP port is exposed to applications that need to send email.

The HTTP port is used for health monitoring and operational inspection.

## Design Goals

The initial design prioritizes:

- Small operational footprint.
- Simple configuration.
- Persistent delivery queue.
- Provider abstraction.
- Provider fallback within the same sender domain.
- Simple HTTP API.
- Simple operational logging.
- Docker-friendly deployment.
- No unnecessary infrastructure dependencies.
