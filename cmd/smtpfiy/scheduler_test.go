package main

import (
	"testing"
)

func providerConfigs(names ...string) []ProviderConfig {
	out := make([]ProviderConfig, 0, len(names))
	for _, n := range names {
		out = append(out, ProviderConfig{Provider: n})
	}
	return out
}

func TestSchedulerOrderStatic(t *testing.T) {
	s := NewScheduler("order")
	cfgs := providerConfigs("resend", "brevo", "mailgun")
	order := s.Select("example.com", cfgs)
	if len(order) != 3 {
		t.Fatalf("expected 3 providers, got %d", len(order))
	}
	for i, want := range []string{"resend", "brevo", "mailgun"} {
		if order[i].Name() != want {
			t.Fatalf("order[%d] = %s, want %s", i, order[i].Name(), want)
		}
	}
}

func TestSchedulerRoundRobinRotates(t *testing.T) {
	s := NewScheduler("rr")
	cfgs := providerConfigs("resend", "brevo", "mailgun")

	first := s.Select("example.com", cfgs)
	if first[0].Name() != "resend" {
		t.Fatalf("first primary = %s, want resend", first[0].Name())
	}
	second := s.Select("example.com", cfgs)
	if second[0].Name() != "brevo" {
		t.Fatalf("second primary = %s, want brevo", second[0].Name())
	}
	third := s.Select("example.com", cfgs)
	if third[0].Name() != "mailgun" {
		t.Fatalf("third primary = %s, want mailgun", third[0].Name())
	}
	// wraps back to resend
	fourth := s.Select("example.com", cfgs)
	if fourth[0].Name() != "resend" {
		t.Fatalf("fourth primary = %s, want resend", fourth[0].Name())
	}
}

func TestSchedulerRoundRobinIsPerDomain(t *testing.T) {
	s := NewScheduler("rr")
	a := providerConfigs("resend", "brevo")
	b := providerConfigs("mailgun", "sendgrid")

	if s.Select("a.com", a)[0].Name() != "resend" {
		t.Fatal("domain a first primary should be resend")
	}
	// domain b maintains its own rotation, starting at its first provider.
	if s.Select("b.com", b)[0].Name() != "mailgun" {
		t.Fatal("domain b first primary should be mailgun")
	}
	// domain a advances independently.
	if s.Select("a.com", a)[0].Name() != "brevo" {
		t.Fatal("domain a second primary should be brevo")
	}
}

func TestSchedulerNoProvidersError(t *testing.T) {
	s := NewScheduler("order")
	if _, err := s.SelectErr("empty.com", nil); err == nil {
		t.Fatal("expected error for domain with no providers")
	}
}

func TestSchedulerBuildsBrevoProvider(t *testing.T) {
	s := NewScheduler("order")
	cfgs := providerConfigs("brevo")
	order := s.Select("example.com", cfgs)
	if len(order) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(order))
	}
	if _, ok := order[0].(*brevoProvider); !ok {
		t.Fatalf("expected *brevoProvider, got %T", order[0])
	}
	if order[0].Name() != "brevo" {
		t.Fatalf("expected name brevo, got %q", order[0].Name())
	}
}
