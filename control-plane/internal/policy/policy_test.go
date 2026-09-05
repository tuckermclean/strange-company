package policy

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// loadTestPolicy returns a Policy exercising the shapes the tests in this
// file care about: three providers under the same "claude-code" harness
// (two of them — anthropic-api and anthropic-oauth — differing only in
// which credential they carry), one provider under a different harness
// entirely (openai-codex, so credential isolation has something real to
// isolate against), one credential-free provider (ollama), and an
// implementation ladder of haiku x3, sonnet x3, opus x1.
func loadTestPolicy(t *testing.T) *Policy {
	t.Helper()

	const providersYAML = `
providers:
  anthropic-api:
    harness: claude-code
    env:
      ANTHROPIC_API_KEY:
        secret: anthropic-credentials
        key: api-key

  anthropic-oauth:
    harness: claude-code
    env:
      CLAUDE_CODE_OAUTH_TOKEN:
        secret: anthropic-credentials
        key: oauth-token

  openai-codex:
    harness: codex
    env:
      OPENAI_API_KEY:
        secret: openai-credentials
        key: api-key

  ollama:
    harness: hermes
    baseUrl: http://ollama.internal:11434
`

	const modelsYAML = `
aliases:
  implement-cheap:
    provider: anthropic-api
    model: claude-haiku-4-5

  implement-mid:
    provider: anthropic-api
    model: claude-sonnet-5

  implement-frontier:
    provider: anthropic-api
    model: claude-opus-5

phases:
  implementation:
    - model: implement-cheap
      max_attempts: 3
    - model: implement-mid
      max_attempts: 3
    - model: implement-frontier
      max_attempts: 1
`

	p, err := Load([]byte(providersYAML), []byte(modelsYAML))
	if err != nil {
		t.Fatalf("loadTestPolicy: %v", err)
	}
	return p
}

func TestResolveWalksTheLadderExactlyAsPolicySays(t *testing.T) {
	p := loadTestPolicy(t) // haiku x3, sonnet x3, opus x1
	for _, tc := range []struct {
		attempt   int
		wantAlias string
	}{
		{1, "implement-cheap"}, {3, "implement-cheap"},
		{4, "implement-mid"}, {6, "implement-mid"},
		{7, "implement-frontier"},
	} {
		got, err := p.Resolve("implementation", tc.attempt)
		if err != nil {
			t.Fatalf("attempt %d: %v", tc.attempt, err)
		}
		if got.Alias != tc.wantAlias {
			t.Errorf("attempt %d: want %s, got %s", tc.attempt, tc.wantAlias, got.Alias)
		}
		if got.Attempt != tc.attempt {
			t.Errorf("attempt %d: Resolution.Attempt = %d, want it to echo the input", tc.attempt, got.Attempt)
		}
		if got.Phase != "implementation" {
			t.Errorf("attempt %d: Resolution.Phase = %q, want %q", tc.attempt, got.Phase, "implementation")
		}
	}
}

// Spec 12.3: no eighth implementation attempt happens automatically.
func TestTheLadderRunsOutRatherThanEscalatingForever(t *testing.T) {
	p := loadTestPolicy(t)
	if _, err := p.Resolve("implementation", 8); !errors.Is(err, ErrLadderExhausted) {
		t.Fatalf("want ErrLadderExhausted, got %v", err)
	}
}

// A phase with a single rung and max_attempts 1 (e.g. planning) is a normal,
// one-element ladder: attempt 1 resolves, attempt 2 is exhausted.
func TestASingleRungPhaseResolvesOnceThenExhausts(t *testing.T) {
	p, err := Load([]byte(`
providers:
  anthropic-api:
    harness: claude-code
    env:
      ANTHROPIC_API_KEY:
        secret: anthropic-credentials
        key: api-key
`), []byte(`
aliases:
  plan:
    provider: anthropic-api
    model: claude-opus-5

phases:
  planning:
    - model: plan
      max_attempts: 1
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	got, err := p.Resolve("planning", 1)
	if err != nil {
		t.Fatalf("Resolve(planning, 1): %v", err)
	}
	if got.Alias != "plan" {
		t.Errorf("Resolve(planning, 1).Alias = %q, want %q", got.Alias, "plan")
	}

	if _, err := p.Resolve("planning", 2); !errors.Is(err, ErrLadderExhausted) {
		t.Fatalf("Resolve(planning, 2): want ErrLadderExhausted, got %v", err)
	}
}

// Spec 29: a run must not receive credentials it does not need.
func TestResolutionCarriesOnlyItsOwnProvidersCredentials(t *testing.T) {
	p := loadTestPolicy(t) // anthropic + openai providers both defined
	got, err := p.Resolve("implementation", 1)
	if err != nil {
		t.Fatal(err)
	}
	for name := range got.Env {
		if strings.Contains(strings.ToUpper(name), "OPENAI") {
			t.Fatalf("an Anthropic run was handed %s", name)
		}
	}
	if len(got.Env) != 1 {
		t.Fatalf("want exactly the one env var its own provider declares, got %v", got.Env)
	}
	if _, ok := got.Env["ANTHROPIC_API_KEY"]; !ok {
		t.Fatalf("want ANTHROPIC_API_KEY, got %v", got.Env)
	}
}

func TestResolveRejectsAnUnknownPhase(t *testing.T) {
	p := loadTestPolicy(t)
	if _, err := p.Resolve("no-such-phase", 1); !errors.Is(err, ErrUnknownPhase) {
		t.Fatalf("want ErrUnknownPhase, got %v", err)
	}
}

func TestValidateRejectsAnAliasPointingAtAnUnknownProvider(t *testing.T) {
	p := &Policy{
		Providers: map[string]Provider{
			"anthropic-api": {Harness: "claude-code"},
		},
		Aliases: map[string]Alias{
			"implement-cheap": {Provider: "does-not-exist", Model: "claude-haiku-4-5"},
		},
		Phases: map[string][]Rung{},
	}

	err := p.Validate()
	if !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("want ErrUnknownProvider, got %v", err)
	}
	if !strings.Contains(err.Error(), "implement-cheap") || !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("error must name the offending alias and provider, got: %v", err)
	}
}

func TestValidateRejectsAPhaseReferencingAnUnknownAlias(t *testing.T) {
	p := &Policy{
		Providers: map[string]Provider{},
		Aliases:   map[string]Alias{},
		Phases: map[string][]Rung{
			"foreman": {{Alias: "foreman-cheap", MaxAttempts: 1}},
		},
	}

	err := p.Validate()
	if !errors.Is(err, ErrUnknownAlias) {
		t.Fatalf("want ErrUnknownAlias, got %v", err)
	}
	if !strings.Contains(err.Error(), "foreman") || !strings.Contains(err.Error(), "foreman-cheap") {
		t.Fatalf("error must name the offending phase and alias, got: %v", err)
	}
}

func TestValidateRejectsARungWithMaxAttemptsBelowOne(t *testing.T) {
	p := &Policy{
		Providers: map[string]Provider{
			"anthropic-api": {Harness: "claude-code"},
		},
		Aliases: map[string]Alias{
			"plan": {Provider: "anthropic-api", Model: "claude-opus-5"},
		},
		Phases: map[string][]Rung{
			"planning": {{Alias: "plan", MaxAttempts: 0}},
		},
	}

	err := p.Validate()
	if err == nil {
		t.Fatal("want an error for max_attempts < 1, got nil")
	}
	if !strings.Contains(err.Error(), "planning") || !strings.Contains(err.Error(), "max_attempts") {
		t.Fatalf("error must name the offending phase and field, got: %v", err)
	}
}

func TestValidateRejectsAProviderWithAnEmptyHarness(t *testing.T) {
	p := &Policy{
		Providers: map[string]Provider{
			"broken": {Harness: ""},
		},
	}

	err := p.Validate()
	if err == nil {
		t.Fatal("want an error for an empty harness, got nil")
	}
	if !strings.Contains(err.Error(), "broken") || !strings.Contains(err.Error(), "harness") {
		t.Fatalf("error must name the offending provider, got: %v", err)
	}
}

func TestValidateRejectsAnEnvEntryWithAnEmptySecretOrKey(t *testing.T) {
	p := &Policy{
		Providers: map[string]Provider{
			"broken": {
				Harness: "claude-code",
				Env: map[string]CredentialRef{
					"ANTHROPIC_API_KEY": {Secret: "", Key: ""},
				},
			},
		},
	}

	err := p.Validate()
	if err == nil {
		t.Fatal("want an error for an empty secret/key, got nil")
	}
	if !strings.Contains(err.Error(), "broken") || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Fatalf("error must name the offending provider and env var, got: %v", err)
	}
}

// Spec 29 / the reason a "provider" and not a "harness" is the unit of
// configuration: claude-code reads ANTHROPIC_API_KEY for one provider and
// CLAUDE_CODE_OAUTH_TOKEN for another. The credential is not derivable from
// the harness.
func TestTheSameHarnessCanCarryDifferentCredentialKinds(t *testing.T) {
	p := loadTestPolicy(t)

	apiKeyProvider, ok := p.Providers["anthropic-api"]
	if !ok {
		t.Fatal("expected anthropic-api provider in test policy")
	}
	oauthProvider, ok := p.Providers["anthropic-oauth"]
	if !ok {
		t.Fatal("expected anthropic-oauth provider in test policy")
	}

	if apiKeyProvider.Harness != oauthProvider.Harness {
		t.Fatalf("expected both providers to share a harness, got %q and %q", apiKeyProvider.Harness, oauthProvider.Harness)
	}

	if _, ok := apiKeyProvider.Env["ANTHROPIC_API_KEY"]; !ok {
		t.Fatalf("anthropic-api provider should carry ANTHROPIC_API_KEY, got %v", apiKeyProvider.Env)
	}
	if _, ok := apiKeyProvider.Env["CLAUDE_CODE_OAUTH_TOKEN"]; ok {
		t.Fatalf("anthropic-api provider should not carry CLAUDE_CODE_OAUTH_TOKEN, got %v", apiKeyProvider.Env)
	}

	if _, ok := oauthProvider.Env["CLAUDE_CODE_OAUTH_TOKEN"]; !ok {
		t.Fatalf("anthropic-oauth provider should carry CLAUDE_CODE_OAUTH_TOKEN, got %v", oauthProvider.Env)
	}
	if _, ok := oauthProvider.Env["ANTHROPIC_API_KEY"]; ok {
		t.Fatalf("anthropic-oauth provider should not carry ANTHROPIC_API_KEY, got %v", oauthProvider.Env)
	}
}

// e.g. a local Ollama with only a baseUrl.
func TestAProviderNeedingNoCredentialsIsValid(t *testing.T) {
	p := loadTestPolicy(t)

	ollama, ok := p.Providers["ollama"]
	if !ok {
		t.Fatal("expected ollama provider in test policy")
	}
	if ollama.BaseURL == "" {
		t.Fatal("expected ollama to declare a baseUrl")
	}
	if len(ollama.Env) != 0 {
		t.Fatalf("expected ollama to need no credentials, got %v", ollama.Env)
	}

	if err := p.Validate(); err != nil {
		t.Fatalf("a credential-free provider must not fail validation: %v", err)
	}
}

// This is the regression test that keeps the shipped policy/*.yaml honest:
// it loads the real files the chart mounts at /policy, not a synthetic
// fixture, and checks they satisfy the aliases and phases the rest of the
// system assumes exist.
func TestLoadDirLoadsTheShippedDefaultPolicy(t *testing.T) {
	p, err := Defaults()
	if err != nil {
		t.Fatalf("Defaults(): %v", err)
	}

	for _, alias := range []string{
		"foreman-cheap", "spec-frontier", "plan", "tests",
		"implement-cheap", "implement-mid", "implement-frontier", "review",
	} {
		if _, ok := p.Aliases[alias]; !ok {
			t.Errorf("expected alias %q to be defined in policy/models.yaml", alias)
		}
	}

	for _, phase := range []string{
		"foreman", "specification", "planning", "tests", "implementation", "review",
	} {
		if _, ok := p.Phases[phase]; !ok {
			t.Errorf("expected phase %q to be defined in policy/models.yaml", phase)
		}
	}

	// Spec 12: three implementation rungs, 3/3/1, exhausting at attempt 8.
	if _, err := p.Resolve("implementation", 8); !errors.Is(err, ErrLadderExhausted) {
		t.Errorf("expected the shipped implementation ladder to exhaust at attempt 8, got %v", err)
	}
}

// The published DeepSeek schedule, verbatim from
// https://api-docs.deepseek.com/quick_start/pricing/ :
// peak is 01:00-04:00 and 06:00-10:00 UTC Mon-Fri, off-peak is half.
func deepseekV4Flash() *Pricing {
	return &Pricing{
		InputPerMTok: 0.44, CachedInputPerMTok: 0.014, OutputPerMTok: 1.32,
		OffPeak: &Pricing{
			InputPerMTok: 0.22, CachedInputPerMTok: 0.007, OutputPerMTok: 0.66,
		},
		PeakUTC: []PeakWindow{
			{Days: []string{"Mon", "Tue", "Wed", "Thu", "Fri"}, From: "01:00", To: "04:00"},
			{Days: []string{"Mon", "Tue", "Wed", "Thu", "Fri"}, From: "06:00", To: "10:00"},
		},
	}
}

// Peak is 35 hours of 168. A flat rate card taken from the published table
// over-charges roughly four runs in five by a factor of two, which makes a
// budget fire at half the spend the operator authorised.
func TestTheSameRunCostsHalfAsMuchOffPeak(t *testing.T) {
	p := deepseekV4Flash()

	// Wednesday 02:00 UTC -- inside the first peak window.
	peak := time.Date(2026, 9, 2, 2, 0, 0, 0, time.UTC)
	// Wednesday 14:00 UTC -- outside both.
	off := time.Date(2026, 9, 2, 14, 0, 0, 0, time.UTC)

	atPeak := p.CostUSD(1_000_000, 1_000_000, 1_000_000, 0, peak, peak.Add(time.Minute))
	atOff := p.CostUSD(1_000_000, 1_000_000, 1_000_000, 0, off, off.Add(time.Minute))

	if want := 0.44 + 1.32 + 0.014; !closeEnough(atPeak, want) {
		t.Errorf("peak = %v, want %v", atPeak, want)
	}
	if want := 0.22 + 0.66 + 0.007; !closeEnough(atOff, want) {
		t.Errorf("off-peak = %v, want %v", atOff, want)
	}
}

// Saturday is off-peak all day: the windows are Monday to Friday.
func TestTheWeekendIsEntirelyOffPeak(t *testing.T) {
	p := deepseekV4Flash()
	sat := time.Date(2026, 9, 5, 2, 0, 0, 0, time.UTC)
	if sat.Weekday() != time.Saturday {
		t.Fatalf("fixture is a %s", sat.Weekday())
	}
	got := p.CostUSD(1_000_000, 0, 0, 0, sat, sat.Add(time.Minute))
	if !closeEnough(got, 0.22) {
		t.Errorf("Saturday 02:00 = %v, want the off-peak rate 0.22", got)
	}
}

// The gap between the two windows is off-peak, which a naive "between the
// first From and the last To" reading would get wrong.
func TestTheGapBetweenTwoPeakWindowsIsOffPeak(t *testing.T) {
	p := deepseekV4Flash()
	gap := time.Date(2026, 9, 2, 5, 0, 0, 0, time.UTC) // 05:00, between 04:00 and 06:00
	got := p.CostUSD(1_000_000, 0, 0, 0, gap, gap.Add(time.Minute))
	if !closeEnough(got, 0.22) {
		t.Errorf("05:00 UTC = %v, want off-peak", got)
	}
}

// A run touching peak at any point is priced at peak. Over-charging the
// off-peak remainder of one short run is the safe direction for a budget.
func TestARunSpanningABoundaryIsPricedAtPeak(t *testing.T) {
	p := deepseekV4Flash()
	start := time.Date(2026, 9, 2, 3, 58, 0, 0, time.UTC) // peak until 04:00
	end := start.Add(5 * time.Minute)                     // ends off-peak
	got := p.CostUSD(1_000_000, 0, 0, 0, start, end)
	if !closeEnough(got, 0.44) {
		t.Errorf("boundary-spanning run = %v, want the peak rate 0.44", got)
	}
}

// A rate card with no schedule keeps behaving exactly as it did.
func TestARateCardWithNoScheduleIsFlat(t *testing.T) {
	p := &Pricing{InputPerMTok: 1.0}
	a := p.CostUSD(1_000_000, 0, 0, 0, time.Date(2026, 9, 2, 2, 0, 0, 0, time.UTC), time.Date(2026, 9, 2, 2, 1, 0, 0, time.UTC))
	b := p.CostUSD(1_000_000, 0, 0, 0, time.Date(2026, 9, 2, 14, 0, 0, 0, time.UTC), time.Date(2026, 9, 2, 14, 1, 0, 0, time.UTC))
	if !closeEnough(a, 1.0) || !closeEnough(b, 1.0) {
		t.Errorf("flat card priced by time of day: %v vs %v", a, b)
	}
}

// covers() cannot read a malformed window and returns false, which would
// quietly bill every run off-peak and halve the ledger. Refuse at load.
func TestAMalformedScheduleIsRefusedAtLoad(t *testing.T) {
	for name, p := range map[string]*Pricing{
		"bad day": {InputPerMTok: 1, OffPeak: &Pricing{InputPerMTok: 1},
			PeakUTC: []PeakWindow{{Days: []string{"Munday"}, From: "01:00", To: "04:00"}}},
		"bad time": {InputPerMTok: 1, OffPeak: &Pricing{InputPerMTok: 1},
			PeakUTC: []PeakWindow{{From: "1am", To: "04:00"}}},
		"backwards window": {InputPerMTok: 1, OffPeak: &Pricing{InputPerMTok: 1},
			PeakUTC: []PeakWindow{{From: "22:00", To: "02:00"}}},
		"schedule with no off-peak rates": {InputPerMTok: 1,
			PeakUTC: []PeakWindow{{From: "01:00", To: "04:00"}}},
		"off-peak rates with no schedule": {InputPerMTok: 1, OffPeak: &Pricing{InputPerMTok: 1}},
		"nested schedule": {InputPerMTok: 1,
			OffPeak: &Pricing{InputPerMTok: 1, PeakUTC: []PeakWindow{{From: "01:00", To: "02:00"}}},
			PeakUTC: []PeakWindow{{From: "01:00", To: "04:00"}}},
	} {
		if got := p.problems("an-alias"); len(got) == 0 {
			t.Errorf("%s: accepted a rate card that cannot price correctly", name)
		}
	}
}

func TestTheDeepSeekScheduleItselfIsAccepted(t *testing.T) {
	if got := deepseekV4Flash().problems("implement-cheap"); len(got) != 0 {
		t.Errorf("the published DeepSeek schedule was refused: %v", got)
	}
}

func closeEnough(a, b float64) bool {
	d := a - b
	return d < 1e-9 && d > -1e-9
}
