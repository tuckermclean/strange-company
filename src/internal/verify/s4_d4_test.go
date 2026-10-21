package verify

import (
	"sort"
	"strings"
	"testing"
)

// 22-AC-33, second clause (brief D4): for the repository named strange-company the verifier library adds
// test/e2e/** to the effective protected set of every agent diff (tester, implementer, producer) and subtracts it
// from the test-classified paths, independent of the repository file, which stays unchanged (it lists the glob under
// elevatedPaths only). The repository identity is Effective.Name (set by policy.MergePolicy from metadata.name), so
// ClassifyRed, CheckIngestion and CheckScope take no new argument.
//
// The repository policy used here is the real .strange-company/policy.yaml plus testPaths `test/**`, which the
// plan says never include test/e2e/**: without the library's own subtraction that path would be a test path.

var s4E2EProtected = []string{"test/e2e/tdd/x.yaml", "test/e2e/x.yaml", "test/e2e/a/b/c/d.yaml"}

// Near misses of the glob test/e2e/**: it is anchored, needs the directory, and is case sensitive.
var s4E2ENear = []string{"test/e2efoo/x.yaml", "test/e2e.yaml", "xtest/e2e/x.yaml", "src/test/e2e/x.yaml", "test/integration/x.yaml", "test/E2E/x.yaml", "test/e2e-extra/x.yaml"}

func s4Strange(t testing.TB, name string) s4PolicySpec {
	return s4PolicySpec{Name: name, Allowed: s4AllClasses, TestPaths: []string{"test/**"}}
}

// The repository file is unchanged: test/e2e/** is not among its protectedPaths, so the library supplies it.
func TestStrangeCompanyPolicyFileDoesNotListE2EProtected_22AC33(t *testing.T) {
	_, eff := s4Effective(t, s4Strange(t, "strange-company"))
	list, ok := eff.Values["/spec/protectedPaths"].([]any)
	if !ok || len(list) == 0 {
		t.Fatalf("effective /spec/protectedPaths = %v", eff.Values["/spec/protectedPaths"])
	}
	for _, g := range list {
		if g == "test/e2e/**" {
			t.Fatal("the repository policy file lists test/e2e/** under protectedPaths; D4 says the library adds it")
		}
	}
	if eff.Name != "strange-company" {
		t.Errorf("Effective.Name = %q, want strange-company", eff.Name)
	}
}

func s4ScopeKeys(t *testing.T, role string, exemption *ExemptionDecision, name string, files []string) map[string]map[string]bool {
	t.Helper()
	plat, eff := s4Effective(t, s4Strange(t, name))
	entries := s4Diff(t, nil, s4Add(files...))
	got, err := CheckScope(ScopeInput{Role: role, Entries: entries, Exemption: exemption, Platform: plat, Effective: eff})
	if err != nil {
		t.Fatalf("CheckScope(%s, %s): %v", role, name, err)
	}
	out := map[string]map[string]bool{}
	for _, v := range got {
		if out[v.Path] == nil {
			out[v.Path] = map[string]bool{}
		}
		out[v.Path][s4Key(v)] = true
	}
	return out
}

// A tester diff touching test/e2e/tdd/x.yaml yields PROTECTED_PATH_VIOLATION in strange-company, for every agent
// role; the same paths are clean in any other repository (and so are the near misses everywhere).
func TestCheckScope_E2EProtectedForStrangeCompany_22AC33(t *testing.T) {
	roles := []struct {
		role string
		ex   *ExemptionDecision
	}{{"tester", nil}, {"implementer", nil}, {"producer", s4Producer("metadata", "auto")}}
	for _, r := range roles {
		t.Run(r.role, func(t *testing.T) {
			got := s4ScopeKeys(t, r.role, r.ex, "strange-company", s4E2EProtected)
			for _, p := range s4E2EProtected {
				if !got[p]["PROT"] {
					t.Errorf("strange-company/%s: %s: violations %v, want PROTECTED_PATH_VIOLATION", r.role, p, s4Sorted(got[p]))
				}
			}
			near := s4ScopeKeys(t, r.role, r.ex, "strange-company", s4E2ENear)
			if len(near) != 0 {
				t.Errorf("strange-company/%s: near misses flagged: %v", r.role, near)
			}
			for _, name := range []string{"demo", "strange-company-fork", "Strange-Company", "strange-company2"} {
				if other := s4ScopeKeys(t, r.role, r.ex, name, append(append([]string{}, s4E2EProtected...), s4E2ENear...)); len(other) != 0 {
					t.Errorf("repository %s/%s: flagged %v, want clean (D4 is for strange-company only)", name, r.role, other)
				}
			}
		})
	}
	// Roles that have no extension to match: any file of the directory is protected, the bare name is not.
	for _, role := range []string{"tester", "implementer"} {
		got := s4ScopeKeys(t, role, nil, "strange-company", []string{"test/e2e/run", "test/e2e/tdd/run.sh", "test/e2e/.hidden"})
		for _, p := range []string{"test/e2e/run", "test/e2e/tdd/run.sh", "test/e2e/.hidden"} {
			if !got[p]["PROT"] {
				t.Errorf("%s: %s not protected: %v", role, p, got[p])
			}
		}
		if bare := s4ScopeKeys(t, role, nil, "strange-company", []string{"test/e2e"}); len(bare) != 0 {
			t.Errorf("%s: the file test/e2e (no directory) is flagged: %v", role, bare)
		}
	}
}

// ClassifyRed (rule R1) for a tester red commit: the Runner classifies test/e2e/tdd/x.yaml as a test file (testPaths
// test/**), and in strange-company that is still a scope violation. The same change is red_valid elsewhere, and a
// near miss is red_valid in strange-company.
func TestClassifyRed_E2EIsProtectedForStrangeCompany_22AC33(t *testing.T) {
	run := func(name, path string) RedClassification {
		fx, _ := loadRedFixture(t, "valid/go_zero_value_stub")
		in := redInputOf(t, fx)
		_, in.Effective = s4Effective(t, s4Strange(t, name))
		in.Scope.ChangedFiles = append(append([]ChangedFile(nil), in.Scope.ChangedFiles...), ChangedFile{Path: path, Change: "added", Class: "test"})
		got, err := ClassifyRed(in)
		if err != nil {
			t.Fatalf("ClassifyRed: %v", err)
		}
		return got
	}
	if got := run("demo", "test/e2e/tdd/x.yaml"); got.Outcome != "red_valid" {
		t.Fatalf("setup: the base fixture with an extra test file is %s %v, want red_valid", got.Outcome, got.Reasons)
	}
	got := run("strange-company", "test/e2e/tdd/x.yaml")
	if got.Outcome != "red_invalid" || len(got.Reasons) == 0 || got.Reasons[0] != "scope_violation" {
		t.Errorf("strange-company: %s %v, want red_invalid / scope_violation", got.Outcome, got.Reasons)
	}
	for _, p := range []string{"test/e2efoo/x.yaml", "test/e2e.yaml", "test/integration/x.yaml"} {
		if got := run("strange-company", p); got.Outcome != "red_valid" {
			t.Errorf("strange-company near miss %s: %s %v, want red_valid", p, got.Outcome, got.Reasons)
		}
	}
}

// CheckIngestion for implementer and producer diffs: test/e2e/** is protected in strange-company and no longer a
// test path there (a pre-existing file is PROTECTED_PATH_VIOLATION only, not existing_test_modified), while another
// repository sees a plain test path.
func TestCheckIngestion_E2EProtectedForStrangeCompany_22AC33(t *testing.T) {
	for _, role := range []string{"implementer", "producer"} {
		for _, name := range []string{"strange-company", "demo"} {
			t.Run(role+"_"+name, func(t *testing.T) {
				s := newScratch(t)
				s.write("README.md", "base\n")
				s.write("test/e2e/tdd/old.yaml", "a: 1\n")
				s.write("test/e2efoo/old.yaml", "a: 1\n")
				red := s.commit("red")
				s.write("test/e2e/tdd/old.yaml", "a: 2\n")
				s.write("test/e2e/tdd/x.yaml", "a: 1\n")
				s.write("test/e2efoo/old.yaml", "a: 2\n")
				s.write("test/e2efoo/new.yaml", "a: 1\n")
				head := s.commit("head")
				plat, eff := s4Effective(t, s4Strange(t, name))
				got, err := CheckIngestion(IngestionInput{Role: role, Git: s.repo(), Start: red, Head: head, MaxFilesChanged: 2000,
					Manifest: s.s3Manifest(red), Platform: plat, Effective: eff})
				if err != nil {
					t.Fatalf("CheckIngestion: %v", err)
				}
				var keys []string
				for _, v := range got {
					keys = append(keys, violationKey(v))
				}
				sort.Strings(keys)
				var want []string
				if name == "strange-company" {
					want = []string{"PROTECTED_PATH_VIOLATION/protected_path_changed test/e2e/tdd/old.yaml", "PROTECTED_PATH_VIOLATION/protected_path_changed test/e2e/tdd/x.yaml"}
				}
				// test/e2efoo/old.yaml existed at red and matches testPaths test/** in both repositories.
				want = append(want, "TEST_TAMPER/existing_test_modified test/e2efoo/old.yaml")
				if name == "demo" {
					want = append(want, "TEST_TAMPER/existing_test_modified test/e2e/tdd/old.yaml")
				}
				sort.Strings(want)
				if strings.Join(keys, "\n") != strings.Join(want, "\n") {
					t.Errorf("violations:\n%s\nwant:\n%s", strings.Join(keys, "\n"), strings.Join(want, "\n"))
				}
			})
		}
	}
}
