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

	// parentage maps a child card to the card it was split out of, and
	// prereqs the cards each must wait for.
	parentage map[uuid.UUID]uuid.UUID
	prereqs   map[uuid.UUID][]store.Prerequisite
	unpriced  int

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
func (f *fakeStore) Parentage(context.Context) (map[uuid.UUID]uuid.UUID, error) {
	return f.parentage, nil
}
func (f *fakeStore) Prerequisites(context.Context) (map[uuid.UUID][]store.Prerequisite, error) {
	return f.prereqs, nil
}
func (f *fakeStore) UnpricedAttempts(context.Context, uuid.UUID) (int, error) {
	return f.unpriced, nil
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

// A retry loop left 262 artifacts of one type on a real card. Rendering a row
// each buries everything else on the page.
func TestRepeatedArtifactsAreCollapsed(t *testing.T) {
	c := aCard("looped", card.Review)
	var artifacts []*store.Artifact
	for i := 0; i < 40; i++ {
		artifacts = append(artifacts, &store.Artifact{
			ID: uuid.New(), Type: store.ArtifactTestMapping, Content: "same", SizeBytes: 4,
		})
	}
	newest := uuid.New()
	artifacts = append(artifacts, &store.Artifact{ID: newest, Type: store.ArtifactTestMapping, Content: "newest", SizeBytes: 6})

	h := serve(t, &fakeStore{cards: []*card.Card{c}, artifacts: artifacts})
	body := get(t, h, "/ui/cards/"+c.ID.String()).Body.String()

	if !strings.Contains(body, "41&times;") && !strings.Contains(body, "41×") {
		t.Error("the page does not say how many there are")
	}
	// The newest is the one worth reading, and it is the one linked at the top.
	if !strings.Contains(body, "artifacts/"+newest.String()) {
		t.Error("the newest artifact is not the one offered")
	}
	if strings.Count(body, "40 earlier") != 1 {
		t.Error("the earlier ones are not collapsed behind a single control")
	}
}

// §7.1 gives each phase its own worker. The page rendered every one of them as
// "agent", flattening the most distinctive thing this architecture does.
func TestTheCardShowsTheRelayOfWorkers(t *testing.T) {
	c := aCard("relayed", card.Review)
	at := func(m int) time.Time { return time.Date(2026, 8, 30, 18, m, 0, 0, time.UTC) }

	h := serve(t, &fakeStore{cards: []*card.Card{c}, history: []store.HistoryEntry{
		{At: at(50), To: "Ready", ActorType: "system", ActorID: "control-plane", Reason: "gate passed"},
		{At: at(51), To: "InProgress", ActorType: "agent", ActorID: "meeseeks-cp-abc-3", Reason: "claimed"},
		{At: at(51), To: "Ready", ActorType: "agent", ActorID: "meeseeks-cp-abc-3", Reason: "phase advanced"},
		{At: at(52), To: "InProgress", ActorType: "agent", ActorID: "meeseeks-cp-abc-4", Reason: "claimed"},
		{At: at(54), To: "Review", ActorType: "agent", ActorID: "meeseeks-cp-abc-4", Reason: "review passed"},
	}})

	body := get(t, h, "/ui/cards/"+c.ID.String()).Body.String()
	if !strings.Contains(body, "abc-3") || !strings.Contains(body, "abc-4") {
		t.Errorf("the page does not name the workers:\n%s", body)
	}
	// The control plane is not a Meeseeks and must not appear in the relay.
	if strings.Contains(body, "<code>control-plane</code>") {
		t.Error("the control plane was listed as a worker")
	}
}

// Two consecutive rows from one worker are one life, not two.
func TestOneWorkersConsecutiveStepsAreOneStint(t *testing.T) {
	c := aCard("x", card.Review)
	at := func(m int) time.Time { return time.Date(2026, 8, 30, 18, m, 0, 0, time.UTC) }

	h := serve(t, &fakeStore{cards: []*card.Card{c}, history: []store.HistoryEntry{
		{At: at(51), To: "InProgress", ActorID: "meeseeks-cp-abc-3"},
		{At: at(51), To: "InProgress", ActorID: "meeseeks-cp-abc-3"},
		{At: at(52), To: "Ready", ActorID: "meeseeks-cp-abc-3"},
	}})

	body := get(t, h, "/ui/cards/"+c.ID.String()).Body.String()
	if n := strings.Count(body, "<code>abc-3</code>"); n != 1 {
		t.Errorf("one worker rendered %d times, want 1", n)
	}
}

// §18 stops automated review at Review and §19 gives the human the final call,
// so a card parked there is waiting on a person -- and until this test existed
// the console offered them only "send back" and "stop". The one act the state
// machine reserves for a human was the one act the UI could not perform.
func TestACardInReviewCanBeAccepted(t *testing.T) {
	c := aCard("ready for a person", card.Review)
	f := &fakeStore{cards: []*card.Card{c}}
	h := serve(t, f)

	body := get(t, h, "/ui/cards/"+c.ID.String()).Body.String()
	if !strings.Contains(body, "/accept") {
		t.Error("a card in Review offers no way to accept it")
	}

	if rec := post(t, h, "/ui/cards/"+c.ID.String()+"/accept", ""); rec.Code != http.StatusSeeOther {
		t.Fatalf("accept: status %d", rec.Code)
	}
	if len(f.moves) != 1 || f.moves[0] != card.Done {
		t.Errorf("moves = %v, want Done", f.moves)
	}
}

// The buttons are derived from CanTransition rather than from a list someone
// maintains, so this asserts the derivation over every state at once: any
// human-legal move must be reachable from the page. A fourth human transition
// added to the state machine should fail here rather than go unnoticed for a
// release, which is exactly how accept went missing.
func TestEveryHumanMoveTheStateMachineAllowsHasAControl(t *testing.T) {
	paths := map[card.State]string{
		card.Ready:   "/send-back",
		card.Blocked: "/block",
		card.Done:    "/accept",
	}

	for _, from := range []card.State{
		card.Backlog, card.Ready, card.InProgress,
		card.Review, card.Blocked, card.NeedsHuman, card.Done,
	} {
		c := aCard("t", from)
		h := serve(t, &fakeStore{cards: []*card.Card{c}})
		body := get(t, h, "/ui/cards/"+c.ID.String()).Body.String()

		for to, path := range paths {
			legal := card.CanTransition(from, to, card.ActorHuman) == nil
			if offered := strings.Contains(body, path); legal != offered {
				t.Errorf("%s -> %s: legal=%v but the page offers it=%v", from, to, legal, offered)
			}
		}
	}
}

// The complaint that produced this: a stopped card that does not say what it
// is stopped for makes the reader infer the state machine from whichever
// buttons happen to be lit.
func TestACardStoppedForAPersonSaysWhatItWants(t *testing.T) {
	for _, s := range []card.State{card.Review, card.NeedsHuman, card.Blocked} {
		c := aCard("t", s)
		h := serve(t, &fakeStore{cards: []*card.Card{c}})
		body := get(t, h, "/ui/cards/"+c.ID.String()).Body.String()
		if !strings.Contains(body, "waiting on you") &&
			!strings.Contains(body, "escalated this card to you") &&
			!strings.Contains(body, "stopped and no worker") {
			t.Errorf("%s: the card does not say what it needs from a person", s)
		}
	}
}

// A card the engine is actively working is not asking for anything, and
// saying otherwise would train the reader to ignore the line.
func TestACardTheEngineIsWorkingAsksForNothing(t *testing.T) {
	c := aCard("running", card.InProgress)
	h := serve(t, &fakeStore{cards: []*card.Card{c}})
	body := get(t, h, "/ui/cards/"+c.ID.String()).Body.String()
	if strings.Contains(body, "waiting on you") {
		t.Error("an InProgress card claims to be waiting on a person")
	}
}

// Decomposition records a parent as depending on each of its pieces, and until
// now nothing but §10's gate ever read that edge. A parent and its six
// children rendered as seven unrelated cards, and the reader had no way to
// tell which belonged to which.
func TestAChildCardSaysWhatItIsPartOf(t *testing.T) {
	parent := aCard("Ship the storage library", card.NeedsHuman)
	child := aCard("Command line with filtering", card.Review)
	f := &fakeStore{
		cards:     []*card.Card{parent, child},
		parentage: map[uuid.UUID]uuid.UUID{child.ID: parent.ID},
	}
	h := serve(t, f)

	board := get(t, h, "/ui").Body.String()
	if !strings.Contains(board, "part of") || !strings.Contains(board, parent.Title) {
		t.Error("the board does not place the child in its parent")
	}

	page := get(t, h, "/ui/cards/"+child.ID.String()).Body.String()
	if !strings.Contains(page, parent.Title) {
		t.Error("the child's page does not name its parent")
	}

	pp := get(t, h, "/ui/cards/"+parent.ID.String()).Body.String()
	if !strings.Contains(pp, child.Title) {
		t.Error("the parent's page does not list its pieces")
	}
}

// A parent's progress is the state of its pieces. Without it the parent looks
// stalled while five of its six children are working.
func TestAParentShowsHowManyOfItsPiecesAreDone(t *testing.T) {
	parent := aCard("Ship the storage library", card.NeedsHuman)
	a, b := aCard("one", card.Done), aCard("two", card.InProgress)
	f := &fakeStore{
		cards:     []*card.Card{parent, a, b},
		parentage: map[uuid.UUID]uuid.UUID{a.ID: parent.ID, b.ID: parent.ID},
	}

	board := get(t, serve(t, f), "/ui").Body.String()
	if !strings.Contains(board, "2 pieces, 1 done") {
		t.Error("the board does not say how much of a decomposed card is finished")
	}
}

// "Review" names the machine's state, not the reader's move. Learning that
// Review means someone must decide requires reading the state machine.
func TestTheBoardNamesTheMoveNotOnlyTheState(t *testing.T) {
	for state, want := range map[card.State]string{
		card.Review:     "Accept it, or send it back",
		card.NeedsHuman: "Read the result, then send it back",
		card.Blocked:    "Send it back to resume",
	} {
		c := aCard("t", state)
		board := get(t, serve(t, &fakeStore{cards: []*card.Card{c}}), "/ui").Body.String()
		if !strings.Contains(board, want) {
			t.Errorf("%s: the board does not tell the reader to %q", state, want)
		}
	}
}

// A parent whose children were deleted must not render a blank link that
// reads as a card you can click through to.
func TestAMissingParentIsSaidRatherThanShownBlank(t *testing.T) {
	child := aCard("orphan", card.Review)
	gone := uuid.New()
	f := &fakeStore{
		cards:     []*card.Card{child},
		parentage: map[uuid.UUID]uuid.UUID{child.ID: gone},
	}

	board := get(t, serve(t, f), "/ui").Body.String()
	if !strings.Contains(board, "no longer exists") {
		t.Error("a child of a vanished parent renders an empty link")
	}
}

// §10's gate has always refused to promote a card whose prerequisites are
// unfinished, so the sequencing worked -- and nothing showed it. A piece of a
// split sat looking idle when it was correctly waiting its turn, and a person
// could approve it and watch nothing happen.
func TestACardWaitingItsTurnSaysWhatItIsWaitingFor(t *testing.T) {
	first := aCard("Storage library", card.InProgress)
	second := aCard("Command line with filtering", card.Backlog)
	f := &fakeStore{
		cards: []*card.Card{first, second},
		prereqs: map[uuid.UUID][]store.Prerequisite{
			second.ID: {{ID: first.ID, Title: first.Title, State: string(card.InProgress)}},
		},
	}
	h := serve(t, f)

	board := get(t, h, "/ui").Body.String()
	if !strings.Contains(board, "Waiting its turn") || !strings.Contains(board, "Storage library") {
		t.Error("the board does not show what a queued card is queued behind")
	}

	page := get(t, h, "/ui/cards/"+second.ID.String()).Body.String()
	if !strings.Contains(page, "cannot start until") {
		t.Error("the card does not say it is blocked")
	}
}

// A button the gate will silently decline is worse than no button: it invites
// a click, does nothing, and teaches the reader the console is lying.
func TestABlockedCardDoesNotOfferActionsTheGateWillRefuse(t *testing.T) {
	first := aCard("Storage library", card.InProgress)
	second := aCard("Command line with filtering", card.Backlog)
	f := &fakeStore{
		cards: []*card.Card{first, second},
		spec:  &store.CardSpec{Content: "## Acceptance criteria\n\n- AC1: it works (verified by: go test)\n"},
		prereqs: map[uuid.UUID][]store.Prerequisite{
			second.ID: {{ID: first.ID, Title: first.Title, State: string(card.InProgress)}},
		},
	}

	page := get(t, serve(t, f), "/ui/cards/"+second.ID.String()).Body.String()
	if strings.Contains(page, "/approve") || strings.Contains(page, "/send-back") {
		t.Error("a card the gate is holding offers moves that will not take effect")
	}
}

// A prerequisite that IS done must stop blocking, or the card never unblocks
// on the page even though the gate has released it.
func TestAMetPrerequisiteDoesNotBlock(t *testing.T) {
	first := aCard("Storage library", card.Done)
	second := aCard("Command line with filtering", card.Backlog)
	f := &fakeStore{
		cards: []*card.Card{first, second},
		prereqs: map[uuid.UUID][]store.Prerequisite{
			second.ID: {{ID: first.ID, Title: first.Title, State: string(card.Done)}},
		},
	}

	page := get(t, serve(t, f), "/ui/cards/"+second.ID.String()).Body.String()
	if strings.Contains(page, "cannot start until") {
		t.Error("a card whose prerequisite is Done still reads as blocked")
	}
}

// "$0.00 of $5.00" beside a card that has been running all night reads as a
// cheap card rather than as a blind meter.
func TestABudgetThatCannotBeEnforcedSaysSo(t *testing.T) {
	budget := 5.0
	c := aCard("expensive", card.InProgress)
	c.MaxCostUSD = &budget
	f := &fakeStore{cards: []*card.Card{c}, unpriced: 4}

	page := get(t, serve(t, f), "/ui/cards/"+c.ID.String()).Body.String()
	if !strings.Contains(page, "not being enforced") {
		t.Error("a card with an unenforceable budget presents it as a real limit")
	}
}

func TestAFullyPricedBudgetIsNotFlagged(t *testing.T) {
	budget := 5.0
	c := aCard("fine", card.InProgress)
	c.MaxCostUSD = &budget
	f := &fakeStore{cards: []*card.Card{c}, unpriced: 0}

	page := get(t, serve(t, f), "/ui/cards/"+c.ID.String()).Body.String()
	if strings.Contains(page, "not being enforced") {
		t.Error("a fully priced card claims its budget is not enforced")
	}
}
