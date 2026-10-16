package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"time"
)

// Message is the normalized internal representation of a received email.
type Message struct {
	ID        string
	From      string
	To        []string
	Cc        []string
	Bcc       []string
	Subject   string
	TextBody  string
	HTMLBody  string
	Headers   map[string]string
	RawSize   int
	Domain    string
	Status    string
	CreatedAt time.Time
}

// MessageStore receives parsed messages from the SMTP server. The persistent
// SQLite implementation is provided by the queue (see queue.go).
type MessageStore interface {
	Store(m Message) error
}

// SMTPServer accepts local SMTP connections and stores parsed messages.
type SMTPServer struct {
	Host           string
	Port           int
	MaxRecipients  int
	MaxMessageSize int
	Store          MessageStore
	Logger         *Logger
	AllowedDomains map[string]bool

	// Auth, when non-nil and Auth.Users is non-empty, advertises and enforces
	// SMTP AUTH (PLAIN/LOGIN). When nil or empty, the listener remains fully
	// unauthenticated (backward compatible).
	Auth *SMTPAuthConfig
	// MaxAuthAttempts is the number of failed AUTH attempts allowed before the
	// connection is closed. Defaults to Defaults.MaxAuthAttempts.
	MaxAuthAttempts int

	// TLSConfig, when non-nil, enables STARTTLS. The connection is upgraded to
	// TLS using this config when the client issues STARTTLS.
	TLSConfig *tls.Config

	ln net.Listener
	mu sync.Mutex
}

// listen opens the TCP listener on the configured host/port.
func (s *SMTPServer) listen() (net.Listener, error) {
	addr := net.JoinHostPort(s.Host, fmt.Sprintf("%d", s.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("smtp listen %s: %w", addr, err)
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	return ln, nil
}

// Shutdown closes the listener and stops accepting connections.
func (s *SMTPServer) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	ln := s.ln
	s.mu.Unlock()
	if ln == nil {
		return nil
	}
	return ln.Close()
}

// serve accepts connections until the listener is closed.
func (s *SMTPServer) serve(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

// Start listens and serves in the background.
func (s *SMTPServer) Start() error {
	ln, err := s.listen()
	if err != nil {
		return err
	}
	go s.serve(ln)
	return nil
}

// SetupTLS bootstraps the TLS configuration used for STARTTLS. When the server
// has no TLSConfig assigned, this is a no-op. It either loads the cert/key from
// the configured PEM files or, when both are empty, generates an in-memory
// self-signed certificate. On load failure it returns a clear error so startup
// fails fast.
func (s *SMTPServer) SetupTLS(cfg SMTPTLSConfig) error {
	if !cfg.SMTPTLSEnabled() {
		return nil
	}
	if s.TLSConfig != nil {
		return nil
	}
	tlsCfg, err := buildTLSConfig(cfg, s.Logger)
	if err != nil {
		return err
	}
	s.TLSConfig = tlsCfg
	return nil
}

// buildTLSConfig loads or generates the TLS config for STARTTLS.
func buildTLSConfig(cfg SMTPTLSConfig, logger *Logger) (*tls.Config, error) {
	if cfg.CertFile != "" || cfg.KeyFile != "" {
		if cfg.CertFile == "" || cfg.KeyFile == "" {
			return nil, fmt.Errorf("smtp_tls: both cert_file and key_file are required when one is set")
		}
		c, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("smtp_tls: loading cert/key: %w", err)
		}
		if logger != nil {
			logger.Info("SMTP TLS  loaded certificate from %s", cfg.CertFile)
		}
		return &tls.Config{Certificates: []tls.Certificate{c}}, nil
	}

	// Generate an in-memory self-signed certificate.
	cert, err := generateSelfSignedCert()
	if err != nil {
		return nil, fmt.Errorf("smtp_tls: generating self-signed cert: %w", err)
	}
	if logger != nil {
		logger.Warn("SMTP TLS  self-signed certificate generated (regenerated on every start)")
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}}, nil
}

// write writes a formatted line to the buffered SMTP writer and flushes.
func write(w *bufio.Writer, format string, args ...interface{}) {
	fmt.Fprintf(w, format, args...)
	w.Flush()
}

// writeEhlo writes the EHLO/HELO greeting. For EHLO, when AUTH and/or STARTTLS
// are configured it emits a multi-line reply: intermediate lines use "250-"
// and the final line uses "250 " (with no dash). HELO always gets a single-line
// reply and never advertises extensions.
func (s *SMTPServer) writeEhlo(w *bufio.Writer, cmd string, advertiseAuth, advertiseTLS bool) {
	if cmd != "EHLO" {
		write(w, "250 smtpfiy\r\n")
		return
	}
	lines := []string{"250-smtpfiy"}
	if advertiseTLS {
		lines = append(lines, "250-STARTTLS")
	}
	if advertiseAuth {
		lines = append(lines, "250-AUTH PLAIN LOGIN")
	}
	// Terminate the multi-line reply with a plain "250 " line.
	lines = append(lines, "250 OK")
	for _, l := range lines {
		write(w, "%s\r\n", l)
	}
}

// generateSelfSignedCert creates an in-memory self-signed certificate valid for
// localhost and 127.0.0.1. Suitable for internal, non-published listeners.
func generateSelfSignedCert() (tls.Certificate, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "smtpfiy", Organization: []string{"smtpfiy"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * 365 * 10 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return tls.X509KeyPair(certPEM, keyPEM)
}

func (s *SMTPServer) handleConn(conn net.Conn) {
	defer conn.Close()

	// Session state that survives a STARTTLS upgrade (reset after the upgrade).
	var (
		from       string
		recipients []string
		inData     bool
		dataBuf    strings.Builder
		authed     bool
		authTries  int
		isTLS      bool
	)

	advertiseAuth := s.Auth != nil && s.Auth.SMTPAuthEnabled()
	advertiseTLS := s.TLSConfig != nil
	maxTries := s.MaxAuthAttempts
	if maxTries <= 0 {
		maxTries = Defaults.MaxAuthAttempts
	}

	writeGreeting := func(c net.Conn, w *bufio.Writer) {
		write(w, "220 smtpfiy SMTP ready\r\n")
	}

	// (Re)start the SMTP session on the current connection. After STARTTLS the
	// same logical connection continues with a fresh reader/writer/state, but no
	// new greeting is sent (the client issues EHLO again and expects 250).
	var startSession func(c net.Conn, sendGreeting bool)
	startSession = func(c net.Conn, sendGreeting bool) {
		r := bufio.NewReader(c)
		w := bufio.NewWriter(c)
		if sendGreeting {
			writeGreeting(c, w)
		}
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")

			if inData {
				if line == "." {
					inData = false
					raw := dataBuf.String()
					msg, perr := s.buildMessage(from, recipients, raw)
					if perr != nil {
						write(w, "451 error processing message: %s\r\n", perr)
						from, recipients = "", nil
						continue
					}
					if serr := s.Store.Store(msg); serr != nil {
						write(w, "451 failed to queue message: %s\r\n", serr)
						from, recipients = "", nil
						continue
					}
					if s.Logger != nil {
						s.Logger.Info("SMTP  id:%s (%s -> %s)",
							msg.ID, msg.From, strings.Join(msg.To, ","))
					}
					write(w, "250 OK: message queued as %s\r\n", msg.ID)
					from, recipients = "", nil
					continue
				}
				dataBuf.WriteString(line)
				dataBuf.WriteString("\r\n")
				continue
			}

			cmd, arg := splitCommand(line)
			switch strings.ToUpper(cmd) {
			case "HELO", "EHLO":
				s.writeEhlo(w, strings.ToUpper(cmd), advertiseAuth, advertiseTLS && !isTLS)
			case "STARTTLS":
				if !advertiseTLS {
					write(w, "502 STARTTLS not supported\r\n")
					continue
				}
				write(w, "220 2.0.0 Ready to start TLS\r\n")
				tlsConn := tls.Server(c, s.TLSConfig)
				if err := tlsConn.Handshake(); err != nil {
					if s.Logger != nil {
						s.Logger.Warn("SMTP TLS handshake failed  err=%v", err)
					}
					return
				}
				if s.Logger != nil {
					s.Logger.Info("SMTP TLS handshake success")
				}
				// Reset SMTP state; client must re-EHLO.
				from, recipients = "", nil
				authed = false
				authTries = 0
				inData = false
				dataBuf.Reset()
				isTLS = true
				startSession(tlsConn, false)
				return
			case "AUTH":
				if !advertiseAuth {
					write(w, "503 AUTH not supported\r\n")
					continue
				}
				if advertiseTLS && !isTLS {
					write(w, "530 5.7.10 Encryption required for AUTH\r\n")
					continue
				}
				ok, closeConn := s.handleAuth(func(f string, a ...interface{}) { write(w, f, a...) }, r, arg, &authed, &authTries, maxTries)
				_ = ok
				if closeConn {
					return
				}
			case "MAIL":
				if advertiseAuth && s.Auth.Required && !authed {
					if advertiseTLS && !isTLS {
						write(w, "523 5.7.10 Encryption required for MAIL FROM\r\n")
						continue
					}
					write(w, "530 Authentication required\r\n")
					continue
				}
				addr, ok := parsePath(arg)
				if !ok {
					write(w, "501 malformed MAIL FROM\r\n")
					continue
				}
				if err := validateAddress(addr); err != nil {
					write(w, "501 bad sender address\r\n")
					continue
				}
				from = addr
				write(w, "250 OK\r\n")
			case "RCPT":
				addr, ok := parsePath(arg)
				if !ok {
					write(w, "501 malformed RCPT TO\r\n")
					continue
				}
				if err := validateAddress(addr); err != nil {
					write(w, "501 bad recipient address\r\n")
					continue
				}
				if s.MaxRecipients > 0 && len(recipients) >= s.MaxRecipients {
					write(w, "452 too many recipients\r\n")
					continue
				}
				recipients = append(recipients, addr)
				write(w, "250 OK\r\n")
			case "DATA":
				if from == "" || len(recipients) == 0 {
					write(w, "503 need MAIL and RCPT first\r\n")
					continue
				}
				write(w, "354 End data with <CR><LF>.<CR><LF>\r\n")
				inData = true
				dataBuf.Reset()
			case "RSET":
				from, recipients = "", nil
				inData = false
				write(w, "250 OK\r\n")
			case "NOOP":
				write(w, "250 OK\r\n")
			case "QUIT":
				write(w, "221 Bye\r\n")
				return
			default:
				write(w, "500 command not recognized\r\n")
			}
		}
	}

	startSession(conn, true)
}

// handleAuth processes an AUTH command (PLAIN or LOGIN). It returns whether the
// connection should be terminated (after exhausting attempts). The authTries
// counter is incremented for each failed attempt; once it reaches maxTries the
// connection is closed with a 421.
func (s *SMTPServer) handleAuth(
	write func(string, ...interface{}),
	r *bufio.Reader,
	arg string,
	authed *bool,
	authTries *int,
	maxTries int,
) (ok, closeConn bool) {
	mech, rest := splitCommand(arg)
	mech = strings.ToUpper(mech)

	var username, password string
	var parsed bool

	switch mech {
	case "PLAIN":
		username, password, parsed = s.parseAuthPlain(write, r, rest)
	case "LOGIN":
		username, password, parsed = s.parseAuthLogin(write, r, rest)
	default:
		write("504 unrecognized authentication type\r\n")
		return false, false
	}

	if !parsed {
		// Malformed/aborted challenge; do not count as a failed credential try.
		return false, false
	}

	if s.checkCredentials(username, password) {
		*authed = true
		if s.Logger != nil {
			s.Logger.Info("SMTP AUTH success  mechanism=%s user=%s", strings.ToLower(mech), username)
		}
		write("235 Authentication successful\r\n")
		return true, false
	}

	*authTries++
	if s.Logger != nil {
		s.Logger.Warn("SMTP AUTH failure  mechanism=%s user=%s attempt=%d", strings.ToLower(mech), username, *authTries)
	}
	if *authTries >= maxTries {
		write("421 too many failed authentication attempts\r\n")
		return false, true
	}
	write("535 Authentication failed\r\n")
	return false, false
}

// checkCredentials compares the supplied credentials against the configured
// users using constant-time comparison for the password.
func (s *SMTPServer) checkCredentials(username, password string) bool {
	for _, u := range s.Auth.Users {
		if subtle.ConstantTimeCompare([]byte(u.Username), []byte(username)) != 1 {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(u.Password), []byte(password)) == 1 {
			return true
		}
	}
	return false
}

// parseAuthPlain parses an AUTH PLAIN argument. The argument may be supplied
// inline (`AUTH PLAIN <base64>`) or on the following continuation line after a
// 334 prompt (when the initial argument is empty).
func (s *SMTPServer) parseAuthPlain(write func(string, ...interface{}), r *bufio.Reader, rest string) (string, string, bool) {
	b64 := strings.TrimSpace(rest)
	if b64 == "" {
		// Server issues a continuation prompt; client sends the blob next.
		write("334 \r\n")
		line, err := readLineNoData(r)
		if err != nil {
			write("501 authentication aborted\r\n")
			return "", "", false
		}
		b64 = strings.TrimSpace(line)
	}
	return decodePlainBlob(b64)
}

// parseAuthLogin handles the AUTH LOGIN challenge-response sequence. The
// username and password are each sent base64-encoded on their own line,
// either inline with the command or as continuation responses.
func (s *SMTPServer) parseAuthLogin(write func(string, ...interface{}), r *bufio.Reader, rest string) (string, string, bool) {
	userB64 := strings.TrimSpace(rest)
	var username string
	if userB64 == "" {
		write("334 %s\r\n", base64.StdEncoding.EncodeToString([]byte("Username:")))
		line, err := readLineNoData(r)
		if err != nil {
			write("501 authentication aborted\r\n")
			return "", "", false
		}
		userB64 = strings.TrimSpace(line)
	}
	u, ok := b64Decode(userB64)
	if !ok {
		write("501 invalid base64 username\r\n")
		return "", "", false
	}
	username = u

	write("334 %s\r\n", base64.StdEncoding.EncodeToString([]byte("Password:")))
	line, err := readLineNoData(r)
	if err != nil {
		write("501 authentication aborted\r\n")
		return "", "", false
	}
	p, ok := b64Decode(strings.TrimSpace(line))
	if !ok {
		write("501 invalid base64 password\r\n")
		return "", "", false
	}
	return username, p, true
}

// decodePlainBlob decodes a base64 AUTH PLAIN blob of the form
// `\0username\0password`.
func decodePlainBlob(b64 string) (string, string, bool) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", "", false
	}
	// Expect at least: \0 user \0 pass
	parts := strings.Split(string(raw), "\x00")
	if len(parts) < 3 {
		return "", "", false
	}
	// parts[0] is the authorization identity (often empty); use parts[1]/[2].
	return parts[1], parts[2], true
}

func b64Decode(s string) (string, bool) {
	dec, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", false
	}
	return string(dec), true
}

// readLineNoData reads a single line from the SMTP reader without interpreting
// DATA continuation. Used during AUTH challenge-response exchanges.
func readLineNoData(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "*" {
		return "", fmt.Errorf("client aborted")
	}
	return line, nil
}
func (s *SMTPServer) buildMessage(from string, recipients []string, raw string) (Message, error) {
	headers, body := splitHeadersBody(raw)
	subject := headers["Subject"]
	fromHeader := headers["From"]
	domain := domainOf(fromHeader)
	if domain == "" {
		domain = domainOf(from)
	}
	if s.AllowedDomains != nil && len(s.AllowedDomains) > 0 && !s.AllowedDomains[domain] {
		return Message{}, fmt.Errorf("sender domain %q not configured", domain)
	}

	msg := Message{
		ID:        newMessageID(),
		From:      from,
		To:        recipients,
		Subject:   subject,
		TextBody:  body,
		Headers:   headers,
		RawSize:   len(raw),
		Domain:    domain,
		CreatedAt: time.Now().UTC(),
	}
	if cc := headers["Cc"]; cc != "" {
		msg.Cc = splitAddresses(cc)
	}
	if bcc := headers["Bcc"]; bcc != "" {
		msg.Bcc = splitAddresses(bcc)
	}
	// Prefer an explicit HTML part if present in the body markers; otherwise
	// leave HTMLBody empty (provider will fall back to text).
	if strings.Contains(body, "<html") || strings.Contains(body, "<body") {
		msg.HTMLBody = body
	}
	return msg, nil
}

// parseMessage is the testable, connection-free parser used by buildMessage
// and unit tests.
func parseMessage(id string, from []string, to []string, raw string) (Message, error) {
	headers, body := splitHeadersBody(raw)
	domain := domainOf(headers["From"])
	if domain == "" && len(from) > 0 {
		domain = domainOf(from[0])
	}
	return Message{
		ID:       id,
		From:     firstOrEmpty(from),
		To:       to,
		Subject:  headers["Subject"],
		TextBody: body,
		Headers:  headers,
		RawSize:  len(raw),
		Domain:   domain,
	}, nil
}

// checkRecipients enforces the recipient limit.
func checkRecipients(recipients []string, max int) error {
	if max > 0 && len(recipients) > max {
		return fmt.Errorf("too many recipients: %d exceeds limit %d", len(recipients), max)
	}
	return nil
}

// validateAddress rejects empty, malformed, or injection-carrying addresses.
func validateAddress(addr string) error {
	if addr == "" {
		return errors.New("empty address")
	}
	if strings.ContainsAny(addr, "\r\n") {
		return errors.New("address contains CRLF (injection)")
	}
	at := strings.LastIndex(addr, "@")
	if at <= 0 || at == len(addr)-1 {
		return errors.New("malformed address")
	}
	local, host := addr[:at], addr[at+1:]
	if strings.Contains(local, "@") || !strings.Contains(host, ".") {
		return errors.New("malformed address")
	}
	return nil
}

// ---- small helpers ----

func splitCommand(line string) (string, string) {
	line = strings.TrimSpace(line)
	parts := strings.SplitN(line, " ", 2)
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], strings.TrimSpace(parts[1])
}

// parsePath extracts the address from a MAIL FROM / RCPT TO argument of the
// form `FROM:<addr>` or `TO:<addr>`. Angle brackets are optional.
func parsePath(arg string) (string, bool) {
	arg = strings.TrimSpace(arg)
	arg = strings.TrimPrefix(arg, "FROM:")
	arg = strings.TrimPrefix(arg, "TO:")
	arg = strings.TrimSpace(arg)
	if strings.HasPrefix(arg, "<") && strings.HasSuffix(arg, ">") {
		arg = arg[1 : len(arg)-1]
	}
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", false
	}
	return arg, true
}

// splitHeadersBody splits raw RFC822-ish message into headers map and body.
func splitHeadersBody(raw string) (map[string]string, string) {
	headers := map[string]string{}
	idx := strings.Index(raw, "\r\n\r\n")
	if idx < 0 {
		idx = strings.Index(raw, "\n\n")
	}
	if idx < 0 {
		return headers, raw
	}
	headerBlock := raw[:idx]
	body := raw[idx:]
	body = strings.TrimPrefix(body, "\r\n\r\n")
	body = strings.TrimPrefix(body, "\n\n")
	for _, line := range strings.Split(headerBlock, "\r\n") {
		if line == "" {
			continue
		}
		kv := strings.SplitN(line, ":", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		headers[key] = strings.TrimSpace(kv[1])
	}
	return headers, body
}

func splitAddresses(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func domainOf(addr string) string {
	return domainOfAddr(extractAddress(addr))
}

// extractAddress pulls the bare email address out of a header value that may be
// of the form `Name <addr>` or a bare `addr`.
func extractAddress(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "<"); i >= 0 {
		if j := strings.LastIndex(s, ">"); j > i {
			return strings.TrimSpace(s[i+1 : j])
		}
	}
	return s
}

func domainOfAddr(addr string) string {
	at := strings.LastIndex(addr, "@")
	if at < 0 {
		return ""
	}
	return strings.TrimSpace(addr[at+1:])
}

func firstOrEmpty(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// newMessageID returns a short random hex identifier.
func newMessageID() string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "000000"
	}
	return hex.EncodeToString(b)
}
