package policy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// F06 slice S4, decision D20 (22-AC-33): "a strange-company repository policy whose elevatedPaths omits
// src/internal/runner/** fails make policy-check with ENFORCEMENT_PLANE_NOT_ELEVATED". make policy-check runs
// sc-policy check, which calls Check, so the invariant must hold in Check and not only in MergePolicy.
//
// Every tree is built in t.TempDir() from the real policy/*.yaml platform files and the real .strange-company
// repository documents, with one edit per case.

const d20Glob = "src/internal/runner/**"

// d20Tree writes a repo-side policy tree. name is metadata.name of the repository policy; mutate edits spec.
func d20Tree(t *testing.T, name string, mutate func(spec map[string]any)) string {
	t.Helper()
	root, dir := repoRoot(t), t.TempDir()
	copyFile := func(src, dst string) {
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"ci.yaml", "test-quarantine.yaml", "verification.yaml"} {
		copyFile(filepath.Join(root, "policy", f), filepath.Join(dir, f))
	}
	copyFile(filepath.Join(root, ".strange-company", "validation.yaml"), filepath.Join(dir, ".strange-company", "validation.yaml"))
	doc := s4Repo(t, name, mutate)
	b, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".strange-company", "policy.yaml"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func d20DropRunner(spec map[string]any) {
	s4SetElevated(spec, s4Without(s4Elevated(spec), d20Glob))
}

func d20PlaneIssues(is []Issue) []Issue {
	var out []Issue
	for _, i := range is {
		if i.Code == s4Code {
			out = append(out, i)
		}
	}
	return out
}

// 22-AC-33: Check reports exactly one ENFORCEMENT_PLANE_NOT_ELEVATED issue, naming the omitted glob.
func TestCheck_PlaneGlobOmitted_D20_22AC33(t *testing.T) {
	_, is := Check(d20Tree(t, "strange-company", d20DropRunner), opts(t, false))
	got := d20PlaneIssues(is)
	if len(got) != 1 {
		t.Fatalf("want exactly one %s issue, got %d in %v", s4Code, len(got), s4Plain(is))
	}
	if !strings.Contains(got[0].Message, d20Glob) {
		t.Errorf("issue does not name %s: %s", d20Glob, got[0].String())
	}
	if len(is) != 1 {
		t.Errorf("want no other issue, got %v", s4Plain(is))
	}
}

// 22-AC-33: the invariant applies to the repository named strange-company only.
func TestCheck_PlaneGlobOmittedOtherRepository_D20_22AC33(t *testing.T) {
	_, is := Check(d20Tree(t, "demo", d20DropRunner), opts(t, false))
	if got := d20PlaneIssues(is); len(got) != 0 {
		t.Errorf("another repository name must not get %s: %v", s4Code, s4Plain(got))
	}
	if len(is) != 0 {
		t.Errorf("setup: the renamed tree must be accepted, got %v", s4Plain(is))
	}
}

// 22-AC-33: the full plane covered (the real policy) reports nothing.
func TestCheck_PlaneCovered_D20_22AC33(t *testing.T) {
	_, is := Check(d20Tree(t, "strange-company", nil), opts(t, false))
	if len(is) != 0 {
		t.Errorf("real policy tree rejected: %v", s4Plain(is))
	}
}

// 22-AC-33: the plane may be covered by protectedPaths instead of elevatedPaths (D3).
func TestCheck_PlaneCoveredByProtectedPaths_D20_22AC33(t *testing.T) {
	_, is := Check(d20Tree(t, "strange-company", func(spec map[string]any) {
		d20DropRunner(spec)
		s4AddProtected(spec, d20Glob)
	}), opts(t, false))
	if got := d20PlaneIssues(is); len(got) != 0 {
		t.Errorf("a glob covered by protectedPaths is reported: %v", s4Plain(got))
	}
}

// 22-AC-33: the command-level path of make policy-check (go run ./cmd/sc-policy check --root .. --dir <dir>)
// exits non-zero and prints the code. The command package is run as a process because its run function is
// private to package main.
func TestSCPolicyCheckCommand_PlaneGlobOmitted_D20_22AC33(t *testing.T) {
	root := repoRoot(t)
	run := func(dir string) (int, string) {
		cmd := exec.Command("go", "run", "./cmd/sc-policy", "check", "--root", root, "--dir", dir)
		cmd.Dir = filepath.Join(root, "src")
		out, err := cmd.CombinedOutput()
		if err == nil {
			return 0, string(out)
		}
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("cannot run sc-policy: %v\n%s", err, out)
		}
		return ee.ExitCode(), string(out)
	}
	if code, out := run(d20Tree(t, "strange-company", nil)); code != 0 {
		t.Fatalf("setup: the real policy tree must pass, exit %d\n%s", code, out)
	}
	code, out := run(d20Tree(t, "strange-company", d20DropRunner))
	if code == 0 {
		t.Errorf("exit 0, want non-zero\n%s", out)
	}
	if !strings.Contains(out, s4Code) {
		t.Errorf("output does not print %s\n%s", s4Code, out)
	}
}
