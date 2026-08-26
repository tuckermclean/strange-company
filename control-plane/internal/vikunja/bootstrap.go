package vikunja

import (
	"strings"
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"
)

// credentialName is the key this package uses to store and retrieve the
// provisioned bot token in the CredentialStore.
const credentialName = "vikunja_bot_token"

// tokenTitle is the title given to the API token this package mints, so it
// is recognizable in Vikunja's own token-management UI.
const tokenTitle = "strange-company-control-plane"

// ErrRegistrationDisabled is wrapped into the error EnsureToken returns when
// login fails and GET /api/v1/info reports that local registration is
// disabled, so the bootstrap account can never be created by this package.
// The operator must pre-create it out of band.
var ErrRegistrationDisabled = errors.New("vikunja: local registration is disabled")

// ErrLocalAuthDisabled is wrapped into the error EnsureToken returns when
// GET /api/v1/info reports that local authentication itself is disabled
// (a pure-SSO instance). Bootstrap cannot work at all in that configuration;
// the operator must set vikunja.token explicitly to a pre-minted token.
var ErrLocalAuthDisabled = errors.New("vikunja: local authentication is disabled")

// desiredScopeGroups is the full set of Vikunja permission groups the
// control plane would like its bot token to hold. It is never used
// unfiltered: EnsureToken always intersects it with whatever groups
// GET /api/v1/routes actually reports the target server supports, so a
// token request never asks for a permission group a given server does not
// expose.
var desiredScopeGroups = []string{
	"task",
	"project",
	"label",
	"comment",
	"assignee",
	"relation",
	"bucket",
	"view",
	"subscription",
	"webhook",
}

// CredentialStore is the persistence dependency Bootstrapper needs. It is
// satisfied by *store.Store; it is expressed as an interface here so tests
// can supply an in-memory implementation without a database.
type CredentialStore interface {
	GetServiceCredential(ctx context.Context, name string) (string, error)
	PutServiceCredential(ctx context.Context, name, secret string, metadata map[string]any) error
}

// Bootstrapper provisions and persists a long-lived Vikunja API token for a
// bot user, minting one on first boot and reusing the stored token on every
// subsequent boot as long as it still validates.
type Bootstrapper struct {
	baseURL    string
	store      CredentialStore
	username   string
	password   string
	httpClient *http.Client
}

// NewBootstrapper returns a Bootstrapper targeting the Vikunja instance at
// baseURL, using username/password to log in (and, if necessary, register)
// the bootstrap account that owns the minted token. If httpClient is nil,
// http.DefaultClient is used.
func NewBootstrapper(baseURL string, store CredentialStore, username, password string, httpClient *http.Client) *Bootstrapper {
	return &Bootstrapper{
		baseURL:    baseURL,
		store:      store,
		username:   username,
		password:   password,
		httpClient: httpClient,
	}
}

// EnsureToken returns a valid, long-lived Vikunja API token for the control
// plane to use, minting and persisting a new one only if none is already
// stored or the stored one no longer validates. It never logs or returns
// the transient login JWT used to mint a new token, and never includes a
// password or token value in any error it returns.
func (b *Bootstrapper) EnsureToken(ctx context.Context) (string, error) {
	stored, err := b.store.GetServiceCredential(ctx, credentialName)
	if err != nil {
		return "", fmt.Errorf("vikunja bootstrap: read stored token: %w", err)
	}

	if stored != "" {
		probe := New(b.baseURL, stored, b.httpClient)
		if _, _, probeErr := probe.CurrentUser(ctx); probeErr == nil {
			return stored, nil
		}
		// The stored token no longer validates (revoked, expired, or the
		// server was reset). Fall through and mint a fresh one.
	}

	return b.mint(ctx)
}

// mint logs in (registering the bootstrap account first if necessary),
// discovers the permission groups the target server actually exposes,
// requests a token scoped to the intersection of those groups and
// desiredScopeGroups, and persists the result.
func (b *Bootstrapper) mint(ctx context.Context) (string, error) {
	anon := New(b.baseURL, "", b.httpClient)

	// Consult GET /api/v1/info up front so we can diagnose -- rather than
	// blindly attempt -- a login/registration sequence that this server's
	// auth configuration guarantees will fail (e.g. a Vikunja instance
	// sitting behind SSO with local registration turned off). This call is
	// best-effort: infoOK is false whenever /info could not be reached or
	// parsed, and every decision below then falls through to the original
	// behaviour unchanged. An unreachable info endpoint is a transient
	// condition the supervisor already retries; a diagnostic improvement
	// must never become a new hard dependency for bootstrap itself.
	info, infoOK := fetchAuthInfo(ctx, anon)

	if infoOK && !info.localEnabled {
		return "", fmt.Errorf(
			"vikunja bootstrap: local authentication is disabled on this instance (auth is SSO-only); "+
				"bootstrap cannot create or use a local account here -- set vikunja.token explicitly to a "+
				"pre-minted API token: %w", ErrLocalAuthDisabled)
	}

	// Vikunja deliberately returns the SAME failure (403, code 1011) for an
	// unknown username and a wrong password, so that login cannot be used to
	// enumerate accounts. That means we cannot ask "does this user exist?" --
	// we can only try to create it and interpret the result.
	//
	// So: on any login failure, attempt to register. If registration reports
	// the account already exists, the account is real and our password is
	// wrong, which is an operator error worth saying plainly rather than
	// retrying forever.
	jwt, err := anon.Login(ctx, b.username, b.password)
	if err != nil {
		if !isAuthFailure(err) {
			return "", fmt.Errorf("vikunja bootstrap: login: %w", err)
		}

		if infoOK && !info.registrationEnabled {
			return "", fmt.Errorf(
				"vikunja bootstrap: login for user %q failed and local registration is disabled on this "+
					"Vikunja instance, so the account cannot be self-provisioned; pre-create it out of band, "+
					"e.g.:\n  kubectl -n <namespace> exec deploy/<vikunja> -- vikunja user create -u %s -e <email> -p <password>: %w",
				b.username, b.username, ErrRegistrationDisabled)
		}

		email := b.username + "@strange-company.local"
		if regErr := anon.Register(ctx, b.username, email, b.password); regErr != nil {
			if isAlreadyExists(regErr) {
				return "", fmt.Errorf(
					"vikunja bootstrap: account %q already exists but the configured password was rejected; "+
						"set vikunja.token explicitly or reset the bootstrap credential: %w", b.username, err)
			}
			return "", fmt.Errorf("vikunja bootstrap: register: %w", regErr)
		}

		jwt, err = anon.Login(ctx, b.username, b.password)
		if err != nil {
			return "", fmt.Errorf("vikunja bootstrap: login after register: %w", err)
		}
	}

	authed := New(b.baseURL, jwt, b.httpClient)

	ownerID, _, err := authed.CurrentUser(ctx)
	if err != nil {
		return "", fmt.Errorf("vikunja bootstrap: fetch current user: %w", err)
	}

	routes, err := authed.Routes(ctx)
	if err != nil {
		return "", fmt.Errorf("vikunja bootstrap: fetch routes: %w", err)
	}

	perms, scopes := intersectPermissions(routes)

	token, err := authed.CreateToken(ctx, tokenTitle, ownerID, perms)
	if err != nil {
		return "", fmt.Errorf("vikunja bootstrap: create token: %w", err)
	}

	metadata := map[string]any{
		"minted_at": time.Now().UTC().Format(time.RFC3339),
		"scopes":    scopes,
	}
	if err := b.store.PutServiceCredential(ctx, credentialName, token, metadata); err != nil {
		return "", fmt.Errorf("vikunja bootstrap: persist token: %w", err)
	}

	return token, nil
}

// authInfo is the subset of GET /api/v1/info this package consults to
// diagnose why a login or registration attempt might be doomed before
// making it.
type authInfo struct {
	localEnabled        bool
	registrationEnabled bool
}

// fetchAuthInfo calls the unauthenticated GET /api/v1/info endpoint and
// extracts its local-auth configuration. ok is false whenever the endpoint
// could not be reached or its response could not be parsed; callers must
// treat that as "unknown" and fall back to their pre-existing behaviour
// rather than fail bootstrap on it -- see the comment in mint for why.
func fetchAuthInfo(ctx context.Context, c *Client) (info authInfo, ok bool) {
	var resp struct {
		Auth struct {
			Local struct {
				Enabled             bool `json:"enabled"`
				RegistrationEnabled bool `json:"registration_enabled"`
			} `json:"local"`
		} `json:"auth"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/info", nil, &resp); err != nil {
		return authInfo{}, false
	}
	return authInfo{
		localEnabled:        resp.Auth.Local.Enabled,
		registrationEnabled: resp.Auth.Local.RegistrationEnabled,
	}, true
}

// intersectPermissions builds the permissions map to request from Vikunja,
// limited to the groups in desiredScopeGroups that are actually present in
// routes. For each included group, the requested actions are every action
// name that group's route entry advertises. The second return value is the
// sorted list of included group names, suitable for storing as metadata.
func intersectPermissions(routes map[string]map[string]any) (map[string]any, []string) {
	perms := make(map[string]any)
	var scopes []string

	// Match on substrings rather than exact names. Vikunja does not promise
	// the group names we would guess -- the Kanban views group, for instance,
	// is not literally "views" -- and an unmatched group silently drops the
	// permission, which then surfaces much later as an opaque 401 on a route
	// the token simply is not scoped for.
	for group, actions := range routes {
		if !wantsGroup(group) {
			continue
		}

		names := make([]string, 0, len(actions))
		for action := range actions {
			names = append(names, action)
		}
		sort.Strings(names)

		perms[group] = names
		scopes = append(scopes, group)
	}

	sort.Strings(scopes)
	return perms, scopes
}

// wantsGroup reports whether a permission group reported by GET /routes is one
// the control plane needs.
func wantsGroup(group string) bool {
	g := strings.ToLower(group)
	for _, want := range desiredScopeGroups {
		if strings.Contains(g, want) {
			return true
		}
	}
	return false
}

// isAuthFailure reports whether err is Vikunja rejecting our credentials, as
// opposed to the server being unreachable or broken.
//
// Vikunja answers an unknown username and a wrong password identically -- HTTP
// 403 with code 1011 -- and hashes a dummy value in the unknown-user case
// specifically so the two cannot be told apart by timing either. Any attempt to
// distinguish them here would be guesswork, so we do not try.
func isAuthFailure(err error) bool {
	var reqErr *RequestError
	if !errors.As(err, &reqErr) {
		return false
	}
	switch reqErr.Status {
	case http.StatusForbidden, http.StatusUnauthorized,
		http.StatusBadRequest, http.StatusPreconditionFailed:
		return true
	}
	return false
}

// isAlreadyExists reports whether a registration failed because the username or
// email is taken, which is how we learn -- after the fact -- that the account
// existed and our password was simply wrong.
func isAlreadyExists(err error) bool {
	var reqErr *RequestError
	if !errors.As(err, &reqErr) {
		return false
	}
	if reqErr.Status == http.StatusConflict {
		return true
	}
	body := strings.ToLower(reqErr.Body)
	return strings.Contains(body, "already exist") ||
		strings.Contains(body, "already in use") ||
		strings.Contains(body, "already taken")
}
