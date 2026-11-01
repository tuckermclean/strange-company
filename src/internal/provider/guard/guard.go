// Package guard is the provider-neutral plan-JSON allowlist guard of plan 03 section 14.4 (ADR-0102): it judges
// `tofu show -json` output against a TofuPlanGuard file and computes the plan digest the operator confirms.
package guard

// Guard is a parsed TofuPlanGuard (contracts/provider.schema.yaml).
//
// SC-STUB(F09 03-AC-3 03-AC-4)
type Guard struct{}

// Attr names a provider-specific attribute that must equal configuration (rule 6): Path is a dotted path with
// optional [n] indexes, evaluated on the planned `after` value of every resource of Type.
//
// SC-STUB(F09 03-AC-3)
type Attr struct {
	Type string
	Path string
	Want string
}

// Expect is the configuration side of the guard decision.
//
// SC-STUB(F09 03-AC-3)
type Expect struct {
	// ExpectedNodeCount resolves a maxCount of `expectedNodeCount`.
	ExpectedNodeCount int
	// ProductCode is the catalogue planCode of MACHINE_CLASS (rule 5).
	ProductCode string
	Region      Attr
	SSHKey      Attr
	// Destroy selects guard-destroy semantics (rule 9).
	Destroy bool
}

// Verdict is the guard result: Pass, or the first failing rule's Code and the offending resource Address.
//
// SC-STUB(F09 03-AC-3)
type Verdict struct {
	Pass    bool
	Code    string
	Address string
	Message string
}

// ParseGuard reads a guard.yaml document.
//
// SC-STUB(F09 03-AC-3)
func ParseGuard(doc []byte) (Guard, error) {
	return Guard{}, nil
}

// Evaluate applies rules 1 to 9 in order to a plan JSON document; the first failure stops evaluation.
//
// SC-STUB(F09 03-AC-3)
func (g Guard) Evaluate(planJSON []byte, e Expect) Verdict {
	return Verdict{}
}

// Digest is the lowercase hex sha256 plan digest of plan 03 section 14.4.
//
// SC-STUB(F09 03-AC-4)
func (g Guard) Digest(planJSON []byte) (string, error) {
	return "", nil
}

// D8 is the first 8 hex characters of a digest.
//
// SC-STUB(F09 03-AC-4)
func D8(digest string) string {
	return ""
}
