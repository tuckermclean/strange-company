// Package card implements the Kanban card state machine described in
// docs/specs/strange-company-control-plane-v1.md sections 4.2, 18, 19 and 32.
//
// The control plane owns canonical execution state; Vikunja owns the
// human-visible board column (section 4.3). Every state change — whether
// proposed by a human, an agent, or the system itself — must be validated
// against CanTransition before it becomes canonical.
package card

import (
	"errors"
	"fmt"
)

// State is a card's canonical execution state. It corresponds 1:1 with the
// Kanban board columns in spec section 4.2, except that "In Progress" and
// "Needs Human" are spelled without spaces here since they are Go
// identifiers-as-values, not display strings.
type State string

// Phase is the sub-stage of work within the coding pipeline (spec section 11)
// while a card is InProgress. Phase is orthogonal to State: it records where
// in SPEC -> PLAN -> TESTS -> IMPLEMENT -> VERIFY -> REVIEW -> COMPLETE a card
// currently sits.
type Phase string

// ActorType identifies who is requesting a state transition. The state
// machine enforces different permissions for different actors — most
// importantly, section 18's rule that automated review can never complete a
// card, and the general principle that an agent must never be able to
// un-block itself.
type ActorType string

// Board states (spec section 4.2).
const (
	Backlog    State = "Backlog"
	Ready      State = "Ready"
	InProgress State = "InProgress"
	Review     State = "Review"
	Done       State = "Done"
	Blocked    State = "Blocked"
	NeedsHuman State = "NeedsHuman"
)

// Coding pipeline phases (spec section 11).
const (
	PhaseSpecification  Phase = "specification"
	PhasePlanning       Phase = "planning"
	PhaseTests          Phase = "tests"
	PhaseImplementation Phase = "implementation"
	PhaseVerification   Phase = "verification"
	PhaseReview         Phase = "review"
	PhaseComplete       Phase = "complete"
)

// Actor types.
const (
	ActorHuman  ActorType = "human"
	ActorAgent  ActorType = "agent"
	ActorSystem ActorType = "system"
)

// ErrIllegalTransition is the sentinel wrapped by every error CanTransition
// returns for a disallowed move. Callers should use errors.Is(err,
// ErrIllegalTransition) rather than matching on error text.
var ErrIllegalTransition = errors.New("illegal card state transition")

// validStates and validPhases back ValidState and ValidPhase. Keeping them as
// sets (rather than switch statements) means adding a new State or Phase only
// requires touching one place.
var validStates = map[State]bool{
	Backlog:    true,
	Ready:      true,
	InProgress: true,
	Review:     true,
	Done:       true,
	Blocked:    true,
	NeedsHuman: true,
}

var validPhases = map[Phase]bool{
	PhaseSpecification:  true,
	PhasePlanning:       true,
	PhaseTests:          true,
	PhaseImplementation: true,
	PhaseVerification:   true,
	PhaseReview:         true,
	PhaseComplete:       true,
}

// Actor sets used below purely to keep the transition table readable: most
// transitions are open to any actor, a handful are restricted to a human.
var (
	anyActor  = []ActorType{ActorHuman, ActorAgent, ActorSystem}
	humanOnly = []ActorType{ActorHuman}
)

// allowed is the whole state machine. A (from, to) pair absent from this map
// is illegal for every actor. Where a pair is present, only the listed
// actors may perform it.
//
// This table is the workflow documentation:
//
//   Backlog    -> Ready       : any actor  (deterministic specification gate, section 10)
//   Backlog    -> Blocked     : any actor  (policy violation, section 24)
//   Backlog    -> NeedsHuman  : any actor  (escalation exhausted / spec insufficient)
//
//   Ready      -> InProgress  : any actor  (claim, section 6)
//   Ready      -> Blocked     : any actor  (policy violation, section 24)
//   Ready      -> NeedsHuman  : any actor  (escalation exhausted)
//
//   InProgress -> Review      : any actor  (deterministic green gate + PR, section 19)
//   InProgress -> Blocked     : any actor  (policy violation, section 24)
//   InProgress -> NeedsHuman  : any actor  (escalation exhausted / budget)
//
//   Review     -> Done        : human only (section 18: automated review cannot move a card to Done)
//   Review     -> Ready       : human only (human rejection, section 19)
//   Review     -> Blocked     : any actor  (policy violation, section 24)
//   Review     -> NeedsHuman  : any actor  (blocking automated review, section 18)
//
//   Blocked    -> Ready       : human only (an agent must never un-block itself)
//   NeedsHuman -> Ready       : human only (an agent must never un-block itself)
//
//   Done                      : terminal — no outgoing transitions for any actor.
var allowed = map[State]map[State][]ActorType{
	Backlog: {
		Ready:      anyActor,
		Blocked:    anyActor,
		NeedsHuman: anyActor,
	},
	Ready: {
		InProgress: anyActor,
		Blocked:    anyActor,
		NeedsHuman: anyActor,
	},
	InProgress: {
		Review:     anyActor,
		Blocked:    anyActor,
		NeedsHuman: anyActor,
	},
	Review: {
		Done:       humanOnly,
		Ready:      humanOnly,
		Blocked:    anyActor,
		NeedsHuman: anyActor,
	},
	Blocked: {
		Ready: humanOnly,
	},
	NeedsHuman: {
		Ready: humanOnly,
	},
}

// CanTransition reports whether actor may move a card from `from` to `to`.
// It returns nil when the transition is legal, and otherwise an error
// wrapping ErrIllegalTransition that names the from state, the to state and
// the actor. CanTransition never panics, even on an unrecognized State.
func CanTransition(from, to State, actor ActorType) error {
	if !ValidState(from) {
		return fmt.Errorf("%w: unknown from-state %q (actor=%s)", ErrIllegalTransition, from, actor)
	}
	if !ValidState(to) {
		return fmt.Errorf("%w: unknown to-state %q (actor=%s)", ErrIllegalTransition, to, actor)
	}
	if from == to {
		return fmt.Errorf("%w: %s -> %s is a no-op transition (actor=%s)", ErrIllegalTransition, from, to, actor)
	}

	actors, ok := allowed[from][to]
	if !ok {
		return fmt.Errorf("%w: %s -> %s is not a permitted transition (actor=%s)", ErrIllegalTransition, from, to, actor)
	}

	for _, a := range actors {
		if a == actor {
			return nil
		}
	}
	return fmt.Errorf("%w: %s -> %s is not permitted for actor %s", ErrIllegalTransition, from, to, actor)
}

// ValidState reports whether s is one of the known board states.
func ValidState(s State) bool {
	return validStates[s]
}

// ValidPhase reports whether p is one of the known coding-pipeline phases.
func ValidPhase(p Phase) bool {
	return validPhases[p]
}
