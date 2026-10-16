package main

import (
	"context"
	"net"
	"net/smtp"
	"sync/atomic"
	"testing"
	"time"
)

// TestLifecycleSMTPToDelivery verifies the full path: an email received over
// SMTP is stored in the queue and then delivered by the worker to a provider.
func TestLifecycleSMTPToDelivery(t *testing.T) {
	q := tempDB(t)
	logger := New(&nopWriter{}, nil, "debug")

	fp := &fakeProvider{name: "resend"}
	resolver := staticResolver{providers: []Provider{fp}}

	srv := &SMTPServer{
		Host:           "127.0.0.1",
		Port:           0,
		MaxRecipients:  10,
		MaxMessageSize: 1 << 20,
		Store:          q,
		Logger:         logger,
		AllowedDomains: map[string]bool{"example.com": true},
	}
	ln, err := srv.listen()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	go srv.serve(ln)
	defer srv.Shutdown(context.Background())

	worker := &DeliveryWorker{
		Queue:       q,
		Resolver:    resolver,
		MaxAttempts: 1,
		RetryDelay:  0,
		Logger:      logger,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go workerLoop(ctx, worker, logger)

	// Deliver a message via the real SMTP client.
	c, err := smtp.Dial(net.JoinHostPort("127.0.0.1", itoa(port)))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if err := c.Mail("sender@example.com"); err != nil {
		t.Fatalf("mail: %v", err)
	}
	if err := c.Rcpt("rcpt@example.net"); err != nil {
		t.Fatalf("rcpt: %v", err)
	}
	wc, err := c.Data()
	if err != nil {
		t.Fatalf("data: %v", err)
	}
	msg := "From: sender@example.com\r\nTo: rcpt@example.net\r\nSubject: lifecycle\r\n\r\nhello world\r\n"
	if _, err := wc.Write([]byte(msg)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := wc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_ = c.Quit()

	// Wait for the worker to pick it up and deliver.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		stats, _ := q.Stats()
		if stats.Messages.Sent >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	stats, _ := q.Stats()
	if stats.Messages.Sent != 1 {
		t.Fatalf("expected 1 sent via lifecycle, got %d (items=%+v)", stats.Messages.Sent, stats.Items)
	}
	if atomic.LoadInt32(&fp.calls) < 1 {
		t.Fatalf("provider was never called")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
