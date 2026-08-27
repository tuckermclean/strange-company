package vikunja

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// shareServer records what the client sent and replies with a scripted
// existing-shares list.
type shareServer struct {
	existing []map[string]any
	puts     []map[string]any
	status   int
	body     string
}

func (s *shareServer) start(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/users"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(s.existing)
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/users"):
			b, _ := io.ReadAll(r.Body)
			var got map[string]any
			_ = json.Unmarshal(b, &got)
			s.puts = append(s.puts, got)
			if s.status != 0 {
				w.WriteHeader(s.status)
				_, _ = io.WriteString(w, s.body)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":1}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "token", nil)
}

// Verified against Vikunja v2.5.0 (pkg/models/project_users.go): the body is
// {"username": ..., "permission": ...}. `main` renamed nothing here, but older
// releases called the field `right` -- sending the wrong name is accepted and
// silently produces read-only access.
func TestSharingSendsTheV250FieldNames(t *testing.T) {
	s := &shareServer{}
	c := s.start(t)

	if err := c.EnsureProjectShares(context.Background(), 2, []string{"tucker"}, 1); err != nil {
		t.Fatalf("EnsureProjectShares: %v", err)
	}
	if len(s.puts) != 1 {
		t.Fatalf("sent %d shares, want 1", len(s.puts))
	}
	got := s.puts[0]
	if got["username"] != "tucker" {
		t.Errorf("username = %v", got["username"])
	}
	if got["permission"] != float64(1) {
		t.Errorf("permission = %v (%T), want 1", got["permission"], got["permission"])
	}
	if _, ok := got["right"]; ok {
		t.Errorf("sent the pre-2.x field name `right`: %v", got)
	}
}

// The supervisor re-runs every reconcile interval. Re-sharing an existing
// share each pass would be a write per minute forever.
func TestAnExistingShareIsNotSentAgain(t *testing.T) {
	s := &shareServer{existing: []map[string]any{{"username": "tucker", "permission": float64(1)}}}
	c := s.start(t)

	if err := c.EnsureProjectShares(context.Background(), 2, []string{"tucker"}, 1); err != nil {
		t.Fatalf("EnsureProjectShares: %v", err)
	}
	if len(s.puts) != 0 {
		t.Fatalf("re-sent an existing share: %v", s.puts)
	}
}

// A share that exists at the wrong permission is worse than none: the human
// sees the board and silently cannot move a card, which spec 4.3 treats as a
// real input.
func TestAShareAtTheWrongPermissionIsCorrected(t *testing.T) {
	s := &shareServer{existing: []map[string]any{{"username": "tucker", "permission": float64(0)}}}
	c := s.start(t)

	if err := c.EnsureProjectShares(context.Background(), 2, []string{"tucker"}, 1); err != nil {
		t.Fatalf("EnsureProjectShares: %v", err)
	}
	if len(s.puts) != 1 || s.puts[0]["permission"] != float64(1) {
		t.Fatalf("did not correct the permission: %v", s.puts)
	}
}

// A typo in a username must not stop the other people on the list from being
// granted access, and the error has to say which name failed.
func TestOneBadUsernameDoesNotBlockTheRest(t *testing.T) {
	s := &shareServer{status: http.StatusNotFound, body: `{"message":"user does not exist"}`}
	c := s.start(t)

	err := c.EnsureProjectShares(context.Background(), 2, []string{"ghost", "tucker"}, 1)
	if err == nil {
		t.Fatal("expected an error naming the failure")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error does not name the failing username: %v", err)
	}
	if len(s.puts) != 2 {
		t.Fatalf("stopped after the first failure: %v", s.puts)
	}
}

func TestNoUsernamesIsANoOp(t *testing.T) {
	s := &shareServer{}
	c := s.start(t)

	if err := c.EnsureProjectShares(context.Background(), 2, nil, 1); err != nil {
		t.Fatalf("EnsureProjectShares: %v", err)
	}
	if len(s.puts) != 0 {
		t.Fatalf("shared with nobody but sent %v", s.puts)
	}
}

// Blank entries come from a values list with a stray comma. Sending one asks
// Vikunja to share with a user named "".
func TestBlankUsernamesAreIgnored(t *testing.T) {
	s := &shareServer{}
	c := s.start(t)

	if err := c.EnsureProjectShares(context.Background(), 2, []string{"", "  ", "tucker"}, 1); err != nil {
		t.Fatalf("EnsureProjectShares: %v", err)
	}
	if len(s.puts) != 1 || s.puts[0]["username"] != "tucker" {
		t.Fatalf("sent %v", s.puts)
	}
}

// 0, 1 and 2 are the only permissions Vikunja defines. Anything else would be
// accepted by the API's length validator and mean nothing.
func TestAnOutOfRangePermissionIsRefused(t *testing.T) {
	s := &shareServer{}
	c := s.start(t)

	for _, p := range []int{-1, 3, 99} {
		if err := c.EnsureProjectShares(context.Background(), 2, []string{"tucker"}, p); err == nil {
			t.Errorf("permission %d was accepted", p)
		}
	}
	if len(s.puts) != 0 {
		t.Fatalf("called the API with an invalid permission: %v", s.puts)
	}
}
