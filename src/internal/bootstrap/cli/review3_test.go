package cli_test

import (
	"testing"

	"strange.company/sc/internal/bootstrap/cli"
)

// F08 S3 carried review items (S2 review 2 non-blockers).

// D8: a policy provider without credential.secretKey fails closed. The key variable is never guessed from the
// provider id.
func TestPreflight_PF19ProviderWithoutSecretKeyFailsClosed_D8(t *testing.T) {
	i := newInst(t)
	mr := schemaExample(t, "model-routing.schema.yaml")
	sub(mr, "spec", "providers", "deepseek")["enabled"] = false
	delete(sub(mr, "spec", "providers", "anthropic", "credential"), "secretKey")
	i.writePolicy("model-routing.yaml", mr)
	code, out := i.exec(nil, i.preflightArgs(i.goodConfirms()...)...)
	if code != 3 {
		t.Fatalf("exit %d, want 3 (fail closed):\n%s", code, out)
	}
	matches(t, out, `(?m)^PF-19 POLICY_FILE_INVALID \(fail\)`)
	if len(i.probes) != 0 {
		t.Errorf("a model key was probed although the policy names no secretKey: %+v", i.probes)
	}
}

// The "=" form must refuse a flag-looking value just like the separate-argument form does.
func TestParse_EqualsFormRefusesFlagLookingValue_Plan50S7_1(t *testing.T) {
	for _, args := range [][]string{
		{"--profile=-x", "preflight"},
		{"--env-file=--non-interactive", "preflight"},
		{"--confirm-budget=-v"},
		{"--log-format=--verbose"},
		{"preflight", "--profile=-x"},
		{"run", "--from-phase=-x"},
		{"--from-phase=--non-interactive"},
		{"--retry-phase=-x"},
		{"verify", "--only=-x"},
	} {
		if inv, err := cli.Parse(args); err == nil {
			t.Errorf("Parse(%v) = %+v, want a usage error (the = form must not take a flag-looking value)", args, inv)
		}
	}
	i := newInst(t)
	for _, args := range [][]string{{"--profile=-x", "preflight"}, {"--env-file=--non-interactive", "preflight"}} {
		if code, out := i.exec(nil, args...); code != 2 {
			t.Errorf("Run(%v) exit %d, want 2:\n%s", args, code, out)
		}
	}
}

// An empty phase name is a usage error, in every position and spelling.
func TestParse_EmptyPhaseNameRefused_Plan50S7_1(t *testing.T) {
	for _, args := range [][]string{
		{"--from-phase", ""},
		{"--from-phase="},
		{"run", "--from-phase", ""},
		{"run", "--from-phase="},
		{"--retry-phase", ""},
		{"--retry-phase="},
		{"run", "--retry-phase", ""},
	} {
		if inv, err := cli.Parse(args); err == nil {
			t.Errorf("Parse(%v) = %+v, want a usage error (empty phase name)", args, inv)
		}
	}
	i := newInst(t)
	if code, out := i.exec(nil, "--from-phase", ""); code != 2 {
		t.Errorf("Run(--from-phase \"\") exit %d, want 2:\n%s", code, out)
	}
}

// The same pipeline flag before and after the command is ambiguous: exit 2, whether the values agree or not.
func TestParse_PipelineFlagBeforeAndAfterCommandRefused_Plan50S7_1(t *testing.T) {
	for _, args := range [][]string{
		{"--from-phase", "seed", "run", "--from-phase", "verify"},
		{"--from-phase", "seed", "run", "--from-phase", "seed"},
		{"--from-phase=seed", "run", "--from-phase=seed"},
		{"--retry-phase", "provision", "run", "--retry-phase", "verify"},
		{"--retry-phase", "provision", "run", "--retry-phase", "provision"},
	} {
		if inv, err := cli.Parse(args); err == nil {
			t.Errorf("Parse(%v) = %+v, want a usage error (flag given before and after the command)", args, inv)
		}
	}
	// different flags on both sides, and the same flag on one side only, stay valid
	for _, args := range [][]string{
		{"--from-phase", "seed", "run", "--retry-phase", "verify"},
		{"--from-phase", "seed", "run"},
		{"run", "--from-phase", "seed"},
	} {
		if _, err := cli.Parse(args); err != nil {
			t.Errorf("Parse(%v): %v, want valid", args, err)
		}
	}
	i := newInst(t)
	if code, out := i.exec(nil, "--from-phase", "seed", "run", "--from-phase", "verify"); code != 2 {
		t.Errorf("exit %d, want 2:\n%s", code, out)
	}
}
