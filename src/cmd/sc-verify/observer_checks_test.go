package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// F06 S7, 22-AC-32 (a toolingFrom omitted in validation.yaml runs from base; plan 22 section 14.1 step 4,
// G6, contracts/repo-validation.schema.yaml `toolingFrom`).

type fxCheckRecord struct{ tool, cwd, target, keys string }

func fxReadCheckRecord(t *testing.T, r *fxRepo, name string) fxCheckRecord {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.out, "check-"+name+".txt"))
	if err != nil {
		t.Fatalf("check %s recorded nothing (it did not run): %v", name, err)
	}
	var rec fxCheckRecord
	for _, f := range strings.Fields(string(b)) {
		k, v, _ := strings.Cut(f, "=")
		switch k {
		case "tool":
			rec.tool = v
		case "cwd":
			rec.cwd = v
		case "target":
			rec.target = v
		case "keys":
			rec.keys = v
		}
	}
	return rec
}

func fxSame(t *testing.T, a, b string) bool {
	t.Helper()
	x, err1 := filepath.EvalSymlinks(a)
	y, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && x == y
}

// 22-AC-32: the checker program at head was weakened by the work under test (head's tools/check always passes);
// the head tree also holds the file the base checker rejects. A check without toolingFrom (and one with an
// explicit base) must run the base checker from a base tree with SC_TARGET_TREE naming the head workspace, so
// it sees the violation; only a check that says `toolingFrom: head` runs the head checker in the workspace.
func TestRun_CheckWithoutToolingFromRunsBaseTooling_22AC32(t *testing.T) {
	r := fxNewRepo(t, fxConfig{
		Fixture: "checks", Shared: []string{"_envdump"}, Suites: []fxSuite{fxGoSuite(nil)},
		Checks: []fxCheck{
			{Name: "default-base", Argv: []string{"go", "run", "./tools/check", "default-base"}},
			{Name: "explicit-head", Argv: []string{"go", "run", "./tools/check", "explicit-head"}, ToolingFrom: "head"},
			{Name: "explicit-base", Argv: []string{"go", "run", "./tools/check", "explicit-base"}, ToolingFrom: "base"},
		},
	})
	res := r.run(fxRunOpts{Phase: fxPhaseGreen, Protected: "preservation\tunit::fx.TestBaseline"})
	if res.Exit != 0 || res.TimedOut {
		t.Fatalf("run exit %d (timed out %v); stderr %q", res.Exit, res.TimedOut, fxTrunc(res.Stderr))
	}
	doc, _ := r.emitDoc(fxPhaseGreen)
	exits := map[string]int{}
	for _, c := range doc.Observed.Checks {
		exits[c.Name] = c.ExitCode
		if !c.Required || c.TimedOut {
			t.Errorf("check %+v, want required and not timed out", c)
		}
	}
	if len(doc.Observed.Checks) != 3 {
		t.Fatalf("checks = %+v, want the 3 configured checks", doc.Observed.Checks)
	}
	if exits["default-base"] != 1 || exits["explicit-base"] != 1 || exits["explicit-head"] != 0 {
		t.Errorf("check exit codes = %v; the base checker must see the violation (1), the weakened head checker not (0)", exits)
	}
	allowedSC := []string{"SC_TARGET_TREE"}
	for _, name := range []string{"default-base", "explicit-base"} {
		rec := fxReadCheckRecord(t, r, name)
		if rec.tool != "base" {
			t.Errorf("check %s ran the %q checker, want the one committed at base", name, rec.tool)
		}
		if fxSame(t, rec.cwd, r.ws) {
			t.Errorf("check %s ran in the head workspace %s, want a base tree", name, rec.cwd)
		}
		if !fxSame(t, rec.target, r.ws) {
			t.Errorf("check %s: SC_TARGET_TREE = %q, want the head workspace %s", name, rec.target, r.ws)
		}
		for _, k := range strings.Split(rec.keys, ",") {
			if strings.HasPrefix(k, "SC_") && k != "SC_TARGET_TREE" {
				t.Errorf("check %s: child environment has %s", name, k)
			}
		}
		if !strings.Contains(rec.keys, "SC_TARGET_TREE") {
			t.Errorf("check %s: SC_TARGET_TREE is not in the child environment (%s)", name, rec.keys)
		}
	}
	head := fxReadCheckRecord(t, r, "explicit-head")
	if head.tool != "head" || !fxSame(t, head.cwd, r.ws) {
		t.Errorf("explicit-head ran the %q checker in %s, want the head checker in the workspace", head.tool, head.cwd)
	}
	for _, k := range strings.Split(head.keys, ",") {
		if strings.HasPrefix(k, "SC_") && k != allowedSC[0] {
			t.Errorf("explicit-head: child environment has %s", k)
		}
	}
	if !fxExists(filepath.Join(r.ws, "VIOLATION")) {
		t.Errorf("fixture: head has no VIOLATION file")
	}
}

// 22-AC-32 (RED side of the same default): checks run at GREEN only. A RED run never executes a check.
func TestRun_RedRunExecutesNoCheck_22AC32(t *testing.T) {
	r := fxNewRepo(t, fxConfig{
		Fixture: "checks", Shared: []string{"_envdump"}, Suites: []fxSuite{fxGoSuite(nil)},
		Checks: []fxCheck{{Name: "default-base", Argv: []string{"go", "run", "./tools/check", "default-base"}}},
	})
	res := r.run(fxRunOpts{Baseline: "run"})
	if res.Exit != 0 {
		t.Fatalf("run exit %d; stderr %q", res.Exit, fxTrunc(res.Stderr))
	}
	if !fxExists(filepath.Join(r.out, "env.txt")) {
		t.Fatalf("no suite ran")
	}
	if fxExists(filepath.Join(r.out, "check-default-base.txt")) {
		t.Errorf("a check ran during RED")
	}
}
