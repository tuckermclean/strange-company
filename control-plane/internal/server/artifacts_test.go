package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
)

type artifactStore struct {
	fakeStore
	artifacts []Artifact
	err       error
}

func (a *artifactStore) ListArtifacts(context.Context, uuid.UUID) ([]Artifact, error) {
	return a.artifacts, a.err
}

func getJSON(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// §21: the stakeholder view answers "what happened to card X?" from artifacts.
// Without an endpoint the evidence exists and nobody can read it.
func TestListArtifactsReturnsTheEvidence(t *testing.T) {
	fake := &artifactStore{artifacts: []Artifact{
		{ID: uuid.New().String(), Type: store.ArtifactImplementationPlan, Actor: "control-plane",
			ContentType: "text/plain", Content: "step one", SHA256: "abc", SizeBytes: 8},
	}}
	s := newCardsTestServer(t, fake)

	rec := getJSON(t, s, "/cards/"+uuid.New().String()+"/artifacts")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Artifacts []map[string]any `json:"artifacts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Artifacts) != 1 {
		t.Fatalf("got %d artifacts", len(body.Artifacts))
	}
	got := body.Artifacts[0]
	if got["type"] != store.ArtifactImplementationPlan {
		t.Errorf("type = %v", got["type"])
	}
	// The hash and the true size travel with the artifact: a reader has to
	// be able to tell a capped log from a complete one.
	for _, field := range []string{"sha256", "size_bytes", "truncated"} {
		if _, ok := got[field]; !ok {
			t.Errorf("response omits %q, so a truncated artifact reads as complete", field)
		}
	}
}

// A card with no artifacts is not an error, and must not render as null --
// a client iterating the list should not have to special-case it.
func TestACardWithNoArtifactsReturnsAnEmptyList(t *testing.T) {
	s := newCardsTestServer(t, &artifactStore{})

	rec := getJSON(t, s, "/cards/"+uuid.New().String()+"/artifacts")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Artifacts []map[string]any `json:"artifacts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Artifacts == nil {
		t.Fatal("artifacts rendered as null rather than an empty list")
	}
}

func TestListArtifactsRejectsAMalformedCardID(t *testing.T) {
	s := newCardsTestServer(t, &artifactStore{})

	if rec := getJSON(t, s, "/cards/not-a-uuid/artifacts"); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}
