package engine

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

// redactValue replaces the registered secrets in every string (and key) of a decoded JSON value.
func (e *Engine) redactValue(v any) any {
	switch x := v.(type) {
	case string:
		return e.red.String(x)
	case []any:
		for i := range x {
			x[i] = e.redactValue(x[i])
		}
		return x
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[e.red.String(k)] = e.redactValue(val)
		}
		return out
	}
	return v
}

// Log writes one JSON line to runs/<run-id>/log.jsonl, redacted.
func (e *Engine) Log(level, event, msg string, fields map[string]any) {
	rec := map[string]any{"ts": e.stamp(), "level": level, "run_id": e.runID, "event": event, "msg": e.red.String(msg)}
	if e.curPipeline != "" {
		rec["pipeline"] = e.curPipeline
	}
	if e.curPhase != "" {
		rec["phase"] = e.curPhase
	}
	if e.curStep != "" {
		rec["step"] = e.curStep
	}
	for _, k := range []string{"attempt", "error_code", "duration_ms"} {
		if v, ok := fields[k]; ok {
			rec[k] = v
		}
	}
	if len(fields) > 0 {
		rest := make(map[string]any, len(fields))
		for k, v := range fields {
			if k != "attempt" && k != "error_code" && k != "duration_ms" {
				rest[k] = v
			}
		}
		fields = rest
	}
	if len(fields) > 0 {
		if b, err := json.Marshal(fields); err == nil {
			dec := json.NewDecoder(bytes.NewReader(b))
			dec.UseNumber()
			var v any
			if dec.Decode(&v) == nil {
				rec["fields"] = e.redactValue(v)
			}
		}
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	line := append(e.red.Bytes(b), '\n')
	e.logMu.Lock()
	defer e.logMu.Unlock()
	if e.logFile == nil {
		if os.MkdirAll(e.runDir(), 0o700) != nil {
			return
		}
		f, err := os.OpenFile(filepath.Join(e.runDir(), "log.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		e.logFile = f
	}
	_, _ = e.logFile.Write(line)
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// ToolLog opens runs/<run-id>/tools/<phase>.<step>.<tool>.log; everything written is redacted.
func (e *Engine) ToolLog(phase, step, tool string) (io.WriteCloser, error) {
	dir := filepath.Join(e.runDir(), "tools")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	name := unsafeName.ReplaceAllString(phase, "_")
	if step != "" {
		name += "." + unsafeName.ReplaceAllString(step, "_")
	}
	name += "." + unsafeName.ReplaceAllString(tool, "_") + ".log"
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return e.red.Writer(f), nil
}

// WriteReport writes the final report to runs/<run-id>/report.txt, redacted.
func (e *Engine) WriteReport(text string) error {
	return writeFileAtomic(filepath.Join(e.runDir(), "report.txt"), []byte(e.red.String(text)))
}

// uploadLog closes the run log and stores it gzipped as runs/<run-id>/log.jsonl.gz (plan 50 section 11). A failed
// upload is not an error of the run: the local log stays.
func (e *Engine) uploadLog(ctx context.Context) {
	e.logMu.Lock()
	if e.logFile != nil {
		_ = e.logFile.Close()
		e.logFile = nil
	}
	e.logMu.Unlock()
	if e.cfg.Store == nil {
		return
	}
	raw, err := os.ReadFile(filepath.Join(e.runDir(), "log.jsonl"))
	if err != nil {
		return
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil || zw.Close() != nil {
		return
	}
	_, _ = e.cfg.Store.Put(context.WithoutCancel(ctx), e.cfg.Cluster+"/runs/"+e.runID+"/log.jsonl.gz", buf.Bytes(), "")
}
