package lint_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 01-AC-11 (instance validation of the three .strange-company files against their JSON Schemas is
// done by `make contracts`, F02 tooling; see TestRealTree_ContractsTarget_01AC11).

// planProtectedPaths parses the code block under "### 13.2" of plans/01-repository.md.
func planProtectedPaths(t *testing.T, root string) []string {
	t.Helper()
	plan := readFile(t, root, "plans/01-repository.md")
	i := strings.Index(plan, "### 13.2 ")
	if i < 0 {
		t.Fatal("section 13.2 not found")
	}
	rest := plan[i:]
	a := strings.Index(rest, "```text\n")
	if a < 0 {
		t.Fatal("13.2 code block not found")
	}
	rest = rest[a+len("```text\n"):]
	b := strings.Index(rest, "```")
	return strings.Fields(rest[:b])
}

// policyList extracts a string list by key from simple YAML: a flow list on the key's line
// (key: ["a", "b"]) or a block list ("- item" lines directly below the key).
func policyList(t *testing.T, body, key string) []string {
	t.Helper()
	lines := strings.Split(body, "\n")
	unq := func(s string) string { return strings.Trim(strings.TrimSpace(s), `"'`) }
	for i, l := range lines {
		tl := strings.TrimSpace(l)
		if !strings.HasPrefix(tl, key+":") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(tl, key+":"))
		var out []string
		if strings.HasPrefix(rest, "[") {
			for _, e := range strings.Split(strings.Trim(rest, "[]"), ",") {
				if e = unq(e); e != "" {
					out = append(out, e)
				}
			}
			return out
		}
		for _, n := range lines[i+1:] {
			tn := strings.TrimSpace(n)
			if tn == "" || strings.HasPrefix(tn, "#") {
				continue
			}
			if !strings.HasPrefix(tn, "- ") {
				break
			}
			out = append(out, unq(strings.TrimPrefix(tn, "- ")))
		}
		return out
	}
	t.Fatalf("key %s not found", key)
	return nil
}

// 01-AC-11: policy.yaml protectedPaths equals the section 13.2 list.
func TestRealTree_PolicyProtectedPathsEqualPlan_01AC11(t *testing.T) {
	root := repoRoot(t)
	want := planProtectedPaths(t, root)
	if len(want) < 50 {
		t.Fatalf("parsed only %d paths from plan 01 section 13.2", len(want))
	}
	b, err := os.ReadFile(filepath.Join(root, ".strange-company", "policy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	got := policyList(t, string(b), "protectedPaths")
	wset, gset := map[string]bool{}, map[string]bool{}
	for _, p := range want {
		wset[p] = true
	}
	for _, p := range got {
		gset[p] = true
	}
	for p := range wset {
		if !gset[p] {
			t.Errorf("protectedPaths lacks %q", p)
		}
	}
	for p := range gset {
		if !wset[p] {
			t.Errorf("protectedPaths has extra %q", p)
		}
	}
}

// 01-AC-11: the three files exist with the governing apiVersion and kind.
func TestRealTree_StrangeCompanyFilesKinds_01AC11(t *testing.T) {
	root := repoRoot(t)
	for file, kind := range map[string]string{
		"repo.yaml":       "RepositoryContext",
		"policy.yaml":     "RepositoryPolicy",
		"validation.yaml": "RepositoryValidation",
	} {
		b, err := os.ReadFile(filepath.Join(root, ".strange-company", file))
		if err != nil {
			t.Errorf("%s: %v", file, err)
			continue
		}
		s := string(b)
		if !regexp.MustCompile(`(?m)^apiVersion: strange\.company/v1alpha1\s*$`).MatchString(s) {
			t.Errorf("%s: apiVersion strange.company/v1alpha1 missing", file)
		}
		if !regexp.MustCompile(`(?m)^kind: ` + kind + `\s*$`).MatchString(s) {
			t.Errorf("%s: kind %s missing", file, kind)
		}
	}
}

// 01-AC-11: `make contracts` exists (instance validation of the three files; mk/contracts.mk).
func TestRealTree_ContractsTarget_01AC11(t *testing.T) {
	root := repoRoot(t)
	all := readFile(t, root, "Makefile")
	ents, _ := filepath.Glob(filepath.Join(root, "mk", "*.mk"))
	for _, e := range ents {
		b, _ := os.ReadFile(e)
		all += "\n" + string(b)
	}
	if !regexp.MustCompile(`(?m)^contracts:`).MatchString(all) {
		t.Error("no `contracts:` target in Makefile or mk/*.mk")
	}
}
