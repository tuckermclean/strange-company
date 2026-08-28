package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/tuckermclean/strange-company/control-plane/internal/card"
)

// recordingCards captures the actor a transition was actually attempted with.
type recordingCards struct {
	CardService
	actor   card.ActorType
	actorID string
	called  bool
}

func (r *recordingCards) Transition(_ context.Context, _ uuid.UUID, _ card.State, actor card.ActorType, actorID, _ string) error {
	r.called = true
	r.actor = actor
	r.actorID = actorID
	return nil
}

// Everything reaching the control plane through MCP is an agent. A model that
// could name itself "human" would inherit the entire human-only column of the
// state machine: Review -> Done (§18: automated review cannot move a card to
// Done), Blocked -> Ready and NeedsHuman -> Ready (an agent must never
// un-block itself).
func TestATransitionThroughMCPIsAlwaysAnAgent(t *testing.T) {
	for _, claimed := range []string{"human", "system", "HUMAN", "agent", ""} {
		rec := &recordingCards{}
		s := NewServer(rec)

		args, _ := json.Marshal(map[string]any{
			"card_id":    uuid.New().String(),
			"to":         string(card.Done),
			"actor_type": claimed,
			"actor_id":   "meeseeks-1",
			"reason":     "done",
		})
		_, _ = s.invokeTool(context.Background(), toolByName(t, "cards.transition"), args)

		if rec.called && rec.actor != card.ActorAgent {
			t.Fatalf("actor_type %q reached the store as %q; MCP must stamp agent", claimed, rec.actor)
		}
	}
}

// The tool must not advertise a field that cannot be honoured: a caller told
// it may choose an actor will believe the choice mattered.
func TestTheTransitionToolDoesNotOfferAnActorType(t *testing.T) {
	for _, tool := range toolRegistry {
		if tool.Name != "cards.transition" {
			continue
		}
		schema, err := json.Marshal(tool.Schema)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(schema), "actor_type") {
			t.Fatalf("cards.transition still offers actor_type: %s", schema)
		}
		return
	}
	t.Fatal("cards.transition is not registered")
}

// Which agent acted still matters for the audit log (§21); only the actor
// TYPE is no longer the caller's to choose.
func TestTheCallingAgentIsStillRecorded(t *testing.T) {
	rec := &recordingCards{}
	s := NewServer(rec)

	args, _ := json.Marshal(map[string]any{
		"card_id":  uuid.New().String(),
		"to":       string(card.Review),
		"actor_id": "meeseeks-7",
		"reason":   "green",
	})
	if _, err := s.invokeTool(context.Background(), toolByName(t, "cards.transition"), args); err != nil {
		t.Fatalf("invokeTool: %v", err)
	}
	if rec.actorID != "meeseeks-7" {
		t.Fatalf("actor_id = %q", rec.actorID)
	}
}

func toolByName(t *testing.T, name string) toolSpec {
	t.Helper()
	for _, tool := range toolRegistry {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q is not registered", name)
	return toolSpec{}
}
