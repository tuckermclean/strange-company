// Package credentials reads provider credentials from a projected Secret
// directory.
//
// policy.CredentialRef names a Kubernetes Secret and a key, which is exactly
// the shape of a projected Secret volume: <dir>/<secret>/<key>. Reading files
// rather than calling the Kubernetes API means no RBAC, and an operator can
// see precisely what the pod can read by looking at the volume.
package credentials

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	// ErrNotFound means no such credential is projected.
	ErrNotFound = errors.New("credentials: not projected")

	// ErrEmpty means the file exists but holds nothing.
	//
	// Distinct from ErrNotFound on purpose: one operator forgot to project
	// the Secret, the other projected an unset value, and telling them
	// apart is the difference between a five-second fix and an afternoon.
	ErrEmpty = errors.New("credentials: projected but empty")

	// ErrInvalidRef means the Secret name or key is not a single path
	// element. providers.yaml is operator-editable, so a name like
	// "../etc" must be refused rather than resolved.
	ErrInvalidRef = errors.New("credentials: secret name and key must each be a single path element")
)

// Dir is a directory of projected Secrets.
type Dir string

// Read returns the credential at <dir>/<secret>/<key>, trimmed.
//
// Trimming matters: a projected Secret routinely ends with a newline and a
// hand-edited one carries spaces. Sending either to a provider produces an
// authentication failure that looks nothing like its cause.
//
// No error returned here ever contains the file's contents. Errors are logged,
// and a credential in a log is a credential in the log store.
func (d Dir) Read(secret, key string) (string, error) {
	if err := validElement(secret); err != nil {
		return "", fmt.Errorf("%w: secret %q", ErrInvalidRef, secret)
	}
	if err := validElement(key); err != nil {
		return "", fmt.Errorf("%w: key %q", ErrInvalidRef, key)
	}
	if d == "" {
		return "", fmt.Errorf("%w: no credentials directory is configured (secret %q, key %q)", ErrNotFound, secret, key)
	}

	path := filepath.Join(string(d), secret, key)
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%w: %s/%s", ErrNotFound, secret, key)
		}
		// os.ReadFile's error names the path, never the contents.
		return "", fmt.Errorf("credentials: reading %s/%s: %w", secret, key, err)
	}

	value := strings.TrimSpace(string(raw))
	if value == "" {
		return "", fmt.Errorf("%w: %s/%s", ErrEmpty, secret, key)
	}
	return value, nil
}

// validElement rejects anything that is not a single, ordinary path element.
func validElement(s string) error {
	if s == "" || s == "." || s == ".." {
		return ErrInvalidRef
	}
	if strings.ContainsRune(s, os.PathSeparator) || strings.ContainsRune(s, '/') {
		return ErrInvalidRef
	}
	if filepath.Clean(s) != s {
		return ErrInvalidRef
	}
	return nil
}
