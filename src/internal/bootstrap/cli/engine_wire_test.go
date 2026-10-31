package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"strange.company/sc/internal/bootstrap/cli"
	"strange.company/sc/internal/bootstrap/engine"
	"strange.company/sc/internal/clock"
)

// preflightUnit runs the real cli.Run preflight as the "preflight" phase of an engine pipeline.
type preflightUnit struct {
	run func() (int, string)
}

func (u *preflightUnit) Name() string                                { return "preflight" }
func (u *preflightUnit) Inputs(context.Context) (any, error)         { return map[string]string{"k": "v"}, nil }
func (u *preflightUnit) Check(context.Context) (engine.Check, error) { return engine.Check{}, nil }
func (u *preflightUnit) Timeout() time.Duration                      { return time.Minute }
func (u *preflightUnit) Retryable(error) bool                        { return false }
func (u *preflightUnit) Steps() []engine.Unit                        { return nil }
func (u *preflightUnit) Run(context.Context) ([]engine.OutputRef, error) {
	code, out := u.run()
	if code != 0 {
		return nil, &engine.Error{Code: "PREFLIGHT_FAILED", Message: fmt.Sprintf("exit %d: %s", code, out)}
	}
	return nil, nil
}

type fixedRand struct{ n byte }

func (r *fixedRand) Read(p []byte) (int, error) {
	for k := range p {
		r.n++
		p[k] = r.n
	}
	return len(p), nil
}

// Wire confirmations (S2 review 1 carry): the confirmations preflight records through Deps.Record reach the
// journal through the engine, with this run's id, the clock's time and the method.
func TestDepsRecord_PreflightConfirmationsPersistInJournal_50AC9(t *testing.T) {
	i := newInst(t)
	dir := filepath.Join(t.TempDir(), ".bootstrap")
	clk := clock.NewFake(time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC))
	eng, err := engine.New(engine.Config{
		Dir: dir, Clock: clk, Rand: &fixedRand{}, Cluster: "lab-one", InstallUID: "6f1c2d3e-4b5a-4c6d-8e7f-0a1b2c3d4e5f",
		Provider: "existing", Profile: "self-hosting-minimum",
		ConfigDigest: "sha256:aa11bb22cc33dd44ee55ff6600112233445566778899aabbccddeeff00112233",
		Command:      "preflight", User: "alice", Host: "host-a", Version: "v0.1.0",
		GitHead:       "4b825dc642cb6eb9a060e54bf8d69288fbee4904",
		ToolboxDigest: "sha256:5555555555555555555555555555555555555555555555555555555555555555", ToolboxSource: "pulled",
	})
	if err != nil {
		t.Fatal(err)
	}
	deps := i.deps()
	deps.Record = eng.RecordConfirmation
	env := map[string]string{}
	for k, v := range i.env {
		env[k] = v
	}
	u := &preflightUnit{run: func() (int, string) {
		var out, errb bytes.Buffer
		code := cli.Run(i.preflightArgs(i.goodConfirms()...), env, &out, &errb, deps)
		return code, out.String() + errb.String()
	}}
	res := eng.Execute(context.Background(), engine.Pipeline{Kind: "install", Phases: []engine.Phase{u}}, engine.Options{})
	if res.Exit != 0 {
		t.Fatalf("Execute exit %d (%s %s), want 0", res.Exit, res.Code, res.Message)
	}
	b, err := os.ReadFile(filepath.Join(dir, "journal.json"))
	if err != nil {
		t.Fatalf("no local journal: %v", err)
	}
	var j struct {
		Confirmations []struct {
			Kind          string `json:"kind"`
			SubjectDigest string `json:"subject_digest"`
			ConfirmedAt   string `json:"confirmed_at"`
			RunID         string `json:"run_id"`
			Method        string `json:"method"`
		} `json:"confirmations"`
	}
	if err := json.Unmarshal(b, &j); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, c := range j.Confirmations {
		seen[c.Kind] = true
		if c.RunID != eng.RunID() || c.RunID == "" {
			t.Errorf("%s: run_id %q, want %q", c.Kind, c.RunID, eng.RunID())
		}
		if c.Method != "flag" {
			t.Errorf("%s: method %q, want flag", c.Kind, c.Method)
		}
		if c.ConfirmedAt != "2026-10-26T12:00:00Z" {
			t.Errorf("%s: confirmed_at %q, want the clock's 2026-10-26T12:00:00Z", c.Kind, c.ConfirmedAt)
		}
		if len(c.SubjectDigest) != len("sha256:")+64 {
			t.Errorf("%s: subject_digest %q is not a sha256 digest", c.Kind, c.SubjectDigest)
		}
	}
	for _, kind := range []string{"model-budget", "provider-backstop"} {
		if !seen[kind] {
			t.Errorf("journal lacks the %s confirmation recorded in preflight; has %v", kind, j.Confirmations)
		}
	}
}
