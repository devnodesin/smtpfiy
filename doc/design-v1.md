
I would build it as a small **`smtp-api-relay`** service rather than a generic mail server.

### Recommended architecture

```text
                         INTERNAL DOCKER NETWORK

┌───────────────┐
│ App 1         │
│ SMTP client   │
└───────┬───────┘
        │
┌───────▼───────┐
│ App 2         │
│ SMTP client   │
└───────┬───────┘
        │
        │ SMTP :2525
        ▼
┌──────────────────────────────────┐
│          smtp-api-relay          │
│                                  │
│  SMTP server                     │
│       │                          │
│       ▼                          │
│  MIME parser                     │
│       │                          │
│       ▼                          │
│  Email normalization              │
│       │                          │
│       ▼                          │
│  Persistent queue                │
│       │                          │
│       ▼                          │
│  Delivery worker                 │
│       │                          │
│       ├── Resend adapter         │
│       ├── Brevo adapter          │
│       └── Mailgun adapter        │
│                                  │
│  Metrics / logs / health         │
└──────────────┬───────────────────┘
               │ HTTPS
               ▼
       ┌─────────────────┐
       │ Email Provider  │
       └─────────────────┘
```

The important part is that **your applications know nothing about Resend/Brevo/etc.**

They only know:

```env
SMTP_HOST=smtp-api-relay
SMTP_PORT=2525
```

---

## I'd make the first version very small

Don't initially build a huge mail platform.

### V1

```text
SMTP
  ↓
MIME parser
  ↓
SQLite queue
  ↓
worker
  ↓
Brevo API
```

Then add providers later.

For example:

```yaml
environment:
  EMAIL_PROVIDER: brevo
  BREVO_API_KEY: xkeysib-xxxxxxxx
  SMTP_PORT: 2525
```

Brevo's current transactional API is `POST /v3/smtp/email`, and it accepts sender, recipients, subject, HTML/text content, attachments, CC/BCC, reply-to, headers, tags, etc. [Brevo](https://developers.brevo.com/docs/send-a-transactional-email?utm_source=chatgpt.com)

That maps very naturally from an incoming SMTP/MIME message.

---

# The queue is important

I would **not** do this:

```text
SMTP
 ↓
call Brevo API
 ↓
return SMTP response
```

Instead:

```text
SMTP
 ↓
parse
 ↓
store
 ↓
250 Queued
```

Then:

```text
                 ┌───────────────┐
                 │    SQLite     │
                 │               │
                 │ pending       │
                 │ sending       │
                 │ sent          │
                 │ failed        │
                 └───────┬───────┘
                         │
                         ▼
                       worker
                         │
                         ▼
                    Brevo API
```

That gives you resilience when the provider is temporarily unavailable.

For example:

```text
10:00:00  App sends email
10:00:00  Relay accepts it
10:00:00  Stored in SQLite
10:00:00  SMTP → 250 OK

10:00:01  Worker → Brevo
10:00:01  Brevo → 503

10:00:11  Retry
10:00:11  Brevo → 503

10:00:31  Retry
10:00:31  Brevo → 201
```

The application doesn't care that Brevo had a temporary failure.

---

# SQLite is enough initially

I wouldn't introduce Redis, RabbitMQ, Kafka, PostgreSQL, etc. for the first version.

Something as simple as:

```text
/data/relay.db
```

with a table like:

```text
emails
──────────────────────────────────
id
status
from_address
to_addresses
cc_addresses
bcc_addresses
subject
text_body
html_body
raw_message
provider
provider_message_id
attempts
next_attempt_at
created_at
sent_at
last_error
```

is perfectly reasonable.

The `raw_message` is particularly useful.

If MIME parsing goes wrong or you later discover that you need some header you weren't originally storing, you still have the original message.

---

# Provider abstraction

Internally I'd define something like:

```text
EmailProvider

Send(ctx, Email) → ProviderResult
```

Then:

```text
providers/
    brevo/
    resend/
    mailgun/
```

Conceptually:

```go
type Provider interface {
    Send(ctx context.Context, email Email) (Result, error)
}
```

Your queue worker doesn't care which provider is being used.

```text
worker
   │
   ▼
Provider.Send()
   │
   ├── Brevo
   ├── Resend
   └── Mailgun
```

This is the part I think will make the service particularly valuable.

---

# Add HTTP as well

I'd actually expose a small HTTP API from day one.

So the relay supports:

### SMTP

```text
smtp://smtp-api-relay:2525
```

and:

### HTTP

```http
POST http://smtp-api-relay:8080/v1/email
```

with:

```json
{
  "from": "noreply@example.com",
  "to": [
    "john@example.com"
  ],
  "subject": "Welcome",
  "text": "Welcome to our service",
  "html": "<h1>Welcome</h1>"
}
```

Both routes feed the **same queue**.

```text
             ┌── SMTP ─────┐
             │             │
Apps ────────┤             ▼
             │          Normalizer
             │             │
             └── HTTP ─────┘
                           │
                           ▼
                         Queue
                           │
                           ▼
                         Worker
                           │
                           ▼
                        Provider
```

That gives you a nice migration path.

Old applications:

```text
SMTP
```

New applications:

```text
HTTP API
```

---

# Provider configuration

I'd keep the configuration extremely simple.

```yaml
provider: brevo

smtp:
  listen: "0.0.0.0:2525"

http:
  listen: "0.0.0.0:8080"

queue:
  database: "/data/relay.db"

brevo:
  api_key: "${BREVO_API_KEY}"
```

Later:

```yaml
provider: resend
```

or:

```yaml
provider: mailgun
```

---

# Authentication

Since this will normally be inside Docker, you can initially restrict SMTP access to the Docker network.

But I'd still support SMTP authentication:

```text
username: app
password: xxx
```

and perhaps multiple credentials:

```text
app1 → credential 1
app2 → credential 2
monitoring → credential 3
```

This gives you the ability to identify which application generated a message.

For example:

```text
X-Relay-App: nextcloud
```

or internally:

```text
source_app = nextcloud
```

That becomes very useful for debugging.

---

# Logging

I'd want logs like:

```text
2026-10-15T10:31:22Z INFO  smtp connection
2026-10-15T10:31:22Z INFO  message accepted
    id=8f31c2
    from=noreply@example.com
    to=user@example.com
    source=nextcloud

2026-10-15T10:31:23Z INFO  delivery started
    id=8f31c2
    provider=brevo

2026-10-15T10:31:23Z INFO  delivery successful
    id=8f31c2
    provider_message_id=...
```

But **never log the API key** and preferably don't log complete email bodies.

---

# Metrics

A few Prometheus metrics would make this much easier to operate:

```text
relay_emails_received_total
relay_emails_queued
relay_emails_sent_total
relay_emails_failed_total
relay_emails_retry_total

relay_delivery_duration_seconds
relay_queue_size
```

Then Grafana can show:

```text
Emails/minute
Queue depth
Delivery latency
Failures
Provider errors
```

---

# Webhooks

I'd also eventually add:

```text
Brevo
   │
   │ webhook
   ▼
smtp-api-relay
   │
   ├── delivered
   ├── bounced
   ├── blocked
   ├── opened
   ├── clicked
   └── spam
```

Brevo currently supports transactional webhook events including delivered, opened, clicked, soft/hard bounce, deferred, spam, blocked, etc. [Brevo](https://developers.brevo.com/docs/send-a-transactional-email?utm_source=chatgpt.com)

That means your relay can eventually maintain:

```text
email
  │
  ├── queued
  ├── submitted
  ├── delivered
  ├── bounced
  └── failed
```

rather than simply knowing whether the API accepted the request.

---

# Technology choice

For this particular service, **I'd use Go**.

Something like:

```text
Go
├── SMTP server
├── MIME parsing
├── HTTP API
├── SQLite
├── background worker
├── provider adapters
├── Prometheus metrics
└── Docker
```

Why Go?

- Single binary
- Tiny Docker image
- Excellent concurrency
- Excellent networking support
- Easy deployment
- No runtime dependency
- Very good fit for a daemon
- Easy to run multiple workers

You could make the entire first version a fairly small project.

---

# One thing I would NOT do

I wouldn't expose the SMTP listener publicly.

Don't do:

```text
Internet
   │
   ▼
:25
   │
   ▼
smtp-api-relay
```

unless you deliberately intend to operate a public mail server.

Instead:

```text
Docker network
     │
     ▼
smtp-api-relay:2525
```

and only the HTTP management/API endpoint should potentially be exposed, preferably behind authentication/reverse proxy.

---

## My proposed V1

I'd make the first version:

```text
┌───────────────────────────────────────┐
│           smtp-api-relay              │
│                                       │
│  SMTP :2525                           │
│       │                               │
│       ▼                               │
│  MIME parser                          │
│       │                               │
│       ▼                               │
│  SQLite                               │
│       │                               │
│       ▼                               │
│  Worker ────────────────┐             │
│                         │             │
│                    Brevo API          │
│                         │             │
│  HTTP :8080             │             │
│       │                 │             │
│       └──────► SQLite ◄─┘             │
│                                       │
│  /health                              │
│  /metrics                             │
└───────────────────────────────────────┘
```

**V1 provider: Brevo.**

Then once this works, add:

```text
Resend
Mailgun
SMTP2GO
```

as adapters.

This also gives you a very clean separation: **applications deal with SMTP/your API; the relay deals with email providers.**

If you want to proceed with this, I can lay out the **actual Docker Compose + Go project structure + SMTP protocol handling + SQLite schema + Brevo adapter** for a production-oriented V1.
