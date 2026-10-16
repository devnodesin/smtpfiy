package main

import (
	"context"
	"fmt"
	"sync"
)

// Scheduler selects, for a sender domain, the ordered list of providers to
// attempt (primary first, then fallbacks within the same domain).
type Scheduler struct {
	strategy string
	mu       sync.Mutex
	rrIndex  map[string]int
}

// NewScheduler builds a scheduler for the given strategy ("order" or "rr").
func NewScheduler(strategy string) *Scheduler {
	return &Scheduler{
		strategy: strategy,
		rrIndex:  map[string]int{},
	}
}

// providerFactory builds a Provider from a provider config. Only "resend" is
// implemented in V1; other providers return a stub so selection/ordering logic
// is exercised and fails clearly at send time until implemented.
func providerFactory(cfg ProviderConfig) (Provider, error) {
	switch cfg.Provider {
	case "resend":
		return NewResendProvider(cfg.APIKey, "", 0), nil
	default:
		return &unsupportedProvider{name: cfg.Provider}, nil
	}
}

// Select returns the providers for a domain in attempt order. It never returns
// nil on success; ordering depends on the strategy.
func (s *Scheduler) Select(domain string, cfgs []ProviderConfig) []Provider {
	out, _ := s.SelectErr(domain, cfgs)
	return out
}

// SelectErr is like Select but returns an error when the domain has no
// configured providers.
func (s *Scheduler) SelectErr(domain string, cfgs []ProviderConfig) ([]Provider, error) {
	if len(cfgs) == 0 {
		return nil, fmt.Errorf("no providers configured for domain %q", domain)
	}
	built := make([]Provider, 0, len(cfgs))
	for _, c := range cfgs {
		p, err := providerFactory(c)
		if err != nil {
			return nil, err
		}
		built = append(built, p)
	}

	if s.strategy != "rr" {
		// "order": configuration order.
		return built, nil
	}

	s.mu.Lock()
	idx := s.rrIndex[domain] % len(built)
	s.rrIndex[domain] = idx + 1
	s.mu.Unlock()

	// Rotate so the selected primary is first, preserving fallback order.
	rotated := make([]Provider, len(built))
	for i := range built {
		rotated[i] = built[(idx+i)%len(built)]
	}
	return rotated, nil
}

// unsupportedProvider is a placeholder for providers not yet implemented.
type unsupportedProvider struct {
	name string
}

func (u *unsupportedProvider) Name() string { return u.name }

func (u *unsupportedProvider) Send(ctx context.Context, msg Message) (string, error) {
	return "", fmt.Errorf("provider %q not implemented in V1", u.name)
}
