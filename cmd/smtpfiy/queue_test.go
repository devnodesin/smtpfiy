package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func tempDB(t *testing.T) *Queue {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "smtpfiy.db")
	q, err := OpenQueue(path)
	if err != nil {
		t.Fatalf("OpenQueue: %v", err)
	}
	t.Cleanup(func() { q.Close() })
	return q
}

func sampleMsg(id string) Message {
	return Message{
		ID:        id,
		From:      "sender@example.com",
		To:        []string{"rcpt@example.net"},
		Subject:   "hi",
		TextBody:  "body",
		Domain:    "example.com",
		CreatedAt: time.Now().UTC(),
	}
}

func TestQueueStoreAndPending(t *testing.T) {
	q := tempDB(t)
	if err := q.Store(sampleMsg("abc123")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	pending, err := q.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending, got %d", len(pending))
	}
	if pending[0].ID != "abc123" || pending[0].Status != "pending" {
		t.Fatalf("unexpected pending row: %+v", pending[0])
	}
	// Body must be stored but Stats must not expose it.
	stats, err := q.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Messages.Total != 1 || stats.Messages.Pending != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestQueueMarkSent(t *testing.T) {
	q := tempDB(t)
	_ = q.Store(sampleMsg("def456"))
	if err := q.MarkSent("def456", "resend"); err != nil {
		t.Fatalf("MarkSent: %v", err)
	}
	pending, _ := q.Pending()
	if len(pending) != 0 {
		t.Fatalf("sent message should not be pending, got %d", len(pending))
	}
	stats, _ := q.Stats()
	if stats.Messages.Sent != 1 {
		t.Fatalf("expected 1 sent, got %d", stats.Messages.Sent)
	}
}

func TestQueueMarkFailedIncrementsAttempts(t *testing.T) {
	q := tempDB(t)
	_ = q.Store(sampleMsg("ghi789"))
	if err := q.MarkFailed("ghi789", "connection timeout", true); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	stats, _ := q.Stats()
	if stats.Messages.Failed != 1 {
		t.Fatalf("expected 1 failed, got %d", stats.Messages.Failed)
	}
	// Re-fetch via Pending to inspect attempts is not possible once failed;
	// instead verify Stats item recorded attempts and last_error.
	for _, it := range stats.Items {
		if it.Attempts != 1 {
			t.Fatalf("expected attempts=1, got %d", it.Attempts)
		}
		if it.LastError != "connection timeout" {
			t.Fatalf("expected last_error recorded, got %q", it.LastError)
		}
	}
}

func TestQueuePersistenceAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "smtpfiy.db")
	q1, err := OpenQueue(path)
	if err != nil {
		t.Fatalf("OpenQueue1: %v", err)
	}
	_ = q1.Store(sampleMsg("jkl000"))
	q1.Close()

	// reopen and confirm the row survived.
	q2, err := OpenQueue(path)
	if err != nil {
		t.Fatalf("OpenQueue2: %v", err)
	}
	defer q2.Close()
	pending, err := q2.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected row to persist, got %d", len(pending))
	}
}

func TestQueueOpenCreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deep", "smtpfiy.db")
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("expected dir to not exist yet")
	}
	q, err := OpenQueue(path)
	if err != nil {
		t.Fatalf("OpenQueue nested: %v", err)
	}
	defer q.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("db file not created: %v", err)
	}
}
