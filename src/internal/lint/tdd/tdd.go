// Package tdd implements the bootstrap TDD discipline checker behind `sc-lint tdd-discipline`
// (plan 52 section 8.3).
package tdd

// Policy is `spec.tddDiscipline` of policy/ci.yaml (plan 52 section 8.3).
type Policy struct {
	ExcludedBranches   []string                  `yaml:"excludedBranches"`
	BehaviorPaths      []string                  `yaml:"behaviorPaths"`
	TestPaths          []string                  `yaml:"testPaths"`
	RedTargets         []string                  `yaml:"redTargets"`
	RedTargetFallbacks map[string]string         `yaml:"redTargetFallbacks"`
	ExemptionClasses   map[string]ExemptionClass `yaml:"exemptionClasses"`
}

// ExemptionClass is one entry of Policy.ExemptionClasses.
type ExemptionClass struct {
	Paths        []string `yaml:"paths"`
	SubjectRegex string   `yaml:"subjectRegex"`
	Requires     string   `yaml:"requires"`
}

// Options select what Check judges. Root is a git work tree. Base is the target ref (for example
// origin/main); B is the merge base of Base and Head. Head defaults to HEAD. Branch is the PR source
// branch, Author the PR author login. A nil Policy is read from Root/policy/ci.yaml (spec.tddDiscipline).
// Env entries (KEY=VALUE) are added to the environment of every red-target run.
type Options struct {
	Root   string
	Base   string
	Head   string
	Branch string
	Author string
	Policy *Policy
	Env    []string
}

// Finding is one failure or warning: a code such as TDD_NO_RED_COMMIT, the commit it concerns (may be
// empty) and a human-readable message.
type Finding struct {
	Code    string `json:"code"`
	Commit  string `json:"commit,omitempty"`
	Message string `json:"message"`
}

// RedRun is the evaluation of one RED commit: its target, the packages it ran, F_R, whether the build
// succeeded and whether a redTargetFallbacks command replaced a missing Make target.
type RedRun struct {
	Commit       string   `json:"commit"`
	Target       string   `json:"target"`
	Packages     []string `json:"packages"`
	Failed       []string `json:"failed"`
	BuildOK      bool     `json:"build_ok"`
	FallbackUsed bool     `json:"fallback_used"`
}

// Red summarises all evaluated RED commits: Failed is the union of their failed sets, BuildOK is true
// only if every RED run built.
type Red struct {
	Commits []RedRun `json:"commits"`
	Failed  []string `json:"failed"`
	BuildOK bool     `json:"build_ok"`
}

// Report is the checker result (out/tdd-discipline/report.json). Result is one of "not-applicable",
// "exempt", "pass" or "fail". Failures hold the failure codes that make Result "fail". Warnings hold
// TDD_RED_TEST_MODIFIED and refused exclusions (TDD_EXCLUSION_INVALID). Exemption names the class
// of a valid exemption.
type Report struct {
	Result    string    `json:"result"`
	Branch    string    `json:"branch"`
	Exemption string    `json:"exemption,omitempty"`
	Failures  []Finding `json:"failures"`
	Warnings  []Finding `json:"warnings"`
	Red       Red       `json:"red"`
}

// SC-STUB(F01 52-AC-4)
func Check(opts Options) (Report, error) {
	return Report{}, nil
}
