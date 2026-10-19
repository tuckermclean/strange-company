package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// CD-68, plan 22 section 9.5: path globs have the tdd-discipline checker's semantics (`**/` zero or more
// directories, `**` any string, `*` and `?` do not cross `/`, everything else literal), and "intersect" and
// "entirely inside" are exact language intersection and inclusion. These tests build a policy tree from
// testdata/valid with a modified exemptionClasses list and assert the exact outcome. Cases that expect "no
// issue" guard against an over-eager (approximating) implementation; cases that expect an issue guard against
// a witness-based approximation that can miss an overlap.

const (
	codeOverlap = "EXEMPTION_AUTO_OVERLAPS_HUMAN"
	codeDead    = "EXEMPTION_PATH_DEAD"
)

// mutatedValid copies testdata/valid and rewrites verification.yaml through mut (which edits spec).
func mutatedValid(t *testing.T, mut func(spec map[string]any)) string {
	t.Helper()
	dir := copyTree(t, td("valid"), false)
	p := filepath.Join(dir, "verification.yaml")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	mut(doc["spec"].(map[string]any))
	out, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, out, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func classes(spec map[string]any) []any { return spec["exemptionClasses"].([]any) }

func classByName(t *testing.T, spec map[string]any, name string) map[string]any {
	t.Helper()
	for _, c := range classes(spec) {
		if m := c.(map[string]any); m["name"] == name {
			return m
		}
	}
	t.Fatalf("no class %q in valid/verification.yaml", name)
	return nil
}

func strs(ss ...string) []any {
	out := make([]any, 0, len(ss))
	for _, s := range ss {
		out = append(out, s)
	}
	return out
}

func setPaths(c map[string]any, allowed, denied []string) {
	c["allowedPaths"] = strs(allowed...)
	if len(denied) == 0 {
		delete(c, "deniedPaths")
	} else {
		c["deniedPaths"] = strs(denied...)
	}
}

// twoClasses reduces exemptionClasses to the auto class documentation-only and the human class migration with
// the given paths, so that exactly one pair is judged.
func twoClasses(t *testing.T, autoAllowed, autoDenied, humanAllowed []string, protected ...string) func(map[string]any) {
	return func(spec map[string]any) {
		auto := classByName(t, spec, "documentation-only")
		human := classByName(t, spec, "migration")
		setPaths(auto, autoAllowed, autoDenied)
		setPaths(human, humanAllowed, nil)
		spec["exemptionClasses"] = []any{auto, human}
		if len(protected) > 0 {
			spec["protectedPaths"] = append(spec["protectedPaths"].([]any), strs(protected...)...)
		}
	}
}

// noStructuralIssues fails when the mutated tree is rejected for any reason other than the codes under test
// (so a "no issue" case cannot pass because the fixture itself is broken).
func noStructuralIssues(t *testing.T, is []Issue) {
	t.Helper()
	for _, i := range is {
		if i.Code != codeOverlap && i.Code != codeDead {
			t.Errorf("mutated tree rejected for an unrelated reason: %s", i)
		}
	}
}

func issuesWith(is []Issue, code string) []Issue {
	var out []Issue
	for _, i := range is {
		if i.Code == code {
			out = append(out, i)
		}
	}
	return out
}

func mentions(is []Issue, subs ...string) bool {
	for _, i := range is {
		ok := true
		for _, s := range subs {
			ok = ok && strings.Contains(i.Message, s)
		}
		if ok {
			return true
		}
	}
	return false
}

// 22-AC-1 (plan 22 section 9.5, CD-68): the effective paths of an auto class do not intersect the allowedPaths
// of a human class, decided by exact language intersection after subtracting deniedPaths.
func TestExactGlobOverlap_AutoVsHuman_22AC1(t *testing.T) {
	cases := []struct {
		name                         string
		autoAllowed, autoDenied, hum []string
		overlap                      bool
	}{
		// overlap: a witness over the prefix of one glob misses these
		{"star-star-md-vs-top-level-dir", []string{"**/*.md"}, nil, []string{"gitops/**"}, true},
		{"star-star-md-vs-nested-migrations", []string{"**/*.md"}, nil, []string{"**/migrations/**"}, true},
		{"question-vs-star-same-length", []string{"notes/??"}, nil, []string{"notes/a*"}, true},
		{"two-recursive-globs", []string{"**/x/**"}, nil, []string{"a/**"}, true},
		{"recursive-zero-directories", []string{"**/x/**"}, nil, []string{"x/**"}, true},
		{"inner-recursive-zero-directories", []string{"a/**/b"}, nil, []string{"a/b"}, true},
		{"bare-double-star-crosses-slash", []string{"a/**b"}, nil, []string{"a/c/b"}, true},
		{"deny-too-narrow-single-star", []string{"docs/**"}, []string{"docs/*/gen/**"}, []string{"docs/gen/**"}, true},
		{"deny-covers-only-part", []string{"gitops/**"}, []string{"gitops/*/x/**"}, []string{"gitops/**"}, true},
		// no overlap: an over-eager implementation reports these
		{"docs-vs-gitops", []string{"docs/**"}, nil, []string{"gitops/**"}, false},
		{"single-star-does-not-cross-slash", []string{"a/*"}, nil, []string{"a/b/c"}, false},
		{"star-md-has-no-directory", []string{"*.md"}, nil, []string{"docs/**"}, false},
		{"question-does-not-cross-slash", []string{"a?c"}, nil, []string{"a/c"}, false},
		{"literal-dot-is-not-wildcard", []string{"a.b"}, nil, []string{"axb"}, false},
		{"slash-needed", []string{"notes/?"}, nil, []string{"notes/*/x"}, false},
		{"deny-zero-directories", []string{"docs/**"}, []string{"docs/**/gen/**"}, []string{"docs/gen/**"}, false},
		{"deny-by-union-of-two-globs", []string{"zz/**"}, []string{"zz/*", "zz/*/**"}, []string{"zz/**"}, false},
		{"deny-whole-allowed", []string{"**/*.md"}, []string{"gitops/**"}, []string{"gitops/**"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := mutatedValid(t, twoClasses(t, c.autoAllowed, c.autoDenied, c.hum))
			_, issues := Check(dir, opts(t, false))
			noStructuralIssues(t, issues)
			got := issuesWith(issues, codeOverlap)
			switch {
			case c.overlap && len(got) == 0:
				t.Errorf("auto %v minus %v vs human %v: want %s; issues:\n%s", c.autoAllowed, c.autoDenied, c.hum, codeOverlap, joinIssues(issues))
			case c.overlap && !mentions(got, "documentation-only", "migration"):
				t.Errorf("%s message names neither class: %s", codeOverlap, joinIssues(got))
			case !c.overlap && len(got) != 0:
				t.Errorf("auto %v minus %v vs human %v: want no %s; issues:\n%s", c.autoAllowed, c.autoDenied, c.hum, codeOverlap, joinIssues(got))
			}
		})
	}
}

// 22-AC-1 (CD-68): with the shipped class list, the pre-CD-68 documentation-only class (no deny for the
// human-class paths) overlaps infrastructure-config and migration, but only through `**/*.md` against
// `gitops/**` and `**/migrations/**`; each deny removes exactly its own overlap.
func TestExactGlobOverlap_ShippedClasses_22AC1(t *testing.T) {
	five := []string{"gitops/**", "deploy/**", "ansible/**", "tofu/**", "**/migrations/**"}
	cases := []struct {
		name         string
		drop         []string
		wantInfra    bool
		wantMigrates bool
	}{
		{"pre-cd68-all-five-removed", five, true, true},
		{"only-gitops-removed", []string{"gitops/**"}, true, false},
		{"only-deploy-removed", []string{"deploy/**"}, true, false},
		{"only-ansible-removed", []string{"ansible/**"}, true, false},
		{"only-tofu-removed", []string{"tofu/**"}, true, false},
		{"only-migrations-removed", []string{"**/migrations/**"}, false, true},
		{"shipped-cd68", nil, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := mutatedValid(t, func(spec map[string]any) {
				doc := classByName(t, spec, "documentation-only")
				var keep []any
				for _, d := range doc["deniedPaths"].([]any) {
					drop := false
					for _, x := range c.drop {
						drop = drop || d == x
					}
					if !drop {
						keep = append(keep, d)
					}
				}
				doc["deniedPaths"] = keep
			})
			_, issues := Check(dir, opts(t, false))
			noStructuralIssues(t, issues)
			got := issuesWith(issues, codeOverlap)
			if has := mentions(got, "documentation-only", "infrastructure-config"); has != c.wantInfra {
				t.Errorf("overlap with infrastructure-config = %v, want %v; issues:\n%s", has, c.wantInfra, joinIssues(issues))
			}
			if has := mentions(got, "documentation-only", "migration"); has != c.wantMigrates {
				t.Errorf("overlap with migration = %v, want %v; issues:\n%s", has, c.wantMigrates, joinIssues(issues))
			}
			if len(got) > 0 != (c.wantInfra || c.wantMigrates) {
				t.Errorf("%s issues = %v, want %v:\n%s", codeOverlap, len(got), c.wantInfra || c.wantMigrates, joinIssues(got))
			}
		})
	}
}

// 22-AC-1 (CD-68): an allowedPaths entry is dead only when its whole language lies inside the union of the
// protected set (exact inclusion); intersection or partial coverage is allowed.
func TestExactGlobDeadEntry_22AC1(t *testing.T) {
	cases := []struct {
		name      string
		allowed   string
		protected []string // appended to the valid protected set
		dead      bool
	}{
		{"inside-one-glob", ".github/workflows/**", nil, true},
		{"inside-by-star-suffix", ".github/**/*.yml", nil, true},
		{"equal-to-protected", "policy/**", nil, true},
		{"inside-through-zero-directories", "gitops/generated/x/**", []string{"gitops/**/generated/**"}, true},
		{"inside-only-by-union-of-two-globs", "zz/**", []string{"zz/*", "zz/*/**"}, true},
		{"partly-protected-gitops", "gitops/**", nil, false},
		{"protected-is-only-root-github", "**/.github/**", nil, false},
		{"protected-is-narrower", "gitops/**/gen*/**", nil, false},
		{"single-star-protected-does-not-cover-subdirs", "zz/**", []string{"zz/*"}, false},
		{"unrelated", "docs/**", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := mutatedValid(t, func(spec map[string]any) {
				doc := classByName(t, spec, "documentation-only")
				doc["allowedPaths"] = append(doc["allowedPaths"].([]any), c.allowed)
				if len(c.protected) > 0 {
					spec["protectedPaths"] = append(spec["protectedPaths"].([]any), strs(c.protected...)...)
				}
			})
			_, issues := Check(dir, opts(t, false))
			noStructuralIssues(t, issues)
			got := issuesWith(issues, codeDead)
			if !c.dead {
				if len(got) != 0 {
					t.Errorf("allowed %q is not entirely protected, want no %s; issues:\n%s", c.allowed, codeDead, joinIssues(got))
				}
				return
			}
			want := "/spec/exemptionClasses/0/allowedPaths/2"
			found := false
			for _, i := range got {
				found = found || (i.Pointer == want && strings.Contains(i.Message, c.allowed))
			}
			if !found {
				t.Errorf("allowed %q is entirely protected, want %s at %s naming it; issues:\n%s", c.allowed, codeDead, want, joinIssues(issues))
			}
			if len(got) != 1 {
				t.Errorf("want exactly one %s issue, got %d:\n%s", codeDead, len(got), joinIssues(got))
			}
		})
	}
}
