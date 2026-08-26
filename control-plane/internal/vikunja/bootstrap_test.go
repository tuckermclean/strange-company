package vikunja

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// memStore is an in-memory CredentialStore used by these tests in place of
// the real PostgreSQL-backed store.
type memStore struct {
	mu    sync.Mutex
	creds map[string]string
	puts  int
}

func (m *memStore) GetServiceCredential(_ context.Context, name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.creds[name], nil
}

func (m *memStore) PutServiceCredential(_ context.Context, name, secret string, _ map[string]any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.creds == nil {
		m.creds = make(map[string]string)
	}
	m.creds[name] = secret
	m.puts++
	return nil
}

// fakeVikunja is a minimal stand-in for the four Vikunja endpoints the
// bootstrap sequence uses. Every request's "METHOD path" is recorded in
// requests, in arrival order, so tests can assert both which calls happened
// and the order they happened in.
type fakeVikunja struct {
	mu sync.Mutex

	server *httptest.Server

	requests []string

	// users holds accounts that already exist on this fake server, as
	// username -> password. An empty map simulates a fresh instance where
	// the bootstrap account has not been registered yet.
	users map[string]string
	// loginFailStatus is the status returned by /login for a username not
	// present in users.
	loginFailStatus int
	registerCalls   int

	jwt    string
	userID int64

	routes map[string]map[string]any

	// validTokens are bearer tokens that GET /user accepts as already
	// valid, independent of the login flow (used to simulate a
	// previously-minted token stored from an earlier boot).
	validTokens map[string]bool

	mintedToken       string
	createTokenStatus int
	tokenRequests     []tokenPermRequest

	// infoStatus is the status GET /api/v1/info responds with. Defaults to
	// 200; set to a non-2xx status to simulate the endpoint being
	// unreachable or erroring.
	infoStatus int
	// infoLocalEnabled and infoRegistrationEnabled are the auth.local
	// fields reported by GET /api/v1/info. Both default to true, matching
	// a normal, non-SSO Vikunja instance with local registration on.
	infoLocalEnabled        bool
	infoRegistrationEnabled bool
}

type tokenPermRequest struct {
	Title       string         `json:"title"`
	OwnerID     int64          `json:"owner_id"`
	Permissions map[string]any `json:"permissions"`
}

// newFakeVikunja returns a fake server with reasonable defaults: no
// pre-registered users, every desired scope group present in /routes, and a
// successful token mint. Individual tests override the fields they care
// about before making requests.
func newFakeVikunja(t *testing.T) *fakeVikunja {
	t.Helper()

	f := &fakeVikunja{
		users:                   make(map[string]string),
		loginFailStatus:         http.StatusPreconditionFailed,
		jwt:                     "login-jwt-token",
		userID:                  7,
		routes:                  defaultRoutes(),
		validTokens:             make(map[string]bool),
		mintedToken:             "minted-api-token",
		createTokenStatus:       http.StatusOK,
		infoStatus:              http.StatusOK,
		infoLocalEnabled:        true,
		infoRegistrationEnabled: true,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/register", f.handleRegister)
	mux.HandleFunc("/api/v1/login", f.handleLogin)
	mux.HandleFunc("/api/v1/user", f.handleUser)
	mux.HandleFunc("/api/v1/routes", f.handleRoutes)
	mux.HandleFunc("/api/v1/tokens", f.handleTokens)
	mux.HandleFunc("/api/v1/info", f.handleInfo)

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)

	return f
}

func defaultRoutes() map[string]map[string]any {
	action := map[string]any{"path": "/api/v1/x", "method": "GET"}
	routes := make(map[string]map[string]any)
	for _, group := range desiredScopeGroups {
		routes[group] = map[string]any{"read_all": action, "create": action}
	}
	return routes
}

func (f *fakeVikunja) record(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
}

func bearerToken(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeVikunja) handleRegister(w http.ResponseWriter, r *http.Request) {
	f.record(r)

	var req struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	f.mu.Lock()
	f.registerCalls++
	f.users[req.Username] = req.Password
	f.mu.Unlock()

	writeJSON(w, http.StatusCreated, map[string]any{"username": req.Username})
}

func (f *fakeVikunja) handleLogin(w http.ResponseWriter, r *http.Request) {
	f.record(r)

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	f.mu.Lock()
	pass, exists := f.users[req.Username]
	failStatus := f.loginFailStatus
	jwt := f.jwt
	f.mu.Unlock()

	if !exists {
		w.WriteHeader(failStatus)
		return
	}
	if pass != req.Password {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"token": jwt})
}

func (f *fakeVikunja) handleUser(w http.ResponseWriter, r *http.Request) {
	f.record(r)

	token := bearerToken(r)

	f.mu.Lock()
	ok := token != "" && (token == f.jwt || f.validTokens[token] || token == f.mintedToken)
	userID := f.userID
	f.mu.Unlock()

	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"id": userID, "username": "strange-company-bootstrap"})
}

func (f *fakeVikunja) handleRoutes(w http.ResponseWriter, r *http.Request) {
	f.record(r)

	f.mu.Lock()
	routes := f.routes
	f.mu.Unlock()

	writeJSON(w, http.StatusOK, routes)
}

func (f *fakeVikunja) handleTokens(w http.ResponseWriter, r *http.Request) {
	f.record(r)

	var req tokenPermRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	f.mu.Lock()
	f.tokenRequests = append(f.tokenRequests, req)
	status := f.createTokenStatus
	minted := f.mintedToken
	f.mu.Unlock()

	if status != http.StatusOK {
		w.WriteHeader(status)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"token": minted})
}

func (f *fakeVikunja) handleInfo(w http.ResponseWriter, r *http.Request) {
	f.record(r)

	f.mu.Lock()
	status := f.infoStatus
	localEnabled := f.infoLocalEnabled
	registrationEnabled := f.infoRegistrationEnabled
	f.mu.Unlock()

	if status != http.StatusOK {
		w.WriteHeader(status)
		return
	}

	writeJSON(w, status, map[string]any{
		"auth": map[string]any{
			"local": map[string]any{
				"enabled":              localEnabled,
				"registration_enabled": registrationEnabled,
			},
		},
	})
}

func (f *fakeVikunja) writeCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	n := 0
	for _, req := range f.requests {
		if strings.HasPrefix(req, "POST ") || strings.HasPrefix(req, "PUT ") {
			n++
		}
	}
	return n
}

func (f *fakeVikunja) sawRequest(want string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, req := range f.requests {
		if req == want {
			return true
		}
	}
	return false
}

func (f *fakeVikunja) indexOf(want string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, req := range f.requests {
		if req == want {
			return i
		}
	}
	return -1
}

func (f *fakeVikunja) lastTokenRequest() tokenPermRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.tokenRequests) == 0 {
		return tokenPermRequest{}
	}
	return f.tokenRequests[len(f.tokenRequests)-1]
}

const (
	testUsername = "strange-company-bootstrap"
	testPassword = "correct-horse-battery-staple"
)

func TestEnsureTokenReturnsTheStoredTokenWithoutMinting(t *testing.T) {
	fake := newFakeVikunja(t)
	fake.validTokens["existing-stored-token"] = true

	store := &memStore{creds: map[string]string{credentialName: "existing-stored-token"}}
	b := NewBootstrapper(fake.server.URL, store, testUsername, testPassword, nil)

	got, err := b.EnsureToken(context.Background())
	if err != nil {
		t.Fatalf("EnsureToken() returned error %v, want nil", err)
	}
	if got != "existing-stored-token" {
		t.Fatalf("EnsureToken() = %q, want %q", got, "existing-stored-token")
	}

	if fake.sawRequest("POST /api/v1/login") {
		t.Fatalf("EnsureToken() called /login, want no login when the stored token still validates")
	}
	if n := fake.writeCalls(); n != 0 {
		t.Fatalf("EnsureToken() made %d write calls, want 0 when the stored token still validates", n)
	}
	if store.puts != 0 {
		t.Fatalf("PutServiceCredential was called %d times, want 0 when the stored token still validates", store.puts)
	}
}

func TestEnsureTokenMintsAndPersistsOnFirstBoot(t *testing.T) {
	fake := newFakeVikunja(t)
	fake.users[testUsername] = testPassword
	fake.mintedToken = "freshly-minted-token"

	store := &memStore{}
	b := NewBootstrapper(fake.server.URL, store, testUsername, testPassword, nil)

	got, err := b.EnsureToken(context.Background())
	if err != nil {
		t.Fatalf("EnsureToken() returned error %v, want nil", err)
	}
	if got != "freshly-minted-token" {
		t.Fatalf("EnsureToken() = %q, want %q", got, "freshly-minted-token")
	}

	if store.puts != 1 {
		t.Fatalf("PutServiceCredential was called %d times, want exactly 1", store.puts)
	}
	if stored := store.creds[credentialName]; stored != "freshly-minted-token" {
		t.Fatalf("stored credential = %q, want %q", stored, "freshly-minted-token")
	}

	loginAt := fake.indexOf("POST /api/v1/login")
	routesAt := fake.indexOf("GET /api/v1/routes")
	tokensAt := fake.indexOf("PUT /api/v1/tokens")

	if loginAt == -1 || routesAt == -1 || tokensAt == -1 {
		t.Fatalf("expected /login, /routes and /tokens to all be called; got requests %v", fake.requests)
	}
	if !(loginAt < routesAt && routesAt < tokensAt) {
		t.Fatalf("expected /login then /routes then PUT /tokens, got order %v", fake.requests)
	}
}

func TestEnsureTokenRegistersWhenTheUserDoesNotExist(t *testing.T) {
	fake := newFakeVikunja(t)
	fake.loginFailStatus = http.StatusPreconditionFailed
	// fake.users starts empty: the bootstrap account does not exist yet.

	store := &memStore{}
	b := NewBootstrapper(fake.server.URL, store, testUsername, testPassword, nil)

	if _, err := b.EnsureToken(context.Background()); err != nil {
		t.Fatalf("EnsureToken() returned error %v, want nil", err)
	}

	if fake.registerCalls != 1 {
		t.Fatalf("register was called %d times, want exactly 1", fake.registerCalls)
	}
}

func TestRequestedScopesNeverExceedServerRoutes(t *testing.T) {
	fake := newFakeVikunja(t)
	fake.users[testUsername] = testPassword
	fake.routes = map[string]map[string]any{
		"tasks":  {"read_all": map[string]any{"path": "/tasks", "method": "GET"}},
		"labels": {"read_all": map[string]any{"path": "/labels", "method": "GET"}},
	}

	store := &memStore{}
	b := NewBootstrapper(fake.server.URL, store, testUsername, testPassword, nil)

	if _, err := b.EnsureToken(context.Background()); err != nil {
		t.Fatalf("EnsureToken() returned error %v, want nil", err)
	}

	req := fake.lastTokenRequest()
	if len(req.Permissions) == 0 {
		t.Fatalf("PUT /tokens was sent no permissions, want tasks and labels")
	}
	for group := range req.Permissions {
		if group != "tasks" && group != "labels" {
			t.Fatalf("requested scope %q that this server does not expose via /routes", group)
		}
	}
}

func TestEnsureTokenRemintsWhenTheStoredTokenIsRejected(t *testing.T) {
	fake := newFakeVikunja(t)
	fake.users[testUsername] = testPassword
	fake.mintedToken = "replacement-token"
	// Note: "stale-token" is deliberately NOT added to fake.validTokens, so
	// the probe GET /user for it returns 401.

	store := &memStore{creds: map[string]string{credentialName: "stale-token"}}
	b := NewBootstrapper(fake.server.URL, store, testUsername, testPassword, nil)

	got, err := b.EnsureToken(context.Background())
	if err != nil {
		t.Fatalf("EnsureToken() returned error %v, want nil", err)
	}
	if got != "replacement-token" {
		t.Fatalf("EnsureToken() = %q, want %q (a freshly minted replacement)", got, "replacement-token")
	}
	if !fake.sawRequest("POST /api/v1/login") {
		t.Fatalf("expected EnsureToken to log in and mint a replacement after the stored token was rejected")
	}
	if store.puts != 1 {
		t.Fatalf("PutServiceCredential was called %d times, want exactly 1", store.puts)
	}
	if store.creds[credentialName] != "replacement-token" {
		t.Fatalf("stored credential = %q, want the replacement token to be persisted", store.creds[credentialName])
	}
}

func TestStoredTokenIsNeverLogged(t *testing.T) {
	fake := newFakeVikunja(t)
	fake.users[testUsername] = testPassword
	fake.createTokenStatus = http.StatusInternalServerError

	store := &memStore{}
	b := NewBootstrapper(fake.server.URL, store, testUsername, testPassword, nil)

	_, err := b.EnsureToken(context.Background())
	if err == nil {
		t.Fatalf("EnsureToken() returned nil error, want an error when token creation fails")
	}

	msg := err.Error()
	if strings.Contains(msg, testPassword) {
		t.Fatalf("error message %q contains the bootstrap password, want it withheld", msg)
	}
	if strings.Contains(msg, fake.jwt) {
		t.Fatalf("error message %q contains the login JWT, want it withheld", msg)
	}
}

func TestEnsureTokenExplainsHowToFixRegistrationDisabled(t *testing.T) {
	fake := newFakeVikunja(t)
	fake.infoLocalEnabled = true
	fake.infoRegistrationEnabled = false
	// fake.users starts empty: the bootstrap account does not exist yet, and
	// on this instance it never can via this package.

	store := &memStore{}
	b := NewBootstrapper(fake.server.URL, store, testUsername, testPassword, nil)

	_, err := b.EnsureToken(context.Background())
	if err == nil {
		t.Fatalf("EnsureToken() returned nil error, want an error explaining that registration is disabled")
	}
	if !errors.Is(err, ErrRegistrationDisabled) {
		t.Fatalf("EnsureToken() error = %v, want it to wrap ErrRegistrationDisabled", err)
	}
	if fake.sawRequest("POST /api/v1/register") {
		t.Fatalf("EnsureToken() called /register, want no register attempt when /info already reports it is disabled")
	}

	msg := err.Error()
	if !strings.Contains(msg, testUsername) {
		t.Fatalf("error message %q does not mention the username %q the operator needs to pre-create", msg, testUsername)
	}
	if !strings.Contains(msg, "user create") {
		t.Fatalf("error message %q does not tell the operator how to fix this (want it to mention %q)", msg, "user create")
	}
}

func TestEnsureTokenReportsLocalAuthDisabled(t *testing.T) {
	fake := newFakeVikunja(t)
	fake.infoLocalEnabled = false

	store := &memStore{}
	b := NewBootstrapper(fake.server.URL, store, testUsername, testPassword, nil)

	_, err := b.EnsureToken(context.Background())
	if err == nil {
		t.Fatalf("EnsureToken() returned nil error, want an error when local authentication is disabled entirely")
	}
	if !errors.Is(err, ErrLocalAuthDisabled) {
		t.Fatalf("EnsureToken() error = %v, want it to wrap ErrLocalAuthDisabled", err)
	}
	if fake.sawRequest("POST /api/v1/login") {
		t.Fatalf("EnsureToken() called /login, want no login attempt when /info reports local auth is disabled")
	}
	if fake.sawRequest("POST /api/v1/register") {
		t.Fatalf("EnsureToken() called /register, want no register attempt when /info reports local auth is disabled")
	}
}

func TestEnsureTokenStillRegistersWhenRegistrationIsEnabled(t *testing.T) {
	fake := newFakeVikunja(t)
	fake.infoLocalEnabled = true
	fake.infoRegistrationEnabled = true
	// fake.users starts empty: the bootstrap account does not exist yet.

	store := &memStore{}
	b := NewBootstrapper(fake.server.URL, store, testUsername, testPassword, nil)

	if _, err := b.EnsureToken(context.Background()); err != nil {
		t.Fatalf("EnsureToken() returned error %v, want nil", err)
	}

	if fake.registerCalls != 1 {
		t.Fatalf("register was called %d times, want exactly 1 when /info reports registration is enabled", fake.registerCalls)
	}
}

func TestEnsureTokenFallsBackWhenInfoIsUnreachable(t *testing.T) {
	fake := newFakeVikunja(t)
	fake.infoStatus = http.StatusInternalServerError
	// fake.users starts empty: the bootstrap account does not exist yet.

	store := &memStore{}
	b := NewBootstrapper(fake.server.URL, store, testUsername, testPassword, nil)

	if _, err := b.EnsureToken(context.Background()); err != nil {
		t.Fatalf("EnsureToken() returned error %v, want nil: an unreachable /info must not block bootstrap", err)
	}

	if fake.registerCalls != 1 {
		t.Fatalf("register was called %d times, want exactly 1: bootstrap must fall back to attempting "+
			"registration when /info cannot be read or parsed", fake.registerCalls)
	}
}

func TestEnsureTokenSkipsInfoWhenStoredTokenIsValid(t *testing.T) {
	fake := newFakeVikunja(t)
	fake.validTokens["existing-stored-token"] = true

	store := &memStore{creds: map[string]string{credentialName: "existing-stored-token"}}
	b := NewBootstrapper(fake.server.URL, store, testUsername, testPassword, nil)

	if _, err := b.EnsureToken(context.Background()); err != nil {
		t.Fatalf("EnsureToken() returned error %v, want nil", err)
	}

	if fake.sawRequest("GET /api/v1/info") {
		t.Fatalf("EnsureToken() called /info, want the fast path (a still-valid stored token) to skip it entirely")
	}
}
