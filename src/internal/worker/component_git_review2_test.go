package worker_test

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// partialDiffBlock returns the text between the PARTIAL-DIFF markers on stdout.
func partialDiffBlock(t *testing.T, stdout string) string {
	t.Helper()
	b := strings.Index(stdout, "::SC-PARTIAL-DIFF-BEGIN::\n")
	e := strings.Index(stdout, "::SC-PARTIAL-DIFF-END::")
	if b < 0 || e < b {
		t.Fatalf("no PARTIAL-DIFF block on stdout (begin %d, end %d)", b, e)
	}
	return stdout[b+len("::SC-PARTIAL-DIFF-BEGIN::\n") : e]
}

// S5a review B1 (27-AC-12, plan 27 section 14.13 "binary-free patch"): the partial diff of an unpushed run carries
// the textual hunk but no binary bytes, although the secret scan reads the patch with --text.
func TestPartialDiff_BinaryFree_27AC12(t *testing.T) {
	fx := buildFixture(t, false)
	pol := map[string]any{"diff_mode": "denylist", "diff_allow": []any{}, "diff_deny": []any{}}
	ops := []harnessOp{
		opWrite("AGENTS.md", agentsMDText+"TEXTHUNK-MARKER protected edit.\n"),
		{Op: "write", Path: ".gitattributes", Content: "*.dat -diff\n*.blob binary\n"},
		{Op: "write-hex", Path: "calc/data.bin", Content: "00010200fefffeff00c328a0a1c0af00"},
		{Op: "write-hex", Path: "calc/hidden.dat", Content: "68656c6c6ffefffeffc328a0a1c0af0a"},
		{Op: "write-hex", Path: "calc/raw.blob", Content: "fefffeffc328a0a1c0af"},
	}
	gr := runGit(t, gitSpec{fx: fx, mode: "ops", ops: ops, pkt: gitPacket(t, fx, pktOpts{policy: pol})})
	res := gr.finish(t)
	wantFailedResult(t, res, "diff_precheck_failed")
	blk := partialDiffBlock(t, gr.proc.stdout.String())
	if !strings.Contains(blk, "TEXTHUNK-MARKER") {
		t.Errorf("the PARTIAL-DIFF block lost the textual hunk:\n%q", blk)
	}
	if strings.ContainsRune(blk, 0) {
		t.Errorf("the PARTIAL-DIFF block contains a NUL byte:\n%q", blk)
	}
	if !utf8.ValidString(blk) || strings.ContainsAny(blk, "\xfe\xff\xc3\xa0\xc0") {
		t.Errorf("the PARTIAL-DIFF block contains binary marker bytes:\n%q", blk)
	}
}

// S5a review N2a (27-AC-9): the workspace branch ref lives only in packed-refs; the harness commits are still
// folded and counted.
func TestHarnessHead_PackedRefsFolded_27AC9(t *testing.T) {
	fx := buildFixture(t, false)
	ops := []harnessOp{
		opWrite("calc/t1_test.go", testSrc("TestOne")), {Op: "commit", Msg: "first"},
		opWrite("calc/t2_test.go", testSrc("TestTwo")), {Op: "commit", Msg: "second"},
		{Op: "git", Args: []string{"pack-refs", "--all"}},
	}
	gr := runGit(t, gitSpec{fx: fx, mode: "ops", ops: ops, pkt: gitPacket(t, fx, pktOpts{})})
	res := gr.finish(t)
	if res["state"] != "completed" || res["pushed"] != true {
		t.Fatalf("state = %v, pushed = %v, want completed and pushed; failure %v", res["state"], res["pushed"], res["failure"])
	}
	commit := resStr(res, "commit")
	if commit == "" || fx.head("agent/sc-1") != commit {
		t.Fatalf("result commit %q, remote agent/sc-1 %q, want the same commit", commit, fx.head("agent/sc-1"))
	}
	if n := gitIn(t, fx.bare, "rev-list", "--count", fx.base+".."+commit); n != "1" {
		t.Errorf("%s commits on top of start_commit, want 1", n)
	}
	ev := gr.event("harness_commits_folded")
	if ev == nil {
		t.Fatalf("no harness_commits_folded event on stdout:\n%s", gr.proc.stdout.String())
	}
	if ev["count"] != float64(2) {
		t.Errorf("harness_commits_folded count = %v, want 2", ev["count"])
	}
}

// S5a review N2b (27-AC-12, plan 27 section 14.10): a harness head that is not a descendant of start_commit fails
// with head_not_descendant and pushes nothing, with the ref loose or only in packed-refs.
func TestHarnessHead_NotDescendant_27AC12(t *testing.T) {
	for name, pack := range map[string]bool{"loose_ref": false, "packed_ref": true} {
		t.Run(name, func(t *testing.T) {
			fx := buildFixture(t, false)
			before := fx.refsDigest()
			ops := []harnessOp{
				opWrite("calc/t1_test.go", testSrc("TestOne")),
				{Op: "git", Args: []string{"checkout", "-q", "--orphan", "unrelated"}},
				{Op: "commit", Msg: "orphan"},
			}
			if pack {
				ops = append(ops, harnessOp{Op: "git", Args: []string{"pack-refs", "--all"}})
			}
			gr := runGit(t, gitSpec{fx: fx, mode: "ops", ops: ops, pkt: gitPacket(t, fx, pktOpts{})})
			res := gr.finish(t)
			detail := wantFailedResult(t, res, "diff_precheck_failed")
			if !strings.Contains(detail, "head_not_descendant") {
				t.Errorf("detail %q does not contain head_not_descendant", detail)
			}
			if got := fx.refsDigest(); got != before {
				t.Errorf("remote refs changed:\n%s", fx.refList())
			}
		})
	}
}
