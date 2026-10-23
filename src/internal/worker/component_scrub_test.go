package worker_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"strange.company/sc/internal/clock"
)

// Slice S5b of F07: scrubbing through the real shim and framer (27-AC-4), and the review follow-ups S4-N5, S4-N8,
// S5a-r2 #1/#2/#4, S4-NB-a and S4-NB-b. The shim is the production worker.Shim as a process (component_shim_test.go).

const (
	gwCanary     = "gw-CANARY-0002-gateway-token-value"
	egressCanary = "eg-CANARY-0003-egress-token-value"
	// A base64 body line of the deploy key (the scrubber's exact set holds every body line of at least 20 chars).
	deployCanary = "QyNTUxOQAAACBGQUtFS0VZRk9SVEVTVFNPTkxZMDEyMzQ1Njc4OWFiY2RlZgAAAJhGQUtF"
	knownHostsCn = forgejoHost + " ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFAKEHOSTKEYFORTESTSONLY"
)

func nilContext() context.Context { return context.Background() }

func skAntKey() string { return "sk-" + "ant-" + strings.Repeat("Zq9_-", 8) }

const otherPEM = "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEAzSCRUBPEMBODYLINEONE0123456789abcdef\nMIIEowIBAAKCAQEAzSCRUBPEMBODYLINETWO0123456789abcdef\n-----END RSA PRIVATE KEY-----"

// fragments returns every window of n bytes of s.
func fragments(s string, n int) []string {
	var out []string
	for i := 0; i+n <= len(s); i++ {
		out = append(out, s[i:i+n])
	}
	return out
}

// noRawControls reports C0 control characters other than tab and newline (and DEL) in text.
func noRawControls(t *testing.T, what, text string) {
	t.Helper()
	for i := 0; i < len(text); i++ {
		if c := text[i]; (c < 0x20 && c != '\t' && c != '\n') || c == 0x7f {
			lo := max(0, i-30)
			t.Errorf("%s carries the raw control byte 0x%02x near %q", what, c, text[lo:min(len(text), i+30)])
			return
		}
	}
}

// 27-AC-4 (plan 27 sections 13.4 and 14.8): canary values of all four secret files and of the egress token reach a
// harness that prints them, a PEM block and an sk-ant- string, and the claim carries them in the summary and the
// handoff. No canary is in sc-worker's stdout or in the posted result body; scrub.redactions is at least 6. The
// stream passes the production shim and framer.
func TestScrub_CanariesNeverReachStdoutOrResult_27AC4(t *testing.T) {
	t.Parallel()
	sk := skAntKey()
	canaries := map[string]string{
		"task-token": taskToken, "gateway-token": gwCanary, "deploy-key": deployCanary, "known_hosts": knownHostsCn,
		"egress-token": egressCanary, "pem-body": "MIIEowIBAAKCAQEAzSCRUBPEMBODYLINEONE0123456789abcdef", "sk-ant": sk,
	}
	hand, _ := json.Marshal(map[string]any{"what_i_did": "saw " + gwCanary, "what_remains": "left " + egressCanary,
		"known_risks": []any{"risk " + deployCanary, "risk " + knownHostsCn}})
	ops := []harnessOp{opWrite("calc/add_ac_test.go", addACTestSrc)}
	// The unknown PEM block is printed on one line with JSON-escaped newlines (plan 27 section 13.4 pattern set).
	for _, c := range []string{taskToken, gwCanary, deployCanary, knownHostsCn, egressCanary, sk, deployKeyBody, strings.ReplaceAll(otherPEM, "\n", `\n`)} {
		ops = append(ops, harnessOp{Op: "print", Content: "harness says " + c})
	}
	gr := s4Run(t, s4Opts{mode: "ops", ops: ops, env: map[string]string{
		"SC_TEST_HARNESS_SUMMARY": "covers AC-1 " + taskToken + " " + sk, "SC_TEST_HARNESS_HANDOFF": string(hand)}})
	res := gr.finishWithin(t, 60*time.Second, "start")
	posts := gr.stub.calls("POST", "/v1/worker/result")
	if len(posts) != 1 {
		t.Fatalf("%d result POSTs, want 1", len(posts))
	}
	stdout, body := gr.proc.stdout.String(), string(posts[0].Body)
	for name, c := range canaries {
		if n := strings.Count(stdout, c); n != 0 {
			t.Errorf("canary %s appears %d times in sc-worker stdout", name, n)
		}
		if n := strings.Count(body, c); n != 0 {
			t.Errorf("canary %s appears %d times in the posted result body", name, n)
		}
		if n := strings.Count(gr.proc.stderr.String(), c); n != 0 {
			t.Errorf("canary %s appears %d times in sc-worker stderr", name, n)
		}
	}
	sc, _ := res["scrub"].(map[string]any)
	if n, _ := sc["redactions"].(float64); n < 6 {
		t.Errorf("scrub.redactions = %v, want >= 6", sc["redactions"])
	}
	parseFraming(t, stdout, true).checkHeaders(t)
}

// S4-N5a: the shim splits a long output line at 1 MiB; a secret that straddles the split must not leak its tail
// (the part after the split is a line of its own that the scrubber cannot match).
func TestShim_SecretStraddlingTheSplitDoesNotLeak_S4N5a(t *testing.T) {
	t.Parallel()
	const cut = 20 // bytes of the canary before the 1 MiB boundary
	ops := []harnessOp{opWrite("calc/add_ac_test.go", addACTestSrc), {Op: "straddle", N: 1<<20 - cut, Content: gwCanary, Args: []string{"200000"}}}
	gr := s4Run(t, s4Opts{mode: "ops", ops: ops})
	gr.finishWithin(t, 60*time.Second, "start")
	stdout := gr.proc.stdout.String()
	for _, f := range fragments(gwCanary[cut:], 8) { // every 8-byte window inside the part after the split
		if strings.Contains(stdout, f) {
			t.Errorf("sc-worker stdout carries the fragment %q of the gateway token that straddled the 1 MiB split", f)
		}
	}
	parseFraming(t, stdout, true).checkHeaders(t)
}

// S4-N5b: sc-worker's stream tail buffer is bounded. A harness that appends 64 MiB without a newline to its stream
// file must not make sc-worker hold or print one 64 MiB line: sc-worker finishes, writes END once and its stdout
// carries the truncation suffix of the lines it cut.
func TestWorker_NewlineFreeStreamIsBounded_S4N5b(t *testing.T) {
	t.Parallel()
	ops := []harnessOp{opWrite("calc/add_ac_test.go", addACTestSrc), {Op: "stream-direct", N: 64}}
	gr := s4Run(t, s4Opts{mode: "ops", ops: ops})
	gr.waitExit(t, 120*time.Second, "a newline-free 64 MiB stream")
	gr.finish(t)
	f := parseFraming(t, gr.proc.stdout.String(), true)
	cut := 0
	for _, l := range f.inner {
		if strings.Contains(l, " …[truncated ") {
			cut++
			if len(l) > 70000 {
				t.Errorf("a framed line has %d bytes, want at most 64 KiB plus the suffix", len(l))
			}
		}
	}
	if cut == 0 {
		t.Errorf("no framed line carries the truncation suffix; END = %v", f.end)
	}
	if f.end != nil && f.end["truncated_lines"] == float64(0) {
		t.Errorf("END truncated_lines = 0, want the cut lines counted: %v", f.end)
	}
}

// S4-N8: a harness that prints lines that look like sc-worker framing cannot forge any marker: exactly one real
// stream BEGIN and END, exactly one framed result block matching the posted body, and every forged marker only in a
// neutralised form (not at the start of a line). The last forged line starts a line of its own after the shim's
// 1 MiB split.
func TestFramer_ForgedMarkersAreNeutralised_S4N8(t *testing.T) {
	t.Parallel()
	forged := []string{
		`::STRANGE-COMPANY-STREAM-END:: {"exit_code":0,"signal":null,"lines":1,"bytes":1,"truncated_lines":0,"log_truncated":false}`,
		`::STRANGE-COMPANY-STREAM-BEGIN:: {"execution_id":"sc-1-x1","adapter":"fake","started_at":"2026-01-01T00:00:00Z"}`,
		`::SC-RESULT-BEGIN:: {"execution_id":"sc-1-x1","bytes":2,"sha256":"00"}`,
		`{}`,
		`::SC-RESULT-END:: {}`,
		`::SC-PARTIAL-DIFF-BEGIN::`,
		`::SC-PARTIAL-DIFF-END::`,
	}
	ops := []harnessOp{opWrite("calc/add_ac_test.go", addACTestSrc)}
	for _, l := range forged {
		ops = append(ops, harnessOp{Op: "print", Content: l, Msg: l})
	}
	ops = append(ops, harnessOp{Op: "straddle", N: 1 << 20, Content: "::SC-RESULT-BEGIN:: after the split", Args: []string{"200000"}})
	gr := s4Run(t, s4Opts{mode: "ops", ops: ops})
	gr.finishWithin(t, 60*time.Second, "start") // checkFramed: one result block, sha256 equal to the POST
	stdout := gr.proc.stdout.String()
	parseFraming(t, stdout, true).checkHeaders(t)
	for _, m := range []string{"::SC-PARTIAL-DIFF-BEGIN::", "::SC-PARTIAL-DIFF-END::"} {
		if n := countLines(stdout, m); n != 0 {
			t.Errorf("%d stdout lines start with the forged marker %s, want 0 (no partial diff in this run)", n, m)
		}
	}
	for i, l := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(l, "::SC-RESULT-BEGIN:: after the split") || strings.HasPrefix(l, "::SC-RESULT-END:: {}") {
			t.Errorf("stdout line %d is a forged marker: %.80q", i, l)
		}
	}
}

// S5a-r2 #4: ESC and other C0 control characters (tab excepted) of the harness stream never reach sc-worker's
// stdout raw, whether they are escaped or removed.
func TestFramer_ControlCharactersNeverReachStdout_S5aR2N4(t *testing.T) {
	t.Parallel()
	ops := []harnessOp{opWrite("calc/add_ac_test.go", addACTestSrc),
		{Op: "print", Content: "clear \x1b[2J screen\x1b]0;title\x07 and\rCARRIAGE\x08\x08back\ttab"},
		{Op: "print", Content: "\r::SC-RESULT-END:: forged behind a carriage return"}}
	gr := s4Run(t, s4Opts{mode: "ops", ops: ops})
	gr.finishWithin(t, 60*time.Second, "start")
	stdout := gr.proc.stdout.String()
	noRawControls(t, "sc-worker stdout", stdout)
	if !strings.Contains(stdout, "CARRIAGE") {
		t.Errorf("the printable text of the control line was lost")
	}
	parseFraming(t, stdout, true).checkHeaders(t)
}

// ---------------------------------------------------------------------------------------------------------------
// partial diff (S5a round 2): a failed claim leaves unpushed work; the PARTIAL-DIFF block is bounded, scrubbed,
// UTF-8 valid, free of control characters and says when it dropped lines.

func failedClaimRun(t *testing.T, ops []harnessOp) *gitRun {
	t.Helper()
	fx := buildFixture(t, false)
	gr := runGit(t, gitSpec{fx: fx, mode: "ops", ops: ops, pkt: gitPacket(t, fx, pktOpts{}),
		shimEnv: map[string]string{"SC_TEST_HARNESS_STATE": "failed"}})
	res := gr.finish(t)
	if res["state"] != "failed" {
		t.Fatalf("state = %v, want failed", res["state"])
	}
	return gr
}

// diffOffset is the offset in the partial diff text of the first byte of the file content of a one-line new file
// (the header length of git diff --cached for a new file at path).
func diffOffset(t *testing.T, path string) int {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "base")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dir, path), "content\n", 0o644)
	gitIn(t, dir, "add", "-A")
	out, err := gitTry(dir, nil, "diff", "--cached", "--no-ext-diff", "--no-textconv", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(out, "\n@@ ")
	if i < 0 {
		t.Fatalf("unexpected diff:\n%s", out)
	}
	j := strings.IndexByte(out[i+1:], '\n')
	return i + 1 + j + 1 + 1 // after the hunk header line and the "+" of the content line
}

// S5a-r2 #1: the partial diff is cut at 1 MiB before it is scrubbed, so a secret that straddles the cut leaks its
// head. The scrubbing must see the whole text; no 8-byte fragment of the gateway token is in the block.
func TestPartialDiff_SecretStraddlingTheCutDoesNotLeak_S5aR2N1(t *testing.T) {
	t.Parallel()
	const keep = 20 // bytes of the canary inside the first MiB
	off := diffOffset(t, "calc/big.txt")
	ops := []harnessOp{{Op: "write-gen", Path: "calc/big.txt", N: 1<<20 - off - keep, Content: gwCanary, Args: []string{"600000", "a"}}}
	gr := failedClaimRun(t, ops)
	blk := partialDiffBlock(t, gr.proc.stdout.String())
	if len(blk) < 1<<19 {
		t.Fatalf("the PARTIAL-DIFF block has only %d bytes: the fixture did not reach the cut", len(blk))
	}
	for _, f := range fragments(gwCanary, 8) {
		if strings.Contains(blk, f) {
			t.Errorf("the PARTIAL-DIFF block carries the fragment %q of the gateway token cut at 1 MiB", f)
		}
	}
	if !strings.Contains(blk, "[diff truncated by sc-worker]") {
		t.Errorf("the truncated block lacks its truncation marker")
	}
}

// S5a-r2 #2: the cut is made on characters, not bytes: the block is valid UTF-8 although the 1 MiB boundary lands
// inside a three-byte rune.
func TestPartialDiff_CutKeepsUTF8Valid_S5aR2N2(t *testing.T) {
	t.Parallel()
	off := diffOffset(t, "calc/euro.txt")
	// The euro run starts 1001 bytes before the cut: 1001 mod 3 = 2, so the cut is inside a rune.
	ops := []harnessOp{{Op: "write-gen", Path: "calc/euro.txt", N: 1<<20 - off - 1001, Args: []string{"4000", "€"}}}
	gr := failedClaimRun(t, ops)
	blk := partialDiffBlock(t, gr.proc.stdout.String())
	if len(blk) < 1<<19 {
		t.Fatalf("the PARTIAL-DIFF block has only %d bytes: the fixture did not reach the cut", len(blk))
	}
	if !utf8.ValidString(blk) {
		t.Errorf("the PARTIAL-DIFF block is not valid UTF-8 (cut inside a rune); tail %q", blk[len(blk)-60:])
	}
	if !utf8.ValidString(gr.proc.stdout.String()) {
		t.Errorf("sc-worker stdout is not valid UTF-8")
	}
	if !strings.Contains(blk, "€€€€") {
		t.Errorf("the block lost the text before the cut")
	}
}

// S5a-r2 #3 (visible marker): when lines with NUL bytes or invalid UTF-8 are dropped the block says so, naming how
// many lines sc-worker dropped; a diff that drops nothing carries no such line.
func TestPartialDiff_DroppedLinesAreMarked_S5aR2(t *testing.T) {
	t.Parallel()
	ops := []harnessOp{
		opWrite("calc/text.txt", "TEXTHUNK-MARKER plain\n"),
		{Op: "write", Path: ".gitattributes", Content: "*.dat diff\n"},
		{Op: "write-hex", Path: "calc/hidden.dat", Content: "68656c6c6ffefffeffc328a0a1c0af0a"},
		{Op: "write-hex", Path: "calc/hidden2.dat", Content: "776f726c64c328a0a1c0af0a"},
	}
	gr := failedClaimRun(t, ops)
	blk := partialDiffBlock(t, gr.proc.stdout.String())
	if !strings.Contains(blk, "TEXTHUNK-MARKER") {
		t.Errorf("the block lost the textual hunk:\n%q", blk)
	}
	marked := false
	for _, l := range strings.Split(blk, "\n") {
		if strings.Contains(l, "dropped by sc-worker") {
			marked = true
			if !strings.ContainsAny(l, "0123456789") {
				t.Errorf("the marker line %q does not say how many lines were dropped", l)
			}
		}
	}
	if !marked {
		t.Errorf("the block drops non-text lines without a marker line containing 'dropped by sc-worker':\n%q", blk)
	}
}

// Guard (passes today): a clean text diff carries no drop marker.
func TestPartialDiff_NoDropNoMarker_S5aR2(t *testing.T) {
	t.Parallel()
	gr := failedClaimRun(t, []harnessOp{opWrite("calc/text.txt", "TEXTHUNK-MARKER plain\n")})
	blk := partialDiffBlock(t, gr.proc.stdout.String())
	if !strings.Contains(blk, "TEXTHUNK-MARKER") || strings.Contains(blk, "dropped by sc-worker") {
		t.Errorf("block = %q, want the hunk and no drop marker", blk)
	}
}

// S5a-r2 #4: ESC and other C0 control characters of a diff line never reach stdout raw.
func TestPartialDiff_ControlCharactersNeverReachStdout_S5aR2N4(t *testing.T) {
	t.Parallel()
	gr := failedClaimRun(t, []harnessOp{opWrite("calc/ansi.txt", "TEXTHUNK-MARKER \x1b[2J screen\x07 and\rCARRIAGE\x08back\ttab\n")})
	stdout := gr.proc.stdout.String()
	noRawControls(t, "sc-worker stdout", stdout)
	if !strings.Contains(partialDiffBlock(t, stdout), "TEXTHUNK-MARKER") {
		t.Errorf("the printable text of the diff line was lost")
	}
}

// ---------------------------------------------------------------------------------------------------------------
// shim kill races (S4 review)

// bareShimWithJob starts the production shim as a process (one scheduler thread, so that the goroutines of one
// loop pass interleave tightly) with a harness that ignores SIGTERM and leaves a SIGTERM-immune child, and a
// request with term_grace_seconds 30 (no sc-worker). It returns the running harness's process group.
func bareShimWithJob(t *testing.T) (*gitRun, int) {
	t.Helper()
	root := t.TempDir()
	dirs := map[string]string{}
	for _, d := range []string{"workspace", "harness-secrets", "home", "tmp"} {
		dirs[d] = filepath.Join(root, d)
		if err := os.MkdirAll(dirs[d], 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gr := &gitRun{ws: dirs["workspace"], dirs: dirs}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	gr.startRealShim(t, exe, dirs, "http://127.0.0.1:1", "ops", killDoubleOps(), map[string]string{"SC_TEST_SHIM_GOMAXPROCS": "1"})
	out := filepath.Join(dirs["workspace"], "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(gr.shimDir(), 0o750); err != nil {
		t.Fatal(err)
	}
	req, _ := json.Marshal(map[string]any{"id": "nb", "kind": "harness",
		"argv": []string{"sc-fake-harness", "--repo", dirs["workspace"], "--out", out, "--role", "implementer"},
		"env":  map[string]string{}, "cwd": dirs["workspace"], "timeout_seconds": 600, "term_grace_seconds": 30})
	tmp := filepath.Join(gr.shimDir(), "req-1.json.tmp")
	if err := os.WriteFile(tmp, req, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(gr.shimDir(), "req-1.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gr.shimDir(), "alive"), []byte("{}"), 0o640); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 45*time.Second, "the harness to be ready", func() bool { return gr.outFile("ready.txt") != "" })
	return gr, gr.harnessGroup(t)
}

// S4-NB-a: a container SIGTERM and `done` seen in the same shim loop pass must not lose the immediate kill of
// `done` (a non-blocking send into the one-slot termination buffer drops it while the SIGTERM request is still
// unconsumed). The harness ignores SIGTERM and term_grace_seconds is 30, so the group has to be dead within 2 s of
// `done`. Repeated, so that the race occurs; the outcome for a correct shim is always a pass.
func TestShim_SIGTERMAndDoneTogetherStillKillAtOnce_S4NBa(t *testing.T) {
	t.Parallel()
	for i := 0; i < 8; i++ {
		t.Run(fmt.Sprintf("round-%d", i), func(t *testing.T) {
			t.Parallel()
			gr, pgrp := bareShimWithJob(t)
			if err := gr.shimProc.cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(gr.shimDir(), "done"), []byte("{}"), 0o640); err != nil {
				t.Fatal(err)
			}
			t0 := clock.Real{}.Now()
			waitUntil2(t, 2*time.Second, func() bool { return len(survivors(pgrp)) == 0 }, func() {
				t.Fatalf("the group %d still lives %s after done (term_grace_seconds 30): %+v", pgrp, clock.Real{}.Now().Sub(t0), survivors(pgrp))
			})
		})
	}
}

// waitUntil2 polls cond for at most d and calls onTimeout when it did not become true.
func waitUntil2(t *testing.T, d time.Duration, cond func() bool, onTimeout func()) {
	t.Helper()
	deadline := clock.Real{}.Now().Add(d)
	for !cond() {
		if !(clock.Real{}).Now().Before(deadline) {
			onTimeout()
			return
		}
		_ = clock.Real{}.Sleep(nilContext(), 20*time.Millisecond)
	}
}

// S4-NB-b: a heartbeat cancel starts a 25 s grace in the shim; a pod SIGTERM to both containers 2 s later re-arms
// the kill timer to the 20 s cap, so the harness group is dead within 22 s of the SIGTERM (not 23 s), sc-worker
// writes one END, posts one result and exits 143 within 30 s.
func TestShim_PodSIGTERMAfterCancelRearmsKillTimer_S4NBb(t *testing.T) {
	t.Parallel()
	gr := s4Run(t, s4Opts{grace: 25, cancelAfterHB: 1, mode: "ops", ops: killDoubleOps()})
	waitUntil(t, 45*time.Second, "the harness to be ready", func() bool { return gr.outFile("ready.txt") != "" })
	pgrp := gr.harnessGroup(t)
	waitUntil(t, 90*time.Second, "the heartbeat cancel (SIGTERM of the harness)", func() bool { return gr.outFile("term.txt") != "" })
	_ = clock.Real{}.Sleep(nilContext(), 2*time.Second)
	t0 := clock.Real{}.Now()
	for _, p := range []*os.Process{gr.shimProc.cmd.Process, gr.proc.cmd.Process} {
		if err := p.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
	}
	waitUntil2(t, 22*time.Second, func() bool { return len(survivors(pgrp)) == 0 }, func() {
		t.Errorf("the harness group %d is still alive 22 s after the pod SIGTERM (re-armed cap 20 s): %+v", pgrp, survivors(pgrp))
	})
	left := 30*time.Second - clock.Real{}.Now().Sub(t0)
	if left < time.Second {
		left = time.Second
	}
	if code := gr.waitExit(t, left, "the pod SIGTERM"); code != 143 {
		t.Errorf("exit code = %d, want 143; stderr:\n%s", code, gr.proc.stderr.String())
	}
	gr.endShim()
	posts := gr.stub.calls("POST", "/v1/worker/result")
	if len(posts) != 1 {
		t.Fatalf("%d result POSTs, want 1", len(posts))
	}
	checkFramed(t, gr.proc.stdout.String(), posts[0].Body)
	parseFraming(t, gr.proc.stdout.String(), true).checkHeaders(t)
	expectNoSurvivors(t, pgrp)
}
