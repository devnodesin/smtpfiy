package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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

func (s *SMTPServer) handleConn(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	defer w.Flush()

	write := func(format string, args ...interface{}) {
		fmt.Fprintf(w, format, args...)
		w.Flush()
	}

	write("220 smtpify SMTP ready\r\n")

	var (
		from       string
		recipients []string
		inData     bool
		dataBuf    strings.Builder
	)

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
					write("451 error processing message: %s\r\n", perr)
					from, recipients = "", nil
					continue
				}
				if serr := s.Store.Store(msg); serr != nil {
					write("451 failed to queue message: %s\r\n", serr)
					from, recipients = "", nil
					continue
				}
				if s.Logger != nil {
					s.Logger.Info("SMTP  id:%s (%s -> %s)",
						msg.ID, msg.From, strings.Join(msg.To, ","))
				}
				write("250 OK: message queued as %s\r\n", msg.ID)
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
			write("250 smtpify\r\n")
		case "MAIL":
			addr, ok := parsePath(arg)
			if !ok {
				write("501 malformed MAIL FROM\r\n")
				continue
			}
			if err := validateAddress(addr); err != nil {
				write("501 bad sender address\r\n")
				continue
			}
			from = addr
			write("250 OK\r\n")
		case "RCPT":
			addr, ok := parsePath(arg)
			if !ok {
				write("501 malformed RCPT TO\r\n")
				continue
			}
			if err := validateAddress(addr); err != nil {
				write("501 bad recipient address\r\n")
				continue
			}
			if s.MaxRecipients > 0 && len(recipients) >= s.MaxRecipients {
				write("452 too many recipients\r\n")
				continue
			}
			recipients = append(recipients, addr)
			write("250 OK\r\n")
		case "DATA":
			if from == "" || len(recipients) == 0 {
				write("503 need MAIL and RCPT first\r\n")
				continue
			}
			write("354 End data with <CR><LF>.<CR><LF>\r\n")
			inData = true
			dataBuf.Reset()
		case "RSET":
			from, recipients = "", nil
			inData = false
			write("250 OK\r\n")
		case "NOOP":
			write("250 OK\r\n")
		case "QUIT":
			write("221 Bye\r\n")
			return
		default:
			write("500 command not recognized\r\n")
		}
	}
}

// buildMessage parses the raw DATA and assembles a normalized Message.
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
