// Package runner defines the harness-agnostic contract that both coding
// adapters (Claude Code, Codex) satisfy: one CodingRunResult regardless of
// which CLI performed the work (spec §2.2), so the rest of the control
// plane never branches on harness identity.
//
// The adapters in this package are built exclusively against recorded real
// CLI output (control-plane/internal/runner/testdata/) and
// docs/reference/coding-harness-notes.md, not against recollection or
// vendor documentation. Four of the six defects found in earlier
// milestones came from guessing an API shape — these adapters are built
// against recorded output instead.
package runner

// Status is the terminal disposition of one coding-harness run. It is a
// closed set: every code path in every adapter's Parse must resolve to
// exactly one of these values, or Parse must return an error for input
// that cannot be trusted at all (empty, malformed, or truncated output).
type Status string

const (
	// StatusCompleted: the harness ran, reached a terminal event, and that
	// event reported success.
	StatusCompleted Status = "completed"

	// StatusFailed: the harness reached a terminal event (or, for Codex,
	// the process exited) but the run did not succeed. This is a genuine
	// implementation attempt per spec §12.1 and burns a model-tier attempt
	// on the escalation ladder.
	StatusFailed Status = "failed"

	// StatusPolicyViolation: the run attempted a forbidden action. Spec
	// §24 requires this to block the card outright (state -> Blocked,
	// reason POLICY_VIOLATION). It is explicitly NOT something another
	// model should "try harder" to get past, so an adapter must never fold
	// this into StatusFailed and let it be retried like an ordinary
	// implementation failure.
	StatusPolicyViolation Status = "policy_violation"

	// StatusTimeout: the run was cut off by the caller's wall-clock
	// budget. Neither adapter can infer this from a harness's own output —
	// a process killed for exceeding its deadline does not get to emit a
	// terminal event explaining why it was killed. The caller that owns
	// the context deadline (the Job controller, spec §16.1) is responsible
	// for setting this status when it terminates a run early; nothing in
	// this package's Parse implementations ever produces it from stdout or
	// exit code alone.
	StatusTimeout Status = "timeout"

	// StatusInfraError: the harness process exited abnormally without ever
	// producing a terminal event. Spec §12.1 is explicit that provider
	// outages, Kubernetes scheduling failures, runner image pull failures
	// and authentication outages must not burn a model-tier attempt, so
	// this is kept distinct from StatusFailed rather than collapsed into
	// it.
	StatusInfraError Status = "infra_error"
)

// Denial is one attempted forbidden action reported by a harness. Today
// only Claude Code's `permission_denials` array can populate this; see the
// commentary in codex.go for why Codex cannot report the same thing.
type Denial struct {
	Tool   string
	Detail string
}

// Usage normalises token accounting across harnesses. Not every harness
// populates every field:
//
//   - Codex's `cache_write_input_tokens` is mapped onto CacheCreationTokens
//     (Claude Code's analogous field is `cache_creation_input_tokens`) —
//     the two harnesses name the same concept differently.
//   - Claude Code's terminal `result` event usage object, as recorded in
//     docs/reference/coding-harness-notes.md and the success fixture, has
//     no reasoning-token field, so ReasoningTokens is always 0 for the
//     Claude Code adapter.
type Usage struct {
	InputTokens         int
	OutputTokens        int
	CachedInputTokens   int
	CacheCreationTokens int
	ReasoningTokens     int
}

// CodingRunResult is the one shape both adapters produce, per spec §2.2:
// the rest of the control plane must not care which harness performed the
// work.
type CodingRunResult struct {
	Status       Status
	Harness      string
	Model        string
	SessionID    string
	Summary      string
	ChangedFiles []string
	Usage        Usage

	// CostUSD is a pointer on purpose. Claude Code reports total_cost_usd
	// directly on its terminal result event; Codex reports tokens only, and
	// its cost has to be computed downstream from a price table (spec
	// §22). nil means "this harness does not report cost" and must never
	// collapse to a recorded 0 — a cost ledger that silently records zero
	// for every Codex run is worse than one that honestly records nothing.
	CostUSD *float64

	// Denials holds attempted forbidden actions. A non-empty Denials
	// implies Status == StatusPolicyViolation. Always empty for Codex today
	// (see codex.go) — that is a real gap in detection coverage, stated
	// explicitly rather than papered over.
	Denials []Denial

	ExitCode   int
	DurationMS int64

	// Raw is the exact bytes Parse was given, kept for audit (spec §21) and
	// so a parsing mistake in this package can be diagnosed against the
	// original stream rather than a lossy summary of it.
	Raw []byte
}
