package card

import (
	"errors"
	"strings"
	"testing"
)

// allStates lists every known State, used by the exhaustive matrix test and
// by the terminal/self-transition tests below.
var allStates = []State{Backlog, Ready, InProgress, Review, Done, Blocked, NeedsHuman}

// allActors lists every known ActorType.
var allActors = []ActorType{ActorHuman, ActorAgent, ActorSystem}

func wantAllowed(t *testing.T, from, to State, actor ActorType) {
	t.Helper()
	if err := CanTransition(from, to, actor); err != nil {
		t.Fatalf("CanTransition(%s, %s, %s) = %v, want nil (transition should be permitted)", from, to, actor, err)
	}
}

func wantForbidden(t *testing.T, from, to State, actor ActorType, rule string) {
	t.Helper()
	err := CanTransition(from, to, actor)
	if err == nil {
		t.Fatalf("%s: CanTransition(%s, %s, %s) = nil, want an error", rule, from, to, actor)
	}
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("%s: CanTransition(%s, %s, %s) error %v does not wrap ErrIllegalTransition", rule, from, to, actor, err)
	}
}

// --- Rule 1 (spec section 18) --------------------------------------------
//
// This is the single most important rule in the file: automated review must
// never be able to complete a card. Only a human may move Review -> Done.

func TestAutomatedReviewCannotCompleteACard(t *testing.T) {
	const rule = "spec 18: automated review must never move a card to Done"

	t.Run("agent is forbidden", func(t *testing.T) {
		wantForbidden(t, Review, Done, ActorAgent, rule)
	})

	t.Run("system is forbidden", func(t *testing.T) {
		wantForbidden(t, Review, Done, ActorSystem, rule)
	})

	t.Run("human is allowed", func(t *testing.T) {
		wantAllowed(t, Review, Done, ActorHuman)
	})
}

// --- Rule 2 (spec section 4.2 / 10) ---------------------------------------
//
// A card must pass through Ready — which requires a spec and acceptance
// criteria — before it can become InProgress. Backlog -> InProgress is
// forbidden for every actor, including a human.

func TestBacklogCannotSkipReadyToInProgress(t *testing.T) {
	const rule = "spec 4.2/10: a card must pass through Ready before InProgress"

	for _, actor := range allActors {
		actor := actor
		t.Run(string(actor), func(t *testing.T) {
			wantForbidden(t, Backlog, InProgress, actor, rule)
		})
	}
}

// --- Rule 9 ----------------------------------------------------------------
//
// Leaving Blocked or NeedsHuman requires ActorHuman. An agent must never be
// able to un-block itself.

func TestOnlyHumanCanUnblockACard(t *testing.T) {
	const rule = "rule 9: an agent must never be able to un-block itself"

	for _, from := range []State{Blocked, NeedsHuman} {
		from := from
		t.Run(string(from)+"/agent forbidden", func(t *testing.T) {
			wantForbidden(t, from, Ready, ActorAgent, rule)
		})
		t.Run(string(from)+"/system forbidden", func(t *testing.T) {
			wantForbidden(t, from, Ready, ActorSystem, rule)
		})
		t.Run(string(from)+"/human allowed", func(t *testing.T) {
			wantAllowed(t, from, Ready, ActorHuman)
		})
	}
}

// --- Remaining individually named rules ------------------------------------

func TestBacklogCanPromoteToReady(t *testing.T) {
	// Rule 3: the promotion happens after the deterministic specification
	// gate (spec section 10); any actor may perform it once the gate passes.
	for _, actor := range allActors {
		wantAllowed(t, Backlog, Ready, actor)
	}
}

func TestClaimingMovesReadyToInProgress(t *testing.T) {
	// Rule 4: this is what atomic claiming does (spec section 6).
	for _, actor := range allActors {
		wantAllowed(t, Ready, InProgress, actor)
	}
}

func TestImplementationMovesToReview(t *testing.T) {
	// Rule 5.
	for _, actor := range allActors {
		wantAllowed(t, InProgress, Review, actor)
	}
}

func TestHumanRejectionReturnsReviewToReady(t *testing.T) {
	// Rule 6 (spec section 19): human rejection returns the card for more
	// work. Only a human may reject.
	const rule = "spec 19: only a human may reject a card from Review back to Ready"

	wantAllowed(t, Review, Ready, ActorHuman)
	wantForbidden(t, Review, Ready, ActorAgent, rule)
	wantForbidden(t, Review, Ready, ActorSystem, rule)
}

func TestPolicyViolationBlocksFromAnyActiveState(t *testing.T) {
	// Rule 7 (spec section 24): a policy violation blocks immediately,
	// regardless of actor. Blocked and NeedsHuman are excluded here since
	// "any active state" from -> Blocked would otherwise include the
	// self-transition Blocked -> Blocked, which rule 11 forbids.
	for _, from := range []State{Backlog, Ready, InProgress, Review} {
		for _, actor := range allActors {
			wantAllowed(t, from, Blocked, actor)
		}
	}
}

func TestEscalationToNeedsHumanFromAnyActiveState(t *testing.T) {
	// Rule 8: escalation exhausted, budget exceeded, or spec insufficient —
	// any actor may raise the escalation. NeedsHuman itself and Blocked are
	// excluded for the same self-transition reason as above.
	for _, from := range []State{Backlog, Ready, InProgress, Review} {
		for _, actor := range allActors {
			wantAllowed(t, from, NeedsHuman, actor)
		}
	}
}

func TestDoneIsTerminal(t *testing.T) {
	// Rule 10: no transition out of Done is legal for any actor.
	const rule = "rule 10: Done is terminal"

	for _, to := range allStates {
		if to == Done {
			continue // covered by TestSelfTransitionIsIllegal
		}
		for _, actor := range allActors {
			wantForbidden(t, Done, to, actor, rule)
		}
	}
}

func TestSelfTransitionIsIllegal(t *testing.T) {
	// Rule 11: a transition from a state to itself would create a
	// meaningless history row.
	const rule = "rule 11: a state cannot transition to itself"

	for _, s := range allStates {
		for _, actor := range allActors {
			wantForbidden(t, s, s, actor, rule)
		}
	}
}

func TestUnknownStateIsIllegalNotPanic(t *testing.T) {
	// Rule 12: an unknown/garbage State value is illegal and must not panic.
	const rule = "rule 12: an unknown state is illegal, not a panic"
	garbage := State("Garbage")

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("CanTransition panicked on garbage from-state: %v", r)
			}
		}()
		wantForbidden(t, garbage, Ready, ActorHuman, rule)
	}()

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("CanTransition panicked on garbage to-state: %v", r)
			}
		}()
		wantForbidden(t, Backlog, garbage, ActorHuman, rule)
	}()

	if ValidState(garbage) {
		t.Fatalf("ValidState(%q) = true, want false", garbage)
	}
}

func TestIllegalTransitionErrorNamesFromToAndActor(t *testing.T) {
	// Rule 13: the returned error must wrap ErrIllegalTransition so callers
	// can use errors.Is, and its message must name the from state, the to
	// state and the actor.
	err := CanTransition(Review, Done, ActorAgent)
	if err == nil {
		t.Fatal("CanTransition(Review, Done, ActorAgent) = nil, want an error")
	}
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("error %v does not wrap ErrIllegalTransition", err)
	}

	msg := err.Error()
	for _, want := range []string{string(Review), string(Done), string(ActorAgent)} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error message %q does not mention %q", msg, want)
		}
	}
}

func TestValidPhaseAcceptsOnlyKnownPhases(t *testing.T) {
	for _, p := range []Phase{
		PhaseSpecification, PhasePlanning, PhaseTests, PhaseImplementation,
		PhaseVerification, PhaseReview, PhaseComplete,
	} {
		if !ValidPhase(p) {
			t.Fatalf("ValidPhase(%q) = false, want true", p)
		}
	}

	if ValidPhase(Phase("bogus")) {
		t.Fatal(`ValidPhase("bogus") = true, want false`)
	}
}

// --- Full transition matrix -------------------------------------------------
//
// This test is in addition to, not a replacement for, the individually named
// tests above. It is the exhaustive cross-check that the transition table
// matches the workflow described in the spec: for every (from, to, actor)
// triple, exactly the documented moves are legal.

func TestTransitionMatrix(t *testing.T) {
	type pair struct{ from, to State }

	// permitted enumerates every (from, to) pair that is legal for at least
	// one actor, and exactly which actors may perform it. Any pair absent
	// from this map — or any actor absent from its list — must be rejected.
	permitted := map[pair][]ActorType{
		{Backlog, Ready}:      allActors,
		{Backlog, Blocked}:    allActors,
		{Backlog, NeedsHuman}: allActors,

		{Ready, InProgress}: allActors,
		{Ready, Blocked}:    allActors,
		{Ready, NeedsHuman}: allActors,

		{InProgress, Review}:     allActors,
		{InProgress, Blocked}:    allActors,
		{InProgress, NeedsHuman}: allActors,

		{Review, Done}:       {ActorHuman},
		{Review, Ready}:      {ActorHuman},
		{Review, Blocked}:    allActors,
		{Review, NeedsHuman}: allActors,

		{Blocked, Ready}: {ActorHuman},

		{NeedsHuman, Ready}: {ActorHuman},
	}

	isPermitted := func(from, to State, actor ActorType) bool {
		actors, ok := permitted[pair{from, to}]
		if !ok {
			return false
		}
		for _, a := range actors {
			if a == actor {
				return true
			}
		}
		return false
	}

	for _, from := range allStates {
		for _, to := range allStates {
			for _, actor := range allActors {
				want := from != to && isPermitted(from, to, actor)
				err := CanTransition(from, to, actor)
				got := err == nil

				if got != want {
					t.Errorf("CanTransition(%s, %s, %s): got allowed=%v, want allowed=%v (err=%v)",
						from, to, actor, got, want, err)
				}
				if err != nil && !errors.Is(err, ErrIllegalTransition) {
					t.Errorf("CanTransition(%s, %s, %s): error %v does not wrap ErrIllegalTransition", from, to, actor, err)
				}
			}
		}
	}
}
