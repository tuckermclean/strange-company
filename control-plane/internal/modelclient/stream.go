package modelclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// defaultIdleTimeout bounds the gap BETWEEN chunks of a streamed response, not
// the response as a whole.
//
// That distinction is the entire reason streaming is here. A reasoning model
// reading a large diff legitimately takes many minutes, and a deadline on the
// whole call has to be guessed against the largest diff anyone will ever
// submit -- guess low and every big card fails, guess high and a genuinely
// hung provider holds a worker for that long. A gap between chunks needs no
// such guess: a model that is working emits, and one that has stopped does not.
const defaultIdleTimeout = 90 * time.Second

// streamChunk is one server-sent event from an OpenAI-compatible stream.
type streamChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		TotalTokens         int `json:"total_tokens"`
		CompletionTokensDet struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
}

// CompleteStreaming sends one chat-completion request with streaming on, and
// assembles the chunks into the same Completion a buffered call returns.
//
// Callers get identical semantics either way -- including ErrBudgetExhausted
// and ErrProviderFailure -- so a step can switch to streaming without learning
// a new failure vocabulary.
//
// Falls back to reading the body as an ordinary completion when the server
// does not actually stream. Not every OpenAI-compatible gateway honours
// "stream", and one that quietly ignores it returns a perfectly good buffered
// answer that would otherwise be discarded as unparseable.
func (c *Client) CompleteStreaming(ctx context.Context, req CompleteRequest) (*Completion, error) {
	wireReq := wireRequest{
		Model:       c.model,
		Messages:    req.Messages,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stream:      true,
		StreamOptions: &streamOptions{
			// Without this an OpenAI-compatible stream reports no usage at
			// all, and §22's ledger would go blind exactly where the most
			// expensive calls are made.
			IncludeUsage: true,
		},
	}
	body, err := json.Marshal(wireReq)
	if err != nil {
		return nil, fmt.Errorf("modelclient: encoding the request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("modelclient: building the request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("modelclient: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return nil, fmt.Errorf("%w: status %d: %s", ErrHTTP, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		// The gateway ignored "stream" and answered normally. Not every
		// OpenAI-compatible server honours it, and discarding a perfectly
		// good buffered answer as unparseable would be the worse failure.
		buffered, rerr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes))
		if rerr != nil {
			return nil, fmt.Errorf("modelclient: reading response: %w", rerr)
		}
		return c.parseBuffered(buffered, req)
	}

	return readStream(ctx, resp.Body, req)
}

// event is one decoded chunk, or the error that ended the stream.
type event struct {
	chunk streamChunk
	err   error
	done  bool
}

// readStream assembles a streamed completion, failing only on a silence longer
// than the caller's idle timeout.
func readStream(ctx context.Context, body io.Reader, req CompleteRequest) (*Completion, error) {
	idle := req.IdleTimeout
	if idle <= 0 {
		idle = defaultIdleTimeout
	}

	events := make(chan event, 16)
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 0, 64<<10), maxResponseBodyBytes)

		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "[DONE]" {
				events <- event{done: true}
				return
			}
			var ch streamChunk
			if err := json.Unmarshal([]byte(payload), &ch); err != nil {
				// A single malformed frame is not a malformed stream.
				continue
			}
			events <- event{chunk: ch}
		}
		if err := scanner.Err(); err != nil {
			events <- event{err: err}
		}
	}()

	var (
		text         strings.Builder
		model        string
		usage        Usage
		reasoning    int
		finishReason string
		sawAnyChunk  bool
	)

	timer := time.NewTimer(idle)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("modelclient: %w", ctx.Err())

		case <-timer.C:
			// Silence, not slowness. A model that is working emits.
			return nil, fmt.Errorf("modelclient: the model sent nothing for %s", idle)

		case ev, ok := <-events:
			if !ok {
				// The stream ended without [DONE]; assemble what arrived
				// rather than discard a complete-looking answer.
				return assemble(text.String(), model, usage, reasoning, finishReason, sawAnyChunk, req)
			}
			if ev.err != nil {
				return nil, fmt.Errorf("modelclient: reading the stream: %w", ev.err)
			}
			if ev.done {
				return assemble(text.String(), model, usage, reasoning, finishReason, sawAnyChunk, req)
			}

			sawAnyChunk = true
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(idle)

			if ev.chunk.Model != "" {
				model = ev.chunk.Model
			}
			for _, choice := range ev.chunk.Choices {
				text.WriteString(choice.Delta.Content)
				if choice.FinishReason != "" {
					finishReason = choice.FinishReason
				}
			}
			if u := ev.chunk.Usage; u != nil {
				usage = Usage{
					PromptTokens:     u.PromptTokens,
					CompletionTokens: u.CompletionTokens,
					TotalTokens:      u.TotalTokens,
				}
				reasoning = u.CompletionTokensDet.ReasoningTokens
			}
		}
	}
}

// assemble applies the same verdict rules a buffered response gets, so a
// caller cannot tell the two paths apart by their errors.
func assemble(text, model string, usage Usage, reasoning int, finishReason string, sawAnyChunk bool, req CompleteRequest) (*Completion, error) {
	if !sawAnyChunk {
		return nil, ErrEmptyResponse
	}

	if strings.TrimSpace(text) == "" {
		// Checked before ErrEmptyResponse for the same reason as the
		// buffered path: both have no content, and only this one tells an
		// operator which number to raise.
		if finishReason == finishReasonLength {
			return nil, fmt.Errorf("%w (max_tokens %d; the model produced %d reasoning tokens and no answer)",
				ErrBudgetExhausted, req.MaxTokens, reasoning)
		}
		if finishReason == finishReasonError {
			return nil, fmt.Errorf("%w: no cause reported", ErrProviderFailure)
		}
		return nil, ErrEmptyResponse
	}

	// A backend outage arrives as a normal-looking stream whose finish
	// reason is "error" and whose content is the error text. §12.1 exists to
	// stop that being recorded as a model answer.
	if finishReason == finishReasonError {
		return nil, fmt.Errorf("%w: %s", ErrProviderFailure, strings.TrimSpace(text))
	}

	return &Completion{Text: text, Model: model, Usage: usage, Raw: []byte(text)}, nil
}
