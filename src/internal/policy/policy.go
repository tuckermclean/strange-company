// Package policy loads and validates the platform policy tree (plans 22 and 26, F04).
package policy

import "io/fs"

// SC-STUB(F04 26-AC-1 26-AC-2 26-AC-3)
// Issue is one policy error, printed as `<CODE>[:<json-pointer>] <file>: <message>`.
type Issue struct {
	Code    string
	Pointer string
	File    string
	Message string
}

// SC-STUB(F04 26-AC-1 26-AC-2 26-AC-3)
// Options configures a check or a Loader. ContractsDir holds the *.schema.yaml files. Runner selects Runner
// mode, which additionally requires every Runner kind of plan 26 section 9.2.
type Options struct {
	ContractsDir string
	Runner       bool
}

// SC-STUB(F04 26-AC-1 26-AC-2 26-AC-3)
// Snapshot is an accepted policy revision.
type Snapshot struct {
	Revision string
}

// SC-STUB(F04 26-AC-1 26-AC-2 26-AC-3)
// Loader keeps the last-known-good snapshot (plan 26 section 9.2 steps 6 and 7).
type Loader struct{}

// SC-STUB(F04 26-AC-1)
func (i Issue) String() string { return "" }

// SC-STUB(F04 26-AC-2)
func Digest(dir string) (string, error) { return "", nil }

// SC-STUB(F04 26-AC-2)
func DigestFS(fsys fs.FS) (string, error) { return "", nil }

// SC-STUB(F04 26-AC-1 22-AC-1)
func Check(dir string, opts Options) (string, []Issue) { return "", nil }

// SC-STUB(F04 26-AC-3)
func NewLoader(opts Options) *Loader { return nil }

// SC-STUB(F04 26-AC-3)
func (l *Loader) Load(dir string) []Issue { return nil }

// SC-STUB(F04 26-AC-3)
func (l *Loader) Snapshot() *Snapshot { return nil }

// SC-STUB(F04 26-AC-3)
func (l *Loader) DispatchEnabled() bool { return false }

// SC-STUB(F04 26-AC-3)
func (l *Loader) LastError() string { return "" }
