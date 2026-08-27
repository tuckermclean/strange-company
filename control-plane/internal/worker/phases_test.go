package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/policy"
)

// phaseStore records phase advances alongside the usual claim bookkeeping.
type phaseStore struct {
	claimed    *card.Card
	advancedTo card.Phase
	advanceErr error
	released   string
	transition card.State
	evidence   []Evidence
}

func (p *phaseStore) ClaimReady(context.Context, string, time.Duration) (*card.Card, error) {
	if p.claimed == nil {
		return nil, ErrNoWork
	}
	return p.claimed, nil
}
func (p *phaseStore) Heartbeat(context.Context, uuid.UUID, string, time.Duration) error { return nil }
func (p *phaseStore) Release(_ context.Context, _ uuid.UUID, _, reason string) error {
	p.released = reason
	return nil
}
func (p *phaseStore) Transition(_ context.Context, _ uuid.UUID, to card.State, _ card.ActorType, _, _ string) error {
	p.transition = to
	return nil
}
func (p *phaseStore) AttachEvidence(_ context.Context, _ uuid.UUID, ev Evidence) error {
	p.evidence = append(p.evidence, ev)
	return nil
}
func (p *phaseStore) AdvancePhase(_ context.Context, _ uuid.UUID, to card.Phase, _ card.ActorType, _, _ string) error {
	if p.advanceErr != nil {
		return p.advanceErr
	}
	p.advancedTo = to
	return nil
}

type stepFunc func(ctx context.Context, c *card.Card, res *policy.Resolution) (Evidence, error)

func (f stepFunc) Do(ctx context.Context, c *card.Card, res *policy.Resolution) (Evidence, error) {
	return f(ctx, c, res)
}

func inProgress() *card.Card {
	return &card.Card{ID: uuid.New(), Title: "t", State: card.InProgress, Phase: card.PhasePlanning}
}

func runWith(t *testing.T, st *phaseStore, ev Evidence) (Outcome, error) {
	t.Helper()
	pol, err := policy.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	m := New("worker-1", st, pol, stepFunc(func(context.Context, *card.Card, *policy.Resolution) (Evidence, error) {
		return ev, nil
	}), nil, time.Minute)
	return m.RunOnce(context.Background())
}

// §11's phases happen while a card is InProgress and the state machine has no
// InProgress -> InProgress transition, so a step that finished a phase has to
// be able to say so without naming a state.
func TestAStepThatAdvancesAPhaseKeepsTheCardInProgress(t *testing.T) {
	st := &phaseStore{claimed: inProgress()}

	outcome, err := runWith(t, st, Evidence{Summary: "plan written", NextPhase: card.PhaseTests})
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if outcome != OutcomeAdvanced {
		t.Fatalf("outcome = %q, want advanced", outcome)
	}
	if st.advancedTo != card.PhaseTests {
		t.Errorf("advanced to %q", st.advancedTo)
	}
	if st.transition != "" {
		t.Errorf("also transitioned the card to %q", st.transition)
	}
	if st.released != "" {
		t.Errorf("released a card that is still being worked on: %q", st.released)
	}
}

// Evidence before the change, exactly as for a transition: §21 requires the
// audit log to explain every move, and a phase advancing with nothing on
// record is the same gap by another name.
func TestEvidenceIsAttachedBeforeThePhaseAdvances(t *testing.T) {
	st := &phaseStore{claimed: inProgress()}

	if _, err := runWith(t, st, Evidence{Summary: "plan written", NextPhase: card.PhaseTests}); err != nil {
		t.Fatal(err)
	}
	if len(st.evidence) != 1 || st.evidence[0].Summary != "plan written" {
		t.Fatalf("evidence = %+v", st.evidence)
	}
}

// A step must say what happens next. Silence would leave a card claimed with
// its lease ticking down and no work being done on it.
func TestAStepThatNamesNeitherAStateNorAPhaseReleasesTheCard(t *testing.T) {
	st := &phaseStore{claimed: inProgress()}

	outcome, err := runWith(t, st, Evidence{Summary: "did something"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if outcome != OutcomeReleased {
		t.Fatalf("outcome = %q, want released", outcome)
	}
	if st.released == "" {
		t.Error("the card was not handed back")
	}
}

// Naming both would leave it ambiguous whether the card moved on or kept
// working, and the two have opposite consequences for the claim.
func TestAStepCannotNameBothAStateAndAPhase(t *testing.T) {
	st := &phaseStore{claimed: inProgress()}

	outcome, _ := runWith(t, st, Evidence{
		Summary: "x", NextState: card.Review, NextPhase: card.PhaseTests,
	})
	if outcome != OutcomeReleased {
		t.Fatalf("outcome = %q, want released", outcome)
	}
	if st.advancedTo != "" || st.transition != "" {
		t.Errorf("acted on an ambiguous instruction: phase=%q state=%q", st.advancedTo, st.transition)
	}
}

// A failed advance must hand the card back rather than leaving it claimed in
// a phase that did not change.
func TestAFailedAdvanceReleasesTheCard(t *testing.T) {
	st := &phaseStore{claimed: inProgress(), advanceErr: errors.New("database down")}

	outcome, err := runWith(t, st, Evidence{Summary: "plan written", NextPhase: card.PhaseTests})
	if err == nil {
		t.Fatal("expected an error")
	}
	if outcome != OutcomeReleased || st.released == "" {
		t.Fatalf("outcome = %q, released = %q", outcome, st.released)
	}
}

// The existing contract is untouched: a step naming a state still transitions.
func TestAStepThatNamesAStateStillTransitions(t *testing.T) {
	st := &phaseStore{claimed: inProgress()}

	outcome, err := runWith(t, st, Evidence{Summary: "green", NextState: card.Review})
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if outcome != OutcomeCompleted || st.transition != card.Review {
		t.Fatalf("outcome = %q, transition = %q", outcome, st.transition)
	}
	if st.advancedTo != "" {
		t.Errorf("also advanced the phase to %q", st.advancedTo)
	}
}
