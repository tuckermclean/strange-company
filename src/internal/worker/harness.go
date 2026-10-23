package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"strange.company/sc/internal/worker/adapters"
)

// The worker side of the shim protocol (plan 27 section 7.4) and the harness run (section 14.7).

const (
	shimPoll         = 100 * time.Millisecond
	egressProxyURL   = "http://sc:@file:egress-token@egress-proxy.strange-company.svc.cluster.local:3128"
	noProxyValue     = ".svc,.svc.cluster.local,10.42.0.0/16,10.43.0.0/16"
	maxStreamLine    = 64 << 10
	validationTailMx = 8 << 10
)

var nilCtx = context.Background()

type shimResult struct {
	ID         string `json:"id"`
	ExitCode   int    `json:"exit_code"`
	Signal     any    `json:"signal"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
	TimedOut   bool   `json:"timed_out"`
}

// shimOutcome is what runShim learned about one request.
type shimOutcome struct {
	res       shimResult
	haveRes   bool
	cancelled bool // sc-worker asked for termination (SIGTERM or cancel_requested)
	backstop  bool // sc-worker's own timeout fired
	lost      bool // no result came back even after termination
}

func (pl *pipeline) shimDir() string { return filepath.Join(pl.w.cfg.WorkspaceDir, ".sc-shim") }

// startAlive keeps the shim's `alive` file fresh (plan 27 section 7.4: the shim leaves when it is older than
// 120 s) until the returned stop function is called.
func (w *worker) startAlive() (stop func()) {
	dir := filepath.Join(w.cfg.WorkspaceDir, ".sc-shim")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		var last time.Time
		for {
			if now := w.clk.Now(); last.IsZero() || now.Sub(last) >= 15*time.Second {
				_ = writeAtomic(filepath.Join(dir, "alive"), []byte(w.stamp(now)))
				last = now
			}
			if w.clk.Sleep(ctx, time.Second) != nil {
				return
			}
		}
	}()
	return func() { cancel(); <-done }
}

// maxTailLine bounds the unterminated tail that sc-worker holds for one stream line. A longer line is cut here (the
// framer truncates it to 64 KiB after scrubbing it whole) and the rest of that line is dropped unread into memory.
const maxTailLine = 1 << 20

// streamTail follows a shim stream file.
type streamTail struct {
	root       string // trusted directory the stream path is resolved beneath
	refuse     func(path string, err error)
	off        int64
	buf        []byte // unterminated tail, at most maxTailLine bytes
	discarding bool   // the rest of the current line is dropped
	dropped    int    // bytes dropped since the last call of takeDropped
}

func (t *streamTail) takeDropped() int {
	n := t.dropped
	t.dropped = 0
	return n
}

// open opens the stream file below the tail's root without following a link.
func (t *streamTail) open(path string) (*os.File, error) {
	rel, ok := strings.CutPrefix(path, t.root+"/")
	if !ok {
		return nil, errBadPath
	}
	f, err := openRegular(t.root, rel, os.O_RDONLY)
	if err != nil && isRefusal(err) && t.refuse != nil {
		t.refuse(path, err)
	}
	return f, err
}

// size is the current size of the stream file (0 when it is not a regular file).
func (t *streamTail) size(path string) int64 {
	f, err := t.open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0
	}
	return st.Size()
}

// read returns the complete lines appended to path since the last call; buf carries an unterminated tail. It reads at
// most one 8 MiB chunk.
func (t *streamTail) read(path string) []string { return t.readTo(path, -1) }

// readTo is read limited to the offsets below end (end < 0: no limit).
func (t *streamTail) readTo(path string, end int64) []string {
	if end >= 0 && end <= t.off {
		return nil
	}
	f, err := t.open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || st.Size() <= t.off {
		return nil
	}
	if _, err := f.Seek(t.off, io.SeekStart); err != nil {
		return nil
	}
	n := int64(8 << 20)
	if end >= 0 && end-t.off < n {
		n = end - t.off
	}
	b, _ := io.ReadAll(io.LimitReader(f, n))
	t.off += int64(len(b))
	var lines []string
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		piece := b
		if i >= 0 {
			piece = b[:i]
		}
		if t.discarding {
			t.dropped += len(piece)
		} else {
			t.buf = append(t.buf, piece...)
			if len(t.buf) > maxTailLine {
				t.dropped += len(t.buf) - maxTailLine
				t.buf = t.buf[:maxTailLine]
				lines = append(lines, string(t.buf))
				t.buf, t.discarding = t.buf[:0], true
			}
		}
		if i < 0 {
			break
		}
		if t.discarding {
			t.dropped++ // the newline
		} else {
			lines = append(lines, strings.TrimSuffix(string(t.buf), "\r"))
			t.buf = t.buf[:0]
		}
		t.discarding = false
		b = b[i+1:]
	}
	return lines
}

// drainBudget is the most that is read from a stream file once the shim's result file exists.
const drainBudget = 8 << 20

// drain reads the stream file up to end (its size when the result file appeared; the shim flushes the stream before
// it writes the result) and at most drainBudget more bytes, and stops at once when stop reports true. It returns
// false when it stopped early (budget or stop): the unterminated tail is then not a whole line.
func (t *streamTail) drain(path string, end int64, stop func() bool, onLine func(string)) (complete bool) {
	limit := t.off + drainBudget
	budgetCut := end < 0 || end > limit
	if budgetCut {
		end = limit
	}
	for !stop() {
		before := t.off
		for _, l := range t.readTo(path, end) {
			onLine(l)
		}
		if t.off == before {
			return !(budgetCut && t.off >= limit)
		}
	}
	return false
}

// runShim sends one run request and tails its stream until the result file appears. timeout is the shim's
// enforced timeout; sc-worker backstops it with a cancel file.
func (pl *pipeline) runShim(kind string, argv []string, env map[string]string, cwd string, timeout, grace int, onLine func(string)) shimOutcome {
	w := pl.w
	pl.reqN++
	n := pl.reqN
	dir := pl.shimDir()
	req, _ := json.Marshal(map[string]any{
		"id": fmt.Sprintf("%s-%d", w.execID, n), "kind": kind, "argv": argv, "env": env, "cwd": cwd,
		"timeout_seconds": timeout, "term_grace_seconds": grace,
	})
	var out shimOutcome
	if err := writeAtomic(filepath.Join(dir, fmt.Sprintf("req-%d.json", n)), req); err != nil {
		w.log("error", "shim_request_failed", map[string]any{"detail": err.Error()})
		out.lost = true
		return out
	}
	stream := filepath.Join(dir, fmt.Sprintf("stream-%d.log", n))
	resPath := filepath.Join(dir, fmt.Sprintf("res-%d.json", n))
	start := w.clk.Now()
	// The shim enforces the timeout itself (SIGTERM, then SIGKILL after the grace period); sc-worker's own
	// backstop fires only when the shim failed to do so.
	hardStop := start.Add(time.Duration(timeout+grace)*time.Second + 5*time.Second)
	tail := streamTail{root: w.cfg.WorkspaceDir, refuse: pl.refuse}
	account := func() { pl.hs.bytes += tail.takeDropped() }
	var cancelAt time.Time
	waitExtra := 10 * time.Second
	cancelWritten := false
	for {
		for _, l := range tail.read(stream) {
			onLine(l)
		}
		pl.flushOut()
		account()
		if b, err := readRegularFile(w.cfg.WorkspaceDir, ".sc-shim/"+filepath.Base(resPath), 1<<20); err == nil {
			if json.Unmarshal(b, &out.res) == nil {
				out.haveRes = true
				complete := tail.drain(stream, tail.size(stream), func() bool { return w.sig.Err() != nil }, onLine)
				if complete {
					if len(tail.buf) > 0 {
						onLine(string(tail.buf))
					}
				} else {
					// A half line is not emitted: a secret cut inside it would no longer be recognised.
					tail.dropped += len(tail.buf)
				}
				tail.buf = tail.buf[:0]
				pl.flushOut()
				account()
				return out
			}
		} else if isRefusal(err) {
			pl.refuse(resPath, err)
		}
		now := w.clk.Now()
		if !cancelWritten {
			switch {
			case w.sig.Err() != nil || w.cancelRq.Load():
				out.cancelled = true
			case !now.Before(hardStop):
				out.backstop = true
			}
			if out.cancelled || out.backstop {
				// The SIGTERM path waits min(term_grace_seconds, 20 s) before the SIGKILL (plan 27 section 14.14).
				cg := grace
				if w.sig.Err() != nil && cg > 20 {
					cg = 20
				}
				cb, _ := json.Marshal(map[string]any{"term_grace_seconds": cg})
				_ = writeAtomic(filepath.Join(dir, fmt.Sprintf("cancel-%d", n)), cb)
				cancelWritten, cancelAt, grace = true, now, cg
				if w.sig.Err() != nil {
					// Pod deletion: the kubelet kills the pod 30 s after the signal.
					waitExtra = 3 * time.Second
				}
			}
		} else if !now.Before(cancelAt.Add(time.Duration(grace)*time.Second + waitExtra)) {
			out.lost = true
			return out
		}
		_ = w.clk.Sleep(nilCtx, shimPoll)
	}
}

// shimDone tells the shim to exit (plan 27 section 7.4).
func (pl *pipeline) shimDone() {
	if pl.doneWritten {
		return
	}
	pl.doneWritten = true
	_ = writeAtomic(filepath.Join(pl.shimDir(), "done"), []byte("{}"))
}

// harnessState is what the line parser learns from the harness stream.
type harnessState struct {
	terminal  bool
	isError   bool
	numTurns  int
	gwCode    string
	lines     int
	bytes     int
	truncated int
	logCut    bool
	cap       bool
}

var gwCodeRe = regexp.MustCompile(`\bSC_[A-Z][A-Z_]{2,}\b`)

var gwVerdict = map[string]string{
	"SC_BUDGET_EXHAUSTED": "budget_denied", "SC_BUDGET_UNDEFINED": "budget_denied", "SC_REQUEST_COST_CAP": "budget_denied",
	"SC_PRICE_UNKNOWN": "budget_denied", "SC_EMERGENCY_STOP": "emergency_stop", "SC_TOKEN_REVOKED": "emergency_stop",
	"SC_UPSTREAM_UNREACHABLE": "model_unavailable", "SC_UPSTREAM_TIMEOUT": "model_unavailable",
	"SC_LEDGER_UNAVAILABLE": "model_unavailable", "SC_PROVIDER_UNAVAILABLE": "model_unavailable",
}

// parse inspects one raw harness line (adapter.Parse for the terminal event and gateway codes).
func (hs *harnessState) parse(line string) {
	if strings.HasPrefix(line, "{") {
		var ev struct {
			Type    string `json:"type"`
			IsError bool   `json:"is_error"`
			Turns   int    `json:"num_turns"`
		}
		if json.Unmarshal([]byte(line), &ev) == nil && (ev.Type == "sc_fake_result" || ev.Type == "result") {
			hs.terminal, hs.isError, hs.numTurns = true, ev.IsError, ev.Turns
		}
	}
	if !strings.Contains(line, "SC_") {
		return
	}
	if m := gwCodeRe.FindString(line); m != "" {
		if _, ok := gwVerdict[m]; ok && hs.gwCode == "" {
			hs.gwCode = m
		}
	}
}

// emit writes one scrubbed harness line to sc-worker's stdout within the log budget (plan 27 section 14.8).
func (pl *pipeline) emit(line string) {
	w := pl.w
	hs := &pl.hs
	hs.parse(line)
	hs.lines++
	if hs.cap {
		return
	}
	// The whole (bounded) line is scrubbed before it is truncated, so a secret across the cut is still caught.
	line = escapeControls(w.scrub.String(line))
	cut := 0
	if len(line) > maxStreamLine {
		cut = len(line) - maxStreamLine
		line = line[:utf8Boundary(line, maxStreamLine)] + fmt.Sprintf(" …[truncated %d bytes]", cut)
	}
	// A harness line must never pass for sc-worker framing.
	if strings.HasPrefix(line, "::SC-") || strings.HasPrefix(line, "::STRANGE-COMPANY-") {
		line = "| " + line
	}
	// max_log_bytes limits what is written, so the escaped bytes are charged.
	hs.bytes += len(line) + 1
	if limit := pl.p.Limits.MaxLogBytes; limit > 0 && hs.bytes > limit {
		hs.cap, hs.logCut = true, true
		pl.obuf = append(pl.obuf, "{\"src\":\"sc-worker\",\"event\":\"log_budget_exhausted\"}\n"...)
		pl.flushOut()
		return
	}
	if cut > 0 {
		hs.truncated++
	}
	pl.obuf = append(append(pl.obuf, line...), '\n')
	if len(pl.obuf) >= 64<<10 {
		pl.flushOut()
	}
}

// flushOut writes the buffered harness lines to stdout in one write (a harness that prints millions of tiny lines
// must not cost a system call each).
func (pl *pipeline) flushOut() {
	if len(pl.obuf) == 0 {
		return
	}
	pl.w.logMu.Lock()
	_, _ = pl.w.out.Write(pl.obuf)
	pl.w.logMu.Unlock()
	pl.obuf = pl.obuf[:0]
}

// harnessVerdict is adapter.Classify (plan 27 section 14.7 step 6).
func (pl *pipeline) harnessVerdict(o shimOutcome) string {
	hs := pl.hs
	switch {
	case o.cancelled:
		return "cancelled"
	case o.backstop || o.res.TimedOut:
		return "harness_timeout"
	case o.lost || !o.haveRes:
		return "harness_error"
	}
	v := "ok"
	switch {
	case o.res.ExitCode != 0 || o.res.Signal != nil:
		v = "harness_error"
	case !hs.terminal:
		return "harness_stream_incomplete"
	case hs.isError:
		v = "harness_error"
	}
	if v == "harness_error" && hs.gwCode != "" {
		return gwVerdict[hs.gwCode]
	}
	return v
}

// harnessEnv builds the child environment from scratch (plan 27 section 14.7).
func (pl *pipeline) harnessEnv(prep adapters.Prepared) map[string]string {
	env := map[string]string{}
	for k, v := range prep.Env {
		env[k] = v
	}
	env["HOME"], env["TMPDIR"] = pl.w.cfg.HomeDir, pl.w.cfg.TmpDir
	env["HTTPS_PROXY"], env["HTTP_PROXY"], env["NO_PROXY"] = egressProxyURL, egressProxyURL, noProxyValue
	return env
}

// runHarness runs the adapter's argv through the shim and returns the verdict.
func (pl *pipeline) runHarness() (verdict string, f *stageFail) {
	w := pl.w
	prep, err := adapters.Prepare(w.pktRaw)
	if err != nil {
		return "", &stageFail{reason: "harness_start_failed", detail: err.Error()}
	}
	for _, cf := range prep.Files {
		p := filepath.Join(w.cfg.HomeDir, filepath.Clean("/"+cf.Path))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err == nil {
			_ = os.WriteFile(p, []byte(cf.Content), os.FileMode(cf.Mode)&0o600)
		}
	}
	pl.env = pl.harnessEnv(prep)
	T := pl.p.Limits.HarnessTimeoutSeconds
	if dl, ok := w.deadline(); ok && !pl.p.Harness.DisableWorkerTimeout {
		if left := int(dl.Sub(w.clk.Now())/time.Second) - 60; left < T {
			T = left
		}
	}
	if T < 30 {
		T = 30
	}
	grace := pl.p.Limits.TermGraceSeconds
	if grace <= 0 {
		grace = 10
	}
	started := w.clk.Now()
	pl.harnessStart = started
	w.log("info", "harness_start", map[string]any{"execution_id": w.execID, "adapter": pl.p.Harness.Adapter, "cli_version": pl.p.Harness.CLIVersion})
	w.logMu.Lock()
	hdr, _ := json.Marshal(map[string]any{"execution_id": w.execID, "adapter": pl.p.Harness.Adapter, "started_at": w.stamp(started)})
	fmt.Fprintf(w.out, "::STRANGE-COMPANY-STREAM-BEGIN:: %s\n", hdr)
	w.logMu.Unlock()

	out := pl.runShim("harness", prep.Argv, pl.env, "/workspace/repo", T, grace, pl.emit)

	pl.harnessEnd = w.clk.Now()
	pl.hout = out
	pl.harnessRan = true
	sig := any(nil)
	if out.res.Signal != nil {
		sig = out.res.Signal
	}
	end, _ := json.Marshal(map[string]any{"exit_code": out.res.ExitCode, "signal": sig, "lines": pl.hs.lines, "bytes": pl.hs.bytes,
		"truncated_lines": pl.hs.truncated, "log_truncated": pl.hs.logCut})
	w.logMu.Lock()
	fmt.Fprintf(w.out, "::STRANGE-COMPANY-STREAM-END:: %s\n", end)
	w.logMu.Unlock()
	verdict = pl.harnessVerdict(out)
	w.log("info", "harness_exit", map[string]any{"verdict": verdict, "exit_code": out.res.ExitCode,
		"duration_ms": pl.harnessEnd.Sub(started).Milliseconds()})
	return verdict, nil
}

// validate runs the packet's validation commands in the harness container (plan 27 section 14.9 step 3).
func (pl *pipeline) validate() []any {
	w := pl.w
	out := []any{}
	env := map[string]string{}
	for k, v := range pl.env {
		if v == "@file:gateway-token" {
			continue
		}
		env[k] = v
	}
	for _, c := range pl.p.Policy.ValidationCommands {
		t := c.TimeoutSeconds
		skipped := false
		if dl, ok := w.deadline(); ok {
			left := int(dl.Sub(w.clk.Now())/time.Second) - 90
			if left < 30 {
				skipped = true
			} else if left < t {
				t = left
			}
		}
		entry := map[string]any{"name": c.Name, "command": clip(strings.Join(c.Argv, " "), 1000)}
		if skipped {
			entry["exit_code"], entry["duration_ms"], entry["timed_out"] = -1, 0, true
			out = append(out, entry)
			continue
		}
		var buf []byte
		started := w.clk.Now()
		o := pl.runShim("validation", c.Argv, env, "/workspace/repo", t, pl.p.Limits.TermGraceSeconds, func(l string) {
			buf = append(buf, l...)
			buf = append(buf, '\n')
			if len(buf) > 4*validationTailMx {
				buf = buf[len(buf)-2*validationTailMx:]
			}
		})
		entry["exit_code"] = o.res.ExitCode
		if !o.haveRes {
			entry["exit_code"] = -1
		}
		entry["duration_ms"] = int(w.clk.Now().Sub(started).Milliseconds())
		entry["timed_out"] = o.res.TimedOut || o.backstop
		tl := tail(w.scrub.String(string(buf)), validationTailMx)
		if tl != "" {
			entry["output_tail"] = tl
		}
		out = append(out, entry)
		if w.sig.Err() != nil {
			break
		}
	}
	return out
}

// utf8Boundary returns the largest index n' <= n that does not split a rune of s.
func utf8Boundary(s string, n int) int {
	if n >= len(s) {
		return len(s)
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return n
}

// escapeControls makes C0 and C1 control characters (and DEL) visible as \u00XX; tab and newline stay. Terminal
// escape sequences in harness output or in a diff therefore never reach the operator's terminal raw.
func escapeControls(s string) string {
	clean := utf8.ValidString(s)
	for i := 0; clean && i < len(s); i++ {
		if c := s[i]; (c < 0x20 && c != '\t' && c != '\n') || c == 0x7f || c == 0xc2 {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && n == 1 {
			fmt.Fprintf(&b, "\\x%02x", s[i]) // an invalid byte (possibly a C1 control to a terminal)
			i++
			continue
		}
		i += n
		if (r < 0x20 && r != '\t' && r != '\n') || (r >= 0x7f && r <= 0x9f) {
			fmt.Fprintf(&b, "\\u%04x", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
