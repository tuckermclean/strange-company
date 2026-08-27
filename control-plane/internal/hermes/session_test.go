package hermes_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tuckermclean/strange-company/control-plane/internal/hermes"
)

// captured is one request the fake gateway received, kept as raw bytes so a
// test can assert on what was NOT sent as easily as on what was.
type captured struct {
	method string
	path   string
	auth   string
	body   []byte
}

func fakeGateway(t *testing.T, status int, respBody string) (*hermes.Client, *[]captured) {
	t.Helper()
	var seen []captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = append(seen, captured{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization"), body: b})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)

	c, err := hermes.New(srv.URL, "test-key")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, &seen
}

const createdSession = `{"object":"hermes.session","session":{"id":"api_1787781129_5343ebc5",` +
	`"source":"api_server","model":"anthropic/claude-fable-5","title":"card: add a health endpoint",` +
	`"has_system_prompt":true,"has_model_config":true}}`

func validRequest() hermes.SpecSession {
	return hermes.SpecSession{
		Title:        "card: add a health endpoint",
		Model:        "anthropic/claude-fable-5",
		SystemPrompt: "You are helping specify one card.",
	}
}

func TestCreateSessionReturnsTheSessionID(t *testing.T) {
	c, seen := fakeGateway(t, http.StatusCreated, createdSession)

	s, err := c.CreateSession(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if s.ID != "api_1787781129_5343ebc5" {
		t.Fatalf("id = %q", s.ID)
	}
	if len(*seen) != 1 {
		t.Fatalf("expected exactly one request, got %d", len(*seen))
	}
	got := (*seen)[0]
	if got.method != http.MethodPost || got.path != "/api/sessions" {
		t.Fatalf("sent %s %s", got.method, got.path)
	}
	if got.auth != "Bearer test-key" {
		t.Fatalf("auth header = %q", got.auth)
	}
}

// The gateway accepts a "profile" key and silently ignores it, and with
// multiplexing off every /p/<name>/ prefix is served by the default profile.
// Either would look like a correctly pinned specifier while running whatever
// the default happens to be, so this client must never reach for one.
func TestCreateSessionNeverSendsAProfile(t *testing.T) {
	c, seen := fakeGateway(t, http.StatusCreated, createdSession)

	if _, err := c.CreateSession(context.Background(), validRequest()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got := (*seen)[0]
	var body map[string]any
	if err := json.Unmarshal(got.body, &body); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	for _, forbidden := range []string{"profile", "agent"} {
		if _, ok := body[forbidden]; ok {
			t.Errorf("request body carries %q: %s", forbidden, got.body)
		}
	}
	if got.path != "/api/sessions" {
		t.Errorf("path %q reaches for a profile prefix", got.path)
	}
	for _, want := range []string{"title", "model", "system_prompt"} {
		if _, ok := body[want]; !ok {
			t.Errorf("request body is missing %q: %s", want, got.body)
		}
	}
}

// A session created without a model inherits whatever the gateway's default
// is -- on a live deployment that was a model its own backend refused. An
// unpinned session is not a working session, so this fails before the call.
func TestCreateSessionRequiresAModelAndAPrompt(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  hermes.SpecSession
	}{
		{"no model", hermes.SpecSession{Title: "t", SystemPrompt: "p"}},
		{"no system prompt", hermes.SpecSession{Title: "t", Model: "m"}},
		{"no title", hermes.SpecSession{Model: "m", SystemPrompt: "p"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, seen := fakeGateway(t, http.StatusCreated, createdSession)
			if _, err := c.CreateSession(context.Background(), tc.req); err == nil {
				t.Fatal("expected an error")
			}
			if len(*seen) != 0 {
				t.Fatalf("made %d HTTP calls for an invalid request", len(*seen))
			}
		})
	}
}

func TestCreateSessionRejectsANonSuccessStatus(t *testing.T) {
	c, _ := fakeGateway(t, http.StatusUnauthorized, `{"detail":"Unauthorized"}`)

	_, err := c.CreateSession(context.Background(), validRequest())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, hermes.ErrHTTP) {
		t.Fatalf("error %v is not ErrHTTP", err)
	}
}

// The gateway returns 201 for bodies it did not understand, so a response
// without an id has to be an error rather than a session with an empty id
// that later reads as "no conversation was ever started".
func TestCreateSessionRejectsAResponseWithoutAnID(t *testing.T) {
	c, _ := fakeGateway(t, http.StatusCreated, `{"object":"hermes.session","session":{"title":"t"}}`)

	if _, err := c.CreateSession(context.Background(), validRequest()); err == nil {
		t.Fatal("expected an error for a session with no id")
	}
}

func TestDeleteSessionIssuesADelete(t *testing.T) {
	c, seen := fakeGateway(t, http.StatusOK, `{}`)

	if err := c.DeleteSession(context.Background(), "api_1787781129_5343ebc5"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	got := (*seen)[0]
	if got.method != http.MethodDelete || got.path != "/api/sessions/api_1787781129_5343ebc5" {
		t.Fatalf("sent %s %s", got.method, got.path)
	}
}
