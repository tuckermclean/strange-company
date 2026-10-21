package verify

import "strange.company/sc/internal/policy"

// CheckIngestion applies the test-protection rules of plan 22 section 14.6 to an implementer or producer diff.
//
// SC-STUB(F06 22-AC-8 22-AC-22 22-AC-31 22-AC-32)
func CheckIngestion(in IngestionInput) ([]Violation, error) { return nil, nil }

// DeriveDiscoveryProtection snapshots the discovery-protected files and semantic keys at a revision
// (plan 22 section 14.5).
//
// SC-STUB(F06 22-AC-32)
func DeriveDiscoveryProtection(in DiscoveryInput) (DiscoveryProtection, error) {
	return DiscoveryProtection{}, nil
}

// IngestionInput is everything CheckIngestion needs: the role, the range Start..Head in the Git repository, the
// role's maxFilesChanged, the manifest (its TestCommit is the red commit) and the policies. Validation is the parsed
// validation.yaml of the base revision; when it is not nil the discovery-protected files are also derived at Head
// (a file that make would now pick up, for example through a literal $(wildcard), is a discovery change).
type IngestionInput struct {
	Role            string
	Git             GitRepo
	Start           string
	Head            string
	MaxFilesChanged int
	Manifest        Manifest
	Platform        policy.Platform
	Effective       policy.Effective
	Validation      map[string]any
}

// Violation is one ingestion violation. Code is TEST_TAMPER (with SubCode), PROTECTED_PATH_VIOLATION or
// GITLINK_DENIED.
type Violation struct {
	Code    string
	SubCode string
	Path    string
	Line    int
	Detail  string
}

// DiscoveryInput names the revision to snapshot, the parsed validation.yaml and the policies.
type DiscoveryInput struct {
	Git        GitRepo
	Rev        string
	Validation map[string]any
	Platform   policy.Platform
	Effective  policy.Effective
}
