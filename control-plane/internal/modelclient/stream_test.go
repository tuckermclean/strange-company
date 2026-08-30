package modelclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func sseServer(t *testing.T, handler func(w http.ResponseWriter, flush func())) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		handler(w, func() {
			if f != nil {
				f.Flush()
			}
		})
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "", "test-model")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func chunk(text string) string {
	return fmt.Sprintf("data: {\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", text)
}

func TestAStreamedAnswerIsAssembledInOrder(t *testing.T) {
	c := sseServer(t, func(w http.ResponseWriter, flush func()) {
		_, _ = io.WriteString(w, chunk("VERDICT: "))
		_, _ = io.WriteString(w, chunk("PASS\n\nLooks right."))
		_, _ = io.WriteString(w, `data: {"usage":{"prompt_tokens":22173,"completion_tokens":26829}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		flush()
	})

	got, err := c.CompleteStreaming(context.Background(), CompleteRequest{MaxTokens: 100})
	if err != nil {
		t.Fatalf("CompleteStreaming: %v", err)
	}
	if got.Text != "VERDICT: PASS\n\nLooks right." {
		t.Errorf("text = %q", got.Text)
	}
	// Without stream_options include_usage an OpenAI-compatible stream
	// reports no usage at all, and §22 would go blind on the dearest calls.
	if got.Usage.CompletionTokens != 26829 {
		t.Errorf("usage = %+v, want the streamed totals", got.Usage)
	}
}

// The whole reason streaming is here: a model reasoning over a large diff is
// working the entire time, and a whole-call deadline fails exactly the large
// cards. A gap between chunks needs no guess about diff size.
func TestASlowButProgressingStreamIsNotKilled(t *testing.T) {
	c := sseServer(t, func(w http.ResponseWriter, flush func()) {
		for i := 0; i < 5; i++ {
			_, _ = io.WriteString(w, chunk("thinking... "))
			flush()
			time.Sleep(60 * time.Millisecond)
		}
		_, _ = io.WriteString(w, chunk("VERDICT: PASS"))
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		flush()
	})

	// Every gap is well under the idle bound; the total run is well over it.
	got, err := c.CompleteStreaming(context.Background(), CompleteRequest{IdleTimeout: 150 * time.Millisecond})
	if err != nil {
		t.Fatalf("CompleteStreaming: %v; a progressing stream was killed", err)
	}
	if !strings.Contains(got.Text, "VERDICT: PASS") {
		t.Errorf("text = %q", got.Text)
	}
}

// Silence, not slowness, is the failure.
func TestAStreamThatGoesQuietFails(t *testing.T) {
	c := sseServer(t, func(w http.ResponseWriter, flush func()) {
		_, _ = io.WriteString(w, chunk("starting"))
		flush()
		time.Sleep(400 * time.Millisecond)
	})

	_, err := c.CompleteStreaming(context.Background(), CompleteRequest{IdleTimeout: 80 * time.Millisecond})
	if err == nil {
		t.Fatal("a silent stream was accepted")
	}
	if !strings.Contains(err.Error(), "sent nothing") {
		t.Errorf("error = %v, want it to name the silence", err)
	}
}

// A caller must not be able to tell the two paths apart by their errors.
func TestAStreamedBudgetExhaustionIsTheSameErrorAsABufferedOne(t *testing.T) {
	c := sseServer(t, func(w http.ResponseWriter, flush func()) {
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":""},"finish_reason":"length"}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"usage":{"completion_tokens":8192,"completion_tokens_details":{"reasoning_tokens":8192}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		flush()
	})

	_, err := c.CompleteStreaming(context.Background(), CompleteRequest{MaxTokens: 8192})
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("error = %v, want ErrBudgetExhausted", err)
	}
	if !strings.Contains(err.Error(), "8192 reasoning tokens") {
		t.Errorf("error = %v, want it to name the number to raise", err)
	}
}

// A backend outage arrives as a normal-looking stream. §12.1 exists to stop
// that being recorded as a model answer.
func TestAStreamedProviderFailureIsNotReadAsAnAnswer(t *testing.T) {
	c := sseServer(t, func(w http.ResponseWriter, flush func()) {
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"HTTP 400: model not supported"},"finish_reason":"error"}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		flush()
	})

	_, err := c.CompleteStreaming(context.Background(), CompleteRequest{})
	if !errors.Is(err, ErrProviderFailure) {
		t.Fatalf("error = %v, want ErrProviderFailure", err)
	}
}

// Not every OpenAI-compatible gateway honours "stream". One that quietly
// ignores it returns a perfectly good buffered answer, and discarding that as
// unparseable would be the worse failure.
func TestAGatewayThatIgnoresStreamStillWorks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"m","choices":[{"message":{"content":"VERDICT: PASS"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`)
	}))
	t.Cleanup(srv.Close)

	c, err := New(srv.URL, "", "m")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := c.CompleteStreaming(context.Background(), CompleteRequest{})
	if err != nil {
		t.Fatalf("CompleteStreaming against a non-streaming gateway: %v", err)
	}
	if got.Text != "VERDICT: PASS" {
		t.Errorf("text = %q", got.Text)
	}
}

// A stream that ends without [DONE] has still delivered its answer.
func TestAStreamEndingWithoutDoneStillYieldsItsAnswer(t *testing.T) {
	c := sseServer(t, func(w http.ResponseWriter, flush func()) {
		_, _ = io.WriteString(w, chunk("VERDICT: PASS"))
		flush()
	})

	got, err := c.CompleteStreaming(context.Background(), CompleteRequest{})
	if err != nil {
		t.Fatalf("CompleteStreaming: %v", err)
	}
	if got.Text != "VERDICT: PASS" {
		t.Errorf("text = %q", got.Text)
	}
}
