package cli_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"strange.company/sc/internal/bootstrap/cli"
)

// --- finding 1: redaction (plan 50 section 10.2, 50-AC-15)

// A probe error that echoes the API key must not put the key on stdout or stderr, in text or JSON.
func TestPreflight_ProbeErrorKeyIsRedacted_50AC15(t *testing.T) {
	const key = "fixture-secret-anthropic-key-0006"
	for _, format := range []string{"text", "json"} {
		i := newInst(t)
		i.probeErr = errors.New("401 invalid x-api-key " + key + " for the request")
		code, out := i.exec(nil, i.preflightArgs(append(i.goodConfirms(), "--log-format", format)...)...)
		if code != 3 {
			t.Fatalf("%s: exit %d, want 3:\n%s", format, code, out)
		}
		contains(t, out, "PF-19")
		if strings.Contains(out, key) {
			t.Errorf("%s: the API key leaked through the probe error:\n%s", format, out)
		}
	}
}

// --- finding 2 (D7): the production probe is built from the policy

type hit struct{ Path, XAPI, Auth, Goog string }

type recorder struct {
	mu   sync.Mutex
	hits []hit
}

func (r *recorder) handler(status int, location string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.hits = append(r.hits, hit{req.URL.Path, req.Header.Get("x-api-key"), req.Header.Get("Authorization"), req.Header.Get("x-goog-api-key")})
		r.mu.Unlock()
		if location != "" {
			w.Header().Set("Location", location)
		}
		w.WriteHeader(status)
	}
}

func (r *recorder) count() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.hits) }

const probeKey = "fixture-secret-probe-key-0007"

// (a) the request goes to baseUrl + /models with the header credential.authScheme names.
func TestModelKeyProbe_RequestFollowsPolicy_50AC15(t *testing.T) {
	for _, tc := range []struct {
		scheme string
		want   hit
	}{
		{"x-api-key", hit{Path: "/v1/models", XAPI: probeKey}},
		{"bearer", hit{Path: "/v1/models", Auth: "Bearer " + probeKey}},
		{"x-goog-api-key", hit{Path: "/v1/models", Goog: probeKey}},
	} {
		rec := &recorder{}
		srv := httptest.NewTLSServer(rec.handler(200, ""))
		probe := cli.NewModelKeyProbe(srv.Client(), map[string]cli.ProbeTarget{"acme": {BaseURL: srv.URL + "/v1", AuthScheme: tc.scheme}})
		if probe == nil {
			srv.Close()
			t.Fatalf("%s: NewModelKeyProbe returned nil", tc.scheme)
		}
		if err := probe("acme", probeKey); err != nil {
			t.Errorf("%s: a 200 answer is an accepted key, got %v", tc.scheme, err)
		}
		if !reflect.DeepEqual(rec.hits, []hit{tc.want}) {
			t.Errorf("%s: requests = %+v, want exactly %+v", tc.scheme, rec.hits, tc.want)
		}
		srv.Close()
	}
}

// A rejected key fails, and the error does not carry the key; an unknown provider has no target.
func TestModelKeyProbe_RejectionAndUnknownProvider_50AC15(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewTLSServer(rec.handler(401, ""))
	defer srv.Close()
	probe := cli.NewModelKeyProbe(srv.Client(), map[string]cli.ProbeTarget{"acme": {BaseURL: srv.URL + "/v1", AuthScheme: "bearer"}})
	if probe == nil {
		t.Fatal("NewModelKeyProbe returned nil")
	}
	err := probe("acme", probeKey)
	if err == nil {
		t.Fatal("a 401 must fail the probe")
	}
	if strings.Contains(err.Error(), probeKey) {
		t.Errorf("the error carries the key: %v", err)
	}
	before := rec.count()
	if probe("nobody", probeKey) == nil {
		t.Error("a provider with no policy target must fail the probe")
	}
	if rec.count() != before {
		t.Error("a request was sent for a provider that is not in the policy")
	}
}

// (b) a redirect is not followed: the target never receives the request, so the key never goes there.
func TestModelKeyProbe_RedirectNotFollowed_50AC15(t *testing.T) {
	other := &recorder{}
	target := httptest.NewTLSServer(other.handler(200, ""))
	defer target.Close()
	first := &recorder{}
	srv := httptest.NewTLSServer(first.handler(302, target.URL+"/v1/models"))
	defer srv.Close()
	client := srv.Client() // follows redirects by default; the probe must not use that behavior
	probe := cli.NewModelKeyProbe(client, map[string]cli.ProbeTarget{"acme": {BaseURL: srv.URL + "/v1", AuthScheme: "bearer"}})
	if probe == nil {
		t.Fatal("NewModelKeyProbe returned nil")
	}
	if probe("acme", probeKey) == nil {
		t.Error("a 302 is not an accepted key")
	}
	if first.count() != 1 {
		t.Errorf("the policy host received %d requests, want 1", first.count())
	}
	if n := other.count(); n != 0 {
		t.Errorf("the redirect target received %d request(s): %+v", n, other.hits)
	}
	if client.CheckRedirect != nil {
		t.Error("the probe mutated the caller's http.Client")
	}
}

// (c) an http:// base URL is refused before anything is sent.
func TestModelKeyProbe_RefusesPlainHTTP_50AC15(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler(200, ""))
	defer srv.Close()
	probe := cli.NewModelKeyProbe(srv.Client(), map[string]cli.ProbeTarget{"acme": {BaseURL: srv.URL + "/v1", AuthScheme: "bearer"}})
	if probe == nil {
		t.Fatal("NewModelKeyProbe returned nil")
	}
	err := probe("acme", probeKey)
	if err == nil {
		t.Fatal("an http:// base URL must be refused")
	}
	if strings.Contains(err.Error(), probeKey) {
		t.Errorf("the error carries the key: %v", err)
	}
	if rec.count() != 0 {
		t.Errorf("a request was sent over plain http: %+v", rec.hits)
	}
}

// --- finding 2: PF-19 and PF-29 iterate the policy, keyed by credential.secretKey

// customProvider renames the disabled fixture provider "deepseek" to "acme-eu" (secretKey stays deepseek, so its key
// is DEEPSEEK_API_KEY), enabled and production, and gives it a budget.
func customProvider(i *inst, withKey bool) {
	i.t.Helper()
	mr := schemaExample(i.t, "model-routing.schema.yaml")
	prov := sub(mr, "spec", "providers")
	p := sub(prov, "deepseek")
	delete(prov, "deepseek")
	p["enabled"] = true
	p["stage"] = "production"
	prov["acme-eu"] = p
	i.writePolicy("model-routing.yaml", mr)

	bp := schemaExample(i.t, "budget-policy.schema.yaml")
	sub(bp, "spec", "global")["monthlyUsd"] = budgetMonthly
	sub(bp, "spec", "global")["dailyUsd"] = budgetDaily
	sub(bp, "spec", "providers", "anthropic")["monthlyUsd"] = anthropicMonth
	pm := sub(bp, "spec", "providers")
	pm["acme-eu"] = map[string]any{"monthlyUsd": "40.00"}
	i.writePolicy("budgets.yaml", bp)
	if withKey {
		i.writeEnv(map[string]string{"DEEPSEEK_API_KEY": "fixture-secret-acme-key-0007"}, nil)
	}
}

func TestPreflight_PF19ProbesNonBuiltinProvider_50AC15(t *testing.T) {
	i := newInst(t)
	customProvider(i, true)
	i.exec(nil, i.preflightArgs(i.goodConfirms()...)...)
	found := false
	for _, p := range i.probes {
		if p.Provider == "acme-eu" && p.Key == "fixture-secret-acme-key-0007" {
			found = true
		}
	}
	if !found {
		t.Errorf("probes = %+v, want a probe for the enabled production provider acme-eu with the key named by its credential.secretKey", i.probes)
	}
}

// PF-29: an attestation is required for every enabled production provider, also one whose key is absent.
func TestPreflight_PF29RequiresAttestationWithoutKey_50AC36(t *testing.T) {
	i := newInst(t)
	// config needs some model key; deepseek is disabled in the fixture policy, so anthropic is enabled with no key
	i.writeEnv(map[string]string{"DEEPSEEK_API_KEY": "fixture-secret-deepseek-key-0007"}, []string{"ANTHROPIC_API_KEY"})
	code, out := i.exec(nil, i.preflightArgs("--confirm-budget", budgetMonthly)...)
	if code != 5 {
		t.Fatalf("exit %d, want 5 (anthropic is enabled and needs a backstop even without a key):\n%s", code, out)
	}
	matches(t, out, `(?m)^PF-29 BACKSTOP_MISSING \(fail\)`)
	contains(t, out, "anthropic")
}

func TestPreflight_PF29CoversNonBuiltinProvider_50AC36(t *testing.T) {
	i := newInst(t)
	customProvider(i, true)
	// the fixture file attests anthropic only: acme-eu is enabled and production, so it is missing
	code, out := i.exec(nil, i.preflightArgs(i.goodConfirms()...)...)
	if code != 5 {
		t.Fatalf("exit %d, want 5:\n%s", code, out)
	}
	matches(t, out, `(?m)^PF-29 BACKSTOP_MISSING \(fail\)`)
	contains(t, out, "acme-eu")
}

// --- finding 4 and 5: parse level

func TestParse_PipelineFlagsBeforeCommand_Plan50S7_1(t *testing.T) {
	for flag, val := range map[string]string{"--from-phase": "seed", "--retry-phase": "provision"} {
		for _, args := range [][]string{{flag, val}, {flag + "=" + val}, {"--non-interactive", flag, val}, {flag, val, "--non-interactive"}} {
			inv, err := cli.Parse(args)
			if err != nil {
				t.Errorf("%v: %v", args, err)
				continue
			}
			if !reflect.DeepEqual(inv.Command, []string{"run"}) || inv.Flags[strings.TrimPrefix(flag, "--")] != val || len(inv.Args) != 0 {
				t.Errorf("%v: got command %v flags %v args %v, want run with %s=%s", args, inv.Command, inv.Flags, inv.Args, flag, val)
			}
		}
	}
}

// With no prior state the CLI must not call the command unknown; this slice has no engine, so it is not built yet.
func TestRun_PipelineFlagsBeforeCommandIsNotUnknown_Plan50S7_1(t *testing.T) {
	i := newInst(t)
	for _, args := range [][]string{{"--from-phase", "seed"}, {"--retry-phase", "provision"}} {
		_, out := i.exec(nil, args...)
		if strings.Contains(out, "unknown command") || strings.Contains(out, "unknown flag") {
			t.Errorf("%v: rejected at parse level:\n%s", args, out)
		}
	}
}

func TestParse_ValueFlagDoesNotSwallowFlag_Plan50S7_1(t *testing.T) {
	for _, args := range [][]string{
		{"--profile", "--non-interactive"},
		{"preflight", "--profile", "--non-interactive"},
		{"--env-file", "--non-interactive", "preflight"},
		{"--confirm-budget", "-v"},
		{"verify", "--only", "--non-interactive"},
		{"run", "--from-phase", "--non-interactive"},
		{"--from-phase", "--non-interactive"},
	} {
		if inv, err := cli.Parse(args); err == nil {
			t.Errorf("Parse(%v) = %+v, want a usage error (a value flag must not take a flag as its value)", args, inv)
		}
	}
	i := newInst(t)
	for _, args := range [][]string{{"--profile", "--non-interactive"}, {"verify", "--only", "--non-interactive"}} {
		if code, out := i.exec(nil, args...); code != 2 {
			t.Errorf("Run(%v) exit %d, want 2:\n%s", args, code, out)
		}
	}
}

// --- finding 6: PF-28 S3_PRIVATE_CIDR must contain the endpoint (plan 50 section 14.2 PF-28)

func TestPreflight_PrivateCIDRMustContainEndpoint_50AC35(t *testing.T) {
	i := newInst(t)
	i.writeEnv(map[string]string{"S3_ENDPOINT": "https://10.20.0.5:9000", "S3_PRIVATE_CIDR": "10.99.0.0/24"}, nil)
	code, out := i.exec(nil, i.preflightArgs(i.goodConfirms()...)...)
	if code != 3 {
		t.Fatalf("exit %d, want 3:\n%s", code, out)
	}
	matches(t, out, `(?m)^PF-28 S3_ENDPOINT_PLACEMENT_UNSUPPORTED \(fail\)`)
}
