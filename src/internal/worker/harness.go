package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

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

func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readNew returns the complete lines appended to path since *off; *partial carries an unterminated tail.
func readNew(path string, off *int64, partial *[]byte) []string {
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() <= *off {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if _, err := f.Seek(*off, io.SeekStart); err != nil {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(f, 8<<20))
	*off += int64(len(b))
	*partial = append(*partial, b...)
	var lines []string
	for {
		i := strings.IndexByte(string(*partial), '\n')
		if i < 0 {
			break
		}
		lines = append(lines, strings.TrimSuffix(string((*partial)[:i]), "\r"))
		*partial = (*partial)[i+1:]
	}
	return lines
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
	hardStop := start.Add(time.Duration(timeout)*time.Second + 5*time.Second)
	lastAlive := start
	var off int64
	var partial []byte
	var cancelAt time.Time
	cancelWritten := false
	for {
		for _, l := range readNew(stream, &off, &partial) {
			onLine(l)
		}
		if b, err := os.ReadFile(resPath); err == nil {
			if st, lerr := os.Lstat(resPath); lerr == nil && st.Mode().IsRegular() && json.Unmarshal(b, &out.res) == nil {
				out.haveRes = true
				for _, l := range readNew(stream, &off, &partial) {
					onLine(l)
				}
				if len(partial) > 0 {
					onLine(string(partial))
				}
				return out
			}
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
				_ = writeAtomic(filepath.Join(dir, fmt.Sprintf("cancel-%d", n)), []byte("{}"))
				cancelWritten, cancelAt = true, now
			}
		} else if !now.Before(cancelAt.Add(time.Duration(grace)*time.Second + 10*time.Second)) {
			out.lost = true
			return out
		}
		if now.Sub(lastAlive) >= 15*time.Second {
			_ = writeAtomic(filepath.Join(dir, "alive"), []byte(w.stamp(now)))
			lastAlive = now
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
	hs.bytes += len(line) + 1
	if hs.cap {
		return
	}
	if limit := pl.p.Limits.MaxLogBytes; limit > 0 && hs.bytes > limit {
		hs.cap, hs.logCut = true, true
		w.logMu.Lock()
		fmt.Fprintf(w.out, "{\"src\":\"sc-worker\",\"event\":\"log_budget_exhausted\"}\n")
		w.logMu.Unlock()
		return
	}
	line = w.scrub.String(line)
	if len(line) > maxStreamLine {
		hs.truncated++
		cut := len(line) - maxStreamLine
		line = line[:maxStreamLine] + fmt.Sprintf(" …[truncated %d bytes]", cut)
	}
	// A harness line must never pass for sc-worker framing.
	if strings.HasPrefix(line, "::SC-") || strings.HasPrefix(line, "::STRANGE-COMPANY-") {
		line = "| " + line
	}
	w.logMu.Lock()
	fmt.Fprintln(w.out, line)
	w.logMu.Unlock()
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
