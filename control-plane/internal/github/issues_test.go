package github_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tuckermclean/strange-company/control-plane/internal/github"
)

type apiStub struct {
	pages   []string // JSON body per page, 1-indexed by the page query
	queries []string
	auth    string
	status  int
}

func (a *apiStub) start(t *testing.T) *github.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.queries = append(a.queries, r.URL.String())
		a.auth = r.Header.Get("Authorization")
		if a.status != 0 {
			w.WriteHeader(a.status)
			_, _ = io.WriteString(w, `{"message":"nope"}`)
			return
		}
		page := 1
		fmt.Sscanf(r.URL.Query().Get("page"), "%d", &page)
		w.Header().Set("Content-Type", "application/json")
		// GitHub advertises the next page in a Link header; a short page
		// is only a hint, and on the last page there is no next link.
		if page < len(a.pages) {
			w.Header().Set("Link", fmt.Sprintf(`<%s?page=%d>; rel="next"`, r.URL.Path, page+1))
		}
		if page >= 1 && page <= len(a.pages) {
			_, _ = io.WriteString(w, a.pages[page-1])
			return
		}
		_, _ = io.WriteString(w, `[]`)
	}))
	t.Cleanup(srv.Close)
	c, err := github.New(srv.URL, "ghp_token", nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

const oneIssue = `[{"number":7,"title":"Add a health endpoint","body":"# Problem\n\nx",
"html_url":"https://github.com/example/repo/issues/7","state":"open",
"labels":[{"name":"agent-ready"}]}]`

func TestListsLabelledIssues(t *testing.T) {
	a := &apiStub{pages: []string{oneIssue}}
	c := a.start(t)

	issues, err := c.ListLabeledIssues(context.Background(), "example/repo", "agent-ready")
	if err != nil {
		t.Fatalf("ListLabeledIssues: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("got %d issues", len(issues))
	}
	got := issues[0]
	if got.Number != 7 || got.Title != "Add a health endpoint" {
		t.Fatalf("issue = %+v", got)
	}
	// Identity must include the repository: issue #7 exists in every repo
	// on GitHub, and two of them are not the same piece of work.
	if got.ExternalID() != "example/repo#7" {
		t.Fatalf("ExternalID = %q", got.ExternalID())
	}
	if a.auth != "Bearer ghp_token" {
		t.Fatalf("auth = %q", a.auth)
	}
	q := a.queries[0]
	for _, want := range []string{"/repos/example/repo/issues", "labels=agent-ready", "state=open"} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q is missing %q", q, want)
		}
	}
}

// GitHub's issues endpoint returns pull requests too -- every PR is an issue
// in that API. Ingesting them would turn every open PR into a card asking an
// agent to implement it.
func TestPullRequestsAreNotIssues(t *testing.T) {
	body := `[{"number":7,"title":"a real issue","html_url":"u","labels":[{"name":"agent-ready"}]},
{"number":8,"title":"a pull request","html_url":"u","labels":[{"name":"agent-ready"}],
"pull_request":{"url":"https://api.github.com/repos/example/repo/pulls/8"}}]`
	a := &apiStub{pages: []string{body}}
	c := a.start(t)

	issues, err := c.ListLabeledIssues(context.Background(), "example/repo", "agent-ready")
	if err != nil {
		t.Fatalf("ListLabeledIssues: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("got %d items; a pull request was ingested as an issue: %+v", len(issues), issues)
	}
	if issues[0].Number != 7 {
		t.Fatalf("kept the wrong item: %+v", issues[0])
	}
}

// A backlog larger than one page must not be silently truncated to the first
// hundred issues. GitHub signals more pages with a Link header rather than by
// returning a full page, so following the count alone would stop early on any
// page that happened to be short.
func TestEveryPageIsRead(t *testing.T) {
	first := `[{"number":1,"title":"one","html_url":"u"},{"number":2,"title":"two","html_url":"u"}]`
	second := `[{"number":3,"title":"three","html_url":"u"}]`
	a := &apiStub{pages: []string{first, second}}
	c := a.start(t)

	issues, err := c.ListLabeledIssues(context.Background(), "example/repo", "agent-ready")
	if err != nil {
		t.Fatalf("ListLabeledIssues: %v", err)
	}
	if len(issues) != 3 {
		t.Fatalf("got %d issues across %d requests; pagination stopped early", len(issues), len(a.queries))
	}
}

func TestAnAPIFailureIsReported(t *testing.T) {
	a := &apiStub{status: http.StatusUnauthorized}
	c := a.start(t)

	_, err := c.ListLabeledIssues(context.Background(), "example/repo", "agent-ready")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "ghp_token") {
		t.Fatalf("error leaks the token: %v", err)
	}
}

func TestAMalformedRepositoryIsRefused(t *testing.T) {
	a := &apiStub{pages: []string{`[]`}}
	c := a.start(t)

	for _, repo := range []string{"", "no-slash", "too/many/slashes", "/repo", "owner/"} {
		if _, err := c.ListLabeledIssues(context.Background(), repo, "agent-ready"); err == nil {
			t.Errorf("repository %q was accepted", repo)
		}
	}
	if len(a.queries) != 0 {
		t.Fatalf("called the API with a malformed repository: %v", a.queries)
	}
}
