package worker

import (
	"errors"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/policy"
)

func infraCard(failures int) *card.Card {
	c := testCard()
	c.InfrastructureFailures = failures
	return c
}

func neverRuns(t *testing.T) *fakeStep {
	t.Helper()
	return &fakeStep{doFunc: func(context.Context, *card.Card, *policy.Resolution) (Evidence, error) {
		t.Error("the step ran; an escalating card must not spend another model call")
		return Evidence{}, nil
	}}
}

// §12.1 is right that an infrastructure failure must not burn an attempt -- a
// provider outage is not the model failing the work. But nothing ever read the
// counter it increments, and "burns no attempt" with no bound on top of it
// means a card that CANNOT run retries forever.
//
// Observed: a reviewer whose read deadline was too short for a large diff timed
// out, was released without burning an attempt, was re-promoted a minute later,
// and timed out again. A full reasoning call every four minutes, indefinitely,
// opening no pull request and telling nobody.
func TestACardThatKeepsFailingForNonModelReasonsReachesAHuman(t *testing.T) {
	c := infraCard(DefaultMaxInfraFailures)
	cards := &fakeCardStore{claimReadyFunc: func() (*card.Card, error) { return c, nil }}

	out, err := New("w1", cards, testPolicy(3), neverRuns(t), testLogger(), time.Minute).
		RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce() error = %v, want nil", err)
	}
	if out != OutcomeEscalated {
		t.Fatalf("outcome = %q, want %q", out, OutcomeEscalated)
	}

	if len(cards.transitionCalls) != 1 {
		t.Fatalf("transitions = %+v, want exactly one", cards.transitionCalls)
	}
	got := cards.transitionCalls[0]
	if got.to != card.NeedsHuman {
		t.Errorf("transitioned to %q, want %q", got.to, card.NeedsHuman)
	}
	// The reason must name the cause. A human opening this card otherwise
	// sees only that it stopped.
	if !strings.Contains(got.reason, "infrastructure") {
		t.Errorf("reason = %q, want it to say why", got.reason)
	}
	// "Escalated to NeedsHuman" with a bare count reads as "the machine gave
	// up on your code", and a human who believes that will not send the card
	// back in -- which is all it needs.
	if !strings.Contains(got.reason, "could not be RUN") {
		t.Errorf("reason = %q, want it to separate 'could not run' from 'the work is bad'", got.reason)
	}
	if !strings.Contains(got.reason, "clears the count") {
		t.Errorf("reason = %q, want it to say how to send the card back", got.reason)
	}
}

// One outage is weather. Recovering from it without troubling anyone is the
// entire point of not counting infrastructure against the ladder.
func TestASingleOutageDoesNotEscalate(t *testing.T) {
	c := infraCard(1)
	ran := false
	cards := &fakeCardStore{claimReadyFunc: func() (*card.Card, error) { return c, nil }}
	step := &fakeStep{doFunc: func(context.Context, *card.Card, *policy.Resolution) (Evidence, error) {
		ran = true
		return Evidence{Summary: "did the work", NextState: card.Review}, nil
	}}

	out, err := New("w1", cards, testPolicy(3), step, testLogger(), time.Minute).
		RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce() error = %v, want nil", err)
	}
	if out == OutcomeEscalated {
		t.Fatal("a single infrastructure failure escalated to a human")
	}
	if !ran {
		t.Error("the step did not run")
	}
}

// The bound must be switchable off, and off must mean exactly the old
// behaviour -- otherwise an operator who needs it back cannot have it.
func TestTheBoundCanBeDisabled(t *testing.T) {
	c := infraCard(500)
	ran := false
	cards := &fakeCardStore{claimReadyFunc: func() (*card.Card, error) { return c, nil }}
	step := &fakeStep{doFunc: func(context.Context, *card.Card, *policy.Resolution) (Evidence, error) {
		ran = true
		return Evidence{Summary: "did the work", NextState: card.Review}, nil
	}}

	out, err := New("w1", cards, testPolicy(3), step, testLogger(), time.Minute).
		WithMaxInfraFailures(0).
		RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce() error = %v, want nil", err)
	}
	if out == OutcomeEscalated {
		t.Fatal("the bound fired while disabled")
	}
	if !ran {
		t.Error("the step did not run with the bound disabled")
	}
}

// The escalation must not leave the card claimed. It is going to a human, and
// a card reporting a worker that is not there is the state §7.1 forbids.
func TestAnEscalatedCardIsNotLeftClaimed(t *testing.T) {
	c := infraCard(DefaultMaxInfraFailures)
	cards := &fakeCardStore{claimReadyFunc: func() (*card.Card, error) { return c, nil }}

	if _, err := New("w1", cards, testPolicy(3), neverRuns(t), testLogger(), time.Minute).
		RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// The store clears the claim on any transition out of InProgress; the
	// worker must not additionally hand the card back to Ready, which would
	// undo the escalation.
	for _, r := range cards.releaseCalls {
		t.Errorf("released the card after escalating it: %q", r.reason)
	}
}

// A policy error is the one class of failure that cannot get better by trying
// again: the YAML does not change because a worker read it twice.
//
// This used to release the card, which brought it straight back to fail
// identically once a reconcile interval, forever -- and because a policy
// failure records no attempt, infrastructure_failures never moved and the
// bound that exists to stop exactly this never saw it.
func TestAPolicyThatCannotResolveAPhaseEscalatesRatherThanLooping(t *testing.T) {
	c := testCard()
	c.Phase = card.Phase("decomposition") // a phase this policy has never heard of
	cards := &fakeCardStore{claimReadyFunc: func() (*card.Card, error) { return c, nil }}

	out, err := New("w1", cards, testPolicy(3), neverRuns(t), testLogger(), time.Minute).
		RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce() error = %v, want nil", err)
	}
	if out != OutcomeEscalated {
		t.Fatalf("outcome = %q, want %q", out, OutcomeEscalated)
	}

	if len(cards.releaseCalls) != 0 {
		t.Errorf("the card was released back into the queue: %+v", cards.releaseCalls)
	}
	if len(cards.transitionCalls) != 1 || cards.transitionCalls[0].to != card.NeedsHuman {
		t.Fatalf("transitions = %+v, want one to NeedsHuman", cards.transitionCalls)
	}

	// The reason has to be actionable. "policy resolution failed" sends an
	// operator to the logs; naming the phase and the file sends them to the
	// line they have to change.
	reason := cards.transitionCalls[0].reason
	for _, want := range []string{"decomposition", "models.yaml", "configuration, not the work"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason = %q, want it to mention %q", reason, want)
		}
	}
}

// The worker counts every step outcome, including failures that never reach a
// model. RecordAttempt only ever saw the steps that got as far as a run, which
// is how six unbounded loops hid from the guard built to stop them.
func TestAStepFailureIsCountedEvenWhenNoRunHappened(t *testing.T) {
	c := testCard()
	cards := &fakeCardStore{claimReadyFunc: func() (*card.Card, error) { return c, nil }}
	step := &fakeStep{doFunc: func(context.Context, *card.Card, *policy.Resolution) (Evidence, error) {
		// Fails before touching a provider, like an unresolvable policy or
		// a child card that could not be written.
		return Evidence{}, errors.New("could not do the thing")
	}}

	_, _ = New("w1", cards, testPolicy(3), step, testLogger(), time.Minute).RunOnce(context.Background())

	if len(cards.stepOutcomes) != 1 {
		t.Fatalf("step outcomes = %v, want one recorded", cards.stepOutcomes)
	}
	if cards.stepOutcomes[0] {
		t.Error("a failed step was recorded as having run")
	}
}

// And a step that ran clears the count: what the bound asks is whether the card
// can make progress now, not whether it has ever had a bad day.
func TestAStepThatRanClearsTheCount(t *testing.T) {
	c := testCard()
	cards := &fakeCardStore{claimReadyFunc: func() (*card.Card, error) { return c, nil }}
	step := &fakeStep{doFunc: func(context.Context, *card.Card, *policy.Resolution) (Evidence, error) {
		return Evidence{Summary: "did the work", NextState: card.Review}, nil
	}}

	if _, err := New("w1", cards, testPolicy(3), step, testLogger(), time.Minute).
		RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(cards.stepOutcomes) != 1 || !cards.stepOutcomes[0] {
		t.Errorf("step outcomes = %v, want one success recorded", cards.stepOutcomes)
	}
}
