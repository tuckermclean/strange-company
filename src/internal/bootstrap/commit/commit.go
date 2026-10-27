// Package commit creates the generated commits of plan 50 section 13 and enforces their scope.
package commit

import (
	"context"

	"strange.company/sc/internal/clock"
)

// Kind is the kind of generated commit.
//
// SC-STUB(F08 50-AC-14)
type Kind int

// SC-STUB(F08 50-AC-14)
const (
	Install Kind = iota
	Recovery
)

// Change is one changed path with its content before and after (nil: absent).
//
// SC-STUB(F08 50-AC-14)
type Change struct {
	Path   string
	Before []byte
	After  []byte
}

// Error is a refusal with a stable code.
//
// SC-STUB(F08 50-AC-14)
type Error struct {
	Code    string
	Message string
	Paths   []string
}

// Error implements error.
//
// SC-STUB(F08 50-AC-14)
func (e *Error) Error() string { return "" }

// CheckScope fails with COMMIT_SCOPE_VIOLATION when a change is outside the commit's scope.
//
// SC-STUB(F08 50-AC-14)
func CheckScope(cluster string, kind Kind, drill bool, changes []Change) error { return nil }

// Request describes a commit to create.
//
// SC-STUB(F08 50-AC-14)
type Request struct {
	Repo       string
	Cluster    string
	Kind       Kind
	Drill      bool
	Subject    string
	BaseDomain string
	InstallUID string
	RunID      string
	Clock      clock.Clock
	Paths      []string
}

// Create stages Paths, scope-checks the staged diff and commits it.
//
// SC-STUB(F08 50-AC-14)
func Create(ctx context.Context, req Request) (sha string, err error) { return "", nil }

// FindInstall returns the commit in HEAD's history that carries the trailer SC-Install-Uid: uid.
//
// SC-STUB(F08 50-AC-14)
func FindInstall(ctx context.Context, repo, uid string) (sha string, found bool, err error) {
	return "", false, nil
}
