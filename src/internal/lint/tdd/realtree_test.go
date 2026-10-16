package tdd_test

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// Real-tree tests for plan 52 section 8.3 (52-AC-4 support): policy/ci.yaml and make tdd-discipline-check.

// planTddBlock parses the yaml code block under "Configuration (`policy/ci.yaml`):" of plan 52.
func planTddBlock(t *testing.T, root string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "plans", "52-ci-strategy.md"))
	if err != nil {
		t.Fatal(err)
	}
	plan := string(b)
	i := strings.Index(plan, "Configuration (`policy/ci.yaml`):")
	if i < 0 {
		t.Fatal("configuration block of section 8.3 not found")
	}
	rest := plan[i:]
	a := strings.Index(rest, "```yaml\n")
	if a < 0 {
		t.Fatal("yaml code block not found")
	}
	rest = rest[a+len("```yaml\n"):]
	rest = rest[:strings.Index(rest, "```")]
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(rest), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func tddSpec(t *testing.T, doc map[string]any) any {
	t.Helper()
	spec, ok := doc["spec"].(map[string]any)
	if !ok {
		t.Fatalf("no spec mapping in %v", doc)
	}
	v, ok := spec["tddDiscipline"]
	if !ok {
		t.Fatal("no spec.tddDiscipline")
	}
	return v
}

// 52-AC-4 (T0-T8 configuration): policy/ci.yaml is a CIPolicy whose spec.tddDiscipline equals the plan block.
func TestRealTree_CIPolicyTddDisciplineEqualsPlan_52AC4(t *testing.T) {
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "policy", "ci.yaml"))
	if err != nil {
		t.Fatalf("policy/ci.yaml: %v", err)
	}
	s := string(b)
	if !regexp.MustCompile(`(?m)^apiVersion: strange\.company/v1alpha1\s*$`).MatchString(s) {
		t.Error("policy/ci.yaml: apiVersion strange.company/v1alpha1 missing")
	}
	if !regexp.MustCompile(`(?m)^kind: CIPolicy\s*$`).MatchString(s) {
		t.Error("policy/ci.yaml: kind CIPolicy missing")
	}
	var got map[string]any
	if err := yaml.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	want := tddSpec(t, planTddBlock(t, root))
	if wm, ok := want.(map[string]any); !ok || len(wm) < 6 {
		t.Fatalf("parsed tddDiscipline from the plan looks wrong: %v", want)
	}
	if g := tddSpec(t, got); !reflect.DeepEqual(g, want) {
		t.Errorf("policy/ci.yaml spec.tddDiscipline differs from the plan 52 section 8.3 block:\n got: %v\nwant: %v", g, want)
	}
}

func runMake(t *testing.T, dir string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command("make", args...)
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

func lineNaming(out, a, b string) bool {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, a) && strings.Contains(l, b) {
			return true
		}
	}
	return false
}

// 52-AC-4: `make contracts` validates policy/ci.yaml against contracts/ci-policy.schema.yaml.
func TestRealTree_MakeContractsValidatesCIPolicy_52AC4(t *testing.T) {
	code, out := runMake(t, repoRoot(t), "contracts")
	if code != 0 {
		t.Errorf("`make contracts` exited %d, want 0:\n%s", code, out)
	}
	if !lineNaming(out, "policy/ci.yaml", "ci-policy.schema.yaml") {
		t.Errorf("no output line names policy/ci.yaml validated against ci-policy.schema.yaml:\n%s", out)
	}
}

// 52-AC-4: a schema violation in policy/ci.yaml fails `make contracts` and names the file.
func TestMakeContracts_RejectsCIPolicyViolation_52AC4(t *testing.T) {
	dir := treeCopy(t)
	if code, out := runMake(t, dir, "contracts"); code != 0 || !lineNaming(out, "policy/ci.yaml", "ci-policy.schema.yaml") {
		t.Fatalf("baseline `make contracts` on the unmodified copy: exit %d\n%s", code, out)
	}
	p := filepath.Join(dir, "policy", "ci.yaml")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, append(append(b, '\n'), []byte("unknownTopLevelField: 1\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out := runMake(t, dir, "contracts")
	if code == 0 || !strings.Contains(out, "policy/ci.yaml") {
		t.Errorf("exit %d; want non-zero naming policy/ci.yaml:\n%s", code, out)
	}
}

// 52-AC-4: the schema accepts redTargetFallbacks (CD-56).
func TestRealTree_SchemaHasRedTargetFallbacks_52AC4(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "contracts", "ci-policy.schema.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "redTargetFallbacks:") {
		t.Error("contracts/ci-policy.schema.yaml lacks redTargetFallbacks")
	}
}

// 52-AC-4: `make tdd-discipline-check` is a target (CI job tdd-discipline runs it).
func TestRealTree_MakeTddDisciplineCheckTarget_52AC4(t *testing.T) {
	root := repoRoot(t)
	code, out := runMake(t, root, "-pn")
	if code != 0 {
		t.Fatalf("make -pn exited %d", code)
	}
	if !regexp.MustCompile(`(?m)^tdd-discipline-check:`).MatchString(out) {
		t.Error("`make -pn` has no target tdd-discipline-check")
	}
	code, out = runMake(t, root, "-n", "tdd-discipline-check")
	if code != 0 || !strings.Contains(out, "tdd-discipline") {
		t.Errorf("`make -n tdd-discipline-check` exit %d, output:\n%s", code, out)
	}
}

// treeCopy copies the working tree (tracked and untracked, not ignored) into a fresh directory.
func treeCopy(t *testing.T) string {
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
		src := filepath.Join(root, rel)
		st, err := os.Lstat(src)
		if err != nil {
			continue
		}
		to := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			t.Fatal(err)
		}
		if st.Mode()&os.ModeSymlink != 0 {
			l, _ := os.Readlink(src)
			if err := os.Symlink(l, to); err != nil {
				t.Fatal(err)
			}
			continue
		}
		in, err := os.Open(src)
		if err != nil {
			t.Fatal(err)
		}
		out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, st.Mode().Perm())
		if err != nil {
			in.Close()
			t.Fatal(err)
		}
		_, err = io.Copy(out, in)
		in.Close()
		out.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return dst
}
