package worker_test

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

// Slice S5b review round 1: the stream drain is bounded (B1, 27-AC-3, plan 27 sections 14.8 and 14.14), the log
// budget is charged with the bytes written (N2), the 16 MiB git capture edge cannot leak half a secret (N3) and a
// lone C1 byte is never written raw (N5).

const drainBound = 30 * time.Second

func (gr *gitRun) waitResFile(t *testing.T) {
	t.Helper()
	waitUntil(t, 90*time.Second, "the shim result file res-1.json", func() bool {
		_, err := os.Stat(filepath.Join(gr.shimDir(), "res-1.json"))
		return err == nil
	})
}

// stopBgWriter kills the background writer's process group at the end of the test.
func (gr *gitRun) stopBgWriter(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if pid, err := strconv.Atoi(gr.outFile("bg.txt")); err == nil && pid > 1 {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	})
}

// B1 (a) and (c), 27-AC-3: a harness that sparsely extends its stream file to 1 TiB, or leaves a background writer
// that appends forever, and exits: sc-worker writes END once, posts one result and exits within 30 s of the
// harness's result file.
func TestDrain_HarnessCannotStallSCWorker_27AC3(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		op   string
	}{{"sparse-1TiB", "stream-sparse"}, {"background-writer", "bg-writer"}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ops := []harnessOp{opWrite("calc/add_ac_test.go", addACTestSrc), {Op: tc.op}}
			gr := s4Run(t, s4Opts{mode: "ops", ops: ops})
			gr.stopBgWriter(t)
			gr.waitResFile(t)
			gr.waitExit(t, drainBound, "the harness result file (the drain must be bounded)")
			gr.finish(t)
			parseFraming(t, gr.proc.stdout.String(), true)
		})
	}
}

// B1 (b), 27-AC-3 / plan 27 section 14.14: the stream file is 1 TiB sparse while the harness still runs; a pod
// SIGTERM must end sc-worker with exit 143 within 30 s, one END and one result.
func TestDrain_SIGTERMWhileStreamIsHuge_27AC3(t *testing.T) {
	t.Parallel()
	ops := []harnessOp{opWrite("calc/add_ac_test.go", addACTestSrc), {Op: "stream-sparse"}, {Op: "hang"}}
	gr := s4Run(t, s4Opts{mode: "ops", ops: ops})
	waitUntil(t, 60*time.Second, "the harness to be ready", func() bool { return gr.outFile("ready.txt") != "" })
	for _, p := range []*os.Process{gr.shimProc.cmd.Process, gr.proc.cmd.Process} {
		if err := p.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
	}
	if code := gr.waitExit(t, drainBound, "the pod SIGTERM"); code != 143 {
		t.Errorf("exit code = %d, want 143; stderr:\n%s", code, gr.proc.stderr.String())
	}
	gr.endShim()
	posts := gr.stub.calls("POST", "/v1/worker/result")
	if len(posts) != 1 {
		t.Fatalf("%d result POSTs, want 1", len(posts))
	}
	parseFraming(t, gr.proc.stdout.String(), true)
}

// harnessLineBytes sums len+1 of the harness stream lines between BEGIN and END (sc-worker's own JSON log lines
// excluded).
func harnessLineBytes(f framing) int {
	n := 0
	for _, l := range f.inner {
		if strings.HasPrefix(l, `{"src":"sc-worker"`) {
			continue
		}
		n += len(l) + 1
	}
	return n
}

// N2 (plan 27 section 14.8): max_log_bytes limits what is written, so control-heavy output (six bytes become 36
// after escaping) must not reach 6 times the budget. Bound: the harness lines on stdout total at most
// max_log_bytes (a line that would cross the budget is not written; each line here is at most 37 bytes).
func TestFramer_LogBudgetChargesEscapedBytes_27AC3(t *testing.T) {
	t.Parallel()
	const budget = 65536
	ops := []harnessOp{opWrite("calc/add_ac_test.go", addACTestSrc), {Op: "print", Content: "\x01\x02\x03\x04\x05\x06", N: 20000}}
	gr := s4Run(t, s4Opts{maxLog: budget, mode: "ops", ops: ops})
	gr.finishWithin(t, 60*time.Second, "start")
	f := parseFraming(t, gr.proc.stdout.String(), true)
	if got := harnessLineBytes(f); got > budget {
		t.Errorf("harness lines on stdout total %d bytes, want at most max_log_bytes %d", got, budget)
	}
	exhausted := 0
	for _, l := range f.inner {
		if strings.Contains(l, "log_budget_exhausted") {
			exhausted++
		}
	}
	if exhausted != 1 {
		t.Errorf("%d log_budget_exhausted lines, want 1", exhausted)
	}
	if f.end == nil || f.end["log_truncated"] != true {
		t.Errorf("END = %v, want log_truncated true", f.end)
	}
}

// noRawC1 fails when text carries an invalid UTF-8 byte or a C1 control character (U+0080 to U+009F).
func noRawC1(t *testing.T, what, text string) {
	t.Helper()
	if !utf8.ValidString(text) {
		for i := 0; i < len(text); {
			r, n := utf8.DecodeRuneInString(text[i:])
			if r == utf8.RuneError && n == 1 {
				t.Errorf("%s carries the raw invalid byte 0x%02x near %q", what, text[i], text[max(0, i-20):min(len(text), i+20)])
				break
			}
			i += n
		}
	}
	for _, r := range text {
		if r >= 0x80 && r <= 0x9f {
			t.Errorf("%s carries the raw C1 control U+%04X", what, r)
			return
		}
	}
}

// N5: a lone 0x9b or 0x85 byte (invalid UTF-8, a C1 control to some terminals) in a harness stream line or a partial
// diff line never reaches stdout raw.
func TestFramer_LoneC1BytesNeverReachStdout_S5bN5(t *testing.T) {
	t.Parallel()
	t.Run("stream", func(t *testing.T) {
		t.Parallel()
		ops := []harnessOp{opWrite("calc/add_ac_test.go", addACTestSrc),
			{Op: "print-hex", Content: hexOf("plain STREAMMARK \x9b[2J text \x85 end")}}
		gr := s4Run(t, s4Opts{mode: "ops", ops: ops})
		gr.finishWithin(t, 60*time.Second, "start")
		stdout := gr.proc.stdout.String()
		if !strings.Contains(stdout, "STREAMMARK") {
			t.Errorf("the printable text of the line was lost")
		}
		noRawC1(t, "sc-worker stdout", stdout)
	})
	t.Run("partial-diff", func(t *testing.T) {
		t.Parallel()
		gr := failedClaimRun(t, []harnessOp{
			{Op: "write", Path: ".gitattributes", Content: "*.dat diff\n"},
			{Op: "write-hex", Path: "calc/c1.dat", Content: hexOf("TEXTHUNK-MARKER \x9b[2J \x85 end\n")},
			opWrite("calc/text.txt", "TEXTHUNK-MARKER2 \u0085 valid c1\n")})
		stdout := gr.proc.stdout.String()
		if !strings.Contains(partialDiffBlock(t, stdout), "TEXTHUNK-MARKER2") {
			t.Errorf("the textual hunk was lost")
		}
		noRawC1(t, "sc-worker stdout", stdout)
	})
}

// N3: git output above the 16 MiB capture is cut; the unterminated last line at the cut edge cannot be scrubbed as a
// whole secret. A failed run whose partial diff starts with a 15.5 MiB dropped (NUL) line and ends with a line that
// carries the gateway token across the 16 MiB edge leaks no 8-byte fragment of it.
func TestPartialDiff_SecretAtCaptureEdgeDoesNotLeak_S5bN3(t *testing.T) {
	t.Parallel()
	const capEdge = 16 << 20
	const keep = 20               // bytes of the canary before the edge
	const bigN = 15<<20 + 512<<10 // a.dat: the dropped NUL line; the kept text before the edge stays below 1 MiB
	bigDat := func() []byte {
		b := make([]byte, bigN+2)
		for i := range b {
			b[i] = 'a'
		}
		b[len(b)-2] = 0
		b[len(b)-1] = '\n'
		return b
	}()
	// Calibrate: the offset of the first content byte of calc/z.txt in git diff --cached of exactly these files.
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "base")
	writeTestFile(t, filepath.Join(dir, ".gitattributes"), "*.dat diff\n", 0o644)
	if err := os.MkdirAll(filepath.Join(dir, "calc"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dir, "calc", "a.dat"), string(bigDat), 0o644)
	writeTestFile(t, filepath.Join(dir, "calc", "z.txt"), "x\n", 0o644)
	gitIn(t, dir, "add", "-A")
	out, err := gitTry(dir, nil, "diff", "--cached", "--no-ext-diff", "--no-textconv", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	hdr := "+++ b/calc/z.txt\n@@ -0,0 +1 @@\n+"
	i := strings.Index(out, hdr)
	if i < 0 {
		t.Fatalf("calibration diff has no z.txt hunk (%d bytes)", len(out))
	}
	start := i + len(hdr) // offset of the first content byte
	pad := capEdge - start - keep
	if pad < 1 {
		t.Fatalf("calibration: pad = %d", pad)
	}
	gr := failedClaimRun(t, []harnessOp{
		{Op: "write", Path: ".gitattributes", Content: "*.dat diff\n"},
		{Op: "write-gen", Path: "calc/a.dat", N: bigN, Content: "\x00", Args: []string{"0", "a"}},
		{Op: "write-gen", Path: "calc/z.txt", N: pad, Content: gwCanary, Args: []string{"0", "a"}},
	})
	stdout := gr.proc.stdout.String()
	blk := partialDiffBlock(t, stdout)
	if !strings.Contains(stdout, "[diff truncated by sc-worker]") {
		t.Fatalf("the run did not reach the 16 MiB capture edge (block %d bytes)", len(blk))
	}
	for _, f := range fragments(gwCanary, 8) {
		if strings.Contains(blk, f) {
			t.Errorf("the PARTIAL-DIFF block carries the fragment %q of the gateway token cut at the 16 MiB capture edge", f)
		}
	}
}

func hexOf(s string) string { return hex.EncodeToString([]byte(s)) }
