package dispatch_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/dispatch"
	"github.com/tuckermclean/strange-company/control-plane/internal/policy"
	"github.com/tuckermclean/strange-company/control-plane/internal/worker"
)

type recordingStep struct {
	called bool
	ev     worker.Evidence
	err    error
}

func (r *recordingStep) Do(context.Context, *card.Card, *policy.Resolution) (worker.Evidence, error) {
	r.called = true
	return r.ev, r.err
}

func cardIn(phase card.Phase) *card.Card {
	return &card.Card{ID: uuid.New(), Title: "t", State: card.InProgress, Phase: phase}
}

func res(phase string) *policy.Resolution {
	return &policy.Resolution{Phase: phase, Model: "a-model"}
}

// A card promoted through the §10 gate still carries phase "specification" --
// the gate is what finished it. Starting planning is bookkeeping, not work,
// and must not cost a model call.
func TestACardStillInSpecificationAdvancesToPlanningWithoutAModel(t *testing.T) {
	planner := &recordingStep{}
	d := dispatch.New(map[card.Phase]worker.Step{card.PhasePlanning: planner}, nil)

	ev, err := d.Do(context.Background(), cardIn(card.PhaseSpecification), res("specification"))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if ev.NextPhase != card.PhasePlanning {
		t.Fatalf("next phase = %q, want planning", ev.NextPhase)
	}
	if planner.called {
		t.Error("ran the planner while the card was still in specification")
	}
}

func TestEachPhaseRunsItsOwnStep(t *testing.T) {
	planner := &recordingStep{ev: worker.Evidence{Summary: "planned", NextPhase: card.PhaseTests}}
	tests := &recordingStep{ev: worker.Evidence{Summary: "tests written", NextPhase: card.PhaseImplementation}}
	d := dispatch.New(map[card.Phase]worker.Step{
		card.PhasePlanning: planner,
		card.PhaseTests:    tests,
	}, nil)

	if _, err := d.Do(context.Background(), cardIn(card.PhasePlanning), res("planning")); err != nil {
		t.Fatal(err)
	}
	if !planner.called || tests.called {
		t.Fatalf("planning ran the wrong step: planner=%v tests=%v", planner.called, tests.called)
	}
}

// A phase with no step is a phase nothing can do. Returning an error would
// release the card back to Ready and the next Meeseeks would claim it and fail
// the same way -- a claim-release loop every reconcile interval, forever,
// looking like activity.
func TestAnUnbuiltPhaseGoesToAHumanRatherThanSpinning(t *testing.T) {
	d := dispatch.New(map[card.Phase]worker.Step{}, nil)

	ev, err := d.Do(context.Background(), cardIn(card.PhaseImplementation), res("implementation"))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if ev.NextState != card.NeedsHuman {
		t.Fatalf("next state = %q, want NeedsHuman", ev.NextState)
	}
	if ev.NextPhase != "" {
		t.Errorf("also advanced the phase: %q", ev.NextPhase)
	}
	// The summary has to name the phase, or a human sees a card in
	// NeedsHuman with no idea which part of the pipeline is missing.
	if !strings.Contains(ev.Summary, string(card.PhaseImplementation)) {
		t.Errorf("summary does not name the phase: %q", ev.Summary)
	}
}

func TestAStepsFailureIsPassedThrough(t *testing.T) {
	boom := errors.New("provider down")
	d := dispatch.New(map[card.Phase]worker.Step{
		card.PhasePlanning: &recordingStep{err: boom},
	}, nil)

	if _, err := d.Do(context.Background(), cardIn(card.PhasePlanning), res("planning")); !errors.Is(err, boom) {
		t.Fatalf("error = %v, want the step's own", err)
	}
}

// An unknown phase is a card nothing can run. It must be visible rather than
// silently retried.
func TestAnUnknownPhaseGoesToAHuman(t *testing.T) {
	d := dispatch.New(map[card.Phase]worker.Step{}, nil)

	ev, err := d.Do(context.Background(), cardIn(card.Phase("daydreaming")), res("x"))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if ev.NextState != card.NeedsHuman {
		t.Fatalf("next state = %q", ev.NextState)
	}
}
