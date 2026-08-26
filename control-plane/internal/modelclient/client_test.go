package modelclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestClient builds a Client pointed at srv using the given apiKey and
// model, failing the test immediately if construction fails.
func newTestClient(t *testing.T, srv *httptest.Server, apiKey, model string) *Client {
	t.Helper()
	c, err := New(srv.URL, apiKey, model, WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	return c
}

func TestNewRejectsAnEmptyBaseURL(t *testing.T) {
	c, err := New("", "some-key", "some-model")
	if err == nil {
		t.Fatalf("New(\"\", ...) returned nil error, want an error")
	}
	if c != nil {
		t.Fatalf("New(\"\", ...) returned a non-nil Client alongside an error")
	}
	if !errors.Is(err, ErrNoBaseURL) {
		t.Fatalf("New(\"\", ...) error = %v, want it to wrap ErrNoBaseURL", err)
	}
	if !strings.Contains(err.Error(), "OpenAI-compatible") {
		t.Fatalf("New(\"\", ...) error = %q, want it to name the problem (OpenAI-compatible endpoint required)", err.Error())
	}
}

func TestCompleteSendsModelAndMessages(t *testing.T) {
	var gotBody wireRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"model":"some-model","choices":[{"message":{"content":"hi"}}]}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "", "some-model")

	req := CompleteRequest{
		Messages: []Message{
			{Role: "system", Content: "you are terse"},
			{Role: "user", Content: "ping"},
		},
		MaxTokens: 42,
	}
	if _, err := c.Complete(context.Background(), req); err != nil {
		t.Fatalf("Complete: unexpected error: %v", err)
	}

	if gotBody.Model != "some-model" {
		t.Fatalf("request model = %q, want %q", gotBody.Model, "some-model")
	}
	if len(gotBody.Messages) != 2 {
		t.Fatalf("request messages = %v, want 2 messages", gotBody.Messages)
	}
	if gotBody.Messages[0] != req.Messages[0] || gotBody.Messages[1] != req.Messages[1] {
		t.Fatalf("request messages = %v, want %v", gotBody.Messages, req.Messages)
	}
	if gotBody.MaxTokens != 42 {
		t.Fatalf("request max_tokens = %d, want 42", gotBody.MaxTokens)
	}
}

func TestCompleteSetsAuthorizationOnlyWhenAKeyIsPresent(t *testing.T) {
	t.Run("key present", func(t *testing.T) {
		var gotHeader string
		var hadHeader bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotHeader = r.Header.Get("Authorization")
			_, hadHeader = r.Header["Authorization"]
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}]}`))
		}))
		defer srv.Close()

		c := newTestClient(t, srv, "secret-key", "m")
		if _, err := c.Complete(context.Background(), CompleteRequest{Messages: []Message{{Role: "user", Content: "hi"}}}); err != nil {
			t.Fatalf("Complete: unexpected error: %v", err)
		}
		if !hadHeader {
			t.Fatalf("Authorization header missing, want it present when apiKey is set")
		}
		if gotHeader != "Bearer secret-key" {
			t.Fatalf("Authorization header = %q, want %q", gotHeader, "Bearer secret-key")
		}
	})

	t.Run("key absent", func(t *testing.T) {
		var hadHeader bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, hadHeader = r.Header["Authorization"]
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}]}`))
		}))
		defer srv.Close()

		c := newTestClient(t, srv, "", "m")
		if _, err := c.Complete(context.Background(), CompleteRequest{Messages: []Message{{Role: "user", Content: "hi"}}}); err != nil {
			t.Fatalf("Complete: unexpected error: %v", err)
		}
		if hadHeader {
			t.Fatalf("Authorization header present, want it entirely ABSENT when apiKey is empty (not just empty-valued)")
		}
	})
}

func TestCompleteReturnsTextAndUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"model": "some-model-v1",
			"choices": [{"message": {"content": "the answer is 2"}}],
			"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
		}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "", "some-model")
	got, err := c.Complete(context.Background(), CompleteRequest{Messages: []Message{{Role: "user", Content: "2+2?"}}})
	if err != nil {
		t.Fatalf("Complete: unexpected error: %v", err)
	}

	if got.Text != "the answer is 2" {
		t.Fatalf("Text = %q, want %q", got.Text, "the answer is 2")
	}
	if got.Model != "some-model-v1" {
		t.Fatalf("Model = %q, want %q", got.Model, "some-model-v1")
	}
	wantUsage := Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}
	if got.Usage != wantUsage {
		t.Fatalf("Usage = %+v, want %+v", got.Usage, wantUsage)
	}
	if len(got.Raw) == 0 {
		t.Fatalf("Raw is empty, want the response body captured as evidence")
	}
}

func TestCompleteOnNon2xxWrapsErrHTTPAndIncludesStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("boom, provider is down"))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "", "m")
	_, err := c.Complete(context.Background(), CompleteRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatalf("Complete: got nil error, want an error for a 500 response")
	}
	if !errors.Is(err, ErrHTTP) {
		t.Fatalf("Complete error = %v, want it to wrap ErrHTTP", err)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("Complete error = %q, want it to include the status code 500", err.Error())
	}
}

func TestCompleteTruncatesTheErrorBody(t *testing.T) {
	hugeBody := strings.Repeat("x", 100*1024) // 100KB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(hugeBody))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "", "m")
	_, err := c.Complete(context.Background(), CompleteRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatalf("Complete: got nil error, want an error for a 502 response")
	}
	if len(err.Error()) >= 700 {
		t.Fatalf("Complete error is %d bytes, want it bounded well under the 100KB body (< 700 bytes)", len(err.Error()))
	}
}

func TestCompleteNeverLeaksTheAPIKeyInErrors(t *testing.T) {
	const apiKey = "top-secret-api-key-do-not-leak"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, apiKey, "m")
	_, err := c.Complete(context.Background(), CompleteRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatalf("Complete: got nil error, want an error for a 500 response")
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Fatalf("Complete error leaks the API key: %q", err.Error())
	}
}

func TestEmptyChoicesIsAnErrorNotAnEmptyString(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "", "m")
	got, err := c.Complete(context.Background(), CompleteRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatalf("Complete: got nil error with zero choices, want ErrEmptyResponse")
	}
	if !errors.Is(err, ErrEmptyResponse) {
		t.Fatalf("Complete error = %v, want it to wrap ErrEmptyResponse", err)
	}
	if got != nil {
		t.Fatalf("Complete returned a non-nil Completion alongside an error: %+v", got)
	}
}

func TestEmptyContentIsAnErrorNotAnEmptyString(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":""}}]}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "", "m")
	got, err := c.Complete(context.Background(), CompleteRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatalf("Complete: got nil error with empty content, want ErrEmptyResponse")
	}
	if !errors.Is(err, ErrEmptyResponse) {
		t.Fatalf("Complete error = %v, want it to wrap ErrEmptyResponse", err)
	}
	if got != nil {
		t.Fatalf("Complete returned a non-nil Completion alongside an error: %+v", got)
	}
}

func TestBaseURLWithTrailingSlashDoesNotDoubleUp(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}]}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL+"/", "", "m", WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	if _, err := c.Complete(context.Background(), CompleteRequest{Messages: []Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatalf("Complete: unexpected error: %v", err)
	}

	if gotPath != "/chat/completions" {
		t.Fatalf("request path = %q, want %q (no double slash)", gotPath, "/chat/completions")
	}
}

func TestCompleteRespectsContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Outlast the client's short deadline (below) but never actually
		// block the test run: whichever fires first wins, and this handler
		// always returns well inside the test's own timeout.
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "", "m")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.Complete(ctx, CompleteRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("Complete: got nil error, want a context-deadline error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Complete error = %v, want it to wrap context.DeadlineExceeded", err)
	}
	if elapsed >= defaultTimeout {
		t.Fatalf("Complete took %v, want it to respect the short caller context deadline rather than the %v default", elapsed, defaultTimeout)
	}
}
