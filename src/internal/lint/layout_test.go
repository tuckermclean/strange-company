package lint_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"strange.company/sc/internal/lint"
)

// Rule IDs of `sc-lint layout` (plan 01 section 6.1, 6.6, 7.3). The two named by the plan are
// agents-md-policy-markers and make-targets-known; the rest are chosen here.
const (
	ruleSymlink  = "no-symlinks"
	ruleExec     = "executable-files"
	ruleIgnore   = "gitignore-entries"
	ruleTopLevel = "top-level-dirs"
	ruleEnvFile  = "no-tracked-bootstrap-env"
	ruleSize     = "file-size-limit"
	ruleAgentsSz = "agents-md-size"
	ruleMarkers  = "agents-md-policy-markers"
	ruleMakeTgt  = "make-targets-known"
)

var allLayoutRules = []string{ruleSymlink, ruleExec, ruleIgnore, ruleTopLevel, ruleEnvFile, ruleSize, ruleAgentsSz, ruleMarkers, ruleMakeTgt}

func mustLayout(t *testing.T, dir string) lint.Result {
	t.Helper()
	r, err := lint.LayoutCheck(dir)
	if err != nil {
		t.Fatalf("LayoutCheck: %v", err)
	}
	return r
}

// 01-AC-1: positive fixture and the real F01 tree.
func TestLayoutCheck_ValidFixturePasses_01AC1(t *testing.T) {
	r := mustLayout(t, layoutTree(t))
	ranRules(t, r, allLayoutRules...)
	if f := errorsOf(r); len(f) != 0 {
		t.Errorf("valid fixture has findings: %v", f)
	}
}

func TestLayoutCheck_RealTreePasses_01AC1(t *testing.T) {
	r := mustLayout(t, repoRoot(t))
	ranRules(t, r, allLayoutRules...)
	for _, f := range errorsOf(r) {
		t.Errorf("real tree: %s", f)
	}
}

// 01-AC-1: one violating tree per rule; the rule ID is in the finding and in the command output.
func TestLayoutCheck_Negatives_01AC1(t *testing.T) {
	cases := []struct {
		name, rule string
		mutate     func(t *testing.T, dir string)
	}{
		{"symlink", ruleSymlink, func(t *testing.T, d string) {
			if err := os.Symlink("README.md", filepath.Join(d, "link.md")); err != nil {
				t.Fatal(err)
			}
		}},
		{"executable yaml", ruleExec, func(t *testing.T, d string) {
			if err := os.Chmod(filepath.Join(d, ".strange-company", "repo.yaml"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing gitignore entry", ruleIgnore, func(t *testing.T, d string) {
			s := readFile(t, d, ".gitignore")
			write(t, d, ".gitignore", strings.Replace(s, "/out/\n", "", 1))
		}},
		{"unknown top-level directory", ruleTopLevel, func(t *testing.T, d string) {
			write(t, d, "docs/x.md", "x\n")
		}},
		{"tracked bootstrap.env", ruleEnvFile, func(t *testing.T, d string) {
			write(t, d, "bootstrap.env", "K=V\n")
			git(t, d, "add", "-f", "bootstrap.env")
		}},
		{"file over 2 MiB", ruleSize, func(t *testing.T, d string) {
			write(t, d, "test/fixtures/big.bin", strings.Repeat("a", 2*1024*1024+1))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := layoutTree(t)
			c.mutate(t, dir)
			git(t, dir, "add", "-A")
			r := mustLayout(t, dir)
			if !hasRule(r, c.rule) {
				t.Errorf("want a %q finding, got %v", c.rule, r.Findings)
			}
			var out, errb bytes.Buffer
			code := lint.Main([]string{"layout", "--root", dir}, &out, &errb)
			if code == 0 {
				t.Errorf("sc-lint layout exit = 0, want non-zero")
			}
			if !contains(out.String()+errb.String(), c.rule) {
				t.Errorf("output lacks rule ID %q:\n%s%s", c.rule, out.String(), errb.String())
			}
		})
	}
}

// 01-AC-1 / plan 01 section 6.1: exactly 2 MiB is allowed; the modes of bootstrap, toolbox/bin and
// test/e2e/**/run are allowed (they are in the valid fixture).
func TestLayoutCheck_SizeBoundary_01AC1(t *testing.T) {
	dir := layoutTree(t)
	write(t, dir, "test/fixtures/edge.bin", strings.Repeat("a", 2*1024*1024))
	git(t, dir, "add", "-A")
	r := mustLayout(t, dir)
	ranRules(t, r, ruleSize)
	if hasRule(r, ruleSize) {
		t.Errorf("a file of exactly 2 MiB must pass: %v", r.Findings)
	}
}

// 01-AC-1: every missing .gitignore entry of plan 01 section 6.6 is reported (one removal each).
func TestLayoutCheck_EachGitignoreEntryRequired_01AC1(t *testing.T) {
	for _, e := range gitignoreEntries {
		t.Run(e, func(t *testing.T) {
			dir := layoutTree(t)
			s := readFile(t, dir, ".gitignore")
			write(t, dir, ".gitignore", strings.Replace(s, e+"\n", "", 1))
			git(t, dir, "add", "-A")
			if r := mustLayout(t, dir); !hasRule(r, ruleIgnore) {
				t.Errorf("removing %q not reported: %v", e, r.Findings)
			}
		})
	}
}

// Plan 01 section 6.6, exact .gitignore entries.
var gitignoreEntries = []string{
	"/bootstrap.env", "/bootstrap.env.*", "!/bootstrap.env.example", "/.bootstrap/", "/.cache/", "/out/",
	"*.tfstate", "*.tfstate.*", "*.tfplan", ".terraform/", "*.agekey", "!/test/fixtures/age/ci-test.agekey",
	"*.tar.age", "kubeconfig", ".DS_Store", "/.claude/settings.local.json", "/.claude/scheduled_tasks.lock",
}

// 01-AC-1 (section 6.6): the real .gitignore carries every exact entry.
func TestRealTree_GitignoreHasAllEntries_01AC1(t *testing.T) {
	have := map[string]bool{}
	for _, l := range strings.Split(readFile(t, repoRoot(t), ".gitignore"), "\n") {
		have[strings.TrimSpace(l)] = true
	}
	for _, e := range gitignoreEntries {
		if !have[e] {
			t.Errorf(".gitignore lacks %q", e)
		}
	}
}

// 01-AC-2, run against the real repository.
func TestRealTree_CheckIgnore_01AC2(t *testing.T) {
	root := repoRoot(t)
	ci := func(path string) int {
		cmd := exec.Command("git", "check-ignore", "-q", path)
		cmd.Dir = root
		err := cmd.Run()
		if err == nil {
			return 0
		}
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		t.Fatal(err)
		return -1
	}
	for _, p := range []string{"bootstrap.env", ".bootstrap/journal.json", "out/x", ".cache/x"} {
		if c := ci(p); c != 0 {
			t.Errorf("git check-ignore -q %s = %d, want 0", p, c)
		}
	}
	if c := ci("bootstrap.env.example"); c != 1 {
		t.Errorf("git check-ignore -q bootstrap.env.example = %d, want 1", c)
	}
}

// 01-AC-10: AGENTS.md size rule, boundary 300 / 301 lines.
func TestLayoutCheck_AgentsMdSize_01AC10(t *testing.T) {
	for _, c := range []struct {
		lines int
		bad   bool
	}{{300, false}, {301, true}} {
		dir := layoutTree(t)
		s := readFile(t, dir, "AGENTS.md")
		n := strings.Count(s, "\n")
		s += strings.Repeat("filler\n", c.lines-n)
		write(t, dir, "AGENTS.md", s)
		git(t, dir, "add", "-A")
		r := mustLayout(t, dir)
		ranRules(t, r, ruleAgentsSz)
		if got := hasRule(r, ruleAgentsSz); got != c.bad {
			t.Errorf("%d lines: size finding = %v, want %v (%v)", c.lines, got, c.bad, r.Findings)
		}
	}
}

// 01-AC-10: every bullet in "Enforced rules" needs a marker whose key exists in policy.yaml.
func TestLayoutCheck_AgentsMdPolicyMarkers_01AC10(t *testing.T) {
	enforced := func(bullets string) string {
		return "# Agent instructions\n\n## Enforced rules\n\n" + bullets + "\n## Notes\n\n- A bullet outside the section needs no marker.\n"
	}
	cases := []struct {
		name, body string
		bad        bool
	}{
		{"all marked", enforced("- a <!-- policy: protectedPaths -->\n- b <!-- policy: testPaths -->\n"), false},
		{"missing marker", enforced("- a <!-- policy: protectedPaths -->\n- b has none\n"), true},
		{"unknown key", enforced("- a <!-- policy: noSuchPolicyKey -->\n"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := layoutTree(t)
			write(t, dir, "AGENTS.md", c.body)
			git(t, dir, "add", "-A")
			r := mustLayout(t, dir)
			ranRules(t, r, ruleMarkers)
			if got := hasRule(r, ruleMarkers); got != c.bad {
				t.Errorf("marker finding = %v, want %v (%v)", got, c.bad, r.Findings)
			}
		})
	}
}

// 01-AC-10: a key may also come from policy/verification.yaml.
func TestLayoutCheck_AgentsMdMarkerKeyFromVerificationPolicy_01AC10(t *testing.T) {
	dir := layoutTree(t)
	write(t, dir, "policy/verification.yaml", "apiVersion: strange.company/v1alpha1\nkind: VerificationPolicy\nspec:\n  verifierOnlyKey: true\n")
	write(t, dir, "AGENTS.md", "## Enforced rules\n\n- a <!-- policy: verifierOnlyKey -->\n")
	git(t, dir, "add", "-A")
	r := mustLayout(t, dir)
	ranRules(t, r, ruleMarkers)
	if hasRule(r, ruleMarkers) {
		t.Errorf("key in policy/verification.yaml rejected: %v", r.Findings)
	}
}

// 01-AC-10 on the real AGENTS.md (independent of the checker).
func TestRealTree_AgentsMd_01AC10(t *testing.T) {
	root := repoRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		t.Fatalf("AGENTS.md: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) > 300 {
		t.Errorf("AGENTS.md has %d lines, max 300", len(lines))
	}
	keys := ""
	for _, p := range []string{".strange-company/policy.yaml", "policy/verification.yaml"} {
		if b, err := os.ReadFile(filepath.Join(root, p)); err == nil {
			keys += string(b) + "\n"
		}
	}
	in, bullets := false, 0
	for _, l := range lines {
		if strings.HasPrefix(l, "## ") {
			in = strings.TrimSpace(strings.TrimPrefix(l, "## ")) == "Enforced rules"
			continue
		}
		if !in || !strings.HasPrefix(strings.TrimSpace(l), "- ") {
			continue
		}
		bullets++
		i := strings.Index(l, "<!-- policy:")
		j := strings.Index(l, "-->")
		if i < 0 || j < i {
			t.Errorf("bullet without policy marker: %q", l)
			continue
		}
		key := strings.TrimSpace(l[i+len("<!-- policy:") : j])
		if key == "" || !strings.Contains(keys, key+":") {
			t.Errorf("policy key %q not found in policy.yaml or policy/verification.yaml", key)
		}
	}
	if bullets == 0 {
		t.Error(`AGENTS.md has no bullets under "## Enforced rules"`)
	}
}

// 01-AC-12: make targets mentioned in plans/*.md exist in `make -pn` or are planned.
func TestLayoutCheck_MakeTargetsKnown_01AC12(t *testing.T) {
	// Fixture Makefile defines verify and layout-check; plans/01-fixture.md mentions `make verify`.
	t.Run("unknown target", func(t *testing.T) {
		dir := layoutTree(t)
		write(t, dir, "plans/02-fixture.md", "Run `make nosuchtarget` first.\n")
		git(t, dir, "add", "-A")
		r := mustLayout(t, dir)
		ranRules(t, r, ruleMakeTgt)
		if !hasRule(r, ruleMakeTgt) {
			t.Errorf("unknown make target not reported: %v", r.Findings)
		}
		var out, errb bytes.Buffer
		if code := lint.Main([]string{"layout", "--root", dir}, &out, &errb); code == 0 || !contains(out.String()+errb.String(), ruleMakeTgt) {
			t.Errorf("exit=%d, output lacks %q:\n%s%s", code, ruleMakeTgt, out.String(), errb.String())
		}
	})
	t.Run("planned target", func(t *testing.T) {
		dir := layoutTree(t)
		write(t, dir, "plans/02-fixture.md", "Run `make nosuchtarget` first.\n")
		// Format assumption (see handoff): one entry per line, a target is written `make <target>`.
		write(t, dir, "test/fixtures/layout/planned-paths.txt", "make nosuchtarget\n")
		git(t, dir, "add", "-A")
		r := mustLayout(t, dir)
		ranRules(t, r, ruleMakeTgt)
		if hasRule(r, ruleMakeTgt) {
			t.Errorf("planned target reported: %v", r.Findings)
		}
	})
}

func TestRealTree_PlannedPathsFileExists_01AC12(t *testing.T) {
	if _, err := os.Stat(filepath.Join(repoRoot(t), "test", "fixtures", "layout", "planned-paths.txt")); err != nil {
		t.Errorf("test/fixtures/layout/planned-paths.txt: %v", err)
	}
}

// Wiring: the Makefile fragments run the checks through sc-lint (plan 01 section 7.1).
func TestMakeWiring_LayoutAndDagCheck_01AC1_01AC14(t *testing.T) {
	root := repoRoot(t)
	mk := readFile(t, root, "Makefile")
	ents, _ := filepath.Glob(filepath.Join(root, "mk", "*.mk"))
	for _, e := range ents {
		b, _ := os.ReadFile(e)
		mk += "\n" + string(b)
	}
	if contains(mk, "not yet implemented (F01 slice S2)") {
		t.Error("layout-check is still the S2 placeholder")
	}
	for _, want := range []string{"sc-lint", "layout", "dag-check:"} {
		if !contains(mk, want) {
			t.Errorf("Makefile/mk lacks %q", want)
		}
	}
}
