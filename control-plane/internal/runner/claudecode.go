package runner

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ClaudeCodeAdapter drives `claude -p --output-format stream-json
// --verbose`. Its event stream and terminal `result` event contract are
// recorded in docs/reference/coding-harness-notes.md, captured from
// claude-code-cli 2.1.246 rather than assumed from documentation:
//
//	system/init  system/hook_started  system/hook_response  assistant
//	rate_limit_event  result/success
//
// Only the terminal `result` event is this adapter's contract — the other
// event types (system/init's tool/mcp/model advertisement, hook events,
// assistant message deltas, rate_limit_event) are informational per the
// notes doc and are not modeled here. See Parse for what is deliberately
// not extracted from testdata/claude-code-success.jsonl.
type ClaudeCodeAdapter struct{}

// Name implements Adapter.
func (ClaudeCodeAdapter) Name() string { return "claude-code" }

// Command implements Adapter. It never emits --dangerously-skip-permissions;
// permissions come entirely from Request.AllowedTools (spec §13, §24).
func (ClaudeCodeAdapter) Command(req Request) []string {
	return []string{
		"claude",
		"-p", req.Task,
		"--model", req.Model,
		"--output-format", "stream-json",
		"--verbose",
		"--max-turns", strconv.Itoa(req.MaxTurns),
		"--allowedTools", strings.Join(req.AllowedTools, ","),
	}
}

// claudeUsage mirrors the subset of the terminal result event's `usage`
// object docs/reference/coding-harness-notes.md calls out as the contract:
// input_tokens, output_tokens, cache_read_input_tokens,
// cache_creation_input_tokens. testdata/claude-code-success.jsonl's usage
// object also carries output_tokens_details, server_tool_use,
// service_tier, cache_creation, inference_geo, iterations and speed; none
// of those are modeled here because the notes doc's table does not list
// them as part of the contract (it lists the four above plus an explicit
// "…").
type claudeUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// claudeResultEvent mirrors the terminal `result` event fields called out
// in docs/reference/coding-harness-notes.md. `modelUsage`,
// `duration_ms`/`duration_api_ms`, `num_turns` and `terminal_reason` are
// present in the fixture but are not modeled onto CodingRunResult: the
// notes doc lists modelUsage as a per-model breakdown of totals already
// captured by Usage and CostUSD, and duration/num_turns are informational
// run timing/turn-count fields the notes doc lists separately from the
// success-detection contract (is_error/subtype/permission_denials).
type claudeResultEvent struct {
	Type              string            `json:"type"`
	Subtype           string            `json:"subtype"`
	IsError           bool              `json:"is_error"`
	StopReason        string            `json:"stop_reason"`
	SessionID         string            `json:"session_id"`
	Result            string            `json:"result"`
	TotalCostUSD      *float64          `json:"total_cost_usd"`
	Usage             claudeUsage       `json:"usage"`
	PermissionDenials []json.RawMessage `json:"permission_denials"`
}

// Parse implements Adapter.
func (a ClaudeCodeAdapter) Parse(stdout []byte, exitCode int, dur time.Duration) (*CodingRunResult, error) {
	lines := splitJSONLines(stdout)
	if len(lines) == 0 {
		return nil, fmt.Errorf("claudecode: empty output, cannot parse a run with no events")
	}

	var terminal *claudeResultEvent
	for i, line := range lines {
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			return nil, fmt.Errorf("claudecode: malformed event at line %d: %w", i+1, err)
		}
		if probe.Type != "result" {
			continue
		}
		var ev claudeResultEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, fmt.Errorf("claudecode: malformed result event at line %d: %w", i+1, err)
		}
		terminal = &ev
	}

	result := &CodingRunResult{
		Harness:    a.Name(),
		ExitCode:   exitCode,
		DurationMS: dur.Milliseconds(),
		Raw:        stdout,
	}

	if terminal == nil {
		// No terminal `result` event anywhere in the stream.
		if exitCode != 0 {
			// Spec §12.1: a non-zero exit with no terminal event is
			// infrastructure failure (provider outage, scheduling
			// failure, image pull failure, auth outage, ...), not an
			// implementation attempt. It must not burn a model-tier
			// attempt, so this is a real (non-error) result rather than
			// a parse failure.
			result.Status = StatusInfraError
			return result, nil
		}
		// Exit 0 but the stream never reached its terminal event: this is
		// truncated/malformed input and must never be treated as a silent
		// success.
		return nil, fmt.Errorf("claudecode: stream ended without a terminal result event (exit %d)", exitCode)
	}

	result.SessionID = terminal.SessionID
	result.Summary = terminal.Result
	result.CostUSD = terminal.TotalCostUSD
	result.Usage = Usage{
		InputTokens:         terminal.Usage.InputTokens,
		OutputTokens:        terminal.Usage.OutputTokens,
		CachedInputTokens:   terminal.Usage.CacheReadInputTokens,
		CacheCreationTokens: terminal.Usage.CacheCreationInputTokens,
	}

	for _, raw := range terminal.PermissionDenials {
		result.Denials = append(result.Denials, parseClaudeDenial(raw))
	}

	switch {
	case len(result.Denials) > 0:
		// Spec §24: an attempted forbidden action blocks the card. Checked
		// before is_error so a policy violation can never be reclassified
		// as an ordinary (retryable) StatusFailed — it is not something
		// another model should "try harder" to get past.
		result.Status = StatusPolicyViolation
	case terminal.IsError:
		result.Status = StatusFailed
	default:
		result.Status = StatusCompleted
	}

	return result, nil
}

// parseClaudeDenial extracts a Denial from one entry of
// permission_denials. docs/reference/coding-harness-notes.md confirms the
// field exists and is authoritative for detecting an attempted forbidden
// action, but testdata/claude-code-success.jsonl's array is always empty,
// so the per-entry schema is NOT confirmed by recorded output — only the
// array's presence/emptiness is. This is therefore a best-effort
// extraction against plausible key names rather than a trusted parse of a
// known shape, and it falls back to the raw JSON so no information is
// silently dropped when the guessed keys don't match.
func parseClaudeDenial(raw json.RawMessage) Denial {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return Denial{Detail: string(raw)}
	}

	var d Denial
	for _, key := range []string{"tool_name", "tool", "toolName"} {
		if v, ok := fields[key]; ok {
			var s string
			if json.Unmarshal(v, &s) == nil {
				d.Tool = s
				break
			}
		}
	}
	for _, key := range []string{"reason", "message", "detail"} {
		if v, ok := fields[key]; ok {
			var s string
			if json.Unmarshal(v, &s) == nil {
				d.Detail = s
				break
			}
		}
	}
	if d.Tool == "" && d.Detail == "" {
		d.Detail = string(raw)
	}
	return d
}
