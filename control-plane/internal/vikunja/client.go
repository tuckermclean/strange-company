// Package vikunja provides a thin client for the parts of the Vikunja REST
// API the control plane needs, plus the first-boot logic that provisions its
// own long-lived API token.
package vikunja

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// requestTimeout bounds every individual HTTP call made by Client, so a
// hung Vikunja instance cannot wedge the bootstrap sequence forever.
const requestTimeout = 15 * time.Second

// maxErrorBodyBytes caps how much of a non-2xx response body is captured
// into a RequestError, keeping logs bounded while still leaving the failure
// debuggable.
const maxErrorBodyBytes = 512

// maxResponseBodyBytes caps how much of any response body (success or
// failure) this client will read into memory, as a defensive limit against
// a misbehaving or malicious server. It is far larger than any JSON payload
// this client expects to decode.
const maxResponseBodyBytes = 10 << 20 // 10 MiB

// Client is a minimal HTTP client for the Vikunja REST API. It is safe to
// construct multiple instances (for example one per token) since it holds
// no mutable state beyond its configuration.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New returns a Client that talks to the Vikunja instance at baseURL. token
// may be empty for calls that do not require authentication (Register,
// Login, Info). If c is nil, http.DefaultClient is used.
func New(baseURL, token string, c *http.Client) *Client {
	if c == nil {
		c = http.DefaultClient
	}
	return &Client{baseURL: baseURL, token: token, http: c}
}

// RequestError is returned when Vikunja responds with a non-2xx status. It
// carries the HTTP method, path and status code plus a truncated response
// body for debugging. It never contains the request's bearer token.
type RequestError struct {
	Method string
	Path   string
	Status int
	Body   string
}

func (e *RequestError) Error() string {
	return fmt.Sprintf("vikunja: %s %s: status %d: %s", e.Method, e.Path, e.Status, e.Body)
}

// do performs an HTTP request against path with the given method and JSON
// body (nil for none), decoding a successful JSON response into out (which
// may be nil to discard the body). It sets the Authorization header when the
// client has a token, and enforces requestTimeout on the call.
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	var reqBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("vikunja: encode request body for %s %s: %w", method, path, err)
		}
		reqBody = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return fmt.Errorf("vikunja: build request for %s %s: %w", method, path, err)
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("vikunja: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes))
	if readErr != nil {
		return fmt.Errorf("vikunja: %s %s: read response body: %w", method, path, readErr)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		truncated := respBody
		if len(truncated) > maxErrorBodyBytes {
			truncated = truncated[:maxErrorBodyBytes]
		}
		return &RequestError{
			Method: method,
			Path:   path,
			Status: resp.StatusCode,
			Body:   string(truncated),
		}
	}

	if out == nil || len(respBody) == 0 {
		return nil
	}

	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("vikunja: %s %s: decode response body: %w", method, path, err)
	}

	return nil
}

// Info calls GET /api/v1/info. It is used as a liveness and token-validity
// probe: a stored token that can no longer authenticate will fail this call
// with a RequestError carrying a 401/403 status.
func (c *Client) Info(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/api/v1/info", nil, nil)
}

// loginResponse mirrors the subset of Vikunja's /api/v1/login response this
// client cares about.
type loginResponse struct {
	Token string `json:"token"`
}

// Login calls POST /api/v1/login and returns the short-lived JWT session
// token Vikunja issues for the given credentials. Callers must not persist
// this token; it exists only to be exchanged for a long-lived API token via
// CreateToken.
func (c *Client) Login(ctx context.Context, username, password string) (string, error) {
	req := struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}{Username: username, Password: password}

	var resp loginResponse
	if err := c.do(ctx, http.MethodPost, "/api/v1/login", req, &resp); err != nil {
		return "", err
	}
	return resp.Token, nil
}

// Register calls POST /api/v1/register to create a new Vikunja user. It is
// only expected to be used once, on the very first boot against a fresh
// Vikunja instance.
func (c *Client) Register(ctx context.Context, username, email, password string) error {
	req := struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}{Username: username, Email: email, Password: password}

	return c.do(ctx, http.MethodPost, "/api/v1/register", req, nil)
}

// Routes calls GET /api/v1/routes, which returns the permission groups this
// Vikunja server actually exposes (and, within each group, its available
// actions). The exact shape of each group's value is opaque to this client;
// only the top-level group names are used, to intersect the permissions we
// request in CreateToken with what the server supports.
func (c *Client) Routes(ctx context.Context) (map[string]map[string]any, error) {
	var resp map[string]map[string]any
	if err := c.do(ctx, http.MethodGet, "/api/v1/routes", nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// createTokenResponse mirrors the subset of Vikunja's PUT /api/v1/tokens
// response this client cares about.
type createTokenResponse struct {
	Token string `json:"token"`
}

// tokenLifetime is how long a minted API token is valid for.
const tokenLifetime = 10 * 365 * 24 * time.Hour

// CreateToken calls PUT /api/v1/tokens to mint a long-lived API token titled
// title, owned by ownerID, scoped to perms. perms must already be limited to
// the permission groups returned by Routes.
func (c *Client) CreateToken(ctx context.Context, title string, ownerID int64, perms map[string]any) (string, error) {
	// expires_at is marked valid:"required" upstream, so a token minted
	// without it is rejected. This is a service credential with no human to
	// rotate it, so it is deliberately long-lived; revoke it in Vikunja to
	// force a re-mint on the next boot.
	req := struct {
		Title       string         `json:"title"`
		OwnerID     int64          `json:"owner_id"`
		Permissions map[string]any `json:"permissions"`
		ExpiresAt   time.Time      `json:"expires_at"`
	}{
		Title:       title,
		OwnerID:     ownerID,
		Permissions: perms,
		ExpiresAt:   time.Now().Add(tokenLifetime),
	}

	var resp createTokenResponse
	if err := c.do(ctx, http.MethodPut, "/api/v1/tokens", req, &resp); err != nil {
		return "", err
	}
	return resp.Token, nil
}

// currentUserResponse mirrors the subset of Vikunja's GET /api/v1/user
// response this client cares about.
type currentUserResponse struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

// CurrentUser calls GET /api/v1/user, returning the id and username of the
// user identified by the client's current token. It doubles as a
// token-validity probe.
func (c *Client) CurrentUser(ctx context.Context) (id int64, username string, err error) {
	var resp currentUserResponse
	if err := c.do(ctx, http.MethodGet, "/api/v1/user", nil, &resp); err != nil {
		return 0, "", err
	}
	return resp.ID, resp.Username, nil
}
