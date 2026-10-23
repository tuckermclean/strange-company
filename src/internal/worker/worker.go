package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/scrub"
	"strange.company/sc/internal/version"
)

// packetInfo is the subset of worker-input.schema.yaml that sc-worker itself reads.
type packetInfo struct {
	Task struct {
		TaskID     string `json:"task_id"`
		Repository string `json:"repository"`
	} `json:"task"`
	Execution struct {
		ExecutionID string `json:"execution_id"`
		Role        string `json:"role"`
		DeadlineAt  string `json:"deadline_at"`
		Traceparent string `json:"traceparent"`
	} `json:"execution"`
	Git struct {
		Repository  string `json:"repository"`
		BaseCommit  string `json:"base_commit"`
		StartCommit string `json:"start_commit"`
		Branch      string `json:"branch"`
		PushAllowed bool   `json:"push_allowed"`
	} `json:"git"`
	Limits struct {
		HeartbeatIntervalSeconds int `json:"heartbeat_interval_seconds"`
		MaxResultBytes           int `json:"max_result_bytes"`
	} `json:"limits"`
	Harness struct {
		Adapter    string `json:"adapter"`
		CLIVersion string `json:"cli_version"`
	} `json:"harness"`
}

type worker struct {
	cfg    Config
	clk    clock.Clock
	out    io.Writer
	errw   io.Writer
	logMu  sync.Mutex
	sig    context.Context // cancelled by SIGTERM or SIGINT
	scrub  *scrub.Scrubber
	client *http.Client

	runnerURL, taskID, execID string
	token                     string
	secrets                   []string // exact-match scrubber set (also used by the diff secret scan)
	started                   time.Time

	pktRaw       []byte
	pi           packetInfo
	haveIdentity bool // pi carries enough to build a schema-valid result

	phase    atomic.Value // string
	cancelRq atomic.Bool
	reported atomic.Bool

	hbCancel context.CancelFunc
	hbWG     sync.WaitGroup
}

func newWorker(cfg Config) *worker {
	w := &worker{cfg: cfg, clk: cfg.Clock, out: cfg.Stdout, errw: cfg.Stderr}
	if w.clk == nil {
		w.clk = clock.Real{}
	}
	if w.out == nil {
		w.out = io.Discard
	}
	if w.errw == nil {
		w.errw = io.Discard
	}
	w.client = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	w.phase.Store("starting")
	w.sig = context.Background()
	return w
}

func (w *worker) getenv(k string) string {
	if w.cfg.Getenv != nil {
		return w.cfg.Getenv(k)
	}
	return os.Getenv(k)
}

func userAgent() string { return "sc-worker/" + version.Version }

func (w *worker) setPhase(p string) { w.phase.Store(p) }

func (w *worker) stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// log writes one JSON line of sc-worker's own log to stdout (plan 27 section 4), scrubbed.
func (w *worker) log(level, event string, fields map[string]any) {
	m := map[string]any{"ts": w.stamp(w.clk.Now()), "level": level, "event": event}
	for k, v := range fields {
		m[k] = v
	}
	if w.scrub != nil {
		m = w.scrub.Value(m).(map[string]any)
	}
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	w.logMu.Lock()
	defer w.logMu.Unlock()
	_, _ = w.out.Write(append(b, '\n'))
}

// errf writes one scrubbed line to stderr.
func (w *worker) errf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	if w.scrub != nil {
		line = w.scrub.String(line)
	}
	w.logMu.Lock()
	defer w.logMu.Unlock()
	_, _ = io.WriteString(w.errw, line)
}

func (w *worker) deadline() (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, w.pi.Execution.DeadlineAt)
	return t, err == nil
}

// startHeartbeat posts a heartbeat every limits.heartbeat_interval_seconds until stopHeartbeat (plan 27
// section 7.1). Failures are logged, never fatal.
func (w *worker) startHeartbeat() {
	interval := time.Duration(w.pi.Limits.HeartbeatIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ctx, cancel := context.WithCancel(w.sig)
	w.hbCancel = cancel
	w.hbWG.Add(1)
	go func() {
		defer w.hbWG.Done()
		for {
			if err := w.clk.Sleep(ctx, interval); err != nil {
				return
			}
			body, _ := json.Marshal(map[string]any{
				"execution_id": w.execID,
				"phase":        w.phase.Load(),
				"at":           w.stamp(w.clk.Now()),
				"redactions":   w.scrub.Redactions(),
			})
			rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
			resp, status, err := w.do(rctx, http.MethodPost, "/v1/worker/heartbeat", body)
			rcancel()
			if err != nil || status != http.StatusOK {
				if ctx.Err() == nil {
					w.log("warn", "heartbeat_failed", map[string]any{"status": status})
				}
				continue
			}
			var hr struct {
				CancelRequested bool `json:"cancel_requested"`
			}
			if json.Unmarshal(resp, &hr) == nil && hr.CancelRequested {
				w.cancelRq.Store(true)
			}
		}
	}()
}

func (w *worker) stopHeartbeat() {
	if w.hbCancel != nil {
		w.hbCancel()
		w.hbWG.Wait()
		w.hbCancel = nil
	}
}

// terminate is the SIGTERM path of plan 27 section 14.14: a failed result (harness_timeout when the pod
// deadline is within 5 s, else cancelled) with a single 5 s post attempt, then exit 143. Without a packet there
// is nothing to report.
func (w *worker) terminate() int {
	w.stopHeartbeat()
	if w.haveIdentity && !w.reported.Load() {
		reason, detail := "cancelled", "sigterm"
		if dl, ok := w.deadline(); ok && !w.clk.Now().Before(dl.Add(-5*time.Second)) {
			reason = "harness_timeout"
		}
		w.report(w.failedResult(reason, detail), modeSingle)
	}
	return ExitSIGTERM
}

// onPanic is the top-level recovery of plan 27 section 14.14: log, best-effort failed/internal, exit 70.
func (w *worker) onPanic(r any) (code int) {
	code = ExitPanic
	defer func() { _ = recover() }()
	w.log("error", "panic", map[string]any{"value": fmt.Sprint(r)})
	w.errf("sc-worker: panic: %v\n", r)
	w.stopHeartbeat()
	if w.haveIdentity && !w.reported.Load() {
		w.report(w.failedResult("internal", "panic"), modeSingle)
	}
	return code
}
