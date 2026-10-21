package verify

import (
	"errors"
	"fmt"
	"sort"

	"strange.company/sc/internal/policy"
)

// strangeCompany is the repository whose enforcement plane the platform protects (plan 22 section 9.3).
const (
	strangeCompany = "strange-company"
	e2eGlob        = "test/e2e/**"
	codeEscalation = "exemption_escalation_required"
	codeRoleDiff   = "ROLE_DIFF_VIOLATION"
	codeNotAllowed = "EXEMPTION_NOT_ALLOWED"
	minJustify     = 20
)

// e2eProtected reports whether the path is a test/e2e/** path of the repository strange-company (D4): the library
// adds the glob to the protected set and removes it from the test-classified paths, whatever the repository file says.
func e2eProtected(eff policy.Effective, p string) bool {
	return eff.Name == strangeCompany && policy.MatchGlob(e2eGlob, p)
}

type exemptionClass struct {
	name             string
	allowed          []string
	denied           []string
	human            bool
	platformHuman    bool
	characterization bool
	repos            []string
	hasRepos         bool
}

// loadClasses reads the platform exemption classes, applying the repository's requireHumanApproval.
func loadClasses(plat policy.Platform, eff policy.Effective) ([]exemptionClass, error) {
	spec, err := specMap(plat)
	if err != nil {
		return nil, err
	}
	raw, ok := spec["exemptionClasses"].([]any)
	if !ok {
		return nil, errors.New("the platform policy has no spec.exemptionClasses")
	}
	requireHuman, err := stringList(eff.Values["/spec/verification/requireHumanApproval"])
	if err != nil {
		return nil, fmt.Errorf("effective /spec/verification/requireHumanApproval: %w", err)
	}
	var out []exemptionClass
	for _, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			return nil, errors.New("an exemption class is not an object")
		}
		c := exemptionClass{}
		c.name, _ = m["name"].(string)
		if c.name == "" {
			return nil, errors.New("an exemption class has no name")
		}
		if c.allowed, err = stringList(m["allowedPaths"]); err != nil {
			return nil, fmt.Errorf("class %s allowedPaths: %w", c.name, err)
		}
		if c.denied, err = stringList(m["deniedPaths"]); err != nil {
			return nil, fmt.Errorf("class %s deniedPaths: %w", c.name, err)
		}
		if _, c.hasRepos = m["repositories"]; c.hasRepos {
			if c.repos, err = stringList(m["repositories"]); err != nil {
				return nil, fmt.Errorf("class %s repositories: %w", c.name, err)
			}
		}
		switch a, _ := m["approval"].(string); a {
		case "human":
			c.human, c.platformHuman = true, true
		case "auto":
		default:
			return nil, fmt.Errorf("class %s has approval %q", c.name, a)
		}
		if j, ok := m["judge"].(map[string]any); ok {
			k, _ := j["kind"].(string)
			c.characterization = k == "characterization"
		}
		for _, h := range requireHuman {
			if h == c.name {
				c.human = true
			}
		}
		out = append(out, c)
	}
	return out, nil
}

func (c exemptionClass) catchAll() bool {
	for _, g := range c.allowed {
		if g == "**" {
			return true
		}
	}
	return false
}

func (c exemptionClass) matches(p string) bool {
	return matchAny(c.allowed, p) && !matchAny(c.denied, p)
}

func findClass(cs []exemptionClass, name string) (exemptionClass, bool) {
	for _, c := range cs {
		if c.name == name {
			return c, true
		}
	}
	return exemptionClass{}, false
}

// ValidateExemption validates an exemption request at task creation (plan 22 section 9.2, steps 1 to 4).
func ValidateExemption(in ExemptionInput) (ExemptionDecision, error) {
	if in.Request.Mode != "verification-first" {
		return ExemptionDecision{}, fmt.Errorf("mode %q takes no exemption (only verification-first does)", in.Request.Mode)
	}
	if len([]rune(in.Request.Justification)) < minJustify {
		return ExemptionDecision{}, fmt.Errorf("the justification has fewer than %d characters", minJustify)
	}
	classes, err := loadClasses(in.Platform, in.Effective)
	if err != nil {
		return ExemptionDecision{}, err
	}
	refuse := func(format string, args ...any) (ExemptionDecision, error) {
		return ExemptionDecision{}, &ExemptionError{Code: codeNotAllowed, Detail: fmt.Sprintf(format, args...)}
	}
	c, ok := findClass(classes, in.Request.Class)
	if !ok {
		return refuse("class %q is not defined by the platform policy", in.Request.Class)
	}
	allowed, err := stringList(in.Effective.Values["/spec/verification/allowedExemptionClasses"])
	if err != nil {
		return ExemptionDecision{}, fmt.Errorf("effective /spec/verification/allowedExemptionClasses: %w", err)
	}
	listed := false
	for _, a := range allowed {
		listed = listed || a == c.name
	}
	if !listed {
		return refuse("class %q is not in the repository's allowedExemptionClasses", c.name)
	}
	if c.hasRepos && !matchAny(c.repos, in.Repository) {
		return refuse("repository %q does not match the repositories of class %q", in.Repository, c.name)
	}
	d := ExemptionDecision{Class: c.name, Status: "granted", Approval: "auto", PolicyRevision: in.PolicyRevision}
	if c.human {
		d.Status, d.Approval = "pending_approval", "human"
	}
	return d, nil
}

// Error implements error.
func (e *ExemptionError) Error() string { return e.Code + ": " + e.Detail }

// CheckScope judges the changed paths of an agent diff against the protected set and, for a producer, against its
// exemption class (plan 22 section 9.2 steps 3 and 5, section 9.3).
func CheckScope(in ScopeInput) ([]Violation, error) {
	switch in.Role {
	case "tester", "implementer", "producer":
	default:
		return nil, fmt.Errorf("scope judges tester, implementer and producer diffs, not role %q", in.Role)
	}
	protected, err := protectedPaths(RedInput{Platform: in.Platform, Effective: in.Effective})
	if err != nil {
		return nil, err
	}
	var (
		classes   []exemptionClass
		own       exemptionClass
		auto      bool
		testClass []string
	)
	if in.Role == "producer" {
		if in.Exemption == nil {
			return nil, errors.New("a producer diff needs the task's recorded exemption")
		}
		if classes, err = loadClasses(in.Platform, in.Effective); err != nil {
			return nil, err
		}
		var ok bool
		if own, ok = findClass(classes, in.Exemption.Class); !ok {
			return nil, fmt.Errorf("exemption class %q is not defined by the platform policy", in.Exemption.Class)
		}
		switch {
		case in.Exemption.Status == "granted" && in.Exemption.Approval == "auto":
			if own.platformHuman {
				return nil, fmt.Errorf("exemption class %q needs human approval, but the decision is auto", own.name)
			}
			auto = true
		case in.Exemption.Status == "pending_approval" && in.Exemption.Approval == "human":
		default:
			return nil, fmt.Errorf("exemption decision %q/%q is neither granted/auto nor pending_approval/human", in.Exemption.Status, in.Exemption.Approval)
		}
		if own.characterization {
			for _, k := range []string{"/spec/testPaths", "/spec/fixturePaths", "/spec/testInfraPaths"} {
				l, err := stringList(in.Effective.Values[k])
				if err != nil {
					return nil, fmt.Errorf("effective %s: %w", k, err)
				}
				testClass = append(testClass, l...)
			}
		}
	}
	var out []Violation
	for _, e := range in.Entries {
		p := e.Path
		if e.OldMode == modeGitlink || e.NewMode == modeGitlink {
			out = append(out, Violation{Code: codeGitlinkDenied, Path: p, Detail: "a gitlink (mode 160000) is denied in an autonomous diff"})
		}
		if matchAny(protected, p) {
			out = append(out, Violation{Code: codeProtectedPath, SubCode: subProtectedPath, Path: p, Detail: "protected path " + e.Status})
		}
		if in.Role != "producer" {
			continue
		}
		if !own.matches(p) || matchAny(testClass, p) {
			out = append(out, Violation{Code: codeRoleDiff, Path: p, Detail: fmt.Sprintf("path is outside the paths of exemption class %s", own.name)})
		}
		if auto {
			for _, c := range classes {
				if c.human && !c.catchAll() && c.matches(p) {
					out = append(out, Violation{Code: codeRoleDiff, SubCode: codeEscalation, Path: p,
						Detail: fmt.Sprintf("path belongs to class %s, which needs human approval, but the task is auto", c.name)})
					break
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		return a.SubCode < b.SubCode
	})
	return out, nil
}

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
