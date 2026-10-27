// Package engine is the bootstrap phase engine (plan 50 sections 8, 16 and 17): the journal with its local and S3
// copies, the run lock, the phase runner with input digests, freeze rules, retry ceiling and the crash hook, and
// `status`.
package engine

import (
	"context"
	"io"
	"time"

	"strange.company/sc/internal/bootstrap/cli"
	"strange.company/sc/internal/bootstrap/redact"
	"strange.company/sc/internal/bootstrap/store"
	"strange.company/sc/internal/clock"
)

// Config is everything one engine instance needs.
//
// SC-STUB(F08 50-AC-5 50-AC-6 50-AC-8 50-AC-9 50-AC-16 50-AC-17)
type Config struct {
	// Dir is the local .bootstrap directory: journal.json, run.lock, runs/<run-id>/.
	Dir string
	// Store is the S3 side (keys below <Cluster>/); nil before the bucket exists: local journal and lock only.
	Store store.S3
	// IfMatch is true when the conformance run recorded If-Match support (plan 50 section 17.3).
	IfMatch bool
	Clock   clock.Clock
	// Rand feeds the run id suffix (nil: crypto/rand); the suffix is the hex of the first two bytes read.
	Rand io.Reader

	Cluster, InstallUID, Provider, Profile, ConfigDigest string
	// Command is the journal run command (default "run").
	Command string
	// User and Host identify the operator in the journal and in the lock object.
	User, Host string
	// Version, GitHead, ToolboxDigest and ToolboxSource fill the journal run entry.
	Version, GitHead, ToolboxDigest, ToolboxSource string

	// Env carries SC_TEST_MODE and SC_TEST_CRASH_AT (plan 50 section 22); the engine never reads os.Environ.
	Env map[string]string
	// Kill delivers the crash (default: SIGKILL to this process). Tests inject a seam.
	Kill func()
	// Redactor is applied to every byte written to logs, tool logs, reports, the journal and S3 copies.
	Redactor *redact.Redactor
	// SourceSwitched probes reality: the root Kustomization's sourceRef.kind is GitRepository (plan 50 section 17.1
	// rule 5). nil means not switched.
	SourceSwitched func(ctx context.Context) (bool, error)
}

// CheckState is the outcome of a Check.
//
// SC-STUB(F08 50-AC-16 50-AC-17)
type CheckState int

// The Check outcomes.
//
// SC-STUB(F08 50-AC-16 50-AC-17)
const (
	NotDone CheckState = iota
	Done
	Drifted
)

// Check is the result of the idempotency probe against the real world.
//
// SC-STUB(F08 50-AC-16 50-AC-17)
type Check struct {
	State  CheckState
	Reason string
}

// OutputRef points at something a phase produced.
//
// SC-STUB(F08 50-AC-9)
type OutputRef struct {
	Kind, Ref, Sha256 string
}

// Unit is a phase or a step (plan 50 section 8.1).
//
// SC-STUB(F08 50-AC-16 50-AC-17)
type Unit interface {
	Name() string
	// Inputs is hashed (canonical JSON) into the input digest.
	Inputs(ctx context.Context) (any, error)
	Check(ctx context.Context) (Check, error)
	Run(ctx context.Context) ([]OutputRef, error)
	Timeout() time.Duration
	Retryable(err error) bool
}

// Phase is a Unit with optional steps. With steps, the engine runs Check and Run of each step in order and never
// calls the phase's own Check or Run; the phase digest still comes from the phase's Inputs.
//
// SC-STUB(F08 50-AC-16 50-AC-17)
type Phase interface {
	Unit
	Steps() []Unit
}

// Pipeline is an ordered list of phases of one pipeline kind (install, expand, ...).
//
// SC-STUB(F08 50-AC-16)
type Pipeline struct {
	Kind   string
	Phases []Phase
}

// Options are the pipeline-control flags (plan 50 sections 7.1 and 16).
//
// SC-STUB(F08 50-AC-16)
type Options struct {
	FromPhase, RetryPhase string
}

// Error is a phase failure with a stable code (plan 50 section 15).
//
// SC-STUB(F08 50-AC-16)
type Error struct {
	Code, Message string
	Retryable     bool
}

// Error returns the message.
//
// SC-STUB(F08 50-AC-16)
func (e *Error) Error() string { return "" }

// Result is the outcome of Execute or Acquire.
//
// SC-STUB(F08 50-AC-5 50-AC-6 50-AC-16 50-AC-17)
type Result struct {
	// Exit is the plan 50 section 7.2 exit code.
	Exit int
	// Code is the stable error code (JOURNAL_CONFLICT, PHASE_FROZEN, RETRY_CEILING, ...), empty on success.
	Code    string
	Message string
	// Drift names succeeded phases whose input digest changed after a later phase consumed their output.
	Drift []string
}

// Engine is one invocation: one run id, one journal, one lock.
//
// SC-STUB(F08 50-AC-5 50-AC-6 50-AC-8 50-AC-9 50-AC-16 50-AC-17)
type Engine struct{}

// New returns an engine for one run.
//
// SC-STUB(F08 50-AC-5 50-AC-6 50-AC-8 50-AC-9 50-AC-16 50-AC-17)
func New(cfg Config) (e *Engine, err error) { return }

// RunID is <UTC yyyymmddThhmmssZ>-<4 hex>.
//
// SC-STUB(F08 50-AC-5 50-AC-6)
func (e *Engine) RunID() (id string) { return }

// Acquire takes the run lock (plan 50 section 17.4): the local OFD lock, then the remote object by conditional
// create. Exit 6 when another live run holds it.
//
// SC-STUB(F08 50-AC-5 50-AC-17)
func (e *Engine) Acquire(ctx context.Context) (res Result) { return }

// Renew extends the lease with an If-Match PUT when the store supports it; otherwise it does nothing.
//
// SC-STUB(F08 50-AC-5)
func (e *Engine) Renew(ctx context.Context) (res Result) { return }

// Release deletes the remote lock object (conditional on its ETag where supported) and frees the local lock.
//
// SC-STUB(F08 50-AC-5)
func (e *Engine) Release(ctx context.Context) (res Result) { return }

// RecordConfirmation is the cli.Deps.Record callback: the confirmation is persisted in the journal with this run's id
// and the clock's time.
//
// SC-STUB(F08 50-AC-9)
func (e *Engine) RecordConfirmation(c cli.Confirmation) {}

// Execute loads the journal (exit 7 on conflict), takes the run lock, runs the pipeline and releases the lock.
//
// SC-STUB(F08 50-AC-6 50-AC-16 50-AC-17)
func (e *Engine) Execute(ctx context.Context, p Pipeline, o Options) (res Result) { return }

// Log writes one JSON line to runs/<run-id>/log.jsonl, redacted.
//
// SC-STUB(F08 50-AC-8)
func (e *Engine) Log(level, event, msg string, fields map[string]any) {}

// ToolLog opens runs/<run-id>/tools/<phase>.<step>.<tool>.log; everything written is redacted.
//
// SC-STUB(F08 50-AC-8)
func (e *Engine) ToolLog(phase, step, tool string) (w io.WriteCloser, err error) { return }

// WriteReport writes the final report to runs/<run-id>/report.txt, redacted.
//
// SC-STUB(F08 50-AC-8)
func (e *Engine) WriteReport(text string) (err error) { return }

// Status implements `sc-bootstrap status [--json]`: it prints the journal summary (or the journal plus next_action
// as JSON) and returns 0, or 7 on a journal conflict after writing both copies under
// runs/<run-id>/journal-conflict/.
//
// SC-STUB(F08 50-AC-6 50-AC-9)
func Status(ctx context.Context, cfg Config, jsonOut bool, stdout, stderr io.Writer) (exit int) {
	return
}
