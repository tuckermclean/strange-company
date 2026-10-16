package gateway_test

import (
	"os"
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

// 52-AC-8 (CD-53): with SC_FORBID_PAID=1 only fake upstreams are accepted: loopback, localhost, and the
// reserved .invalid and .test names. Everything else is refused, including malformed values.
func TestForbidPaidAllowlistOnly_CD53(t *testing.T) {
	t.Setenv("SC_FORBID_PAID", "1")
	for _, u := range []string{
		"https://eu.api.anthropic.com", "https://api.mistral.ai", "https://203.0.113.5", "http://[2001:db8::1]",
		"api.anthropic.com", "api.anthropic.com:443", "ftp://localhost", "", "https://x@api.anthropic.com",
	} {
		if err := gateway.ValidateUpstream(u); err == nil {
			t.Errorf("SC_FORBID_PAID=1: upstream %q was accepted", u)
		}
	}
	for _, u := range []string{
		"http://127.0.0.1:8080", "http://[::1]", "http://localhost", "https://fake.invalid/v1", "https://provider.test",
	} {
		if err := gateway.ValidateUpstream(u); err != nil {
			t.Errorf("SC_FORBID_PAID=1: fake upstream %q refused: %v", u, err)
		}
	}
}

// 52-AC-8: with SC_FORBID_PAID unset or 0 real hosts stay accepted.
func TestForbidPaidUnsetAllowsRealHosts_CD53(t *testing.T) {
	for _, v := range []string{"0", "unset"} {
		t.Setenv("SC_FORBID_PAID", "0")
		if v == "unset" {
			os.Unsetenv("SC_FORBID_PAID")
		}
		for _, u := range []string{"https://api.anthropic.com", "https://api.mistral.ai", "https://eu.api.anthropic.com"} {
			if err := gateway.ValidateUpstream(u); err != nil {
				t.Errorf("SC_FORBID_PAID=%s: %q refused: %v", v, u, err)
			}
		}
	}
}

// 52-AC-8 (CD-53): userinfo does not decide the verdict; the host does. Fake hosts with userinfo are accepted,
// real hosts with userinfo stay refused.
func TestForbidPaidUserinfo_CD53(t *testing.T) {
	t.Setenv("SC_FORBID_PAID", "1")
	for _, u := range []string{"http://u:p@localhost:8080", "http://user@127.0.0.1"} {
		if err := gateway.ValidateUpstream(u); err != nil {
			t.Errorf("SC_FORBID_PAID=1: fake upstream with userinfo %q refused: %v", u, err)
		}
	}
	if err := gateway.ValidateUpstream("https://x@api.anthropic.com"); err == nil {
		t.Errorf("SC_FORBID_PAID=1: real host with userinfo was accepted")
	}
}
