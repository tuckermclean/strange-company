package runner

import (
	"os"
	"strings"
	"testing"
	"time"
)

// contractCase pairs an Adapter with the forbidden-flag substrings its own
// harness must never emit, and the recorded fixture that exercises a real
// success run for it.
type contractCase struct {
	name         string
	adapter      Adapter
	testdataFile string
	forbidden    []string
}

func contractCases() []contractCase {
	return []contractCase{
		{
			name:         "claude-code",
			adapter:      ClaudeCodeAdapter{},
			testdataFile: "testdata/claude-code-success.jsonl",
			forbidden:    []string{"dangerously-skip-permissions"},
		},
		{
			name:         "codex",
			adapter:      CodexAdapter{},
			testdataFile: "testdata/codex-success.jsonl",
			forbidden:    []string{"yolo", "dangerously-bypass"},
		},
	}
}

// TestAdapters_NameNonEmpty is a shared invariant: every Adapter must
// self-identify.
func TestAdapters_NameNonEmpty(t *testing.T) {
	for _, tc := range contractCases() {
		t.Run(tc.name, func(t *testing.T) {
			if tc.adapter.Name() == "" {
				t.Fatalf("Name() is empty")
			}
		})
	}
}

// TestAdapters_CommandNeverEmitsForbiddenFlags_AndIncludesModel is the
// shared invariant behind spec §13, §14 and §24: neither adapter may ever
// generate a permission-bypass invocation, and both must honour the
// policy-selected model (spec §2.3) rather than hardcoding one.
func TestAdapters_CommandNeverEmitsForbiddenFlags_AndIncludesModel(t *testing.T) {
	for _, tc := range contractCases() {
		t.Run(tc.name, func(t *testing.T) {
			req := Request{
				Task:         "implement the acceptance criteria",
				Model:        "policy-selected-model",
				MaxTurns:     5,
				AllowedTools: []string{"Bash", "Edit"},
				WorkDir:      "/workspace",
			}

			argv := tc.adapter.Command(req)
			if len(argv) == 0 {
				t.Fatalf("Command() returned empty argv")
			}

			joined := strings.ToLower(strings.Join(argv, " "))
			for _, bad := range tc.forbidden {
				if strings.Contains(joined, bad) {
					t.Errorf("argv contains forbidden substring %q: %v", bad, argv)
				}
			}

			found := false
			for _, arg := range argv {
				if arg == req.Model {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("argv does not include the requested model %q: %v", req.Model, argv)
			}
		})
	}
}

// TestAdapters_EmptyInputNeverPanicsNeverSucceeds is a shared invariant:
// input that cannot be trusted at all must always be an error, and Parse
// must never return (nil, nil) for it — a nil result with a nil error
// would be indistinguishable from a valid, empty success.
func TestAdapters_EmptyInputNeverPanicsNeverSucceeds(t *testing.T) {
	for _, tc := range contractCases() {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Parse(empty) panicked: %v", r)
				}
			}()

			result, err := tc.adapter.Parse([]byte(""), 0, 0)
			if err == nil {
				t.Errorf("Parse(empty) returned nil error, want an error")
			}
			if result != nil {
				t.Errorf("Parse(empty) returned a non-nil result alongside an error: %+v", result)
			}
		})
	}
}

// TestAdapters_NilResultNeverPairsWithNilError is the general form of the
// same invariant, exercised against a real success stream as well: no
// matter what Parse returns, "no result and no error" must never happen.
func TestAdapters_NilResultNeverPairsWithNilError(t *testing.T) {
	for _, tc := range contractCases() {
		t.Run(tc.name, func(t *testing.T) {
			data, err := os.ReadFile(tc.testdataFile)
			if err != nil {
				t.Fatalf("reading fixture: %v", err)
			}

			result, err := tc.adapter.Parse(data, 0, time.Second)
			if result == nil && err == nil {
				t.Fatalf("Parse() returned (nil, nil) — a caller cannot distinguish this from a valid empty result")
			}
			if err != nil {
				t.Fatalf("Parse() of a recorded success fixture returned error %v, want nil", err)
			}
		})
	}
}

// TestAdapters_SuccessFixturePopulatesSharedFields is the table-driven
// check the plan calls for: the same assertions run across both adapters
// against their own recorded success fixture, proving both sides of the
// CodingRunResult contract (spec §2.2) independent of harness identity.
func TestAdapters_SuccessFixturePopulatesSharedFields(t *testing.T) {
	for _, tc := range contractCases() {
		t.Run(tc.name, func(t *testing.T) {
			data, err := os.ReadFile(tc.testdataFile)
			if err != nil {
				t.Fatalf("reading fixture: %v", err)
			}

			result, err := tc.adapter.Parse(data, 0, 2*time.Second)
			if err != nil {
				t.Fatalf("Parse() returned error %v, want nil", err)
			}
			if result == nil {
				t.Fatalf("Parse() returned nil result with nil error")
			}

			if result.Harness != tc.adapter.Name() {
				t.Errorf("Harness = %q, want %q", result.Harness, tc.adapter.Name())
			}
			if result.Status != StatusCompleted {
				t.Errorf("Status = %q, want %q for a recorded success run", result.Status, StatusCompleted)
			}
			if result.Summary == "" {
				t.Errorf("Summary is empty, want the run's final text")
			}
			if result.Usage == (Usage{}) {
				t.Errorf("Usage is the zero value, want populated token counts")
			}
			if result.SessionID == "" {
				t.Errorf("SessionID is empty, want the run's correlator")
			}
			if len(result.Denials) != 0 {
				t.Errorf("Denials = %v, want none for a clean success run", result.Denials)
			}
		})
	}
}
