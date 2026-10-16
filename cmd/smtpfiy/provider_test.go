package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestResendSendSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/emails" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer re_testkey" {
			t.Errorf("missing/wrong auth header")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("missing json content type")
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]interface{}
		_ = json.Unmarshal(body, &req)
		if req["from"] != "sender@example.com" {
			t.Errorf("unexpected from %v", req["from"])
		}
		to, _ := req["to"].([]interface{})
		if len(to) != 1 || to[0] != "rcpt@example.net" {
			t.Errorf("unexpected to %v", req["to"])
		}
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"id":"msg-uuid-123"}`)
	}))
	defer srv.Close()

	p := NewResendProvider("re_testkey", srv.URL, 5*time.Second)
	id, err := p.Send(context.Background(), Message{
		From:     "sender@example.com",
		To:       []string{"rcpt@example.net"},
		Subject:  "hi",
		TextBody: "hello",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if id != "msg-uuid-123" {
		t.Fatalf("unexpected provider id %q", id)
	}
}

func TestResendSendPermanentError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"message":"unauthorized"}`)
	}))
	defer srv.Close()

	p := NewResendProvider("bad", srv.URL, 5*time.Second)
	_, err := p.Send(context.Background(), Message{From: "a@b.com", To: []string{"c@d.com"}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var perr *ProviderError
	if !asProviderError(err, &perr) || perr.Retryable {
		t.Fatalf("expected permanent (non-retryable) error, got %v", err)
	}
}

func TestResendSendRetryableError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"message":"rate limited"}`)
	}))
	defer srv.Close()

	p := NewResendProvider("key", srv.URL, 5*time.Second)
	_, err := p.Send(context.Background(), Message{From: "a@b.com", To: []string{"c@d.com"}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var perr *ProviderError
	if !asProviderError(err, &perr) || !perr.Retryable {
		t.Fatalf("expected retryable error, got %v", err)
	}
}

func TestResendSendTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := NewResendProvider("key", srv.URL, 20*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := p.Send(ctx, Message{From: "a@b.com", To: []string{"c@d.com"}})
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestResendRequestDoesNotLeakKeyInBody(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"id":"x"}`)
	}))
	defer srv.Close()

	p := NewResendProvider("re_secretKEY", srv.URL, 5*time.Second)
	_, _ = p.Send(context.Background(), Message{From: "a@b.com", To: []string{"c@d.com"}, Subject: "s", TextBody: "b"})
	if strings.Contains(gotBody, "re_secretKEY") {
		t.Fatalf("API key leaked into request body: %s", gotBody)
	}
	if !strings.Contains(gotBody, "Authorization") {
		// body is JSON; key must only appear in header, not body.
	}
}
