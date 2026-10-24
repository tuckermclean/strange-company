package worker_test

import (
	"strings"
	"testing"
	"time"
)

// Slice S6c (27-AC-12, plan 27 section 14.10 "secret scan -> no push"): two follow-ups of the S6b review-3.
//
// F2: the scan examined added lines only, so a harness that put a secret into a PATH NAME (new file, modified file,
// rename or copy target) got it pushed. Path names are scanned too; a hit is failed/secret_detected, nothing is pushed
// and the secret is absent from the posted result, stdout (events included).
//
// F1: git output is capped at 16 MiB (gitOutCap); a truncated patch or commit object must fail closed. Reading: an
// oversized change that cannot be examined is a diff pre-check failure (the reason the code uses for diffs it refuses:
// diff_precheck_failed), not a silent pass. The tests do not require a particular detail.
//
// The only helper change is the additive double op write-fill in component_git_support_test.go (a file of N copies of
// Content followed by Msg), because a >16 MiB file cannot travel through the SC_TEST_HARNESS_OPS environment variable.

func s6cSecret() string { return "sk-" + "ant-" + strings.Repeat("Qw7_-", 8) }

func s6cFoldedRun(t *testing.T, extra map[string]any, ops []harnessOp) (*gitRun, string) {
	t.Helper()
	fx := buildFixture(t, false)
	before := fx.refsDigest()
	pol := map[string]any{"diff_mode": "denylist", "diff_allow": []any{}, "diff_deny": []any{}}
	for k, v := range extra {
		pol[k] = v
	}
	return runGit(t, gitSpec{fx: fx, mode: "ops", ops: ops, pkt: gitPacket(t, fx, pktOpts{policy: pol})}), before
}

// 27-AC-12 / F2: a new file whose path holds the secret (content clean), folded path.
func TestSecretScan_SecretInAddedFilePath_S6cF2(t *testing.T) {
	t.Parallel()
	secret := s6cSecret()
	gr, before := s6cFoldedRun(t, nil, []harnessOp{opWrite("calc/"+secret+".txt", "harmless\n")})
	res := gr.finish(t)
	wantFailedResult(t, res, "secret_detected")
	assertNothingLeaked(t, gr, before, secret)
}

// 27-AC-12 / F2: a rename whose target path holds the secret (pure rename: the patch has no added lines and no +++ line).
func TestSecretScan_SecretInRenameTargetPath_S6cF2(t *testing.T) {
	t.Parallel()
	secret := s6cSecret()
	ops := []harnessOp{{Op: "git", Args: []string{"mv", "calc/calc_sub_test.go", "calc/" + secret + "_test.go"}}}
	gr, before := s6cFoldedRun(t, nil, ops)
	res := gr.finish(t)
	wantFailedResult(t, res, "secret_detected")
	assertNothingLeaked(t, gr, before, secret)
}

// 27-AC-12 / F2, skip_fold: commit 1 adds a file whose path holds the secret, commit 2 deletes it; the net diff is clean
// but commit 1 (and its tree) would be published.
func TestKnob_SkipFoldScansIntermediatePathNamesForSecrets_S6cF2(t *testing.T) {
	t.Parallel()
	secret := s6cSecret()
	p := "calc/" + secret + ".txt"
	ops := []harnessOp{
		opWrite(p, "harmless\n"), {Op: "commit", Msg: "first"},
		{Op: "delete", Path: p}, opWrite("calc/ok.go", "package calc\n"), {Op: "commit", Msg: "second"},
	}
	gr, before := skipFoldRun(t, ops)
	res := gr.finish(t)
	wantFailedResult(t, res, "secret_detected")
	assertNothingLeaked(t, gr, before, secret)
}

// 27-AC-12 / F1, skip_fold: commit 1 adds a >16 MiB file whose secret lies past the output cap, commit 2 deletes it.
func TestKnob_SkipFoldFailsClosedOnCommitBeyondOutputCap_S6cF1(t *testing.T) {
	t.Parallel()
	secret := s6cSecret()
	ops := []harnessOp{
		{Op: "write-fill", Path: "calc/big.txt", Content: strings.Repeat("x", 1023) + "\n", N: 17 * 1024, Msg: "// " + secret + "\n"},
		{Op: "commit", Msg: "first"},
		{Op: "delete", Path: "calc/big.txt"}, opWrite("calc/ok.go", "package calc\n"), {Op: "commit", Msg: "second"},
	}
	gr, before := skipFoldRun(t, ops)
	res := gr.finishWithin(t, 150*time.Second, "start (a >16 MiB diff is read and scanned)")
	wantFailedResult(t, res, "diff_precheck_failed")
	assertNothingLeaked(t, gr, before, secret)
}

// 27-AC-12 / F1, folded: max_diff_bytes above the cap (the schema has no maximum) and a >16 MiB diff with the secret past
// the cap must not pass the scan on the truncated patch.
func TestSecretScan_FailsClosedOnDiffBeyondOutputCap_S6cF1(t *testing.T) {
	t.Parallel()
	secret := s6cSecret()
	ops := []harnessOp{
		{Op: "write-fill", Path: "calc/big.txt", Content: strings.Repeat("x", 1023) + "\n", N: 17 * 1024, Msg: "// " + secret + "\n"},
	}
	gr, before := s6cFoldedRun(t, map[string]any{"max_diff_bytes": 1 << 30}, ops)
	res := gr.finishWithin(t, 150*time.Second, "start (a >16 MiB diff is read and scanned)")
	wantFailedResult(t, res, "diff_precheck_failed")
	assertNothingLeaked(t, gr, before, secret)
}
