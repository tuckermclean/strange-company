package teststep_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/codingrun"
	"github.com/tuckermclean/strange-company/control-plane/internal/policy"
	"github.com/tuckermclean/strange-company/control-plane/internal/redgate"
	"github.com/tuckermclean/strange-company/control-plane/internal/runner"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
	"github.com/tuckermclean/strange-company/control-plane/internal/teststep"
)

type fakeBoard struct {
	spec      *store.CardSpec
	artifacts []*store.Artifact
	put       []store.Artifact
	specErr   error
}

func (f *fakeBoard) GetSpec(context.Context, uuid.UUID) (*store.CardSpec, error) {
	return f.spec, f.specErr
}
func (f *fakeBoard) ListArtifacts(context.Context, uuid.UUID) ([]*store.Artifact, error) {
	return f.artifacts, nil
}
func (f *fakeBoard) PutArtifact(_ context.Context, a store.Artifact) (*store.Artifact, error) {
	f.put = append(f.put, a)
	a.ID = uuid.New()
	return &a, nil
}

type fakeRunner struct {
	req    codingrun.Request
	result *runner.CodingRunResult
	err    error
	calls  int
}

func (f *fakeRunner) Run(_ context.Context, req codingrun.Request) (*runner.CodingRunResult, error) {
	f.calls++
	f.req = req
	return f.result, f.err
}

// fakeVerifier answers the red gate. Green baseline, failing candidate is the
// state §11.3 requires before a card may proceed.
type fakeVerifier struct {
	outcomes []redgate.RunOutcome
	err      error
	calls    int
	refs     []string
}

func (f *fakeVerifier) Verify(_ context.Context, req codingrun.VerifyRequest) (redgate.RunOutcome, error) {
	f.refs = append(f.refs, req.Ref)
	i := f.calls
	f.calls++
	if f.err != nil {
		return redgate.RunOutcome{}, f.err
	}
	if i >= len(f.outcomes) {
		i = len(f.outcomes) - 1
	}
	return f.outcomes[i], nil
}

func redGate() *fakeVerifier {
	return &fakeVerifier{outcomes: []redgate.RunOutcome{
		{Completed: true, ExitCode: 0}, // baseline green
		{Completed: true, ExitCode: 1}, // new tests fail
	}}
}

func board() *fakeBoard {
	return &fakeBoard{
		spec: &store.CardSpec{Content: "# Context\n\nx\n\n# Acceptance criteria\n\n- AC1: returns 200 — verified by: `go test ./...`", Approved: true},
		artifacts: []*store.Artifact{
			{Type: store.ArtifactImplementationPlan, Content: "1. add a handler\n2. test with go test"},
		},
	}
}

func testCard() *card.Card {
	repo, ref := "https://github.com/example/repo", "main"
	return &card.Card{
		ID: uuid.New(), Title: "Add a health endpoint",
		State: card.InProgress, Phase: card.PhaseTests,
		RepoURL: &repo, RepoBaseRef: &ref,
	}
}

func res() *policy.Resolution {
	return &policy.Resolution{
		Phase: "tests", Alias: "tests", ProviderName: "anthropic-api",
		Model: "claude-sonnet-5", Harness: policy.Harness("claude-code"), Attempt: 1,
	}
}

func ok() *runner.CodingRunResult {
	return &runner.CodingRunResult{
		Status: runner.StatusCompleted, Harness: "claude-code",
		Model: "claude-sonnet-5", Summary: "wrote two failing tests",
	}
}

func TestWritingTestsAdvancesToImplementation(t *testing.T) {
	b, r, v := board(), &fakeRunner{result: ok()}, redGate()

	ev, err := teststep.New(b, b, r, v, codingrun.GitIdentity{Username: "x"}, nil).Do(context.Background(), testCard(), res())
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if ev.NextPhase != card.PhaseImplementation {
		t.Fatalf("next phase = %q", ev.NextPhase)
	}
	if len(b.put) == 0 {
		t.Fatal("recorded no artifact for the run")
	}
	if b.put[0].Type != store.ArtifactTestMapping {
		t.Errorf("artifact type = %q", b.put[0].Type)
	}
}

// §11.2's inputs are the spec, the plan, the repository and the criteria. The
// plan is the whole reason planning ran first; a test-writer that never sees
// it is starting from scratch on work already paid for.
func TestTheTaskCarriesTheSpecificationAndThePlan(t *testing.T) {
	b, r, v := board(), &fakeRunner{result: ok()}, redGate()

	if _, err := teststep.New(b, b, r, v, codingrun.GitIdentity{Username: "x"}, nil).Do(context.Background(), testCard(), res()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"AC1", "add a handler"} {
		if !strings.Contains(r.req.Task, want) {
			t.Errorf("task is missing %q", want)
		}
	}
	if r.req.Branch == "" || !strings.HasPrefix(r.req.Branch, "agent/") {
		t.Errorf("branch = %q; §16.2 requires an agent/ branch", r.req.Branch)
	}
	if r.req.BaseRef != "main" {
		t.Errorf("base ref = %q", r.req.BaseRef)
	}
}

// "The test-writing agent MUST NOT implement the requested feature" is §11.2
// in capitals. It cannot be enforced from here, but it must at least be said.
func TestTheTaskForbidsImplementingTheFeature(t *testing.T) {
	b, r, v := board(), &fakeRunner{result: ok()}, redGate()

	if _, err := teststep.New(b, b, r, v, codingrun.GitIdentity{Username: "x"}, nil).Do(context.Background(), testCard(), res()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.req.Task, codingrun.TestCommandPath) {
		t.Errorf("the task does not ask for the test command script both gates run:\n%s", r.req.Task)
	}
	if !strings.Contains(strings.ToLower(r.req.Task), "must not implement") {
		t.Errorf("the task does not forbid implementing the feature:\n%s", r.req.Task)
	}
}

// Planning runs before tests for a reason. Without a plan the test-writer is
// guessing, and §11.1 already forbids that of the planner.
func TestTestsAreNotWrittenWithoutAPlan(t *testing.T) {
	b := board()
	b.artifacts = nil
	r, v := &fakeRunner{result: ok()}, redGate()

	if _, err := teststep.New(b, b, r, v, codingrun.GitIdentity{Username: "x"}, nil).Do(context.Background(), testCard(), res()); err == nil {
		t.Fatal("expected an error")
	}
	if r.calls != 0 {
		t.Fatal("started a coding run with no plan to work from")
	}
}

// §12.1: an infrastructure failure is not a failed attempt. It must not
// advance the phase either -- there are no tests, so nothing can be
// implemented against them.
func TestAnInfrastructureFailureNeitherAdvancesNorCounts(t *testing.T) {
	b, v := board(), redGate()
	r := &fakeRunner{result: &runner.CodingRunResult{
		Status: runner.StatusInfraError, Harness: "claude-code", Summary: "pod evicted",
	}}

	ev, err := teststep.New(b, b, r, v, codingrun.GitIdentity{Username: "x"}, nil).Do(context.Background(), testCard(), res())
	if err == nil {
		t.Fatal("expected an error so the worker hands the card back")
	}
	if ev.NextPhase != "" || ev.NextState != "" {
		t.Fatalf("moved the card on an infrastructure failure: %+v", ev)
	}
}

// A harness that ran and failed is a real outcome, and the ladder decides what
// happens next -- but the card must not advance to implementation with no
// tests written.
func TestAFailedRunDoesNotAdvanceToImplementation(t *testing.T) {
	b, v := board(), redGate()
	r := &fakeRunner{result: &runner.CodingRunResult{
		Status: runner.StatusFailed, Harness: "claude-code", Summary: "could not write tests",
	}}

	ev, err := teststep.New(b, b, r, v, codingrun.GitIdentity{Username: "x"}, nil).Do(context.Background(), testCard(), res())
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if ev.NextPhase == card.PhaseImplementation {
		t.Fatal("advanced to implementation with no tests")
	}
	if ev.NextState != card.NeedsHuman {
		t.Fatalf("next state = %q", ev.NextState)
	}
}

func TestAMissingSpecificationIsRefused(t *testing.T) {
	b := board()
	b.specErr = errors.New("no spec")
	r, v := &fakeRunner{result: ok()}, redGate()

	if _, err := teststep.New(b, b, r, v, codingrun.GitIdentity{Username: "x"}, nil).Do(context.Background(), testCard(), res()); err == nil {
		t.Fatal("expected an error")
	}
	if r.calls != 0 {
		t.Fatal("started a coding run with no specification")
	}
}

// §11.3: "If the new tests pass without implementation, the test phase fails."
// The card must not reach an implementer with tests that prove nothing.
func TestTestsThatPassWithoutTheFeatureStopTheCard(t *testing.T) {
	b, r := board(), &fakeRunner{result: ok()}
	v := &fakeVerifier{outcomes: []redgate.RunOutcome{
		{Completed: true, ExitCode: 0}, // baseline green
		{Completed: true, ExitCode: 0}, // and still green WITH the new tests
	}}

	ev, err := teststep.New(b, b, r, v, codingrun.GitIdentity{Username: "x"}, nil).Do(context.Background(), testCard(), res())
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if ev.NextPhase == card.PhaseImplementation {
		t.Fatal("advanced with tests that pass against the unimplemented state")
	}
	if ev.NextState != card.NeedsHuman {
		t.Fatalf("next state = %q", ev.NextState)
	}
}

// The gate compares the base ref with the agent branch. Verifying the same ref
// twice would compare a thing with itself and always report a broken baseline
// or a green candidate.
func TestTheGateComparesTheBaseRefWithTheAgentBranch(t *testing.T) {
	b, r, v := board(), &fakeRunner{result: ok()}, redGate()

	if _, err := teststep.New(b, b, r, v, codingrun.GitIdentity{Username: "x"}, nil).Do(context.Background(), testCard(), res()); err != nil {
		t.Fatal(err)
	}
	if len(v.refs) != 2 {
		t.Fatalf("verified %d refs, want two", len(v.refs))
	}
	if v.refs[0] != "main" || !strings.HasPrefix(v.refs[1], "agent/") {
		t.Fatalf("compared %q with %q", v.refs[0], v.refs[1])
	}
}

// A baseline that was already failing makes nothing attributable to the new
// tests, so the card stops rather than being called red.
func TestABrokenBaselineStopsTheCard(t *testing.T) {
	b, r := board(), &fakeRunner{result: ok()}
	v := &fakeVerifier{outcomes: []redgate.RunOutcome{
		{Completed: true, ExitCode: 1}, // baseline already failing
		{Completed: true, ExitCode: 1},
	}}

	ev, err := teststep.New(b, b, r, v, codingrun.GitIdentity{Username: "x"}, nil).Do(context.Background(), testCard(), res())
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if ev.NextState != card.NeedsHuman {
		t.Fatalf("next state = %q", ev.NextState)
	}
	if !strings.Contains(strings.ToLower(ev.Summary), "baseline") {
		t.Errorf("the summary does not explain what is wrong: %q", ev.Summary)
	}
}

// Checks that never finished are an outage, not a verdict. The card is handed
// back rather than stopped.
func TestAnInconclusiveGateHandsTheCardBack(t *testing.T) {
	b, r := board(), &fakeRunner{result: ok()}
	v := &fakeVerifier{outcomes: []redgate.RunOutcome{{Completed: false}}}

	if _, err := teststep.New(b, b, r, v, codingrun.GitIdentity{Username: "x"}, nil).Do(context.Background(), testCard(), res()); err == nil {
		t.Fatal("expected an error so the worker hands the card back")
	}
}
