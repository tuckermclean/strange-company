package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func readYAML(t testing.TB, path string) any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := yaml.Unmarshal(b, &v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return v
}

func jsonPtr(parts ...string) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString("/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(p))
	}
	return b.String()
}

// diffPointers lists the JSON pointers at which a and b differ.
func diffPointers(a, b any, at []string, out *[]string) {
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if aok && bok {
		keys := map[string]bool{}
		for k := range am {
			keys[k] = true
		}
		for k := range bm {
			keys[k] = true
		}
		for k := range keys {
			diffPointers(am[k], bm[k], append(append([]string{}, at...), k), out)
		}
		return
	}
	if !reflect.DeepEqual(a, b) {
		*out = append(*out, jsonPtr(at...))
	}
}

// 22-AC-1: the committed policy/verification.yaml equals verification.schema.yaml examples[0] except for
// documented local overrides (each differing JSON pointer named in a `# local-override: <pointer>` comment).
func TestCommittedVerificationPolicyEqualsExample_22AC1(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "policy", "verification.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("policy/verification.yaml must be committed: %v", err)
	}
	schema := readYAML(t, filepath.Join(root, "contracts", "verification.schema.yaml")).(map[string]any)
	ex := schema["examples"].([]any)[0]
	var diffs []string
	diffPointers(readYAML(t, path), ex, nil, &diffs)
	sort.Strings(diffs)
	documented := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s*#\s*local-override:\s*(/\S*)`).FindAllStringSubmatch(string(raw), -1) {
		documented[m[1]] = true
	}
	for _, d := range diffs {
		if !documented[d] {
			t.Errorf("policy/verification.yaml differs from examples[0] at %s without a '# local-override: %s' comment", d, d)
		}
	}
}

// 26-AC-1 and 22-AC-1: the committed policy/ validates against its schemas by kind and has a revision.
func TestCheck_CommittedPolicyIsValid_26AC1(t *testing.T) {
	dir := filepath.Join(repoRoot(t), "policy")
	rev, issues := Check(dir, opts(t, false))
	if len(issues) != 0 {
		t.Fatalf("committed policy has issues: %v", issues)
	}
	if want := refDigest(t, dir); rev != want {
		t.Fatalf("Check revision = %q, want %q", rev, want)
	}
}

// 26-AC-1: the complete valid tree passes in CI mode and in Runner mode.
func TestCheck_ValidTreeBothModes_26AC1(t *testing.T) {
	for _, runner := range []bool{false, true} {
		rev, issues := Check(td("valid"), opts(t, runner))
		if len(issues) != 0 {
			t.Errorf("runner=%v: issues on valid tree: %v", runner, issues)
		}
		if rev != recordedDigest(t) {
			t.Errorf("runner=%v: revision %q, want %q", runner, rev, recordedDigest(t))
		}
	}
}

// 26-AC-1 (D1): CI mode checks only the kinds present; Runner mode requires every Runner kind.
func TestCheck_RunnerModeRequiresEveryKind_26AC1(t *testing.T) {
	dir := td("runner-only", "missing-kinds")
	if _, issues := Check(dir, opts(t, false)); len(issues) != 0 {
		t.Fatalf("CI mode must accept a partial tree: %v", issues)
	}
	_, issues := Check(dir, opts(t, true))
	for _, kind := range []string{"HarnessRegistry", "ResourceProfiles", "ModelRouting", "PricingTable", "BudgetPolicy", "RoleDefinition"} {
		found := false
		for _, i := range issues {
			if i.Code == "POLICY_KIND_MISSING" && strings.Contains(i.Message, kind) {
				found = true
			}
		}
		if !found {
			t.Errorf("Runner mode: no POLICY_KIND_MISSING issue naming %s; issues: %v", kind, issues)
		}
	}
}

// Issue formatting: `<CODE>[:<json-pointer>] <file>: <message>`.
func TestIssueString_Format_26AC1(t *testing.T) {
	got := Issue{Code: "POLICY_SCHEMA_INVALID", Pointer: "/spec/a", File: "ci.yaml", Message: "bad"}.String()
	if want := "POLICY_SCHEMA_INVALID:/spec/a ci.yaml: bad"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	got = Issue{Code: "POLICY_UNKNOWN_KIND", File: "x.yaml", Message: "m"}.String()
	if want := "POLICY_UNKNOWN_KIND x.yaml: m"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

// policyKindSchemas are the contracts whose x-invalid-examples have one fixture each (repo-policy is not a
// policy/ kind; its fixtures belong to the merge slice).
var policyKindSchemas = []string{"verification", "harness", "resource-profile", "model-routing", "pricing", "budget-policy", "role", "ci-policy", "test-quarantine"}

// 26-AC-1: one fixture per schema x-invalid-examples entry; each is rejected with the fixture's code.
func TestCheck_SchemaInvalidExamples_26AC1(t *testing.T) {
	for _, k := range policyKindSchemas {
		schema := readYAML(t, filepath.Join(repoRoot(t), "contracts", k+".schema.yaml")).(map[string]any)
		invalid := schema["x-invalid-examples"].([]any)
		for i, x := range invalid {
			name := fmt.Sprintf("schema-%s-%d", k, i)
			t.Run(name, func(t *testing.T) {
				dir := td("invalid", name)
				// the fixture holds exactly the schema's invalid value
				var found bool
				_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
					if err == nil && d.Type().IsRegular() {
						found = true
						if !reflect.DeepEqual(readYAML(t, p), x.(map[string]any)["value"]) {
							t.Errorf("%s does not hold x-invalid-examples[%d].value of %s.schema.yaml", p, i, k)
						}
					}
					return nil
				})
				if !found {
					t.Fatalf("no fixture file in %s", dir)
				}
				checkRejected(t, name)
			})
		}
	}
}

func checkRejected(t *testing.T, name string) {
	t.Helper()
	code, ptr := expectation(t, td("invalid", name+".expect"))
	_, issues := Check(td("invalid", name), opts(t, false))
	for _, i := range issues {
		if i.Code == code && (ptr == "" || i.Pointer == ptr) {
			if i.File == "" || i.Message == "" {
				t.Errorf("issue %+v lacks file or message", i)
			}
			return
		}
	}
	t.Errorf("fixture %s: want issue %s%s; got %v", name, code, map[bool]string{true: ":" + ptr}[ptr != ""], issues)
}

// 26-AC-1 and plan 26 section 9.2 step 1: unknown kind, duplicate (kind, name), file over 1 MiB, anchors,
// aliases, non-UTF-8 and a real file name containing "__" are each rejected with the fixture's code.
func TestCheck_Step1Rejections_26AC1(t *testing.T) {
	for _, name := range []string{"unknown-kind", "duplicate-kind-name", "file-too-large", "yaml-anchor", "yaml-alias", "not-utf8", "reserved-filename"} {
		t.Run(name, func(t *testing.T) { checkRejected(t, name) })
	}
}

// 26-AC-1: every committed invalid fixture is covered (a new fixture needs a code, and every code is distinct
// from the success path).
func TestCheck_EveryInvalidFixtureIsRejected_26AC1(t *testing.T) {
	expects, _ := filepath.Glob(td("invalid", "*.expect"))
	if len(expects) < 41+7 {
		t.Fatalf("found %d invalid fixtures, want at least 48", len(expects))
	}
	for _, e := range expects {
		name := strings.TrimSuffix(filepath.Base(e), ".expect")
		t.Run(name, func(t *testing.T) { checkRejected(t, name) })
	}
}
