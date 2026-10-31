package cli_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"strange.company/sc/internal/bootstrap/cli"
)

func contains(t testing.TB, out string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
}

func matches(t testing.TB, out, re string) {
	t.Helper()
	if !regexp.MustCompile(re).MatchString(out) {
		t.Errorf("output does not match %s:\n%s", re, out)
	}
}

// CD-70(e): a parsed command that is not built yet exits 2 with COMMAND_NOT_IMPLEMENTED; an unknown one does not.
func TestRun_NotImplementedVersusUnknown_CD70e(t *testing.T) {
	i := newInst(t)
	for _, args := range [][]string{{"edge-bypass"}, {"supply-chain", "init"}, {"--non-interactive", "kubeconfig", "refresh", "--via-ssh"}} {
		code, out := i.exec(nil, args...)
		if code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
		contains(t, out, "COMMAND_NOT_IMPLEMENTED")
	}
	code, out := i.exec(nil, "frobnicate")
	if code != 2 || strings.Contains(out, "COMMAND_NOT_IMPLEMENTED") || !strings.Contains(out, "frobnicate") {
		t.Errorf("unknown command: exit %d, output %q; want 2 naming the command and not COMMAND_NOT_IMPLEMENTED", code, out)
	}
	for _, args := range [][]string{{"preflight", "--nope"}, {"--log-format", "xml", "preflight"}, {"--profile"}} {
		if code, out := i.exec(nil, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2 (%s)", args, code, out)
		}
	}
}

// The toolbox commands keep working through the new tree (F01/F03 behaviour).
func TestRun_VersionStillWorks(t *testing.T) {
	i := newInst(t)
	code, out := i.exec(nil, "version")
	if code != 0 || !strings.Contains(out, "sc-bootstrap") {
		t.Fatalf("version: exit %d, output %q", code, out)
	}
}

// 50-AC-2: an unknown key exits 2 and PF-02 names the key and suggests the nearest known one.
func TestPreflight_UnknownKeyNamesKeyAndSuggests_50AC2(t *testing.T) {
	i := newInst(t)
	fixture := filepath.Join(repoRoot(t), "test", "fixtures", "bootstrap", "invalid-unknown-key.env")
	code, out := i.exec(nil, "--env-file", fixture, "preflight")
	if code != 2 {
		t.Fatalf("exit %d, want 2:\n%s", code, out)
	}
	contains(t, out, "PF-02", "MAX_MONTLY_COST", "MAX_MONTHLY_COST")
	if !strings.Contains(strings.ToLower(out), "did you mean") {
		t.Errorf("PF-02 must give a 'did you mean' suggestion:\n%s", out)
	}
	if strings.Contains(out, "fixture-secret") {
		t.Errorf("a secret value reached the output:\n%s", out)
	}
	// With nothing close, the key is still named and the run still exits 2.
	i.writeEnv(map[string]string{"ZZZ_QQQ_UNRELATED": "1"}, nil)
	code, out = i.exec(nil, i.preflightArgs()...)
	if code != 2 {
		t.Fatalf("unrelated unknown key: exit %d, want 2:\n%s", code, out)
	}
	contains(t, out, "PF-02", "ZZZ_QQQ_UNRELATED")
}

// 50-AC-2 and plan 50 section 14.2: the wrapper hands the process environment to the CLI; a fully valid install
// passes and lists the checks of this slice in the section 14.2 line format.
func TestPreflight_ValidInstallPasses(t *testing.T) {
	i := newInst(t)
	code, out := i.exec(nil, i.preflightArgs(i.goodConfirms()...)...)
	if code != 0 {
		t.Fatalf("exit %d, want 0:\n%s", code, out)
	}
	for _, id := range []string{"PF-01", "PF-02", "PF-06", "PF-19", "PF-26", "PF-28", "PF-29", "PF-30"} {
		matches(t, out, `(?m)^`+id+` pass\b`)
	}
	if strings.Contains(out, "fixture-secret") {
		t.Errorf("a secret value reached the output:\n%s", out)
	}
}

// S1 carry-over: normalization that would emit a document violating bootstrap-config.schema.yaml fails PF-02, exit 2.
func TestPreflight_SchemaInvalidNormalizationExits2_S1Carryover(t *testing.T) {
	for name, set := range map[string]map[string]string{
		"s3 credential instance 1": {"S3_CRED_1_ACCESS_KEY_ID": "fixture-secret-a-0006", "S3_CRED_1_SECRET_ACCESS_KEY": "fixture-secret-b-0006"},
		"endpoint with a space":    {"S3_ENDPOINT": "https://a b"},
		"upper-case machine class": {"MACHINE_CLASS": "UPPER"},
	} {
		t.Run(name, func(t *testing.T) {
			i := newInst(t)
			i.writeEnv(set, nil)
			code, out := i.exec(nil, i.preflightArgs(i.goodConfirms()...)...)
			if code != 2 {
				t.Fatalf("exit %d, want 2:\n%s", code, out)
			}
			contains(t, out, "PF-02")
			if strings.Contains(out, "fixture-secret") {
				t.Errorf("a secret value reached the output:\n%s", out)
			}
		})
	}
}

// 50-AC-7 (CLI leg): SC_TEST_* in the process environment with a paid provider is refused, exit 2.
func TestPreflight_TestModeRefusedWithPaidProvider_50AC7(t *testing.T) {
	root := repoRoot(t)
	ovh := filepath.Join(root, "test", "fixtures", "bootstrap", "valid", "ovh-tunnel.env")
	for name, extra := range map[string]map[string]string{
		"crash-at":   {"SC_TEST_MODE": "1", "SC_TEST_CRASH_AT": "plan.storage-apply:after"},
		"stop-after": {"SC_TEST_MODE": "1", "SC_TEST_STOP_AFTER": "configure"},
	} {
		t.Run(name, func(t *testing.T) {
			i := newInst(t)
			code, out := i.exec(extra, "--env-file", ovh, "--non-interactive", "preflight")
			if code != 2 {
				t.Fatalf("exit %d, want 2:\n%s", code, out)
			}
			contains(t, out, "TEST_MODE_REFUSED_PAID")
		})
	}
	// The same variables stay allowed with a non-paid provider.
	i := newInst(t)
	code, out := i.exec(map[string]string{"SC_TEST_MODE": "1", "SC_TEST_STOP_AFTER": "configure"}, i.preflightArgs(i.goodConfirms()...)...)
	if code != 0 {
		t.Fatalf("existing provider with SC_TEST_MODE: exit %d, want 0:\n%s", code, out)
	}
}

// 50-AC-15, PF-19: a key carrying a forbidden prefix from policy/model-routing.yaml exits 3; no live call is made
// with it and the value is never printed.
func TestPreflight_ForbiddenKeyPrefix_50AC15(t *testing.T) {
	i := newInst(t)
	bad := fixtureKeyPrefix() + "01-" + strings.Repeat("x", 24)
	i.writeEnv(map[string]string{"ANTHROPIC_API_KEY": bad}, nil)
	code, out := i.exec(nil, i.preflightArgs(i.goodConfirms()...)...)
	if code != 3 {
		t.Fatalf("exit %d, want 3:\n%s", code, out)
	}
	matches(t, out, `(?m)^PF-19 MODEL_KEY_FORBIDDEN_PREFIX \(fail\)`)
	if strings.Contains(out, bad) {
		t.Errorf("the key value reached the output:\n%s", out)
	}
	for _, p := range i.probes {
		if p.Key == bad {
			t.Errorf("a live call was made with the forbidden key (%s)", p.Provider)
		}
	}
}

// 50-AC-15, PF-19: a valid key is probed once for its provider; a failing probe fails the check.
func TestPreflight_ModelKeyProbe_50AC15(t *testing.T) {
	i := newInst(t)
	code, out := i.exec(nil, i.preflightArgs(i.goodConfirms()...)...)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if len(i.probes) != 1 || i.probes[0].Provider != "anthropic" || i.probes[0].Key != "fixture-secret-anthropic-key-0006" {
		t.Errorf("probes = %+v, want one call for anthropic with the configured key", i.probes)
	}
	i = newInst(t)
	i.probeErr = os.ErrDeadlineExceeded
	code, out = i.exec(nil, i.preflightArgs(i.goodConfirms()...)...)
	if code != 3 {
		t.Fatalf("failing probe: exit %d, want 3:\n%s", code, out)
	}
	matches(t, out, `(?m)^PF-19 [A-Z_]+ \(fail\)`)
}

// D4: an absent policy file fails closed with a named code, exit 3.
func TestPreflight_AbsentPolicyFailsClosed_D4(t *testing.T) {
	for file, pf := range map[string]string{"model-routing.yaml": "PF-19", "budgets.yaml": "PF-26"} {
		t.Run(file, func(t *testing.T) {
			i := newInst(t)
			if err := os.Remove(filepath.Join(i.repo, "policy", file)); err != nil {
				t.Fatal(err)
			}
			code, out := i.exec(nil, i.preflightArgs(i.goodConfirms()...)...)
			if code != 3 {
				t.Fatalf("exit %d, want 3:\n%s", code, out)
			}
			matches(t, out, `(?m)^`+pf+` [A-Z_]+ \(fail\)`)
		})
	}
}

// 50-AC-15, PF-26: with a valid install and --non-interactive but no budget confirmation, exit 5.
func TestPreflight_BudgetConfirmationMissingExits5_50AC15(t *testing.T) {
	i := newInst(t)
	bf := i.backstopFile("backstop-ok.yaml", 125, true)
	code, out := i.exec(nil, i.preflightArgs("--confirm-backstops", bf)...)
	if code != 5 {
		t.Fatalf("exit %d, want 5:\n%s", code, out)
	}
	contains(t, out, "PF-26", budgetMonthly, budgetDaily) // the budget is printed next to the confirmation request
	if len(i.recs) != 0 && i.recs[0].Kind == "model-budget" {
		t.Errorf("no model-budget confirmation may be recorded: %+v", i.recs)
	}
}

// 50-AC-15, PF-26: --confirm-budget or SC_CONFIRM_BUDGET equal to global.monthlyUsd passes and records model-budget
// (method flag, once per budget-file digest); a different amount is a mismatch, exit 5.
func TestPreflight_BudgetConfirmation_50AC15(t *testing.T) {
	i := newInst(t)
	bf := i.backstopFile("backstop-ok.yaml", 125, true)
	want := sha256File(t, filepath.Join(i.repo, "policy", "budgets.yaml"))
	for name, c := range map[string]struct {
		args []string
		env  map[string]string
	}{
		"flag": {args: []string{"--confirm-budget", budgetMonthly}},
		"env":  {env: map[string]string{"SC_CONFIRM_BUDGET": budgetMonthly}},
	} {
		i.recs = nil
		code, out := i.exec(c.env, i.preflightArgs(append(c.args, "--confirm-backstops", bf)...)...)
		if code != 0 {
			t.Fatalf("%s: exit %d, want 0:\n%s", name, code, out)
		}
		var rec *cli.Confirmation
		for n := range i.recs {
			if i.recs[n].Kind == "model-budget" {
				rec = &i.recs[n]
			}
		}
		if rec == nil {
			t.Fatalf("%s: no model-budget confirmation recorded: %+v", name, i.recs)
		}
		if rec.Method != "flag" || rec.SubjectDigest != want {
			t.Errorf("%s: model-budget record %+v, want method flag and digest %s", name, *rec, want)
		}
	}
	i.recs = nil
	code, out := i.exec(nil, i.preflightArgs("--confirm-budget", "100", "--confirm-backstops", bf)...)
	if code != 5 {
		t.Fatalf("mismatched budget: exit %d, want 5:\n%s", code, out)
	}
	for _, r := range i.recs {
		if r.Kind == "model-budget" {
			t.Errorf("a mismatched budget must not be recorded: %+v", r)
		}
	}
}

// 50-AC-36, PF-29: an enabled production provider (monthlyUsd 100) with no attestation exits 5.
func TestPreflight_BackstopMissingExits5_50AC36(t *testing.T) {
	i := newInst(t)
	code, out := i.exec(nil, i.preflightArgs("--confirm-budget", budgetMonthly)...)
	if code != 5 {
		t.Fatalf("exit %d, want 5:\n%s", code, out)
	}
	contains(t, out, "PF-29", "anthropic")
	if _, err := os.Stat(i.backstopRecord()); err == nil {
		t.Errorf("provider-backstops.yaml must not exist without an attestation")
	}
}

func (i *inst) backstopRecord() string {
	return filepath.Join(i.repo, "gitops", "clusters", "lab-one", "config", "provider-backstops.yaml")
}

// 50-AC-36, PF-29: an attestation within 1.25 x monthlyUsd (exactly 125) passes and is recorded in
// provider-backstops.yaml with the console-MFA attestation and as a provider-backstop confirmation. Both the flag and
// SC_CONFIRM_BACKSTOPS carry it.
func TestPreflight_BackstopAttestationRecorded_50AC36(t *testing.T) {
	for name, viaEnv := range map[string]bool{"flag": false, "env": true} {
		t.Run(name, func(t *testing.T) {
			i := newInst(t)
			bf := i.backstopFile("b.yaml", 125, true)
			var code int
			var out string
			if viaEnv {
				code, out = i.exec(map[string]string{"SC_CONFIRM_BACKSTOPS": bf}, i.preflightArgs("--confirm-budget", budgetMonthly)...)
			} else {
				code, out = i.exec(nil, i.preflightArgs("--confirm-budget", budgetMonthly, "--confirm-backstops", bf)...)
			}
			if code != 0 {
				t.Fatalf("exit %d, want 0:\n%s", code, out)
			}
			matches(t, out, `(?m)^PF-29 pass\b`)
			b, err := os.ReadFile(i.backstopRecord())
			if err != nil {
				t.Fatalf("provider-backstops.yaml not written: %v", err)
			}
			var doc struct {
				Providers map[string]struct {
					LimitUsd          float64 `yaml:"limitUsd"`
					ConsoleMfaEnabled bool    `yaml:"consoleMfaEnabled"`
				} `yaml:"providers"`
			}
			if err := yaml.Unmarshal(b, &doc); err != nil {
				t.Fatal(err)
			}
			if p := doc.Providers["anthropic"]; p.LimitUsd != 125 || !p.ConsoleMfaEnabled {
				t.Errorf("record %s: want anthropic limitUsd 125 with the MFA attestation", b)
			}
			found := false
			for _, r := range i.recs {
				if r.Kind == "provider-backstop" {
					found = true
					if r.Method != "flag" || !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(r.SubjectDigest) {
						t.Errorf("provider-backstop record %+v, want method flag and a sha256 digest", r)
					}
				}
			}
			if !found {
				t.Errorf("no provider-backstop confirmation recorded: %+v", i.recs)
			}
		})
	}
}

// 50-AC-36, PF-29: a limit above 1.25 x monthlyUsd is rejected (200, 126), a file lacking the console-MFA
// attestation is rejected (absent or false); nothing is recorded.
func TestPreflight_BackstopRejected_50AC36(t *testing.T) {
	for name, c := range map[string]struct {
		limit any
		mfa   any
	}{
		"limit 200":    {200, true},
		"limit 126":    {126, true},
		"no mfa":       {125, nil},
		"mfa disabled": {125, false},
	} {
		t.Run(name, func(t *testing.T) {
			i := newInst(t)
			bf := i.backstopFile("b.yaml", c.limit, c.mfa)
			code, out := i.exec(nil, i.preflightArgs("--confirm-budget", budgetMonthly, "--confirm-backstops", bf)...)
			if code != 5 {
				t.Fatalf("exit %d, want 5:\n%s", code, out)
			}
			contains(t, out, "PF-29")
			if _, err := os.Stat(i.backstopRecord()); err == nil {
				t.Errorf("provider-backstops.yaml must not be written for a rejected attestation")
			}
			for _, r := range i.recs {
				if r.Kind == "provider-backstop" {
					t.Errorf("a rejected attestation must not be recorded: %+v", r)
				}
			}
		})
	}
}

// 50-AC-34, PF-30: the Traefik ClusterIP is derived from SERVICE_CIDR and shown on the PF-30 line.
func TestPreflight_TraefikIPDerived_50AC34(t *testing.T) {
	for cidr, want := range map[string]string{"": "10.43.0.20", "10.96.0.0/12": "10.96.0.20"} {
		i := newInst(t)
		if cidr != "" {
			i.writeEnv(map[string]string{"SERVICE_CIDR": cidr}, nil)
		}
		code, out := i.exec(nil, i.preflightArgs(i.goodConfirms()...)...)
		if code != 0 {
			t.Fatalf("SERVICE_CIDR=%q: exit %d:\n%s", cidr, code, out)
		}
		matches(t, out, `(?m)^PF-30 pass\b.*`+regexp.QuoteMeta(want))
	}
}

// 50-AC-34, PF-30: SERVICE_CIDR=10.43.0.0/27 exits 3 with SERVICE_CIDR_TOO_SMALL; a cluster DNS IP injected equal
// to the derived address exits 3 with SERVICE_CIDR_IP_COLLISION (D3).
func TestPreflight_ServiceCIDRFailures_50AC34(t *testing.T) {
	i := newInst(t)
	i.writeEnv(map[string]string{"SERVICE_CIDR": "10.43.0.0/27"}, nil)
	code, out := i.exec(nil, i.preflightArgs(i.goodConfirms()...)...)
	if code != 3 {
		t.Fatalf("/27: exit %d, want 3:\n%s", code, out)
	}
	matches(t, out, `(?m)^PF-30 SERVICE_CIDR_TOO_SMALL \(fail\)`)

	i = newInst(t)
	i.dns = mustAddr("10.43.0.20")
	code, out = i.exec(nil, i.preflightArgs(i.goodConfirms()...)...)
	if code != 3 {
		t.Fatalf("collision: exit %d, want 3:\n%s", code, out)
	}
	matches(t, out, `(?m)^PF-30 SERVICE_CIDR_IP_COLLISION \(fail\)`)
}

// 50-AC-35, PF-28: a private S3_ENDPOINT without S3_PRIVATE_CIDR exits 3 with S3_ENDPOINT_PLACEMENT_UNSUPPORTED; with
// S3_PRIVATE_CIDR it passes; a public endpoint on 443 passes without it.
func TestPreflight_S3EndpointPlacement_50AC35(t *testing.T) {
	i := newInst(t)
	i.writeEnv(map[string]string{"S3_ENDPOINT": "https://10.20.0.5:9000"}, nil)
	code, out := i.exec(nil, i.preflightArgs(i.goodConfirms()...)...)
	if code != 3 {
		t.Fatalf("exit %d, want 3:\n%s", code, out)
	}
	matches(t, out, `(?m)^PF-28 S3_ENDPOINT_PLACEMENT_UNSUPPORTED \(fail\)`)

	i.writeEnv(map[string]string{"S3_ENDPOINT": "https://10.20.0.5:9000", "S3_PRIVATE_CIDR": "10.20.0.0/24"}, nil)
	code, out = i.exec(nil, i.preflightArgs(i.goodConfirms()...)...)
	if code != 0 {
		t.Fatalf("with S3_PRIVATE_CIDR: exit %d, want 0:\n%s", code, out)
	}
	matches(t, out, `(?m)^PF-28 pass\b`)

	i.writeEnv(map[string]string{"S3_ENDPOINT": "https://s3.internal.example.net"}, nil) // resolves to a private address
	if code, out = i.exec(nil, i.preflightArgs(i.goodConfirms()...)...); code != 3 {
		t.Fatalf("private resolution: exit %d, want 3:\n%s", code, out)
	}
	matches(t, out, `(?m)^PF-28 S3_ENDPOINT_PLACEMENT_UNSUPPORTED \(fail\)`)
}
