package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
)

// seedReadyCard migrates the schema (if not already applied) and inserts one
// card in state Ready, returning its id.
func seedReadyCard(t *testing.T, s *Store) uuid.UUID {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() returned error %v, want nil", err)
	}

	id := uuid.New()

	_, err := s.Pool().Exec(ctx, `
		INSERT INTO cards (id, title, source_type, state, phase, risk_class, effective_priority)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, 100)
	`, id.String(), "seeded card", "manual", string(card.Ready), string(card.PhaseSpecification), "R1")
	if err != nil {
		t.Fatalf("seeding ready card returned error %v, want nil", err)
	}

	return id
}

// TestTenConcurrentWorkersClaimExactlyOnce is the milestone gate for spec
// section 6: ten workers race for a single Ready card, genuinely
// simultaneously, and exactly one of them must win.
func TestTenConcurrentWorkersClaimExactlyOnce(t *testing.T) {
	s := openTestStore(t)
	cardID := seedReadyCard(t, s)

	const workers = 10

	var (
		start  = make(chan struct{})
		wg     sync.WaitGroup
		mu     sync.Mutex
		wins   int
		noWork int
		other  []error
	)

	wg.Add(workers)
	for i := 0; i < workers; i++ {
		workerID := fmt.Sprintf("worker-%d", i)
		go func(workerID string) {
			defer wg.Done()

			<-start

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			c, err := s.ClaimReady(ctx, workerID, time.Minute)

			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				if c == nil {
					other = append(other, fmt.Errorf("worker %s: ClaimReady returned nil card with nil error", workerID))
					return
				}
				wins++
			case errors.Is(err, ErrNoWork):
				noWork++
			default:
				other = append(other, fmt.Errorf("worker %s: %w", workerID, err))
			}
		}(workerID)
	}

	close(start)
	wg.Wait()

	if len(other) > 0 {
		t.Fatalf("spec section 6: exactly one caller must win the claim race; got unexpected errors: %v", other)
	}
	if wins != 1 {
		t.Errorf("spec section 6: exactly one caller must win the claim race; got %d winners, want 1", wins)
	}
	if noWork != workers-1 {
		t.Errorf("spec section 6: exactly one caller must win the claim race; got %d ErrNoWork losers, want %d", noWork, workers-1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var historyCount int
	err := s.Pool().QueryRow(ctx, `
		SELECT count(*)
		FROM card_history
		WHERE card_id = $1::uuid AND from_state = $2 AND to_state = $3
	`, cardID.String(), string(card.Ready), string(card.InProgress)).Scan(&historyCount)
	if err != nil {
		t.Fatalf("counting card_history rows returned error %v, want nil", err)
	}
	if historyCount != 1 {
		t.Errorf("spec section 6: exactly one caller must win the claim race; got %d card_history rows for Ready->InProgress, want 1", historyCount)
	}
}

func TestExpiredLeaseIsReclaimable(t *testing.T) {
	s := openTestStore(t)
	seedReadyCard(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := s.ClaimReady(ctx, "worker-a", -1*time.Hour); err != nil {
		t.Fatalf("first ClaimReady() returned error %v, want nil", err)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	c, err := s.ClaimReady(ctx2, "worker-b", time.Minute)
	if err != nil {
		t.Fatalf("ClaimReady() with an expired lease held returned error %v, want nil", err)
	}
	if c == nil {
		t.Fatal("ClaimReady() with an expired lease held returned nil card, want a card")
	}
	if c.ClaimedBy == nil || *c.ClaimedBy != "worker-b" {
		t.Errorf("got claimed_by %v, want %q", c.ClaimedBy, "worker-b")
	}
}

func TestUnexpiredLeaseIsNotStealable(t *testing.T) {
	s := openTestStore(t)
	seedReadyCard(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := s.ClaimReady(ctx, "worker-a", 10*time.Minute); err != nil {
		t.Fatalf("first ClaimReady() returned error %v, want nil", err)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	_, err := s.ClaimReady(ctx2, "worker-b", time.Minute)
	if !errors.Is(err, ErrNoWork) {
		t.Errorf("second ClaimReady() while lease unexpired: got error %v, want ErrNoWork", err)
	}
}

func TestHeartbeatFromNonClaimantIsRejected(t *testing.T) {
	s := openTestStore(t)
	cardID := seedReadyCard(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := s.ClaimReady(ctx, "worker-a", 10*time.Minute); err != nil {
		t.Fatalf("ClaimReady() returned error %v, want nil", err)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	err := s.Heartbeat(ctx2, cardID, "worker-b", time.Minute)
	if !errors.Is(err, ErrNotClaimant) {
		t.Errorf("Heartbeat() from non-claimant: got error %v, want ErrNotClaimant", err)
	}
}

func TestHeartbeatExtendsTheLease(t *testing.T) {
	s := openTestStore(t)
	cardID := seedReadyCard(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	claimed, err := s.ClaimReady(ctx, "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("ClaimReady() returned error %v, want nil", err)
	}
	if claimed.LeaseExpiresAt == nil {
		t.Fatal("claimed card has nil LeaseExpiresAt, want a value")
	}
	before := *claimed.LeaseExpiresAt

	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	if err := s.Heartbeat(ctx2, cardID, "worker-a", 10*time.Minute); err != nil {
		t.Fatalf("Heartbeat() returned error %v, want nil", err)
	}

	ctx3, cancel3 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel3()

	after, err := s.GetCard(ctx3, cardID)
	if err != nil {
		t.Fatalf("GetCard() returned error %v, want nil", err)
	}
	if after.LeaseExpiresAt == nil {
		t.Fatal("card after Heartbeat() has nil LeaseExpiresAt, want a value")
	}
	if !after.LeaseExpiresAt.After(before) {
		t.Errorf("got lease_expires_at %v after Heartbeat(), want a time after %v", *after.LeaseExpiresAt, before)
	}
}

func TestReleaseReturnsTheCardToReady(t *testing.T) {
	s := openTestStore(t)
	cardID := seedReadyCard(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := s.ClaimReady(ctx, "worker-a", 10*time.Minute); err != nil {
		t.Fatalf("ClaimReady() returned error %v, want nil", err)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	if err := s.Release(ctx2, cardID, "worker-a", "handed back"); err != nil {
		t.Fatalf("Release() returned error %v, want nil", err)
	}

	ctx3, cancel3 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel3()

	released, err := s.GetCard(ctx3, cardID)
	if err != nil {
		t.Fatalf("GetCard() returned error %v, want nil", err)
	}
	if released.State != card.Ready {
		t.Errorf("got state %q after Release(), want %q", released.State, card.Ready)
	}
	if released.ClaimedBy != nil {
		t.Errorf("got claimed_by %q after Release(), want nil", *released.ClaimedBy)
	}

	ctx4, cancel4 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel4()

	reclaimed, err := s.ClaimReady(ctx4, "worker-b", time.Minute)
	if err != nil {
		t.Fatalf("ClaimReady() after Release() returned error %v, want nil", err)
	}
	if reclaimed.ID != cardID {
		t.Errorf("got reclaimed card id %v, want %v", reclaimed.ID, cardID)
	}
}

func TestTransitionRejectsAnIllegalMove(t *testing.T) {
	s := openTestStore(t)
	cardID := seedReadyCard(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := s.ClaimReady(ctx, "worker-a", 10*time.Minute); err != nil {
		t.Fatalf("ClaimReady() returned error %v, want nil", err)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	err := s.Transition(ctx2, cardID, card.Done, card.ActorAgent, "worker-a", "trying to skip review")
	if !errors.Is(err, card.ErrIllegalTransition) {
		t.Errorf("Transition(InProgress -> Done, agent): got error %v, want errors.Is(err, card.ErrIllegalTransition)", err)
	}

	ctx3, cancel3 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel3()

	var historyCount int
	if err := s.Pool().QueryRow(ctx3, `
		SELECT count(*)
		FROM card_history
		WHERE card_id = $1::uuid AND to_state = $2
	`, cardID.String(), string(card.Done)).Scan(&historyCount); err != nil {
		t.Fatalf("counting card_history rows returned error %v, want nil", err)
	}
	if historyCount != 0 {
		t.Errorf("got %d card_history rows recording the rejected transition, want 0 (append-only history must not record illegal moves)", historyCount)
	}
}

func TestTransitionWritesImmutableHistory(t *testing.T) {
	s := openTestStore(t)
	cardID := seedReadyCard(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := s.ClaimReady(ctx, "worker-a", 10*time.Minute); err != nil {
		t.Fatalf("ClaimReady() returned error %v, want nil", err)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	if err := s.Transition(ctx2, cardID, card.Review, card.ActorAgent, "worker-a", "tests green, PR opened"); err != nil {
		t.Fatalf("Transition(InProgress -> Review, agent) returned error %v, want nil", err)
	}

	ctx3, cancel3 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel3()

	rows, err := s.Pool().Query(ctx3, `
		SELECT from_state, to_state, actor_type, actor_id, reason
		FROM card_history
		WHERE card_id = $1::uuid AND to_state = $2
	`, cardID.String(), string(card.Review))
	if err != nil {
		t.Fatalf("querying card_history returned error %v, want nil", err)
	}
	defer rows.Close()

	type historyRow struct {
		from, to, actorType, actorID, reason string
	}
	var got []historyRow

	for rows.Next() {
		var r historyRow
		if err := rows.Scan(&r.from, &r.to, &r.actorType, &r.actorID, &r.reason); err != nil {
			t.Fatalf("scanning card_history row returned error %v, want nil", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating card_history rows returned error %v, want nil", err)
	}

	if len(got) != 1 {
		t.Fatalf("got %d card_history rows for InProgress -> Review, want exactly 1", len(got))
	}

	want := historyRow{
		from:      string(card.InProgress),
		to:        string(card.Review),
		actorType: string(card.ActorAgent),
		actorID:   "worker-a",
		reason:    "tests green, PR opened",
	}
	if got[0] != want {
		t.Errorf("got card_history row %+v, want %+v", got[0], want)
	}
}
