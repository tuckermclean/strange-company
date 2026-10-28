// Package engine is the bootstrap phase engine (plan 50 sections 8, 16 and 17): the journal with its local and S3
// copies, the run lock, the phase runner with input digests, freeze rules, retry ceiling and the crash hook, and
// `status`.
package engine

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"strange.company/sc/internal/bootstrap/cli"
	"strange.company/sc/internal/bootstrap/redact"
	"strange.company/sc/internal/bootstrap/store"
	"strange.company/sc/internal/clock"
)

// Config is everything one engine instance needs.
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
	// AtEnd, when set, runs once after the pipeline ended and before the run log is uploaded: the place for a record
	// that summarizes the run (the prompt counter of plan 50 section 22, 50-AC-31).
	AtEnd func()
}

// CheckState is the outcome of a Check.
type CheckState int

// The Check outcomes.
const (
	NotDone CheckState = iota
	Done
	Drifted
)

// Check is the result of the idempotency probe against the real world.
type Check struct {
	State  CheckState
	Reason string
}

// OutputRef points at something a phase produced.
type OutputRef struct {
	Kind, Ref, Sha256 string
}

// Unit is a phase or a step (plan 50 section 8.1).
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
type Phase interface {
	Unit
	Steps() []Unit
}

// Pipeline is an ordered list of phases of one pipeline kind (install, expand, ...).
type Pipeline struct {
	Kind   string
	Phases []Phase
}

// Options are the pipeline-control flags (plan 50 sections 7.1 and 16).
type Options struct {
	FromPhase, RetryPhase string
}

// Error is a phase failure with a stable code (plan 50 section 15).
type Error struct {
	Code, Message string
	Retryable     bool
	// Exit is the plan 50 section 7.2 exit code the failure maps to; zero means 1 (phase failed).
	Exit int
	// Procedure is the repository-relative procedure to follow (procedures/<name>.md[#anchor]); optional.
	Procedure string
}

// Error returns the message.
func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Message
}

// Result is the outcome of Execute or Acquire.
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
type Engine struct {
	cfg   Config
	clk   clock.Clock
	red   *redact.Redactor
	runID string

	ifMatch bool

	mu      sync.Mutex
	pending []confirmationJ

	// journal state, valid inside Execute
	jr         *journalDoc
	remoteETag string
	drift      []string

	// run lock state
	lockFile   *os.File
	lockETag   string
	remoteHeld bool
	lastRenew  time.Time

	// logging
	logMu                 sync.Mutex
	logFile               *os.File
	curPipeline, curPhase string
	curStep               string

	crashed bool

	// saveFailed latches the first JOURNAL_CONFLICT / JOURNAL_REMOTE_WRITE_FAILED of the run (plan 50 section 17.3).
	saveFailed *Result
	// prevCode is the error code each unit record carried before its current attempt began.
	prevCode map[*unitJ]string
	// afterSuccess marks the units whose current attempt began from a success.
	afterSuccess map[*unitJ]bool
	unitStart    time.Time
}

// New returns an engine for one run.
func New(cfg Config) (*Engine, error) {
	if cfg.Dir == "" {
		return nil, errors.New("engine: Config.Dir is required")
	}
	e := &Engine{cfg: cfg, clk: cfg.Clock, red: cfg.Redactor, ifMatch: cfg.IfMatch && cfg.Store != nil}
	if e.clk == nil {
		e.clk = clock.Real{}
	}
	if e.red == nil {
		e.red = redact.New()
	}
	if e.cfg.Command == "" {
		e.cfg.Command = "run"
	}
	if e.cfg.User == "" {
		e.cfg.User = orDefault(os.Getenv("USER"), "unknown")
	}
	if e.cfg.Host == "" {
		h, err := os.Hostname()
		if err != nil || h == "" {
			h = "unknown"
		}
		e.cfg.Host = h
	}
	src := cfg.Rand
	if src == nil {
		src = rand.Reader
	}
	var b [2]byte
	if _, err := io.ReadFull(src, b[:]); err != nil {
		return nil, fmt.Errorf("engine: run id: %w", err)
	}
	e.runID = e.clk.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:])
	return e, nil
}

// RunID is <UTC yyyymmddThhmmssZ>-<4 hex>.
func (e *Engine) RunID() string { return e.runID }

// RecordConfirmation is the cli.Deps.Record callback: the confirmation is persisted in the journal with this run's id
// and the clock's time (at the next journal write).
func (e *Engine) RecordConfirmation(c cli.Confirmation) {
	e.addConfirmation(c.Kind, c.SubjectDigest, c.Method)
}

func (e *Engine) addConfirmation(kind, subject, method string) {
	c := confirmationJ{Kind: kind, SubjectDigest: subject, ConfirmedAt: e.stamp(), RunID: e.runID, Method: method}
	e.mu.Lock()
	e.pending = append(e.pending, c)
	e.mu.Unlock()
}

// phaseState maps a phase to the installation state it advances to (plan 50 section 14.1).
var phaseState = map[string]string{
	"plan": "planned", "provision": "provisioned", "configure": "configured", "seed": "seeded",
	"forge-init": "forge-initialized", "flux-bootstrap": "flux-sourced", "verify": "verified", "control-transfer": "transferred",
}

var stateRank = map[string]int{
	"new": 0, "planned": 1, "provisioned": 2, "configured": 3, "seeded": 4, "forge-initialized": 5, "flux-sourced": 6,
	"verified": 7, "transferred": 8,
}

// rankOf ranks a state; states after "transferred" (expanding, destroying, ...) rank above it.
func rankOf(state string) int {
	if r, ok := stateRank[state]; ok {
		return r
	}
	return 9
}

// advancedPast reports whether installation.state moved past the state the phase produces: a later phase may have
// consumed its output (plan 50 section 17.1 rule 4).
func (e *Engine) advancedPast(phase string) bool {
	target, ok := phaseState[phase]
	return ok && rankOf(e.jr.Installation.State) > rankOf(target)
}

func (e *Engine) advance(phase string) {
	if target, ok := phaseState[phase]; ok && rankOf(target) > rankOf(e.jr.Installation.State) {
		e.jr.Installation.State = target
		if target == "transferred" && e.jr.Installation.TransferredAt == nil {
			e.jr.Installation.TransferredAt = ptr(e.stamp())
		}
	}
}

// frozenPhases never re-run once the root Kustomization switched to GitRepository (plan 50 section 17.1 rule 5).
var frozenPhases = map[string]bool{"seed": true, "forge-init": true, "flux-bootstrap": true}

// switched reads the freeze condition from reality first, then from the journal facts.
func (e *Engine) switched(ctx context.Context) (bool, error) {
	if e.cfg.SourceSwitched != nil {
		on, err := e.cfg.SourceSwitched(ctx)
		if err != nil {
			return false, err
		}
		if on {
			return true, nil
		}
	}
	var f struct {
		Transfer struct {
			SwitchingAt *string `json:"switching_at"`
			SwitchedAt  *string `json:"switched_at"`
		} `json:"transfer"`
	}
	if len(e.jr.Facts) > 0 {
		_ = json.Unmarshal(e.jr.Facts, &f)
	}
	return f.Transfer.SwitchingAt != nil || f.Transfer.SwitchedAt != nil, nil
}

var codeRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]+(:[a-z0-9-]+)?$`)

const (
	stPending     = "pending"
	stRunning     = "running"
	stSucceeded   = "succeeded"
	stFailed      = "failed"
	stSkipped     = "skipped"
	stInterrupted = "interrupted"
)

// retryCeiling is the attempt count at which an unchanged failure is no longer retried automatically (plan 50 section 16).
const retryCeiling = 5

func digestOf(ctx context.Context, u Unit) (string, error) {
	in, err := u.Inputs(ctx)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:]), nil
}

// Execute loads the journal (exit 7 on conflict), takes the run lock, runs the pipeline and releases the lock.
func (e *Engine) Execute(ctx context.Context, p Pipeline, o Options) Result {
	if res := e.Acquire(ctx); res.Exit != 0 {
		res.Message = e.red.String(res.Message)
		return res
	}
	res := e.executeLocked(ctx, p, o)
	res.Message = e.red.String(res.Message)
	if e.cfg.AtEnd != nil {
		e.cfg.AtEnd()
	}
	e.uploadLog(ctx)
	if rel := e.Release(ctx); rel.Exit != 0 && res.Exit == 0 {
		res = rel
		res.Message = e.red.String(res.Message)
	}
	return res
}

func (e *Engine) executeLocked(ctx context.Context, p Pipeline, o Options) Result {
	lr, fail := e.load(ctx)
	if fail != nil {
		return *fail
	}
	if lr.conflict {
		return e.conflictResult(lr)
	}
	e.jr, e.remoteETag = lr.doc, lr.remoteETag
	if e.jr.Cluster == "" {
		e.jr.Cluster = e.cfg.Cluster
	}
	if e.cfg.ConfigDigest != "" {
		e.jr.Installation.ConfigDigest = e.cfg.ConfigDigest
	}
	run := e.beginRun()
	pl := e.pipelineFor(p)
	e.curPipeline = pl.Pipeline

	finish := func(res Result) Result { return e.finish(ctx, run, pl, res) }

	names := map[string]int{}
	for i, ph := range p.Phases {
		names[ph.Name()] = i
	}
	fromIdx := -1
	if o.FromPhase != "" {
		i, ok := names[o.FromPhase]
		if !ok {
			return finish(Result{Exit: 2, Code: "UNKNOWN_PHASE", Message: fmt.Sprintf("--from-phase %s: the pipeline has no such phase", o.FromPhase)})
		}
		fromIdx = i
	}
	if o.RetryPhase != "" {
		if _, ok := names[o.RetryPhase]; !ok {
			return finish(Result{Exit: 2, Code: "UNKNOWN_PHASE", Message: fmt.Sprintf("--retry-phase %s: the pipeline has no such phase", o.RetryPhase)})
		}
	}
	frozen, err := e.switched(ctx)
	if err != nil {
		return finish(Result{Exit: 1, Code: "SOURCE_PROBE_FAILED", Message: e.red.String("the root Kustomization cannot be read: " + err.Error())})
	}
	if frozen && o.FromPhase != "" && frozenPhases[o.FromPhase] {
		return finish(Result{Exit: 2, Code: "PHASE_FROZEN", Message: fmt.Sprintf(
			"--from-phase %s: the root Kustomization has switched to the in-cluster Git source; seed, forge-init and flux-bootstrap are frozen (procedures/seed-flux-handoff.md)", o.FromPhase)})
	}
	if res := e.save(ctx); res != nil {
		return *res
	}
	e.Log("info", "run-start", "run started", map[string]any{"command": e.cfg.Command, "pipeline": p.Kind})

	transferred := e.jr.Installation.State == "transferred" && (p.Kind == "install" || p.Kind == "") && o.FromPhase == ""
	out := Result{}
	for i, ph := range p.Phases {
		pj := e.phaseRec(pl, ph.Name())
		e.curPhase, e.curStep = ph.Name(), ""
		switch {
		case frozen && frozenPhases[ph.Name()]:
			pj.Status = stSkipped
			e.Log("info", "phase-frozen", "frozen after the source switch: not re-run", nil)
			if res := e.save(ctx); res != nil {
				return finish(*res)
			}
			continue
		case transferred && ph.Name() != "preflight" && ph.Name() != "verify":
			if e.reportDrift(ctx, ph) {
				e.noteDrift(ph.Name())
			}
			pj.Status = stSkipped
			if res := e.save(ctx); res != nil {
				return finish(*res)
			}
			continue
		}
		if e.clk.Now().Sub(e.lastRenew) >= renewEvery {
			if res := e.Renew(ctx); res.Exit != 0 {
				out = res
				break
			}
		}
		if res := e.phase(ctx, ph, pj, fromIdx >= 0 && i >= fromIdx, o); res != nil {
			out = *res
			break
		}
	}
	e.curPhase, e.curStep = "", ""
	out.Drift = append([]string(nil), e.drift...)
	return finish(out)
}

func (e *Engine) noteDrift(name string) {
	for _, d := range e.drift {
		if d == name {
			return
		}
	}
	e.drift = append(e.drift, name)
}

// reportDrift probes the phase (or its steps) without running anything.
func (e *Engine) reportDrift(ctx context.Context, ph Phase) bool {
	units := ph.Steps()
	if len(units) == 0 {
		units = []Unit{ph}
	}
	drifted := false
	for _, u := range units {
		if c, err := u.Check(ctx); err == nil && c.State == Drifted {
			drifted = true
		}
	}
	return drifted
}

// beginRun appends this invocation to the journal's runs.
func (e *Engine) beginRun() *runJ {
	r := &runJ{
		RunID: e.runID, Command: e.cfg.Command, StartedAt: e.stamp(), Outcome: "running",
		Operator: operatorJ{User: e.cfg.User, Host: e.cfg.Host}, SCBootstrapVer: orDefault(e.cfg.Version, "dev"),
		Toolbox: toolboxJ{ImageDigest: orDefault(e.cfg.ToolboxDigest, zeroDigest), Source: orDefault(e.cfg.ToolboxSource, "built")},
		GitHead: orDefault(e.cfg.GitHead, zeroSHA1),
	}
	e.jr.Runs = append(e.jr.Runs, r)
	if len(e.jr.Runs) > maxRuns {
		e.jr.Runs = e.jr.Runs[len(e.jr.Runs)-maxRuns:]
	}
	return r
}

// pipelineFor finds (or starts) the journal entry of the pipeline kind and makes sure it lists every phase.
func (e *Engine) pipelineFor(p Pipeline) *pipelineJ {
	kind := orDefault(p.Kind, "install")
	var pl *pipelineJ
	gen := 0
	for _, x := range e.jr.Pipelines {
		if x.Pipeline == kind {
			pl = x
			gen = x.Generation
		}
	}
	if pl == nil {
		pl = &pipelineJ{Pipeline: kind, Generation: gen + 1, StartedAt: ptr(e.stamp())}
		e.jr.Pipelines = append(e.jr.Pipelines, pl)
	}
	pl.Status, pl.FinishedAt = stRunning, nil
	byName := map[string]*unitJ{}
	for _, ph := range pl.Phases {
		byName[ph.Name] = ph
	}
	var ordered []*unitJ
	seen := map[string]bool{}
	for _, ph := range p.Phases {
		u := byName[ph.Name()]
		if u == nil {
			u = &unitJ{Name: ph.Name(), Status: stPending, OutputRefs: []outputRefJ{}, Steps: []*unitJ{}, phase: true}
		}
		seen[u.Name] = true
		ordered = append(ordered, u)
	}
	for _, ph := range pl.Phases {
		if !seen[ph.Name] {
			ordered = append(ordered, ph)
		}
	}
	pl.Phases = ordered
	return pl
}

func (e *Engine) phaseRec(pl *pipelineJ, name string) *unitJ {
	for _, ph := range pl.Phases {
		if ph.Name == name {
			return ph
		}
	}
	return nil
}

// finish records the outcome of the run and the pipeline.
func (e *Engine) finish(ctx context.Context, run *runJ, pl *pipelineJ, res Result) Result {
	code := res.Exit
	run.ExitCode = &code
	run.FinishedAt = ptr(e.stamp())
	switch {
	case res.Exit == 0:
		run.Outcome = "succeeded"
	case res.Exit == 2 || res.Exit == 5:
		run.Outcome = "refused"
	case res.Exit == 75:
		run.Outcome = "waiting-on-human"
	case res.Exit == 130:
		run.Outcome = "interrupted"
	default:
		run.Outcome = "failed"
	}
	switch {
	case res.Exit == 0:
		pl.Status, pl.FinishedAt = stSucceeded, ptr(e.stamp())
	case res.Exit == 130:
		pl.Status = stInterrupted
	case res.Exit == 2 || res.Exit == 5:
		pl.Status = stPending
		for _, ph := range pl.Phases {
			if ph.Status != stPending && ph.Status != stSucceeded && ph.Status != stSkipped {
				pl.Status = stFailed
			}
		}
		if pl.Status == stPending {
			pl.Status = pendingOrDone(pl)
		}
	default:
		pl.Status = stFailed
	}
	if sres := e.save(ctx); sres != nil {
		if res.Exit == 0 {
			return *sres
		}
		if sres.Exit == 7 {
			// a journal conflict is exit 7 wherever it is found, also by the save that ends a failed run
			res = *sres
		}
	}
	e.Log("info", "run-end", "run finished", map[string]any{"exit": res.Exit, "code": res.Code})
	res.Message = e.red.String(res.Message)
	return res
}

func pendingOrDone(pl *pipelineJ) string {
	for _, ph := range pl.Phases {
		if ph.Status == stPending {
			return stPending
		}
	}
	return stSucceeded
}

// crash delivers the test crash (plan 50 section 22) when SC_TEST_CRASH_AT names this boundary.
func (e *Engine) crash(step, when string) {
	if e.cfg.Env["SC_TEST_MODE"] != "1" || e.crashed {
		return
	}
	spec := e.cfg.Env["SC_TEST_CRASH_AT"]
	i := strings.LastIndex(spec, ":")
	if i < 0 || spec[:i] != step || spec[i+1:] != when {
		return
	}
	e.crashed = true
	if e.cfg.Kill != nil {
		e.cfg.Kill()
		return
	}
	killSelf()
}

// phase processes one phase (and its steps). A non-nil Result stops the pipeline.
func (e *Engine) phase(ctx context.Context, ph Phase, pj *unitJ, inRange bool, o Options) *Result {
	name := ph.Name()
	steps := ph.Steps()
	if inRange {
		resetForRange(pj)
		for _, s := range pj.Steps {
			resetForRange(s)
		}
	}
	if len(steps) == 0 {
		_, res := e.leaf(ctx, ph, pj, name, o, nil, func() { e.advance(name) })
		return res
	}
	e.crash(name, "before")
	digest, err := digestOf(ctx, ph)
	if err != nil {
		return e.failUnit(ctx, ph, pj, nil, fmt.Errorf("%w", &Error{Code: "INPUTS_FAILED", Message: "cannot compute the inputs of " + name + ": " + err.Error()}))
	}
	prevOK := (pj.Status == stSucceeded || pj.Status == stSkipped) && pj.InputDigest != nil
	same := prevOK && *pj.InputDigest == digest
	byName := map[string]*unitJ{}
	for _, s := range pj.Steps {
		byName[s.Name] = s
	}
	var ordered []*unitJ
	for _, u := range steps {
		s := byName[u.Name()]
		if s == nil {
			s = &unitJ{Name: u.Name(), Status: stPending, OutputRefs: []outputRefJ{}}
		}
		ordered = append(ordered, s)
	}
	pj.Steps = ordered
	started := false
	onStart := func() {
		if started {
			return
		}
		started = true
		pj.Status, pj.Attempt = stRunning, pj.Attempt+1
		pj.StartedAt, pj.FinishedAt, pj.Error = ptr(e.stamp()), nil, nil
		pj.LastRunID = ptr(e.runID)
	}
	ranAny := false
	for i, u := range steps {
		e.curStep = u.Name()
		ran, res := e.leaf(ctx, u, ordered[i], name, o, onStart, nil)
		ranAny = ranAny || ran
		if res != nil {
			// the failing step's error is the phase's error
			if ordered[i].Status == stFailed {
				pj.Status, pj.Error, pj.FinishedAt = stFailed, ordered[i].Error, ptr(e.stamp())
			} else if ordered[i].Status == stInterrupted {
				pj.Status, pj.FinishedAt = stInterrupted, ptr(e.stamp())
			}
			if sres := e.save(ctx); sres != nil {
				return sres
			}
			return res
		}
	}
	e.curStep = ""
	pj.LastRunID = ptr(e.runID)
	pj.Error = nil
	pj.OutputRefs = []outputRefJ{}
	switch {
	case ranAny:
		pj.Status, pj.InputDigest, pj.FinishedAt = stSucceeded, ptr(digest), ptr(e.stamp())
		e.advance(name)
	case prevOK && same:
		pj.Status = stSkipped
	case prevOK && e.advancedPast(name):
		pj.Status = stSkipped
		e.noteDrift(name)
	default:
		pj.Status, pj.InputDigest = stSucceeded, ptr(digest)
		pj.StartedAt, pj.FinishedAt = orNow(pj.StartedAt, e.stamp()), ptr(e.stamp())
		e.advance(name)
	}
	if sres := e.save(ctx); sres != nil {
		return sres
	}
	e.crash(name, "after")
	return nil
}

func orNow(p *string, now string) *string {
	if p != nil {
		return p
	}
	return &now
}

// resetForRange sets a done record back to pending: --from-phase re-probes it. A failed record stays failed so the
// retry ceiling still applies.
func resetForRange(u *unitJ) {
	if u.Status == stSucceeded || u.Status == stSkipped {
		u.Status = stPending
	}
}

// leaf runs the Check/Run protocol of plan 50 section 8.1 for a step or a stepless phase and records it in r.
func (e *Engine) leaf(ctx context.Context, u Unit, r *unitJ, phaseName string, o Options, onStart func(), onSuccess func()) (bool, *Result) {
	isPhase := r.phase
	e.unitStart = time.Time{} // a probe that fails before the unit starts logs duration 0, not the previous unit's
	e.crash(u.Name(), "before")
	d, err := digestOf(ctx, u)
	if err != nil {
		return false, e.failUnit(ctx, u, r, onStart, &Error{Code: "INPUTS_FAILED", Message: "cannot compute the inputs of " + u.Name() + ": " + err.Error()})
	}
	prevOK := (r.Status == stSucceeded || r.Status == stSkipped) && r.InputDigest != nil
	same := prevOK && *r.InputDigest == d
	past := e.advancedPast(phaseName)
	run := false
	if prevOK && !same && !past {
		run = true // a changed digest before the install moved on: the phase is pending again
	} else {
		chk, cerr := u.Check(ctx)
		if cerr != nil {
			return false, e.failUnit(ctx, u, r, onStart, &Error{Code: "CHECK_FAILED", Message: "the check of " + u.Name() + " failed: " + cerr.Error()})
		}
		switch {
		case chk.State == Done && prevOK:
			r.Status = stSkipped
			if !same {
				e.noteDrift(phaseName)
			}
		case chk.State == Done:
			now := e.stamp()
			r.Status, r.InputDigest, r.Error = stSucceeded, ptr(d), nil
			r.StartedAt, r.FinishedAt = orNow(r.StartedAt, now), ptr(now)
			if r.OutputRefs == nil {
				r.OutputRefs = []outputRefJ{}
			}
			if onSuccess != nil {
				onSuccess()
			}
		default:
			run = true
		}
		if chk.State == Drifted {
			e.Log("warn", "drifted", chk.Reason, nil)
		}
	}
	if !run {
		if isPhase {
			r.LastRunID = ptr(e.runID)
		}
		if res := e.save(ctx); res != nil {
			return false, res
		}
		e.crash(u.Name(), "after")
		return false, nil
	}

	lastDigest := ""
	if r.InputDigest != nil {
		lastDigest = *r.InputDigest
	}
	if r.Error != nil && r.Error.Code != "" {
		if e.prevCode == nil {
			e.prevCode = map[*unitJ]string{}
		}
		e.prevCode[r] = r.Error.Code
	}
	if r.Status == stFailed && r.Error != nil && r.Attempt >= retryCeiling && lastDigest == d && o.RetryPhase != phaseName && phaseName != "preflight" {
		// preflight is always re-run and read-only (plan 50 section 14.2): the ceiling never applies to it.
		msg := fmt.Sprintf("%s failed %d times with %s and its inputs did not change; change the cause or pass --retry-phase %s",
			u.Name(), r.Attempt, r.Error.Code, phaseName)
		return false, &Result{Exit: 1, Code: "RETRY_CEILING", Message: e.red.String(msg)}
	}
	if r.Status == stFailed && (o.RetryPhase == phaseName || (lastDigest != "" && lastDigest != d)) {
		r.Attempt = 0
	}
	if onStart != nil {
		onStart()
	}
	if r.Status == stSucceeded || r.Status == stSkipped {
		// a failure now is the first of a new series (plan 50 section 16): failUnit counts it as attempt 1
		if e.afterSuccess == nil {
			e.afterSuccess = map[*unitJ]bool{}
		}
		e.afterSuccess[r] = true
	}
	r.Status, r.Attempt = stRunning, r.Attempt+1
	r.InputDigest, r.StartedAt, r.FinishedAt, r.Error = ptr(d), ptr(e.stamp()), nil, nil
	if isPhase {
		r.LastRunID = ptr(e.runID)
	}
	if res := e.save(ctx); res != nil {
		return false, res
	}
	e.unitStart = e.clk.Now()
	e.Log("info", "unit-start", u.Name(), map[string]any{"attempt": r.Attempt})

	// The timeout is measured on the injected clock (plan 52 D8). Only the wall clock needs a watcher to stop a Run
	// that hangs; an injected clock is judged after Run returns.
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	timeout, began := u.Timeout(), e.clk.Now()
	if _, real := e.clk.(clock.Real); real && timeout > 0 {
		go func() {
			if e.clk.Sleep(rctx, timeout) == nil {
				cancel()
			}
		}()
	}
	var refs []OutputRef
	var rerr error
	for try := 1; ; try++ {
		refs, rerr = u.Run(rctx)
		if rerr == nil || rctx.Err() != nil || !u.Retryable(rerr) || try >= 5 {
			break
		}
		delay := time.Duration(5<<(try-1)) * time.Second
		if delay > time.Minute {
			delay = time.Minute
		}
		e.Log("warn", "unit-retry", fmt.Sprintf("%s: transient error, retrying in %s", u.Name(), delay), map[string]any{"try": try})
		if serr := e.clk.Sleep(rctx, delay); serr != nil {
			break
		}
	}
	if rerr != nil {
		if ctx.Err() != nil {
			r.Status, r.FinishedAt = stInterrupted, ptr(e.stamp())
			return true, &Result{Exit: 130, Code: "INTERRUPTED", Message: "interrupted; the state is consistent and the run can be resumed"}
		}
		if timeout > 0 && e.clk.Now().Sub(began) >= timeout {
			rerr = &Error{Code: "PHASE_TIMEOUT", Message: fmt.Sprintf("%s exceeded its timeout of %s", u.Name(), u.Timeout())}
		}
		return true, e.failUnit(ctx, u, r, nil, rerr)
	}
	r.Status, r.FinishedAt, r.Error = stSucceeded, ptr(e.stamp()), nil
	r.OutputRefs = []outputRefJ{}
	for _, ref := range refs {
		r.OutputRefs = append(r.OutputRefs, outputRefJ{Kind: ref.Kind, Ref: e.red.String(ref.Ref), Sha256: ref.Sha256})
	}
	if onSuccess != nil {
		onSuccess()
	}
	e.Log("info", "unit-done", u.Name(), map[string]any{"attempt": r.Attempt, "duration_ms": e.sinceUnitStart()})
	if res := e.save(ctx); res != nil {
		return true, res
	}
	e.crash(u.Name(), "after")
	return true, nil
}

// failUnit records a failure in r, saves the journal and returns the Result that stops the pipeline.
func (e *Engine) failUnit(ctx context.Context, u Unit, r *unitJ, onStart func(), err error) *Result {
	if onStart != nil {
		onStart()
	}
	code, msg, exit, procedure := "PHASE_FAILED", err.Error(), 1, ""
	var pe *Error
	if errors.As(err, &pe) {
		if pe.Code != "" {
			code = pe.Code
		}
		msg, procedure = pe.Error(), pe.Procedure
		if pe.Exit != 0 {
			exit = pe.Exit
		}
	}
	if !codeRe.MatchString(code) {
		msg, code = code+": "+msg, "PHASE_FAILED"
	}
	msg = e.red.String(msg)
	switch {
	case r.Status != stRunning:
		// the probe failed before the unit started: this is the unit's failed attempt
		if r.Status == stSucceeded || r.Status == stSkipped {
			r.Attempt = 0
		}
		r.Attempt++
	case e.afterSuccess[r]:
		r.Attempt = 1
	}
	r.Status, r.FinishedAt = stFailed, ptr(e.stamp())
	if prev := e.prevCode[r]; prev != "" && prev != code {
		r.Attempt = 1
	}
	r.Error = &errJ{Code: code, Message: truncateMessage(msg), Retryable: u.Retryable(err), Procedure: procedure}
	e.Log("error", "unit-failed", msg, map[string]any{"attempt": r.Attempt, "error_code": code, "duration_ms": e.sinceUnitStart()})
	if res := e.save(ctx); res != nil {
		return res
	}
	return &Result{Exit: exit, Code: code, Message: msg}
}

// sinceUnitStart is the injected-clock milliseconds since the current unit attempt began.
func (e *Engine) sinceUnitStart() int64 {
	if e.unitStart.IsZero() {
		return 0
	}
	return e.clk.Now().Sub(e.unitStart).Milliseconds()
}
