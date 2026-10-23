package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// F06 slice S4 (22-AC-33, 22-AC-11): the enforcement-plane loader invariant of plan 22 section 9.3 and the
// auto-versus-human overlap invariant of section 9.5.
//
// Interface choices (the plan is silent on both):
//   - the invariant lives in MergePolicy (the loader that produces the effective policy); it reports
//     ENFORCEMENT_PLANE_NOT_ELEVATED with Pointer /spec/merge/elevatedPaths, one issue per uncovered glob whose
//     message names the glob; Effective.Values is nil as for every other issue;
//   - MergePolicy records metadata.name of the repository policy in Effective.Name (the verifier library needs the
//     repository identity for test/e2e/** of section 9.3, D4); a rejected policy leaves it empty;
//   - the list is plan 22 section 9.3 with jobbuilder read as jobs (D3); "covered" is exact glob inclusion of the
//     required glob in the union of the repository's elevatedPaths and its protectedPaths (F04's glob algebra,
//     never string equality).
//
// The real files policy/verification.yaml and .strange-company/policy.yaml are the inputs; every case is one edit.

const s4Code = "ENFORCEMENT_PLANE_NOT_ELEVATED"

// s4Plane is the list of plan 22 section 9.3 with jobbuilder read as jobs (brief D3). Written out here on purpose:
// it is the specification, not a copy of the implementation's list.
var s4Plane = []string{
	"src/internal/verify/**", "src/internal/statemachine/**", "src/internal/policy/**", "src/internal/runner/**",
	"src/internal/forge/**", "src/internal/gateway/**", "src/internal/ledger/**", "src/internal/jobs/**",
	"src/internal/scrub/**", "src/internal/lint/**",
	"src/cmd/sc-verify/**", "src/cmd/sc-ci-config/**", "src/cmd/agent-runner/**", "src/cmd/sc-worker/**",
	"src/cmd/model-gateway/**", "src/cmd/sc-gitops/**", "src/cmd/egress-proxy/**", "src/cmd/sc-release/**",
	"src/cmd/sc-e2e/**",
}

func s4Platform(t testing.TB) Platform {
	t.Helper()
	return Platform{Verification: mergeDoc(t, filepath.Join(repoRoot(t), "policy", "verification.yaml"))}
}

// s4Repo is the real repository policy, cloned, renamed, and edited by mutate.
func s4Repo(t testing.TB, name string, mutate func(spec map[string]any)) map[string]any {
	t.Helper()
	doc := mergeClone(t, mergeDoc(t, filepath.Join(repoRoot(t), ".strange-company", "policy.yaml")))
	doc["metadata"] = map[string]any{"name": name}
	if mutate != nil {
		mutate(doc["spec"].(map[string]any))
	}
	return doc
}

func s4Elevated(spec map[string]any) []string {
	var out []string
	if m, ok := spec["merge"].(map[string]any); ok {
		for _, g := range m["elevatedPaths"].([]any) {
			out = append(out, g.(string))
		}
	}
	return out
}

func s4SetElevated(spec map[string]any, globs []string) {
	l := make([]any, len(globs))
	for i, g := range globs {
		l[i] = g
	}
	m, _ := spec["merge"].(map[string]any)
	if m == nil {
		m = map[string]any{}
		spec["merge"] = m
	}
	m["elevatedPaths"] = l
}

func s4Without(l []string, drop ...string) []string {
	var out []string
	for _, g := range l {
		keep := true
		for _, d := range drop {
			keep = keep && g != d
		}
		if keep {
			out = append(out, g)
		}
	}
	return out
}

func s4AddProtected(spec map[string]any, globs ...string) {
	l, _ := spec["protectedPaths"].([]any)
	for _, g := range globs {
		l = append(l, g)
	}
	spec["protectedPaths"] = l
}

func s4Merge(t testing.TB, name string, mutate func(spec map[string]any)) (Effective, []Issue) {
	t.Helper()
	return MergePolicy(opts(t, false), s4Platform(t), s4Repo(t, name, mutate))
}

func s4Plain(is []Issue) []string {
	var out []string
	for _, i := range is {
		out = append(out, i.String())
	}
	return out
}

// 22-AC-33 twin: the repository's own policy covers the whole list (jobbuilder read as jobs), so it is accepted
// and the effective policy carries the repository name.
func TestMergePolicy_RealRepositoryPolicyCoversPlane_22AC33(t *testing.T) {
	eff, is := s4Merge(t, "strange-company", nil)
	if len(is) != 0 {
		t.Fatalf("the real .strange-company/policy.yaml is rejected: %v", s4Plain(is))
	}
	if eff.Values == nil {
		t.Error("accepted policy has no values")
	}
	if eff.Name != "strange-company" {
		t.Errorf("Effective.Name = %q, want strange-company (metadata.name of the merged repository policy)", eff.Name)
	}
}

// 22-AC-33: Effective.Name is metadata.name for any accepted repository policy, and empty on rejection.
func TestMergePolicy_RecordsRepositoryName_22AC33(t *testing.T) {
	for _, name := range []string{"demo", "strange-company-fork", "Strange-Company", "strange-company2"} {
		eff, is := s4Merge(t, name, func(spec map[string]any) { delete(spec, "merge") })
		if len(is) != 0 {
			t.Fatalf("%s: unexpected issues %v", name, s4Plain(is))
		}
		if eff.Name != name {
			t.Errorf("Effective.Name = %q, want %q", eff.Name, name)
		}
	}
	eff, is := s4Merge(t, "strange-company", func(spec map[string]any) {
		s4SetElevated(spec, s4Without(s4Elevated(spec), "src/internal/runner/**"))
	})
	if len(is) == 0 {
		t.Fatal("setup: a policy without the runner glob must be rejected")
	}
	if eff.Name != "" || eff.Values != nil {
		t.Errorf("rejected policy leaves Effective %+v, want the zero value", eff)
	}
}

// 22-AC-33: a strange-company repository policy whose elevatedPaths omits any glob of the enforcement plane is
// rejected with ENFORCEMENT_PLANE_NOT_ELEVATED naming that glob (one case per glob, one edit each).
func TestMergePolicy_OmittedPlaneGlobIsRejected_22AC33(t *testing.T) {
	for _, g := range s4Plane {
		t.Run(strings.NewReplacer("/", "_", "*", "x").Replace(g), func(t *testing.T) {
			eff, is := s4Merge(t, "strange-company", func(spec map[string]any) {
				s4SetElevated(spec, s4Without(s4Elevated(spec), g))
			})
			if eff.Values != nil {
				t.Error("rejected policy has values")
			}
			if len(is) != 1 || is[0].Code != s4Code {
				t.Fatalf("issues = %v, want exactly one %s", s4Plain(is), s4Code)
			}
			if is[0].Pointer != "/spec/merge/elevatedPaths" || is[0].File == "" || !strings.Contains(is[0].Message, g) {
				t.Errorf("issue %+v: want pointer /spec/merge/elevatedPaths, a file, and a message naming %q", is[0], g)
			}
		})
	}
}

// 22-AC-33: no elevatedPaths at all (and no merge section) leaves every glob uncovered; each is named.
func TestMergePolicy_NoElevatedPathsNamesEveryGlob_22AC33(t *testing.T) {
	for _, how := range []string{"empty list", "no merge section"} {
		eff, is := s4Merge(t, "strange-company", func(spec map[string]any) {
			if how == "empty list" {
				s4SetElevated(spec, nil)
			} else {
				delete(spec, "merge")
			}
		})
		if eff.Values != nil {
			t.Errorf("%s: rejected policy has values", how)
		}
		for _, g := range s4Plane {
			found := false
			for _, i := range is {
				if i.Code == s4Code && strings.Contains(i.Message, g) {
					found = true
				}
			}
			if !found {
				t.Errorf("%s: no %s issue names %q; issues %v", how, s4Code, g, s4Plain(is))
			}
		}
		for _, i := range is {
			if i.Code != s4Code {
				t.Errorf("%s: unexpected issue %s", how, i)
			}
		}
	}
}

// 22-AC-33 (D3): the plan's own spelling jobbuilder is not the directory of the Job builder (src/internal/jobs),
// so it does not cover the list entry for jobs.
func TestMergePolicy_JobbuilderSpellingDoesNotCoverJobs_22AC33(t *testing.T) {
	_, is := s4Merge(t, "strange-company", func(spec map[string]any) {
		s4SetElevated(spec, append(s4Without(s4Elevated(spec), "src/internal/jobs/**"), "src/internal/jobbuilder/**"))
	})
	if len(is) != 1 || is[0].Code != s4Code || !strings.Contains(is[0].Message, "src/internal/jobs/**") {
		t.Errorf("issues = %v, want one %s naming src/internal/jobs/**", s4Plain(is), s4Code)
	}
}

// 22-AC-33: coverage is glob inclusion in F04's algebra, not string equality. The required glob is
// src/internal/runner/**; the case replaces its entry by the candidate(s). Each case states why it covers or not.
func TestMergePolicy_PlaneCoverageIsGlobInclusion_22AC33(t *testing.T) {
	const target = "src/internal/runner/**"
	type tc struct {
		name      string
		elevated  []string
		protected []string
		covered   bool
	}
	cases := []tc{
		// covered: a superset language
		{"parent_directory", []string{"src/internal/**"}, nil, true},
		{"whole_src", []string{"src/**"}, nil, true},
		{"catch_all", []string{"**"}, nil, true},
		{"any_directory_runner", []string{"**/runner/**"}, nil, true},
		{"inner_double_star", []string{"src/**/runner/**"}, nil, true},
		{"star_in_segment", []string{"src/internal/run*/**"}, nil, true},
		{"question_in_segment", []string{"src/internal/runne?/**"}, nil, true},
		{"double_star_without_slash", []string{"src/internal/runner**"}, nil, true},
		{"union_of_two_globs", []string{"src/internal/runner/*", "src/internal/runner/*/**"}, nil, true},
		{"covered_by_protected_only", nil, []string{"src/internal/runner/**"}, true},
		{"protected_parent", nil, []string{"src/internal/runner**"}, true},
		{"split_between_elevated_and_protected", []string{"src/internal/runner/*"}, []string{"src/internal/runner/*/**"}, true},
		// not covered: a strictly smaller or different language
		{"one_level_only", []string{"src/internal/runner/*"}, nil, false},
		{"sub_directory_only", []string{"src/internal/runner/a/**"}, nil, false},
		{"go_files_only", []string{"src/internal/runner/**/*.go"}, nil, false},
		{"direct_go_files", []string{"src/internal/runner/*.go"}, nil, false},
		{"directory_name_without_suffix", []string{"src/internal/runner"}, nil, false},
		{"trailing_slash_only", []string{"src/internal/runner/"}, nil, false},
		{"literal_braces", []string{"src/internal/{runner,forge}/**"}, nil, false},
		{"literal_brackets", []string{"src/internal/ru[n]ner/**"}, nil, false},
		{"shorter_name", []string{"src/internal/runne/**"}, nil, false},
		{"longer_name", []string{"src/internal/runnerx/**"}, nil, false},
		{"case_differs", []string{"src/internal/Runner/**"}, nil, false},
		{"dot_slash_prefix", []string{"./src/internal/runner/**"}, nil, false},
		{"basename_only", []string{"**/runner"}, nil, false},
		{"protected_sub_directory_only", nil, []string{"src/internal/runner/a/**"}, false},
		{"elevated_and_protected_both_partial", []string{"src/internal/runner/*"}, []string{"src/internal/runner/a/**"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eff, is := s4Merge(t, "strange-company", func(spec map[string]any) {
				s4SetElevated(spec, append(s4Without(s4Elevated(spec), target), c.elevated...))
				s4AddProtected(spec, c.protected...)
			})
			if c.covered {
				if len(is) != 0 || eff.Values == nil {
					t.Errorf("covered by %v + protected %v but rejected: %v", c.elevated, c.protected, s4Plain(is))
				}
				return
			}
			if len(is) != 1 || is[0].Code != s4Code || !strings.Contains(is[0].Message, target) {
				t.Errorf("not covered by %v + protected %v, want one %s naming %s; got %v", c.elevated, c.protected, s4Code, target, s4Plain(is))
			}
		})
	}
}

// 22-AC-33: one broad glob covers a whole family, and covering one family member never covers another family.
func TestMergePolicy_BroadGlobCoversFamilyOnly_22AC33(t *testing.T) {
	internal, cmd := []string{}, []string{}
	for _, g := range s4Plane {
		if strings.HasPrefix(g, "src/internal/") {
			internal = append(internal, g)
		} else {
			cmd = append(cmd, g)
		}
	}
	// src/internal/** replaces every internal entry: accepted. Without any src/cmd entry: the cmd globs are uncovered.
	_, is := s4Merge(t, "strange-company", func(spec map[string]any) {
		s4SetElevated(spec, append(s4Without(s4Elevated(spec), internal...), "src/internal/**"))
	})
	if len(is) != 0 {
		t.Errorf("src/internal/** replacing the internal entries is rejected: %v", s4Plain(is))
	}
	_, is = s4Merge(t, "strange-company", func(spec map[string]any) {
		s4SetElevated(spec, append(s4Without(s4Elevated(spec), append(append([]string{}, internal...), cmd...)...), "src/internal/**"))
	})
	for _, g := range cmd {
		found := false
		for _, i := range is {
			found = found || (i.Code == s4Code && strings.Contains(i.Message, g))
		}
		if !found {
			t.Errorf("src/internal/** alone must leave %s uncovered; issues %v", g, s4Plain(is))
		}
	}
	for _, i := range is {
		for _, g := range internal {
			if strings.Contains(i.Message, g) {
				t.Errorf("src/internal/** covers %s but issue says otherwise: %s", g, i)
			}
		}
	}
}

// 22-AC-33: the invariant applies only to the repository named strange-company (exact, case-sensitive).
func TestMergePolicy_PlaneInvariantOnlyForStrangeCompany_22AC33(t *testing.T) {
	for _, name := range []string{"demo", "strange-company-fork", "Strange-Company", "strange-company2", "x"} {
		eff, is := s4Merge(t, name, func(spec map[string]any) { delete(spec, "merge") })
		if len(is) != 0 || eff.Values == nil {
			t.Errorf("repository %q without any elevatedPaths: issues %v, want none", name, s4Plain(is))
		}
	}
	_, is := s4Merge(t, "strange-company", func(spec map[string]any) { delete(spec, "merge") })
	if !hasCode(is, s4Code) {
		t.Errorf("repository strange-company without elevatedPaths: issues %v, want %s", s4Plain(is), s4Code)
	}
}

// 22-AC-33: a negated glob is not part of the glob language (schema pattern), so it can neither cover the plane
// nor be accepted. The case is outside the repo-policy schema by construction.
func TestMergePolicy_NegatedGlobDoesNotCover_22AC33(t *testing.T) {
	for _, name := range []string{"strange-company", "demo"} {
		eff, is := s4Merge(t, name, func(spec map[string]any) {
			s4SetElevated(spec, append(s4Elevated(spec), "!src/internal/runner/**"))
		})
		if len(is) == 0 || eff.Values != nil {
			t.Errorf("%s: a negated elevatedPaths entry was accepted", name)
		}
	}
	_, is := s4Merge(t, "strange-company", func(spec map[string]any) {
		s4SetElevated(spec, append(s4Without(s4Elevated(spec), "src/internal/runner/**"), "!src/internal/runner/**"))
	})
	if len(is) == 0 {
		t.Error("a negated entry in place of the runner glob was accepted")
	}
}

// 22-AC-11 (TDD-12; F04 invariant, already satisfied): a platform policy whose auto class has effective paths that
// intersect a non-catch-all human class fails the policy check (make policy-check = Check on the tree), the real
// policy passes, and a catch-all human class is ignored. The mutation is one edit of the real policy/verification.yaml:
// the metadata class (auto, **/*.yaml) stops denying gitops/**, which infrastructure-config (human) allows.
func TestCheck_AutoClassOverlappingHumanClassFails_22AC11(t *testing.T) {
	root := repoRoot(t)
	src := filepath.Join(root, "policy")
	if _, issues := Check(src, opts(t, false)); len(issues) != 0 {
		t.Fatalf("the real policy has issues: %v", s4Plain(issues))
	}
	dir := copyTree(t, src, false)
	path := filepath.Join(dir, "verification.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	edited := false
	for _, c := range doc["spec"].(map[string]any)["exemptionClasses"].([]any) {
		cl := c.(map[string]any)
		if cl["name"] != "metadata" {
			continue
		}
		var keep []any
		for _, d := range cl["deniedPaths"].([]any) {
			if d != "gitops/**" {
				keep = append(keep, d)
			} else {
				edited = true
			}
		}
		cl["deniedPaths"] = keep
	}
	if !edited {
		t.Fatal("setup: metadata class does not deny gitops/** in the real policy")
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
	_, issues := Check(dir, opts(t, false))
	found := false
	for _, i := range issues {
		found = found || (i.Code == "EXEMPTION_AUTO_OVERLAPS_HUMAN" && strings.Contains(i.Message, "metadata") && strings.Contains(i.Message, "infrastructure-config"))
	}
	if !found {
		t.Errorf("issues %v, want EXEMPTION_AUTO_OVERLAPS_HUMAN naming metadata and infrastructure-config", s4Plain(issues))
	}
}
