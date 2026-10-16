package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	var configPaths configList
	flag.Var(&configPaths, "c", "comma-separated list of YAML configuration files (overrides default search paths)")
	flag.Parse()

	cfg, err := Load(configPaths.paths...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "smtpify: configuration error: %v\n", err)
		os.Exit(1)
	}
	if err := cfg.ValidateLog(); err != nil {
		fmt.Fprintf(os.Stderr, "smtpify: configuration error: %v\n", err)
		os.Exit(1)
	}

	logger, err := NewFromConfig(os.Stdout, cfg.Server.LogLevel, cfg.Server.LogFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "smtpify: logger error: %v\n", err)
		os.Exit(1)
	}
	defer logger.Close()

	logger.Info("starting smtpify")
	logger.Info("configuration loaded")
	logger.Info("http  listen: %s:%d", cfg.Server.Host, cfg.Server.Port)
	logger.Info("smtp  listen: %s:%d", cfg.Server.SMTPHost, cfg.Server.SMTPPort)
	logger.Info("started smtpify")
}

// configList is a flag.Value accepting a comma-separated list of paths.
type configList struct {
	paths []string
}

func (c *configList) String() string {
	return strings.Join(c.paths, ",")
}

func (c *configList) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			c.paths = append(c.paths, p)
		}
	}
	return nil
}
