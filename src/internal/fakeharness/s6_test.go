package fakeharness_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"strange.company/sc/internal/clock"
)

// Slice S6a of F07: 28-AC-7 for all 51 scenarios of the harness.schema.yaml enum, and the environment / file-list dump of
// 27-AC-5 (plan 27 section 22: the fake harness dumps its environment and `find /var/run /var/sc -type f` to
// /workspace/out).

// scenarioEnum reads the fake.scenario enum of contracts/harness.schema.yaml.
func scenarioEnum(t *testing.T) []string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "contracts", "harness.schema.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var find func(v any) []string
	find = func(v any) []string {
		switch x := v.(type) {
		case map[string]any:
			if sc, ok := x["scenario"].(map[string]any); ok {
				if en, ok := sc["enum"].([]any); ok {
					var out []string
					for _, e := range en {
						out = append(out, e.(string))
					}
					return out
				}
			}
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if out := find(x[k]); out != nil {
					return out
				}
			}
		case []any:
			for _, e := range x {
				if out := find(e); out != nil {
					return out
				}
			}
		}
		return nil
	}
	en := find(doc)
	if len(en) != 51 { // plan 28 section 14.4 lists 51 rows, the same as the contract enum
		t.Fatalf("the fake.scenario enum has %d values, want 51", len(en))
	}
	return en
}

// roleFor is the first role of the "Roles" column of plan 28 section 14.4 for the scenario.
func roleFor(scenario string) string {
	switch scenario {
	case "test-tamper", "test-delete", "protected-path-edit", "golden-update", "test-helper-weaken", "new-test-monkeypatch",
		"nested-module-hide", "multi-commit", "test-spec-conflict", "green-after-retry", "large-diff", "not-descendant",
		"green", "test-skip", "discovery-config-edit", "test-env-detection", "produce-docs":
		return "implementer"
	}
	if strings.HasPrefix(scenario, "review-") || strings.HasPrefix(scenario, "adjudicate-") {
		return "reviewer"
	}
	return "tester"
}

// 28-AC-7: every fake scenario issues at least one request to SC_FAKE_GATEWAY_URL (the Gateway call is the first thing
// each scenario does; the process is stopped as soon as the stub Gateway has seen it, so the 24 h sleepers are
// covered too).
func TestRun_EveryScenarioCallsTheGateway_28AC7(t *testing.T) {
	for _, scenario := range scenarioEnum(t) {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f := newFx(t)
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"--scenario", scenario, "--role", roleFor(scenario), "--attempt", "1", "--task", "sc-1", "--execution", "sc-1-x1",
				"--repo", f.repo, "--context", f.ctxDir, "--out", f.out}
			cmd := exec.Command(exe, append([]string{"-test.run=^TestHelperProcess_Run$", "--"}, args...)...)
			cmd.Env = []string{helperEnv + "=1", "SC_FAKE_GATEWAY_URL=" + f.gw.URL, "SC_FAKE_GATEWAY_TOKEN=gw-token-for-tests", "PATH=" + os.Getenv("PATH")}
			var out lockedBuf
			cmd.Stdout, cmd.Stderr = &out, &out
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { _ = cmd.Wait(); close(done) }()
			defer func() { _ = cmd.Process.Kill(); <-done }()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
		poll:
			for f.hits.Load() < 1 {
				select {
				case <-done:
					break poll
				default:
				}
				if (clock.Real{}).Sleep(ctx, 20*time.Millisecond) != nil {
					break
				}
			}
			if f.hits.Load() < 1 {
				t.Errorf("scenario %s sent no request to SC_FAKE_GATEWAY_URL within 20 s; output:\n%s", scenario, out.String())
			}
		})
	}
}

// 27-AC-5 (Layer 1): the fake harness dumps its environment (NAME=VALUE per line) to <out>/env.txt and the list of the
// files below /var/run and /var/sc to <out>/files.txt, whatever the scenario.
func TestRun_DumpsEnvironmentAndFileList_27AC5(t *testing.T) {
	f := newFx(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, append([]string{"-test.run=^TestHelperProcess_Run$", "--"}, f.args("failure")...)...)
	cmd.Env = []string{helperEnv + "=1", "SC_FAKE_GATEWAY_URL=" + f.gw.URL, "SC_FAKE_GATEWAY_TOKEN=gw-token-for-tests",
		"PATH=" + os.Getenv("PATH"), "S6_DUMP_PROBE=probe-value-27ac5"}
	_ = cmd.Run() // failure exits 1
	env, err := os.ReadFile(filepath.Join(f.out, "env.txt"))
	if err != nil {
		t.Fatalf("the fake harness wrote no env.txt to --out: %v", err)
	}
	lines := strings.Split(string(env), "\n")
	has := func(want string) bool {
		for _, l := range lines {
			if l == want {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"S6_DUMP_PROBE=probe-value-27ac5", "SC_FAKE_GATEWAY_TOKEN=gw-token-for-tests"} {
		if !has(want) {
			t.Errorf("env.txt lacks the line %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(f.out, "files.txt")); err != nil {
		t.Errorf("the fake harness wrote no files.txt to --out: %v", err)
	}
}
