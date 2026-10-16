package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Defaults holds all fallback default values. Business logic must reference
// these via the Defaults struct rather than hardcoding literals.
var Defaults = struct {
	Host        string
	Port        int
	SMTPHost    string
	SMTPPort    int
	DBPath      string
	Scheduler   string
	Retry       int
	RetryDelay  int
	LogLevel    string
	LogFile     string
	SearchPaths []string
}{
	Host:       "127.0.0.1",
	Port:       7001,
	SMTPHost:   "127.0.0.1",
	SMTPPort:   2525,
	DBPath:     "/smtpfiy/data/smtpfiy.db",
	Scheduler:  "order",
	Retry:      1,
	RetryDelay: 60,
	LogLevel:   "info",
	LogFile:    "/smtpfiy/log/smtpfiy.log",
	SearchPaths: []string{
		"/etc/smtpfiy.conf",
		"/etc/smtpfiy/smtpfiy.yaml",
	},
}

// supportedProviders is the set of provider adapters recognized by V1.
// Resend is the only configured adapter initially; others will be added later.
var supportedProviders = map[string]bool{
	"resend": true,
}

// ServerConfig holds global service configuration.
type ServerConfig struct {
	Host       string `yaml:"host"`
	Port       int    `yaml:"port"`
	SMTPHost   string `yaml:"smtp_host"`
	SMTPPort   int    `yaml:"smtp_port"`
	DBPath     string `yaml:"db_path"`
	Scheduler  string `yaml:"scheduler"`
	Retry      int    `yaml:"retry"`
	RetryDelay int    `yaml:"retry_delay"`
	LogLevel   string `yaml:"log_level"`
	LogFile    string `yaml:"log_file"`
}

// ProviderConfig describes a single provider configured for a sender domain.
type ProviderConfig struct {
	Provider     string `yaml:"provider"`
	APIKey       string `yaml:"api_key"`
	LimitDaily   int    `yaml:"limit_daily"`
	LimitMonthly int    `yaml:"limit_monthly"`
}

// DomainConfig holds the providers configured for a single sender domain.
type DomainConfig struct {
	Providers []ProviderConfig `yaml:"providers"`
}

// rawConfig mirrors the on-disk YAML file structure for a single file.
type rawConfig struct {
	Server  ServerConfig            `yaml:"server"`
	Domains map[string]DomainConfig `yaml:"domains"`
}

// Config is the merged, validated in-memory configuration.
type Config struct {
	Server  ServerConfig
	Domains map[string]DomainConfig
}

// maskedAPIKey returns a redacted representation of an API key.
func maskedAPIKey(key string) string {
	if key == "" {
		return ""
	}
	return "***"
}

// String returns a diagnostics-safe representation of the configuration with
// all API keys masked.
func (c *Config) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "server: host=%s port=%d smtp_host=%s smtp_port=%d db_path=%s scheduler=%s retry=%d retry_delay=%d log_level=%s log_file=%s\n",
		c.Server.Host, c.Server.Port, c.Server.SMTPHost, c.Server.SMTPPort,
		c.Server.DBPath, c.Server.Scheduler, c.Server.Retry, c.Server.RetryDelay,
		c.Server.LogLevel, c.Server.LogFile)
	for domain, dc := range c.Domains {
		fmt.Fprintf(&b, "domain: %s\n", domain)
		for _, p := range dc.Providers {
			fmt.Fprintf(&b, "  provider=%s api_key=%s limit_daily=%d limit_monthly=%d\n",
				p.Provider, maskedAPIKey(p.APIKey), p.LimitDaily, p.LimitMonthly)
		}
	}
	return b.String()
}

// resolvePaths determines which configuration files to load. When paths are
// provided they are used directly; otherwise the default search paths are used.
func resolvePaths(paths []string) ([]string, error) {
	if len(paths) > 0 {
		return paths, nil
	}
	return Defaults.SearchPaths, nil
}

// loadFile reads and decodes a single YAML configuration file.
func loadFile(path string) (*rawConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %q: %w", path, err)
	}
	var rc rawConfig
	if err := yaml.Unmarshal(data, &rc); err != nil {
		return nil, fmt.Errorf("parsing config %q: %w", path, err)
	}
	return &rc, nil
}

// merge applies a single file's contents into the accumulator. It rejects
// duplicate domain entries rather than silently overwriting them.
func merge(acc *rawConfig, rc *rawConfig, path string) error {
	if acc.Server == (ServerConfig{}) {
		acc.Server = rc.Server
	} else if rc.Server != (ServerConfig{}) {
		// Server block may only be defined once across all files.
		return fmt.Errorf("duplicate server configuration in %q", path)
	}
	for domain, dc := range rc.Domains {
		if _, exists := acc.Domains[domain]; exists {
			return fmt.Errorf("duplicate configuration for domain %q in %q", domain, path)
		}
		acc.Domains[domain] = dc
	}
	return nil
}

// Load loads one or more YAML configuration files, merges them, applies
// defaults, and validates the result. At least one readable file is required.
func Load(paths ...string) (*Config, error) {
	resolved, err := resolvePaths(paths)
	if err != nil {
		return nil, err
	}

	found := false
	acc := &rawConfig{Domains: map[string]DomainConfig{}}
	for _, p := range resolved {
		info, statErr := os.Stat(p)
		if statErr != nil {
			continue
		}
		// Reject non-regular files (e.g. directories, sockets, devices).
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("config path %q is not a regular file", p)
		}
		found = true
		rc, loadErr := loadFile(p)
		if loadErr != nil {
			return nil, loadErr
		}
		if err := merge(acc, rc, p); err != nil {
			return nil, err
		}
	}
	if !found {
		return nil, fmt.Errorf("no configuration file found in: %s", strings.Join(resolved, ", "))
	}

	cfg := &Config{
		Server:  applyServerDefaults(acc.Server),
		Domains: acc.Domains,
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// applyServerDefaults fills any unset server fields with centralized defaults.
func applyServerDefaults(s ServerConfig) ServerConfig {
	if s.Host == "" {
		s.Host = Defaults.Host
	}
	if s.Port == 0 {
		s.Port = Defaults.Port
	}
	if s.SMTPHost == "" {
		s.SMTPHost = Defaults.SMTPHost
	}
	if s.SMTPPort == 0 {
		s.SMTPPort = Defaults.SMTPPort
	}
	if s.DBPath == "" {
		s.DBPath = Defaults.DBPath
	}
	if s.Scheduler == "" {
		s.Scheduler = Defaults.Scheduler
	}
	if s.Retry == 0 {
		s.Retry = Defaults.Retry
	}
	if s.RetryDelay == 0 {
		s.RetryDelay = Defaults.RetryDelay
	}
	if s.LogLevel == "" {
		s.LogLevel = Defaults.LogLevel
	}
	if s.LogFile == "" {
		s.LogFile = Defaults.LogFile
	}
	return s
}

// supportedLogLevels is the set of valid log_level values.
var supportedLogLevels = map[string]bool{
	"debug": true,
	"info":  true,
	"warn":  true,
	"error": true,
}

// ValidateLog enforces that the configured log_level is one of the supported
// values (debug/info/warn/error).
func (c *Config) ValidateLog() error {
	if !supportedLogLevels[c.Server.LogLevel] {
		return fmt.Errorf("invalid log_level %q (must be one of debug, info, warn, error)", c.Server.LogLevel)
	}
	return nil
}
func (c *Config) Validate() error {
	switch c.Server.Scheduler {
	case "order", "rr":
	default:
		return fmt.Errorf("invalid scheduler %q (must be order or rr)", c.Server.Scheduler)
	}
	if len(c.Domains) == 0 {
		return errors.New("at least one sender domain must be configured")
	}
	for domain, dc := range c.Domains {
		if len(dc.Providers) == 0 {
			return fmt.Errorf("domain %q has no providers configured", domain)
		}
		for _, p := range dc.Providers {
			if !supportedProviders[p.Provider] {
				return fmt.Errorf("domain %q uses unsupported provider %q", domain, p.Provider)
			}
			if p.APIKey == "" {
				return fmt.Errorf("domain %q provider %q has empty api_key", domain, p.Provider)
			}
		}
	}
	return nil
}

// DefaultSearchPaths returns a copy of the default config search paths.
func DefaultSearchPaths() []string {
	out := make([]string, len(Defaults.SearchPaths))
	copy(out, Defaults.SearchPaths)
	return out
}

// DefaultsApplied returns a ServerConfig fully populated with defaults. Useful
// for tests and for building a baseline config.
func DefaultsApplied() ServerConfig {
	return applyServerDefaults(ServerConfig{})
}

// AbsDBPath returns the database path, made absolute relative to the current
// working directory when it is relative.
func (c *Config) AbsDBPath() string {
	if filepath.IsAbs(c.Server.DBPath) {
		return c.Server.DBPath
	}
	wd, err := os.Getwd()
	if err != nil {
		return c.Server.DBPath
	}
	return filepath.Join(wd, c.Server.DBPath)
}
