package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Provider delivers a normalized Message through a provider's API.
type Provider interface {
	Name() string
	Send(ctx context.Context, msg Message) (providerID string, err error)
}

// ProviderError is a typed delivery error carrying retryability information.
type ProviderError struct {
	Provider   string
	StatusCode int
	Retryable  bool
	Message    string
	// RetryAfter, when > 0, is the suggested wait before the next attempt
	// (e.g. from a provider's rate-limit reset header). The retry engine
	// prefers it over the fixed retry_delay, capped at Defaults.MaxRetryAfter.
	RetryAfter time.Duration
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("provider %s: %s (status=%d retryable=%v)",
		e.Provider, e.Message, e.StatusCode, e.Retryable)
}

// asProviderError extracts a *ProviderError if present.
func asProviderError(err error, target **ProviderError) bool {
	if err == nil {
		return false
	}
	if pe, ok := err.(*ProviderError); ok {
		*target = pe
		return true
	}
	return false
}

// newNetworkError builds the retryable ProviderError returned when the
// transport itself fails (connect, TLS, timeout).
func newNetworkError(provider string, err error) *ProviderError {
	return &ProviderError{
		Provider:   provider,
		StatusCode: 0,
		Retryable:  true,
		Message:    "network error: " + err.Error(),
	}
}

// classifyRetryable reports whether a non-2xx status code is worth retrying.
func classifyRetryable(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests || statusCode >= 500
}

// cleanProviderMessage strips control characters (e.g. CR/LF that could split
// log lines) and bounds the length of untrusted provider response bodies
// before they are logged or stored.
func cleanProviderMessage(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	const max = 200
	if len(s) > max {
		s = s[:max]
	}
	return s
}

// providerErrorMessage formats an untrusted provider response body for
// logging/storage: it strips control characters and falls back to the HTTP
// status text when the body is empty.
func providerErrorMessage(statusCode int, body []byte) string {
	msg := cleanProviderMessage(strings.TrimSpace(string(body)))
	if msg == "" {
		msg = http.StatusText(statusCode)
	}
	return msg
}

// resendProvider delivers email via the Resend REST API.
type resendProvider struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// resendRequest is the JSON payload sent to Resend.
type resendRequest struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	Text    string   `json:"text,omitempty"`
	HTML    string   `json:"html,omitempty"`
	Cc      []string `json:"cc,omitempty"`
	Bcc     []string `json:"bcc,omitempty"`
	ReplyTo []string `json:"reply_to,omitempty"`
}

// resendResponse is the success envelope returned by Resend.
type resendResponse struct {
	ID      string `json:"id"`
	Message string `json:"message"`
}

// NewResendProvider constructs a Resend adapter. baseURL overrides the default
// endpoint (useful for testing); pass the empty string to use production.
func NewResendProvider(apiKey, baseURL string, timeout time.Duration) Provider {
	if baseURL == "" {
		baseURL = "https://api.resend.com"
	}
	return &resendProvider{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func (p *resendProvider) Name() string { return "resend" }

func (p *resendProvider) Send(ctx context.Context, msg Message) (string, error) {
	body, err := json.Marshal(resendRequest{
		From:    msg.From,
		To:      msg.To,
		Subject: msg.Subject,
		Text:    msg.TextBody,
		HTML:    msg.HTMLBody,
		Cc:      msg.Cc,
		Bcc:     msg.Bcc,
	})
	if err != nil {
		return "", fmt.Errorf("resend: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/emails", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("resend: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		// Network/timeout errors are retryable.
		return "", newNetworkError(p.Name(), err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var r resendResponse
		if err := json.Unmarshal(respBody, &r); err != nil {
			return "", fmt.Errorf("resend: decode response: %w", err)
		}
		if r.ID == "" {
			return "", &ProviderError{
				Provider:   p.Name(),
				StatusCode: resp.StatusCode,
				Retryable:  false,
				Message:    "empty id in success response",
			}
		}
		return r.ID, nil
	}

	// Classify error.
	return "", &ProviderError{
		Provider:   p.Name(),
		StatusCode: resp.StatusCode,
		Retryable:  classifyRetryable(resp.StatusCode),
		Message:    providerErrorMessage(resp.StatusCode, respBody),
	}
}

// brevoContact is a Brevo sender/recipient object: a bare email address. V2
// sends bare addresses only; display names are not sent.
type brevoContact struct {
	Email string `json:"email"`
}

// brevoRequest is the JSON payload sent to Brevo's send-transactional-email
// endpoint. Custom message headers are not forwarded in V2.
type brevoRequest struct {
	Sender      brevoContact   `json:"sender"`
	To          []brevoContact `json:"to"`
	Subject     string         `json:"subject"`
	HTMLContent string         `json:"htmlContent,omitempty"`
	TextContent string         `json:"textContent,omitempty"`
	Cc          []brevoContact `json:"cc,omitempty"`
	Bcc         []brevoContact `json:"bcc,omitempty"`
}

// brevoResponse is the success envelope returned by Brevo.
type brevoResponse struct {
	MessageID string `json:"messageId"`
}

// brevoProvider delivers email via the Brevo (formerly Sendinblue) REST API.
type brevoProvider struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// NewBrevoProvider constructs a Brevo adapter. baseURL overrides the default
// endpoint (useful for testing); pass the empty string to use production.
func NewBrevoProvider(apiKey, baseURL string, timeout time.Duration) Provider {
	if baseURL == "" {
		baseURL = "https://api.brevo.com/v3"
	}
	return &brevoProvider{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func (p *brevoProvider) Name() string { return "brevo" }

func (p *brevoProvider) Send(ctx context.Context, msg Message) (string, error) {
	body, err := json.Marshal(brevoRequest{
		Sender:      brevoContact{Email: msg.From},
		To:          toBrevoContacts(msg.To),
		Subject:     msg.Subject,
		HTMLContent: msg.HTMLBody,
		TextContent: msg.TextBody,
		Cc:          toBrevoContacts(msg.Cc),
		Bcc:         toBrevoContacts(msg.Bcc),
	})
	if err != nil {
		return "", fmt.Errorf("brevo: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/smtp/email", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("brevo: build request: %w", err)
	}
	req.Header.Set("api-key", p.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		// Network/timeout errors are retryable.
		return "", newNetworkError(p.Name(), err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var r brevoResponse
		if err := json.Unmarshal(respBody, &r); err != nil {
			return "", fmt.Errorf("brevo: decode response: %w", err)
		}
		if r.MessageID == "" {
			return "", &ProviderError{
				Provider:   p.Name(),
				StatusCode: resp.StatusCode,
				Retryable:  false,
				Message:    "empty messageId in success response",
			}
		}
		return r.MessageID, nil
	}

	// Classify error.
	pe := &ProviderError{
		Provider:   p.Name(),
		StatusCode: resp.StatusCode,
		Retryable:  classifyRetryable(resp.StatusCode),
		Message:    providerErrorMessage(resp.StatusCode, respBody),
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		// x-sib-ratelimit-reset is seconds until the rate limit resets.
		if secs, perr := strconv.Atoi(resp.Header.Get("x-sib-ratelimit-reset")); perr == nil && secs > 0 {
			pe.RetryAfter = time.Duration(secs) * time.Second
		}
	}
	return "", pe
}

// toBrevoContacts converts bare address strings to Brevo contact objects.
func toBrevoContacts(addrs []string) []brevoContact {
	out := make([]brevoContact, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, brevoContact{Email: a})
	}
	return out
}
