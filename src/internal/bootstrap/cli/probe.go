package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// NewModelKeyProbe builds the production PF-19 probe from the policy: GET <BaseURL>/models with the key in the header
// that AuthScheme names. It refuses a non-https BaseURL without sending and never follows a redirect (the caller's
// client is copied, not modified). The /models endpoint is not defined by plan 53: (verify).
func NewModelKeyProbe(client *http.Client, targets map[string]ProbeTarget) func(provider, key string) error {
	c := http.Client{}
	if client != nil {
		c = *client
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return func(provider, key string) error {
		t, ok := targets[provider]
		if !ok {
			return fmt.Errorf("provider %s is not in the policy: no probe endpoint", provider)
		}
		u, err := url.Parse(t.BaseURL)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("provider %s: upstream.baseUrl must be an https URL", provider)
		}
		u.Path = strings.TrimRight(u.Path, "/") + "/models"
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, u.String(), nil)
		if err != nil {
			return errors.New("cannot build the probe request")
		}
		switch t.AuthScheme {
		case "x-api-key":
			req.Header.Set("x-api-key", key)
			req.Header.Set("anthropic-version", "2023-06-01")
		case "bearer":
			req.Header.Set("Authorization", "Bearer "+key)
		case "x-goog-api-key":
			req.Header.Set("x-goog-api-key", key)
		default:
			return fmt.Errorf("provider %s: unknown credential.authScheme %q", provider, t.AuthScheme)
		}
		resp, err := c.Do(req)
		if err != nil {
			return errors.New("request failed: " + strings.ReplaceAll(err.Error(), key, "[redacted]"))
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return nil
	}
}

// ProbeTarget is the policy view of one provider for NewModelKeyProbe.
type ProbeTarget struct {
	BaseURL    string
	AuthScheme string
}
