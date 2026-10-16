package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
		return "", &ProviderError{
			Provider:   p.Name(),
			StatusCode: 0,
			Retryable:  true,
			Message:    "network error: " + err.Error(),
		}
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
	retryable := resp.StatusCode == http.StatusTooManyRequests ||
		resp.StatusCode >= 500
	message := strings.TrimSpace(string(respBody))
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}
	return "", &ProviderError{
		Provider:   p.Name(),
		StatusCode: resp.StatusCode,
		Retryable:  retryable,
		Message:    message,
	}
}
