package worker_test

import (
	"strings"
	"testing"
	"time"
)

// Slice S6c review round 1 (27-AC-12, plan 27 section 14.10).
//
// B1: the path-name scan skipped the `diff --git` line of the entry that follows a header-only entry (empty file,
// mode-only change, pure rename), because the header state was only left at a hunk. A secret in such a path was pushed.
// N3: when the aggregate patch is beyond the 16 MiB output cap and max_diff_bytes is smaller than the real size, the
// failure names the max_diff_bytes rule (27-AC-12), not the generic diff_too_large.
// N4: skip_fold fails closed on a commit object beyond the output cap.

// 27-AC-12 / B1(a), folded: two empty files; the secret-named one sorts after a.txt.
func TestSecretScan_PathAfterEmptyFileEntry_S6cR1B1a(t *testing.T) {
	t.Parallel()
	secret := s6cSecret()
	ops := []harnessOp{opWrite("calc/a.txt", ""), opWrite("calc/"+secret+".txt", "")}
	gr, before := s6cFoldedRun(t, nil, ops)
	res := gr.finish(t)
	wantFailedResult(t, res, "secret_detected")
	assertNothingLeaked(t, gr, before, secret)
}

// 27-AC-12 / B1(b), folded: a mode-only change (chmod +x) followed by an empty secret-named file.
func TestSecretScan_PathAfterModeOnlyEntry_S6cR1B1b(t *testing.T) {
	t.Parallel()
	secret := s6cSecret()
	ops := []harnessOp{
		{Op: "chmod", Path: "calc/calc_sub_test.go", Content: "0755"},
		opWrite("calc/"+secret+".txt", ""),
	}
	gr, before := s6cFoldedRun(t, nil, ops)
	res := gr.finish(t)
	wantFailedResult(t, res, "secret_detected")
	assertNothingLeaked(t, gr, before, secret)
}

// 27-AC-12 / B1(c), skip_fold: the same as (a) in an intermediate commit that a later commit cleans up.
func TestKnob_SkipFoldScansPathAfterEmptyFileEntry_S6cR1B1c(t *testing.T) {
	t.Parallel()
	secret := s6cSecret()
	ops := []harnessOp{
		opWrite("calc/a.txt", ""), opWrite("calc/"+secret+".txt", ""), {Op: "commit", Msg: "first"},
		{Op: "delete", Path: "calc/a.txt"}, {Op: "delete", Path: "calc/" + secret + ".txt"},
		opWrite("calc/zz_ok.go", "package calc\n"), {Op: "commit", Msg: "second"},
	}
	gr, before := skipFoldRun(t, ops)
	res := gr.finish(t)
	wantFailedResult(t, res, "secret_detected")
	assertNothingLeaked(t, gr, before, secret)
}

// 27-AC-12 / N3: a >16 MiB patch with max_diff_bytes (1 MiB) below its real size names the max_diff_bytes rule.
func TestPrecheck_MaxDiffBytesNamedBeyondOutputCap_S6cR1N3(t *testing.T) {
	t.Parallel()
	ops := []harnessOp{
		{Op: "write-fill", Path: "calc/big.txt", Content: strings.Repeat("x", 1023) + "\n", N: 17 * 1024, Msg: "end\n"},
	}
	gr, _ := s6cFoldedRun(t, map[string]any{"max_diff_bytes": 1 << 20}, ops)
	res := gr.finishWithin(t, 150*time.Second, "start (a >16 MiB diff is read)")
	detail := wantFailedResult(t, res, "diff_precheck_failed")
	if !strings.Contains(detail, "max_diff_bytes") {
		t.Errorf("detail %q does not name the rule max_diff_bytes", detail)
	}
}

// 27-AC-12 / N4, skip_fold: a commit message beyond the output cap cannot be examined: fail closed.
func TestKnob_SkipFoldFailsClosedOnCommitObjectBeyondOutputCap_S6cR1N4(t *testing.T) {
	t.Parallel()
	ops := []harnessOp{
		opWrite("calc/ok.go", "package calc\n"),
		{Op: "commit-fill", Content: strings.Repeat("m", 1023) + "\n", N: 17 * 1024},
		opWrite("calc/ok2.go", "package calc\n"), {Op: "commit", Msg: "second"},
	}
	gr, _ := skipFoldRun(t, ops)
	res := gr.finishWithin(t, 150*time.Second, "start (a >16 MiB commit object is read)")
	wantFailedResult(t, res, "diff_precheck_failed")
}
