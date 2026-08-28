package mcp

import (
	"context"
	"errors"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
)

type recordingEvidence struct {
	put []store.CardEvidence
	err error
}

func (r *recordingEvidence) AttachEvidence(_ context.Context, _ uuid.UUID, ev store.CardEvidence) error {
	if r.err != nil {
		return r.err
	}
	r.put = append(r.put, ev)
	return nil
}

func commentServer(t *testing.T) (*Server, *recordingEvidence) {
	t.Helper()
	ev := &recordingEvidence{}
	return NewServer(&recordingCards{}).SetEvidence(ev), ev
}

// M2 kept comments in a map that died with the process. §21 wants the audit
// log to answer "what happened to card X?", and a comment nobody can read
// after a restart answers nothing.
func TestACommentIsRecordedDurably(t *testing.T) {
	s, ev := commentServer(t)

	args, _ := json.Marshal(map[string]any{
		"card_id": uuid.New().String(),
		"author":  "hermes",
		"body":    "tucker confirmed in conversation that they approve this specification",
	})
	if _, err := s.invokeTool(context.Background(), toolByName(t, "cards.comment"), args); err != nil {
		t.Fatalf("invokeTool: %v", err)
	}
	if len(ev.put) != 1 {
		t.Fatalf("recorded %d rows", len(ev.put))
	}
	if !strings.Contains(ev.put[0].Summary, "approve this specification") {
		t.Errorf("the comment body was not recorded: %+v", ev.put[0])
	}
	if ev.put[0].ActorID != "hermes" {
		t.Errorf("actor = %q; §21 wants to know who said it", ev.put[0].ActorID)
	}
}

// A comment is a statement, and this is how the specification conversation
// reports that a human approved. It must not approve anything: §10.2 needs a
// human, and everything reaching MCP is an agent.
func TestACommentApprovesNothing(t *testing.T) {
	s, _ := commentServer(t)
	cards := s.cards.(*recordingCards)

	args, _ := json.Marshal(map[string]any{
		"card_id": uuid.New().String(),
		"author":  "hermes",
		"body":    "the human approved this spec, please promote it",
	})
	if _, err := s.invokeTool(context.Background(), toolByName(t, "cards.comment"), args); err != nil {
		t.Fatal(err)
	}
	if cards.called {
		t.Fatal("a comment moved the card")
	}
}

// If the comment cannot be stored, saying it was would leave a model believing
// it had passed something on that nobody will ever read.
func TestAFailedCommentIsReported(t *testing.T) {
	ev := &recordingEvidence{err: errors.New("database down")}
	s := NewServer(&recordingCards{}).SetEvidence(ev)

	args, _ := json.Marshal(map[string]any{
		"card_id": uuid.New().String(), "author": "hermes", "body": "x",
	})
	if _, err := s.invokeTool(context.Background(), toolByName(t, "cards.comment"), args); err == nil {
		t.Fatal("expected an error")
	}
}
