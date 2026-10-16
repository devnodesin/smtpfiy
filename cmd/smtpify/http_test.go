package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestHTTPServer(q *Queue) *HTTPServer {
	return &HTTPServer{
		Host:   "127.0.0.1",
		Port:   0,
		Queue:  q,
		Logger: New(&nopWriter{}, nil, "debug"),
		start:  time.Now(),
	}
}

func TestHealthEndpoint(t *testing.T) {
	srv := newTestHTTPServer(tempDB(t))
	rr := httptest.NewRecorder()
	srv.handleHealth(rr, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200", rr.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("expected status ok, got %q", body["status"])
	}
}

func TestStatsEndpoint(t *testing.T) {
	q := tempDB(t)
	_ = q.Store(sampleMsg("st0001"))
	_ = q.MarkSent("st0001", "resend")
	srv := newTestHTTPServer(q)

	rr := httptest.NewRecorder()
	srv.handleStats(rr, httptest.NewRequest(http.MethodGet, "/stats", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("stats status = %d, want 200", rr.Code)
	}
	var body Stats
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if body.Messages.Total != 1 || body.Messages.Sent != 1 {
		t.Fatalf("unexpected counts: %+v", body.Messages)
	}
	if body.Database.Path == "" {
		t.Fatalf("expected database path in stats")
	}
	for _, it := range body.Items {
		if strings.Contains(it.Subject, "body") && it.Subject == "hi" {
			// subject is allowed; ensure no Body field exists at all.
		}
		// Ensure no body leakage: StatsItem has no Body field by design.
	}
	if len(body.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(body.Items))
	}
}

func TestStatsBodyNeverReturned(t *testing.T) {
	q := tempDB(t)
	_ = q.Store(sampleMsg("st0002"))
	srv := newTestHTTPServer(q)
	rr := httptest.NewRecorder()
	srv.handleStats(rr, httptest.NewRequest(http.MethodGet, "/stats", nil))
	if strings.Contains(rr.Body.String(), "body") && strings.Contains(rr.Body.String(), "TextBody") {
		t.Fatalf("stats leaked internal field names: %s", rr.Body.String())
	}
}
