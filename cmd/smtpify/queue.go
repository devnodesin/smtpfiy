package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// QueueStatus enumerates delivery states for a queued message.
type QueueStatus string

const (
	StatusPending    QueueStatus = "pending"
	StatusProcessing QueueStatus = "processing"
	StatusSent       QueueStatus = "sent"
	StatusFailed     QueueStatus = "failed"
)

// StatsItem is a delivery record suitable for /stats. It intentionally omits
// the email body and any secrets.
type StatsItem struct {
	ID        string
	From      string
	To        []string
	Subject   string
	Provider  string
	Status    string
	Attempts  int
	CreatedAt string
	SentAt    string
	FailedAt  string
	LastError string
}

// Stats is the aggregated service statistics returned by /stats.
type Stats struct {
	Uptime   string
	Database DatabaseInfo
	Messages MessageCounts
	Items    []StatsItem
}

// DatabaseInfo describes the queue database.
type DatabaseInfo struct {
	Path string
	Size string
}

// MessageCounts holds totals by status.
type MessageCounts struct {
	Total      int
	Pending    int
	Processing int
	Sent       int
	Failed     int
}

// Queue is the persistent SQLite message queue.
type Queue struct {
	db   *sql.DB
	path string
}

// OpenQueue opens (creating if necessary) the SQLite database and ensures the
// messages table exists. Parent directories are created as needed.
func OpenQueue(path string) (*Queue, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("creating db dir %q: %w", dir, err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening db %q: %w", path, err)
	}
	db.SetMaxOpenConns(1) // serialize writes; simple and safe for a single relay.
	q := &Queue{db: db, path: path}
	if err := q.initSchema(); err != nil {
		db.Close()
		return nil, err
	}
	return q, nil
}

func (q *Queue) initSchema() error {
	const schema = `
CREATE TABLE IF NOT EXISTS messages (
	id          TEXT PRIMARY KEY,
	from_addr   TEXT NOT NULL,
	to_addrs    TEXT NOT NULL,
	subject     TEXT,
	text_body   TEXT,
	html_body   TEXT,
	domain      TEXT,
	status      TEXT NOT NULL,
	provider    TEXT,
	attempts    INTEGER NOT NULL DEFAULT 0,
	last_error  TEXT,
	created_at  TEXT NOT NULL,
	sent_at     TEXT,
	failed_at   TEXT
);`
	if _, err := q.db.Exec(schema); err != nil {
		return fmt.Errorf("creating schema: %w", err)
	}
	return nil
}

// Close closes the underlying database.
func (q *Queue) Close() error {
	return q.db.Close()
}

// Store inserts a message with status pending. Satisfies the MessageStore
// interface used by the SMTP server.
func (q *Queue) Store(m Message) error {
	const ins = `
INSERT INTO messages
	(id, from_addr, to_addrs, subject, text_body, html_body, domain, status, attempts, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`
	_, err := q.db.Exec(ins,
		m.ID, m.From, strings.Join(m.To, ","), m.Subject,
		m.TextBody, m.HTMLBody, m.Domain, string(StatusPending),
		m.CreatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("storing message %q: %w", m.ID, err)
	}
	return nil
}

// Pending returns messages eligible for delivery (pending or processing).
func (q *Queue) Pending() ([]Message, error) {
	const qry = `
SELECT id, from_addr, to_addrs, subject, text_body, html_body, domain, status,
       provider, attempts, last_error, created_at, sent_at, failed_at
FROM messages
WHERE status IN (?, ?)
ORDER BY created_at ASC`
	rows, err := q.db.Query(qry, string(StatusPending), string(StatusProcessing))
	if err != nil {
		return nil, fmt.Errorf("query pending: %w", err)
	}
	defer rows.Close()
	return scanMessages(rows)
}

// MarkSent records a successful delivery.
func (q *Queue) MarkSent(id, provider string) error {
	const upd = `
UPDATE messages
SET status = ?, provider = ?, sent_at = ?, last_error = ''
WHERE id = ?`
	_, err := q.db.Exec(upd, string(StatusSent), provider,
		time.Now().UTC().Format(time.RFC3339), id)
	if err != nil {
		return fmt.Errorf("mark sent %q: %w", id, err)
	}
	return nil
}

// MarkFailed records a failed attempt. It increments attempts and records the
// error and failed_at timestamp. The caller (retry engine) decides whether to
// keep the message pending (retry) or mark it failed; this method always keeps
// it retryable by leaving status as-is unless forceFailed is set.
func (q *Queue) MarkFailed(id, lastError string, forceFailed bool) error {
	status := string(StatusPending)
	if forceFailed {
		status = string(StatusFailed)
	}
	const upd = `
UPDATE messages
SET attempts = attempts + 1, last_error = ?, failed_at = ?, status = ?
WHERE id = ?`
	_, err := q.db.Exec(upd, lastError,
		time.Now().UTC().Format(time.RFC3339), status, id)
	if err != nil {
		return fmt.Errorf("mark failed %q: %w", id, err)
	}
	return nil
}

// Stats returns aggregated counts and recent items (no email body).
func (q *Queue) Stats() (Stats, error) {
	counts := MessageCounts{}
	const countQ = `SELECT status, COUNT(*) FROM messages GROUP BY status`
	rows, err := q.db.Query(countQ)
	if err != nil {
		return Stats{}, fmt.Errorf("stats counts: %w", err)
	}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			rows.Close()
			return Stats{}, fmt.Errorf("stats scan: %w", err)
		}
		counts.Total += n
		switch QueueStatus(status) {
		case StatusPending:
			counts.Pending = n
		case StatusProcessing:
			counts.Processing = n
		case StatusSent:
			counts.Sent = n
		case StatusFailed:
			counts.Failed = n
		}
	}
	rows.Close()

	const itemsQ = `
SELECT id, from_addr, to_addrs, subject, provider, status, attempts,
       created_at, sent_at, failed_at, last_error
FROM messages
ORDER BY created_at DESC
LIMIT 50`
	items, err := q.queryStatsItems(itemsQ)
	if err != nil {
		return Stats{}, err
	}

	size := "0 B"
	if fi, err := os.Stat(q.path); err == nil {
		size = fmt.Sprintf("%d B", fi.Size())
	}
	return Stats{
		Database: DatabaseInfo{Path: q.path, Size: size},
		Messages: counts,
		Items:    items,
	}, nil
}

func (q *Queue) queryStatsItems(query string, args ...interface{}) ([]StatsItem, error) {
	rows, err := q.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("stats items: %w", err)
	}
	defer rows.Close()
	var items []StatsItem
	for rows.Next() {
		var (
			id, from, to, subject, provider, status, createdAt, sentAt, failedAt, lastError sql.NullString
			attempts                                                                        int
		)
		if err := rows.Scan(&id, &from, &to, &subject, &provider, &status,
			&attempts, &createdAt, &sentAt, &failedAt, &lastError); err != nil {
			return nil, fmt.Errorf("stats item scan: %w", err)
		}
		items = append(items, StatsItem{
			ID:        id.String,
			From:      from.String,
			To:        splitCSV(to.String),
			Subject:   subject.String,
			Provider:  provider.String,
			Status:    status.String,
			Attempts:  attempts,
			CreatedAt: createdAt.String,
			SentAt:    sentAt.String,
			FailedAt:  failedAt.String,
			LastError: lastError.String,
		})
	}
	return items, nil
}

func scanMessages(rows *sql.Rows) ([]Message, error) {
	var out []Message
	for rows.Next() {
		var (
			id, from, to, subject, textBody, htmlBody, domain, status string
			provider, lastError, createdAt, sentAt, failedAt          sql.NullString
			attempts                                                  int
		)
		if err := rows.Scan(&id, &from, &to, &subject, &textBody, &htmlBody,
			&domain, &status, &provider, &attempts, &lastError, &createdAt,
			&sentAt, &failedAt); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		out = append(out, Message{
			ID:        id,
			From:      from,
			To:        splitCSV(to),
			Subject:   subject,
			TextBody:  textBody,
			HTMLBody:  htmlBody,
			Domain:    domain,
			Status:    status,
			Headers:   map[string]string{},
			RawSize:   len(textBody) + len(htmlBody),
			CreatedAt: parseTime(createdAt.String),
		})
	}
	return out, nil
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
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

func parseTime(s string) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}
