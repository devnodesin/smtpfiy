# Logging

`smtpfiy` uses simple operational logging focused on service lifecycle and email delivery.

## Log Format

Each log entry uses:

```text
timestamp LEVEL message
```

Email events additionally include a short message ID and relevant delivery information.

## Service Startup

Example:

```text
2026-10-16T10:30:00Z INFO  starting smtpfiy
2026-10-16T10:30:00Z INFO  configuration loaded
2026-10-16T10:30:00Z INFO  database opened        path=/smtpfiy/data/smtpfiy.db
2026-10-16T10:30:00Z INFO  SMTP server listening  address=127.0.0.1:2525
2026-10-16T10:30:00Z INFO  HTTP server listening  address=127.0.0.1:7001
2026-10-16T10:30:00Z INFO  started smtpfiy
```

## Email Events

Only important email delivery events are logged.

### SMTP

Email received by the SMTP server:

```text
2026-10-16T10:30:00Z INFO  SMTP  id:8f21a4 (alerts@example.com -> user@example.net)
```

### SENT

Email successfully sent through a provider:

```text
2026-10-16T10:30:02Z INFO  SENT  id:8f21a4 via resend (alerts@example.com -> user@example.net)
```

### FAIL

Provider delivery failed:

```text
2026-10-16T10:40:00Z ERROR FAIL  id:b73d11 via brevo (connection timeout, retry)
```

Provider fallback:

```text
2026-10-16T10:40:00Z ERROR FAIL  id:b73d11 via brevo (connection timeout, trying next)
2026-10-16T10:40:01Z INFO  SENT  id:b73d11 via resend (alerts@example.com -> user@example.net)
```

Permanent failure:

```text
2026-10-16T10:45:00Z ERROR FAIL  id:c91d22 (all providers failed)
```

## Log Levels

- `debug` - Detailed diagnostic information
- `info` - Normal service operations
- `warn` - Recoverable problems or unusual conditions
- `error` - Failed operations

Configured with:

```yaml
server:
  log_level: info
  log_file: "/smtpfiy/log/smtpfiy.log"
```

## Queue Monitoring

Queue activity is **not logged**.

Queue status and statistics are available through:

```text
/stats
/metrics
```

This keeps the normal log focused on:

```text
SMTP
SENT
FAIL
```

## Sensitive Data

Logs must not contain:

- Email body
- Provider API keys
- Passwords
- Authentication credentials
- Sensitive HTTP headers

Email addresses may be logged as shown in the examples.
