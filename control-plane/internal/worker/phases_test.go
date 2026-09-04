package worker

import (
	"context"
	"errors"
	"strings"
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
	order      []string

	// phaseAttempts is what PhaseAttempts reports for the current run, and
	// unpriced how many of the card's runs have no known cost.
	phaseAttempts int
	unpriced      int
}

func (p *phaseStore) UnpricedAttempts(context.Context, uuid.UUID) (int, error) {
	return p.unpriced, nil
}

func (p *phaseStore) ClaimReady(context.Context, string, time.Duration) (*card.Card, error) {
	if p.claimed == nil {
		return nil, ErrNoWork
	}
	return p.claimed, nil
}
func (p *phaseStore) NoteStepOutcome(context.Context, uuid.UUID, bool) error { return nil }
func (p *phaseStore) PhaseAttempts(context.Context, uuid.UUID, string) (int, error) {
	return p.phaseAttempts, nil
}

func (p *phaseStore) Heartbeat(context.Context, uuid.UUID, string, time.Duration) error { return nil }
func (p *phaseStore) Release(_ context.Context, _ uuid.UUID, _, reason string) error {
	p.released = reason
	p.order = append(p.order, "release")
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
	p.order = append(p.order, "advance")
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

// §7.1: "perform exactly one workflow ... release claim → EXIT", and "a card
// may require several Meeseeks over its lifetime. That is desirable."
//
// So a step that finishes a phase advances it and then HANDS THE CARD BACK.
// Keeping the claim would make one worker carry a card through planning,
// tests and implementation -- exactly the long-running assistant with an
// ever-growing pile of context §7 forbids -- and would also park the card
// under a live lease no other worker could take.
func TestAdvancingAPhaseHandsTheCardBack(t *testing.T) {
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
	if st.released == "" {
		t.Error("kept the claim; the next phase needs a fresh Meeseeks (§7.1)")
	}
	// The reason names the phase, so the audit log says why the card came
	// back rather than implying the worker gave up.
	if !strings.Contains(st.released, string(card.PhaseTests)) {
		t.Errorf("release reason %q does not name the new phase", st.released)
	}
}

// The phase must be advanced BEFORE the card is released, or the next
// Meeseeks claims it and re-runs the phase that just finished.
func TestThePhaseAdvancesBeforeTheCardIsHandedBack(t *testing.T) {
	st := &phaseStore{claimed: inProgress()}

	if _, err := runWith(t, st, Evidence{Summary: "plan written", NextPhase: card.PhaseTests}); err != nil {
		t.Fatal(err)
	}
	if len(st.order) < 2 {
		t.Fatalf("order = %v", st.order)
	}
	var advanceAt, releaseAt = -1, -1
	for i, step := range st.order {
		switch step {
		case "advance":
			advanceAt = i
		case "release":
			releaseAt = i
		}
	}
	if advanceAt == -1 || releaseAt == -1 || advanceAt > releaseAt {
		t.Fatalf("phase advanced after the release: %v", st.order)
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

// The ladder index used to come from implementation_attempt, which every
// phase increments and nothing resets. A card that had spent ONE
// implementation attempt resolved its next review at attempt 2 -- off the end
// of review's one-rung ladder -- so the reviewer was refused before it ran and
// the whole correctable path (review asks for a fix, implementation makes it,
// review looks again) could never complete for any card.
func TestReviewIsNotIndexedByImplementationAttempts(t *testing.T) {
	c := &card.Card{
		ID: uuid.New(), Title: "Command line with filtering",
		State: card.InProgress, Phase: card.PhaseReview,
		// Two implementation attempts already spent on this card.
		ImplementationAttempt: 2,
	}
	// ...and no review attempt yet in this verification cycle.
	st := &phaseStore{claimed: c, phaseAttempts: 0}

	if _, err := runWith(t, st, Evidence{Summary: "review completed", NextState: card.Review}); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if st.transition == card.NeedsHuman {
		t.Fatalf("review escalated on a card whose implementation attempts are irrelevant to it: %q", st.released)
	}
}

// Two reviews in a row with no implementation between them IS a retry, and
// review's ladder is one rung. Per-cycle counting must not become no counting.
func TestASecondReviewInTheSameCycleStillExhausts(t *testing.T) {
	c := &card.Card{
		ID: uuid.New(), Title: "t",
		State: card.InProgress, Phase: card.PhaseReview,
	}
	st := &phaseStore{claimed: c, phaseAttempts: 1}

	if _, err := runWith(t, st, Evidence{Summary: "review completed", NextState: card.Review}); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if st.transition != card.NeedsHuman {
		t.Error("a second review in one cycle did not exhaust the ladder")
	}
}

// Implementation stays cumulative across the whole card (§12.3). Resetting it
// each time review sent work back is what would make the correctable loop
// unbounded.
func TestImplementationStaysCumulative(t *testing.T) {
	c := &card.Card{
		ID: uuid.New(), Title: "t",
		State: card.InProgress, Phase: card.PhaseImplementation,
		// Past the end of the 3+3+1 ladder.
		ImplementationAttempt: 7,
	}
	st := &phaseStore{claimed: c, phaseAttempts: 0}

	if _, err := runWith(t, st, Evidence{Summary: "implemented", NextPhase: card.PhaseReview}); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if st.transition != card.NeedsHuman {
		t.Error("an eighth implementation attempt was allowed; the ladder is not cumulative")
	}
}

// §22's budget existed as a column, was rendered by the UI, the API and the
// Vikunja description, and was compared to spend by nothing. The state
// machine's own table lists "InProgress -> NeedsHuman: budget" as a reason and
// there was no code behind it: a card could spend without limit until a person
// noticed.
func TestACardOverItsBudgetStops(t *testing.T) {
	budget := 5.0
	c := &card.Card{
		ID: uuid.New(), Title: "expensive",
		State: card.InProgress, Phase: card.PhaseImplementation,
		CostUSD: 5.25, MaxCostUSD: &budget,
	}
	st := &phaseStore{claimed: c, unpriced: 0}

	if _, err := runWith(t, st, Evidence{Summary: "implemented", NextPhase: card.PhaseReview}); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if st.transition != card.NeedsHuman {
		t.Fatal("a card past its budget kept working")
	}
}

func TestACardInsideItsBudgetKeepsWorking(t *testing.T) {
	budget := 5.0
	c := &card.Card{
		ID: uuid.New(), Title: "fine",
		State: card.InProgress, Phase: card.PhaseImplementation,
		CostUSD: 1.10, MaxCostUSD: &budget,
	}
	st := &phaseStore{claimed: c}

	if _, err := runWith(t, st, Evidence{Summary: "implemented", NextPhase: card.PhaseReview}); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if st.transition == card.NeedsHuman {
		t.Errorf("a card inside its budget was stopped: %q", st.released)
	}
}

// The trap this whole guard is built around. cost_usd is a FLOOR whenever any
// run is unpriced -- opencode reports zero for providers models.dev has no
// rates for, and the Hermes gateway reports no cost at all. Comparing a budget
// against that number would enforce arithmetic rather than a limit, and would
// stop cards for spending an amount nobody measured.
func TestAnUnpricedCardIsNotStoppedByANumberNobodyMeasured(t *testing.T) {
	budget := 0.01
	c := &card.Card{
		ID: uuid.New(), Title: "unpriced",
		State: card.InProgress, Phase: card.PhaseImplementation,
		// Reads as over budget, but only because nothing has been priced.
		CostUSD: 0.02, MaxCostUSD: &budget,
	}
	st := &phaseStore{claimed: c, unpriced: 3}

	if _, err := runWith(t, st, Evidence{Summary: "implemented", NextPhase: card.PhaseReview}); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if st.transition == card.NeedsHuman {
		t.Error("stopped a card on a cost figure that is missing three of its runs")
	}
}

// A card with no budget is not a card with a budget of zero.
func TestACardWithNoBudgetIsNotStopped(t *testing.T) {
	c := &card.Card{
		ID: uuid.New(), Title: "no budget",
		State: card.InProgress, Phase: card.PhaseImplementation,
		CostUSD: 900,
	}
	st := &phaseStore{claimed: c}

	if _, err := runWith(t, st, Evidence{Summary: "implemented", NextPhase: card.PhaseReview}); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if st.transition == card.NeedsHuman {
		t.Error("a card with no budget was stopped anyway")
	}
}
