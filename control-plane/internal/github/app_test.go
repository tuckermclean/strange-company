package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testKeyPEM(t *testing.T) ([]byte, *rsa.PrivateKey) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k),
	}), k
}

// The assertion is hand-rolled, so its signature is verified here rather than
// trusted: a JWT GitHub rejects fails at the first call and says only "401".
func TestTheAppAssertionIsAValidSignedJWT(t *testing.T) {
	pemBytes, key := testKeyPEM(t)
	app, err := NewApp("https://api.github.com", "12345", pemBytes, nil)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}

	now := time.Now()
	token, err := app.jwt(now)
	if err != nil {
		t.Fatalf("jwt: %v", err)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("assertion has %d segments, want 3", len(parts))
	}

	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("signature is not base64url: %v", err)
	}
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("the assertion does not verify against its own key: %v", err)
	}

	var claims map[string]any
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	_ = json.Unmarshal(raw, &claims)

	if claims["iss"] != "12345" {
		t.Errorf("iss = %v, want the App id", claims["iss"])
	}
	// Backdated, because GitHub refuses a token issued in its own future and
	// clocks disagree.
	if iat := int64(claims["iat"].(float64)); iat > now.Unix() {
		t.Errorf("iat is in the future relative to now")
	}
	// GitHub refuses anything longer than ten minutes.
	if exp := int64(claims["exp"].(float64)); exp-now.Unix() > 600 {
		t.Errorf("exp is %d seconds out, past GitHub's ten-minute limit", exp-now.Unix())
	}
}

// An operator pasting a key should not have to know which encoding they have.
func TestBothPrivateKeyEncodingsAreAccepted(t *testing.T) {
	_, key := testKeyPEM(t)

	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})

	if _, err := NewApp("https://api.github.com", "1", encoded, nil); err != nil {
		t.Errorf("a PKCS#8 key was rejected: %v", err)
	}
}

func TestNoCredentialsIsADistinctError(t *testing.T) {
	if _, err := NewApp("https://api.github.com", "", nil, nil); !errors.Is(err, ErrNoAppCredentials) {
		t.Errorf("error = %v, want ErrNoAppCredentials", err)
	}
}

// The narrowing is the whole point: an App installed on several repositories
// issues a token for all of them by default, and a coding Job working on one
// has no business holding a credential for the others.
func TestATokenIsNarrowedToTheOneRepository(t *testing.T) {
	pemBytes, _ := testKeyPEM(t)

	var mintBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/installation"):
			_, _ = io.WriteString(w, `{"id":4242}`)
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &mintBody)
			_, _ = io.WriteString(w, `{"token":"ghs_scoped","expires_at":"2099-01-01T00:00:00Z"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	app, err := NewApp(srv.URL, "1", pemBytes, srv.Client())
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}

	tok, err := app.TokenFor(context.Background(), "tuckermclean/sandbox-derp")
	if err != nil {
		t.Fatalf("TokenFor: %v", err)
	}
	if tok != "ghs_scoped" {
		t.Errorf("token = %q", tok)
	}

	repos, _ := mintBody["repositories"].([]any)
	if len(repos) != 1 || repos[0] != "sandbox-derp" {
		t.Errorf("minted for %v, want only the one repository", mintBody["repositories"])
	}
}

// A token that dies mid-clone leaves a Job with a checkout it cannot push,
// which surfaces as the agent having failed rather than a credential aging out.
func TestATokenNearExpiryIsReminted(t *testing.T) {
	pemBytes, _ := testKeyPEM(t)
	mints := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/installation") {
			_, _ = io.WriteString(w, `{"id":1}`)
			return
		}
		mints++
		// Expires in five minutes: inside the refresh margin.
		exp := time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339)
		_, _ = io.WriteString(w, `{"token":"ghs_x","expires_at":"`+exp+`"}`)
	}))
	t.Cleanup(srv.Close)

	app, _ := NewApp(srv.URL, "1", pemBytes, srv.Client())
	for i := 0; i < 3; i++ {
		if _, err := app.TokenFor(context.Background(), "o/r"); err != nil {
			t.Fatalf("TokenFor: %v", err)
		}
	}
	if mints != 3 {
		t.Errorf("minted %d times; a token inside the refresh margin was reused", mints)
	}
}

// And one comfortably in the future is reused, or every call would mint.
func TestAFreshTokenIsReused(t *testing.T) {
	pemBytes, _ := testKeyPEM(t)
	mints := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/installation") {
			_, _ = io.WriteString(w, `{"id":1}`)
			return
		}
		mints++
		exp := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		_, _ = io.WriteString(w, `{"token":"ghs_x","expires_at":"`+exp+`"}`)
	}))
	t.Cleanup(srv.Close)

	app, _ := NewApp(srv.URL, "1", pemBytes, srv.Client())
	for i := 0; i < 3; i++ {
		_, _ = app.TokenFor(context.Background(), "o/r")
	}
	if mints != 1 {
		t.Errorf("minted %d times, want 1", mints)
	}
}
