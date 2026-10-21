package verify

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"strange.company/sc/internal/policy"
)

// Slice S4 (policy scope) helpers. The inputs are the real platform policy (policy/verification.yaml) and the real
// repository policy (.strange-company/policy.yaml), cloned and edited one field at a time, merged by the real
// policy.MergePolicy; Git facts are derived by DeriveDiff from scratch repositories.
//
// Interface choices of S4 (plan 22 sections 9.2 and 9.3 are silent on the Go signatures):
//   - scope.go: ValidateExemption(ExemptionInput) (ExemptionDecision, error). EXEMPTION_NOT_ALLOWED is an
//     *ExemptionError (errors.As) with Code set; any other refusal (mode, justification) is a non-nil error of any
//     kind, never a decision. A granted decision has Status granted and Approval auto, a human one pending_approval
//     and human; it carries the class and the policy revision it was computed with.
//   - scope.go: CheckScope(ScopeInput) ([]Violation, error) judges the changed paths of a tester, implementer or
//     producer diff. PROTECTED_PATH_VIOLATION (SubCode protected_path_changed, as CheckIngestion) for a path in the
//     protected set; ROLE_DIFF_VIOLATION for a producer path outside its class (allowedPaths, deniedPaths) and, with
//     SubCode exemption_escalation_required, for a path whose strictest approval over every non-catch-all platform
//     class matching it is human while the task's approval is auto. The input names no repository: the repository
//     identity (D4) is Effective.Name, which policy.MergePolicy sets from metadata.name.
//   - Roles other than tester, implementer and producer, a producer without Exemption, and an Exemption naming a
//     class the platform does not define are errors (22-R-3, fail closed).
const (
	s4RoleDiff       = "ROLE_DIFF_VIOLATION"
	s4Escalation     = "exemption_escalation_required"
	s4ProtectedCode  = "PROTECTED_PATH_VIOLATION"
	s4NotAllowedCode = "EXEMPTION_NOT_ALLOWED"
)

func s4Clone(t testing.TB, m map[string]any) map[string]any {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func s4Strings(l ...string) []any {
	out := make([]any, len(l))
	for i, s := range l {
		out[i] = s
	}
	return out
}

// s4PlatformClasses are the exemption class names of the real platform policy, in file order.
func s4PlatformClasses(t testing.TB, plat policy.Platform) []string {
	t.Helper()
	spec, _ := plat.Verification["spec"].(map[string]any)
	list, _ := spec["exemptionClasses"].([]any)
	var out []string
	for _, c := range list {
		out = append(out, c.(map[string]any)["name"].(string))
	}
	if len(out) < 7 {
		t.Fatalf("platform policy has %d exemption classes, want at least the seven of plan 22 section 9.2", len(out))
	}
	return out
}

// s4PolicySpec describes the edits of the repository policy: the repository name (metadata.name), the
// spec.verification section (nil = the real file's, which has none), and extra test paths.
type s4PolicySpec struct {
	Name         string
	Allowed      []string // spec.verification.allowedExemptionClasses; nil = key absent
	AllowedEmpty bool     // allowedExemptionClasses: []
	RequireHuman []string
	TestPaths    []string // appended to spec.testPaths
}

func (p s4PolicySpec) key() string {
	return strings.Join([]string{p.Name, strings.Join(p.Allowed, ","), map[bool]string{true: "empty"}[p.AllowedEmpty], strings.Join(p.RequireHuman, ","), strings.Join(p.TestPaths, ",")}, "|")
}

var (
	s4Mu    sync.Mutex
	s4Cache = map[string]s4Merged{}
)

type s4Merged struct {
	plat policy.Platform
	eff  policy.Effective
}

// s4Effective merges the real repository policy, edited as ps says, with the real platform policy.
func s4Effective(t testing.TB, ps s4PolicySpec) (policy.Platform, policy.Effective) {
	t.Helper()
	s4Mu.Lock()
	defer s4Mu.Unlock()
	k := ps.key()
	if e, ok := s4Cache[k]; ok {
		return e.plat, e.eff
	}
	plat := platformPolicy(t)
	doc := s4Clone(t, readYAMLMap(t, filepath.Join(repoRoot(t), ".strange-company", "policy.yaml")))
	doc["metadata"] = map[string]any{"name": ps.Name}
	spec := doc["spec"].(map[string]any)
	if ps.Allowed != nil || ps.AllowedEmpty || ps.RequireHuman != nil {
		v := map[string]any{}
		if ps.Allowed != nil {
			v["allowedExemptionClasses"] = s4Strings(ps.Allowed...)
		} else if ps.AllowedEmpty {
			v["allowedExemptionClasses"] = []any{}
		}
		if ps.RequireHuman != nil {
			v["requireHumanApproval"] = s4Strings(ps.RequireHuman...)
		}
		spec["verification"] = v
	}
	if len(ps.TestPaths) > 0 {
		tp, _ := spec["testPaths"].([]any)
		spec["testPaths"] = append(tp, s4Strings(ps.TestPaths...)...)
	}
	eff, issues := policy.MergePolicy(policy.Options{ContractsDir: filepath.Join(repoRoot(t), "contracts")}, plat, doc)
	if len(issues) != 0 {
		t.Fatalf("test repository policy %q rejected: %v", k, issues)
	}
	s4Cache[k] = s4Merged{plat, eff}
	return plat, eff
}

// s4Diff builds a scratch repository whose base holds README.md, docs/old.md and src/old.go (plus the given extra
// base files), applies the head changes (path -> content; an empty content deletes the path) and returns the entries
// of git's own raw diff.
func s4Diff(t *testing.T, baseExtra []string, changes map[string]string) []DiffEntry {
	t.Helper()
	s := newScratch(t)
	s.write("README.md", "base\n")
	s.write("docs/old.md", "base\n")
	s.write("src/old.go", "package old\n")
	for _, p := range baseExtra {
		s.write(p, "base\n")
	}
	base := s.commit("base")
	var paths []string
	for p := range changes {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if changes[p] == "" {
			s.remove(p)
		} else {
			s.write(p, changes[p])
		}
	}
	head := s.commit("head")
	entries, err := DeriveDiff(s.repo(), base, head, 2000)
	if err != nil {
		t.Fatalf("DeriveDiff: %v", err)
	}
	if len(entries) != len(changes) {
		t.Fatalf("setup: %d raw entries for %d changes", len(entries), len(changes))
	}
	return entries
}

// s4Add is a change set that adds (or modifies) every path.
func s4Add(paths ...string) map[string]string {
	m := map[string]string{}
	for _, p := range paths {
		m[p] = "changed\n"
	}
	return m
}

// s4Key classifies a violation for the expectations: PROT, ESC (ROLE_DIFF_VIOLATION with the escalation sub-code),
// ROLE (any other ROLE_DIFF_VIOLATION) or the raw code.
func s4Key(v Violation) string {
	switch {
	case v.Code == s4ProtectedCode:
		return "PROT"
	case v.Code == s4RoleDiff && v.SubCode == s4Escalation:
		return "ESC"
	case v.Code == s4RoleDiff:
		return "ROLE"
	}
	return v.Code + "/" + v.SubCode
}

// s4Want is the expectation for one path: every Need key is present, no Forbid key is present. A path without an
// expectation must have no violation at all.
type s4Want struct{ Need, Forbid []string }

func s4Check(t *testing.T, got []Violation, want map[string]s4Want) {
	t.Helper()
	byPath := map[string]map[string]bool{}
	for _, v := range got {
		if byPath[v.Path] == nil {
			byPath[v.Path] = map[string]bool{}
		}
		byPath[v.Path][s4Key(v)] = true
		if v.Detail == "" {
			t.Errorf("violation %+v has no detail", v)
		}
	}
	for p, keys := range byPath {
		w, ok := want[p]
		if !ok {
			t.Errorf("%s: unexpected violation(s) %v, want none", p, s4Sorted(keys))
			continue
		}
		for _, f := range w.Forbid {
			if keys[f] {
				t.Errorf("%s: has forbidden %s (violations %v)", p, f, s4Sorted(keys))
			}
		}
	}
	for p, w := range want {
		for _, n := range w.Need {
			if !byPath[p][n] {
				t.Errorf("%s: missing %s (violations %v)", p, n, s4Sorted(byPath[p]))
			}
		}
	}
}

func s4Sorted(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func s4Need(keys ...string) s4Want { return s4Want{Need: keys} }

func s4NeedNot(need string, forbid ...string) s4Want {
	return s4Want{Need: []string{need}, Forbid: forbid}
}
