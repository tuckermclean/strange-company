// Package fakeharness is the library entry of the sc-fake-harness binary (plan 28 section 8.2): the cooperative
// fake harness that sc-worker runs through the shim in the component tests.
package fakeharness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"strange.company/sc/internal/clock"
)

// Run executes the fake harness for the given sc-fake-harness argv (without the program name) and environment
// and returns the process exit code. args carry --scenario, --role, --attempt, --task, --execution, --repo,
// --context and --out; getenv answers SC_FAKE_GATEWAY_URL and SC_FAKE_GATEWAY_TOKEN.
//
// This build implements the cooperative tester and implementer behaviours of the scenarios success, red-valid
// and push, and the scenarios failure, timeout and ignore-cancel for every role (plan 28 section 14.4); the
// other scenarios belong to later slices of F07.
func Run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	f := map[string]string{}
	for i := 0; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "--") {
			fmt.Fprintf(stderr, "sc-fake-harness: unexpected argument %q\n", args[i])
			return 2
		}
		name := strings.TrimPrefix(args[i], "--")
		if i+1 >= len(args) {
			fmt.Fprintf(stderr, "sc-fake-harness: --%s needs a value\n", name)
			return 2
		}
		f[name] = args[i+1]
		i++
	}
	for _, need := range []string{"scenario", "role", "repo", "out"} {
		if f[need] == "" {
			fmt.Fprintf(stderr, "sc-fake-harness: --%s is required\n", need)
			return 2
		}
	}
	step := func(name string) {
		b, _ := json.Marshal(map[string]any{"type": "sc_fake_step", "step": name})
		fmt.Fprintln(stdout, string(b))
	}
	result := func(isErr bool) {
		b, _ := json.Marshal(map[string]any{"type": "sc_fake_result", "is_error": isErr, "num_turns": 1})
		fmt.Fprintln(stdout, string(b))
	}

	scenario := f["scenario"]
	switch scenario {
	case "success", "red-valid", "push":
		if f["role"] != "tester" && f["role"] != "implementer" {
			fmt.Fprintf(stderr, "sc-fake-harness: role %q is not implemented for scenario %s in this build\n", f["role"], scenario)
			return 2
		}
	case "failure", "timeout", "ignore-cancel":
	default:
		fmt.Fprintf(stderr, "sc-fake-harness: scenario %q is not implemented in this build\n", scenario)
		return 2
	}

	// The signal handling of timeout (exit on SIGTERM) and ignore-cancel (swallow it) is armed before the model call.
	sigCtx, stopSig := context.WithCancel(context.Background())
	defer stopSig()
	if scenario == "timeout" || scenario == "ignore-cancel" {
		sigc := make(chan os.Signal, 8)
		signal.Notify(sigc, syscall.SIGTERM)
		defer signal.Stop(sigc)
		if scenario == "timeout" {
			var cancel context.CancelFunc
			sigCtx, cancel = context.WithCancel(sigCtx)
			defer cancel()
			go func() {
				select {
				case <-sigc:
					cancel()
				case <-sigCtx.Done():
				}
			}()
		} else {
			go func() {
				for {
					select {
					case <-sigc: // ignored on purpose (a wedged harness)
					case <-sigCtx.Done():
						return
					}
				}
			}()
		}
	}

	// Every scenario first calls the Gateway (plan 28 section 9.2, Fake).
	step("gateway_request")
	if code, ok := gatewayCall(getenv, f); !ok {
		if code != "" {
			fmt.Fprintf(stdout, "SC_%s\n", code)
		}
		result(true)
		return 1
	}

	switch scenario {
	case "failure":
		step("plan")
		step("work")
		result(true)
		return 1
	case "timeout", "ignore-cancel":
		// After the model call the fake sleeps for 24 h; timeout honors SIGTERM, ignore-cancel does not.
		if err := (clock.Real{}).Sleep(sigCtx, 24*time.Hour); err != nil {
			return 143
		}
		result(false)
		return 0
	}

	push := f["scenario"] == "push"
	var claim map[string]any
	var err error
	step("write_files")
	switch f["role"] {
	case "tester":
		claim, err = tester(f["repo"], push)
	default:
		claim, err = implementer(f["repo"], push)
	}
	if err != nil {
		fmt.Fprintf(stderr, "sc-fake-harness: %v\n", err)
		result(true)
		return 1
	}
	step("write_claim")
	b, _ := json.Marshal(claim)
	if err := os.WriteFile(filepath.Join(f["out"], "claim.json"), b, 0o644); err != nil {
		fmt.Fprintf(stderr, "sc-fake-harness: claim: %v\n", err)
		result(true)
		return 1
	}
	result(false)
	return 0
}

// gatewayCall posts the fixed fake request; ok is false on any non-200, with the x-sc-error-code header value
// (without the SC_ prefix) in code.
func gatewayCall(getenv func(string) string, f map[string]string) (code string, ok bool) {
	url := strings.TrimRight(getenv("SC_FAKE_GATEWAY_URL"), "/")
	if url == "" {
		return "", false
	}
	attempt := f["attempt"]
	if attempt == "" {
		attempt = "1"
	}
	body, _ := json.Marshal(map[string]any{
		"model": "fake-model", "max_tokens": 64, "stream": false,
		"messages": []any{map[string]any{"role": "user", "content": f["scenario"] + "/" + f["role"] + "/" + attempt}},
	})
	req, err := http.NewRequest(http.MethodPost, url+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", false
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := getenv("SC_FAKE_GATEWAY_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	cl := &http.Client{Timeout: 30 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return strings.TrimPrefix(resp.Header.Get("x-sc-error-code"), "SC_"), false
	}
	return "", true
}

const testSource = `package calc

import "testing"

func TestAddAC1(t *testing.T) {
	if Add(2, 3) != 5 {
		t.Fatal("Add(2, 3) != 5")
	}
}

func TestAddAC2(t *testing.T) {
	if Add(-1, 1) != 0 {
		t.Fatal("Add(-1, 1) != 0")
	}
}
`

func write(repo, rel, content string) error {
	p := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(content), 0o644)
}

func handoff(did string) map[string]any {
	return map[string]any{
		"what_i_did": did, "what_remains": "Nothing.", "recommended_next_role": nil,
		"questions": []any{}, "known_risks": []any{},
	}
}

// tester writes calc/add_ac_test.go (and two more test files for the push scenario).
func tester(repo string, push bool) (map[string]any, error) {
	files := []string{"calc/add_ac_test.go"}
	if err := write(repo, files[0], testSource); err != nil {
		return nil, err
	}
	if push {
		extra := map[string]string{
			"calc/add_edge_test.go": "package calc\n\nimport \"testing\"\n\nfunc TestAddEdgeZero(t *testing.T) {\n\tif Add(0, 0) != 0 {\n\t\tt.Fatal(\"Add(0, 0) != 0\")\n\t}\n}\n",
			"calc/add_neg_test.go":  "package calc\n\nimport \"testing\"\n\nfunc TestAddNegative(t *testing.T) {\n\tif Add(-2, -3) != -5 {\n\t\tt.Fatal(\"Add(-2, -3) != -5\")\n\t}\n}\n",
		}
		for _, n := range []string{"calc/add_edge_test.go", "calc/add_neg_test.go"} {
			if err := write(repo, n, extra[n]); err != nil {
				return nil, err
			}
			files = append(files, n)
		}
	}
	fl := make([]any, len(files))
	for i, f := range files {
		fl[i] = f
	}
	return map[string]any{
		"state": "completed", "summary": "Add tests for Add covering AC-1 and AC-2",
		"role_output": map[string]any{"kind": "tester", "test_files": fl, "ac_mapping": []any{
			map[string]any{"ac_id": "AC-1", "tests": []any{"TestAddAC1"}},
			map[string]any{"ac_id": "AC-2", "tests": []any{"TestAddAC2"}},
		}},
		"handoff": handoff("Wrote the acceptance tests for Add."),
	}, nil
}

// implementer makes Add return a+b (and adds two documentation files for the push scenario).
func implementer(repo string, push bool) (map[string]any, error) {
	p := filepath.Join(repo, "calc", "calc.go")
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	const stub = "func Add(a, b int) int { return 0 }"
	if !bytes.Contains(b, []byte(stub)) {
		return nil, fmt.Errorf("calc/calc.go has no stub Add to implement")
	}
	if err := os.WriteFile(p, bytes.Replace(b, []byte(stub), []byte("func Add(a, b int) int { return a + b }"), 1), 0o644); err != nil {
		return nil, err
	}
	changed := []any{"calc/calc.go"}
	if push {
		for n, c := range map[string]string{
			"calc/doc.go":     "// Package calc is the tiny library the fake harness scenarios work on.\npackage calc\n",
			"calc/CHANGES.md": "# Changes\n\n- Add returns the sum of its arguments.\n",
		} {
			if err := write(repo, n, c); err != nil {
				return nil, err
			}
		}
		changed = append(changed, "calc/CHANGES.md", "calc/doc.go")
	}
	return map[string]any{
		"state": "completed", "summary": "Make Add return the sum of its arguments",
		"role_output": map[string]any{"kind": "implementer", "changed_files": changed, "ac_addressed": []any{"AC-1"}},
		"handoff":     handoff("Implemented Add."),
	}, nil
}
