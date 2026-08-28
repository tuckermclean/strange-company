package plan_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/modelclient"
	"github.com/tuckermclean/strange-company/control-plane/internal/plan"
	"github.com/tuckermclean/strange-company/control-plane/internal/policy"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
)

const specText = `# Context

The service exposes no health signal.

# Task

Add a health endpoint.

# Acceptance criteria

- AC1: returns 200 when healthy — verified by: ` + "`curl -fsS localhost:8080/healthz`"

type fakeSpecs struct {
	spec *store.CardSpec
	err  error
}

func (f *fakeSpecs) GetSpec(context.Context, uuid.UUID) (*store.CardSpec, error) {
	return f.spec, f.err
}

type fakeArtifacts struct {
	put []store.Artifact
	err error
}

func (f *fakeArtifacts) PutArtifact(_ context.Context, a store.Artifact) (*store.Artifact, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.put = append(f.put, a)
	a.ID = uuid.New()
	return &a, nil
}

type fakeModel struct {
	prompt string
	reply  string
	err    error
}

func (f *fakeModel) Complete(_ context.Context, req modelclient.CompleteRequest) (*modelclient.Completion, error) {
	for _, m := range req.Messages {
		f.prompt += m.Content + "\n"
	}
	if f.err != nil {
		return nil, f.err
	}
	return &modelclient.Completion{Text: f.reply, Model: "claude-opus-5"}, nil
}

func testCard() *card.Card {
	repo := "https://github.com/example/repo"
	ref := "main"
	return &card.Card{
		ID: uuid.New(), Title: "Add a health endpoint",
		State: card.InProgress, Phase: card.PhasePlanning,
		RepoURL: &repo, RepoBaseRef: &ref,
	}
}

func resolution() *policy.Resolution {
	return &policy.Resolution{Phase: "planning", Alias: "plan", ProviderName: "anthropic-api", Model: "claude-opus-5"}
}

func step(specs *fakeSpecs, arts *fakeArtifacts, m *fakeModel) *plan.Step {
	return plan.New(specs, arts, func(*policy.Resolution) (plan.Completer, error) { return m, nil }, nil)
}

func TestAPlanIsStoredAndThePhaseAdvances(t *testing.T) {
	specs := &fakeSpecs{spec: &store.CardSpec{Content: specText, Approved: true}}
	arts := &fakeArtifacts{}
	m := &fakeModel{reply: "1. Add handler in server.go\n2. Verify with curl"}
	c := testCard()

	ev, err := step(specs, arts, m).Do(context.Background(), c, resolution())
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if ev.NextPhase != card.PhaseTests {
		t.Errorf("next phase = %q, want tests", ev.NextPhase)
	}
	if ev.NextState != "" {
		t.Errorf("named a state as well as a phase: %q", ev.NextState)
	}
	if len(arts.put) != 1 {
		t.Fatalf("stored %d artifacts", len(arts.put))
	}
	got := arts.put[0]
	if got.Type != store.ArtifactImplementationPlan {
		t.Errorf("artifact type = %q", got.Type)
	}
	if got.Content != m.reply {
		t.Errorf("stored %q, not the plan the model returned", got.Content)
	}
	// The model is recorded so the cost ledger and §21's audit can say which
	// rung produced this.
	if got.Model != "claude-opus-5" {
		t.Errorf("artifact model = %q", got.Model)
	}
}

// §11.1: the plan's inputs are the spec, the repository and the criteria. A
// planner given none of them is guessing, which the same section forbids.
func TestThePromptCarriesTheSpecAndTheRepository(t *testing.T) {
	specs := &fakeSpecs{spec: &store.CardSpec{Content: specText, Approved: true}}
	m := &fakeModel{reply: "a plan"}

	if _, err := step(specs, &fakeArtifacts{}, m).Do(context.Background(), testCard(), resolution()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Add a health endpoint", "https://github.com/example/repo", "AC1", "returns 200 when healthy"} {
		if !strings.Contains(m.prompt, want) {
			t.Errorf("prompt is missing %q", want)
		}
	}
}

// §11.1: "A planner may declare SPEC_INSUFFICIENT ... It may not guess."
// Detected structurally rather than by reading the prose, so the decision is
// the model's to state and nobody's to interpret.
func TestAPlannerMayDeclareTheSpecInsufficient(t *testing.T) {
	specs := &fakeSpecs{spec: &store.CardSpec{Content: specText, Approved: true}}
	arts := &fakeArtifacts{}
	m := &fakeModel{reply: "SPEC_INSUFFICIENT: the failure mode for a dead database is unstated"}

	ev, err := step(specs, arts, m).Do(context.Background(), testCard(), resolution())
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if ev.NextState != card.NeedsHuman {
		t.Errorf("next state = %q, want NeedsHuman", ev.NextState)
	}
	if ev.NextPhase != "" {
		t.Errorf("advanced the phase on an insufficient spec: %q", ev.NextPhase)
	}
	// The declaration is evidence: without it a human sees a card in
	// NeedsHuman and no statement of what was missing.
	if len(arts.put) != 1 || arts.put[0].Type != store.ArtifactFailureSummary {
		t.Fatalf("artifacts = %+v", arts.put)
	}
	if !strings.Contains(arts.put[0].Content, "failure mode for a dead database") {
		t.Errorf("the reason was not recorded: %q", arts.put[0].Content)
	}
}

// A provider outage is not a failed plan. Returning an error hands the card
// back for another attempt; storing an empty plan and advancing would send the
// test-writer off a document the model never produced.
func TestAProviderFailureIsNotAPlan(t *testing.T) {
	specs := &fakeSpecs{spec: &store.CardSpec{Content: specText, Approved: true}}
	arts := &fakeArtifacts{}
	m := &fakeModel{err: modelclient.ErrProviderFailure}

	_, err := step(specs, arts, m).Do(context.Background(), testCard(), resolution())
	if err == nil {
		t.Fatal("expected an error")
	}
	if len(arts.put) != 0 {
		t.Fatalf("stored an artifact for a call that failed: %+v", arts.put)
	}
}

// An empty answer is not a plan either, and advancing on one would hand the
// test-writer nothing to work from.
func TestAnEmptyPlanIsRefused(t *testing.T) {
	specs := &fakeSpecs{spec: &store.CardSpec{Content: specText, Approved: true}}
	arts := &fakeArtifacts{}
	m := &fakeModel{reply: "   \n  "}

	if _, err := step(specs, arts, m).Do(context.Background(), testCard(), resolution()); err == nil {
		t.Fatal("expected an error")
	}
	if len(arts.put) != 0 {
		t.Fatalf("stored an empty plan: %+v", arts.put)
	}
}

// Planning without a specification is the guess §11.1 forbids, and it must not
// cost a model call to discover.
func TestPlanningWithoutASpecificationCallsNoModel(t *testing.T) {
	specs := &fakeSpecs{err: errors.New("no spec")}
	m := &fakeModel{reply: "a plan"}

	ev, err := step(specs, &fakeArtifacts{}, m).Do(context.Background(), testCard(), resolution())
	if err == nil && ev.NextState != card.NeedsHuman {
		t.Fatalf("planning proceeded without a specification: ev = %+v, err = %v", ev, err)
	}
	if m.prompt != "" {
		t.Error("spent a model call to discover there was no specification")
	}
}
