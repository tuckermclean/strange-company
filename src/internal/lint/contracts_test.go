package lint_test

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 01-AC-11: `.strange-company/{repo,policy,validation}.yaml` validate against their schemas via
// `make contracts` instance validation. The schema is selected from apiVersion + kind (plan 01 8.3).

// contractsFiles maps each governed file to the schema file its kind must select.
var contractsFiles = []struct{ file, kind, schema string }{
	{"repo.yaml", "RepositoryContext", "context.schema.yaml"},
	{"policy.yaml", "RepositoryPolicy", "repo-policy.schema.yaml"},
	{"validation.yaml", "RepositoryValidation", "repo-validation.schema.yaml"},
}

// runMakeContracts runs `make contracts` with SC_IN_CONTAINER=1 in dir; returns exit code and
// combined output.
func runMakeContracts(t *testing.T, dir string) (int, string) {
	t.Helper()
	cmd := exec.Command("make", "contracts")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "SC_IN_CONTAINER=1")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if err == nil {
		return 0, out.String()
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), out.String()
	}
	t.Fatalf("running make: %v", err)
	return -1, ""
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := os.Lstat(src)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode()&os.ModeSymlink != 0 {
		l, _ := os.Readlink(src)
		if err := os.Symlink(l, dst); err != nil {
			t.Fatal(err)
		}
		return
	}
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, st.Mode().Perm())
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
}

// tempTreeCopy copies the working tree (tracked and untracked, not ignored) into a fresh
// git repository with one commit and returns its path.
func tempTreeCopy(t *testing.T) string {
	t.Helper()
	root := repoRoot(t)
	ls := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	ls.Dir = root
	b, err := ls.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	dst := t.TempDir()
	for _, rel := range strings.Split(string(b), "\x00") {
		if rel == "" {
			continue
		}
		if _, err := os.Lstat(filepath.Join(root, rel)); err != nil {
			continue // deleted but still indexed
		}
		copyFile(t, filepath.Join(root, rel), filepath.Join(dst, rel))
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "-A"},
		{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "-m", "tree"},
	} {
		c := exec.Command("git", args...)
		c.Dir = dst
		if o, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, o)
		}
	}
	return dst
}

// requireBaselinePasses makes a negative test meaningful: the unmodified copy must pass, so a
// later failure is attributable to the mutation and not to a missing target.
func requireBaselinePasses(t *testing.T, dir string) {
	t.Helper()
	if code, out := runMakeContracts(t, dir); code != 0 {
		t.Fatalf("baseline `make contracts` on the unmodified tree exited %d, want 0:\n%s", code, out)
	} else {
		// A bare `make contracts` can "succeed" with nothing to do (the contracts/ directory exists);
		// require evidence that real validation ran.
		for _, f := range contractsFiles {
			if !lineNaming(out, f.file, f.schema) {
				t.Fatalf("baseline `make contracts` did not report validating %s against %s:\n%s", f.file, f.schema, out)
			}
		}
	}
}

// lineNaming reports whether some output line mentions both the file and the schema.
func lineNaming(out, file, schema string) bool {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, file) && strings.Contains(l, schema) {
			return true
		}
	}
	return false
}

// 01-AC-11: exits 0 on the real tree and reports each file validated against its schema.
func TestRealTree_MakeContractsValidatesInstances_01AC11(t *testing.T) {
	root := repoRoot(t)
	code, out := runMakeContracts(t, root)
	if code != 0 {
		t.Errorf("`make contracts` exited %d, want 0:\n%s", code, out)
	}
	for _, f := range contractsFiles {
		if !lineNaming(out, f.file, f.schema) {
			t.Errorf("output has no line naming %s validated against %s (kind %s):\n%s", f.file, f.schema, f.kind, out)
		}
	}
}

// 01-AC-11: a schema violation in any one of the three files fails `make contracts` and names it.
// The appended top-level key is rejected by all three schemas (additionalProperties: false).
func TestMakeContracts_RejectsSchemaViolation_01AC11(t *testing.T) {
	for _, f := range contractsFiles {
		f := f
		t.Run(f.file, func(t *testing.T) {
			dir := tempTreeCopy(t)
			requireBaselinePasses(t, dir)
			p := filepath.Join(dir, ".strange-company", f.file)
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, append(append(b, '\n'), []byte("unknownTopLevelField: 1\n")...), 0o644); err != nil {
				t.Fatal(err)
			}
			code, out := runMakeContracts(t, dir)
			if code == 0 {
				t.Errorf("`make contracts` exited 0 with a schema violation in %s:\n%s", f.file, out)
			}
			if !strings.Contains(out, f.file) {
				t.Errorf("output does not name %s:\n%s", f.file, out)
			}
		})
	}
}

// 01-AC-11: a wrongly-typed field (metadata.name must be a string) also fails and names the file.
func TestMakeContracts_RejectsWrongType_01AC11(t *testing.T) {
	dir := tempTreeCopy(t)
	requireBaselinePasses(t, dir)
	p := filepath.Join(dir, ".strange-company", "validation.yaml")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?m)^(\s+name:).*$`)
	loc := re.FindIndex(b)
	if loc == nil {
		t.Fatal("validation.yaml has no metadata name line to corrupt")
	}
	mut := append([]byte{}, b[:loc[0]]...)
	mut = append(mut, []byte(re.FindSubmatch(b)[1])...)
	mut = append(mut, []byte(" 12345")...)
	mut = append(mut, b[loc[1]:]...)
	if err := os.WriteFile(p, mut, 0o644); err != nil {
		t.Fatal(err)
	}
	code, out := runMakeContracts(t, dir)
	if code == 0 {
		t.Errorf("`make contracts` exited 0 with a wrong-typed name:\n%s", out)
	}
	if !strings.Contains(out, "validation.yaml") {
		t.Errorf("output does not name validation.yaml:\n%s", out)
	}
}

// 01-AC-11: a file whose kind maps to no schema fails rather than being skipped.
func TestMakeContracts_UnknownKindFails_01AC11(t *testing.T) {
	dir := tempTreeCopy(t)
	requireBaselinePasses(t, dir)
	p := filepath.Join(dir, ".strange-company", "repo.yaml")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?m)^kind:.*$`)
	if !re.Match(b) {
		t.Fatal("repo.yaml has no kind line")
	}
	if err := os.WriteFile(p, re.ReplaceAll(b, []byte("kind: NoSuchKind")), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out := runMakeContracts(t, dir)
	if code == 0 {
		t.Errorf("`make contracts` exited 0 for a kind with no schema (silently skipped):\n%s", out)
	}
	if !strings.Contains(out, "repo.yaml") || !strings.Contains(out, "NoSuchKind") {
		t.Errorf("output must name repo.yaml and the unmapped kind NoSuchKind:\n%s", out)
	}
}
