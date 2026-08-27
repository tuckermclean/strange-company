// Package modelclient implements a minimal, provider-agnostic client for
// the OpenAI-compatible /chat/completions HTTP shape. That shape is not
// one vendor's protocol: DeepSeek, Ollama, vLLM, llama.cpp, Together, Groq
// and many other self-hosted or cloud inference servers all implement it,
// which is exactly why this package is useful for the control plane's
// cheap ambiguity-screening call (docs/specs/strange-company-control-plane-v1.md
// §10.1) — it can point at whichever screening provider policy names today
// without a Go change tomorrow (policy §2.3, §2.5).
//
// This package never names a vendor and contains no "if provider == ..."
// branch: a caller's policy.Provider.BaseURL (see
// control-plane/internal/policy/policy.go) is the only signal it acts on.
// That base URL is also this package's one hard requirement. Anthropic's
// Messages API does not speak this shape at all, and the shipped policy's
// anthropic-api / anthropic-oauth providers carry no baseUrl (see
// control-plane/internal/policy/defaults/providers.yaml) — they are simply
// not eligible to be handed to this client. New refuses an empty base URL
// outright, with an error that names the problem, rather than letting a
// caller silently build a malformed OpenAI-shaped request against an
// endpoint that speaks a different protocol entirely.
package modelclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// defaultTimeout bounds a Complete call when the caller's context carries
// no deadline of its own, so a hung or slow provider cannot wedge a caller
// forever.
const defaultTimeout = 60 * time.Second

// maxRawBytes caps how much of a successful response body Completion.Raw
// retains as evidence. A chat-completion response is small JSON; nothing
// legitimate needs more than this.
const maxRawBytes = 64 * 1024

// maxErrorBodyBytes caps how much of a non-2xx response body is folded
// into the returned error. An error page from a misconfigured proxy or
// gateway can be arbitrarily large HTML; the returned error stays small
// regardless of how large the body actually was.
const maxErrorBodyBytes = 512

// finishReasonError is the value an OpenAI-compatible gateway uses to say the
// turn failed. It is not part of the OpenAI specification; it is what Hermes
// returns, verified against a live gateway (docs/reference/hermes-integration-notes.md).
const finishReasonError = "error"

// maxResponseBodyBytes is an outer safety cap on how much of any response
// body this client will ever read into memory, success or failure.
const maxResponseBodyBytes = 10 * 1024 * 1024

// Sentinel errors. Callers should match with errors.Is — every error this
// package returns also wraps one of these plus enough free text to act on
// without reading Go source.
var (
	// ErrNoBaseURL is returned by New when baseURL is empty (or reduces to
	// empty once trailing slashes are trimmed). It is the mechanism that
	// keeps this client from ever being pointed at a provider — such as
	// Anthropic's — that does not speak the OpenAI-compatible shape this
	// package emits.
	ErrNoBaseURL = errors.New("modelclient: base URL is required")

	// ErrEmptyResponse is returned when a 2xx response has no choices, or
	// its first choice has empty message content. A caller must never see
	// a nil error paired with an empty string and mistake that for a real
	// (if vacuous) answer.
	ErrEmptyResponse = errors.New("modelclient: empty response")

	// ErrHTTP is returned when the provider responds with a non-2xx
	// status. The returned error also names the status code and carries a
	// truncated body.
	ErrHTTP = errors.New("modelclient: non-2xx response")

	// ErrProviderFailure is returned when the provider reports a failed
	// turn inside an otherwise successful HTTP response.
	//
	// A Hermes gateway answers a backend failure with HTTP 200, the error
	// text in the assistant message, and finish_reason "error". Returning
	// that content would record an outage as a model answer; spec 12.1
	// then counts it as an implementation attempt and burns a rung of the
	// escalation ladder on a problem no model was ever asked to solve.
	ErrProviderFailure = errors.New("modelclient: provider reported a failed turn")
)

// Client is a minimal OpenAI-compatible /chat/completions client bound to
// one base URL, credential and model.
type Client struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
}

// Option configures a Client constructed by New.
type Option func(*Client)

// WithHTTPClient overrides the *http.Client used for requests. Tests use
// this to point at an httptest.Server; production callers may use it to
// tune transport settings. When not supplied, New uses a plain &http.Client{}.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) {
		c.httpClient = h
	}
}

// New constructs a Client. baseURL must be non-empty: this client speaks
// only the OpenAI-compatible /chat/completions shape, and a provider
// without a base URL (every shipped Anthropic provider, for instance) does
// not speak that shape at all. Handing such a provider's (empty) base URL
// to New fails closed here, naming the problem, rather than letting a
// malformed request go out over the wire.
//
// apiKey may be empty — a local Ollama or vLLM instance needs no
// credential, and Complete only sends an Authorization header when apiKey
// is non-empty, because sending an empty bearer token breaks some servers.
//
// A trailing slash on baseURL is tolerated and trimmed so Complete never
// produces a double slash before "/chat/completions".
func New(baseURL, apiKey, model string, opts ...Option) (*Client, error) {
	trimmed := strings.TrimRight(baseURL, "/")
	if trimmed == "" {
		return nil, fmt.Errorf(
			"%w: the screening provider must expose an OpenAI-compatible endpoint (a non-empty baseUrl in policy) — Anthropic's API is not OpenAI-compatible and cannot be used here",
			ErrNoBaseURL,
		)
	}

	c := &Client{
		baseURL:    trimmed,
		apiKey:     apiKey,
		model:      model,
		httpClient: &http.Client{},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// Message is one OpenAI-compatible chat message.
type Message struct {
	Role    string `json:"role"` // "system" | "user"
	Content string `json:"content"`
}

// CompleteRequest is one chat-completion request.
type CompleteRequest struct {
	Messages    []Message
	MaxTokens   int
	Temperature *float64 // nil means do not send the field
	JSONObject  bool     // request response_format {"type":"json_object"} when supported
}

// Usage reports token accounting from a completion response. A response
// that omits usage entirely yields a zero Usage, not an error.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// Completion is the result of one successful chat-completion call.
type Completion struct {
	Text  string
	Model string
	Usage Usage
	Raw   []byte // response body, capped at maxRawBytes, for evidence
}

// wireRequest is the JSON shape POSTed to {baseURL}/chat/completions.
type wireRequest struct {
	Model          string          `json:"model"`
	Messages       []Message       `json:"messages"`
	MaxTokens      int             `json:"max_tokens"`
	Temperature    *float64        `json:"temperature,omitempty"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

// responseFormat requests structured JSON output on providers that
// support it. It is only ever attached when CompleteRequest.JSONObject is
// true, so providers that don't understand the field simply never see it.
type responseFormat struct {
	Type string `json:"type"`
}

// wireResponse is the subset of the OpenAI-compatible chat-completion
// response shape this client understands. Providers may return additional
// fields; they are ignored, not rejected.
type wireResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		// FinishReason distinguishes a real answer from a failed turn
		// delivered with a 200. Ordinary values ("stop", "length",
		// "tool_calls", "content_filter", or absent) all describe an
		// answer, and whether a truncated one is usable is the caller's
		// decision, not this client's.
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// Complete sends one chat-completion request and returns the first
// choice's text plus usage and raw evidence.
//
// It honours ctx's own deadline/cancellation; when ctx carries no deadline
// at all, Complete applies defaultTimeout so a hung provider cannot wedge
// the caller forever.
func (c *Client) Complete(ctx context.Context, req CompleteRequest) (*Completion, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}

	wireReq := wireRequest{
		Model:       c.model,
		Messages:    req.Messages,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
	}
	if req.JSONObject {
		wireReq.ResponseFormat = &responseFormat{Type: "json_object"}
	}

	body, err := json.Marshal(wireReq)
	if err != nil {
		return nil, fmt.Errorf("modelclient: encoding request: %w", err)
	}

	url := c.baseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("modelclient: building request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("modelclient: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return nil, fmt.Errorf("%w: status %d: %s", ErrHTTP, resp.StatusCode, string(snippet))
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("modelclient: reading response: %w", err)
	}

	var wireResp wireResponse
	if err := json.Unmarshal(respBody, &wireResp); err != nil {
		return nil, fmt.Errorf("modelclient: decoding response: %w", err)
	}

	if len(wireResp.Choices) == 0 {
		return nil, ErrEmptyResponse
	}

	// Checked before the empty-content check on purpose: a failed turn can
	// come back with no content at all, and reporting that as "the provider
	// returned nothing" would send an operator looking for a parsing bug
	// instead of an outage.
	if wireResp.Choices[0].FinishReason == finishReasonError {
		cause := strings.TrimSpace(wireResp.Choices[0].Message.Content)
		if cause == "" {
			cause = "no cause reported"
		}
		return nil, fmt.Errorf("%w: %s", ErrProviderFailure, cause)
	}

	if wireResp.Choices[0].Message.Content == "" {
		return nil, ErrEmptyResponse
	}

	raw := respBody
	if len(raw) > maxRawBytes {
		raw = raw[:maxRawBytes]
	}

	return &Completion{
		Text:  wireResp.Choices[0].Message.Content,
		Model: wireResp.Model,
		Usage: Usage{
			PromptTokens:     wireResp.Usage.PromptTokens,
			CompletionTokens: wireResp.Usage.CompletionTokens,
			TotalTokens:      wireResp.Usage.TotalTokens,
		},
		Raw: raw,
	}, nil
}
