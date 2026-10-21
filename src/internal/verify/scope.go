package verify

import "strange.company/sc/internal/policy"

// ValidateExemption validates an exemption request at task creation (plan 22 section 9.2, steps 1 to 4).
//
// SC-STUB(F06 22-AC-10 22-AC-11)
func ValidateExemption(in ExemptionInput) (ExemptionDecision, error) {
	return ExemptionDecision{}, nil
}

// Error implements error.
//
// SC-STUB(F06 22-AC-10)
func (e *ExemptionError) Error() string { return "" }

// CheckScope judges the changed paths of an agent diff against the protected set and, for a producer, against its
// exemption class (plan 22 section 9.2 steps 3 and 5, section 9.3).
//
// SC-STUB(F06 22-AC-11 22-AC-33)
func CheckScope(in ScopeInput) ([]Violation, error) { return nil, nil }

// ExemptionRequest is the exemption_request of a verification-first task (plan 22 section 9.2 step 1). Mode is
// test-first or verification-first.
type ExemptionRequest struct {
	Mode          string
	Class         string
	Justification string
}

// ExemptionInput is everything ValidateExemption needs. Repository is the `org/name` of the managed repository (it
// is matched against a class's `repositories` globs); Effective is the merged repository policy.
type ExemptionInput struct {
	Platform       policy.Platform
	Effective      policy.Effective
	Repository     string
	Request        ExemptionRequest
	PolicyRevision string
}

// ExemptionDecision is the decision recorded on the task: Status is granted (Approval auto) or pending_approval
// (Approval human).
type ExemptionDecision struct {
	Class          string
	Status         string
	Approval       string
	PolicyRevision string
}

// ExemptionError carries EXEMPTION_NOT_ALLOWED.
type ExemptionError struct {
	Code   string
	Detail string
}

// ScopeInput is a diff to judge. Role is tester, implementer or producer; Entries come from DeriveDiff; Exemption
// is the task's recorded decision and is required for the producer role.
type ScopeInput struct {
	Role      string
	Entries   []DiffEntry
	Exemption *ExemptionDecision
	Platform  policy.Platform
	Effective policy.Effective
}
