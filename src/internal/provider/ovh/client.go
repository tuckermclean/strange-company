package ovh

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// maxBody bounds every API response read.
const maxBody = 4 << 20

// ClientConfig configures the signed read-only API client; Endpoint is the API root, for example
// https://eu.api.ovh.com/1.0.
type ClientConfig struct {
	Endpoint    string
	Credentials Credentials
	HTTPClient  *http.Client
}

// Client is the signed OVH API client; its timestamp comes from GET /auth/time, never from the wall clock.
// It only issues GET requests.
type Client struct {
	endpoint string
	creds    Credentials
	hc       *http.Client
}

// NewClient returns a client for cfg.
func NewClient(cfg ClientConfig) *Client {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{endpoint: strings.TrimRight(cfg.Endpoint, "/"), creds: cfg.Credentials, hc: hc}
}

func (c *Client) do(ctx context.Context, url string, h http.Header, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	for k, v := range h {
		req.Header[k] = v
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("ovh: GET %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("ovh: GET %s: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ovh: GET %s: status %d", url, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("ovh: GET %s: decode: %w", url, err)
	}
	return nil
}

// Get reads path (with its query) and decodes the JSON response into out. The signature covers the exact URL sent.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	var ts int64
	if err := c.do(ctx, c.endpoint+"/auth/time", nil, &ts); err != nil {
		return err
	}
	url := c.endpoint + path
	return c.do(ctx, url, SignedHeaders(c.creds, http.MethodGet, url, "", ts), out)
}
