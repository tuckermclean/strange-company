package implstep_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/codingrun"
	"github.com/tuckermclean/strange-company/control-plane/internal/implstep"
	"github.com/tuckermclean/strange-company/control-plane/internal/policy"
	"github.com/tuckermclean/strange-company/control-plane/internal/redgate"
	"github.com/tuckermclean/strange-company/control-plane/internal/runner"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
)

type fakeBoard struct {
	spec      *store.CardSpec
	artifacts []*store.Artifact
	put       []store.Artifact
	recorded  []store.AttemptRecord
}

func (f *fakeBoard) GetSpec(context.Context, uuid.UUID) (*store.CardSpec, error) {
	return f.spec, nil
}
func (f *fakeBoard) ListArtifacts(context.Context, uuid.UUID) ([]*store.Artifact, error) {
	return f.artifacts, nil
}
func (f *fakeBoard) PutArtifact(_ context.Context, a store.Artifact) (*store.Artifact, error) {
	f.put = append(f.put, a)
	a.ID = uuid.New()
	return &a, nil
}
func (f *fakeBoard) RecordAttempt(_ context.Context, rec store.AttemptRecord) (*store.AttemptOutcome, error) {
	f.recorded = append(f.recorded, rec)
	return &store.AttemptOutcome{CountedAsAttempt: true, AttemptNumber: len(f.recorded)}, nil
}

type fakeRunner struct {
	req     codingrun.Request
	err     error
	result  *runner.CodingRunResult
	verify  redgate.RunOutcome
	calls   int
	verifies int
}

func (f *fakeRunner) Run(_ context.Context, req codingrun.Request) (*runner.CodingRunResult, error) {
	f.calls++
	f.req = req
	return f.result, f.err
}
func (f *fakeRunner) Verify(context.Context, codingrun.VerifyRequest) (redgate.RunOutcome, error) {
	f.verifies++
	return f.verify, nil
}

func board() *fakeBoard {
	return &fakeBoard{
		spec: &store.CardSpec{Content: "# Context\n\nx", Approved: true},
		artifacts: []*store.Artifact{
			{Type: store.ArtifactImplementationPlan, Content: "1. add a handler"},
		},
	}
}

func testCard() *card.Card {
	repo, ref := "https://github.com/example/repo", "main"
	return &card.Card{
		ID: uuid.New(), Title: "Add a health endpoint",
		State: card.InProgress, Phase: card.PhaseImplementation,
		RepoURL: &repo, RepoBaseRef: &ref,
	}
}

func res(attempt int) *policy.Resolution {
	return &policy.Resolution{
		Phase: "implementation", Alias: "implement-cheap", ProviderName: "anthropic-api",
		Model: "claude-haiku-4-5", Harness: policy.Harness("claude-code"), Attempt: attempt,
	}
}

func done() *runner.CodingRunResult {
	return &runner.CodingRunResult{Status: runner.StatusCompleted, Harness: "claude-code", Model: "claude-haiku-4-5", Summary: "implemented"}
}

// The green gate (§19): the tests the red gate proved failing now pass. That,
// not the model's own account, is what moves a card to Review.
func TestAGreenVerificationSendsTheCardToTheReviewPhase(t *testing.T) {
	b := board()
	r := &fakeRunner{result: done(), verify: redgate.RunOutcome{Completed: true, ExitCode: 0}}

	ev, err := implstep.New(b, b, b, r, r, codingrun.GitIdentity{Username: "x"}, nil).Do(context.Background(), testCard(), res(1))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	// The review PHASE: §18's automated review runs before a human sees
	// anything, and a card in the Review state is not claimable.
	if ev.NextPhase != card.PhaseReview {
		t.Fatalf("next phase = %q, want review", ev.NextPhase)
	}
	if r.verifies != 1 {
		t.Errorf("ran verification %d times", r.verifies)
	}
	// A passing run is not an attempt: §12.1 counts an attempt when
	// verification FAILED.
	if len(b.recorded) != 1 || b.recorded[0].Result.Status != runner.StatusCompleted {
		t.Fatalf("recorded = %+v", b.recorded)
	}
}

// §12.1: the agent worked, the runner regained control, verification ran and
// failed. That is the definition of an implementation attempt, and it is what
// moves the escalation ladder.
func TestAFailingVerificationCountsAnAttemptAndRetries(t *testing.T) {
	b := board()
	r := &fakeRunner{result: done(), verify: redgate.RunOutcome{Completed: true, ExitCode: 1}}

	ev, err := implstep.New(b, b, b, r, r, codingrun.GitIdentity{Username: "x"}, nil).Do(context.Background(), testCard(), res(1))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if ev.NextPhase != card.PhaseImplementation {
		t.Fatalf("next phase = %q; a failed attempt stays in implementation for the next rung", ev.NextPhase)
	}
	if ev.NextState != "" {
		t.Errorf("moved the card off implementation: %q", ev.NextState)
	}
	if len(b.recorded) != 1 || b.recorded[0].Result.Status != runner.StatusFailed {
		t.Fatalf("the failed verification was not recorded as a failed attempt: %+v", b.recorded)
	}
}

// §12.1: an infrastructure failure is not an attempt. Recording one burns a
// rung of the ladder on a problem no model was asked to solve.
func TestAnInfrastructureFailureIsRecordedButNotAsAnAttempt(t *testing.T) {
	b := board()
	r := &fakeRunner{result: &runner.CodingRunResult{
		Status: runner.StatusInfraError, Harness: "claude-code", Summary: "pod evicted",
	}}

	_, err := implstep.New(b, b, b, r, r, codingrun.GitIdentity{Username: "x"}, nil).Do(context.Background(), testCard(), res(1))
	if err == nil {
		t.Fatal("expected an error so the worker hands the card back")
	}
	if len(b.recorded) != 1 || b.recorded[0].Result.Status != runner.StatusInfraError {
		t.Fatalf("the outage was not recorded: %+v", b.recorded)
	}
	if r.verifies != 0 {
		t.Error("ran the green gate against a run that never happened")
	}
}

// §12.2: "The model does not receive seven pages of previous model monologue.
// It receives evidence." A retry gets the failing test output, not the last
// model's account of itself.
func TestARetryReceivesEvidenceNotMonologue(t *testing.T) {
	b := board()
	b.artifacts = append(b.artifacts,
		&store.Artifact{Type: store.ArtifactTestOutput, Content: "FAIL health_test.go:12 expected 200 got 404"},
		&store.Artifact{Type: store.ArtifactFailureSummary, Content: "I felt the handler should live elsewhere and reflected at length"},
	)
	r := &fakeRunner{result: done(), verify: redgate.RunOutcome{Completed: true, ExitCode: 0}}

	if _, err := implstep.New(b, b, b, r, r, codingrun.GitIdentity{Username: "x"}, nil).Do(context.Background(), testCard(), res(2)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.req.Task, "expected 200 got 404") {
		t.Errorf("the retry did not receive the failing test output:\n%s", r.req.Task)
	}
	if strings.Contains(r.req.Task, "reflected at length") {
		t.Errorf("the retry received a previous model's monologue:\n%s", r.req.Task)
	}
}

// The tests are the contract the red gate established. An implementer that
// edits them can make anything pass.
func TestTheTaskForbidsChangingTheTests(t *testing.T) {
	b := board()
	r := &fakeRunner{result: done(), verify: redgate.RunOutcome{Completed: true}}

	if _, err := implstep.New(b, b, b, r, r, codingrun.GitIdentity{Username: "x"}, nil).Do(context.Background(), testCard(), res(1)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(r.req.Task), "must not") ||
		!strings.Contains(strings.ToLower(r.req.Task), "test") {
		t.Errorf("the task does not forbid editing the tests:\n%s", r.req.Task)
	}
}

// A verification that never completed is not a green light and not a failed
// attempt: it is an outage, and the card must be retried without the ladder
// moving.
func TestAnIncompleteVerificationDoesNotCountAsAnAttempt(t *testing.T) {
	b := board()
	r := &fakeRunner{result: done(), verify: redgate.RunOutcome{Completed: false}}

	_, err := implstep.New(b, b, b, r, r, codingrun.GitIdentity{Username: "x"}, nil).Do(context.Background(), testCard(), res(1))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, rec := range b.recorded {
		if rec.Result.Status == runner.StatusFailed {
			t.Fatal("counted an attempt on a verification that never ran")
		}
	}
}

// A provider whose harness cannot run a coding Job -- a chat-completion
// endpoint pointed at the implementation ladder -- is a policy mistake, not a
// transient failure. Returning an error would hand the card back, and the next
// Meeseeks would claim it and fail identically every reconcile interval,
// forever, looking like progress the whole time.
func TestAHarnessThatCannotCodeStopsTheCardInsteadOfSpinning(t *testing.T) {
	b := board()
	r := &fakeRunner{err: codingrun.ErrNoAdapter}

	ev, err := implstep.New(b, b, b, r, r, codingrun.GitIdentity{Username: "x"}, nil).Do(context.Background(), testCard(), res(1))
	if err != nil {
		t.Fatalf("Do returned an error, so the card would be handed back and retried: %v", err)
	}
	if ev.NextState != card.NeedsHuman {
		t.Fatalf("next state = %q, want NeedsHuman", ev.NextState)
	}
	// The operator has to learn which alias to repoint.
	if ev.Detail["alias"] != "implement-cheap" {
		t.Errorf("the evidence does not name the alias to fix: %+v", ev.Detail)
	}
}

// The implementation phase writes the code, and its raw output was the one
// thing nothing kept -- not on failure, not on success. It survived only in the
// Job's pod log, and the Job is deleted as soon as the control plane reads it.
func TestTheImplementationRunLogIsAlwaysKept(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status runner.Status
	}{
		{"a run that shipped", runner.StatusCompleted},
		{"a run that failed", runner.StatusFailed},
		{"a run that never got going", runner.StatusInfraError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := board()
			r := &fakeRunner{
				result: &runner.CodingRunResult{
					Status: tc.status, Summary: "did a thing",
					Harness: "opencode", Model: "deepseek-v4",
					Raw: []byte("tool: write src/x.js\nassistant: done"),
				},
				verify: redgate.RunOutcome{Completed: true, ExitCode: 0},
			}

			_, _ = implstep.New(b, b, b, r, r, codingrun.GitIdentity{Username: "x"}, nil).
				Do(context.Background(), testCard(), res(1))

			var found *store.Artifact
			for i := range b.put {
				if b.put[i].Type == store.ArtifactRunLog {
					found = &b.put[i]
				}
			}
			if found == nil {
				t.Fatalf("no run log stored for %s; the only copy is in a deleted pod", tc.status)
			}
			if !strings.Contains(found.Content, "tool: write src/x.js") {
				t.Errorf("run log = %q, want the harness's actual output", found.Content)
			}
		})
	}
}
