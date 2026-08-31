package github

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// App authenticates as a GitHub App and mints installation access tokens.
//
// §29 says "GitHub App credentials" and always did; this system drifted to
// personal access tokens, and the difference matters most at the point where a
// credential is handed to an agent. A PAT is long-lived, carries whatever scope
// its owner granted, and reaches every repository that owner can reach. An
// installation token expires in an hour, is scoped to the repositories the App
// is installed on, and carries only the permissions the App was granted.
//
// So the blast radius of the credential a coding Job holds stops being "every
// repository you own" and becomes "this repository, for the next hour".
type App struct {
	id         string
	key        *rsa.PrivateKey
	baseURL    string
	httpClient *http.Client

	mu     sync.Mutex
	tokens map[string]*installationToken // repository -> token
	installs map[string]int64            // repository -> installation id
}

type installationToken struct {
	Token     string
	ExpiresAt time.Time
}

// ErrNoAppCredentials reports that no App is configured.
var ErrNoAppCredentials = errors.New("github: no App id and private key configured")

// NewApp builds an App from its numeric id and PEM-encoded private key.
func NewApp(baseURL, appID string, privateKeyPEM []byte, h *http.Client) (*App, error) {
	if strings.TrimSpace(appID) == "" || len(privateKeyPEM) == 0 {
		return nil, ErrNoAppCredentials
	}
	key, err := parsePrivateKey(privateKeyPEM)
	if err != nil {
		return nil, err
	}
	if h == nil {
		h = &http.Client{Timeout: defaultTimeout}
	}
	return &App{
		id: strings.TrimSpace(appID), key: key,
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: h,
		tokens:     map[string]*installationToken{},
		installs:   map[string]int64{},
	}, nil
}

// parsePrivateKey accepts the PKCS#1 GitHub hands out and the PKCS#8 some
// tooling converts it to, because an operator pasting a key should not have to
// know which one they have.
func parsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("github: the App private key is not PEM-encoded")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("github: parsing the App private key: %w", err)
	}
	k, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("github: the App private key is not RSA")
	}
	return k, nil
}

// jwt mints the short-lived assertion that authenticates as the App itself.
//
// Hand-rolled rather than pulling in a JWT library: this is one signature over
// two base64 segments, and the alternative is a dependency in a module that has
// three.
//
// The issued-at is backdated a minute because GitHub rejects a token issued in
// its future, and clocks disagree.
func (a *App) jwt(now time.Time) (string, error) {
	header := base64URL([]byte(`{"alg":"RS256","typ":"JWT"}`))

	claims, err := json.Marshal(map[string]any{
		"iat": now.Add(-time.Minute).Unix(),
		// GitHub refuses anything longer than ten minutes.
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": a.id,
	})
	if err != nil {
		return "", fmt.Errorf("github: encoding App claims: %w", err)
	}

	signing := header + "." + base64URL(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("github: signing the App assertion: %w", err)
	}
	return signing + "." + base64URL(sig), nil
}

func base64URL(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// TokenFor returns an installation token scoped to one repository, minting a
// new one when the cached one is close to expiry.
//
// Refreshed early rather than on expiry: a token that dies mid-clone leaves a
// coding Job with a checkout it cannot push, which surfaces as the agent having
// failed rather than as a credential having aged out.
func (a *App) TokenFor(ctx context.Context, repository string) (string, error) {
	a.mu.Lock()
	if t, ok := a.tokens[repository]; ok && time.Until(t.ExpiresAt) > 10*time.Minute {
		a.mu.Unlock()
		return t.Token, nil
	}
	a.mu.Unlock()

	install, err := a.installationFor(ctx, repository)
	if err != nil {
		return "", err
	}

	_, name, err := splitRepository(repository)
	if err != nil {
		return "", err
	}

	tok, err := a.mintToken(ctx, install, name)
	if err != nil {
		return "", err
	}

	a.mu.Lock()
	a.tokens[repository] = tok
	a.mu.Unlock()
	return tok.Token, nil
}

// installationFor finds which installation covers a repository.
func (a *App) installationFor(ctx context.Context, repository string) (int64, error) {
	a.mu.Lock()
	if id, ok := a.installs[repository]; ok {
		a.mu.Unlock()
		return id, nil
	}
	a.mu.Unlock()

	owner, name, err := splitRepository(repository)
	if err != nil {
		return 0, err
	}

	body, err := a.appRequest(ctx, http.MethodGet,
		fmt.Sprintf("/repos/%s/%s/installation", url.PathEscape(owner), url.PathEscape(name)), nil)
	if err != nil {
		return 0, fmt.Errorf("github: finding the App installation on %s (is the App installed there?): %w", repository, err)
	}

	var out struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, fmt.Errorf("github: decoding the installation: %w", err)
	}
	if out.ID == 0 {
		return 0, fmt.Errorf("github: no installation of this App on %s", repository)
	}

	a.mu.Lock()
	a.installs[repository] = out.ID
	a.mu.Unlock()
	return out.ID, nil
}

// mintToken asks for a token narrowed to one repository.
//
// The narrowing is the point. An App installed on several repositories issues
// a token for all of them by default, and a coding Job working on one has no
// business holding a credential for the others.
func (a *App) mintToken(ctx context.Context, installation int64, repo string) (*installationToken, error) {
	payload, err := json.Marshal(map[string]any{"repositories": []string{repo}})
	if err != nil {
		return nil, fmt.Errorf("github: encoding the token request: %w", err)
	}

	body, err := a.appRequest(ctx, http.MethodPost,
		fmt.Sprintf("/app/installations/%d/access_tokens", installation), payload)
	if err != nil {
		return nil, fmt.Errorf("github: minting an installation token: %w", err)
	}

	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("github: decoding the installation token: %w", err)
	}
	if out.Token == "" {
		return nil, errors.New("github: the installation token response carried no token")
	}
	if out.ExpiresAt.IsZero() {
		// Believe an hour rather than treating it as immortal: a token with
		// no known expiry that is cached forever is a long-lived credential
		// wearing a short-lived name.
		out.ExpiresAt = time.Now().Add(time.Hour)
	}
	return &installationToken{Token: out.Token, ExpiresAt: out.ExpiresAt}, nil
}

// appRequest calls the API authenticated as the App itself.
func (a *App) appRequest(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	assertion, err := a.jwt(time.Now())
	if err != nil {
		return nil, err
	}
	return doGitHub(ctx, a.httpClient, method, a.baseURL+path, "Bearer "+assertion, body)
}

// doGitHub performs one authenticated API call.
//
// Shared by the App (authenticating as itself with a signed assertion) and
// available to anything else that needs a raw call, so the API version header
// and the error shape are written once rather than per caller.
func doGitHub(ctx context.Context, h *http.Client, method, url, authorization string, body []byte) ([]byte, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, fmt.Errorf("github: building request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := h.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// The body is read but the credential never is: an App assertion in
		// an error message would outlive the log line it landed in.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return nil, fmt.Errorf("github: %s: status %d: %s",
			method, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return io.ReadAll(resp.Body)
}
