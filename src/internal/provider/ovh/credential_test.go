package ovh_test

import (
	"context"
	"encoding/json"
	"testing"

	"strange.company/sc/internal/provider/ovh"
)

func credFixture(t *testing.T, rel string) ovh.CurrentCredential {
	t.Helper()
	var c ovh.CurrentCredential
	if err := json.Unmarshal(fixture(t, rel), &c); err != nil {
		t.Fatalf("fixture %s: %v", rel, err)
	}
	return c
}

// 04-AC-5: a credential with rule {method: POST, path: /*} fails PF-07 with OVH_CREDENTIAL_TOO_BROAD.
func TestCredentialScope_PostWildcardTooBroad_04AC5(t *testing.T) {
	f := ovh.CheckCredentialScope(credFixture(t, "auth/current-credential-too-broad.json"), "")
	if f.ID != "PF-07" || f.Status != "fail" || f.Code != "OVH_CREDENTIAL_TOO_BROAD" {
		t.Errorf("finding = %+v, want PF-07 fail OVH_CREDENTIAL_TOO_BROAD", f)
	}
}

// 04-AC-5: the plan 04 section 10 rule set passes (vps, no cloud project).
func TestCredentialScope_Section10RuleSetPasses_04AC5(t *testing.T) {
	f := ovh.CheckCredentialScope(credFixture(t, "auth/current-credential-ok.json"), "")
	if f.ID != "PF-07" || f.Status != "pass" || f.Code != "" {
		t.Errorf("finding = %+v, want PF-07 pass", f)
	}
}

// 04-AC-5: rules are checked against the section 10 list as a subset (04-R-4).
func TestCredentialScope_SubsetRules_04AC5(t *testing.T) {
	rule := func(m, p string) ovh.AccessRule { return ovh.AccessRule{Method: m, Path: p} }
	cases := []struct {
		name    string
		project string
		rules   []ovh.AccessRule
		broad   bool
	}{
		{"subset of the list", "", []ovh.AccessRule{rule("GET", "/auth/time"), rule("GET", "/me")}, false},
		{"empty rule set", "", nil, false},
		{"GET wildcard", "", []ovh.AccessRule{rule("GET", "/*")}, true},
		{"DELETE on me", "", []ovh.AccessRule{rule("DELETE", "/me/*")}, true},
		{"POST on me", "", []ovh.AccessRule{rule("POST", "/me/*")}, true},
		{"PUT on order catalogue", "", []ovh.AccessRule{rule("PUT", "/order/catalog/*")}, true},
		{"domain routes", "", []ovh.AccessRule{rule("GET", "/domain/*")}, true},
		{"one broad rule among good ones", "", []ovh.AccessRule{rule("GET", "/me"), rule("POST", "/*")}, true},
		{"cloud project outside public-cloud", "", []ovh.AccessRule{rule("GET", "/cloud/project/abc123/*")}, true},
		{"own cloud project", "abc123", []ovh.AccessRule{rule("POST", "/cloud/project/abc123/*")}, false},
		{"other cloud project", "abc123", []ovh.AccessRule{rule("POST", "/cloud/project/zzz999/*")}, true},
		{"every cloud project", "abc123", []ovh.AccessRule{rule("POST", "/cloud/project/*")}, true},
		{"cloud root", "abc123", []ovh.AccessRule{rule("GET", "/cloud/*")}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := ovh.CheckCredentialScope(ovh.CurrentCredential{Rules: c.rules, Status: "validated"}, c.project)
			if c.broad {
				if f.Status != "fail" || f.Code != "OVH_CREDENTIAL_TOO_BROAD" || f.ID != "PF-07" {
					t.Errorf("finding = %+v, want PF-07 fail OVH_CREDENTIAL_TOO_BROAD", f)
				}
				return
			}
			if f.Status != "pass" || f.ID != "PF-07" {
				t.Errorf("finding = %+v, want PF-07 pass", f)
			}
		})
	}
}

// 04-AC-5 (plan 04 section 14.2 step 4): the client reads GET /auth/currentCredential, signed, and applies the check.
func TestCredentialScope_ThroughClient_04AC5(t *testing.T) {
	for _, c := range []struct{ fixture, status, code string }{
		{"auth/current-credential-too-broad.json", "fail", "OVH_CREDENTIAL_TOO_BROAD"},
		{"auth/current-credential-ok.json", "pass", ""},
	} {
		f := newFakeOVH(t, map[string]json.RawMessage{"GET /auth/currentCredential": fixture(t, c.fixture)})
		got, err := f.client().CredentialScope(context.Background(), "")
		if err != nil {
			t.Fatalf("%s: CredentialScope: %v", c.fixture, err)
		}
		if got.ID != "PF-07" || got.Status != c.status || got.Code != c.code {
			t.Errorf("%s: finding = %+v, want PF-07 %s %q", c.fixture, got, c.status, c.code)
		}
	}
}
