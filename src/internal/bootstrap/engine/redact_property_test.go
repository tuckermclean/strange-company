package engine_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"strange.company/sc/internal/bootstrap/engine"
	"strange.company/sc/internal/bootstrap/redact"
)

const secretAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./+=:@!#$%^&*()~`\"\\<>é«»"

// randSecret is a random secret of 12..40 characters. About one in ten holds a quote, a backslash, an angle bracket
// or a newline, so its JSON-escaped form differs from the value.
func randSecret(rng *rand.Rand, withNewline bool) string {
	n := 12 + rng.Intn(29)
	rs := []rune(secretAlphabet)
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteRune(rs[rng.Intn(len(rs))])
	}
	s := sb.String()
	if withNewline {
		s = s[:n/2] + "\n" + s[n/2:]
	}
	return s
}

// escapedForms are the encodings a secret may take inside a JSON log line.
func escapedForms(s string) []string {
	forms := map[string]bool{s: true}
	b, _ := json.Marshal(s) // HTML-escaping on
	forms[string(b[1:len(b)-1])] = true
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	forms[strings.TrimSuffix(buf.String(), "\n")[1:len(strings.TrimSuffix(buf.String(), "\n"))-1]] = true
	var out []string
	for f := range forms {
		out = append(out, f)
	}
	return out
}

// walk returns the content of every file below dir and every S3 object (decompressed when it is gzip), by name.
func allOutputs(t *testing.T, v *tenv) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	_ = filepath.WalkDir(v.dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			out["local:"+p] = b
		}
		return nil
	})
	keys, _ := v.st.List(context.Background(), "")
	for _, k := range keys {
		b := v.st.raw(k)
		if strings.HasSuffix(k, ".gz") {
			zr, err := gzip.NewReader(bytes.NewReader(b))
			if err != nil {
				t.Errorf("S3 object %s is not valid gzip: %v", k, err)
				continue
			}
			b, _ = io.ReadAll(zr)
		}
		out["s3:"+k] = b
	}
	return out
}

// 50-AC-8: 10 000 random secret strings injected into structured logs, tool logs and the report produce zero
// occurrences in outputs: not in any file below .bootstrap, not in any S3 object, not in status output, not in the
// result message. The outputs must still hold the surrounding text (the check is not vacuous).
func TestRedactor_TenThousandSecretsLeakNowhere_50AC8(t *testing.T) {
	const batches, perBatch = 20, 500
	rng := rand.New(rand.NewSource(8))
	total := 0
	for b := 0; b < batches; b++ {
		v := newEnv(t)
		r := redact.New()
		secrets := make([]string, perBatch)
		for i := range secrets {
			secrets[i] = randSecret(rng, i%20 == 0)
			r.Register(fmt.Sprintf("SC-FIXTURE-%d", i%7), secrets[i])
		}
		total += len(secrets)
		e := v.eng(func(c *engine.Config) { c.Redactor = r })

		var report strings.Builder
		for i, s := range secrets {
			e.Log("info", "fixture", fmt.Sprintf("line %d holds %s end-%d", i, s, i), map[string]any{"token": s, "nested": map[string]any{"k": s}, "list": []any{s}})
			report.WriteString(fmt.Sprintf("report %d: %s ok-%d\n", i, s, i))
		}
		for tool := 0; tool < 5; tool++ {
			w, err := e.ToolLog("provision", "apply", fmt.Sprintf("tool%d", tool))
			if err != nil || w == nil {
				t.Fatalf("ToolLog: %v", err)
			}
			var raw strings.Builder
			for i := tool; i < len(secrets); i += 5 {
				raw.WriteString(fmt.Sprintf("out %s tail-%d\n", secrets[i], i))
			}
			data := []byte(raw.String())
			for len(data) > 0 { // random chunking: a secret is often split across Write calls
				n := 1 + rng.Intn(7)
				if n > len(data) {
					n = len(data)
				}
				if _, err := w.Write(data[:n]); err != nil {
					t.Fatal(err)
				}
				data = data[n:]
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
		}
		if err := e.WriteReport(report.String()); err != nil {
			t.Fatal(err)
		}
		bad := newUnit("provision")
		bad.runFn = func(*unit) ([]engine.OutputRef, error) {
			return nil, &engine.Error{Code: "PROVIDER_REJECTED", Message: "rejected " + secrets[0] + " and " + secrets[1], Retryable: false}
		}
		res := e.Execute(context.Background(), pipe(newUnit("preflight"), bad), engine.Options{})
		if res.Exit != 1 {
			t.Fatalf("batch %d: Execute exit %d (%s)", b, res.Exit, res.Message)
		}
		var sout, serr bytes.Buffer
		engine.Status(context.Background(), v.cfg(), false, &sout, &serr)
		var jout bytes.Buffer
		engine.Status(context.Background(), v.cfg(), true, &jout, &serr)

		outs := allOutputs(t, v)
		outs["result.message"] = []byte(res.Message)
		outs["status.stdout"] = sout.Bytes()
		outs["status.json"] = jout.Bytes()
		outs["status.stderr"] = serr.Bytes()
		leaks := 0
		for name, content := range outs {
			text := string(content)
			for i, s := range secrets {
				for _, form := range escapedForms(s) {
					if n := strings.Count(text, form); n > 0 {
						leaks += n
						if leaks <= 5 {
							t.Errorf("batch %d: secret #%d (%q) occurs %d times in %s", b, i, form, n, name)
						}
					}
				}
			}
		}
		if leaks > 0 {
			t.Fatalf("batch %d: %d occurrences of secrets in the outputs, want 0", b, leaks)
		}
		// not vacuous: the surrounding text survived, and the markers are there
		var logText, report2 string
		var toolText strings.Builder
		for name, c := range outs {
			switch {
			case strings.HasSuffix(name, "log.jsonl"):
				logText = string(c)
			case strings.HasSuffix(name, "report.txt"):
				report2 = string(c)
			case strings.Contains(name, "/tools/"):
				toolText.Write(c)
			}
		}
		if got := strings.Count(logText, "end-"); got < perBatch {
			t.Errorf("batch %d: structured log holds %d lines with their text, want %d", b, got, perBatch)
		}
		if got := strings.Count(report2, "ok-"); got < perBatch {
			t.Errorf("batch %d: report holds %d lines with their text, want %d", b, got, perBatch)
		}
		if got := strings.Count(toolText.String(), "tail-"); got < perBatch {
			t.Errorf("batch %d: tool logs hold %d lines with their text, want %d", b, got, perBatch)
		}
		if got := strings.Count(report2, "«redacted:SC-FIXTURE-"); got < perBatch {
			t.Errorf("batch %d: report holds %d «redacted:SC-FIXTURE-n» markers, want %d", b, got, perBatch)
		}
		if lines := strings.Split(strings.TrimSpace(logText), "\n"); len(lines) > 0 {
			var m map[string]any
			if err := json.Unmarshal([]byte(lines[0]), &m); err != nil || str(m["run_id"]) != e.RunID() {
				t.Errorf("batch %d: first log line is not JSON with run_id %s: %v %v", b, e.RunID(), err, m)
			}
		}
	}
	if total != 10000 {
		t.Fatalf("injected %d secrets, want 10000", total)
	}
}

// Plan 50 section 11: the run log is uploaded to S3 as runs/<run-id>/log.jsonl.gz.
func TestExecute_UploadsGzippedRunLog_Plan50S11(t *testing.T) {
	v := newEnv(t)
	e := v.eng()
	e.Log("info", "fixture", "hello", nil)
	if res := e.Execute(context.Background(), pipe(newUnit("preflight")), engine.Options{}); res.Exit != 0 {
		t.Fatalf("exit %d", res.Exit)
	}
	b := v.st.raw(cluster + "/runs/" + e.RunID() + "/log.jsonl.gz")
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("runs/<run-id>/log.jsonl.gz missing or not gzip: %v", err)
	}
	text, _ := io.ReadAll(zr)
	if !strings.Contains(string(text), "hello") || !strings.Contains(string(text), e.RunID()) {
		t.Errorf("uploaded log lacks the logged line:\n%s", text)
	}
}
