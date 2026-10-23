package verify

import (
	"path/filepath"
	"testing"

	"strange.company/sc/internal/policy"
)

// S4 review 1, decision D21: (B1) a characterization-judged producer (the refactor class) may not add or change
// test-classified paths (plan 22 section 9.2 "** minus test-classified paths", section 14.8 TDD-B9); (N2) a gitlink
// entry is GITLINK_DENIED for every role (section 9.3); (c) CheckScope refuses an inconsistent exemption decision.
//
// CheckIngestion has no producer rule for new test-classified files (plan 22 section 14.6 allows new test files
// for implementers and producers alike; it protects only existing ones), so B1 is asserted on CheckScope only.

// s4MergeExtra merges the real repository policy, renamed, with extra globs appended to its testPaths, fixturePaths
// and testInfraPaths (the s4 helpers can append only to testPaths).
func s4MergeExtra(t *testing.T, name string, testPaths, fixturePaths, infraPaths []string) (policy.Platform, policy.Effective) {
	t.Helper()
	plat := platformPolicy(t)
	doc := s4Clone(t, readYAMLMap(t, filepath.Join(repoRoot(t), ".strange-company", "policy.yaml")))
	doc["metadata"] = map[string]any{"name": name}
	spec := doc["spec"].(map[string]any)
	spec["verification"] = map[string]any{"allowedExemptionClasses": s4Strings(s4AllClasses...)}
	for k, extra := range map[string][]string{"testPaths": testPaths, "fixturePaths": fixturePaths, "testInfraPaths": infraPaths} {
		if len(extra) == 0 {
			continue
		}
		cur, _ := spec[k].([]any)
		spec[k] = append(cur, s4Strings(extra...)...)
	}
	eff, issues := policy.MergePolicy(policy.Options{ContractsDir: filepath.Join(repoRoot(t), "contracts")}, plat, doc)
	if len(issues) != 0 {
		t.Fatalf("test repository policy rejected: %v", issues)
	}
	return plat, eff
}

// 22-AC-11 (D21 B1): the refactor producer adds a test-infrastructure file, a repository-added testPaths file and a
// repository-added fixture or test-infrastructure file: ROLE_DIFF_VIOLATION for each path.
func TestCheckScope_RefactorProducerMayNotAddTestClassifiedPaths_D21_22AC11(t *testing.T) {
	cases := []struct {
		name                      string
		testPaths, fixture, infra []string
		files                     []string
	}{
		{name: "real_test_infra_and_fixture_paths", files: []string{"src/internal/testutil/zz.go", "test/fixtures/x.json", "src/internal/testutil/sub/deep.go"}},
		{name: "repo_added_testpaths", testPaths: []string{"src/**/*.test.ts"}, files: []string{"src/b.test.ts", "src/deep/c.test.ts"}},
		{name: "repo_added_fixturepaths", fixture: []string{"**/__mocks__/**"}, files: []string{"__mocks__/http.js", "src/__mocks__/fs.js"}},
		{name: "repo_added_testinfrapaths", infra: []string{"**/__mocks__/**"}, files: []string{"__mocks__/http.js", "src/__mocks__/fs.js"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plat, eff := s4MergeExtra(t, "demo", c.testPaths, c.fixture, c.infra)
			entries := s4Diff(t, nil, s4Add(c.files...))
			got, err := CheckScope(ScopeInput{Role: "producer", Entries: entries, Exemption: s4Producer("refactor", "human"), Platform: plat, Effective: eff})
			if err != nil {
				t.Fatalf("CheckScope: %v", err)
			}
			want := map[string]s4Want{}
			for _, f := range c.files {
				want[f] = s4NeedNot("ROLE", "ESC")
			}
			s4Check(t, got, want)
		})
	}
}

// Twins of the above (pass today and must keep passing): an ordinary source file stays clean for the refactor
// producer, also under the extended repository policy; a metadata producer (not characterization-judged) keeps
// touching metadata paths under test/** (TestCheckScope_E2EProtectedForStrangeCompany).
func TestCheckScope_RefactorAndMetadataTwins_D21_22AC11(t *testing.T) {
	t.Run("refactor_ordinary_source_clean", func(t *testing.T) {
		plat, eff := s4MergeExtra(t, "demo", []string{"src/**/*.test.ts"}, nil, []string{"**/__mocks__/**"})
		entries := s4Diff(t, []string{"src/main.go"}, s4Add("src/main.go", "src/internal/app/app.go", "docs/a.md"))
		got, err := CheckScope(ScopeInput{Role: "producer", Entries: entries, Exemption: s4Producer("refactor", "human"), Platform: plat, Effective: eff})
		if err != nil || len(got) != 0 {
			t.Errorf("violations %s, err %v, want a clean diff", rvKeysD21(got), err)
		}
	})
	t.Run("metadata_producer_test_paths_unchanged", func(t *testing.T) {
		plat, eff := s4Effective(t, s4Strange(t, "strange-company"))
		entries := s4Diff(t, nil, s4Add("test/fixtures/a.yaml", "test/integration/y.yaml", "src/internal/verify/testdata/x.json"))
		got, err := CheckScope(ScopeInput{Role: "producer", Entries: entries, Exemption: s4Producer("metadata", "auto"), Platform: plat, Effective: eff})
		if err != nil || len(got) != 0 {
			t.Errorf("violations %s, err %v, want a clean diff", rvKeysD21(got), err)
		}
	})
}

func rvKeysD21(vs []Violation) string {
	out := ""
	for _, v := range vs {
		out += " " + v.Path + "=" + s4Key(v)
	}
	return out
}

// 22-AC-33 (D21 N2): an entry with old or new mode 160000 is GITLINK_DENIED for the tester, the implementer and the
// producer; a regular file at the same path is not.
func TestCheckScope_GitlinkDeniedForEveryRole_D21_22AC33(t *testing.T) {
	plat, eff := s4Effective(t, s4PolicySpec{Name: "demo", Allowed: s4AllClasses})
	roles := []struct {
		role string
		ex   *ExemptionDecision
	}{{"tester", nil}, {"implementer", nil}, {"producer", s4Producer("documentation-only", "auto")}}
	entries := map[string]DiffEntry{
		"added":    {Path: "docs/sub", Status: "A", OldMode: "000000", NewMode: "160000"},
		"modified": {Path: "docs/sub", Status: "M", OldMode: "160000", NewMode: "160000"},
		"deleted":  {Path: "docs/sub", Status: "D", OldMode: "160000", NewMode: "000000"},
		"replaced": {Path: "docs/sub", Status: "T", OldMode: "160000", NewMode: "100644"},
		"became":   {Path: "docs/sub", Status: "T", OldMode: "100644", NewMode: "160000"},
	}
	for _, r := range roles {
		for name, e := range entries {
			t.Run(r.role+"/"+name, func(t *testing.T) {
				got, err := CheckScope(ScopeInput{Role: r.role, Entries: []DiffEntry{e}, Exemption: r.ex, Platform: plat, Effective: eff})
				if err != nil {
					t.Fatalf("CheckScope: %v", err)
				}
				for _, v := range got {
					if v.Code == "GITLINK_DENIED" && v.Path == "docs/sub" {
						return
					}
				}
				t.Errorf("violations %s, want GITLINK_DENIED for docs/sub", rvKeysD21(got))
			})
		}
		t.Run(r.role+"/regular_file_twin", func(t *testing.T) {
			e := DiffEntry{Path: "docs/sub", Status: "M", OldMode: "100644", NewMode: "100644"}
			got, err := CheckScope(ScopeInput{Role: r.role, Entries: []DiffEntry{e}, Exemption: r.ex, Platform: plat, Effective: eff})
			if err != nil || len(got) != 0 {
				t.Errorf("violations %s, err %v, want a clean regular file", rvKeysD21(got), err)
			}
		})
	}
}

// 22-AC-11 (D21 c): CheckScope refuses an exemption decision that ValidateExemption cannot have produced: granted
// with human approval, pending_approval with auto, an unknown status, auto for a class the platform marks human.
// Twins: granted/auto for an auto class and pending_approval/human for a human class are judged.
//
// Not encoded: "auto for a class the repository's requireHumanApproval lists" would contradict the merged
// TestCheckScope_DocumentationOnlyProducer_22AC11/stale_auto_decision, which expects an escalation, not an error.
func TestCheckScope_InconsistentExemptionDecisionRefused_D21_22AC11(t *testing.T) {
	plat, eff := s4Effective(t, s4PolicySpec{Name: "demo", Allowed: s4AllClasses})
	entries := s4Diff(t, nil, s4Add("docs/a.md"))
	run := func(d *ExemptionDecision) ([]Violation, error) {
		return CheckScope(ScopeInput{Role: "producer", Entries: entries, Exemption: d, Platform: plat, Effective: eff})
	}
	bad := map[string]*ExemptionDecision{
		"granted_human":                {Class: "documentation-only", Status: "granted", Approval: "human", PolicyRevision: s4Revision},
		"pending_approval_auto":        {Class: "documentation-only", Status: "pending_approval", Approval: "auto", PolicyRevision: s4Revision},
		"unknown_status_auto":          {Class: "documentation-only", Status: "bogus", Approval: "auto", PolicyRevision: s4Revision},
		"unknown_status_human":         {Class: "benchmark", Status: "bogus", Approval: "human", PolicyRevision: s4Revision},
		"empty_status":                 {Class: "documentation-only", Approval: "auto", PolicyRevision: s4Revision},
		"auto_for_platform_human":      {Class: "benchmark", Status: "granted", Approval: "auto", PolicyRevision: s4Revision},
		"auto_for_platform_human_migr": {Class: "migration", Status: "granted", Approval: "auto", PolicyRevision: s4Revision},
	}
	for name, d := range bad {
		t.Run(name, func(t *testing.T) {
			if got, err := run(d); err == nil {
				t.Errorf("no error (violations %s)", rvKeysD21(got))
			}
		})
	}
	t.Run("twin_granted_auto_for_auto_class", func(t *testing.T) {
		if got, err := run(s4Producer("documentation-only", "auto")); err != nil || len(got) != 0 {
			t.Errorf("violations %s, err %v, want a clean docs/a.md", rvKeysD21(got), err)
		}
	})
	t.Run("twin_pending_human_for_human_class", func(t *testing.T) {
		if _, err := run(s4Producer("benchmark", "human")); err != nil {
			t.Errorf("err %v, want the decision judged", err)
		}
	})
}
