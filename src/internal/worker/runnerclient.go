package worker

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"time"
)

// The Runner worker API calls of plan 27 section 7.1. Every call carries the task token, the packet's
// traceparent (once known) and the sc-worker User-Agent.

var packetBackoff = []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second}

// do performs one request against the Runner. A transport error is returned as err; any HTTP response is
// returned with its status and body.
func (w *worker) do(ctx context.Context, method, path string, body []byte) ([]byte, int, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, w.runnerURL+path, rd)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+w.token)
	req.Header.Set("User-Agent", userAgent())
	req.Header.Set("Accept", "application/json")
	if w.pi.Execution.Traceparent != "" {
		req.Header.Set("traceparent", w.pi.Execution.Traceparent)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return b, resp.StatusCode, nil
}

// retryable reports whether a status is retried by the packet and result calls (429 and 5xx).
func retryable(status int) bool { return status == http.StatusTooManyRequests || status >= 500 }

// fetchPacket gets the task packet (10 s per attempt; connect errors, 429 and 5xx back off 1, 2, 4, 8, 16, 30,
// 30, 30 s). 401, 403, 404, 409 and every other non-retryable status end in exit 2 without retry. ok is false
// when code is the exit code to return.
func (w *worker) fetchPacket() (raw []byte, code int, ok bool) {
	for attempt := 0; ; attempt++ {
		ctx, cancel := context.WithTimeout(w.sig, 10*time.Second)
		b, status, err := w.do(ctx, http.MethodGet, "/v1/worker/packet", nil)
		cancel()
		if w.sig.Err() != nil {
			return nil, ExitSIGTERM, false
		}
		if err == nil && status == http.StatusOK {
			return b, 0, true
		}
		w.log("warn", "packet_attempt_failed", map[string]any{"attempt": attempt + 1, "status": status})
		if err == nil && !retryable(status) {
			return nil, w.exit(ExitEnv, "packet request refused"), false
		}
		if attempt >= len(packetBackoff) {
			return nil, w.exit(ExitEnv, "packet unobtainable after retries"), false
		}
		if w.clk.Sleep(w.sig, packetBackoff[attempt]) != nil {
			return nil, ExitSIGTERM, false
		}
	}
}

// resultBackoff is the delay before retry n of the result post: 1, 2, 4, 8, 16, 30 s, then every 30 s.
func resultBackoff(n int) time.Duration {
	d := []time.Duration{1, 2, 4, 8, 16}
	if n < len(d) {
		return d[n] * time.Second
	}
	return 30 * time.Second
}

// postResult sends the identical body until accepted (200/202 -> 0), refused (other 4xx except 429 -> 4), or
// the retry window min(600 s, deadline_at - now - 30 s) is spent (-> 3). In single mode there is one 5 s
// attempt that ignores SIGTERM (the SIGTERM and panic paths), and the returned code is only informational.
func (w *worker) postResult(body []byte, mode reportMode) int {
	if mode == modeSingle {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return w.postClass(w.postOnce(ctx, body))
	}
	window := 600 * time.Second
	if dl, ok := w.deadline(); ok {
		if left := dl.Sub(w.clk.Now()) - 30*time.Second; left < window {
			window = left
		}
	}
	end := w.clk.Now().Add(window)
	for n := 0; ; n++ {
		ctx, cancel := context.WithTimeout(w.sig, 15*time.Second)
		status, err := w.postOnce(ctx, body)
		cancel()
		switch c := w.postClass(status, err); c {
		case ExitOK, ExitRejected:
			return c
		}
		if w.sig.Err() != nil {
			return w.lastPost(body)
		}
		d := resultBackoff(n)
		if !w.clk.Now().Add(d).Before(end) {
			w.log("error", "result_post_exhausted", map[string]any{"attempts": n + 1})
			return ExitPostFailed
		}
		if w.clk.Sleep(w.sig, d) != nil {
			return w.lastPost(body)
		}
	}
}

// lastPost is the one more single 5 s attempt with the identical body after SIGTERM ended the retry window
// (plan 27 section 14.14); the code is always ExitSIGTERM.
func (w *worker) lastPost(body []byte) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = w.postOnce(ctx, body)
	return ExitSIGTERM
}

func (w *worker) postOnce(ctx context.Context, body []byte) (int, error) {
	_, status, err := w.do(ctx, http.MethodPost, "/v1/worker/result", body)
	w.log("info", "result_post", map[string]any{"status": status, "failed": err != nil})
	return status, err
}

// postClass maps one attempt to ExitOK, ExitRejected, or ExitPostFailed (retry).
func (w *worker) postClass(status int, err error) int {
	switch {
	case err != nil:
		return ExitPostFailed
	case status == http.StatusOK || status == http.StatusAccepted:
		return ExitOK
	case retryable(status):
		return ExitPostFailed
	}
	return ExitRejected
}
