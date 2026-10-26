package worker_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"strange.company/sc/internal/clock"
)

// Slice S6b of F07 (Layer 1): the follow-ups of the S6a round-3 review (F1, F2, F3) and the hostile fake knobs
// parent_override, claim_commit_override, push_extra_refs and skip_fold (S6a-r1 N7, plan 28 sections 8.2, 14.4, 28-R-5).
//
// Readings of plan 28 section 14.4 where the table is not explicit (reported in the handback):
//   - parent_override: base  -> the commit sc-worker creates has parent base_commit instead of start_commit, and the
//     push uses the normal lease (--force-with-lease=<ref>:<start_commit>), so the remote branch moves to it;
//   - claim_commit_override: start -> the push is normal (the remote branch head is the new commit), but the posted
//     result's commit field is start_commit;
//   - push_extra_refs -> first `git push origin HEAD:refs/heads/main` (rejected by the remote; event
//     fake_push_main_rejected), then refs/heads/scratch/sc-fake and the tag sc-fake-tag are pushed (to the new commit
//     or at least to an existing one), then the normal push of the branch;
//   - skip_fold -> the harness's local commits are pushed as they are: start_commit..head holds exactly the harness
//     commits and no harness_commits_folded event is logged.
// Runner-side verdicts (violations, quarantine) are C02 and are not asserted.

func probeValidation() []any {
	return []any{map[string]any{"name": "probe", "argv": []any{"sc-probe", "--code", "0"}, "timeout_seconds": 300}}
}

func noBranchPushed(t *testing.T, gr *gitRun) {
	t.Helper()
	if h := gr.fx.head("agent/sc-1"); h != "" {
		t.Errorf("agent/sc-1 was pushed (%s), want nothing pushed", h)
	}
}

// s6bForgedRun runs the F1 scenario with the test-owned shim, which (unlike the production shim) does not kill a harness
// that forges res-<n>.json: the harness stays alive until it has forged res-2.json as well, exactly the chain of the
// review (the production shim kills the harness within 50 ms of a forged result, so there the chain is a race that the
// harness may win only partly; the test shim makes it deterministic). The test shim's own res-1.json, written when the
// harness exits, carries foreign_request as the production shim's does once it absorbed req-2.
func s6bForgedRun(t *testing.T) *gitRun {
	t.Helper()
	fx := buildFixture(t, false)
	pkt := gitPacket(t, fx, pktOpts{policy: map[string]any{"validation_commands": probeValidation()}})
	pkt["execution"].(map[string]any)["deadline_at"] = clock.Real{}.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339)
	validate(t, "worker-input", pkt)
	ops := []harnessOp{opWrite("calc/add_ac_test.go", addACTestSrc), {Op: "write-claim"}, {Op: "forge-res-live"}}
	return runGit(t, gitSpec{fx: fx, pkt: pkt, mode: "ops", ops: ops, realClock: true,
		shimResExtraByN: map[string]map[string]any{"1": {"foreign_request": true}}})
}

// S6a-r3 F1 (security, residual N3'): a live harness forges res-1.json, sc-worker reads it and sends req-2; req-2 is
// absorbed as foreign (never runs) and the harness forges res-2.json as well. The shim's later real res-1.json
// (foreign_request) must still be noticed: before commit and push sc-worker re-reads every result it consumed (after a
// settle bound) and fails closed: failed/internal workspace_tampered, nothing pushed.
func TestRun_ForgedResultsWhileHarnessAliveAreTampering_S6aR3F1(t *testing.T) {
	t.Parallel()
	gr := s6bForgedRun(t)
	res := gr.finish(t)
	if detail := wantFailedResult(t, res, "internal"); detail != "workspace_tampered" {
		t.Errorf("detail = %q, want workspace_tampered (res-1 and res-2 were forged by the live harness)", detail)
	}
	noBranchPushed(t, gr)
}

// S6a-r3 F2 (sc-worker side): the harness makes .sc-shim unreadable (mode 0300) and plants req-99.json. After the
// harness sc-worker checks that .sc-shim is still a directory with mode 0770: failed/internal workspace_tampered,
// nothing pushed.
func TestRun_ShimDirModeChangedByHarnessIsTampering_S6aR3F2(t *testing.T) {
	t.Parallel()
	ops := []harnessOp{opWrite("calc/add_ac_test.go", addACTestSrc), {Op: "chmod", Path: "@shim@", Content: "0300"},
		opWrite("@shim@/req-99.json", "{}")}
	gr := s4Run(t, s4Opts{mode: "ops", ops: ops, validation: probeValidation()})
	t.Cleanup(func() { _ = os.Chmod(gr.shimDir(), 0o755) })
	res := gr.finishWithin(t, 90*time.Second, "the result")
	if detail := wantFailedResult(t, res, "internal"); detail != "workspace_tampered" {
		t.Errorf("detail = %q, want workspace_tampered (.sc-shim is mode 0300)", detail)
	}
	noBranchPushed(t, gr)
}

// S6a-r3 F2 (shim side): when the shim cannot list its directory (ReadDir fails) it cannot know what was planted, so the
// result of the job it finishes carries foreign_request (or sweep_incomplete).
func TestShim_UnreadableDirectoryCountsAsForeignRequest_S6aR3F2(t *testing.T) {
	t.Parallel()
	ops := []harnessOp{opWrite("calc/add_ac_test.go", addACTestSrc), {Op: "chmod", Path: "@shim@", Content: "0300"},
		opWrite("@shim@/req-99.json", "{}"), {Op: "pause", N: 800}}
	gr := s4Run(t, s4Opts{mode: "ops", ops: ops})
	t.Cleanup(func() { _ = os.Chmod(gr.shimDir(), 0o755) })
	waitUntil(t, 60*time.Second, "res-1.json", func() bool {
		_, err := os.Stat(filepath.Join(gr.shimDir(), "res-1.json"))
		return err == nil
	})
	res1 := gr.shimJSON(t, "res-1.json")
	if res1["foreign_request"] != true && res1["sweep_incomplete"] != true {
		t.Errorf("res-1.json = %v, want foreign_request or sweep_incomplete true (the directory was unreadable)", res1)
	}
	gr.finishWithin(t, 90*time.Second, "the result")
}

// S6a-r3 F3: a res-2.json.tmp (symlink or regular file) planted before sc-worker writes req-2.json is tampering, found
// at once (not after the validation timeout), and the shim never writes through the planted link.
func TestRun_PreplantedResultTmpIsTampering_S6aR3F3(t *testing.T) {
	for name, plant := range map[string]harnessOp{
		"symlink": {Op: "symlink", Path: "@shim@/res-2.json.tmp", Content: "@out@/victim"},
		"regular": opWrite("@shim@/res-2.json.tmp", "stale"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ops := []harnessOp{opWrite("@out@/victim", "orig"), opWrite("calc/add_ac_test.go", addACTestSrc), plant}
			validation := []any{map[string]any{"name": "probe", "argv": []any{"sc-probe", "--code", "0"}, "timeout_seconds": 600}}
			gr := s4Run(t, s4Opts{mode: "ops", ops: ops, validation: validation})
			res := gr.finishWithin(t, 60*time.Second, "the result (well under the validation timeout)")
			if detail := wantFailedResult(t, res, "internal"); detail != "workspace_tampered" {
				t.Errorf("detail = %q, want workspace_tampered", detail)
			}
			if got := gr.outFile("victim"); got != "orig" {
				t.Errorf("out/victim = %q, want orig (the shim wrote through the planted res-2.json.tmp)", got)
			}
			noBranchPushed(t, gr)
		})
	}
}

// runKnobRow runs one scenario through the real pipeline (real fake harness) and returns the fixture, the run, the
// posted result and the red commit (implementer rows).
func runKnobRow(t *testing.T, r scenarioRow, prep func(fx *fixture)) (*fixture, *gitRun, map[string]any, string) {
	t.Helper()
	rowSem <- struct{}{}
	defer func() { <-rowSem }()
	if r.role == "" {
		r.role = "tester"
	}
	fx := buildFixture(t, false)
	if prep != nil {
		prep(fx)
	}
	pkt, red := buildRowPacket(t, fx, r)
	gr := runGit(t, gitSpec{fx: fx, pkt: pkt})
	return fx, gr, gr.finish(t), red
}

// 28-R-5 / plan 28 14.4 not-descendant (knob parent_override: base): sc-worker commits on top of base_commit instead of
// the red commit and pushes with the normal lease, so the remote branch moves to a commit whose parent is base_commit.
func TestKnob_ParentOverrideBase_S6bN7(t *testing.T) {
	t.Parallel()
	fx, _, res, red := runKnobRow(t, scenarioRow{scenario: "not-descendant", role: "implementer"}, nil)
	if res["state"] != "completed" || res["pushed"] != true {
		t.Fatalf("state %v pushed %v (failure %v), want completed and pushed", res["state"], res["pushed"], res["failure"])
	}
	head := fx.head("agent/sc-1")
	if head == "" || head == red {
		t.Fatalf("remote agent/sc-1 = %q (red %s), want it moved to the new commit", head, red)
	}
	if c := resStr(res, "commit"); c != head {
		t.Errorf("result commit = %q, want the remote head %s", c, head)
	}
	if parent := gitIn(t, fx.bare, "rev-parse", head+"^"); parent != fx.base {
		t.Errorf("parent of the pushed commit = %s, want base_commit %s (not the red commit %s)", parent, fx.base, red)
	}
}

// Plan 28 14.4 wrong-commit-claim (knob claim_commit_override: start): normal commit and push, but the result's commit is
// start_commit. Tester (start = base) and implementer (start = the red commit).
func TestKnob_ClaimCommitOverrideStart_S6bN7(t *testing.T) {
	for _, role := range []string{"tester", "implementer"} {
		t.Run(role, func(t *testing.T) {
			t.Parallel()
			fx, _, res, red := runKnobRow(t, scenarioRow{scenario: "wrong-commit-claim", role: role}, nil)
			start := fx.base
			if role == "implementer" {
				start = red
			}
			if res["state"] != "completed" || res["pushed"] != true {
				t.Fatalf("state %v pushed %v (failure %v), want completed and pushed", res["state"], res["pushed"], res["failure"])
			}
			head := fx.head("agent/sc-1")
			if head == "" || head == start {
				t.Fatalf("remote agent/sc-1 = %q (start %s), want the normally pushed new commit", head, start)
			}
			if parent := gitIn(t, fx.bare, "rev-parse", head+"^"); parent != start {
				t.Errorf("parent of the pushed commit = %s, want start_commit %s", parent, start)
			}
			if c := resStr(res, "commit"); c != start {
				t.Errorf("result commit = %q, want start_commit %s (claim_commit_override: start)", c, start)
			}
		})
	}
}

// Plan 28 14.4 push-other-branch (knob push_extra_refs): sc-worker first tries to push HEAD to refs/heads/main (the remote
// rejects it, here by a pre-receive hook standing for branch protection; event fake_push_main_rejected), then pushes
// refs/heads/scratch/sc-fake and the tag sc-fake-tag, then behaves like success.
func TestKnob_PushExtraRefs_S6bN7(t *testing.T) {
	t.Parallel()
	hook := "#!/bin/sh\nwhile read old new ref; do\n\tif [ \"$ref\" = refs/heads/main ]; then echo 'protected branch' >&2; exit 1; fi\ndone\nexit 0\n"
	fx, gr, res, _ := runKnobRow(t, scenarioRow{scenario: "push-other-branch"}, func(fx *fixture) {
		writeTestFile(t, filepath.Join(fx.bare, "hooks", "pre-receive"), hook, 0o755)
	})
	if res["state"] != "completed" || res["pushed"] != true {
		t.Fatalf("state %v pushed %v (failure %v), want completed and pushed (behaves like success)", res["state"], res["pushed"], res["failure"])
	}
	if got := fx.head("main"); got != fx.base {
		t.Errorf("main = %q, want unchanged %s", got, fx.base)
	}
	head := fx.head("agent/sc-1")
	if head == "" || resStr(res, "commit") != head {
		t.Errorf("agent/sc-1 = %q, result commit %q, want the same pushed commit", head, resStr(res, "commit"))
	}
	if got := fx.head("scratch/sc-fake"); got == "" {
		t.Errorf("refs/heads/scratch/sc-fake was not pushed:\n%s", fx.refList())
	}
	if out, err := gitTry(fx.bare, nil, "rev-parse", "--verify", "-q", "refs/tags/sc-fake-tag"); err != nil || out == "" {
		t.Errorf("tag sc-fake-tag was not pushed:\n%s", fx.refList())
	}
	if gr.event("fake_push_main_rejected") == nil {
		t.Errorf("no fake_push_main_rejected event on stdout")
	}
}

// Guard (passes already): without the knob (scenario success) nothing but agent/sc-1 is pushed.
func TestKnob_NoExtraRefsWithoutPushExtraRefs_S6bN7(t *testing.T) {
	t.Parallel()
	fx, _, res, _ := runKnobRow(t, scenarioRow{scenario: "success"}, nil)
	if res["state"] != "completed" {
		t.Fatalf("state %v, want completed", res["state"])
	}
	if got := gitIn(t, fx.bare, "for-each-ref", "--format=%(refname)"); got != "refs/heads/agent/sc-1\nrefs/heads/main" {
		t.Errorf("remote refs:\n%s\nwant only refs/heads/agent/sc-1 and refs/heads/main", got)
	}
}

// Plan 28 14.4 multi-commit (knob skip_fold): the harness makes two local commits; sc-worker does not fold them and pushes
// both: start_commit..head is exactly the two harness commits, and no harness_commits_folded event is logged.
func TestKnob_SkipFold_S6bN7(t *testing.T) {
	t.Parallel()
	fx, gr, res, red := runKnobRow(t, scenarioRow{scenario: "multi-commit", role: "implementer"}, nil)
	if res["state"] != "completed" || res["pushed"] != true {
		t.Fatalf("state %v pushed %v (failure %v), want completed and pushed", res["state"], res["pushed"], res["failure"])
	}
	head := fx.head("agent/sc-1")
	if head == "" || resStr(res, "commit") != head {
		t.Fatalf("agent/sc-1 = %q, result commit %q, want the same pushed commit", head, resStr(res, "commit"))
	}
	if n := gitIn(t, fx.bare, "rev-list", "--count", red+".."+head); n != "2" {
		t.Errorf("%s commits on top of start_commit, want the 2 unfolded harness commits", n)
	}
	if ev := gr.event("harness_commits_folded"); ev != nil {
		t.Errorf("event harness_commits_folded = %v, want none (skip_fold)", ev)
	}
}
