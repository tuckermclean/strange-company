package lint_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"strange.company/sc/internal/lint"
)

const dagYAML = `apiVersion: strange.company/v1alpha1
kind: DependencyGraph
metadata:
  name: fixture
nodes:
  - {id: A, phase: 1, name: alpha, plans: ["01"], deps: []}
  - {id: B, phase: 1, name: beta, plans: ["01", "02"], deps: [A]}
  - {id: C, phase: 1, name: gamma, plans: ["03"], deps: []}
`

const plan01 = `# Plan 01

### GATE-A

- **01-AC-1** alpha works.

### GATE-B

- **01-AC-2** beta works.
`

const plan02 = `# Plan 02

### GATE-B

- **02-AC-1** beta also works.
`

const plan03 = `# Plan 03

### GATE-C

- [ ] **03-AC-1** gamma works.
`

func dagTree(t *testing.T, dag string, plans map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "plans/dag.yaml", dag)
	for name, body := range plans {
		write(t, dir, "plans/"+name, body)
	}
	return dir
}

func validPlans() map[string]string {
	return map[string]string{"01-one.md": plan01, "02-two.md": plan02, "03-three.md": plan03}
}

func mustDag(t *testing.T, dir string) lint.Result {
	t.Helper()
	r, err := lint.DagCheck(dir)
	if err != nil {
		t.Fatalf("DagCheck: %v", err)
	}
	return r
}

var dagRules = []string{"DAG_CYCLE", "DAG_UNKNOWN_NODE", "DAG_GATE_MISSING", "DAG_GATE_UNLISTED", "DAG_AC_DUPLICATE"}

// 01-AC-14: positive fixture and the committed plans/dag.yaml.
func TestDagCheck_ValidFixturePasses_01AC14(t *testing.T) {
	r := mustDag(t, dagTree(t, dagYAML, validPlans()))
	ranRules(t, r, dagRules...)
	if f := errorsOf(r); len(f) != 0 {
		t.Errorf("valid fixture has findings: %v", f)
	}
}

func TestDagCheck_RealTreePasses_01AC14(t *testing.T) {
	r := mustDag(t, repoRoot(t))
	ranRules(t, r, dagRules...)
	for _, f := range errorsOf(r) {
		t.Errorf("real tree: %s", f)
	}
}

// 01-AC-14: one violating tree per rule; the rule ID is in the finding and in the command output.
func TestDagCheck_Negatives_01AC14(t *testing.T) {
	cycle := `apiVersion: strange.company/v1alpha1
kind: DependencyGraph
metadata: {name: fixture}
nodes:
  - {id: A, phase: 1, name: alpha, plans: ["01"], deps: [B]}
  - {id: B, phase: 1, name: beta, plans: ["01", "02"], deps: [A]}
  - {id: C, phase: 1, name: gamma, plans: ["03"], deps: []}
`
	unknownDep := `apiVersion: strange.company/v1alpha1
kind: DependencyGraph
metadata: {name: fixture}
nodes:
  - {id: A, phase: 1, name: alpha, plans: ["01"], deps: []}
  - {id: B, phase: 1, name: beta, plans: ["01", "02"], deps: [Z]}
  - {id: C, phase: 1, name: gamma, plans: ["03"], deps: []}
`
	unknownAfter := `apiVersion: strange.company/v1alpha1
kind: DependencyGraph
metadata: {name: fixture}
nodes:
  - {id: A, phase: 1, name: alpha, plans: ["01"], deps: []}
  - {id: B, phase: 1, name: beta, plans: ["01", "02"], deps: [A], after: [Z]}
  - {id: C, phase: 1, name: gamma, plans: ["03"], deps: []}
`
	with := func(name, body string) map[string]string {
		p := validPlans()
		p[name] = body
		return p
	}
	cases := []struct {
		name, rule, dag string
		plans           map[string]string
	}{
		{"two-node cycle", "DAG_CYCLE", cycle, validPlans()},
		{"unknown dependency", "DAG_UNKNOWN_NODE", unknownDep, validPlans()},
		{"unknown after", "DAG_UNKNOWN_NODE", unknownAfter, validPlans()},
		{"gate missing", "DAG_GATE_MISSING", dagYAML, with("02-two.md", "# Plan 02\n\n- **02-AC-1** no gate heading.\n")},
		{"gate unlisted", "DAG_GATE_UNLISTED", dagYAML, with("02-two.md", plan02+"\n### GATE-A\n\n- **02-AC-2** wrong plan.\n")},
		{"duplicate AC", "DAG_AC_DUPLICATE", dagYAML, with("01-one.md", plan01+"\n- **01-AC-1** defined again.\n")},
		{"duplicate AC across plans", "DAG_AC_DUPLICATE", dagYAML, with("02-two.md", plan02+"\n- **01-AC-2** defined in another plan.\n")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := dagTree(t, c.dag, c.plans)
			r := mustDag(t, dir)
			found := false
			for _, f := range r.Findings {
				if f.Rule == c.rule && f.Severity != "warning" {
					found = true
				}
			}
			if !found {
				t.Errorf("want error finding %s, got %v", c.rule, r.Findings)
			}
			var out, errb bytes.Buffer
			if code := lint.Main([]string{"dag", "--root", dir}, &out, &errb); code == 0 {
				t.Errorf("sc-lint dag exit = 0, want non-zero")
			}
			if !contains(out.String()+errb.String(), c.rule) {
				t.Errorf("output lacks %s:\n%s%s", c.rule, out.String(), errb.String())
			}
		})
	}
}

// 01-AC-14: a `uses:` marker naming a component of a non-upstream node is a warning, not a failure.
func TestDagCheck_UsesNonUpstreamIsWarning_01AC14(t *testing.T) {
	dag := dagYAML + "components:\n  comp-a: A\n  comp-c: C\n"
	p := validPlans()
	p["01-one.md"] = plan01 + "\n- **01-AC-3** beta uses things. <!-- uses: comp-a,comp-c -->\n"
	dir := dagTree(t, dag, p)
	r := mustDag(t, dir)
	ranRules(t, r, dagRules...)
	warned := 0
	for _, f := range r.Findings {
		if f.Severity == "warning" {
			warned++
		}
	}
	if warned != 1 {
		t.Errorf("want exactly 1 warning (comp-c; comp-a is upstream), got %v", r.Findings)
	}
	if f := errorsOf(r); len(f) != 0 {
		t.Errorf("a non-upstream uses marker must not fail: %v", f)
	}
	var out, errb bytes.Buffer
	if code := lint.Main([]string{"dag", "--root", dir}, &out, &errb); code != 0 {
		t.Errorf("exit = %d, want 0 with a warning only:\n%s%s", code, out.String(), errb.String())
	}
}

// Plan 01 section 7.1: dag-check writes out/dag-check/report.json.
func TestDagCheck_WritesReport_01AC14(t *testing.T) {
	dir := dagTree(t, dagYAML, validPlans())
	var out, errb bytes.Buffer
	lint.Main([]string{"dag", "--root", dir}, &out, &errb)
	if _, err := os.Stat(filepath.Join(dir, "out", "dag-check", "report.json")); err != nil {
		t.Errorf("report.json: %v", err)
	}
}
