package runner

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestCodexAdapter_Name(t *testing.T) {
	if got := (CodexAdapter{}).Name(); got != "codex" {
		t.Fatalf("Name() = %q, want %q", got, "codex")
	}
}

func TestCodexAdapter_Command_NoForbiddenFlags_IncludesModel(t *testing.T) {
	req := Request{
		Task:     "implement the widget",
		Model:    "gpt-5-codex",
		MaxTurns: 7,
		WorkDir:  "/workspace",
	}

	argv := (CodexAdapter{}).Command(req)

	if len(argv) == 0 {
		t.Fatalf("Command() returned empty argv")
	}
	if argv[0] != "codex" {
		t.Fatalf("argv[0] = %q, want %q", argv[0], "codex")
	}

	foundModel := false
	for _, arg := range argv {
		lower := strings.ToLower(arg)
		if strings.Contains(lower, "yolo") {
			t.Fatalf("argv references Codex's unrestricted/yolo execution mode: %v", argv)
		}
		if strings.Contains(lower, "dangerously-bypass") {
			t.Fatalf("argv references an unrestricted sandbox-bypass flag: %v", argv)
		}
		if arg == req.Model {
			foundModel = true
		}
	}
	if !foundModel {
		t.Fatalf("argv does not include the requested model %q: %v", req.Model, argv)
	}

	// Codex must never run in anything other than the restricted sandbox
	// this adapter selects.
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--sandbox workspace-write") {
		t.Fatalf("argv does not select the workspace-write sandbox: %v", argv)
	}
}

// TestCodexAdapter_Parse_SuccessFixture parses the recorded real output in
// testdata/codex-success.jsonl. The point of this test is CostUSD: Codex
// reports tokens only, so CostUSD must come back nil, never 0.
func TestCodexAdapter_Parse_SuccessFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/codex-success.jsonl")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}

	result, err := (CodexAdapter{}).Parse(data, 0, 1500*time.Millisecond)
	if err != nil {
		t.Fatalf("Parse() returned error %v, want nil", err)
	}
	if result == nil {
		t.Fatalf("Parse() returned nil result with nil error")
	}

	if result.Status != StatusCompleted {
		t.Errorf("Status = %q, want %q", result.Status, StatusCompleted)
	}
	if result.Harness != "codex" {
		t.Errorf("Harness = %q, want %q", result.Harness, "codex")
	}
	if result.SessionID != "00000000-0000-4000-8000-000000000000" {
		t.Errorf("SessionID = %q, want the fixture's thread_id", result.SessionID)
	}
	if result.Summary != "ok" {
		t.Errorf("Summary = %q, want %q (item.completed's text)", result.Summary, "ok")
	}

	if result.CostUSD != nil {
		t.Fatalf("CostUSD = %v, want nil — Codex reports tokens only, never a cost figure", *result.CostUSD)
	}
	if len(result.Denials) != 0 {
		t.Errorf("Denials = %v, want none — Codex has no denial-reporting field at all", result.Denials)
	}

	wantUsage := Usage{
		InputTokens:         13384,
		OutputTokens:        5,
		CachedInputTokens:   9600,
		CacheCreationTokens: 0,
		ReasoningTokens:     0,
	}
	if result.Usage != wantUsage {
		t.Errorf("Usage = %+v, want %+v", result.Usage, wantUsage)
	}

	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
	if result.DurationMS != 1500 {
		t.Errorf("DurationMS = %d, want 1500", result.DurationMS)
	}
}

func TestCodexAdapter_Parse_EmptyInput_Errors(t *testing.T) {
	result, err := (CodexAdapter{}).Parse([]byte(""), 0, 0)
	if err == nil {
		t.Fatalf("Parse(empty) returned nil error, want an error")
	}
	if result != nil {
		t.Fatalf("Parse(empty) returned a non-nil result alongside an error: %+v", result)
	}
}

func TestCodexAdapter_Parse_MalformedInput_Errors(t *testing.T) {
	result, err := (CodexAdapter{}).Parse([]byte("{this is not valid json"), 0, 0)
	if err == nil {
		t.Fatalf("Parse(malformed) returned nil error, want an error")
	}
	if result != nil {
		t.Fatalf("Parse(malformed) returned a non-nil result alongside an error: %+v", result)
	}
}

// TestCodexAdapter_Parse_NoTerminalEvent_ExitZero_IsFailed covers the
// asymmetry stated in docs/reference/coding-harness-notes.md: Codex has no
// is_error/subtype field, so success has to come from the exit code PLUS
// the presence of a terminal turn.completed. A stream that ends without one
// is a failure even on exit 0 — unlike Claude Code's equivalent scenario,
// this is a real Status, not a Go error, because every individual line
// here is well-formed JSON; the run simply never completed a turn.
func TestCodexAdapter_Parse_NoTerminalEvent_ExitZero_IsFailed(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"sess-incomplete"}`,
		`{"type":"turn.started"}`,
	}, "\n")

	result, err := (CodexAdapter{}).Parse([]byte(stream), 0, time.Second)
	if err != nil {
		t.Fatalf("Parse() returned error %v, want nil", err)
	}
	if result == nil {
		t.Fatalf("Parse() returned nil result with nil error")
	}
	if result.Status != StatusFailed {
		t.Fatalf("Status = %q, want %q (exit 0 does not mean success without a terminal turn.completed)", result.Status, StatusFailed)
	}
	if result.CostUSD != nil {
		t.Fatalf("CostUSD = %v, want nil", *result.CostUSD)
	}
}

// TestCodexAdapter_Parse_NoTerminalEvent_NonZeroExit_InfraError covers
// spec §12.1 for Codex: a non-zero exit with no terminal event is
// infrastructure failure, not an implementation failure.
func TestCodexAdapter_Parse_NoTerminalEvent_NonZeroExit_InfraError(t *testing.T) {
	stream := `{"type":"thread.started","thread_id":"sess-killed"}`

	result, err := (CodexAdapter{}).Parse([]byte(stream), 1, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("Parse() returned error %v, want nil (infra failures are a Status, not a Go error)", err)
	}
	if result == nil {
		t.Fatalf("Parse() returned nil result with nil error")
	}
	if result.Status != StatusInfraError {
		t.Fatalf("Status = %q, want %q", result.Status, StatusInfraError)
	}
}
