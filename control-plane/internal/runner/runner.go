package runner

import (
	"bytes"
	"os"
	"time"
)

// Request is what the control plane asks a coding harness to do. It is
// harness-agnostic; each Adapter's Command translates it into that
// specific harness's own argv, using only the fields that harness's CLI
// actually supports (documented per-field below, and per-adapter in
// claudecode.go / codex.go).
type Request struct {
	// Task is the natural-language instruction handed to the coding agent.
	Task string

	// Model is a policy alias already resolved to a concrete model string
	// by the policy package (spec §2.3) before it ever reaches this
	// package. This package never chooses or hardcodes a vendor model.
	Model string

	// MaxTurns bounds how many agentic turns the harness may take, checked
	// against the policy turn limit (docs/reference/coding-harness-notes.md).
	// Claude Code's CLI exposes this directly as --max-turns. Codex's
	// `codex exec` has no documented equivalent flag, so CodexAdapter.Command
	// does not use this field — documented there rather than silently
	// dropped.
	MaxTurns int

	// AllowedTools is the card's allowlist (spec §24), translated into
	// Claude Code's --allowedTools flag. Codex has no equivalent CLI flag:
	// its permissions come entirely from --sandbox, so CodexAdapter.Command
	// does not use this field.
	AllowedTools []string

	// WorkDir is the checked-out repository the harness should run inside
	// (spec §16.2: git is the persistent workspace). It never appears on
	// the generated argv — the caller sets it as exec.Cmd.Dir. Codex in
	// particular refuses to run outside a git repository
	// ("Not inside a trusted directory..."), so the caller must ensure
	// WorkDir is one; no Adapter assumes it can run anywhere.
	WorkDir string
}

// Adapter is the harness-agnostic contract every coding-CLI integration
// satisfies (spec §2.2): the company must not care which harness performed
// the work.
//
// Both Claude Code and Codex read stdin unless it is explicitly closed:
// Codex blocks *indefinitely* on "Reading additional input from
// stdin...", and Claude Code stalls for 3 seconds before proceeding with a
// warning (docs/reference/coding-harness-notes.md). A Kubernetes Job has no
// stdin at all. Command cannot fix this itself — it only returns argv, and
// there is no argv element that means "close stdin". The caller that turns
// this argv into an exec.Cmd MUST explicitly attach a closed/empty stdin to
// every invocation, for example:
//
//	stdin, err := runner.OpenNullStdin()
//	if err != nil { ... }
//	defer stdin.Close()
//	cmd := exec.Command(argv[0], argv[1:]...)
//	cmd.Stdin = stdin
//
// This is load-bearing enough (an unclosed stdin either hangs a Job forever
// or wastes 3 seconds on every single run) that callers should not rely on
// os/exec's implicit "nil Stdin reads from the null device" default —
// set it explicitly, every time.
type Adapter interface {
	// Name identifies the harness, e.g. "claude-code" or "codex". Never
	// empty.
	Name() string

	// Command returns argv for the harness invocation — never a shell
	// string, so there is no shell-injection surface. It must never emit a
	// permission-bypass flag (Claude Code's --dangerously-skip-permissions,
	// or Codex's unrestricted/yolo execution mode): permissions always come
	// from the card's allowlist via Request.AllowedTools and the sandbox
	// mode the adapter itself selects, per spec §13, §14 and §24.
	Command(req Request) []string

	// Parse turns one completed run's raw stdout, process exit code and
	// wall-clock duration into a CodingRunResult. It never returns
	// (nil, nil): input that cannot be trusted at all (empty, unparseable
	// JSON, a stream that stops mid-object) is an error, never a silent
	// success and never a silently zero-value result.
	Parse(stdout []byte, exitCode int, dur time.Duration) (*CodingRunResult, error)
}

// OpenNullStdin opens the OS's null device for reading, for a caller to
// assign explicitly to exec.Cmd.Stdin. See the Adapter doc comment for why
// this must never be left to an implicit default.
func OpenNullStdin() (*os.File, error) {
	return os.Open(os.DevNull)
}

// splitJSONLines splits raw stream-JSON/JSONL output into non-empty,
// trimmed lines. Both harnesses emit one JSON object per line; this is
// shared plumbing common to both adapters, not a parsing decision either
// one makes independently.
func splitJSONLines(data []byte) [][]byte {
	var lines [][]byte
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}
