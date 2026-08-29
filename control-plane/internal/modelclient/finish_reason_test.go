package modelclient_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tuckermclean/strange-company/control-plane/internal/modelclient"
)

func clientAgainst(t *testing.T, body string) *modelclient.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	c, err := modelclient.New(srv.URL, "k", "test-model")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func complete(t *testing.T, c *modelclient.Client) (*modelclient.Completion, error) {
	t.Helper()
	return c.Complete(context.Background(), modelclient.CompleteRequest{
		Messages:  []modelclient.Message{{Role: "user", Content: "hi"}},
		MaxTokens: 16,
	})
}

// Observed on a live Hermes gateway: a backend failure comes back as HTTP 200
// with the error text as assistant content and finish_reason "error". Reading
// content on a 200 records an outage as a model answer -- per spec §12.1 that
// misclassification burns a rung of the escalation ladder on a problem no
// model was ever asked to solve.
func TestAFinishReasonOfErrorIsNotAnAnswer(t *testing.T) {
	c := clientAgainst(t, `{"model":"hermes-agent","choices":[{"index":0,`+
		`"message":{"role":"assistant","content":"HTTP 400: {\"detail\":\"The 'anthropic/claude-opus-4.6' model is not supported when using Codex with a ChatGPT account.\"}"},`+
		`"finish_reason":"error"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`)

	_, err := complete(t, c)
	if err == nil {
		t.Fatal("a finish_reason of \"error\" was accepted as a completion")
	}
	if !errors.Is(err, modelclient.ErrProviderFailure) {
		t.Fatalf("error %v is not ErrProviderFailure", err)
	}
}

// The error text is the only diagnostic the gateway gives, so losing it would
// leave an operator with a failure and no cause.
func TestAProviderFailureCarriesTheReportedCause(t *testing.T) {
	c := clientAgainst(t, `{"choices":[{"message":{"content":"HTTP 400: model not supported"},"finish_reason":"error"}]}`)

	_, err := complete(t, c)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "model not supported") {
		t.Fatalf("error does not carry the cause: %v", err)
	}
}

// Truncation is a real answer that stopped early, not a failure: the caller
// decides whether a short answer is usable. Only "error" is fatal here.
func TestOrdinaryFinishReasonsStillReturnTheAnswer(t *testing.T) {
	for _, reason := range []string{"stop", "length", "tool_calls", "content_filter", ""} {
		t.Run("finish_reason="+reason, func(t *testing.T) {
			c := clientAgainst(t, `{"choices":[{"message":{"content":"the answer"},"finish_reason":"`+reason+`"}]}`)

			got, err := complete(t, c)
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if got.Text != "the answer" {
				t.Fatalf("text = %q", got.Text)
			}
		})
	}
}

// A failed turn can come back with empty content as easily as with error text.
// Reporting that as "the provider returned nothing" would send the caller
// looking for a parsing bug instead of an outage.
func TestAnEmptyErrorTurnIsReportedAsAProviderFailure(t *testing.T) {
	c := clientAgainst(t, `{"choices":[{"message":{"content":""},"finish_reason":"error"}]}`)

	_, err := complete(t, c)
	if !errors.Is(err, modelclient.ErrProviderFailure) {
		t.Fatalf("error %v is not ErrProviderFailure", err)
	}
}

// A reasoning model spends completion tokens thinking before it writes
// anything, and those tokens are billed against max_tokens. With a tight
// budget it returns empty content and finish_reason "length" -- the whole
// budget went on reasoning and the answer never started.
//
// Reported as "empty response" this is undiagnosable: it looks like the
// provider returned nothing, when in fact it returned a great deal and none of
// it was the answer.
func TestABudgetExhaustedBeforeAnyContentSaysSo(t *testing.T) {
	c := clientAgainst(t, `{"choices":[{"message":{"content":"","reasoning_content":"We need to answer. User"},`+
		`"finish_reason":"length"}],"usage":{"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":5}}}`)

	_, err := complete(t, c)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, modelclient.ErrBudgetExhausted) {
		t.Fatalf("error = %v, want ErrBudgetExhausted", err)
	}
	if errors.Is(err, modelclient.ErrEmptyResponse) {
		t.Error("reported as an empty response, which hides what happened")
	}
	// The message has to name the budget, or an operator cannot tell which
	// number to raise.
	if !strings.Contains(err.Error(), "16") {
		t.Errorf("error does not name the token budget that was too small: %v", err)
	}
}

// Truncation AFTER some content is different: there is an answer, just a
// clipped one, and whether that is usable is the caller's decision.
func TestTruncationAfterSomeContentIsStillAnAnswer(t *testing.T) {
	c := clientAgainst(t, `{"choices":[{"message":{"content":"partial ans"},"finish_reason":"length"}]}`)

	got, err := complete(t, c)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got.Text != "partial ans" {
		t.Fatalf("text = %q", got.Text)
	}
}
