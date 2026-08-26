package runner

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// CodexAdapter drives `codex exec --json --sandbox workspace-write`. Its
// event stream is recorded in docs/reference/coding-harness-notes.md,
// captured from codex-cli 0.149.1:
//
//	thread.started  turn.started  item.completed  turn.completed
//
// Codex reports no equivalent of Claude Code's permission_denials: no
// observed event on any recorded stream names an attempted forbidden
// action. Detecting a Codex policy violation is therefore structurally
// weaker than for Claude Code — it could only ever be inferred indirectly
// from sandbox/exit-code behaviour, never read as a first-class field — and
// CodexAdapter.Parse never sets StatusPolicyViolation or populates Denials.
// That gap is deliberate and stated here rather than papered over with a
// guessed field that was never observed in testdata/codex-success.jsonl.
type CodexAdapter struct{}

// Name implements Adapter.
func (CodexAdapter) Name() string { return "codex" }

// Command implements Adapter. It never enables Codex's unrestricted/yolo
// execution mode; the sandbox is always workspace-write, per spec §14 and
// §24. Codex has no CLI flag in the recorded contract for a
// Request.AllowedTools-style allowlist — its permissions come entirely
// from --sandbox, so that field is not used here. Request.MaxTurns
// likewise has no documented Codex equivalent and is not used.
//
// Codex also refuses to run outside a git repository ("Not inside a
// trusted directory and --skip-git-repo-check was not specified"). This
// adapter does not pass --skip-git-repo-check: the caller is responsible
// for running it inside the checked-out agent branch (Request.WorkDir),
// which spec §16.2 guarantees is a real git repository.
func (CodexAdapter) Command(req Request) []string {
	return []string{
		"codex", "exec",
		"--json",
		"--sandbox", "workspace-write",
		"--model", req.Model,
		req.Task,
	}
}

// codexUsage mirrors turn.completed's usage object exactly as recorded in
// docs/reference/coding-harness-notes.md: input_tokens, cached_input_tokens,
// cache_write_input_tokens, output_tokens, reasoning_output_tokens.
type codexUsage struct {
	InputTokens           int `json:"input_tokens"`
	CachedInputTokens     int `json:"cached_input_tokens"`
	CacheWriteInputTokens int `json:"cache_write_input_tokens"`
	OutputTokens          int `json:"output_tokens"`
	ReasoningOutputTokens int `json:"reasoning_output_tokens"`
}

// codexItem mirrors item.completed's item.{id, text, type}, per the notes
// doc.
type codexItem struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Text string `json:"text"`
}

// codexEvent is a union of the four observed Codex event shapes. Only the
// fields relevant to each event's type are populated by the CLI; unrelated
// fields are simply absent from that line's JSON and left zero-valued here.
type codexEvent struct {
	Type     string     `json:"type"`
	ThreadID string     `json:"thread_id"`
	Item     codexItem  `json:"item"`
	Usage    codexUsage `json:"usage"`
}

// Parse implements Adapter.
func (a CodexAdapter) Parse(stdout []byte, exitCode int, dur time.Duration) (*CodingRunResult, error) {
	lines := splitJSONLines(stdout)
	if len(lines) == 0 {
		return nil, fmt.Errorf("codex: empty output, cannot parse a run with no events")
	}

	result := &CodingRunResult{
		Harness:    a.Name(),
		ExitCode:   exitCode,
		DurationMS: dur.Milliseconds(),
		Raw:        stdout,
		// Codex reports tokens only, never a cost figure, on any observed
		// event. CostUSD is therefore always nil for this adapter — the
		// caller must compute cost downstream from a price table (spec
		// §22) — and must never default it to a recorded 0.
		CostUSD: nil,
	}

	var sessionID string
	var summaries []string
	var terminal *codexEvent

	for i, line := range lines {
		var ev codexEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, fmt.Errorf("codex: malformed event at line %d: %w", i+1, err)
		}
		switch ev.Type {
		case "thread.started":
			sessionID = ev.ThreadID
		case "item.completed":
			if ev.Item.Text != "" {
				summaries = append(summaries, ev.Item.Text)
			}
		case "turn.completed":
			completed := ev
			terminal = &completed
		}
	}

	result.SessionID = sessionID
	result.Summary = strings.Join(summaries, "\n\n")

	if terminal == nil {
		// No terminal turn.completed anywhere in the stream.
		if exitCode != 0 {
			// Spec §12.1: infra failure, not an implementation attempt —
			// must not burn a model-tier attempt.
			result.Status = StatusInfraError
			return result, nil
		}
		// Codex has no is_error/subtype field on turn.completed at all
		// (unlike Claude Code): success has to come from the process exit
		// code plus the presence of a terminal turn.completed. A clean
		// exit that never reached one is still a failure, not a silent
		// success — the exit code alone cannot be trusted.
		result.Status = StatusFailed
		return result, nil
	}

	result.Usage = Usage{
		InputTokens:         terminal.Usage.InputTokens,
		OutputTokens:        terminal.Usage.OutputTokens,
		CachedInputTokens:   terminal.Usage.CachedInputTokens,
		CacheCreationTokens: terminal.Usage.CacheWriteInputTokens,
		ReasoningTokens:     terminal.Usage.ReasoningOutputTokens,
	}

	if exitCode == 0 {
		result.Status = StatusCompleted
	} else {
		// A terminal event was reached, but the process itself still
		// exited non-zero. Codex has no is_error field to consult, so the
		// exit code is the only remaining success signal once a terminal
		// event exists.
		result.Status = StatusFailed
	}

	return result, nil
}
