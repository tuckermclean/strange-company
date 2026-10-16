package lint_test

import (
	"os"
	"path/filepath"
	"testing"
)

// 01-AC-1: plan 01 section 6.1 allows mode 100755 only for bootstrap, toolbox/bin/* and
// test/e2e/**/run. A .sh under test/e2e is not allowed to be executable.
func TestLayoutCheck_ExecutableFilesFollowPlan_01AC1(t *testing.T) {
	t.Run("e2e non-run file executable", func(t *testing.T) {
		dir := layoutTree(t)
		write(t, dir, "test/e2e/x/foo.sh", "#!/bin/sh\n")
		if err := os.Chmod(filepath.Join(dir, "test/e2e/x/foo.sh"), 0o755); err != nil {
			t.Fatal(err)
		}
		git(t, dir, "add", "-A")
		r := mustLayout(t, dir)
		ranRules(t, r, ruleExec)
		found := false
		for _, f := range r.Findings {
			if f.Rule == ruleExec && f.Severity != "warning" && contains(f.Path+f.Message, "foo.sh") {
				found = true
			}
		}
		if !found {
			t.Errorf("executable test/e2e/x/foo.sh not reported under %s: %v", ruleExec, r.Findings)
		}
	})
	t.Run("e2e run executable passes", func(t *testing.T) {
		dir := layoutTree(t) // fixture already tracks test/e2e/x/run at 100755
		r := mustLayout(t, dir)
		ranRules(t, r, ruleExec)
		for _, f := range r.Findings {
			if f.Rule == ruleExec {
				t.Errorf("unexpected %s finding: %v", ruleExec, f)
			}
		}
	})
}

// 01-AC-1: the real tree complies with the executable-files rule.
func TestRealTree_NoStrayExecutables_01AC1(t *testing.T) {
	root := repoRoot(t)
	matches, _ := filepath.Glob(filepath.Join(root, "test", "e2e", "wrapper", "*.sh"))
	if len(matches) == 0 {
		t.Fatal("no wrapper scripts found")
	}
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode()&0o111 != 0 {
			t.Errorf("%s is executable; plan 01 section 6.1 forbids it", m)
		}
	}
}

// 01-AC-14: lettered AC ids (15-AC-26b, 41-AC-5a) are distinct ids; a repeat of one is a duplicate.
func TestDagCheck_LetteredACIds_01AC14(t *testing.T) {
	with := func(extra string) map[string]string {
		p := validPlans()
		p["01-one.md"] = plan01 + extra
		return p
	}
	t.Run("same lettered id twice", func(t *testing.T) {
		dir := dagTree(t, dagYAML, with("\n- **01-AC-3b** first.\n- **01-AC-3b** again.\n"))
		r := mustDag(t, dir)
		ranRules(t, r, dagRules...)
		if !hasRule(r, "DAG_AC_DUPLICATE") {
			t.Errorf("duplicate 01-AC-3b not reported: %v", r.Findings)
		}
	})
	t.Run("3a and 3b coexist", func(t *testing.T) {
		dir := dagTree(t, dagYAML, with("\n- **01-AC-3a** first.\n- **01-AC-3b** second.\n"))
		r := mustDag(t, dir)
		ranRules(t, r, dagRules...)
		if hasRule(r, "DAG_AC_DUPLICATE") {
			t.Errorf("01-AC-3a and 01-AC-3b wrongly duplicate: %v", r.Findings)
		}
	})
}

// 01-AC-12: `make <target>` anywhere inside a backtick span is checked.
func TestLayoutCheck_MakeTargetInsideSpan_01AC12(t *testing.T) {
	const span = "Run `./bootstrap exec -- make no-such-target` first.\n"
	t.Run("unknown target in span", func(t *testing.T) {
		dir := layoutTree(t)
		write(t, dir, "plans/02-fixture.md", span)
		git(t, dir, "add", "-A")
		r := mustLayout(t, dir)
		ranRules(t, r, ruleMakeTgt)
		if !hasRule(r, ruleMakeTgt) {
			t.Errorf("unknown target inside span not reported: %v", r.Findings)
		}
	})
	t.Run("planned target in span", func(t *testing.T) {
		dir := layoutTree(t)
		write(t, dir, "plans/02-fixture.md", span)
		write(t, dir, "test/fixtures/layout/planned-paths.txt", "make no-such-target\n")
		git(t, dir, "add", "-A")
		r := mustLayout(t, dir)
		ranRules(t, r, ruleMakeTgt)
		if hasRule(r, ruleMakeTgt) {
			t.Errorf("planned target reported: %v", r.Findings)
		}
	})
}
