package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/policy"
)

// --- hand-written fakes -----------------------------------------------

// releaseCall and transitionCall record the arguments a fake CardStore was
// invoked with, so tests can make assertions about them after the fact.
type releaseCall struct {
	cardID   uuid.UUID
	workerID string
	reason   string
}

type transitionCall struct {
	cardID   uuid.UUID
	to       card.State
	actor    card.ActorType
	actorID  string
	reason   string
}

// fakeCardStore is a hand-written CardStore double. All state is guarded by
// mu because RunOnce's heartbeat goroutine and its main goroutine can call
// it concurrently, and tests run under -race.
type fakeCardStore struct {
	mu sync.Mutex

	claimReadyFunc func() (*card.Card, error)
	heartbeatFunc  func(cardID uuid.UUID) error
	releaseFunc    func(cardID uuid.UUID, workerID, reason string) error
	transitionFunc func(cardID uuid.UUID, to card.State, actor card.ActorType, actorID, reason string) error
	evidenceFunc   func(cardID uuid.UUID, ev Evidence) error

	claimReadyCalls int
	heartbeatCalls  int
	releaseCalls    []releaseCall
	transitionCalls []transitionCall
	evidenceCalls   []Evidence
	advancedTo      card.Phase
	callOrder       []string
}

func (f *fakeCardStore) ClaimReady(ctx context.Context, workerID string, lease time.Duration) (*card.Card, error) {
	f.mu.Lock()
	f.claimReadyCalls++
	f.callOrder = append(f.callOrder, "claim")
	fn := f.claimReadyFunc
	f.mu.Unlock()
	if fn == nil {
		return nil, ErrNoWork
	}
	return fn()
}

func (f *fakeCardStore) Heartbeat(ctx context.Context, cardID uuid.UUID, workerID string, lease time.Duration) error {
	f.mu.Lock()
	f.heartbeatCalls++
	f.callOrder = append(f.callOrder, "heartbeat")
	fn := f.heartbeatFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(cardID)
	}
	return nil
}

func (f *fakeCardStore) Release(ctx context.Context, cardID uuid.UUID, workerID, reason string) error {
	f.mu.Lock()
	f.releaseCalls = append(f.releaseCalls, releaseCall{cardID, workerID, reason})
	f.callOrder = append(f.callOrder, "release")
	fn := f.releaseFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(cardID, workerID, reason)
	}
	return nil
}

func (f *fakeCardStore) Transition(ctx context.Context, cardID uuid.UUID, to card.State, actor card.ActorType, actorID, reason string) error {
	f.mu.Lock()
	f.transitionCalls = append(f.transitionCalls, transitionCall{cardID, to, actor, actorID, reason})
	f.callOrder = append(f.callOrder, "transition")
	fn := f.transitionFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(cardID, to, actor, actorID, reason)
	}
	return nil
}

// AdvancePhase satisfies CardStore for the tests written before a step could
// finish a phase. phaseStore in phases_test.go exercises it properly.
func (f *fakeCardStore) AdvancePhase(_ context.Context, _ uuid.UUID, to card.Phase, _ card.ActorType, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callOrder = append(f.callOrder, "advance")
	f.advancedTo = to
	return nil
}

func (f *fakeCardStore) AttachEvidence(ctx context.Context, cardID uuid.UUID, ev Evidence) error {
	f.mu.Lock()
	f.evidenceCalls = append(f.evidenceCalls, ev)
	f.callOrder = append(f.callOrder, "evidence")
	fn := f.evidenceFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(cardID, ev)
	}
	return nil
}

func (f *fakeCardStore) ClaimReadyCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.claimReadyCalls
}

func (f *fakeCardStore) HeartbeatCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.heartbeatCalls
}

func (f *fakeCardStore) ReleaseCallsLen() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.releaseCalls)
}

func (f *fakeCardStore) TransitionCalls() []transitionCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]transitionCall, len(f.transitionCalls))
	copy(out, f.transitionCalls)
	return out
}

func (f *fakeCardStore) CallOrder() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.callOrder))
	copy(out, f.callOrder)
	return out
}

// fakeStep is a hand-written Step double: a struct with a function field,
// no mocking library.
type fakeStep struct {
	doFunc func(ctx context.Context, c *card.Card, res *policy.Resolution) (Evidence, error)
}

func (f *fakeStep) Do(ctx context.Context, c *card.Card, res *policy.Resolution) (Evidence, error) {
	return f.doFunc(ctx, c, res)
}

// --- test helpers -------------------------------------------------------

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testCard() *card.Card {
	return &card.Card{
		ID:        uuid.New(),
		State:     card.InProgress,
		Phase:     card.PhaseImplementation,
		RiskClass: "low",
	}
}

// testPolicy returns a minimal, valid policy.Policy whose "implementation"
// phase is a single-rung ladder with the given attempt budget. It is a real
// *policy.Policy -- Resolve is a pure function of this data, so there is no
// need to mock the policy package.
func testPolicy(maxAttempts int) *policy.Policy {
	return &policy.Policy{
		Providers: map[string]policy.Provider{
			"prov": {Harness: "test-harness"},
		},
		Aliases: map[string]policy.Alias{
			"impl-cheap": {Provider: "prov", Model: "test-model"},
		},
		Phases: map[string][]policy.Rung{
			string(card.PhaseImplementation): {
				{Alias: "impl-cheap", MaxAttempts: maxAttempts},
			},
		},
	}
}

// --- tests ----------------------------------------------------------------

// Spec 7: claim one thing, make it stop being your problem, cease to exist.
func TestAWorkerHandlesExactlyOneCardThenExits(t *testing.T) {
	c := testCard()
	cards := &fakeCardStore{
		claimReadyFunc: func() (*card.Card, error) { return c, nil },
	}
	step := &fakeStep{doFunc: func(ctx context.Context, c *card.Card, res *policy.Resolution) (Evidence, error) {
		return Evidence{Summary: "did the work", NextState: card.Review}, nil
	}}
	m := New("meeseeks-1", cards, testPolicy(3), step, testLogger(), 30*time.Millisecond)

	outcome, err := m.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce returned an unexpected error: %v", err)
	}
	if outcome != OutcomeCompleted {
		t.Fatalf("expected OutcomeCompleted, got %q", outcome)
	}
	if got := cards.ClaimReadyCalls(); got != 1 {
		t.Fatalf("spec 7: a Meeseeks must claim exactly one card, but ClaimReady was called %d times", got)
	}
}

func TestNoClaimableWorkExitsCleanlyRatherThanSpinning(t *testing.T) {
	cards := &fakeCardStore{
		claimReadyFunc: func() (*card.Card, error) { return nil, ErrNoWork },
	}
	step := &fakeStep{doFunc: func(ctx context.Context, c *card.Card, res *policy.Resolution) (Evidence, error) {
		t.Fatal("spec 7: a Meeseeks must not perform a step when there is no claimable card")
		return Evidence{}, nil
	}}
	m := New("meeseeks-1", cards, testPolicy(3), step, testLogger(), 30*time.Millisecond)

	outcome, err := m.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("spec 7: no claimable work is a normal, quiet exit, not an error: %v", err)
	}
	if outcome != OutcomeNoWork {
		t.Fatalf("expected OutcomeNoWork, got %q", outcome)
	}
	if n := len(cards.TransitionCalls()); n != 0 {
		t.Fatalf("spec 7: no card was claimed, so no transition should have happened (got %d)", n)
	}
	if n := cards.ReleaseCallsLen(); n != 0 {
		t.Fatalf("spec 7: no card was claimed, so no release should have happened (got %d)", n)
	}
}

func TestTheLeaseIsReleasedEvenWhenTheStepFails(t *testing.T) {
	c := testCard()
	cards := &fakeCardStore{claimReadyFunc: func() (*card.Card, error) { return c, nil }}
	stepErr := errors.New("boom")
	step := &fakeStep{doFunc: func(ctx context.Context, c *card.Card, res *policy.Resolution) (Evidence, error) {
		return Evidence{}, stepErr
	}}
	m := New("meeseeks-1", cards, testPolicy(3), step, testLogger(), 30*time.Millisecond)

	outcome, err := m.RunOnce(context.Background())
	if !errors.Is(err, stepErr) {
		t.Fatalf("expected the step's error to surface from RunOnce, got %v", err)
	}
	if outcome != OutcomeReleased {
		t.Fatalf("expected OutcomeReleased, got %q", outcome)
	}
	if n := cards.ReleaseCallsLen(); n != 1 {
		t.Fatalf("spec 7: the lease must always be released, even when the step fails (Release called %d times)", n)
	}
}

func TestAnExhaustedLadderSendsTheCardToNeedsHuman(t *testing.T) {
	c := testCard()
	c.ImplementationAttempt = 3 // the ladder below exhausts after 3 attempts
	cards := &fakeCardStore{claimReadyFunc: func() (*card.Card, error) { return c, nil }}
	step := &fakeStep{doFunc: func(ctx context.Context, c *card.Card, res *policy.Resolution) (Evidence, error) {
		t.Fatal("spec 12.3: no eighth (or any further) attempt runs automatically once the ladder is exhausted")
		return Evidence{}, nil
	}}
	m := New("meeseeks-1", cards, testPolicy(3), step, testLogger(), 30*time.Millisecond)

	outcome, err := m.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("escalation is a normal outcome, not an error: %v", err)
	}
	if outcome != OutcomeEscalated {
		t.Fatalf("expected OutcomeEscalated, got %q", outcome)
	}

	transitions := cards.TransitionCalls()
	if len(transitions) != 1 {
		t.Fatalf("expected exactly one transition, got %d", len(transitions))
	}
	tc := transitions[0]
	if tc.to != card.NeedsHuman {
		t.Fatalf("spec 12.3: an exhausted ladder must send the card to NeedsHuman, got %q", tc.to)
	}
	if !strings.Contains(tc.reason, "implementation") {
		t.Fatalf("spec 12.3: the escalation reason must name the exhausted ladder, got %q", tc.reason)
	}
}

func TestEvidenceIsAttachedBeforeTheTransition(t *testing.T) {
	c := testCard()
	cards := &fakeCardStore{claimReadyFunc: func() (*card.Card, error) { return c, nil }}
	step := &fakeStep{doFunc: func(ctx context.Context, c *card.Card, res *policy.Resolution) (Evidence, error) {
		return Evidence{Summary: "done", NextState: card.Review}, nil
	}}
	m := New("meeseeks-1", cards, testPolicy(3), step, testLogger(), 30*time.Millisecond)

	if _, err := m.RunOnce(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	order := cards.CallOrder()
	evIdx, trIdx := -1, -1
	for i, name := range order {
		if name == "evidence" && evIdx == -1 {
			evIdx = i
		}
		if name == "transition" && trIdx == -1 {
			trIdx = i
		}
	}
	if evIdx == -1 || trIdx == -1 {
		t.Fatalf("expected both an evidence call and a transition call, got order %v", order)
	}
	if evIdx > trIdx {
		t.Fatalf("spec 21: evidence must be attached before the transition -- a card must never arrive in a new state with no evidence explaining why (call order: %v)", order)
	}
}

func TestHeartbeatKeepsTheLeaseAliveDuringALongStep(t *testing.T) {
	c := testCard()
	cards := &fakeCardStore{claimReadyFunc: func() (*card.Card, error) { return c, nil }}
	step := &fakeStep{doFunc: func(ctx context.Context, c *card.Card, res *policy.Resolution) (Evidence, error) {
		time.Sleep(80 * time.Millisecond)
		return Evidence{Summary: "done", NextState: card.Review}, nil
	}}
	lease := 30 * time.Millisecond // heartbeat interval ~= 10ms
	m := New("meeseeks-1", cards, testPolicy(3), step, testLogger(), lease)

	if _, err := m.RunOnce(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	afterRunOnce := cards.HeartbeatCalls()
	if afterRunOnce < 1 {
		t.Fatalf("spec 6/7: the lease must be kept alive with heartbeats during a long step, got %d heartbeats", afterRunOnce)
	}

	// The heartbeat goroutine must not outlive RunOnce: give it time to
	// misbehave, then confirm the count did not move.
	time.Sleep(50 * time.Millisecond)
	if n := cards.HeartbeatCalls(); n != afterRunOnce {
		t.Fatalf("spec 7: a Meeseeks must not outlive its step -- heartbeat count changed from %d to %d after RunOnce returned", afterRunOnce, n)
	}
}

func TestAnIllegalTransitionFromAStepIsRejected(t *testing.T) {
	c := testCard()
	c.State = card.InProgress
	cards := &fakeCardStore{claimReadyFunc: func() (*card.Card, error) { return c, nil }}
	// InProgress -> Done is not a permitted transition (card/state.go): only
	// Review -> Done, and only for a human actor, is. A step claiming the
	// card is done outright must be rejected, not accepted.
	step := &fakeStep{doFunc: func(ctx context.Context, c *card.Card, res *policy.Resolution) (Evidence, error) {
		return Evidence{Summary: "done", NextState: card.Done}, nil
	}}
	cards.transitionFunc = func(cardID uuid.UUID, to card.State, actor card.ActorType, actorID, reason string) error {
		return card.CanTransition(card.InProgress, to, actor)
	}
	m := New("meeseeks-1", cards, testPolicy(3), step, testLogger(), 30*time.Millisecond)

	outcome, err := m.RunOnce(context.Background())
	if err == nil {
		t.Fatal("expected the illegal transition's error to surface from RunOnce")
	}
	if !errors.Is(err, card.ErrIllegalTransition) {
		t.Fatalf("expected the error to wrap card.ErrIllegalTransition, got %v", err)
	}
	if outcome != OutcomeReleased {
		t.Fatalf("expected OutcomeReleased, got %q", outcome)
	}
	if n := cards.ReleaseCallsLen(); n != 1 {
		t.Fatalf("spec 7: a rejected transition must not corrupt the card -- it must be released instead (got %d releases)", n)
	}
}
