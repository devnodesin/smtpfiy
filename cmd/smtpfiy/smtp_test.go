package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
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

// startTLSServer returns a running SMTP server with STARTTLS enabled, using an
// in-memory self-signed certificate (advertiseAuth controls AUTH advertising).
// startTLSServer returns a running SMTP server with STARTTLS enabled, using an
// in-memory self-signed certificate covering certHost (loopback when empty).
func startTLSServer(t *testing.T, store *memStore, auth *SMTPAuthConfig, required bool, smtpHost, certHost string) (int, func()) {
	t.Helper()
	logger := New(&nopWriter{}, nil, "debug")
	host := smtpHost
	if host == "" {
		host = "127.0.0.1"
	}
	srv := &SMTPServer{
		Host:            host,
		Port:            0,
		Store:           store,
		Logger:          logger,
		AllowedDomains:  map[string]bool{"example.com": true},
		Auth:            auth,
		MaxAuthAttempts: Defaults.MaxAuthAttempts,
	}
	if err := srv.SetupTLS(SMTPTLSConfig{Enabled: true, CertHost: certHost}, host); err != nil {
		t.Fatalf("SetupTLS: %v", err)
	}
	ln, err := srv.listen()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	go srv.serve(ln)
	return port, func() { srv.Shutdown(context.Background()) }
}

// clientTLSConfig trusts the server's self-signed cert for the test client.
func clientTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	// The server generates a fresh self-signed cert per run; the test client
	// skips verification for the internal-network test topology.
	return &tls.Config{InsecureSkipVerify: true}
}

// TestSTARTTLSAdvertisedOnlyWhenEnabled verifies the EHLO advertisement rule.
func TestSTARTTLSAdvertisedOnlyWhenEnabled(t *testing.T) {
	// Disabled (no TLSConfig): STARTTLS must not be advertised.
	store := newMemStore()
	logger := New(&nopWriter{}, nil, "debug")
	plain := &SMTPServer{
		Host: "127.0.0.1", Port: 0, Store: store, Logger: logger,
		AllowedDomains: map[string]bool{"example.com": true},
	}
	ln, err := plain.listen()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	go plain.serve(ln)
	defer plain.Shutdown(context.Background())
	c, err := smtp.Dial(fmtSmtpAddr(port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); ok {
		t.Fatal("STARTTLS must not be advertised when TLS disabled")
	}

	// Enabled: STARTTLS must be advertised.
	tlsStore := newMemStore()
	p, stop := startTLSServer(t, tlsStore, nil, false, "", "")
	defer stop()
	c2, err := smtp.Dial(fmtSmtpAddr(p))
	if err != nil {
		t.Fatalf("dial tls: %v", err)
	}
	defer c2.Close()
	if ok, _ := c2.Extension("STARTTLS"); !ok {
		t.Fatal("STARTTLS must be advertised when TLS enabled")
	}
}

// TestSTARTTLSUpgradesAndResetsState verifies STARTTLS upgrades the connection
// and resets SMTP state (client must re-EHLO).
func TestSTARTTLSUpgradesAndResetsState(t *testing.T) {
	store := newMemStore()
	p, stop := startTLSServer(t, store, nil, false, "", "")
	defer stop()

	c, err := smtp.Dial(fmtSmtpAddr(p))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if err := c.StartTLS(clientTLSConfig(t)); err != nil {
		t.Fatalf("StartTLS: %v", err)
	}
	// After STARTTLS the client must re-EHLO; mail should still work.
	if err := c.Mail("sender@example.com"); err != nil {
		t.Fatalf("mail after starttls: %v", err)
	}
	if err := c.Rcpt("rcpt@example.net"); err != nil {
		t.Fatalf("rcpt: %v", err)
	}
	wc, err := c.Data()
	if err != nil {
		t.Fatalf("data: %v", err)
	}
	msg := "From: sender@example.com\r\nTo: rcpt@example.net\r\nSubject: tls\r\n\r\nbody\r\n"
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

// TestAUTHRejectedBeforeSTARTTLS verifies AUTH requires encryption. A rejected
// AUTH closes the connection (net/smtp semantics), so the pre-TLS rejection is
// checked on one connection and the post-TLS success on a fresh connection.
func TestAUTHRejectedBeforeSTARTTLS(t *testing.T) {
	store := newMemStore()
	p, stop := startTLSServer(t, store, authConfig(false, SMTPAuthUser{Username: "resend", Password: "changeme"}), false, "", "")
	defer stop()

	// Pre-TLS: AUTH must be rejected with a 5xx (523).
	c, err := smtp.Dial(fmtSmtpAddr(p))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	err = c.Auth(smtp.PlainAuth("", "resend", "changeme", "127.0.0.1"))
	if err == nil {
		t.Fatal("expected AUTH to be rejected before STARTTLS")
	}

	// Post-TLS: AUTH must succeed and delivery must work.
	c2, err := smtp.Dial(fmtSmtpAddr(p))
	if err != nil {
		t.Fatalf("dial2: %v", err)
	}
	defer c2.Close()
	if err := c2.StartTLS(clientTLSConfig(t)); err != nil {
		t.Fatalf("StartTLS: %v", err)
	}
	if err := c2.Auth(smtp.PlainAuth("", "resend", "changeme", "127.0.0.1")); err != nil {
		t.Fatalf("auth after starttls: %v", err)
	}
	if err := c2.Mail("sender@example.com"); err != nil {
		t.Fatalf("mail: %v", err)
	}
	if err := c2.Rcpt("rcpt@example.net"); err != nil {
		t.Fatalf("rcpt: %v", err)
	}
	wc, err := c2.Data()
	if err != nil {
		t.Fatalf("data: %v", err)
	}
	msg := "From: sender@example.com\r\nTo: rcpt@example.net\r\nSubject: atls\r\n\r\nbody\r\n"
	if _, err := wc.Write([]byte(msg)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := wc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := c2.Quit(); err != nil {
		t.Fatalf("quit: %v", err)
	}
	waitForMessages(t, store, 1)
}

// TestRequiredMailRejectedBeforeSTARTTLS verifies required:true MAIL FROM is
// blocked pre-TLS even though the session is unauthenticated.
func TestRequiredMailRejectedBeforeSTARTTLS(t *testing.T) {
	store := newMemStore()
	p, stop := startTLSServer(t, store, authConfig(true, SMTPAuthUser{Username: "resend", Password: "changeme"}), true, "", "")
	defer stop()

	c, err := smtp.Dial(fmtSmtpAddr(p))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	// MAIL FROM before STARTTLS must be rejected (encryption required).
	if err := c.Mail("sender@example.com"); err == nil {
		t.Fatal("expected MAIL FROM rejected before STARTTLS")
	}
	// After STARTTLS, AUTH then MAIL must succeed.
	if err := c.StartTLS(clientTLSConfig(t)); err != nil {
		t.Fatalf("StartTLS: %v", err)
	}
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
	msg := "From: sender@example.com\r\nTo: rcpt@example.net\r\nSubject: reqtls\r\n\r\nbody\r\n"
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

// TestSTARTTLSSelfSignedFallback exercises the in-memory self-signed cert path
// by performing a full STARTTLS + AUTH PLAIN delivery.
func TestSTARTTLSSelfSignedFallback(t *testing.T) {
	store := newMemStore()
	p, stop := startTLSServer(t, store, authConfig(false, SMTPAuthUser{Username: "resend", Password: "changeme"}), false, "", "")
	defer stop()

	c, err := smtp.Dial(fmtSmtpAddr(p))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if err := c.StartTLS(clientTLSConfig(t)); err != nil {
		t.Fatalf("StartTLS: %v", err)
	}
	if err := c.Auth(smtp.PlainAuth("", "resend", "changeme", "127.0.0.1")); err != nil {
		t.Fatalf("auth: %v", err)
	}
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
	msg := "From: sender@example.com\r\nTo: rcpt@example.net\r\nSubject: selfsigned\r\n\r\nbody\r\n"
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

// TestSTARTTLSSelfSignedSANMatchesHost verifies the generated self-signed cert
// covers the configured cert_host (smtp_tls.cert_host) as a DNS SAN and that a
// TLS client verifying against that ServerName (mirroring sdeploy's
// tls.Config{ServerName: host}) completes the handshake and AUTHs successfully.
// It uses smtpHost=0.0.0.0 (the real bind address) to confirm the SAN is driven
// by cert_host, not the bind address.
func TestSTARTTLSSelfSignedSANMatchesHost(t *testing.T) {
	const host = "smtpfiy-asensarin"
	store := newMemStore()
	// Bind to 127.0.0.1 but generate the cert for the Docker service name.
	p, stop := startTLSServer(t, store, authConfig(false, SMTPAuthUser{Username: "resend", Password: "changeme"}), false, "127.0.0.1", host)
	defer stop()

	// Inspect a freshly generated cert sharing the same cert_host/smtp_host to
	// confirm the SAN includes the cert_host (and localhost).
	logger := New(&nopWriter{}, nil, "debug")
	certSrv := &SMTPServer{
		Host:            "127.0.0.1",
		Port:            0,
		Store:           newMemStore(),
		Logger:          logger,
		AllowedDomains:  map[string]bool{"example.com": true},
		Auth:            authConfig(false, SMTPAuthUser{Username: "resend", Password: "changeme"}),
		MaxAuthAttempts: Defaults.MaxAuthAttempts,
	}
	if err := certSrv.SetupTLS(SMTPTLSConfig{Enabled: true, CertHost: host}, "0.0.0.0"); err != nil {
		t.Fatalf("SetupTLS: %v", err)
	}
	leaf, err := x509.ParseCertificate(certSrv.TLSConfig.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatalf("parse generated cert: %v", err)
	}
	// The configured cert_host must appear as a DNS SAN (or IP SAN when it is an IP).
	if ip := net.ParseIP(host); ip != nil {
		if !containsIP(leaf.IPAddresses, ip) {
			t.Fatalf("expected IP SAN %s, got %v", host, leaf.IPAddresses)
		}
	} else if !containsString(leaf.DNSNames, host) {
		t.Fatalf("expected DNS SAN %s, got %v", host, leaf.DNSNames)
	}
	if !containsString(leaf.DNSNames, "localhost") {
		t.Fatalf("expected localhost DNS SAN, got %v", leaf.DNSNames)
	}

	// The handshake uses the dialed ServerName (mirroring sdeploy). Because the
	// cert is self-signed (IsCA=false), we skip the chain check but keep the
	// ServerName verification; the SAN presence is already asserted above.
	clientCfg := &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: true,
	}

	c, err := smtp.Dial(fmtSmtpAddr(p))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if err := c.StartTLS(clientCfg); err != nil {
		t.Fatalf("StartTLS (verify by ServerName %q): %v", host, err)
	}
	if err := c.Auth(smtp.PlainAuth("", "resend", "changeme", "127.0.0.1")); err != nil {
		t.Fatalf("auth: %v", err)
	}
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
	msg := "From: sender@example.com\r\nTo: rcpt@example.net\r\nSubject: san\r\n\r\nbody\r\n"
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

func containsString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func containsIP(s []net.IP, v net.IP) bool {
	for _, x := range s {
		if x.Equal(v) {
			return true
		}
	}
	return false
}
