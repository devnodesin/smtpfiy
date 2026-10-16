package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/smtp"
	"net/textproto"
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

// authConfig returns a test SMTP AUTH config.
func authConfig(required bool, users ...SMTPAuthUser) *SMTPAuthConfig {
	return &SMTPAuthConfig{Required: required, Users: users}
}

func plainBlob(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte("\x00" + user + "\x00" + pass))
}

// TestAUTHAdvertisedOnEhlo verifies AUTH is advertised only when configured.
func TestAUTHAdvertisedOnEhlo(t *testing.T) {
	store := newMemStore()
	logger := New(&nopWriter{}, nil, "debug")
	srv := &SMTPServer{
		Host:            "127.0.0.1",
		Port:            0,
		Store:           store,
		Logger:          logger,
		AllowedDomains:  map[string]bool{"example.com": true},
		Auth:            authConfig(false, SMTPAuthUser{Username: "resend", Password: "changeme"}),
		MaxAuthAttempts: Defaults.MaxAuthAttempts,
	}
	ln, err := srv.listen()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	go srv.serve(ln)
	defer srv.Shutdown(context.Background())

	c, err := smtp.Dial(fmtSmtpAddr(port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	ok, _ := c.Extension("AUTH")
	if !ok {
		t.Fatal("expected AUTH extension to be advertised")
	}
}

// TestAUTHNotAdvertisedWhenUnconfigured verifies backward compatibility.
func TestAUTHNotAdvertisedWhenUnconfigured(t *testing.T) {
	store := newMemStore()
	logger := New(&nopWriter{}, nil, "debug")
	srv := &SMTPServer{
		Host:           "127.0.0.1",
		Port:           0,
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

	c, err := smtp.Dial(fmtSmtpAddr(port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	ok, _ := c.Extension("AUTH")
	if ok {
		t.Fatal("AUTH must not be advertised when unconfigured")
	}
}

// TestAUTHPlainSuccess delivers mail after a successful AUTH PLAIN.
func TestAUTHPlainSuccess(t *testing.T) {
	store := newMemStore()
	logger := New(&nopWriter{}, nil, "debug")
	srv := &SMTPServer{
		Host:            "127.0.0.1",
		Port:            0,
		Store:           store,
		Logger:          logger,
		AllowedDomains:  map[string]bool{"example.com": true},
		Auth:            authConfig(false, SMTPAuthUser{Username: "resend", Password: "changeme"}),
		MaxAuthAttempts: Defaults.MaxAuthAttempts,
	}
	ln, err := srv.listen()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	go srv.serve(ln)
	defer srv.Shutdown(context.Background())

	if err := sendMailAuth(t, port, "resend", "changeme"); err != nil {
		t.Fatalf("sendMailAuth: %v", err)
	}
	if store.count() != 1 {
		t.Fatalf("expected 1 stored message, got %d", store.count())
	}
}

// TestAUTHPlainWrongPassword fails authentication with wrong credentials.
func TestAUTHPlainWrongPassword(t *testing.T) {
	store := newMemStore()
	logger := New(&nopWriter{}, nil, "debug")
	srv := &SMTPServer{
		Host:            "127.0.0.1",
		Port:            0,
		Store:           store,
		Logger:          logger,
		AllowedDomains:  map[string]bool{"example.com": true},
		Auth:            authConfig(false, SMTPAuthUser{Username: "resend", Password: "changeme"}),
		MaxAuthAttempts: Defaults.MaxAuthAttempts,
	}
	ln, err := srv.listen()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	go srv.serve(ln)
	defer srv.Shutdown(context.Background())

	err = sendMailAuth(t, port, "resend", "wrong")
	if err == nil {
		t.Fatal("expected authentication failure, got nil")
	}
	if store.count() != 0 {
		t.Fatalf("no message should be stored on auth failure, got %d", store.count())
	}
}

// TestAUTHLoginFlow exercises the AUTH LOGIN challenge-response sequence.
func TestAUTHLoginFlow(t *testing.T) {
	store := newMemStore()
	logger := New(&nopWriter{}, nil, "debug")
	srv := &SMTPServer{
		Host:            "127.0.0.1",
		Port:            0,
		Store:           store,
		Logger:          logger,
		AllowedDomains:  map[string]bool{"example.com": true},
		Auth:            authConfig(false, SMTPAuthUser{Username: "resend", Password: "changeme"}),
		MaxAuthAttempts: Defaults.MaxAuthAttempts,
	}
	ln, err := srv.listen()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	go srv.serve(ln)
	defer srv.Shutdown(context.Background())

	if err := sendMailAuthLogin(t, port, "resend", "changeme"); err != nil {
		t.Fatalf("sendMailAuthLogin: %v", err)
	}
	if store.count() != 1 {
		t.Fatalf("expected 1 stored message, got %d", store.count())
	}
}

// TestAUTHRequiredBlocksUnauthenticatedMail verifies required:true enforces AUTH.
func TestAUTHRequiredBlocksUnauthenticatedMail(t *testing.T) {
	store := newMemStore()
	logger := New(&nopWriter{}, nil, "debug")
	srv := &SMTPServer{
		Host:            "127.0.0.1",
		Port:            0,
		Store:           store,
		Logger:          logger,
		AllowedDomains:  map[string]bool{"example.com": true},
		Auth:            authConfig(true, SMTPAuthUser{Username: "resend", Password: "changeme"}),
		MaxAuthAttempts: Defaults.MaxAuthAttempts,
	}
	ln, err := srv.listen()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	go srv.serve(ln)
	defer srv.Shutdown(context.Background())

	c, err := smtp.Dial(fmtSmtpAddr(port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	// MAIL FROM without AUTH must be rejected with 530.
	if err := c.Mail("sender@example.com"); err == nil {
		t.Fatal("expected MAIL FROM to be rejected before AUTH")
	}
	if store.count() != 0 {
		t.Fatalf("no message should be stored, got %d", store.count())
	}

	// After authenticating, delivery should succeed.
	if err := c.Auth(smtp.PlainAuth("", "resend", "changeme", "127.0.0.1")); err != nil {
		t.Fatalf("auth: %v", err)
	}
	if err := c.Mail("sender@example.com"); err != nil {
		t.Fatalf("mail after auth: %v", err)
	}
	if err := c.Rcpt("rcpt@example.net"); err != nil {
		t.Fatalf("rcpt: %v", err)
	}
	wc, err := c.Data()
	if err != nil {
		t.Fatalf("data: %v", err)
	}
	msg := "From: sender@example.com\r\nTo: rcpt@example.net\r\nSubject: req\r\n\r\nbody\r\n"
	if _, err := wc.Write([]byte(msg)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := wc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := c.Quit(); err != nil {
		t.Fatalf("quit: %v", err)
	}
	waitForMessages(t, store, 1)
}

// TestTooManyAuthAttempts closes the connection after exhausting attempts.
func TestTooManyAuthAttempts(t *testing.T) {
	store := newMemStore()
	logger := New(&nopWriter{}, nil, "debug")
	srv := &SMTPServer{
		Host:            "127.0.0.1",
		Port:            0,
		Store:           store,
		Logger:          logger,
		AllowedDomains:  map[string]bool{"example.com": true},
		Auth:            authConfig(false, SMTPAuthUser{Username: "resend", Password: "changeme"}),
		MaxAuthAttempts: 3,
	}
	ln, err := srv.listen()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	go srv.serve(ln)
	defer srv.Shutdown(context.Background())

	c, err := smtp.Dial(fmtSmtpAddr(port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	for i := 0; i < 3; i++ {
		if err := c.Auth(smtp.PlainAuth("", "resend", "bad", "127.0.0.1")); err == nil {
			t.Fatal("expected auth failure")
		}
	}
	// Connection should be closed by the server after exhausting attempts.
	if _, _, err := c.Text.ReadResponse(0); err == nil {
		t.Fatal("expected connection closed after too many attempts")
	}
}

// sendMailAuth delivers a single message using smtp.PlainAuth (matches sdeploy).
func sendMailAuth(t *testing.T, port int, user, pass string) error {
	t.Helper()
	c, err := smtp.Dial(fmtSmtpAddr(port))
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Auth(smtp.PlainAuth("", user, pass, "127.0.0.1")); err != nil {
		return err
	}
	if err := c.Mail("sender@example.com"); err != nil {
		return err
	}
	if err := c.Rcpt("rcpt@example.net"); err != nil {
		return err
	}
	wc, err := c.Data()
	if err != nil {
		return err
	}
	msg := "From: sender@example.com\r\nTo: rcpt@example.net\r\nSubject: auth\r\n\r\nbody\r\n"
	if _, err := wc.Write([]byte(msg)); err != nil {
		return err
	}
	if err := wc.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// sendMailAuthLogin drives the AUTH LOGIN flow manually via raw SMTP.
func sendMailAuthLogin(t *testing.T, port int, user, pass string) error {
	t.Helper()
	conn, err := net.Dial("tcp", fmtSmtpAddr(port))
	if err != nil {
		return err
	}
	defer conn.Close()
	tc := textproto.NewConn(conn)
	readReply := func() (int, string) {
		code, msg, _ := tc.ReadResponse(0)
		return code, msg
	}
	if _, _, err := tc.ReadResponse(220); err != nil {
		return err
	}
	if err := tc.PrintfLine("EHLO client"); err != nil {
		return err
	}
	readReply() // EHLO multi-line reply

	if err := tc.PrintfLine("AUTH LOGIN"); err != nil {
		return err
	}
	if code, _ := readReply(); code != 334 {
		return fmt.Errorf("expected 334 challenge, got %d", code)
	}
	if err := tc.PrintfLine("%s", base64.StdEncoding.EncodeToString([]byte(user))); err != nil {
		return err
	}
	if code, _ := readReply(); code != 334 {
		return fmt.Errorf("expected 334 password challenge, got %d", code)
	}
	if err := tc.PrintfLine("%s", base64.StdEncoding.EncodeToString([]byte(pass))); err != nil {
		return err
	}
	if code, _ := readReply(); code != 235 {
		return fmt.Errorf("expected 235 success, got %d", code)
	}
	if err := tc.PrintfLine("MAIL FROM:<sender@example.com>"); err != nil {
		return err
	}
	if code, _ := readReply(); code != 250 {
		return fmt.Errorf("expected 250 MAIL, got %d", code)
	}
	if err := tc.PrintfLine("RCPT TO:<rcpt@example.net>"); err != nil {
		return err
	}
	if code, _ := readReply(); code != 250 {
		return fmt.Errorf("expected 250 RCPT, got %d", code)
	}
	if err := tc.PrintfLine("DATA"); err != nil {
		return err
	}
	if code, _ := readReply(); code != 354 {
		return fmt.Errorf("expected 354 DATA, got %d", code)
	}
	if err := tc.PrintfLine("From: sender@example.com\r\nTo: rcpt@example.net\r\nSubject: login\r\n\r\nbody\r\n."); err != nil {
		return err
	}
	if code, _ := readReply(); code != 250 {
		return fmt.Errorf("expected 250 queued, got %d", code)
	}
	if err := tc.PrintfLine("QUIT"); err != nil {
		return err
	}
	readReply()
	return nil
}

func waitForMessages(t *testing.T, store *memStore, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if store.count() >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected %d stored messages, got %d", n, store.count())
}
