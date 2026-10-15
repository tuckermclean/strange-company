package gateway_test

import (
	"strings"
	"testing"

	"strange.company/sc/internal/gateway"
)

// 52-AC-8 (plan 52 section 9.4 D6): with SC_FORBID_PAID=1 the Gateway refuses the real provider
// hosts as upstream URLs, whatever the case, port or path.
func TestForbidPaidRefusesRealHosts(t *testing.T) {
	t.Setenv("SC_FORBID_PAID", "1")
	for _, u := range []string{
		"https://api.anthropic.com",
		"https://api.anthropic.com/v1/messages",
		"https://api.openai.com/v1",
		"https://API.Anthropic.com:443/v1/messages",
		"http://api.openai.com:80/v1/responses",
	} {
		err := gateway.ValidateUpstream(u)
		if err == nil {
			t.Errorf("SC_FORBID_PAID=1: upstream %q was accepted", u)
			continue
		}
		if !strings.Contains(strings.ToLower(err.Error()), "api.") {
			t.Errorf("refusal for %q should name the host, got %q", u, err)
		}
	}
}

// 52-AC-8: fake upstreams (loopback, in-cluster fake) stay usable with SC_FORBID_PAID=1.
func TestForbidPaidAllowsFakeHosts(t *testing.T) {
	t.Setenv("SC_FORBID_PAID", "1")
	for _, u := range []string{"http://127.0.0.1:9999/v1", "http://localhost:8080", "http://fake-provider.invalid/v1"} {
		if err := gateway.ValidateUpstream(u); err != nil {
			t.Errorf("SC_FORBID_PAID=1: fake upstream %q refused: %v", u, err)
		}
	}
}

// 52-AC-8: without SC_FORBID_PAID=1 the refusal is not applied (opt-in lanes use the real hosts).
func TestForbidPaidOffAllowsRealHosts(t *testing.T) {
	t.Setenv("SC_FORBID_PAID", "0")
	if err := gateway.ValidateUpstream("https://api.openai.com/v1"); err != nil {
		t.Errorf("SC_FORBID_PAID=0: real host refused: %v", err)
	}
}
