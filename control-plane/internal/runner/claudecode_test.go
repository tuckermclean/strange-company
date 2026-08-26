package runner

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestClaudeCodeAdapter_Name(t *testing.T) {
	if got := (ClaudeCodeAdapter{}).Name(); got != "claude-code" {
		t.Fatalf("Name() = %q, want %q", got, "claude-code")
	}
}

func TestClaudeCodeAdapter_Command_NoForbiddenFlags_IncludesModel(t *testing.T) {
	req := Request{
		Task:         "implement the widget",
		Model:        "claude-opus-5",
		MaxTurns:     7,
		AllowedTools: []string{"Bash", "Edit", "Read"},
		WorkDir:      "/workspace",
	}

	argv := (ClaudeCodeAdapter{}).Command(req)

	if len(argv) == 0 {
		t.Fatalf("Command() returned empty argv")
	}
	if argv[0] != "claude" {
		t.Fatalf("argv[0] = %q, want %q", argv[0], "claude")
	}

	foundModel := false
	for _, arg := range argv {
		if arg == "--dangerously-skip-permissions" {
			t.Fatalf("argv contains forbidden flag --dangerously-skip-permissions: %v", argv)
		}
		if strings.Contains(arg, "dangerously-skip-permissions") {
			t.Fatalf("argv element %q references the forbidden permission-bypass flag: %v", arg, argv)
		}
		if arg == req.Model {
			foundModel = true
		}
	}
	if !foundModel {
		t.Fatalf("argv does not include the requested model %q: %v", req.Model, argv)
	}
}

// TestClaudeCodeAdapter_Parse_SuccessFixture parses the recorded real
// output in testdata/claude-code-success.jsonl and asserts the full
// result: status, session id, cost, tokens, summary, harness.
func TestClaudeCodeAdapter_Parse_SuccessFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/claude-code-success.jsonl")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}

	result, err := (ClaudeCodeAdapter{}).Parse(data, 0, 3898*time.Millisecond)
	if err != nil {
		t.Fatalf("Parse() returned error %v, want nil", err)
	}
	if result == nil {
		t.Fatalf("Parse() returned nil result with nil error")
	}

	if result.Status != StatusCompleted {
		t.Errorf("Status = %q, want %q", result.Status, StatusCompleted)
	}
	if result.Harness != "claude-code" {
		t.Errorf("Harness = %q, want %q", result.Harness, "claude-code")
	}
	if result.SessionID != "00000000-0000-4000-8000-000000000000" {
		t.Errorf("SessionID = %q, want the fixture's session_id", result.SessionID)
	}
	if result.Summary != "ok" {
		t.Errorf("Summary = %q, want %q", result.Summary, "ok")
	}
	if len(result.Denials) != 0 {
		t.Errorf("Denials = %v, want none for a clean success run", result.Denials)
	}

	if result.CostUSD == nil {
		t.Fatalf("CostUSD = nil, want a non-nil pointer matching the fixture's total_cost_usd")
	}
	if *result.CostUSD != 0.239828 {
		t.Errorf("*CostUSD = %v, want 0.239828", *result.CostUSD)
	}

	wantUsage := Usage{
		InputTokens:         2,
		OutputTokens:        4,
		CachedInputTokens:   15936,
		CacheCreationTokens: 23175,
	}
	if result.Usage != wantUsage {
		t.Errorf("Usage = %+v, want %+v", result.Usage, wantUsage)
	}

	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
	if result.DurationMS != 3898 {
		t.Errorf("DurationMS = %d, want 3898", result.DurationMS)
	}
}

// TestClaudeCodeAdapter_Parse_PermissionDenial is the load-bearing test:
// spec §24 says an attempted forbidden action blocks the card, and it is
// explicitly NOT something another model should "try harder" to get past.
// The recorded success fixture's permission_denials array is always empty,
// so this stream is synthesised here as string literals to exercise the
// non-empty case.
func TestClaudeCodeAdapter_Parse_PermissionDenial(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"sess-denied"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"attempting forbidden action"}]}}`,
		`{"type":"result","is_error":false,"subtype":"success","session_id":"sess-denied","result":"blocked",` +
			`"total_cost_usd":0.01,` +
			`"usage":{"input_tokens":1,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":0},` +
			`"permission_denials":[{"tool_name":"Bash","reason":"blocked: rm -rf /"}]}`,
	}, "\n")

	result, err := (ClaudeCodeAdapter{}).Parse([]byte(stream), 0, time.Second)
	if err != nil {
		t.Fatalf("Parse() returned error %v, want nil", err)
	}
	if result == nil {
		t.Fatalf("Parse() returned nil result with nil error")
	}

	if result.Status != StatusPolicyViolation {
		t.Fatalf("Status = %q, want %q (is_error was false — denials must win regardless)", result.Status, StatusPolicyViolation)
	}
	if len(result.Denials) != 1 {
		t.Fatalf("Denials = %v, want exactly one", result.Denials)
	}
	if result.Denials[0].Tool != "Bash" {
		t.Errorf("Denials[0].Tool = %q, want %q", result.Denials[0].Tool, "Bash")
	}
	if result.Denials[0].Detail != "blocked: rm -rf /" {
		t.Errorf("Denials[0].Detail = %q, want %q", result.Denials[0].Detail, "blocked: rm -rf /")
	}
}

func TestClaudeCodeAdapter_Parse_EmptyInput_Errors(t *testing.T) {
	result, err := (ClaudeCodeAdapter{}).Parse([]byte(""), 0, 0)
	if err == nil {
		t.Fatalf("Parse(empty) returned nil error, want an error")
	}
	if result != nil {
		t.Fatalf("Parse(empty) returned a non-nil result alongside an error: %+v", result)
	}
}

func TestClaudeCodeAdapter_Parse_MalformedInput_Errors(t *testing.T) {
	result, err := (ClaudeCodeAdapter{}).Parse([]byte("{this is not valid json"), 0, 0)
	if err == nil {
		t.Fatalf("Parse(malformed) returned nil error, want an error")
	}
	if result != nil {
		t.Fatalf("Parse(malformed) returned a non-nil result alongside an error: %+v", result)
	}
}

// TestClaudeCodeAdapter_Parse_TruncatedStream_ExitZero_Errors covers a
// stream of otherwise well-formed events that never reaches its terminal
// `result` event, with a clean exit code. This must never be treated as a
// silent success.
func TestClaudeCodeAdapter_Parse_TruncatedStream_ExitZero_Errors(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"sess-cut"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"partial"}]}}`,
	}, "\n")

	result, err := (ClaudeCodeAdapter{}).Parse([]byte(stream), 0, time.Second)
	if err == nil {
		t.Fatalf("Parse(truncated, exit 0) returned nil error, want an error")
	}
	if result != nil {
		t.Fatalf("Parse(truncated, exit 0) returned a non-nil result alongside an error: %+v", result)
	}
}

// TestClaudeCodeAdapter_Parse_NoTerminalEvent_NonZeroExit_InfraError covers
// spec §12.1: a non-zero exit with no terminal event is infrastructure
// failure, not an implementation failure, and must not burn a model-tier
// attempt. This is a real (non-error) result, deliberately distinct from
// the exit-0 truncation case above.
func TestClaudeCodeAdapter_Parse_NoTerminalEvent_NonZeroExit_InfraError(t *testing.T) {
	stream := `{"type":"system","subtype":"init","session_id":"sess-killed"}`

	result, err := (ClaudeCodeAdapter{}).Parse([]byte(stream), 137, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("Parse() returned error %v, want nil (infra failures are a Status, not a Go error)", err)
	}
	if result == nil {
		t.Fatalf("Parse() returned nil result with nil error")
	}
	if result.Status != StatusInfraError {
		t.Fatalf("Status = %q, want %q", result.Status, StatusInfraError)
	}
	if result.Status == StatusFailed {
		t.Fatalf("infra failure must never be classified as StatusFailed: it must not burn a model-tier attempt")
	}
	if result.ExitCode != 137 {
		t.Errorf("ExitCode = %d, want 137", result.ExitCode)
	}
}
