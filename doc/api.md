# API

`smtpfiy` provides HTTP endpoints for health monitoring and service statistics.

## Endpoints

```text
GET /health
GET /stats
```

---

## `/health`

Health endpoint for Docker and service monitoring.

### Request

```http
GET /health
```

### Healthy Response

HTTP `200`:

```json
{
  "status": "ok"
}
```

### Unhealthy Response

Return an HTTP `5xx` status when `smtpfiy` is unable to operate normally:

```json
{
  "status": "error"
}
```

The endpoint must be lightweight and suitable for frequent Docker health checks.

### Docker Healthcheck

```yaml
healthcheck:
  test: ["CMD", "curl", "-f", "http://localhost:7001/health"]
  interval: 30s
  timeout: 5s
  retries: 3
```

---

## `/stats`

Returns service statistics and detailed message information.

### Request

```http
GET /stats
```

### Response

```json
{
  "uptime": "1h23m",
  "database": {
    "path": "/smtpfiy/data/smtpfiy.db",
    "size": "2.4 MB"
  },
  "messages": {
    "total": 120,
    "pending": 4,
    "processing": 1,
    "sent": 112,
    "failed": 3
  },
  "items": [
    {
      "id": "8f21a4",
      "from": "alerts@example.com",
      "to": ["user@example.net"],
      "subject": "Server Alert",
      "provider": "resend",
      "status": "sent",
      "attempts": 1,
      "created_at": "2026-10-16T10:30:00Z",
      "sent_at": "2026-10-16T10:30:02Z"
    },
    {
      "id": "b73d11",
      "from": "alerts@example.com",
      "to": ["admin@example.net"],
      "subject": "Daily Report",
      "provider": "brevo",
      "status": "failed",
      "attempts": 2,
      "created_at": "2026-10-16T10:40:00Z",
      "last_error": "connection timeout"
    }
  ]
}
```

### Message Details

Each message may include:

- `id`
- `from`
- `to`
- `subject`
- `provider`
- `status`
- `attempts`
- `created_at`
- `sent_at`
- `failed_at`
- `last_error`

The email body must **not** be returned.

## Endpoint Purpose

```text
/health  → Docker/service health check
/stats   → Service summary + detailed message inspection
```

This keeps the HTTP API minimal and avoids having two endpoints exposing essentially the same information.
