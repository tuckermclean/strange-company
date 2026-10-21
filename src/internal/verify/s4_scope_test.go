package verify

import (
	"testing"
)

// 22-AC-11 (and the path scope of plan 22 section 9.2 step 5): CheckScope on real Git diffs, every class of the real
// platform policy crossed with a set of path classes. Each case lists, per changed path, the violations that must
// (and must not) be reported; a path without an entry must be clean.

func s4Producer(class, approval string) *ExemptionDecision {
	return &ExemptionDecision{Class: class, Status: map[string]string{"auto": "granted", "human": "pending_approval"}[approval], Approval: approval, PolicyRevision: s4Revision}
}

type s4ScopeCase struct {
	name      string
	policy    s4PolicySpec
	exemption *ExemptionDecision
	baseExtra []string
	changes   map[string]string
	want      map[string]s4Want
}

func s4RunScope(t *testing.T, cases []s4ScopeCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plat, eff := s4Effective(t, c.policy)
			entries := s4Diff(t, c.baseExtra, c.changes)
			got, err := CheckScope(ScopeInput{Role: "producer", Entries: entries, Exemption: c.exemption, Platform: plat, Effective: eff})
			if err != nil {
				t.Fatalf("CheckScope: %v", err)
			}
			s4Check(t, got, c.want)
		})
	}
}

var s4AllClasses = []string{"documentation-only", "research", "infrastructure-config", "migration", "benchmark", "metadata", "refactor"}

// 22-AC-11: a documentation-only producer diff touching src/main.go is ROLE_DIFF_VIOLATION; paths of other human
// classes need escalation; protected paths are PROTECTED_PATH_VIOLATION; paths of the class are clean.
func TestCheckScope_DocumentationOnlyProducer_22AC11(t *testing.T) {
	ps := s4PolicySpec{Name: "demo", Allowed: s4AllClasses}
	s4RunScope(t, []s4ScopeCase{{
		name: "auto", policy: ps, exemption: s4Producer("documentation-only", "auto"),
		baseExtra: []string{"src/main.go"},
		changes: s4Add("docs/a.md", "README.md", "docs/guide/b.md", "src/main.go", "charts/x/values.yaml", "gitops/apps/a.yaml", "deploy/a.yaml",
			"docs/migrations/a.md", "docs/skills/a.md", "context/x.md", "docs/AGENTS.md", "docs/SOUL.md"),
		want: map[string]s4Want{
			"src/main.go":          s4NeedNot("ROLE", "ESC"),
			"charts/x/values.yaml": s4Need("ESC"),
			"gitops/apps/a.yaml":   s4Need("ESC"),
			"deploy/a.yaml":        s4Need("ESC"),
			"docs/migrations/a.md": s4Need("ESC"),
			"docs/skills/a.md":     s4NeedNot("ROLE", "ESC"),
			"context/x.md":         s4NeedNot("ROLE", "ESC"),
			"docs/AGENTS.md":       s4Need("PROT"),
			"docs/SOUL.md":         s4NeedNot("ROLE", "ESC"),
		},
	}, {
		// A deletion and a rename out of the class are changed paths like any other.
		name: "auto_deletion_and_rename_out", policy: ps, exemption: s4Producer("documentation-only", "auto"),
		baseExtra: []string{"src/other.go"},
		changes:   map[string]string{"src/old.go": "", "docs/old.md": "", "src/new_name.go": "package old\n"},
		want: map[string]s4Want{
			"src/old.go":      s4NeedNot("ROLE", "ESC"),
			"src/new_name.go": s4NeedNot("ROLE", "ESC"),
		},
	}, {
		// With requireHumanApproval the task's approval is human: escalation never applies, the path scope still does.
		name: "human_by_repository", policy: s4PolicySpec{Name: "demo", Allowed: s4AllClasses, RequireHuman: []string{"documentation-only"}},
		exemption: s4Producer("documentation-only", "human"),
		baseExtra: []string{"src/main.go"},
		changes:   s4Add("docs/a.md", "src/main.go", "charts/x/values.yaml", "docs/migrations/a.md"),
		want: map[string]s4Want{
			"src/main.go":          s4NeedNot("ROLE", "ESC"),
			"charts/x/values.yaml": s4NeedNot("ROLE", "ESC"),
			"docs/migrations/a.md": s4NeedNot("ROLE", "ESC"),
		},
	}, {
		// A decision recorded as auto while the repository now requires human approval for the class: the path
		// needs human (its own class is human) and the task is auto.
		name: "stale_auto_decision", policy: s4PolicySpec{Name: "demo", Allowed: s4AllClasses, RequireHuman: []string{"documentation-only"}},
		exemption: s4Producer("documentation-only", "auto"),
		changes:   s4Add("docs/a.md"),
		want:      map[string]s4Want{"docs/a.md": s4Need("ESC")},
	}})
}

// 22-AC-11: a metadata task whose diff touches charts/x/values.yaml is ROLE_DIFF_VIOLATION with the sub-code
// exemption_escalation_required (the path belongs to the human class infrastructure-config), even when the
// repository does not allow that class. A path of two auto classes stays clean, and a repository that requires
// human approval for documentation-only turns the overlap with metadata into an escalation (no laundering).
func TestCheckScope_MetadataProducerEscalation_22AC11(t *testing.T) {
	all := s4PolicySpec{Name: "demo", Allowed: s4AllClasses}
	metaOnly := s4PolicySpec{Name: "demo", Allowed: []string{"metadata"}}
	laundering := s4PolicySpec{Name: "demo", Allowed: s4AllClasses, RequireHuman: []string{"documentation-only"}}
	good := s4Add("config/a.yaml", "data/b.json", "docs/schema.yaml", "deep/er/c.yml")
	s4RunScope(t, []s4ScopeCase{{
		name: "all_classes_allowed", policy: all, exemption: s4Producer("metadata", "auto"),
		baseExtra: []string{"src/main.go"},
		changes:   mergeChanges(good, s4Add("charts/x/values.yaml", "gitops/x/values.yaml", "deploy/a.yaml", "db/migrations/a.yaml", "go.mod", "package.json", "src/main.go", "docs/x.md", ".github/wf.yaml", ".woodpecker/ci.yaml")),
		want: map[string]s4Want{
			"charts/x/values.yaml": s4Need("ESC"),
			"gitops/x/values.yaml": s4Need("ESC"),
			"deploy/a.yaml":        s4Need("ESC"),
			"db/migrations/a.yaml": s4Need("ESC"),
			"go.mod":               s4NeedNot("ROLE", "ESC"),
			"package.json":         s4NeedNot("ROLE", "ESC"),
			"src/main.go":          s4NeedNot("ROLE", "ESC"),
			"docs/x.md":            s4NeedNot("ROLE", "ESC"),
			".github/wf.yaml":      s4Need("PROT"),
			".woodpecker/ci.yaml":  s4Need("PROT"),
		},
	}, {
		// The repository allows metadata only; escalation still looks at every platform class.
		name: "only_metadata_allowed", policy: metaOnly, exemption: s4Producer("metadata", "auto"),
		changes: s4Add("config/a.yaml", "charts/x/values.yaml"),
		want:    map[string]s4Want{"charts/x/values.yaml": s4Need("ESC")},
	}, {
		name: "overlap_with_auto_class_is_clean", policy: all, exemption: s4Producer("metadata", "auto"),
		changes: good, want: map[string]s4Want{},
	}, {
		name: "overlap_with_class_made_human_by_repository", policy: laundering, exemption: s4Producer("metadata", "auto"),
		changes: s4Add("config/a.yaml", "docs/schema.yaml", "docs/deep/x.json"),
		want: map[string]s4Want{
			"docs/schema.yaml": s4Need("ESC"),
			"docs/deep/x.json": s4Need("ESC"),
		},
	}, {
		// A human metadata task (requireHumanApproval for metadata) may touch the human classes' paths.
		name: "metadata_made_human", policy: s4PolicySpec{Name: "demo", Allowed: s4AllClasses, RequireHuman: []string{"metadata"}},
		exemption: s4Producer("metadata", "human"),
		changes:   s4Add("config/a.yaml", "charts/x/values.yaml"),
		want:      map[string]s4Want{"charts/x/values.yaml": s4NeedNot("ROLE", "ESC")},
	}})
}

func mergeChanges(ms ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// 22-AC-11: the remaining classes crossed with path classes: research changes nothing, human classes are scoped by
// their allowedPaths and deniedPaths, the catch-all classes by their denied paths and the protected set.
func TestCheckScope_ClassesByPathClass_22AC11(t *testing.T) {
	ps := s4PolicySpec{Name: "demo", Allowed: s4AllClasses}
	s4RunScope(t, []s4ScopeCase{{
		name: "research_any_change", policy: ps, exemption: s4Producer("research", "auto"),
		changes: s4Add("docs/a.md", "src/main.go"),
		want: map[string]s4Want{
			"docs/a.md":   s4NeedNot("ROLE", "ESC"),
			"src/main.go": s4NeedNot("ROLE", "ESC"),
		},
	}, {
		name: "research_no_change", policy: ps, exemption: s4Producer("research", "auto"),
		changes: map[string]string{}, want: map[string]s4Want{},
	}, {
		name: "infrastructure_config", policy: ps, exemption: s4Producer("infrastructure-config", "human"),
		changes: s4Add("gitops/apps/a.yaml", "charts/x/values.yaml", "deploy/a.yaml", "ansible/p.yaml", "tofu/main.tf", "docs/a.md", "src/main.go",
			"gitops/clusters/c/flux-system/g.yaml", "gitops/platform/admission/p.yaml", "charts/x/secrets.sops.yaml"),
		want: map[string]s4Want{
			"docs/a.md":                            s4NeedNot("ROLE", "ESC"),
			"src/main.go":                          s4NeedNot("ROLE", "ESC"),
			"gitops/clusters/c/flux-system/g.yaml": s4Need("PROT"), // the real repository policy protects gitops/clusters/**
			"gitops/platform/admission/p.yaml":     s4Need("PROT"),
			"charts/x/secrets.sops.yaml":           s4Need("PROT"),
		},
	}, {
		name: "migration", policy: ps, exemption: s4Producer("migration", "human"),
		changes: s4Add("db/migrations/001.sql", "a/b/migrations/c.sql", "db/schema.sql", "migrations.sql"),
		want: map[string]s4Want{
			"db/schema.sql":  s4NeedNot("ROLE", "ESC"),
			"migrations.sql": s4NeedNot("ROLE", "ESC"),
		},
	}, {
		name: "benchmark_catch_all", policy: ps, exemption: s4Producer("benchmark", "human"),
		changes: s4Add("src/main.go", "docs/a.md", "bench/x.go", ".woodpecker/ci.yaml", "docs/AGENTS.md", "policy/x.yaml"),
		want: map[string]s4Want{
			".woodpecker/ci.yaml": s4Need("PROT"),
			"docs/AGENTS.md":      s4Need("PROT"),
			"policy/x.yaml":       s4Need("PROT"),
		},
	}, {
		name: "refactor_catch_all_minus_tests", policy: ps, exemption: s4Producer("refactor", "human"),
		changes: s4Add("src/main.go", "docs/a.md", "src/main_test.go", "a/testdata/x.golden", "pkg/tests/x.go", "pkg/test/y.go", "web/__tests__/z.js", ".github/wf.yaml"),
		want: map[string]s4Want{
			"src/main_test.go":    s4NeedNot("ROLE", "ESC"),
			"a/testdata/x.golden": s4NeedNot("ROLE", "ESC"),
			"pkg/tests/x.go":      s4NeedNot("ROLE", "ESC"),
			"pkg/test/y.go":       s4NeedNot("ROLE", "ESC"),
			"web/__tests__/z.js":  s4NeedNot("ROLE", "ESC"),
			".github/wf.yaml":     s4Need("PROT"),
		},
	}})
}

// 22-AC-33 / plan 22 section 9.3: a tester or implementer diff touching a protected path is
// PROTECTED_PATH_VIOLATION; any other path is clean (no class applies to these roles). Real Git raw entries.
func TestCheckScope_TesterAndImplementerProtectedPaths_22AC33(t *testing.T) {
	plat, eff := s4Effective(t, s4PolicySpec{Name: "demo", Allowed: s4AllClasses})
	changes := s4Add(".woodpecker/ci.yaml", "docs/AGENTS.md", "policy/verification.yaml", "contracts/x.schema.yaml", ".claude/settings.json",
		"src/main.go", "src/main_test.go", "docs/a.md", "woodpecker/ci.yaml", "Makefile")
	want := map[string]s4Want{
		".woodpecker/ci.yaml":      s4Need("PROT"),
		"docs/AGENTS.md":           s4Need("PROT"),
		"policy/verification.yaml": s4Need("PROT"),
		"contracts/x.schema.yaml":  s4Need("PROT"),
		".claude/settings.json":    s4Need("PROT"),
		"Makefile":                 s4Need("PROT"), // the real repository policy protects it
	}
	for _, role := range []string{"tester", "implementer"} {
		entries := s4Diff(t, nil, changes)
		got, err := CheckScope(ScopeInput{Role: role, Entries: entries, Platform: plat, Effective: eff})
		if err != nil {
			t.Fatalf("%s: %v", role, err)
		}
		t.Run(role, func(t *testing.T) { s4Check(t, got, want) })
	}
}

// Fail closed (22-R-3): roles that have no diff to judge, a producer without a recorded exemption, an exemption of
// a class the platform does not define.
func TestCheckScope_InvalidInputsFailClosed_22AC11(t *testing.T) {
	plat, eff := s4Effective(t, s4PolicySpec{Name: "demo", Allowed: s4AllClasses})
	entries := s4Diff(t, nil, s4Add("docs/a.md"))
	for name, in := range map[string]ScopeInput{
		"reviewer":             {Role: "reviewer", Entries: entries},
		"empty_role":           {Role: "", Entries: entries},
		"producer_upper":       {Role: "Producer", Entries: entries, Exemption: s4Producer("documentation-only", "auto")},
		"producer_no_decision": {Role: "producer", Entries: entries},
		"unknown_class":        {Role: "producer", Entries: entries, Exemption: s4Producer("no-such-class", "auto")},
		"unknown_approval":     {Role: "producer", Entries: entries, Exemption: &ExemptionDecision{Class: "documentation-only", Approval: "maybe"}},
		"empty_approval":       {Role: "producer", Entries: entries, Exemption: &ExemptionDecision{Class: "documentation-only"}},
	} {
		in.Platform, in.Effective = plat, eff
		got, err := CheckScope(in)
		if err == nil {
			t.Errorf("%s: no error (violations %v)", name, got)
		}
	}
	// Twin: the well-formed producer input is judged.
	if got, err := CheckScope(ScopeInput{Role: "producer", Entries: entries, Exemption: s4Producer("documentation-only", "auto"), Platform: plat, Effective: eff}); err != nil || len(got) != 0 {
		t.Errorf("twin: violations %v, err %v, want a clean docs/a.md", got, err)
	}
}

// The violations are a deterministic function of the entries: same order on every call, and the result does not
// depend on the order of the entries.
func TestCheckScope_DeterministicOrder_22AC11(t *testing.T) {
	plat, eff := s4Effective(t, s4PolicySpec{Name: "demo", Allowed: s4AllClasses})
	entries := s4Diff(t, []string{"src/main.go"}, s4Add("src/main.go", "charts/x/values.yaml", ".github/wf.yaml", "docs/a.md", "docs/skills/s.md"))
	in := ScopeInput{Role: "producer", Entries: entries, Exemption: s4Producer("documentation-only", "auto"), Platform: plat, Effective: eff}
	first, err := CheckScope(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) < 3 {
		t.Fatalf("violations = %v, want at least three", first)
	}
	rev := append([]DiffEntry(nil), entries...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	in2 := in
	in2.Entries = rev
	for i := 0; i < 3; i++ {
		again, err := CheckScope(in2)
		if err != nil || jsonOf(again) != jsonOf(first) {
			t.Fatalf("run %d: %s (err %v), want %s", i, jsonOf(again), err, jsonOf(first))
		}
	}
}
