package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// F06 S7, 22-AC-24 (child environment, replay export; plan 22 sections 8 and 14.1 step 4).

// fxCheckChildEnv asserts the from-scratch child environment of plan 22 section 8 on every recorded block:
// PATH (the fixed toolchain value), HOME and TMPDIR (fresh per run), the suite's env and CI=true, nothing else,
// and no SC_ variable (other than those in sc, which a check may be given).
func fxCheckChildEnv(t *testing.T, r *fxRepo, blocks []fxEnvBlock, suiteEnv map[string]string, sc ...string) {
	t.Helper()
	allowed := map[string]bool{"PATH": true, "HOME": true, "TMPDIR": true, "CI": true, "PWD": true}
	for k := range suiteEnv {
		allowed[k] = true
	}
	for _, k := range sc {
		allowed[k] = true
	}
	homes, tmps := map[string]int{}, map[string]int{}
	for i, b := range blocks {
		// A run is identified by the process sc-verify started: several blocks of one run share HOME and TMPDIR.
		run := -i - 1
		for j, p := range b.Procs {
			if fxIsRun(p) && j > 0 {
				run = b.Procs[j-1].Pid
				break
			}
		}
		var keys []string
		for k, v := range b.Env {
			keys = append(keys, k)
			if !allowed[k] {
				t.Errorf("block %d (%s): unexpected variable %s=%q in the child environment", i, b.Cwd, k, v)
			}
			if strings.HasPrefix(k, "SC_") && !allowed[k] {
				t.Errorf("block %d: SC_ variable %s in the child environment", i, k)
			}
			for _, secret := range []string{r.leak, r.nonce, r.intent, "fxleak"} {
				if strings.Contains(v, secret) {
					t.Errorf("block %d: %s=%q carries a value of the launching environment", i, k, v)
				}
			}
		}
		sort.Strings(keys)
		for k, v := range suiteEnv {
			if b.Env[k] != v {
				t.Errorf("block %d: %s = %q, want the suite's %q", i, k, b.Env[k], v)
			}
		}
		for _, k := range []string{"GOFLAGS", "GOENV", "LEAK_SENTINEL", "SC_EVIDENCE_NONCE", "SC_INTENT_ID"} {
			if _, ok := b.Env[k]; ok {
				t.Errorf("block %d: %s leaked into the child environment", i, k)
			}
		}
		if b.Env["CI"] != "true" {
			t.Errorf("block %d: CI = %q, want true", i, b.Env["CI"])
		}
		if !strings.HasSuffix(b.Env["PATH"], r.childPath) || strings.Contains(b.Env["PATH"], "fx-leak-path") {
			t.Errorf("block %d: PATH = %q, want the fixed toolchain value %q (and nothing of the launching PATH)", i, b.Env["PATH"], r.childPath)
		}
		home, tmp := b.Env["HOME"], b.Env["TMPDIR"]
		if home == "" || tmp == "" || home == r.home || tmp == r.tmp || home == tmp {
			t.Errorf("block %d: HOME %q TMPDIR %q must be fresh directories of their own", i, home, tmp)
		}
		if !b.HomeDir || !b.TmpDir {
			t.Errorf("block %d: HOME or TMPDIR is not an existing directory", i)
		}
		if rel, err := filepath.Rel(r.ws, home); err == nil && !strings.HasPrefix(rel, "..") {
			t.Errorf("block %d: HOME %q is inside the workspace", i, home)
		}
		if x, ok := homes[home]; ok && x != run {
			t.Errorf("block %d: HOME %q was already used by another run (it must be fresh per run)", i, home)
		}
		if x, ok := tmps[tmp]; ok && x != run {
			t.Errorf("block %d: TMPDIR %q was already used by another run (it must be fresh per run)", i, tmp)
		}
		homes[home], tmps[tmp] = run, run
		if len(b.ProcEnv) != len(b.Env) {
			t.Errorf("block %d: /proc/self/environ has %d variables, os.Environ %d", i, len(b.ProcEnv), len(b.Env))
		}
		for k, v := range b.ProcEnv {
			if b.Env[k] != v {
				t.Errorf("block %d: /proc/self/environ %s=%q differs from os.Environ", i, k, v)
			}
		}
	}
}

// fxCheckOwnProcessGroup asserts, from the recorded ancestry, that the suite's command was executed without a
// shell, directly by sc-verify, as the leader of its own process group.
func fxCheckOwnProcessGroup(t *testing.T, blocks []fxEnvBlock) {
	t.Helper()
	for i, b := range blocks {
		idx := -1
		for j, p := range b.Procs {
			if fxIsRun(p) {
				idx = j
				break
			}
		}
		if idx < 1 {
			t.Errorf("block %d: sc-verify is not among the ancestors of the test binary: %+v", i, b.Procs)
			continue
		}
		sv, child := b.Procs[idx], b.Procs[idx-1]
		if filepath.Base(child.Exe) != "go" {
			t.Errorf("block %d: the suite's command runs as %s, want the go command directly (no shell)", i, child.Exe)
		}
		if child.Pgrp != child.Pid || child.Pgrp == sv.Pgrp {
			t.Errorf("block %d: the suite's command has pid %d pgrp %d, sc-verify pgrp %d; want its own process group", i, child.Pid, child.Pgrp, sv.Pgrp)
		}
	}
}

// 22-AC-24: the child environment of a suite run contains no SC_ variable (the fixture test prints its
// environment). The launching environment is hostile: GOFLAGS, GOENV, a secret, the nonce and the intent id, and
// a PATH without the toolchain. Each variable would change the suite if it leaked.
func TestRun_ChildEnvironmentIsBuiltFromScratch_22AC24(t *testing.T) {
	suiteEnv := map[string]string{"FX_EXTRA": "suite-value"}
	r := fxNewRepo(t, fxConfig{Fixture: "env", Shared: []string{"_envdump"}, Suites: []fxSuite{fxGoSuite(suiteEnv)}})
	res := r.run(fxRunOpts{Baseline: "run", Hostile: true})
	if res.Exit != 0 || res.TimedOut {
		t.Fatalf("run exit %d (timed out %v); stderr %q", res.Exit, res.TimedOut, fxTrunc(res.Stderr))
	}
	blocks := fxReadEnvBlocks(t, filepath.Join(r.out, "env.txt"))
	if len(blocks) < 2 {
		t.Fatalf("the fixture test ran %d times, want at least the base and the head run", len(blocks))
	}
	full := r.suiteEnv(suiteEnv)
	fxCheckChildEnv(t, r, blocks, full)
	fxCheckOwnProcessGroup(t, blocks)
	cwds := map[string]bool{}
	for _, b := range blocks {
		cwds[b.Cwd] = true
	}
	if len(cwds) < 2 || !cwds[r.ws] {
		t.Errorf("the suite ran in %v, want the head workspace %s and a separate base tree", cwds, r.ws)
	}
	doc, _ := r.emitDoc(fxPhaseRed)
	for _, id := range []string{"unit::fx.TestLeakedViaGOFLAGS", "unit::fx.TestLeakedViaGOENV"} {
		if _, ok := doc.test(id); ok {
			t.Errorf("%s was compiled into the suite: the launching GOFLAGS or GOENV leaked", id)
		}
	}
	if got := fxDecodeIDSet(t, doc.Observed.TestIDSets["head_present"]); strings.Join(got, ",") != "unit::fx.TestEnvDump,unit::fx.TestPlain" {
		t.Errorf("head_present = %v, want exactly TestEnvDump and TestPlain", got)
	}
	roots := []string{r.record, r.ws, r.home, r.tmp}
	for _, secret := range []string{r.nonce, r.intent} {
		if hits := fxFilesWith(roots, secret); len(hits) != 0 {
			t.Errorf("a file holds %q: %v", secret, hits)
		}
	}
}

// 22-AC-24: a suite env entry named SC_* is not passed on. run either refuses the validation (exit 2, nothing
// ran) or runs the suite without the variable.
func TestRun_SuiteEnvCannotReintroduceSCVariables_22AC24(t *testing.T) {
	suiteEnv := map[string]string{"SC_PHASE": "from-suite", "SC_FAKE": "x", "FX_EXTRA": "suite-value"}
	r := fxNewRepo(t, fxConfig{Fixture: "env", Shared: []string{"_envdump"}, Suites: []fxSuite{fxGoSuite(suiteEnv)}})
	res := r.run(fxRunOpts{})
	if res.Exit == 2 {
		if fxExists(filepath.Join(r.out, "env.txt")) {
			t.Errorf("run exited 2 but a suite ran")
		}
		return
	}
	if res.Exit != 0 {
		t.Fatalf("run exit %d; stderr %q", res.Exit, fxTrunc(res.Stderr))
	}
	blocks := fxReadEnvBlocks(t, filepath.Join(r.out, "env.txt"))
	if len(blocks) == 0 {
		t.Fatal("the fixture test did not run")
	}
	for i, b := range blocks {
		for k, v := range b.Env {
			if strings.HasPrefix(k, "SC_") {
				t.Errorf("block %d: %s=%q in the child environment", i, k, v)
			}
		}
		if b.Env["FX_EXTRA"] != "suite-value" {
			t.Errorf("block %d: the suite's other env entries are lost: FX_EXTRA = %q", i, b.Env["FX_EXTRA"])
		}
	}
}

// 22-AC-24, replay export: at GREEN the change-criterion manifest tests run again against `git archive` of the
// red commit, in a fresh directory with no .git (and none above it), with every stub marker line removed and in
// the same from-scratch environment (no SC_* variable). Tests that fail only while a .git entry, the test(...)
// commit subject or the stub marker is visible then pass in the replay, which is what G9 detects; a test that
// fails because the implementation is absent still fails there, which shows the replay ran the red tree.
func TestRun_ReplayExportHasNoGitNoSCVariablesNoStubMarkers_22AC24(t *testing.T) {
	suiteEnv := map[string]string{}
	r := fxNewRepo(t, fxConfig{Fixture: "replay", Shared: []string{"_envdump"}, Suites: []fxSuite{fxGoSuite(suiteEnv)}})
	if r.red == "" {
		t.Fatal("fixture has no red commit")
	}
	ids := []string{"unit::fx.TestStillRed", "unit::fx.TestMarker", "unit::fx.TestGit", "unit::fx.TestSubject", "unit::fx.TestReplayEnv"}
	protected := "change\t" + strings.Join(ids, "\nchange\t") + "\npreservation\tunit::fx.TestBaseline"
	res := r.run(fxRunOpts{Phase: fxPhaseGreen, Protected: protected})
	if res.Exit != 0 || res.TimedOut {
		t.Fatalf("run exit %d (timed out %v); stderr %q", res.Exit, res.TimedOut, fxTrunc(res.Stderr))
	}
	doc, _ := r.emitDoc(fxPhaseGreen)
	rp := doc.Observed.RedReplay
	if rp == nil {
		t.Fatalf("the GREEN observation has no red_replay")
	}
	if rp.RedCommit != r.red {
		t.Errorf("red_replay.red_commit = %q, want SC_RED_SHA %q", rp.RedCommit, r.red)
	}
	got := map[string]string{}
	for _, x := range rp.Tests {
		if _, dup := got[x.ID]; dup {
			t.Errorf("red_replay lists %s twice", x.ID)
		}
		got[x.ID] = x.Status
	}
	want := map[string]string{
		"unit::fx.TestStillRed":  "failed_assertion", // the red tree has the stub, not the implementation
		"unit::fx.TestMarker":    "passed",           // the marker line is removed from the export
		"unit::fx.TestGit":       "passed",           // no .git in or above the export
		"unit::fx.TestSubject":   "passed",           // git log finds no repository
		"unit::fx.TestReplayEnv": "passed",           // records the replay's environment
	}
	for id, st := range want {
		if got[id] != st {
			t.Errorf("red_replay %s = %q, want %q", id, got[id], st)
		}
	}
	if len(got) != len(want) {
		t.Errorf("red_replay lists %v, want only the 5 change-criterion tests (not the preservation test)", got)
	}
	for _, id := range append(append([]string(nil), ids...), "unit::fx.TestBaseline") {
		if x, ok := doc.test(id); !ok || x.StatusHead != "passed" {
			t.Errorf("head run: protected test %s = %+v (%v), want it reported as passed", id, x, ok)
		}
	}
	blocks := fxReadEnvBlocks(t, filepath.Join(r.out, "env.txt"))
	fxCheckChildEnv(t, r, blocks, r.suiteEnv(suiteEnv))
	var withGit, withoutGit int
	for _, b := range blocks {
		if b.GitAbove {
			withGit++
		} else {
			withoutGit++
		}
	}
	if withGit == 0 || withoutGit == 0 {
		t.Errorf("env blocks with a .git above: %d, without: %d; want the head runs inside the repository and the replay outside any", withGit, withoutGit)
	}
	if b, err := os.ReadFile(filepath.Join(r.ws, "stub.go")); err != nil || strings.Contains(string(b), "SC-STUB") {
		t.Errorf("the head tree was changed or still has a marker: %v", err)
	}
}
