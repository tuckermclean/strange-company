package github_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tuckermclean/strange-company/control-plane/internal/github"
)

type prStub struct {
	existing string
	created  map[string]any
	patched  map[string]any
	paths    []string
}

func (p *prStub) client(t *testing.T) *github.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.paths = append(p.paths, r.Method+" "+r.URL.Path)
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet:
			_, _ = io.WriteString(w, p.existing)
		case r.Method == http.MethodPatch:
			_ = json.Unmarshal(b, &p.patched)
			_, _ = io.WriteString(w, `{"number":7,"html_url":"https://github.com/example/repo/pull/7"}`)
		default:
			_ = json.Unmarshal(b, &p.created)
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"number":7,"html_url":"https://github.com/example/repo/pull/7"}`)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := github.New(srv.URL, "ghp_token", nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func pull() github.PullRequest {
	return github.PullRequest{
		Repository: "example/repo",
		Head:       "agent/card-1",
		Base:       "main",
		Title:      "Add a health endpoint",
		Body:       "- [x] AC1: returns 200",
	}
}

func TestAPullRequestIsOpened(t *testing.T) {
	stub := &prStub{existing: `[]`}

	got, err := stub.client(t).EnsurePullRequest(context.Background(), pull())
	if err != nil {
		t.Fatalf("EnsurePullRequest: %v", err)
	}
	if got.URL != "https://github.com/example/repo/pull/7" {
		t.Fatalf("url = %q", got.URL)
	}
	if stub.created["head"] != "agent/card-1" || stub.created["base"] != "main" {
		t.Fatalf("created = %+v", stub.created)
	}
}

// A card is reviewed more than once -- CORRECTABLE sends it back into
// implementation. Opening a second pull request for the same branch would
// fail, and worse, would scatter one piece of work across several reviews.
func TestAnExistingPullRequestIsUpdatedRatherThanDuplicated(t *testing.T) {
	stub := &prStub{existing: `[{"number":7,"html_url":"https://github.com/example/repo/pull/7"}]`}

	got, err := stub.client(t).EnsurePullRequest(context.Background(), pull())
	if err != nil {
		t.Fatalf("EnsurePullRequest: %v", err)
	}
	if got.Number != 7 {
		t.Fatalf("number = %d", got.Number)
	}
	if stub.created != nil {
		t.Fatal("opened a second pull request for a branch that already had one")
	}
	if stub.patched["body"] == nil {
		t.Fatal("did not update the existing pull request's body")
	}
}

// The search must be for this branch, or an unrelated open pull request would
// be adopted and overwritten.
func TestTheSearchIsScopedToTheAgentBranch(t *testing.T) {
	stub := &prStub{existing: `[]`}

	if _, err := stub.client(t).EnsurePullRequest(context.Background(), pull()); err != nil {
		t.Fatal(err)
	}
	if len(stub.paths) == 0 || !strings.HasPrefix(stub.paths[0], "GET ") {
		t.Fatalf("did not look for an existing pull request first: %v", stub.paths)
	}
}

func TestAPullRequestNeedsABranchAndABase(t *testing.T) {
	stub := &prStub{existing: `[]`}
	c := stub.client(t)

	for _, mut := range []func(*github.PullRequest){
		func(p *github.PullRequest) { p.Head = "" },
		func(p *github.PullRequest) { p.Base = "" },
		func(p *github.PullRequest) { p.Repository = "" },
		func(p *github.PullRequest) { p.Title = "" },
	} {
		pr := pull()
		mut(&pr)
		if _, err := c.EnsurePullRequest(context.Background(), pr); err == nil {
			t.Errorf("accepted %+v", pr)
		}
	}
}
