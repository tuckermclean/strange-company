// Package hermes talks to a Hermes gateway's session API.
//
// It exists for one job: opening the §10.2 specification conversation that a
// human continues in the Hermes dashboard. Creating a session costs no model
// call -- POST /api/sessions returns 201 without one -- so the control plane
// can hand a card off without spending anything to do it.
//
// Everything here is verified against a live gateway; see
// docs/reference/hermes-integration-notes.md.
package hermes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultTimeout = 30 * time.Second

// maxErrorBodyBytes bounds how much of a failing response is quoted back.
const maxErrorBodyBytes = 512

var (
	// ErrNoBaseURL is returned when no gateway URL was configured.
	ErrNoBaseURL = errors.New("hermes: base URL is required")

	// ErrHTTP is returned for any non-2xx response.
	ErrHTTP = errors.New("hermes: non-2xx response")

	// ErrIncompleteSession is returned when the gateway accepts a create
	// request but returns no session id.
	//
	// This is not defensive: the gateway returns 201 for bodies it did not
	// understand, so a missing id is a real outcome. A Session with an
	// empty ID would later read as "no conversation was ever started".
	ErrIncompleteSession = errors.New("hermes: gateway returned a session with no id")
)

// Client is a Hermes gateway client.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient overrides the HTTP client, for tests and for callers that
// need their own transport.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) {
		if h != nil {
			c.httpClient = h
		}
	}
}

// New builds a Client against a gateway base URL. The key is the gateway's
// API_SERVER_KEY, sent as a bearer token.
func New(baseURL, apiKey string, opts ...Option) (*Client, error) {
	baseURL = strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, ErrNoBaseURL
	}
	if _, err := url.Parse(baseURL); err != nil {
		return nil, fmt.Errorf("hermes: invalid base URL: %w", err)
	}

	c := &Client{
		baseURL:    baseURL,
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: defaultTimeout},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// SpecSession is the conversation to open.
//
// There is deliberately no Profile field. A "profile" key in the create body
// is accepted and silently ignored, and the real mechanism -- a /p/<name>/ URL
// prefix -- is served by the default profile whenever multiplexing is off, so
// a typo and a correct pin are indistinguishable. Model and SystemPrompt are
// set on the session instead, which is observable in the response.
type SpecSession struct {
	// Title is what a human sees in the dashboard session list. It is how
	// they find the conversation, so it is required.
	Title string

	// Model pins the model for this session. Required: an unpinned session
	// inherits the gateway's default, which on a live deployment was a
	// model its own backend refused.
	Model string

	// SystemPrompt seeds the conversation with the card's context.
	SystemPrompt string
}

func (s SpecSession) validate() error {
	var missing []string
	if strings.TrimSpace(s.Title) == "" {
		missing = append(missing, "title")
	}
	if strings.TrimSpace(s.Model) == "" {
		missing = append(missing, "model")
	}
	if strings.TrimSpace(s.SystemPrompt) == "" {
		missing = append(missing, "system prompt")
	}
	if len(missing) > 0 {
		return fmt.Errorf("hermes: specification session needs a %s", strings.Join(missing, " and a "))
	}
	return nil
}

// Session is the subset of a created session this package uses. The gateway
// returns considerably more; the rest is ignored, not rejected.
type Session struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Model  string `json:"model"`
	Title  string `json:"title"`
}

// createRequest is the wire body. Its fields are exactly what the gateway was
// observed to honour -- title, model and system_prompt all come back reflected
// on the created session.
type createRequest struct {
	Title        string `json:"title"`
	Model        string `json:"model"`
	SystemPrompt string `json:"system_prompt"`
}

type createResponse struct {
	Session Session `json:"session"`
}

// CreateSession opens a specification conversation and returns it. No model
// call is made, and nothing is charged.
func (c *Client) CreateSession(ctx context.Context, req SpecSession) (*Session, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}

	body, err := json.Marshal(createRequest{
		Title:        req.Title,
		Model:        req.Model,
		SystemPrompt: req.SystemPrompt,
	})
	if err != nil {
		return nil, fmt.Errorf("hermes: encoding request: %w", err)
	}

	respBody, err := c.do(ctx, http.MethodPost, "/api/sessions", body)
	if err != nil {
		return nil, err
	}

	var parsed createResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("hermes: decoding response: %w", err)
	}
	if strings.TrimSpace(parsed.Session.ID) == "" {
		return nil, ErrIncompleteSession
	}
	return &parsed.Session, nil
}

// DeleteSession removes a session, so a conversation opened for a card that
// then went nowhere does not accumulate in someone's dashboard.
func (c *Client) DeleteSession(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("hermes: session id is required")
	}
	_, err := c.do(ctx, http.MethodDelete, "/api/sessions/"+url.PathEscape(id), nil)
	return err
}

func (c *Client) do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("hermes: building request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hermes: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return nil, fmt.Errorf("%w: %s %s: status %d: %s",
			ErrHTTP, method, path, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	return io.ReadAll(resp.Body)
}
