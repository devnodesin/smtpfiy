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

func TestBrevoSendSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/smtp/email" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("api-key") != "xkeysib-testkey" {
			t.Errorf("missing/wrong api-key header, got %q", r.Header.Get("api-key"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("missing json content type")
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]interface{}
		_ = json.Unmarshal(body, &req)
		sender, _ := req["sender"].(map[string]interface{})
		if sender["email"] != "sender@example.com" {
			t.Errorf("unexpected sender %v", req["sender"])
		}
		to, _ := req["to"].([]interface{})
		if len(to) != 1 {
			t.Errorf("unexpected to %v", req["to"])
		} else if toFirst, _ := to[0].(map[string]interface{}); toFirst["email"] != "rcpt@example.net" {
			t.Errorf("unexpected to %v", req["to"])
		}
		if req["subject"] != "hi" {
			t.Errorf("unexpected subject %v", req["subject"])
		}
		if req["htmlContent"] != "<b>hi</b>" {
			t.Errorf("unexpected htmlContent %v", req["htmlContent"])
		}
		if req["textContent"] != "hello" {
			t.Errorf("unexpected textContent %v", req["textContent"])
		}
		cc, _ := req["cc"].([]interface{})
		if len(cc) != 1 {
			t.Errorf("unexpected cc %v", req["cc"])
		} else if ccFirst, _ := cc[0].(map[string]interface{}); ccFirst["email"] != "cc@example.net" {
			t.Errorf("unexpected cc %v", req["cc"])
		}
		bcc, _ := req["bcc"].([]interface{})
		if len(bcc) != 1 {
			t.Errorf("unexpected bcc %v", req["bcc"])
		} else if bccFirst, _ := bcc[0].(map[string]interface{}); bccFirst["email"] != "bcc@example.net" {
			t.Errorf("unexpected bcc %v", req["bcc"])
		}
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"messageId":"<123456@sibautomation.com>"}`)
	}))
	defer srv.Close()

	p := NewBrevoProvider("xkeysib-testkey", srv.URL, 5*time.Second)
	id, err := p.Send(context.Background(), Message{
		From:     "sender@example.com",
		To:       []string{"rcpt@example.net"},
		Cc:       []string{"cc@example.net"},
		Bcc:      []string{"bcc@example.net"},
		Subject:  "hi",
		TextBody: "hello",
		HTMLBody: "<b>hi</b>",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if id != "<123456@sibautomation.com>" {
		t.Fatalf("unexpected provider id %q", id)
	}
}

func TestBrevoSendPermanentError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"code":"unauthorized","message":"invalid api key"}`)
	}))
	defer srv.Close()

	p := NewBrevoProvider("bad", srv.URL, 5*time.Second)
	_, err := p.Send(context.Background(), Message{From: "a@b.com", To: []string{"c@d.com"}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var perr *ProviderError
	if !asProviderError(err, &perr) || perr.Retryable {
		t.Fatalf("expected permanent (non-retryable) error, got %v", err)
	}
}

func TestBrevoSendRetryableError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"code":"internal","message":"oops"}`)
	}))
	defer srv.Close()

	p := NewBrevoProvider("key", srv.URL, 5*time.Second)
	_, err := p.Send(context.Background(), Message{From: "a@b.com", To: []string{"c@d.com"}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var perr *ProviderError
	if !asProviderError(err, &perr) || !perr.Retryable {
		t.Fatalf("expected retryable error, got %v", err)
	}
}

func TestBrevoSendRateLimitReset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-sib-ratelimit-limit", "1000")
		w.Header().Set("x-sib-ratelimit-remaining", "0")
		w.Header().Set("x-sib-ratelimit-reset", "45")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"code":"rate_limit","message":"rate limit exceeded"}`)
	}))
	defer srv.Close()

	p := NewBrevoProvider("key", srv.URL, 5*time.Second)
	_, err := p.Send(context.Background(), Message{From: "a@b.com", To: []string{"c@d.com"}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var perr *ProviderError
	if !asProviderError(err, &perr) || !perr.Retryable {
		t.Fatalf("expected retryable error, got %v", err)
	}
	if perr.RetryAfter != 45*time.Second {
		t.Fatalf("expected RetryAfter 45s from x-sib-ratelimit-reset, got %v", perr.RetryAfter)
	}
}

func TestBrevoSendTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	p := NewBrevoProvider("key", srv.URL, 20*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := p.Send(ctx, Message{From: "a@b.com", To: []string{"c@d.com"}})
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestBrevoRequestDoesNotLeakKeyInBody(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"messageId":"<x>"}`)
	}))
	defer srv.Close()

	p := NewBrevoProvider("xkeysib_secretKEY", srv.URL, 5*time.Second)
	_, _ = p.Send(context.Background(), Message{From: "a@b.com", To: []string{"c@d.com"}, Subject: "s", TextBody: "b"})
	if strings.Contains(gotBody, "xkeysib_secretKEY") {
		t.Fatalf("API key leaked into request body: %s", gotBody)
	}
}
