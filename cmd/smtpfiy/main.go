package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	var configPaths configList
	flag.Var(&configPaths, "c", "comma-separated list of YAML configuration files (overrides default search paths)")
	flag.Parse()

	if err := run(configPaths.paths...); err != nil {
		fmt.Fprintf(os.Stderr, "smtpfiy: %v\n", err)
		os.Exit(1)
	}
}

func run(paths ...string) error {
	cfg, err := Load(paths...)
	if err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}
	if err := cfg.ValidateLog(); err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}

	logger, err := NewFromConfig(os.Stdout, cfg.Server.LogLevel, cfg.Server.LogFile)
	if err != nil {
		return fmt.Errorf("logger error: %w", err)
	}
	defer logger.Close()

	logger.Info("starting smtpfiy")
	logger.Info("configuration loaded")

	queue, err := OpenQueue(cfg.AbsDBPath())
	if err != nil {
		return fmt.Errorf("queue error: %w", err)
	}
	defer queue.Close()

	scheduler := NewScheduler(cfg.Server.Scheduler)
	resolver := &configResolver{cfg: cfg, scheduler: scheduler}

	// SMTP server: stores received messages into the queue.
	smtpSrv := &SMTPServer{
		Host:            cfg.Server.SMTPHost,
		Port:            cfg.Server.SMTPPort,
		MaxRecipients:   100,
		MaxMessageSize:  25 * 1024 * 1024,
		Store:           queue,
		Logger:          logger,
		AllowedDomains:  domainSet(cfg.Domains),
		Auth:            &cfg.Server.SMTPAuth,
		MaxAuthAttempts: Defaults.MaxAuthAttempts,
	}
	if err := smtpSrv.SetupTLS(cfg.Server.SMTPTLS, cfg.Server.SMTPHost); err != nil {
		return fmt.Errorf("smtp tls setup: %w", err)
	}
	if err := smtpSrv.Start(); err != nil {
		return fmt.Errorf("smtp server error: %w", err)
	}
	logger.Info("SMTP server listening  address=%s:%d", cfg.Server.SMTPHost, cfg.Server.SMTPPort)

	// HTTP server for health/stats.
	httpSrv := NewHTTPServer(cfg.Server.Host, cfg.Server.Port, queue, logger)
	if err := httpSrv.Listen(); err != nil {
		return fmt.Errorf("http server error: %w", err)
	}
	logger.Info("HTTP server listening  address=%s:%d", cfg.Server.Host, cfg.Server.Port)

	// Delivery worker loop.
	worker := &DeliveryWorker{
		Queue:       queue,
		Resolver:    resolver,
		MaxAttempts: cfg.Server.Retry,
		RetryDelay:  time.Duration(cfg.Server.RetryDelay) * time.Second,
		Logger:      logger,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go workerLoop(ctx, worker, logger)

	logger.Info("started smtpfiy")

	// Signal handling for graceful shutdown.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	logger.Info("shutting down smtpfiy")
	cancel()
	_ = smtpSrv.Shutdown(context.Background())
	_ = httpSrv.Shutdown(context.Background())
	return nil
}

// workerLoop repeatedly processes pending messages until ctx is cancelled.
func workerLoop(ctx context.Context, w *DeliveryWorker, logger *Logger) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				pending, err := w.Queue.Pending()
				if err != nil || len(pending) == 0 {
					break
				}
				// Bound each message's processing with a per-attempt context
				// timeout so a stalled provider request cannot block the loop
				// indefinitely. The delivery worker applies its own retry
				// delay, but we cap total work per message here.
				workCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				procErr := w.ProcessOne(workCtx)
				cancel()
				if procErr != nil {
					logger.Error("delivery worker error: %v", procErr)
					break
				}
			}
		}
	}
}

// configResolver implements ProviderResolver using the loaded configuration and
// scheduler. Only providers configured for the message's sender domain are
// returned, preserving the same-domain fallback rule.
type configResolver struct {
	cfg       *Config
	scheduler *Scheduler
}

func (r *configResolver) ProvidersForDomain(domain string) []Provider {
	dc, ok := r.cfg.Domains[domain]
	if !ok {
		return nil
	}
	return r.scheduler.Select(domain, dc.Providers)
}

func domainSet(domains map[string]DomainConfig) map[string]bool {
	out := make(map[string]bool, len(domains))
	for d := range domains {
		out[d] = true
	}
	return out
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
