package main

import (
	"context"
	"fmt"
	"time"
)

// ProviderResolver supplies the ordered providers to try for a sender domain.
type ProviderResolver interface {
	ProvidersForDomain(domain string) []Provider
}

// DeliveryWorker attempts delivery of pending messages through providers,
// applying the configured retry policy.
type DeliveryWorker struct {
	Queue       *Queue
	Resolver    ProviderResolver
	MaxAttempts int
	RetryDelay  time.Duration
	Logger      *Logger
}

// ProcessOne processes a single pending message: selects providers for its
// domain, attempts delivery in order with retries, and records the outcome in
// the queue. It returns an error only for internal failures (not delivery
// failures, which are recorded in the queue).
func (w *DeliveryWorker) ProcessOne(ctx context.Context) error {
	pending, err := w.Queue.Pending()
	if err != nil {
		return fmt.Errorf("delivery: load pending: %w", err)
	}
	if len(pending) == 0 {
		return nil
	}
	msg := pending[0]

	providers := w.Resolver.ProvidersForDomain(msg.Domain)
	if len(providers) == 0 {
		_ = w.Queue.MarkFailed(msg.ID, "no providers for domain", true)
		if w.Logger != nil {
			w.Logger.Error("FAIL  id:%s (no providers for domain %s)", msg.ID, msg.Domain)
		}
		return nil
	}

	delivered, lastErr := w.deliver(ctx, msg, providers)

	if delivered {
		_ = w.Queue.MarkSent(msg.ID, lastErr.providerName)
		if w.Logger != nil {
			w.Logger.Info("SENT  id:%s via %s (%s -> %s)",
				msg.ID, lastErr.providerName, msg.From, joinTo(msg.To))
		}
		return nil
	}

	// Terminal failure after all attempts/providers exhausted.
	_ = w.Queue.MarkFailed(msg.ID, lastErr.message, true)
	if w.Logger != nil {
		w.Logger.Error("FAIL  id:%s (%s)", msg.ID, lastErr.message)
	}
	return nil
}

// deliverResult records the outcome of a delivery attempt sequence.
type deliverResult struct {
	providerName string
	message      string
}

// deliver tries each provider in order, retrying retryable failures up to
// MaxAttempts total across providers. It returns whether delivery succeeded and
// the last error detail.
func (w *DeliveryWorker) deliver(ctx context.Context, msg Message, providers []Provider) (bool, deliverResult) {
	maxAttempts := w.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	var lastErr error
	var lastProvider string
	attempt := 0

	for _, p := range providers {
		// Try this provider until it succeeds, exhausts attempts, or fails
		// permanently (then fall through to the next provider).
		for attempt < maxAttempts {
			attempt++
			lastProvider = p.Name()
			id, err := p.Send(ctx, msg)
			if err == nil {
				return true, deliverResult{providerName: p.Name(), message: id}
			}
			lastErr = err

			if !isRetryable(err) {
				// Permanent (or non-retryable) failure: stop on this
				// provider and fall back to the next one.
				break
			}
			// Retryable: if attempts remain, wait then retry this provider.
			if attempt >= maxAttempts {
				break
			}
			if w.RetryDelay > 0 {
				if !sleepCtx(ctx, w.RetryDelay) {
					return false, deliverResult{providerName: lastProvider, message: ctx.Err().Error()}
				}
			}
		}
	}
	return false, deliverResult{providerName: lastProvider, message: errMessage(lastErr)}
}

// sleepCtx waits for d or until ctx is cancelled; returns false if cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func errMessage(err error) string {
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}

func isRetryable(err error) bool {
	var perr *ProviderError
	if asProviderError(err, &perr) {
		return perr.Retryable
	}
	// Network/timeout errors without a ProviderError are treated as retryable.
	return true
}

func joinTo(addrs []string) string {
	out := ""
	for i, a := range addrs {
		if i > 0 {
			out += ","
		}
		out += a
	}
	return out
}
