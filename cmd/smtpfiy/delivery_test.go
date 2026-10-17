package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// fakeProvider simulates a provider with configurable behavior for tests.
type fakeProvider struct {
	name      string
	failTimes int32 // number of attempts that should fail (retryable)
	permanent bool
	calls     int32
	lastMsgID string
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Send(ctx context.Context, msg Message) (string, error) {
	n := atomic.AddInt32(&f.calls, 1)
	if f.permanent {
		return "", &ProviderError{Provider: f.name, StatusCode: 401, Retryable: false, Message: "permanent"}
	}
	if n <= atomic.LoadInt32(&f.failTimes) {
		return "", &ProviderError{Provider: f.name, StatusCode: 503, Retryable: true, Message: "retryable"}
	}
	f.lastMsgID = "prov-" + msg.ID
	return f.lastMsgID, nil
}

// staticResolver returns a fixed provider list for every domain (test helper).
type staticResolver struct {
	providers []Provider
}

func (s staticResolver) ProvidersForDomain(domain string) []Provider { return s.providers }

// slowProvider blocks for delay per call (or until ctx is cancelled) and can
// fail a fixed number of attempts retryably.
type slowProvider struct {
	name      string
	delay     time.Duration
	failTimes int32
	calls     int32
	lastMsgID string
}

func (f *slowProvider) Name() string { return f.name }

func (f *slowProvider) Send(ctx context.Context, msg Message) (string, error) {
	n := atomic.AddInt32(&f.calls, 1)
	select {
	case <-ctx.Done():
		return "", &ProviderError{Provider: f.name, StatusCode: 0, Retryable: true, Message: "network error: " + ctx.Err().Error()}
	case <-time.After(f.delay):
	}
	if n <= atomic.LoadInt32(&f.failTimes) {
		return "", &ProviderError{Provider: f.name, StatusCode: 503, Retryable: true, Message: "retryable"}
	}
	f.lastMsgID = "prov-" + msg.ID
	return f.lastMsgID, nil
}

// rateLimitThenOK returns a 429 with RetryAfter once, then succeeds.
type rateLimitThenOK struct {
	name      string
	reset     time.Duration
	failed    bool
	lastMsgID string
}

func (r *rateLimitThenOK) Name() string { return r.name }

func (r *rateLimitThenOK) Send(ctx context.Context, msg Message) (string, error) {
	if !r.failed {
		r.failed = true
		return "", &ProviderError{Provider: r.name, StatusCode: 429, Retryable: true, Message: "rate limited", RetryAfter: r.reset}
	}
	r.lastMsgID = "prov-" + msg.ID
	return r.lastMsgID, nil
}

func TestDeliverySucceedsFirstTry(t *testing.T) {
	q := tempDB(t)
	_ = q.Store(sampleMsg("ok0001"))
	fp := &fakeProvider{name: "resend"}
	w := &DeliveryWorker{
		Queue:       q,
		Resolver:    staticResolver{providers: []Provider{fp}},
		MaxAttempts: 1,
		RetryDelay:  0,
		Logger:      New(&nopWriter{}, nil, "debug"),
	}
	if err := w.ProcessOne(context.Background()); err != nil {
		t.Fatalf("ProcessOne: %v", err)
	}
	stats, _ := q.Stats()
	if stats.Messages.Sent != 1 {
		t.Fatalf("expected 1 sent, got %d (items=%+v)", stats.Messages.Sent, stats.Items)
	}
}

func TestDeliveryRetriesThenSucceeds(t *testing.T) {
	q := tempDB(t)
	_ = q.Store(sampleMsg("rt0002"))
	fp := &fakeProvider{name: "resend", failTimes: 2} // fails twice, succeeds 3rd
	w := &DeliveryWorker{
		Queue:       q,
		Resolver:    staticResolver{providers: []Provider{fp}},
		MaxAttempts: 3,
		RetryDelay:  0,
		Logger:      New(&nopWriter{}, nil, "debug"),
	}
	if err := w.ProcessOne(context.Background()); err != nil {
		t.Fatalf("ProcessOne: %v", err)
	}
	if atomic.LoadInt32(&fp.calls) != 3 {
		t.Fatalf("expected 3 provider calls, got %d", fp.calls)
	}
	stats, _ := q.Stats()
	if stats.Messages.Sent != 1 {
		t.Fatalf("expected 1 sent, got %d", stats.Messages.Sent)
	}
}

func TestDeliveryPermanentFailsImmediately(t *testing.T) {
	q := tempDB(t)
	_ = q.Store(sampleMsg("pf0003"))
	fp := &fakeProvider{name: "resend", permanent: true}
	w := &DeliveryWorker{
		Queue:       q,
		Resolver:    staticResolver{providers: []Provider{fp}},
		MaxAttempts: 3,
		RetryDelay:  0,
		Logger:      New(&nopWriter{}, nil, "debug"),
	}
	if err := w.ProcessOne(context.Background()); err != nil {
		t.Fatalf("ProcessOne: %v", err)
	}
	if atomic.LoadInt32(&fp.calls) != 1 {
		t.Fatalf("permanent failure should not retry, got %d calls", fp.calls)
	}
	stats, _ := q.Stats()
	if stats.Messages.Failed != 1 {
		t.Fatalf("expected 1 failed, got %d", stats.Messages.Failed)
	}
}

func TestDeliveryExhaustsRetries(t *testing.T) {
	q := tempDB(t)
	_ = q.Store(sampleMsg("ex0004"))
	fp := &fakeProvider{name: "resend", failTimes: 99} // always retryable
	w := &DeliveryWorker{
		Queue:       q,
		Resolver:    staticResolver{providers: []Provider{fp}},
		MaxAttempts: 2,
		RetryDelay:  0,
		Logger:      New(&nopWriter{}, nil, "debug"),
	}
	if err := w.ProcessOne(context.Background()); err != nil {
		t.Fatalf("ProcessOne: %v", err)
	}
	if atomic.LoadInt32(&fp.calls) != 2 {
		t.Fatalf("expected 2 attempts (max), got %d", fp.calls)
	}
	stats, _ := q.Stats()
	if stats.Messages.Failed != 1 {
		t.Fatalf("expected 1 failed, got %d", stats.Messages.Failed)
	}
}

func TestDeliveryRetryDelayIsRespected(t *testing.T) {
	q := tempDB(t)
	_ = q.Store(sampleMsg("dl0005"))
	fp := &fakeProvider{name: "resend", failTimes: 1}
	w := &DeliveryWorker{
		Queue:       q,
		Resolver:    staticResolver{providers: []Provider{fp}},
		MaxAttempts: 2,
		RetryDelay:  50 * time.Millisecond,
		Logger:      New(&nopWriter{}, nil, "debug"),
	}
	start := time.Now()
	if err := w.ProcessOne(context.Background()); err != nil {
		t.Fatalf("ProcessOne: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Fatalf("expected retry delay ~50ms, got %v", elapsed)
	}
}

func TestDeliverySendTimeoutBoundsEachRequest(t *testing.T) {
	q := tempDB(t)
	_ = q.Store(sampleMsg("st0006"))
	sp := &slowProvider{name: "resend", delay: 200 * time.Millisecond}
	w := &DeliveryWorker{
		Queue:       q,
		Resolver:    staticResolver{providers: []Provider{sp}},
		MaxAttempts: 3,
		RetryDelay:  0,
		SendTimeout: 50 * time.Millisecond,
		Logger:      New(&nopWriter{}, nil, "debug"),
	}
	start := time.Now()
	if err := w.ProcessOne(context.Background()); err != nil {
		t.Fatalf("ProcessOne: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 400*time.Millisecond {
		t.Fatalf("expected each request bounded by SendTimeout, took %v", elapsed)
	}
	if atomic.LoadInt32(&sp.calls) != 3 {
		t.Fatalf("expected 3 bounded attempts, got %d", sp.calls)
	}
	stats, _ := q.Stats()
	if stats.Messages.Failed != 1 {
		t.Fatalf("expected 1 failed after bounded attempts, got %d", stats.Messages.Failed)
	}
}

func TestDeliveryRetryWaitNotCutShortBySendTimeout(t *testing.T) {
	q := tempDB(t)
	_ = q.Store(sampleMsg("rt0007"))
	rp := &rateLimitThenOK{name: "brevo", reset: 100 * time.Millisecond}
	w := &DeliveryWorker{
		Queue:       q,
		Resolver:    staticResolver{providers: []Provider{rp}},
		MaxAttempts: 2,
		RetryDelay:  0,
		SendTimeout: 10 * time.Millisecond,
		Logger:      New(&nopWriter{}, nil, "debug"),
	}
	start := time.Now()
	if err := w.ProcessOne(context.Background()); err != nil {
		t.Fatalf("ProcessOne: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 80*time.Millisecond {
		t.Fatalf("expected RetryAfter wait ~100ms honored, got %v", elapsed)
	}
	stats, _ := q.Stats()
	if stats.Messages.Sent != 1 {
		t.Fatalf("expected 1 sent after rate-limit wait, got %d", stats.Messages.Sent)
	}
}

func TestRetryWaitPrefersRetryAfter(t *testing.T) {
	w := &DeliveryWorker{RetryDelay: 0}
	err := &ProviderError{Provider: "brevo", StatusCode: 429, Retryable: true, RetryAfter: 2 * time.Second}
	if got := w.retryWait(err); got != 2*time.Second {
		t.Fatalf("expected retryWait 2s from RetryAfter, got %v", got)
	}
}

func TestRetryWaitCapsAtMaxRetryAfter(t *testing.T) {
	w := &DeliveryWorker{RetryDelay: 0}
	err := &ProviderError{Provider: "brevo", StatusCode: 429, Retryable: true, RetryAfter: time.Hour}
	want := time.Duration(Defaults.MaxRetryAfter) * time.Second
	if got := w.retryWait(err); got != want {
		t.Fatalf("expected retryWait capped at %v, got %v", want, got)
	}
}

func TestRetryWaitFallsBackToRetryDelay(t *testing.T) {
	w := &DeliveryWorker{RetryDelay: 30 * time.Second}
	err := &ProviderError{Provider: "resend", StatusCode: 503, Retryable: true}
	if got := w.retryWait(err); got != 30*time.Second {
		t.Fatalf("expected retryWait to fall back to RetryDelay 30s, got %v", got)
	}
}
