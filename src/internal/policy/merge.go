package policy

// SC-STUB(F04 22-AC-2 26-AC-4)
// Platform is the platform side of a merge: the VerificationPolicy document, the optional ModelRouting document
// (routing rows of plan 22 section 9.4) and the RoleDefinition documents by role name (diffLimits rows).
type Platform struct {
	Verification map[string]any
	ModelRouting map[string]any
	Roles        map[string]map[string]any
}

// SC-STUB(F04 22-AC-2 26-AC-4)
// Effective is the merged policy. Values maps the JSON pointer of each repository-policy field that was set to its
// merged value. It is nil when the repository policy is rejected (no partial result).
type Effective struct {
	Values map[string]any
}

// SC-STUB(F04 22-AC-2 26-AC-4)
// MergePolicy applies the tighten-only merge of plan 22 section 9.4 to a repository policy document.
func MergePolicy(opts Options, platform Platform, repo map[string]any) (Effective, []Issue) {
	return Effective{}, nil
}
