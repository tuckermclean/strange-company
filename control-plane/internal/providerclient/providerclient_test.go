package providerclient_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuckermclean/strange-company/control-plane/internal/credentials"
	"github.com/tuckermclean/strange-company/control-plane/internal/modelclient"
	"github.com/tuckermclean/strange-company/control-plane/internal/policy"
	"github.com/tuckermclean/strange-company/control-plane/internal/providerclient"
)

const apiKey = "sk-deepseek-not-in-any-error"

func credsDir(t *testing.T) credentials.Dir {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deepseek-credentials"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "deepseek-credentials", "api-key"), []byte(apiKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return credentials.Dir(root)
}

func resolution(baseURL string) *policy.Resolution {
	return &policy.Resolution{
		Phase:        "foreman",
		Alias:        "foreman-cheap",
		ProviderName: "deepseek",
		Model:        "deepseek-v4-flash",
		Harness:      policy.Harness("hermes"),
		BaseURL:      baseURL,
		Env: map[string]policy.CredentialRef{
			"DEEPSEEK_API_KEY": {Secret: "deepseek-credentials", Key: "api-key"},
		},
	}
}

// The whole point: the request must reach the provider policy named, carrying
// that provider's credential -- not a gateway that will substitute its own
// global route for both.
func TestTheClientCallsTheProviderPolicyNamed(t *testing.T) {
	var gotAuth, gotModel, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotModel = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()

	c, err := providerclient.New(resolution(srv.URL), credsDir(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Complete(context.Background(), modelclient.CompleteRequest{
		Messages: []modelclient.Message{{Role: "user", Content: "hi"}}, MaxTokens: 8,
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if gotAuth != "Bearer "+apiKey {
		t.Errorf("Authorization = %q; the provider's own credential was not sent", gotAuth)
	}
	if !strings.Contains(gotModel, `"deepseek-v4-flash"`) {
		t.Errorf("request body does not carry the policy model: %s", gotModel)
	}
	if gotPath != "/chat/completions" {
		t.Errorf("path = %q", gotPath)
	}
}

// A provider with no baseUrl cannot be called in-process. Falling back to any
// other endpoint is precisely the failure this change removes, so it must be
// an error -- and one that says which provider and which phase.
func TestAProviderWithNoBaseURLIsRefused(t *testing.T) {
	_, err := providerclient.New(resolution(""), credsDir(t))
	if !errors.Is(err, providerclient.ErrNoBaseURL) {
		t.Fatalf("error = %v, want ErrNoBaseURL", err)
	}
	for _, want := range []string{"deepseek", "foreman"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %q: %v", want, err)
		}
	}
}

func TestAMissingCredentialIsRefusedAndNamed(t *testing.T) {
	res := resolution("https://api.deepseek.com")
	res.Env = map[string]policy.CredentialRef{
		"DEEPSEEK_API_KEY": {Secret: "absent-credentials", Key: "api-key"},
	}

	_, err := providerclient.New(res, credsDir(t))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"DEEPSEEK_API_KEY", "absent-credentials", "deepseek"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Errorf("error leaks a credential: %v", err)
	}
}

// providers.yaml's ollama entry has no `env` at all, deliberately: a local
// endpoint that needs no credential is a valid provider, not a broken one.
func TestACredentialFreeProviderIsAllowed(t *testing.T) {
	var sawAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawAuth = r.Header["Authorization"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()

	res := resolution(srv.URL)
	res.ProviderName = "ollama"
	res.Env = nil

	c, err := providerclient.New(res, credsDir(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Complete(context.Background(), modelclient.CompleteRequest{
		Messages: []modelclient.Message{{Role: "user", Content: "hi"}}, MaxTokens: 8,
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if sawAuth {
		t.Error("sent an Authorization header for a credential-free provider")
	}
}

// Two credentials give no way to know which is the bearer token. Picking one
// by map order would work until the day it silently picked the other.
func TestAmbiguousCredentialsAreRefused(t *testing.T) {
	res := resolution("https://api.example.com")
	res.Env = map[string]policy.CredentialRef{
		"DEEPSEEK_API_KEY": {Secret: "deepseek-credentials", Key: "api-key"},
		"OTHER_API_KEY":    {Secret: "deepseek-credentials", Key: "api-key"},
	}

	_, err := providerclient.New(res, credsDir(t))
	if !errors.Is(err, providerclient.ErrAmbiguousCredential) {
		t.Fatalf("error = %v, want ErrAmbiguousCredential", err)
	}
}

func TestANilResolutionIsRefused(t *testing.T) {
	if _, err := providerclient.New(nil, credsDir(t)); err == nil {
		t.Fatal("expected an error")
	}
}
