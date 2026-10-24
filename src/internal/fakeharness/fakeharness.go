// Package fakeharness is the library entry of the sc-fake-harness binary (plan 28 section 8.2): the fake harness
// that sc-worker runs through the shim in the component tests. It implements the scenarios of plan 28 section 14.4.
package fakeharness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"strange.company/sc/internal/clock"
)

// maxOverBudgetCalls bounds the tight loop of the over-budget scenario (plan 28 section 14.4).
const maxOverBudgetCalls = 10000

// maxSpendLoopCalls bounds the spend-loop scenario (one request per second).
const maxSpendLoopCalls = 7200

// Run executes the fake harness for the given sc-fake-harness argv (without the program name) and environment
// and returns the process exit code. args carry --scenario, --role, --attempt, --task, --execution, --repo,
// --context and --out; getenv answers SC_FAKE_GATEWAY_URL and SC_FAKE_GATEWAY_TOKEN.
//
// Every scenario of the table first dumps its environment and file list to <out> (27-AC-5) and then calls the
// Gateway at least once (28-AC-7); what follows is the behavior of its row. Scenarios whose effect is produced by an
// sc-worker knob (skip_report, raw_result, disable_cancel, parent_override, ...) behave here like their cooperative
// base.
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
	scenario := f["scenario"]
	sp, ok := scenarios[scenario]
	if !ok {
		fmt.Fprintf(stderr, "sc-fake-harness: scenario %q is not known\n", scenario)
		return 2
	}
	if !sp.allows(f["role"]) {
		fmt.Fprintf(stderr, "sc-fake-harness: role %q is not valid for scenario %s\n", f["role"], scenario)
		return 2
	}
	r := &run{
		scenario: scenario, role: f["role"], repo: f["repo"], ctxDir: f["context"], out: f["out"],
		attempt: 1, getenv: getenv, stdout: stdout, stderr: stderr, f: f,
	}
	fmt.Sscanf(f["attempt"], "%d", &r.attempt)

	// 27-AC-5: what this container can see, whatever the scenario does next.
	dumpEnvironment(r.out)

	// The signal handling of timeout (exit on SIGTERM) and ignore-cancel (swallow it) is armed before the model call.
	sigCtx, stopSig := context.WithCancel(context.Background())
	defer stopSig()
	if sp.sleeper {
		sigc := make(chan os.Signal, 8)
		signal.Notify(sigc, syscall.SIGTERM)
		defer signal.Stop(sigc)
		if scenario != "ignore-cancel" {
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
	r.step("gateway_request")
	if code, ok := r.gatewaySequence(); !ok {
		if code != "" {
			fmt.Fprintf(stdout, "SC_%s\n", code)
		}
		r.result(true, nil)
		return 1
	}

	switch {
	case scenario == "failure":
		r.step("plan")
		r.step("work")
		r.result(true, nil)
		return 1
	case sp.sleeper:
		// After the model call the fake sleeps for 24 h; timeout honors SIGTERM, ignore-cancel does not.
		if err := (clock.Real{}).Sleep(sigCtx, 24*time.Hour); err != nil {
			return 143
		}
		r.result(false, nil)
		return 0
	case scenario == "malformed-stream":
		fmt.Fprintln(stdout, "this is not json")
		fmt.Fprintln(stdout, "<<<garbage>>> 0xdeadbeef")
		return 0
	case scenario == "claim-missing":
		r.step("plan")
		r.result(false, nil)
		return 0
	}

	r.step("write_files")
	claim, err := sp.work(r)
	if err != nil {
		fmt.Fprintf(stderr, "sc-fake-harness: %v\n", err)
		r.result(true, nil)
		return 1
	}
	r.step("write_claim")
	b, _ := json.Marshal(claim)
	if err := os.WriteFile(filepath.Join(r.out, "claim.json"), b, 0o644); err != nil {
		fmt.Fprintf(stderr, "sc-fake-harness: claim: %v\n", err)
		r.result(true, nil)
		return 1
	}
	if sp.after != nil {
		sp.after(r)
	}
	r.result(false, sp.denials)
	return 0
}

// run is the state of one invocation.
type run struct {
	scenario, role, repo, ctxDir, out string
	attempt                           int
	getenv                            func(string) string
	stdout, stderr                    io.Writer
	f                                 map[string]string
}

func (r *run) step(name string) {
	b, _ := json.Marshal(map[string]any{"type": "sc_fake_step", "step": name})
	fmt.Fprintln(r.stdout, string(b))
}

// result prints the terminal event; denials, when not nil, becomes its permission_denials list.
func (r *run) result(isErr bool, denials []map[string]any) {
	ev := map[string]any{"type": "sc_fake_result", "is_error": isErr, "num_turns": 1}
	if denials != nil {
		ev["permission_denials"] = denials
	}
	b, _ := json.Marshal(ev)
	fmt.Fprintln(r.stdout, string(b))
}

// gatewaySequence issues the Gateway requests of the scenario. ok is false when the scenario must stop: the Gateway
// denied the request (code is the x-sc-error-code without the SC_ prefix) or there was no Gateway.
func (r *run) gatewaySequence() (code string, ok bool) {
	switch r.scenario {
	case "over-budget":
		// A tight loop until the first non-200 (28-AC-7), at most maxOverBudgetCalls requests.
		for i := 0; i < maxOverBudgetCalls; i++ {
			if code, ok := r.gatewayCall("fake-model"); !ok {
				return code, false
			}
		}
		return "", false
	case "spend-loop":
		// One request per second until any non-200.
		for i := 0; i < maxSpendLoopCalls; i++ {
			if code, ok := r.gatewayCall("fake-model"); !ok {
				return code, false
			}
			_ = (clock.Real{}).Sleep(context.Background(), time.Second)
		}
		return "", false
	case "model-switch":
		if code, ok := r.gatewayCall("fake-model"); !ok {
			return code, false
		}
		// The second request names a model outside allowed_models; the denial is expected and does not end the run.
		if _, ok := r.gatewayCall("fake-model-b"); !ok {
			r.step("model_b_denied")
		}
		return "", true
	}
	return r.gatewayCall("fake-model")
}

// gatewayCall posts the fixed fake request; ok is false on any non-200, with the x-sc-error-code header value
// (without the SC_ prefix) in code.
func (r *run) gatewayCall(model string) (code string, ok bool) {
	url := strings.TrimRight(r.getenv("SC_FAKE_GATEWAY_URL"), "/")
	if url == "" {
		return "", false
	}
	body, _ := json.Marshal(map[string]any{
		"model": model, "max_tokens": 64, "stream": false,
		"messages": []any{map[string]any{"role": "user", "content": fmt.Sprintf("%s/%s/%d", r.scenario, r.role, r.attempt)}},
	})
	req, err := http.NewRequest(http.MethodPost, url+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", false
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := r.getenv("SC_FAKE_GATEWAY_TOKEN"); tok != "" {
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

// dumpEnvironment writes the environment (NAME=VALUE per line, sorted) to <out>/env.txt and the files below /var/run
// and /var/sc (the equivalent of `find /var/run /var/sc -type f`) to <out>/files.txt (27-AC-5, plan 27 section 22).
// It is best effort: a scenario never fails because of the dump.
func dumpEnvironment(out string) {
	env := os.Environ()
	sort.Strings(env)
	_ = os.WriteFile(filepath.Join(out, "env.txt"), []byte(strings.Join(env, "\n")+"\n"), 0o644)
	var files []string
	for _, root := range []string{"/var/run", "/var/sc"} {
		files = append(files, ListFiles(root, root)...)
	}
	_ = os.WriteFile(filepath.Join(out, "files.txt"), []byte(strings.Join(files, "\n")+"\n"), 0o644)
}

// ListFiles lists the regular files below root, following a symlinked root (on Debian /var/run is a link to /run),
// named by their path below display.
func ListFiles(root, display string) []string {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil
	}
	var files []string
	_ = filepath.WalkDir(real, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			rel, err := filepath.Rel(real, p)
			if err == nil {
				files = append(files, filepath.Join(display, rel))
			}
		}
		return nil
	})
	return files
}
