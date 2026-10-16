# Configuration

`smtpfiy` uses YAML configuration files.

## Configuration Files

At least **one configuration file is required**.

A single file may contain both `server` and `domains` configuration.

Configuration may also be split across multiple files for easier maintenance.

Example: `/etc/smtpfiy.conf` or `/etc/smtpfiy/smtpfiy.yaml`

### Default Configuration Paths

If `-c` is not specified, `smtpfiy` searches:

```text
/etc/smtpfiy.conf
/etc/smtpfiy/smtpfiy.yaml
```

The service must find at least one valid configuration file.

---

## Custom Configuration

Use `-c` to specify one or more configuration files. Multiple files are comma-separated:

```bash
smtpfiy -c /etc/mysmtp.yaml

## OR
smtpfiy -c /etc/sdeploy/main.yaml,/etc/sdeploy/providers.yaml

## OR

smtpfiy -c /sdeploy/smtpfiy.conf,/sdeploy/myconfig.yaml
```

`-c` overrides the default configuration paths.

---

## Multiple Configuration Files

Configuration files are loaded and merged into one in-memory configuration.

A single file can contain everything:

```yaml
server:
  host: "127.0.0.1"
  port: 7001
  smtp_host: "127.0.0.1"
  smtp_port: 2525
  db_path: "/smtpfiy/data/smtpfiy.db"
  scheduler: order
  retry: 1
  retry_delay: 60
  log_level: info
  log_file: "/smtpfiy/log/smtpfiy.log"

domains:
  example.com:
    providers:
      - provider: brevo
        api_key: "..."
        limit_daily: 100
        limit_monthly: 3000
```

Or the configuration can be split:

```text
main.yaml
providers.yaml
```

`main.yaml`:

```yaml
server:
  host: "127.0.0.1"
  port: 7001
  smtp_host: "127.0.0.1"
  smtp_port: 2525
  db_path: "/smtpfiy/data/smtpfiy.db"
  scheduler: order
  retry: 1
  retry_delay: 60
  log_level: info
  log_file: "/smtpfiy/log/smtpfiy.log"
```

`providers.yaml`:

```yaml
domains:
  example.com:
    providers:
      - provider: brevo
        api_key: "..."
        limit_daily: 100
        limit_monthly: 3000
```

---

## Duplicate Configuration

Duplicate configuration entries are **errors**.

`smtpfiy` must not silently overwrite, merge, or choose between duplicate entries.

For example, if two files define:

```yaml
domains:
  example.com:
```

startup must fail with a clear error identifying the duplicate configuration.

This ensures deterministic behavior and prevents configuration mismatches.

---

## Configuration Structure

### `server`

Global service configuration:

```yaml
server:
  host: "127.0.0.1"
  port: 7001
  smtp_host: "127.0.0.1"
  smtp_port: 2525
  db_path: "/smtpfiy/data/smtpfiy.db"
  scheduler: order
  retry: 1
  retry_delay: 60
  log_level: info
  log_file: "/smtpfiy/log/smtpfiy.log"
```

- `host` - HTTP bind address (default: 127.0.0.1)
- `port` - HTTP port (default: 7001)
- `smtp_host` - SMTP bind address (default: 127.0.0.1)
- `smtp_port` - SMTP port (default: 2525)
- `db_path` - SQLite database path (default: `/smtpfiy/data/smtpfiy.db`)
- `scheduler` - Provider selection method (order|rr) (default: order)
- `retry` - Maximum retry attempts; 0 disables retry (default: 1)
- `retry_delay` - Delay between retries in seconds (default: 60)
- `log_level` - `debug`, `info` (default), `warn`, or `error`
- `log_file` - Log file path (default: `/smtpfiy/log/smtpfiy.log`)

### `domains`

Defines sender domains and their providers:

```yaml
domains:
  example.com:
    providers:
      - provider: brevo
        api_key: "..."
        limit_daily: 100
        limit_monthly: 3000
```

- `provider`: Provider adapter name
- `api_key`: Provider API key
- `limit_daily`: Maximum emails per day
- `limit_monthly`: Maximum emails per month

Provider fallback is always restricted to providers configured for the **same sender domain**.

---

## Configuration Loading

```text
-c specified -> load specified files -> validate -> reject duplicates -> merge -> load into memory -> start smtpfiy
```

If no `-c` is provided:

```text
/etc/smtpfiy.conf
/etc/smtpfiy/smtpfiy.yaml
    ↓
find configuration
    ↓
load
    ↓
validate
    ↓
start smtpfiy
```

At least one configuration file must be available.
