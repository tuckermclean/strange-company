package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/runner"
)

// resultOf builds a minimal *runner.CodingRunResult for one test, since only
// Status, Summary and Usage/CostUSD matter to the classification and
// evidence-recording logic under test here.
func resultOf(status runner.Status) *runner.CodingRunResult {
	return &runner.CodingRunResult{
		Status:  status,
		Harness: "claude-code",
		Model:   "claude-haiku-4-5",
		Summary: "test summary",
	}
}

// recordOf builds an AttemptRecord for cardID with the given run id and
// result, using a fixed identity (alias/provider/harness/model) that is
// irrelevant to the behaviours under test.
func recordOf(cardID uuid.UUID, runID string, result *runner.CodingRunResult) AttemptRecord {
	return AttemptRecord{
		CardID:     cardID,
		RunID:      runID,
		Phase:      "implementation",
		ModelAlias: "haiku",
		Provider:   "anthropic",
		Harness:    "claude-code",
		Model:      "claude-haiku-4-5",
		Result:     result,
	}
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestAFailedVerificationCountsAsAnImplementationAttempt is spec §12.1's
// positive case: the agent did work, the runner regained control,
// verification ran, and it failed. That is the one thing that must burn a
// model-tier attempt.
func TestAFailedVerificationCountsAsAnImplementationAttempt(t *testing.T) {
	s := openTestStore(t)
	cardID := seedReadyCard(t, s)

	outcome, err := s.RecordAttempt(testCtx(t), recordOf(cardID, "run-1", resultOf(runner.StatusFailed)))
	if err != nil {
		t.Fatalf("RecordAttempt(StatusFailed) returned error %v, want nil", err)
	}

	if !outcome.CountedAsAttempt {
		t.Errorf("spec §12.1: a failed verification must count as an implementation attempt; got CountedAsAttempt=false")
	}
	if outcome.AttemptNumber != 1 {
		t.Errorf("got AttemptNumber %d, want 1 (this is the first attempt)", outcome.AttemptNumber)
	}
	if outcome.ImplementationAttempts != 1 {
		t.Errorf("got ImplementationAttempts %d, want 1: a failed attempt must burn a model-tier rung", outcome.ImplementationAttempts)
	}
	if outcome.InfrastructureFailures != 0 {
		t.Errorf("got InfrastructureFailures %d, want 0: a genuine failure must not also count as infra noise", outcome.InfrastructureFailures)
	}
}

// TestAProviderOutageDoesNotBurnAModelRung is spec §12.1's core guarantee:
// infrastructure noise (StatusInfraError) must never look like a model
// failed to solve the problem, or the ladder silently exhausts itself on
// something no model could have fixed.
func TestAProviderOutageDoesNotBurnAModelRung(t *testing.T) {
	s := openTestStore(t)
	cardID := seedReadyCard(t, s)

	outcome, err := s.RecordAttempt(testCtx(t), recordOf(cardID, "run-1", resultOf(runner.StatusInfraError)))
	if err != nil {
		t.Fatalf("RecordAttempt(StatusInfraError) returned error %v, want nil", err)
	}

	if outcome.CountedAsAttempt {
		t.Errorf("spec §12.1: a provider outage must not count as an implementation attempt; got CountedAsAttempt=true")
	}
	if outcome.AttemptNumber != 0 {
		t.Errorf("got AttemptNumber %d, want 0: an infra error assigns no rung on the ladder", outcome.AttemptNumber)
	}
	if outcome.ImplementationAttempts != 0 {
		t.Errorf("got ImplementationAttempts %d, want 0: infra noise must not burn a Haiku/Sonnet/Opus rung", outcome.ImplementationAttempts)
	}
	if outcome.InfrastructureFailures != 1 {
		t.Errorf("got InfrastructureFailures %d, want 1: the outage itself must still be counted", outcome.InfrastructureFailures)
	}
}

// TestATimeoutDoesNotBurnAModelRung: a wall-clock kill is the same class of
// failure as a scheduling or provider outage (spec §12.1) -- the agent never
// got to finish, so there is no failed idea to hold against the model.
func TestATimeoutDoesNotBurnAModelRung(t *testing.T) {
	s := openTestStore(t)
	cardID := seedReadyCard(t, s)

	outcome, err := s.RecordAttempt(testCtx(t), recordOf(cardID, "run-1", resultOf(runner.StatusTimeout)))
	if err != nil {
		t.Fatalf("RecordAttempt(StatusTimeout) returned error %v, want nil", err)
	}

	if outcome.CountedAsAttempt {
		t.Errorf("a killed run must not count as an implementation attempt; got CountedAsAttempt=true")
	}
	if outcome.ImplementationAttempts != 0 {
		t.Errorf("got ImplementationAttempts %d, want 0: a timeout must not burn a model-tier rung", outcome.ImplementationAttempts)
	}
	if outcome.InfrastructureFailures != 1 {
		t.Errorf("got InfrastructureFailures %d, want 1: the kill itself must still be counted as infra noise", outcome.InfrastructureFailures)
	}
}

// TestAPolicyViolationBurnsNeitherCounter: spec §24 sends a policy violation
// straight to Blocked -- it is explicitly not something to retry harder, so
// counting it against either the implementation ladder or the infra counter
// would misrepresent why the card stopped.
func TestAPolicyViolationBurnsNeitherCounter(t *testing.T) {
	s := openTestStore(t)
	cardID := seedReadyCard(t, s)

	outcome, err := s.RecordAttempt(testCtx(t), recordOf(cardID, "run-1", resultOf(runner.StatusPolicyViolation)))
	if err != nil {
		t.Fatalf("RecordAttempt(StatusPolicyViolation) returned error %v, want nil", err)
	}

	if outcome.CountedAsAttempt {
		t.Errorf("spec §24: a policy violation must not count as an implementation attempt; got CountedAsAttempt=true")
	}
	if outcome.ImplementationAttempts != 0 {
		t.Errorf("got ImplementationAttempts %d, want 0: §24 requires blocking, not retrying harder", outcome.ImplementationAttempts)
	}
	if outcome.InfrastructureFailures != 0 {
		t.Errorf("got InfrastructureFailures %d, want 0: a policy violation is not infrastructure noise either", outcome.InfrastructureFailures)
	}
}

// TestACompletedRunDoesNotIncrementFailures: success is recorded for audit
// but must not touch either escalation counter.
func TestACompletedRunDoesNotIncrementFailures(t *testing.T) {
	s := openTestStore(t)
	cardID := seedReadyCard(t, s)

	outcome, err := s.RecordAttempt(testCtx(t), recordOf(cardID, "run-1", resultOf(runner.StatusCompleted)))
	if err != nil {
		t.Fatalf("RecordAttempt(StatusCompleted) returned error %v, want nil", err)
	}

	if outcome.CountedAsAttempt {
		t.Errorf("a completed run must not count as a failed implementation attempt; got CountedAsAttempt=true")
	}
	if outcome.ImplementationAttempts != 0 {
		t.Errorf("got ImplementationAttempts %d, want 0 after a completed run", outcome.ImplementationAttempts)
	}
	if outcome.InfrastructureFailures != 0 {
		t.Errorf("got InfrastructureFailures %d, want 0 after a completed run", outcome.InfrastructureFailures)
	}
}

// TestAttemptNumbersAreSequentialAndSkipNonAttempts: fail, infra, fail must
// number 1, then 2 -- the infra row in between must not consume a number,
// or the sequence would misrepresent which rung of the ladder a later
// attempt actually occupies.
func TestAttemptNumbersAreSequentialAndSkipNonAttempts(t *testing.T) {
	s := openTestStore(t)
	cardID := seedReadyCard(t, s)
	ctx := testCtx(t)

	first, err := s.RecordAttempt(ctx, recordOf(cardID, "run-1", resultOf(runner.StatusFailed)))
	if err != nil {
		t.Fatalf("RecordAttempt(run-1, StatusFailed) returned error %v, want nil", err)
	}
	if first.AttemptNumber != 1 {
		t.Fatalf("got AttemptNumber %d for the first failure, want 1", first.AttemptNumber)
	}

	infra, err := s.RecordAttempt(ctx, recordOf(cardID, "run-2", resultOf(runner.StatusInfraError)))
	if err != nil {
		t.Fatalf("RecordAttempt(run-2, StatusInfraError) returned error %v, want nil", err)
	}
	if infra.AttemptNumber != 0 {
		t.Fatalf("got AttemptNumber %d for the infra error, want 0 (not counted)", infra.AttemptNumber)
	}

	second, err := s.RecordAttempt(ctx, recordOf(cardID, "run-3", resultOf(runner.StatusFailed)))
	if err != nil {
		t.Fatalf("RecordAttempt(run-3, StatusFailed) returned error %v, want nil", err)
	}
	if second.AttemptNumber != 2 {
		t.Fatalf("got AttemptNumber %d for the second failure, want 2: the infra row in between must not consume a number", second.AttemptNumber)
	}

	attempts, err := s.ListAttempts(ctx, cardID)
	if err != nil {
		t.Fatalf("ListAttempts() returned error %v, want nil", err)
	}
	if len(attempts) != 3 {
		t.Fatalf("got %d stored attempts, want 3", len(attempts))
	}
	if attempts[1].AttemptNumber != nil {
		t.Errorf("stored infra row has AttemptNumber %v, want nil (NULL)", *attempts[1].AttemptNumber)
	}
}

// TestCostIsNullWhenTheHarnessDoesNotReportIt: Codex reports tokens only.
// Storing 0 for its cost would make every cost question quietly wrong, so
// a nil Result.CostUSD must read back as nil, not zero.
func TestCostIsNullWhenTheHarnessDoesNotReportIt(t *testing.T) {
	s := openTestStore(t)
	cardID := seedReadyCard(t, s)

	result := resultOf(runner.StatusFailed)
	result.CostUSD = nil

	if _, err := s.RecordAttempt(testCtx(t), recordOf(cardID, "run-1", result)); err != nil {
		t.Fatalf("RecordAttempt() returned error %v, want nil", err)
	}

	attempts, err := s.ListAttempts(testCtx(t), cardID)
	if err != nil {
		t.Fatalf("ListAttempts() returned error %v, want nil", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("got %d stored attempts, want 1", len(attempts))
	}
	if attempts[0].CostUSD != nil {
		t.Errorf("got CostUSD %v, want nil: a harness that does not report cost must not be recorded as free", *attempts[0].CostUSD)
	}
}

// TestCostIsStoredWhenReported: Claude Code reports total_cost_usd directly,
// and that figure must survive the round trip.
func TestCostIsStoredWhenReported(t *testing.T) {
	s := openTestStore(t)
	cardID := seedReadyCard(t, s)

	cost := 1.234567
	result := resultOf(runner.StatusFailed)
	result.CostUSD = &cost

	if _, err := s.RecordAttempt(testCtx(t), recordOf(cardID, "run-1", result)); err != nil {
		t.Fatalf("RecordAttempt() returned error %v, want nil", err)
	}

	attempts, err := s.ListAttempts(testCtx(t), cardID)
	if err != nil {
		t.Fatalf("ListAttempts() returned error %v, want nil", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("got %d stored attempts, want 1", len(attempts))
	}
	if attempts[0].CostUSD == nil {
		t.Fatal("got CostUSD nil, want a reported value")
	}
	if diff := *attempts[0].CostUSD - cost; diff > 1e-6 || diff < -1e-6 {
		t.Errorf("got CostUSD %v, want %v", *attempts[0].CostUSD, cost)
	}
}

// TestTokensAreRecordedForEveryRunIncludingInfraFailures: evidence must
// survive even when the run did not count as an attempt, or an operator
// diagnosing an outage loses the tokens that were burned finding it.
func TestTokensAreRecordedForEveryRunIncludingInfraFailures(t *testing.T) {
	s := openTestStore(t)
	cardID := seedReadyCard(t, s)

	result := resultOf(runner.StatusInfraError)
	result.Usage = runner.Usage{InputTokens: 321, OutputTokens: 45}

	if _, err := s.RecordAttempt(testCtx(t), recordOf(cardID, "run-1", result)); err != nil {
		t.Fatalf("RecordAttempt() returned error %v, want nil", err)
	}

	attempts, err := s.ListAttempts(testCtx(t), cardID)
	if err != nil {
		t.Fatalf("ListAttempts() returned error %v, want nil", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("got %d stored attempts, want 1", len(attempts))
	}
	if attempts[0].InputTokens != 321 || attempts[0].OutputTokens != 45 {
		t.Errorf("got tokens (in=%d, out=%d), want (in=321, out=45): token evidence must survive even for a run that did not count", attempts[0].InputTokens, attempts[0].OutputTokens)
	}
}

// TestListAttemptsReturnsRunsInOrder verifies the read side preserves
// insertion order, which downstream feedback assembly (spec §12.2) depends
// on to find "the previous attempt".
func TestListAttemptsReturnsRunsInOrder(t *testing.T) {
	s := openTestStore(t)
	cardID := seedReadyCard(t, s)
	ctx := testCtx(t)

	runIDs := []string{"run-1", "run-2", "run-3"}
	for _, id := range runIDs {
		if _, err := s.RecordAttempt(ctx, recordOf(cardID, id, resultOf(runner.StatusFailed))); err != nil {
			t.Fatalf("RecordAttempt(%s) returned error %v, want nil", id, err)
		}
	}

	attempts, err := s.ListAttempts(ctx, cardID)
	if err != nil {
		t.Fatalf("ListAttempts() returned error %v, want nil", err)
	}
	if len(attempts) != len(runIDs) {
		t.Fatalf("got %d stored attempts, want %d", len(attempts), len(runIDs))
	}
	for i, id := range runIDs {
		if attempts[i].RunID != id {
			t.Errorf("attempt %d: got RunID %q, want %q (out of order)", i, attempts[i].RunID, id)
		}
	}
}

// TestRecordAttemptOnAMissingCardIsNotFound guards against silently
// recording evidence against a card that does not exist.
func TestRecordAttemptOnAMissingCardIsNotFound(t *testing.T) {
	s := openTestStore(t)
	if err := s.Migrate(testCtx(t)); err != nil {
		t.Fatalf("Migrate() returned error %v, want nil", err)
	}

	missing := uuid.New()
	_, err := s.RecordAttempt(testCtx(t), recordOf(missing, "run-1", resultOf(runner.StatusFailed)))
	if !errors.Is(err, ErrCardNotFound) {
		t.Errorf("RecordAttempt() on a missing card: got error %v, want ErrCardNotFound", err)
	}
}

// TestLadderExhaustedComparesAgainstTheSuppliedLimit verifies this package
// stays ignorant of the ladder's actual shape (spec §12.3) and simply
// compares against whatever limit the caller supplies.
func TestLadderExhaustedComparesAgainstTheSuppliedLimit(t *testing.T) {
	s := openTestStore(t)
	cardID := seedReadyCard(t, s)
	ctx := testCtx(t)

	for i := 0; i < 3; i++ {
		if _, err := s.RecordAttempt(ctx, recordOf(cardID, fmt.Sprintf("run-%d", i), resultOf(runner.StatusFailed))); err != nil {
			t.Fatalf("RecordAttempt() returned error %v, want nil", err)
		}
	}

	exhausted, err := s.LadderExhausted(ctx, cardID, 3)
	if err != nil {
		t.Fatalf("LadderExhausted(limit=3) returned error %v, want nil", err)
	}
	if !exhausted {
		t.Errorf("got LadderExhausted(limit=3)=false after 3 failures, want true")
	}

	notExhausted, err := s.LadderExhausted(ctx, cardID, 4)
	if err != nil {
		t.Fatalf("LadderExhausted(limit=4) returned error %v, want nil", err)
	}
	if notExhausted {
		t.Errorf("got LadderExhausted(limit=4)=true after 3 failures, want false: the limit is the caller's to supply, not this package's to assume")
	}
}

// TestCountersAndRowAreWrittenAtomically is the test that catches a
// non-transactional implementation: many callers race to record failed
// attempts against the same card, and the final counter must equal exactly
// the number of counted runs -- no lost updates, no double counting.
func TestCountersAndRowAreWrittenAtomically(t *testing.T) {
	s := openTestStore(t)
	cardID := seedReadyCard(t, s)

	const workers = 20

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, workers)

	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			<-start

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			_, err := s.RecordAttempt(ctx, recordOf(cardID, fmt.Sprintf("run-%d", i), resultOf(runner.StatusFailed)))
			if err != nil {
				errs <- err
			}
		}(i)
	}

	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent RecordAttempt() returned error %v, want nil", err)
	}

	final, err := s.GetCard(testCtx(t), cardID)
	if err != nil {
		t.Fatalf("GetCard() returned error %v, want nil", err)
	}
	if final.ImplementationAttempt != workers {
		t.Errorf("got implementation_attempt %d after %d concurrent counted attempts, want exactly %d (a non-transactional implementation loses or duplicates updates)", final.ImplementationAttempt, workers, workers)
	}

	attempts, err := s.ListAttempts(testCtx(t), cardID)
	if err != nil {
		t.Fatalf("ListAttempts() returned error %v, want nil", err)
	}
	if len(attempts) != workers {
		t.Fatalf("got %d stored attempt rows, want %d", len(attempts), workers)
	}

	var numbers []int
	for _, a := range attempts {
		if a.AttemptNumber == nil {
			t.Fatalf("counted attempt %s has nil AttemptNumber, want a value", a.RunID)
		}
		numbers = append(numbers, *a.AttemptNumber)
	}
	sort.Ints(numbers)
	for i, n := range numbers {
		if n != i+1 {
			t.Fatalf("attempt numbers are not the exact sequence 1..%d: got %v", workers, numbers)
		}
	}
}
