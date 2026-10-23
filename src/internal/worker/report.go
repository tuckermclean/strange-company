package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"strange.company/sc/internal/contracts"
	"strange.company/sc/internal/worker/result"
)

type reportMode int

const (
	modeNormal reportMode = iota // retry window of plan 27 section 7.1
	modeSingle                   // one 5 s attempt (SIGTERM and panic paths)
)

const defaultMaxResultBytes = 262144

var (
	schemaOnce sync.Once
	schemaSet  map[string]*jsonschema.Schema
	schemaErr  error
)

// schemas compiles the embedded contract copies once.
func schemas() (map[string]*jsonschema.Schema, error) {
	schemaOnce.Do(func() {
		docs := map[string][]byte{}
		for _, n := range []string{"worker-input", "worker-result", "context", "handoff"} {
			docs[n+".schema.yaml"] = EmbeddedSchema(n)
		}
		schemaSet, schemaErr = contracts.CompileSchemaBytes(docs)
	})
	return schemaSet, schemaErr
}

func validateAgainst(name string, v any) error {
	set, err := schemas()
	if err != nil {
		return fmt.Errorf("embedded schemas: %w", err)
	}
	return set[name].Validate(v)
}

// acceptPacket validates raw against the embedded worker-input schema and the identity checks of plan 27
// section 14.2 step 2. It fills w.pi / w.haveIdentity as far as the packet is parseable and returns "" when the
// packet is accepted, else a short detail.
func (w *worker) acceptPacket(raw []byte) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "packet is not JSON"
	}
	var pi packetInfo
	_ = json.Unmarshal(raw, &pi)
	w.pi = pi
	w.haveIdentity = pi.Execution.ExecutionID != "" && pi.Execution.Role != "" && pi.Git.BaseCommit != "" &&
		pi.Git.StartCommit != "" && pi.Task.TaskID != ""
	if err := validateAgainst("worker-input", v); err != nil {
		return oneLine("schema: " + err.Error())
	}
	if pi.Execution.ExecutionID != w.execID {
		return "execution_id differs from SC_EXECUTION_ID"
	}
	if pi.Task.TaskID != w.taskID {
		return "task_id differs from SC_TASK_ID"
	}
	return ""
}

func oneLine(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			r = ' '
		}
		out = append(out, r)
	}
	return clip(string(out), 400)
}

// clip shortens s to at most n runes.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// failedResult builds a schema-shaped failed result from the packet identity (plan 27 section 14.12 step 1)
// for a failure that happened before or without a harness run.
func (w *worker) failedResult(reason, detail string) map[string]any {
	pi := w.pi
	role := pi.Execution.Role
	var branch any
	if pi.Git.PushAllowed && role != "reviewer" && role != "researcher" && role != "planner" && pi.Git.Branch != "" {
		branch = pi.Git.Branch
	}
	adapter := pi.Harness.Adapter
	switch adapter {
	case "claude-code", "codex", "opencode", "fake":
	default:
		adapter = "fake"
	}
	repo := pi.Git.Repository
	if repo == "" {
		repo = pi.Task.Repository
	}
	summary := "Worker failed: " + reason
	if detail != "" {
		summary += " (" + clip(detail, 300) + ")"
	}
	now := w.stamp(w.clk.Now())
	return map[string]any{
		"schema_version": "v1alpha1",
		"task_id":        w.taskID,
		"execution_id":   w.execID,
		"repository":     repo,
		"role":           role,
		"state":          "failed",
		"branch":         branch,
		"base_commit":    pi.Git.BaseCommit,
		"start_commit":   pi.Git.StartCommit,
		"commit":         nil,
		"pushed":         false,
		"summary":        summary,
		"failure":        map[string]any{"reason": reason, "detail": clip(detail, 2000)},
		"validation":     []any{},
		"harness_report": map[string]any{
			"adapter":             adapter,
			"cli_version":         clip(pi.Harness.CLIVersion, 40),
			"exit_code":           -1,
			"stream_complete":     false,
			"terminal_event_seen": false,
		},
		"handoff": map[string]any{
			"what_i_did":            "sc-worker stopped before the harness ran: " + reason + ".",
			"what_remains":          "The whole role remains to be done.",
			"recommended_next_role": nil,
			"questions":             []any{},
			"known_risks":           []any{},
		},
		"timings": map[string]any{"started_at": w.stamp(w.started), "finished_at": now},
	}
}

// selfValidate checks doc against the embedded worker-result schema.
func (w *worker) selfValidate(doc map[string]any) error {
	b, err := result.Canonical(doc)
	if err != nil {
		return err
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	return validateAgainst("worker-result", v)
}

// finalize scrubs doc, fits it to max_result_bytes and self-validates it (plan 27 section 14.12 steps 2 to 4),
// replacing it by a minimal failed/internal result when it cannot be made valid.
func (w *worker) finalize(doc map[string]any) []byte {
	limit := w.pi.Limits.MaxResultBytes
	if limit <= 0 {
		limit = defaultMaxResultBytes
	}
	for round := 0; round < 2; round++ {
		w.scrub.Value(doc)
		doc["scrub"] = map[string]any{"redactions": w.scrub.Redactions()}
		body, err := result.Fit(doc, limit)
		if err != nil {
			w.log("error", "result_too_large", nil)
			doc = w.failedResult("internal", "result_too_large")
			continue
		}
		var v any
		if err := json.Unmarshal(body, &v); err == nil {
			err = validateAgainst("worker-result", v)
		}
		if err != nil {
			w.log("error", "result_self_invalid", map[string]any{"detail": oneLine(err.Error())})
			doc = w.failedResult("internal", "result_self_invalid")
			continue
		}
		return body
	}
	// Even the minimal result is unusable; post it as it is and let the Runner answer.
	b, _ := result.Canonical(doc)
	return b
}

// report finalizes doc, writes the framed block to stdout, then posts it (plan 27 section 14.12 steps 5 and 6)
// and returns the exit code of the post.
func (w *worker) report(doc map[string]any, mode reportMode) int {
	w.setPhase("reporting")
	body := w.finalize(doc)
	sum := sha256.Sum256(body)
	hdr, _ := json.Marshal(struct {
		ExecutionID string `json:"execution_id"`
		Bytes       int    `json:"bytes"`
		SHA256      string `json:"sha256"`
	}{w.execID, len(body), hex.EncodeToString(sum[:])})
	w.logMu.Lock()
	fmt.Fprintf(w.out, "::SC-RESULT-BEGIN:: %s\n%s\n::SC-RESULT-END::\n", hdr, body)
	w.logMu.Unlock()
	w.reported.Store(true)
	if mode == modeNormal {
		w.stopHeartbeat()
	}
	return w.postResult(body, mode)
}
