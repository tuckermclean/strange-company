package worker

import (
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
