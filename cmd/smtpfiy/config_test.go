package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp %q: %v", name, err)
	}
	return path
}

const singleServer = `
server:
  host: "127.0.0.1"
  port: 7001
  smtp_host: "127.0.0.1"
  smtp_port: 2525
  db_path: "/tmp/smtpfiy.db"
  scheduler: order
  retry: 1
  retry_delay: 60
  log_level: info
  log_file: "/tmp/smtpfiy.log"
`

const domainBlock = `
domains:
  example.com:
    providers:
      - provider: resend
        api_key: "key-123"
        limit_daily: 100
        limit_monthly: 3000
`

func TestLoadSMTPAuth(t *testing.T) {
	const authServer = `
server:
  smtp_auth:
    required: true
    users:
      - username: "resend"
        password: "changeme"
`
	path := writeTemp(t, "main.yaml", authServer+domainBlock)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Server.SMTPAuth.SMTPAuthEnabled() {
		t.Fatal("expected SMTP AUTH to be enabled")
	}
	if !cfg.Server.SMTPAuth.Required {
		t.Fatal("expected required: true")
	}
	if len(cfg.Server.SMTPAuth.Users) != 1 || cfg.Server.SMTPAuth.Users[0].Username != "resend" {
		t.Fatalf("unexpected users: %+v", cfg.Server.SMTPAuth.Users)
	}

	// Empty users means disabled (backward compatible).
	disabled := writeTemp(t, "disabled.yaml", singleServer+domainBlock)
	cfg2, err := Load(disabled)
	if err != nil {
		t.Fatalf("Load disabled: %v", err)
	}
	if cfg2.Server.SMTPAuth.SMTPAuthEnabled() {
		t.Fatal("expected SMTP AUTH disabled when users empty")
	}
}

func TestLoadMergeMultipleFiles(t *testing.T) {
	main := writeTemp(t, "main.yaml", singleServer)
	prov := writeTemp(t, "providers.yaml", domainBlock)

	cfg, err := Load(main, prov)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Port != 7001 {
		t.Fatalf("server not merged: %+v", cfg.Server)
	}
	if len(cfg.Domains) != 1 || cfg.Domains["example.com"].Providers[0].Provider != "resend" {
		t.Fatalf("domains not merged: %+v", cfg.Domains)
	}
}

func TestLoadDuplicateDomainRejected(t *testing.T) {
	a := writeTemp(t, "a.yaml", domainBlock)
	b := writeTemp(t, "b.yaml", domainBlock)

	_, err := Load(a, b)
	if err == nil {
		t.Fatal("expected duplicate domain error, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
}

func TestLoadDuplicateServerRejected(t *testing.T) {
	a := writeTemp(t, "a.yaml", singleServer)
	b := writeTemp(t, "b.yaml", singleServer)

	_, err := Load(a, b)
	if err == nil {
		t.Fatal("expected duplicate server error, got nil")
	}
}

func TestLoadDefaultsApplied(t *testing.T) {
	path := writeTemp(t, "minimal.yaml", domainBlock)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Host != Defaults.Host {
		t.Fatalf("expected default host %q, got %q", Defaults.Host, cfg.Server.Host)
	}
	if cfg.Server.Port != Defaults.Port {
		t.Fatalf("expected default port %d, got %d", Defaults.Port, cfg.Server.Port)
	}
	if cfg.Server.Scheduler != Defaults.Scheduler {
		t.Fatalf("expected default scheduler %q, got %q", Defaults.Scheduler, cfg.Server.Scheduler)
	}
}

func TestValidateUnsupportedProvider(t *testing.T) {
	path := writeTemp(t, "bad.yaml", `
domains:
  example.com:
    providers:
      - provider: brevo
        api_key: "k"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected unsupported provider error, got nil")
	}
}

func TestValidateEmptyAPIKey(t *testing.T) {
	path := writeTemp(t, "bad.yaml", `
domains:
  example.com:
    providers:
      - provider: resend
        api_key: ""
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected empty api_key error, got nil")
	}
}

func TestLoadNoFileFound(t *testing.T) {
	_, err := Load("/nonexistent/path/to/config.yaml")
	if err == nil {
		t.Fatal("expected no config file error, got nil")
	}
}

func TestValidateLogInvalidLevel(t *testing.T) {
	path := writeTemp(t, "bad.yaml", `
server:
  log_level: verbose
`+domainBlock)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.ValidateLog(); err == nil {
		t.Fatal("expected invalid log_level error, got nil")
	}
}

func TestValidateLogValidLevel(t *testing.T) {
	path := writeTemp(t, "ok.yaml", singleServer+domainBlock)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.ValidateLog(); err != nil {
		t.Fatalf("expected valid log_level, got: %v", err)
	}
}

func TestStringMasksAPIKey(t *testing.T) {
	cfg := &Config{
		Server: DefaultsApplied(),
		Domains: map[string]DomainConfig{
			"example.com": {
				Providers: []ProviderConfig{
					{Provider: "resend", APIKey: "secret-key", LimitDaily: 100, LimitMonthly: 3000},
				},
			},
		},
	}
	out := cfg.String()
	if strings.Contains(out, "secret-key") {
		t.Fatalf("api key leaked in String(): %s", out)
	}
	if !strings.Contains(out, "api_key=***") {
		t.Fatalf("expected masked api key, got: %s", out)
	}
}
