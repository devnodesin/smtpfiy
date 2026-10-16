package main

import (
	"bufio"
	"context"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseSMTPTransaction(t *testing.T) {
	const raw = "" +
		"From: Sender <sender@example.com>\r\n" +
		"To: rcpt@example.net\r\n" +
		"Subject: Hello\r\n" +
		"\r\n" +
		"This is the body.\r\n"

	msg, err := parseMessage("8f21a4", []string{"sender@example.com"}, []string{"rcpt@example.net"}, raw)
	if err != nil {
		t.Fatalf("parseMessage: %v", err)
	}
	if msg.ID != "8f21a4" {
		t.Fatalf("unexpected id %q", msg.ID)
	}
	if msg.From != "sender@example.com" {
		t.Fatalf("unexpected from %q", msg.From)
	}
	if len(msg.To) != 1 || msg.To[0] != "rcpt@example.net" {
		t.Fatalf("unexpected to %v", msg.To)
	}
	if msg.Subject != "Hello" {
		t.Fatalf("unexpected subject %q", msg.Subject)
	}
	if msg.Domain != "example.com" {
		t.Fatalf("unexpected domain %q", msg.Domain)
	}
	if !strings.Contains(msg.TextBody, "This is the body.") {
		t.Fatalf("unexpected body %q", msg.TextBody)
	}
}

func TestSenderDomainDetectedFromHeader(t *testing.T) {
	const raw = "From: Someone <a@foo.org>\r\nTo: b@bar.org\r\nSubject: x\r\n\r\nbody\r\n"
	msg, err := parseMessage("ab12cd", []string{"a@foo.org"}, []string{"b@bar.org"}, raw)
	if err != nil {
		t.Fatalf("parseMessage: %v", err)
	}
	if msg.Domain != "foo.org" {
		t.Fatalf("expected domain foo.org, got %q", msg.Domain)
	}
}

func TestEnforceRecipientLimit(t *testing.T) {
	const maxRecipients = 2
	recipients := []string{"a@example.net", "b@example.net", "c@example.net"}
	err := checkRecipients(recipients, maxRecipients)
	if err == nil {
		t.Fatal("expected recipient limit error, got nil")
	}
}

func TestRejectEmbeddedCRLFInAddress(t *testing.T) {
	// An address containing CRLF is an injection attempt and must be rejected.
	if err := validateAddress("a@b.com\r\nRCPT TO:<x@y.com>"); err == nil {
		t.Fatal("expected injection address to be rejected, got nil")
	}
}

func TestRejectInvalidAddress(t *testing.T) {
	for _, bad := range []string{"", "not-an-email", "a@b@c.com"} {
		if err := validateAddress(bad); err == nil {
			t.Fatalf("expected %q to be rejected", bad)
		}
	}
}

// TestSMTPServerEndToEnd starts the listener and delivers a message using the
// standard library smtp client, asserting the server accepts and stores it.
func TestSMTPServerEndToEnd(t *testing.T) {
	store := newMemStore()
	logger := New(&nopWriter{}, nil, "debug")
	srv := &SMTPServer{
		Host:           "127.0.0.1",
		Port:           0, // ephemeral
		MaxRecipients:  10,
		Store:          store,
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

	addr := fmtSmtpAddr(port)
	c, err := smtp.Dial(addr)
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
	msg := "From: sender@example.com\r\nTo: rcpt@example.net\r\nSubject: e2e\r\n\r\nhi there\r\n"
	if _, err := wc.Write([]byte(msg)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := wc.Close(); err != nil {
		t.Fatalf("close data: %v", err)
	}
	if err := c.Quit(); err != nil {
		t.Fatalf("quit: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if store.count() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if store.count() != 1 {
		t.Fatalf("expected 1 stored message, got %d", store.count())
	}
	got := store.first()
	if got.Subject != "e2e" || got.Domain != "example.com" {
		t.Fatalf("unexpected stored message: %+v", got)
	}
}

// nopWriter discards output (used so end-to-end test stays quiet).
type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

func fmtSmtpAddr(port int) string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
}

// memStore is an in-memory MessageStore used by tests.
type memStore struct {
	mu   sync.Mutex
	msgs []Message
}

func newMemStore() *memStore { return &memStore{} }

func (m *memStore) Store(msg Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs = append(m.msgs, msg)
	return nil
}

func (m *memStore) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.msgs)
}

func (m *memStore) first() Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.msgs) == 0 {
		return Message{}
	}
	return m.msgs[0]
}

// ensure bufio import used even if tests change.
var _ = bufio.NewReader
