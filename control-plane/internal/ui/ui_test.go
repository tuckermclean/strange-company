package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/runner"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
)

type fakeStore struct {
	cards     []*card.Card
	spec      *store.CardSpec
	attempts  []store.StoredAttempt
	artifacts []*store.Artifact
	history   []store.HistoryEntry
	evidence  []store.CardEvidence
	session   string

	moves     []card.State
	moveErr   error
	approvals []string
}

func (f *fakeStore) ListCards(context.Context) ([]*card.Card, error) { return f.cards, nil }
func (f *fakeStore) GetCard(_ context.Context, id uuid.UUID) (*card.Card, error) {
	for _, c := range f.cards {
		if c.ID == id {
			return c, nil
		}
	}
	return nil, context.Canceled
}
func (f *fakeStore) GetSpec(context.Context, uuid.UUID) (*store.CardSpec, error) { return f.spec, nil }
func (f *fakeStore) ListArtifacts(context.Context, uuid.UUID) ([]*store.Artifact, error) {
	return f.artifacts, nil
}
func (f *fakeStore) ListAttempts(context.Context, uuid.UUID) ([]store.StoredAttempt, error) {
	return f.attempts, nil
}
func (f *fakeStore) ListHistory(context.Context, uuid.UUID, int) ([]store.HistoryEntry, error) {
	return f.history, nil
}
func (f *fakeStore) ListEvidence(context.Context, uuid.UUID) ([]store.CardEvidence, error) {
	return f.evidence, nil
}
func (f *fakeStore) GetSpecSession(context.Context, uuid.UUID) (string, error) {
	return f.session, nil
}
func (f *fakeStore) Transition(_ context.Context, _ uuid.UUID, to card.State, _ card.ActorType, _, _ string) error {
	if f.moveErr != nil {
		return f.moveErr
	}
	f.moves = append(f.moves, to)
	return nil
}
func (f *fakeStore) ApproveSpec(_ context.Context, _ uuid.UUID, by string) error {
	f.approvals = append(f.approvals, by)
	return nil
}

func serve(t *testing.T, s Store) http.Handler {
	t.Helper()
	h, err := New(s, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mux := http.NewServeMux()
	h.Routes(mux)
	return mux
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func aCard(title string, state card.State) *card.Card {
	return &card.Card{
		ID: uuid.New(), Title: title, State: state,
		Phase: card.PhaseImplementation, UpdatedAt: time.Now(),
	}
}

// The home screen's whole job is to say where the engine is without anyone
// having to read a database.
func TestTheEngineViewSeparatesRunningFromWhatNeedsAPerson(t *testing.T) {
	working := aCard("in flight", card.InProgress)
	claimed := "meeseeks-control-plane-6f5-16"
	working.ClaimedBy = &claimed

	h := serve(t, &fakeStore{cards: []*card.Card{
		working,
		aCard("escalated", card.NeedsHuman),
		aCard("waiting to merge", card.Review),
	}})

	body := get(t, h, "/ui").Body.String()
	for _, want := range []string{"Running now", "Needs you", "in flight", "escalated", "waiting to merge", "6f5-16"} {
		if !strings.Contains(body, want) {
			t.Errorf("the engine view is missing %q", want)
		}
	}
}

// An idle engine is a state, not a fault, and a blank screen reads as broken.
func TestAnIdleEngineSaysSo(t *testing.T) {
	h := serve(t, &fakeStore{})
	if body := get(t, h, "/ui").Body.String(); !strings.Contains(body, "idle") {
		t.Error("an empty engine renders nothing that explains itself")
	}
}

// The runs are the centrepiece: a run that failed and a run that could not
// start are what tell a sceptic this system knows what it does not know.
func TestACardShowsItsRunsIncludingTheOnesThatFailed(t *testing.T) {
	c := aCard("the work", card.Review)
	cost := 0.31
	h := serve(t, &fakeStore{
		cards: []*card.Card{c},
		attempts: []store.StoredAttempt{
			{Phase: "tests", Status: runner.StatusCompleted, Harness: "opencode",
				Summary: "acceptance tests written and red", CostUSD: &cost, InputTokens: 7699, OutputTokens: 164},
			{Phase: "implementation", Status: runner.StatusFailed, CountedAsAttempt: true,
				Summary: "the acceptance tests still fail"},
			{Phase: "review", Status: runner.StatusInfraError,
				Summary: "review did not complete: context deadline exceeded"},
		},
	})

	body := get(t, h, "/ui/cards/"+c.ID.String()).Body.String()
	for _, want := range []string{
		"acceptance tests written and red",
		"the acceptance tests still fail",
		"review did not complete",
		"did not count against the ladder", // §12.1, stated where a reader sees it
		"$0.31",
		"7.7k in / 164 out",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the card page is missing %q", want)
		}
	}
}

// A cost of zero is unknown, not free, everywhere it appears.
func TestAnUnpricedCardNeverShowsAPrice(t *testing.T) {
	c := aCard("unpriced", card.Review)
	h := serve(t, &fakeStore{cards: []*card.Card{c}})

	body := get(t, h, "/ui/cards/"+c.ID.String()).Body.String()
	if !strings.Contains(body, "unpriced") {
		t.Error("an unpriced card does not say so")
	}
	if strings.Contains(body, "$0.00") {
		t.Error("an unpriced card shows $0.00, which reads as free")
	}
}

// A button that cannot work is worse than a missing one: it invites a click
// that produces an error the person cannot act on.
func TestOnlyLegalActionsAreOffered(t *testing.T) {
	done := aCard("finished", card.Done)
	h := serve(t, &fakeStore{cards: []*card.Card{done}})

	body := get(t, h, "/ui/cards/"+done.ID.String()).Body.String()
	if strings.Contains(body, "/block") {
		t.Error("a Done card offers a Stop button the state machine would refuse")
	}
}

func post(t *testing.T, h http.Handler, path, origin string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// The three gestures the Vikunja board owns today.
func TestTheConsoleCanStopACardAndSendItBack(t *testing.T) {
	c := aCard("running away", card.InProgress)
	f := &fakeStore{cards: []*card.Card{c}}
	h := serve(t, f)

	if rec := post(t, h, "/ui/cards/"+c.ID.String()+"/block", ""); rec.Code != http.StatusSeeOther {
		t.Fatalf("block: status %d", rec.Code)
	}
	if rec := post(t, h, "/ui/cards/"+c.ID.String()+"/send-back", ""); rec.Code != http.StatusSeeOther {
		t.Fatalf("send-back: status %d", rec.Code)
	}
	if len(f.moves) != 2 || f.moves[0] != card.Blocked || f.moves[1] != card.Ready {
		t.Errorf("moves = %v, want Blocked then Ready", f.moves)
	}
}

// Authentication lives at the ingress, so a browser that has been through it
// carries its credential on every request -- including one another site caused.
func TestACrossSiteFormPostIsRefused(t *testing.T) {
	c := aCard("x", card.InProgress)
	f := &fakeStore{cards: []*card.Card{c}}
	h := serve(t, f)

	rec := post(t, h, "/ui/cards/"+c.ID.String()+"/block", "https://evil.example")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if len(f.moves) != 0 {
		t.Error("a cross-site post moved a card")
	}
}

// §21: the audit must never show a person approving something no person read,
// and this process never learns who the ingress let through.
func TestAnApprovalIsAttributedToTheConsoleNotToANamedPerson(t *testing.T) {
	c := aCard("needs a signature", card.Backlog)
	f := &fakeStore{cards: []*card.Card{c}}
	h := serve(t, f)

	if rec := post(t, h, "/ui/cards/"+c.ID.String()+"/approve", ""); rec.Code != http.StatusSeeOther {
		t.Fatalf("approve: status %d", rec.Code)
	}
	if len(f.approvals) != 1 || !strings.Contains(f.approvals[0], "human") {
		t.Errorf("approver = %v, want it to name a human at the console", f.approvals)
	}
}

// §4.3 refusing a move is the system working, and must not read like a fault.
func TestARefusedMoveIsShownOnTheCardRatherThanAsAServerError(t *testing.T) {
	c := aCard("x", card.InProgress)
	f := &fakeStore{cards: []*card.Card{c}, moveErr: card.ErrIllegalTransition}
	h := serve(t, f)

	rec := post(t, h, "/ui/cards/"+c.ID.String()+"/block", "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want a redirect back to the card", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Location"), "error=") {
		t.Errorf("location = %q, want the refusal carried back", rec.Header().Get("Location"))
	}
}

// The click. Everything else on a card summarises; this is the thing itself.
func TestAnArtifactCanBeRead(t *testing.T) {
	c := aCard("the work", card.Review)
	id := uuid.New()
	f := &fakeStore{cards: []*card.Card{c}, artifacts: []*store.Artifact{{
		ID: id, Type: store.ArtifactImplementationPlan, ContentType: "text/markdown",
		Content: "1. add a cube helper\n2. test it", SizeBytes: 31,
	}}}
	h := serve(t, f)

	// The card links to it rather than merely naming it.
	if body := get(t, h, "/ui/cards/"+c.ID.String()).Body.String(); !strings.Contains(body, "/artifacts/"+id.String()) {
		t.Error("the card lists an artifact with no way to open it")
	}

	body := get(t, h, "/ui/cards/"+c.ID.String()+"/artifacts/"+id.String()).Body.String()
	if !strings.Contains(body, "add a cube helper") {
		t.Errorf("the artifact page does not show its contents:\n%s", body)
	}
}

// Useful for piping a transcript elsewhere, and for anyone who would rather
// read it in their own tools.
func TestAnArtifactCanBeFetchedRaw(t *testing.T) {
	c := aCard("x", card.Review)
	id := uuid.New()
	h := serve(t, &fakeStore{cards: []*card.Card{c}, artifacts: []*store.Artifact{{
		ID: id, Type: store.ArtifactRunLog, Content: "assistant: wrote it",
	}}})

	rec := get(t, h, "/ui/cards/"+c.ID.String()+"/artifacts/"+id.String()+"?raw=1")
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("content type = %q, want text/plain", got)
	}
	if rec.Body.String() != "assistant: wrote it" {
		t.Errorf("body = %q, want the artifact verbatim", rec.Body.String())
	}
}

// A harness transcript is the agent's own account, verified by nothing. Showing
// it next to gate results without saying so invites a reader to trust the two
// equally.
func TestAHarnessTranscriptIsLabelledAsUnverified(t *testing.T) {
	c := aCard("x", card.Review)
	id := uuid.New()
	h := serve(t, &fakeStore{cards: []*card.Card{c}, artifacts: []*store.Artifact{{
		ID: id, Type: store.ArtifactRunLog, Content: "assistant: I think this is right",
	}}})

	body := get(t, h, "/ui/cards/"+c.ID.String()+"/artifacts/"+id.String()).Body.String()
	if !strings.Contains(body, "verified by nothing") {
		t.Error("a transcript is shown with nothing marking it as the model's own account")
	}
}

// An artifact belonging to a different card must not be readable through this
// card's URL: the card id in the path is authorisation, not decoration.
func TestAnArtifactFromAnotherCardIsNotServed(t *testing.T) {
	mine := aCard("mine", card.Review)
	h := serve(t, &fakeStore{cards: []*card.Card{mine}})

	rec := get(t, h, "/ui/cards/"+mine.ID.String()+"/artifacts/"+uuid.New().String())
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// §12.2's evidence is the narrative the history's one-line reasons compress.
func TestEachStepsAccountIsShown(t *testing.T) {
	c := aCard("x", card.Review)
	h := serve(t, &fakeStore{cards: []*card.Card{c}, evidence: []store.CardEvidence{
		{ActorID: "meeseeks-1", Summary: "acceptance tests written and red"},
	}})

	if body := get(t, h, "/ui/cards/"+c.ID.String()).Body.String(); !strings.Contains(body, "acceptance tests written and red") {
		t.Error("the card shows no account of what each step did")
	}
}

// The session id has been stored since M4 and surfaced nowhere, so finding a
// conversation meant hunting the dashboard's list by title.
func TestACardLinksToItsSpecificationConversation(t *testing.T) {
	c := aCard("needs discussion", card.Backlog)
	h, err := New(&fakeStore{cards: []*card.Card{c}, session: "api_1724_abc"}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h = h.WithDashboard("https://hermes.example.com/")
	mux := http.NewServeMux()
	h.Routes(mux)

	body := get(t, mux, "/ui/cards/"+c.ID.String()).Body.String()
	if !strings.Contains(body, "https://hermes.example.com/sessions/api_1724_abc") {
		t.Errorf("the card does not link to its conversation:\n%s", body)
	}
}

// Without a public dashboard URL the console must still say a conversation
// exists, rather than silently hiding it.
func TestAConversationIsReportedEvenWithoutADashboardURL(t *testing.T) {
	c := aCard("needs discussion", card.Backlog)
	h := serve(t, &fakeStore{cards: []*card.Card{c}, session: "api_1724_abc"})

	body := get(t, h, "/ui/cards/"+c.ID.String()).Body.String()
	if !strings.Contains(body, "api_1724_abc") {
		t.Error("a card with an open conversation says nothing about it")
	}
}
