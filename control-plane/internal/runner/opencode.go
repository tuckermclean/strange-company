package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// OpenCodeAdapter integrates opencode (opencode.ai), an OpenAI-compatible
// coding harness.
//
// It exists so the escalation ladder can run on a provider that is not
// Anthropic or OpenAI -- spec §2.2's whole point is that the company must not
// care which harness performed the work. A chat-completion provider like
// DeepSeek cannot implement anything on its own: it has no working tree and no
// branch. opencode gives it both.
//
// VERSION MATTERS. --format was added after opencode 0.4.x: an older build
// rejects it, prints the `run` help and exits non-zero, which reaches the
// control plane as "produced no readable events" and looks exactly like a
// dropped event stream. The runner image pins a version that has it.
//
// Provider configuration lives in opencode.json in the workspace, written by
// the runner entrypoint, not on this argv. That is deliberate: an API key on a
// command line is visible in every process listing in the container.
type OpenCodeAdapter struct{}

// Name implements Adapter.
func (OpenCodeAdapter) Name() string { return "opencode" }

// Command implements Adapter.
//
// It never passes --auto. opencode documents that flag as "auto-approve
// permissions that are not explicitly denied", which is a permission bypass of
// exactly the kind this interface forbids (§13, §14, §24) -- the same reason
// Claude Code's --dangerously-skip-permissions and Codex's unrestricted mode
// are absent from their adapters.
//
// Permissions instead come from the `permission` block of opencode.json, which
// the entrypoint writes with explicit allow/deny values and never "ask": an
// "ask" in a Job with no terminal is a run that waits for an answer nobody can
// give.
//
// Request.MaxTurns and Request.AllowedTools have no opencode CLI equivalent in
// the documented flag set, so they are not used here -- stated rather than
// silently dropped.
func (OpenCodeAdapter) Command(req Request) []string {
	return []string{
		"opencode", "run",
		"--format", "json",
		// Logs to stderr, JSON to stdout. A Job's pod log merges the two,
		// and Parse skips lines it cannot read -- so this costs nothing
		// and means a run that dies before emitting any event still
		// explains itself. The first real run produced no events and no
		// reason, which took a round trip to diagnose.
		"--print-logs", "--log-level", "INFO",
		"--model", req.Model,
		req.Task,
	}
}

// openCodeEvent is the subset of opencode's newline-delimited JSON events this
// adapter reads. Other event types and fields are ignored, not rejected.
type openCodeEvent struct {
	Type string `json:"type"`
	Text string `json:"text"`

	Tokens *struct {
		Input     int `json:"input"`
		Output    int `json:"output"`
		Reasoning int `json:"reasoning"`
		Cache     struct {
			Read  int `json:"read"`
			Write int `json:"write"`
		} `json:"cache"`
	} `json:"tokens"`

	// Cost is a pointer so "not reported" stays distinguishable from zero.
	Cost *float64 `json:"cost"`
}

// Parse implements Adapter.
//
// One behaviour here is a deliberate concession to a documented upstream bug:
// opencode can exit before emitting its final step_finish event, and drops
// text and step-finish events specifically in containerised environments --
// which is exactly where this runs (anomalyco/opencode issues 26855, 31435).
//
// So a stream with no step_finish is still a completed run when the process
// exited zero. Reporting it as unparseable would make every successful run an
// infrastructure failure and the escalation ladder would never move. The cost
// stays nil rather than 0: a ledger that silently records zero is worse than
// one that honestly records nothing.
func (OpenCodeAdapter) Parse(stdout []byte, exitCode int, dur time.Duration) (*CodingRunResult, error) {
	result := &CodingRunResult{
		Harness:    "opencode",
		ExitCode:   exitCode,
		DurationMS: dur.Milliseconds(),
		Raw:        stdout,
	}

	var (
		text      []string
		sawEvent  bool
		sawFinish bool
	)

	for _, line := range splitJSONLines(stdout) {
		var ev openCodeEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			// A single malformed line is not a malformed run: the stream
			// is append-only and a truncated tail is common.
			continue
		}
		sawEvent = true

		switch ev.Type {
		case "text":
			if t := strings.TrimSpace(ev.Text); t != "" {
				text = append(text, t)
			}
		case "step_finish":
			sawFinish = true
			if ev.Tokens != nil {
				result.Usage.InputTokens = ev.Tokens.Input
				result.Usage.OutputTokens = ev.Tokens.Output
				result.Usage.CachedInputTokens = ev.Tokens.Cache.Read
				result.Usage.CacheCreationTokens = ev.Tokens.Cache.Write
				result.Usage.ReasoningTokens = ev.Tokens.Reasoning
			}
			if ev.Cost != nil {
				cost := *ev.Cost
				result.CostUSD = &cost
			}
		}
	}

	if !sawEvent {
		return nil, errors.New("runner: opencode produced no readable events")
	}

	result.Summary = strings.Join(text, "\n")
	if result.Summary == "" {
		result.Summary = fmt.Sprintf("opencode exited %d with no narrative output", exitCode)
	}

	if exitCode == 0 {
		result.Status = StatusCompleted
	} else {
		result.Status = StatusFailed
	}

	if !sawFinish {
		// Recorded so a reader of the ledger knows the accounting is
		// incomplete rather than assuming the run was free.
		result.Summary += "\n\n(opencode exited without a step_finish event; token and cost accounting for this run is missing)"
	}

	return result, nil
}
