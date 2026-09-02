package specsession_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/tuckermclean/strange-company/control-plane/internal/ambiguity"
	"github.com/tuckermclean/strange-company/control-plane/internal/hermes"
	"github.com/tuckermclean/strange-company/control-plane/internal/specsession"
)

type fakeGateway struct {
	sessions []*hermes.Session
	listErr  error

	created  []hermes.SpecSession
	deleted  []string
	nextID   string
	createErr error

	// messages is how many turns each session already holds, and opened
	// records the first turn posted into one.
	messages map[string]int
	opened   map[string]string
	countErr error
	turnErr  error
}

func (f *fakeGateway) MessageCount(_ context.Context, id string) (int, error) {
	if f.countErr != nil {
		return 0, f.countErr
	}
	return f.messages[id], nil
}

func (f *fakeGateway) OpenTurn(_ context.Context, id, message string) error {
	if f.turnErr != nil {
		return f.turnErr
	}
	if f.opened == nil {
		f.opened = map[string]string{}
	}
	f.opened[id] = message
	if f.messages == nil {
		f.messages = map[string]int{}
	}
	f.messages[id] += 2
	return nil
}

func (f *fakeGateway) ListSessions(context.Context) ([]*hermes.Session, error) {
	return f.sessions, f.listErr
}

func (f *fakeGateway) CreateSession(_ context.Context, req hermes.SpecSession) (*hermes.Session, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.created = append(f.created, req)
	return &hermes.Session{ID: f.nextID, Title: req.Title, Model: req.Model}, nil
}

func (f *fakeGateway) DeleteSession(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

type fakeStore struct {
	existing string
	recorded string

	// recordErr and existingAfterRecord together mimic losing a race: the
	// write is refused, and a re-read then shows the winner's session.
	recordErr           error
	existingAfterRecord string
}

func (f *fakeStore) GetSpecSession(_ context.Context, _ uuid.UUID) (string, error) {
	return f.existing, nil
}

func (f *fakeStore) RecordSpecSession(_ context.Context, _ uuid.UUID, id string) error {
	if f.recordErr != nil {
		f.existing = f.existingAfterRecord
		return f.recordErr
	}
	f.recorded = id
	f.existing = id
	return nil
}

func opener(g *fakeGateway, s *fakeStore) *specsession.Opener {
	return specsession.NewOpener(g, s, "anthropic/claude-fable-5")
}

func TestOpenCreatesTheSessionAndRecordsIt(t *testing.T) {
	g := &fakeGateway{nextID: "api_1"}
	s := &fakeStore{}

	got, err := opener(g, s).Open(context.Background(), testCard(), nil, testReport())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got != "api_1" {
		t.Fatalf("session id = %q", got)
	}
	if s.recorded != "api_1" {
		t.Fatalf("recorded %q", s.recorded)
	}
	if len(g.created) != 1 {
		t.Fatalf("created %d sessions", len(g.created))
	}
	if g.created[0].Model != "anthropic/claude-fable-5" {
		t.Fatalf("model = %q; an unpinned session inherits the gateway default", g.created[0].Model)
	}
}

// A card is looked at repeatedly. Opening a second conversation each pass
// would fill the dashboard with duplicates and split the human's context.
func TestOpenIsIdempotentForACardAlreadyInConversation(t *testing.T) {
	g := &fakeGateway{nextID: "api_new"}
	s := &fakeStore{existing: "api_already_open"}

	got, err := opener(g, s).Open(context.Background(), testCard(), nil, testReport())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got != "api_already_open" {
		t.Fatalf("session id = %q, want the existing one", got)
	}
	if len(g.created) != 0 {
		t.Fatalf("created %d sessions for a card already in conversation", len(g.created))
	}
}

// Two workers can screen the same card at once. The loser must clean up the
// session it created, or an untouched conversation nobody is pointed at sits
// in the dashboard forever.
func TestOpenDeletesItsSessionWhenAnotherWorkerWonTheRace(t *testing.T) {
	g := &fakeGateway{nextID: "api_loser"}
	s := &fakeStore{
		recordErr:           errors.New("store: card already has a specification session"),
		existingAfterRecord: "api_winner",
	}

	got, err := opener(g, s).Open(context.Background(), testCard(), nil, testReport())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got != "api_winner" {
		t.Fatalf("session id = %q, want the recorded winner", got)
	}
	if len(g.deleted) != 1 || g.deleted[0] != "api_loser" {
		t.Fatalf("deleted %v, want the orphaned session", g.deleted)
	}
}

// §10.1: only score 2 and 3 require a human. Opening a conversation for a
// mechanical card spends someone's attention on a question nobody asked.
func TestOpenRefusesACardThatDoesNotNeedAHuman(t *testing.T) {
	for _, score := range []ambiguity.Score{ambiguity.ScoreMechanical, ambiguity.ScoreMinorInterpretation} {
		g := &fakeGateway{nextID: "api_x"}
		s := &fakeStore{}
		report := testReport()
		report.Score = score

		_, err := opener(g, s).Open(context.Background(), testCard(), nil, report)
		if !errors.Is(err, specsession.ErrNoHumanNeeded) {
			t.Fatalf("score %d: error = %v, want ErrNoHumanNeeded", score, err)
		}
		if len(g.created) != 0 {
			t.Fatalf("score %d: opened a conversation anyway", score)
		}
	}
}

// Nothing is recorded if the gateway call fails, so the next pass retries
// rather than leaving the card pointing at a session that does not exist.
func TestOpenRecordsNothingWhenTheGatewayFails(t *testing.T) {
	g := &fakeGateway{createErr: errors.New("gateway down")}
	s := &fakeStore{}

	if _, err := opener(g, s).Open(context.Background(), testCard(), nil, testReport()); err == nil {
		t.Fatal("expected an error")
	}
	if s.recorded != "" {
		t.Fatalf("recorded %q despite a failed create", s.recorded)
	}
}


// A rollout between creating a session and recording its id leaves the
// conversation in the gateway and no record on the card. The gateway then
// refuses the duplicate title on every later pass, so the card retried once a
// minute all night with the only evidence being an error saying the thing it
// wanted already existed.
func TestAConversationThatAlreadyExistsIsAdoptedRatherThanRetriedForever(t *testing.T) {
	c := testCard()
	g := &fakeGateway{
		createErr: errors.New(`hermes: status 400: {"error":{"code":"invalid_title"}}`),
		sessions: []*hermes.Session{
			{ID: "api_other", Title: "something else"},
			{ID: "api_1788135613_bee9906a", Title: specsession.Title(c)},
		},
	}
	st := &fakeStore{}

	id, err := opener(g, st).Open(context.Background(), c, nil, testReport())
	if err != nil {
		t.Fatalf("Open: %v; the conversation exists and was not found", err)
	}
	if id != "api_1788135613_bee9906a" {
		t.Errorf("session = %q, want the one already carrying this card's title", id)
	}
	if st.recorded != "api_1788135613_bee9906a" {
		t.Errorf("recorded %q; the card still does not know where its conversation is", st.recorded)
	}
}

// Adoption must not paper over a genuine failure: if nothing carries the
// title, the original error is what the operator needs to see.
func TestACreateFailureWithNoMatchingSessionStillFails(t *testing.T) {
	g := &fakeGateway{
		createErr: errors.New("hermes: status 503"),
		sessions:  []*hermes.Session{{ID: "api_other", Title: "something else"}},
	}
	st := &fakeStore{}

	if _, err := opener(g, st).Open(context.Background(), testCard(), nil, testReport()); err == nil {
		t.Fatal("Open succeeded with no session created and none to adopt")
	}
	if st.recorded != "" {
		t.Errorf("recorded %q for a card with no conversation", st.recorded)
	}
}

// A session created through POST /api/sessions holds no messages: the system
// prompt lives on the row, not in the history. A person who opened one found
// a blank chat window with no sign of which card it belonged to.
func TestANewConversationIsActuallyOpened(t *testing.T) {
	g := &fakeGateway{nextID: "api_1_abc"}
	o := opener(g, &fakeStore{})

	id, err := o.Open(context.Background(), testCard(), nil, testReport())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if g.opened[id] == "" {
		t.Fatal("the conversation was created but never opened; a person sees an empty chat")
	}
	if !strings.Contains(g.opened[id], "Add a health endpoint") {
		t.Errorf("the opening turn does not name the card: %q", g.opened[id])
	}
}

// The turn costs a model call and can fail. When it does the count stays at
// zero, which is the condition that makes the next pass try again -- and that
// is also what repairs every session created before there was a turn to post.
func TestAConversationLeftEmptyIsOpenedOnALaterPass(t *testing.T) {
	g := &fakeGateway{nextID: "api_1_abc", turnErr: errors.New("gateway down")}
	st := &fakeStore{}
	o := opener(g, st)

	id, err := o.Open(context.Background(), testCard(), nil, testReport())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if id == "" {
		t.Fatal("a failed opening turn lost the session")
	}

	// Next pass: the session is on the card, and still empty.
	st.existing = id
	g.turnErr = nil
	if _, err := o.Open(context.Background(), testCard(), nil, testReport()); err != nil {
		t.Fatalf("second Open: %v", err)
	}
	if g.opened[id] == "" {
		t.Error("an empty conversation was never opened on a later pass")
	}
}

// Re-posting an opening turn on every reconcile would cost a model call a
// minute and bury the human's own conversation.
func TestAConversationAlreadyUnderwayIsNotReopened(t *testing.T) {
	g := &fakeGateway{messages: map[string]int{"api_1_abc": 7}}
	o := opener(g, &fakeStore{existing: "api_1_abc"})

	if _, err := o.Open(context.Background(), testCard(), nil, testReport()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(g.opened) != 0 {
		t.Errorf("posted into a conversation that already had messages: %v", g.opened)
	}
}
