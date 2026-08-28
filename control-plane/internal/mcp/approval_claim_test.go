package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
)

type recordingArtifacts struct {
	put []store.Artifact
}

func (r *recordingArtifacts) PutArtifact(_ context.Context, a store.Artifact) (*store.Artifact, error) {
	r.put = append(r.put, a)
	a.ID = uuid.New()
	return &a, nil
}

func claimServer(t *testing.T) (*Server, *recordingArtifacts, *recordingCards) {
	t.Helper()
	arts, cards := &recordingArtifacts{}, &recordingCards{}
	s := NewServer(cards)
	s.SetArtifacts(arts)
	return s, arts, cards
}

// A model in the specification conversation can say a human approved. That is
// worth recording -- it is what the human said -- but §10.2 needs a human, and
// a model asserting one is not one.
func TestReportingApprovalRecordsAClaimNotAnApproval(t *testing.T) {
	s, arts, _ := claimServer(t)
	id := uuid.New()

	args, _ := json.Marshal(map[string]any{
		"card_id":     id.String(),
		"approved_by": "tucker",
		"note":        "confirmed in conversation",
	})
	result, err := s.invokeTool(context.Background(), toolByName(t, "specs.report_human_approval"), args)
	if err != nil {
		t.Fatalf("invokeTool: %v", err)
	}

	if len(arts.put) != 1 {
		t.Fatalf("recorded %d artifacts", len(arts.put))
	}
	got := arts.put[0]
	if got.Type != store.ArtifactHumanDecision {
		t.Errorf("artifact type = %q", got.Type)
	}
	if !strings.Contains(got.Content, "tucker") {
		t.Errorf("the claim does not name who is said to have approved: %q", got.Content)
	}

	// The reply must not let a model believe it has approved anything.
	encoded, _ := json.Marshal(result)
	if !strings.Contains(strings.ToLower(string(encoded)), "not an approval") {
		t.Errorf("the result does not say this is not an approval: %s", encoded)
	}
}

// The whole point: this tool must never reach the approval that gates
// promotion. If it could, a model would approve its own specification.
func TestReportingApprovalCannotPromoteACard(t *testing.T) {
	s, _, cards := claimServer(t)

	args, _ := json.Marshal(map[string]any{
		"card_id":     uuid.New().String(),
		"approved_by": "tucker",
	})
	if _, err := s.invokeTool(context.Background(), toolByName(t, "specs.report_human_approval"), args); err != nil {
		t.Fatal(err)
	}
	if cards.called {
		t.Fatal("a reported approval moved the card")
	}
}

// A claim that names nobody is not evidence of anything.
func TestAReportedApprovalMustNameSomeone(t *testing.T) {
	s, arts, _ := claimServer(t)

	args, _ := json.Marshal(map[string]any{"card_id": uuid.New().String()})
	if _, err := s.invokeTool(context.Background(), toolByName(t, "specs.report_human_approval"), args); err == nil {
		t.Fatal("expected an error")
	}
	if len(arts.put) != 0 {
		t.Fatalf("recorded an unattributed claim: %+v", arts.put)
	}
}

// The tool's own description has to say what it does not do, because the model
// reading it is the one that would otherwise assume it approved something.
func TestTheToolSaysItIsNotAnApproval(t *testing.T) {
	tool := toolByName(t, "specs.report_human_approval")
	if !strings.Contains(strings.ToLower(tool.Description), "does not") {
		t.Fatalf("description does not state the limit: %q", tool.Description)
	}
}
