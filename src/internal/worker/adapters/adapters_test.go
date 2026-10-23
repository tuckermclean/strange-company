package adapters_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"strange.company/sc/internal/contracts"
	"strange.company/sc/internal/worker/adapters"
)

func validatePacket(t testing.TB, raw []byte) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	set, err := contracts.CompileSchemas(filepath.Join(root, "contracts"))
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if err := set["worker-input"].Validate(v); err != nil {
		t.Fatalf("packet is not a schema-valid worker-input: %v", err)
	}
}

func readFile(t testing.TB, parts ...string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// allowedTools returns the comma-split value of --allowedTools.
func allowedTools(t testing.TB, argv []string) []string {
	t.Helper()
	for i, a := range argv {
		if a == "--allowedTools" && i+1 < len(argv) {
			return strings.Split(argv[i+1], ",")
		}
	}
	t.Fatalf("argv has no --allowedTools: %q", argv)
	return nil
}

var auxModelVar = regexp.MustCompile(`^ANTHROPIC_(SMALL_FAST_MODEL|DEFAULT_[A-Z]+_MODEL)$`)

// 28-AC-5: Prepare output for (claude-code, role) equals the committed golden. The fixed prompt and system
// addendum texts are not specified by plan 28, so they are checked by their stated bounds (prompt <= 4 KiB with
// the INDEX.md and claim.json instruction and the role name; addendum <= 2 KiB) and compared as placeholders.
func TestPrepare_ClaudeCodeGoldens_28AC5(t *testing.T) {
	for _, role := range []string{"tester", "reviewer"} {
		t.Run(role, func(t *testing.T) {
			dir := filepath.Join("testdata", "claude-code")
			packet := readFile(t, dir, "packet-"+role+".json")
			validatePacket(t, packet)

			got, err := adapters.Prepare(packet)
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			argv := append([]string(nil), got.Argv...)
			for i := 0; i+1 < len(argv); i++ {
				switch argv[i] {
				case "-p":
					p := argv[i+1]
					if len(p) == 0 || len(p) > 4096 || !strings.Contains(p, "/workspace/context/INDEX.md") ||
						!strings.Contains(p, "/workspace/out/claim.json") || !strings.Contains(p, role) {
						t.Errorf("prompt violates plan 27 section 7.2 (len %d): %q", len(p), p)
					}
					argv[i+1] = "<PROMPT>"
				case "--append-system-prompt":
					if n := len(argv[i+1]); n == 0 || n > 2048 {
						t.Errorf("system addendum has %d bytes, want 1..2048", n)
					}
					argv[i+1] = "<SYSTEM_ADDENDUM>"
				}
			}

			var want adapters.Prepared
			if err := json.Unmarshal(readFile(t, dir, "prepare-"+role+".golden.json"), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(argv, want.Argv) {
				t.Errorf("argv differs from the golden\n got: %q\nwant: %q", argv, want.Argv)
			}
			for k, v := range want.Env {
				if got.Env[k] != v {
					t.Errorf("env %s = %q, want %q", k, got.Env[k], v)
				}
			}
			for k, v := range got.Env {
				if _, ok := want.Env[k]; ok {
					continue
				}
				if !auxModelVar.MatchString(k) || v != "claude-sonnet-5" {
					t.Errorf("unexpected env %s=%q beside the golden (only per-class model variables set to the model are allowed)", k, v)
				}
				if strings.Contains(v, "@file:") {
					t.Errorf("env %s carries a token reference outside gateway-token", k)
				}
			}
			for k, v := range got.Env {
				if strings.Contains(v, "@file:") && !(k == "ANTHROPIC_API_KEY" && v == "@file:gateway-token") {
					t.Errorf("token reference %s=%q; only ANTHROPIC_API_KEY=@file:gateway-token is allowed (plan 28 section 8.1)", k, v)
				}
			}
			if !reflect.DeepEqual(got.Files, want.Files) && !(len(got.Files) == 0 && len(want.Files) == 0) {
				t.Errorf("files = %v, want %v", got.Files, want.Files)
			}

			tools := allowedTools(t, got.Argv)
			hasEdit := false
			for _, tl := range tools {
				if tl == "Edit" {
					hasEdit = true
				}
			}
			if role == "reviewer" && hasEdit {
				t.Errorf("reviewer --allowedTools contains Edit: %q", tools)
			}
			if role == "tester" && !hasEdit {
				t.Errorf("tester --allowedTools lacks Edit: %q", tools)
			}
		})
	}
}

func withFakeScenario(t testing.TB, adapter, scenario string) []byte {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(readFile(t, "testdata", "claude-code", "packet-tester.json"), &p); err != nil {
		t.Fatal(err)
	}
	h := p["harness"].(map[string]any)
	h["fake_scenario"] = scenario
	if adapter != "claude-code" {
		p["harness"] = map[string]any{"adapter": adapter, "cli_version": "0.1.0", "fake_scenario": scenario}
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	validatePacket(t, b)
	return b
}

// 28-AC-6: fake knobs are inert for real adapters. A claude-code packet that names a fake scenario derives no
// knob and still runs the pre-checks; the same scenario on adapter fake does derive the knob (positive control).
func TestFakeKnobs_InertForRealAdapters_28AC6(t *testing.T) {
	for _, scenario := range []string{"test-tamper", "push-no-result", "malformed-result", "multi-commit", "ignore-cancel", "push-other-branch", "not-descendant", "wrong-commit-claim"} {
		t.Run("claude-code/"+scenario, func(t *testing.T) {
			packet := withFakeScenario(t, "claude-code", scenario)
			knobs, err := adapters.DeriveFakeKnobs(packet)
			if err != nil {
				t.Fatalf("DeriveFakeKnobs: %v", err)
			}
			if knobs != (adapters.FakeKnobs{}) {
				t.Errorf("knobs = %+v for adapter claude-code, want all off", knobs)
			}
			if knobs.SkipPrechecks {
				t.Errorf("skip_prechecks is true for adapter claude-code")
			}
			run, err := adapters.PrechecksEnabled(packet)
			if err != nil {
				t.Fatalf("PrechecksEnabled: %v", err)
			}
			if !run {
				t.Errorf("pre-checks are disabled for adapter claude-code with fake_scenario %s", scenario)
			}
		})
	}

	t.Run("fake/test-tamper_positive_control", func(t *testing.T) {
		packet := withFakeScenario(t, "fake", "test-tamper")
		knobs, err := adapters.DeriveFakeKnobs(packet)
		if err != nil {
			t.Fatalf("DeriveFakeKnobs: %v", err)
		}
		if !knobs.SkipPrechecks {
			t.Errorf("adapter fake, scenario test-tamper: skip_prechecks = false, want true (plan 28 section 14.4)")
		}
		run, err := adapters.PrechecksEnabled(packet)
		if err != nil {
			t.Fatalf("PrechecksEnabled: %v", err)
		}
		if run {
			t.Errorf("adapter fake, scenario test-tamper: pre-checks still enabled")
		}
	})
	t.Run("fake/push-no-result_positive_control", func(t *testing.T) {
		knobs, err := adapters.DeriveFakeKnobs(withFakeScenario(t, "fake", "push-no-result"))
		if err != nil {
			t.Fatalf("DeriveFakeKnobs: %v", err)
		}
		if !knobs.SkipReport {
			t.Errorf("adapter fake, scenario push-no-result: skip_report = false, want true")
		}
	})
}
