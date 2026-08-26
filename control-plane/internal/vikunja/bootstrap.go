package vikunja

import (
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

// desiredScopeGroups is the full set of Vikunja permission groups the
// control plane would like its bot token to hold. It is never used
// unfiltered: EnsureToken always intersects it with whatever groups
// GET /api/v1/routes actually reports the target server supports, so a
// token request never asks for a permission group a given server does not
// expose.
var desiredScopeGroups = []string{
	"tasks",
	"projects",
	"labels",
	"comments",
	"assignees",
	"relations",
	"buckets",
	"views",
	"subscriptions",
	"webhooks",
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

	jwt, err := anon.Login(ctx, b.username, b.password)
	if err != nil {
		if !looksLikeUnknownUser(err) {
			return "", fmt.Errorf("vikunja bootstrap: login: %w", err)
		}

		email := b.username + "@strange-company.local"
		if regErr := anon.Register(ctx, b.username, email, b.password); regErr != nil {
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

// intersectPermissions builds the permissions map to request from Vikunja,
// limited to the groups in desiredScopeGroups that are actually present in
// routes. For each included group, the requested actions are every action
// name that group's route entry advertises. The second return value is the
// sorted list of included group names, suitable for storing as metadata.
func intersectPermissions(routes map[string]map[string]any) (map[string]any, []string) {
	perms := make(map[string]any)
	var scopes []string

	for _, group := range desiredScopeGroups {
		actions, ok := routes[group]
		if !ok {
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

// looksLikeUnknownUser reports whether err is the kind of failure Vikunja
// returns from /api/v1/login when the given username has no account yet,
// as opposed to, say, a wrong password or a network failure. Vikunja does
// not expose a distinct error code for this, so this checks the HTTP status
// codes it is known to use for "no such user" (400 and 412) rather than
// hard-coding on the response body's contents.
func looksLikeUnknownUser(err error) bool {
	var reqErr *RequestError
	if !errors.As(err, &reqErr) {
		return false
	}
	return reqErr.Status == http.StatusBadRequest || reqErr.Status == http.StatusPreconditionFailed
}
