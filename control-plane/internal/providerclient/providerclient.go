// Package providerclient builds a model client for a resolved policy rung.
//
// It exists because the control plane once routed its own model calls through
// the Hermes gateway, which ignores the requested model and answers from its
// own global route: policy chose deepseek-v4-flash and claude-opus-4-6
// answered, at frontier prices, while the logs said otherwise. Spec 22 exists
// to test the model-tiering thesis, so a cheap rung silently served by the
// expensive model makes every measurement of it meaningless.
//
// The rule this package enforces: a model call the control plane makes itself
// goes to the provider policy named, at that provider's baseUrl, with that
// provider's credential. There is no fallback, because the fallback is what
// hid the problem.
package providerclient

import (
	"errors"
	"fmt"
	"sort"

	"github.com/tuckermclean/strange-company/control-plane/internal/credentials"
	"github.com/tuckermclean/strange-company/control-plane/internal/modelclient"
	"github.com/tuckermclean/strange-company/control-plane/internal/policy"
)

var (
	// ErrNoBaseURL means the provider cannot be called in-process.
	//
	// Deliberately not recoverable: substituting any other endpoint is
	// exactly the silent misrouting this package exists to prevent.
	ErrNoBaseURL = errors.New("providerclient: provider has no baseUrl and cannot be called directly")

	// ErrAmbiguousCredential means the provider declares more than one
	// credential, so there is no way to know which is the bearer token.
	// Choosing by map order would work until the day it chose the other.
	ErrAmbiguousCredential = errors.New("providerclient: provider declares more than one credential")
)

// New builds a model client for a resolved rung.
//
// A provider with no `env` block is valid and gets no Authorization header --
// providers.yaml's ollama entry is exactly that, on purpose.
func New(res *policy.Resolution, creds credentials.Dir) (*modelclient.Client, error) {
	if res == nil {
		return nil, errors.New("providerclient: no resolution")
	}
	if res.BaseURL == "" {
		return nil, fmt.Errorf("%w: provider %q, phase %q", ErrNoBaseURL, res.ProviderName, res.Phase)
	}

	apiKey, err := credentialFor(res, creds)
	if err != nil {
		return nil, err
	}

	return modelclient.New(res.BaseURL, apiKey, res.Model)
}

// credentialFor resolves the single credential a direct call needs.
func credentialFor(res *policy.Resolution, creds credentials.Dir) (string, error) {
	switch len(res.Env) {
	case 0:
		return "", nil
	case 1:
	default:
		names := make([]string, 0, len(res.Env))
		for name := range res.Env {
			names = append(names, name)
		}
		sort.Strings(names)
		return "", fmt.Errorf("%w: provider %q declares %v", ErrAmbiguousCredential, res.ProviderName, names)
	}

	for name, ref := range res.Env {
		value, err := creds.Read(ref.Secret, ref.Key)
		if err != nil {
			return "", fmt.Errorf("providerclient: provider %q, phase %q, credential %s: %w",
				res.ProviderName, res.Phase, name, err)
		}
		return value, nil
	}
	return "", nil // unreachable: len(res.Env) == 1
}
