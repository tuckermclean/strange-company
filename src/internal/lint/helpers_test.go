package lint_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"strange.company/sc/internal/lint"
)

// repoRoot finds the real repository root (the directory holding SPEC.md and plans/).
func repoRoot(t *testing.T) string {
	t.Helper()
	d, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(d, "SPEC.md")); err == nil {
			if _, err := os.Stat(filepath.Join(d, "plans", "dag.yaml")); err == nil {
				return d
			}
		}
		p := filepath.Dir(d)
		if p == d {
			t.Fatal("repository root not found")
		}
		d = p
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=t@example.invalid", "-c", "user.name=t", "-c", "core.fileMode=true"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// layoutTree copies testdata/layout/valid into a fresh git repository (tracked files are what
// layout-check judges). Fixture files are stored 0644; modes that the real tree needs are set here.
func layoutTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "layout", "valid"))); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"bootstrap", "toolbox/bin/tool", "test/e2e/x/run"} {
		if err := os.Chmod(filepath.Join(dir, f), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	git(t, dir, "init", "-q")
	git(t, dir, "add", "-A")
	return dir
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func hasRule(r lint.Result, rule string) bool {
	for _, f := range r.Findings {
		if f.Rule == rule {
			return true
		}
	}
	return false
}

func errorsOf(r lint.Result) []lint.Finding {
	var out []lint.Finding
	for _, f := range r.Findings {
		if f.Severity != "warning" {
			out = append(out, f)
		}
	}
	return out
}

func ranRules(t *testing.T, r lint.Result, want ...string) {
	t.Helper()
	have := map[string]bool{}
	for _, id := range r.Rules {
		have[id] = true
	}
	for _, id := range want {
		if !have[id] {
			t.Errorf("rule %q was not evaluated (evaluated: %v)", id, r.Rules)
		}
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
