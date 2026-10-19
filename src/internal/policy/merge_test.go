package policy

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"strange.company/sc/internal/contracts"
)

// Plan 22 section 9.4 row -> repository-policy field -> platform counterpart (the fixtures under
// testdata/merge/<row>/ hold one tightening and, where the row has one, one loosening repository policy):
//
//	union          protectedPaths, protectedBranches, discoveryConfig.{wholeFiles,semanticKeys},
//	               staticChecks.<check>.<lang>, humanAuthority.machineAccounts   (no loosening attempt: n/a)
//	allow-sets     verification.allowedExemptionClasses (exemptionClasses[].name), verification.stubPanicLanguages
//	               (stubPolicy.allowPanicStubsFor), humanAuthority.forgejoTeams, modelRouting.allowedProviders
//	               (ModelRouting providers)
//	maxima         retry.*, testSpecConflict.maxConflictsPerTask, task.maxBaseLag.commits,
//	               diffLimits.<role>.* (RoleDefinition diffRules), modelRouting.maxTier (highest tier of ModelRouting tiers)
//	minima         merge.humanApprovalsRequired
//	durations      watchdog.groups.<g>.{alertAfter,escalateAfter}, task.maxBaseLag.age
//	ordered-enums  testProtection.existingTestModification, testSpecConflict.resolutionApproval (schema const:
//	               the loosening fixture is REPO_POLICY_INVALID, not POLICY_LOOSENING), requireHumanApproval
//	definitions    testPaths, fixturePaths, testInfraPaths, stubPaths

func mergeDoc(t testing.TB, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	v, err := contracts.ParseYAML(b)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	m, _ := v.(map[string]any)
	if m == nil {
		t.Fatalf("%s: not a mapping", path)
	}
	return m
}

func mergeClone(t testing.TB, m map[string]any) map[string]any {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// basePlatform is the platform of testdata/valid.
func basePlatform(t testing.TB) Platform {
	t.Helper()
	p := Platform{
		Verification: mergeDoc(t, td("valid", "verification.yaml")),
		ModelRouting: mergeDoc(t, td("valid", "model-routing.yaml")),
		Roles:        map[string]map[string]any{},
	}
	for _, r := range requiredRoles {
		p.Roles[r] = mergeDoc(t, td("valid", "roles", r+".yaml"))
	}
	return p
}

func clonePlatform(t testing.TB, p Platform) Platform {
	q := Platform{Verification: mergeClone(t, p.Verification), ModelRouting: mergeClone(t, p.ModelRouting), Roles: map[string]map[string]any{}}
	for k, v := range p.Roles {
		q.Roles[k] = mergeClone(t, v)
	}
	return q
}

// canon renders v as JSON in which every array is sorted (arrays are sets for the merge).
func canon(v any) string {
	var norm func(any) any
	norm = func(x any) any {
		switch y := x.(type) {
		case map[string]any:
			o := map[string]any{}
			for k, e := range y {
				o[k] = norm(e)
			}
			return o
		case []any:
			return normList(y, norm)
		case []string:
			l := make([]any, len(y))
			for i, s := range y {
				l[i] = s
			}
			return normList(l, norm)
		}
		return x
	}
	b, _ := json.Marshal(norm(v))
	return string(b)
}

func normList(y []any, norm func(any) any) []any {
	l := make([]any, len(y))
	s := make([]string, len(y))
	for i, e := range y {
		b, _ := json.Marshal(norm(e))
		s[i] = string(b)
	}
	sort.Strings(s)
	for i := range s {
		var e any
		_ = json.Unmarshal([]byte(s[i]), &e)
		l[i] = e
	}
	return l
}

func readMerged(t testing.TB, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return m
}

// readExpectLines reads `CODE:/pointer` lines.
func readExpectLines(t testing.TB, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}

func issueKeys(is []Issue) []string {
	var out []string
	for _, i := range is {
		k := i.Code
		if i.Pointer != "" {
			k += ":" + i.Pointer
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// mergeRow is one row of plan 22 section 9.4.
type mergeRow struct {
	name string
	// platform adapts the platform for this row (nil = testdata/valid unchanged).
	platform func(testing.TB, Platform) Platform
	// loosen lists the loosening fixtures (without extension); empty for the union row.
	loosen []string
	// loosenPlatform, when set, adapts the platform for the loosening fixtures only.
	loosenPlatform func(testing.TB, Platform) Platform
}

func withoutFrontier(t testing.TB, p Platform) Platform {
	q := clonePlatform(t, p)
	// tiers.frontier is schema-required: keep it, with no candidates.
	q.ModelRouting["spec"].(map[string]any)["tiers"].(map[string]any)["frontier"] = map[string]any{"candidates": []any{}}
	return q
}

func looserPlatform(t testing.TB, p Platform) Platform {
	q := clonePlatform(t, p)
	spec := q.Verification["spec"].(map[string]any)
	spec["testProtection"].(map[string]any)["existingTestModification"] = "flag"
	spec["testSpecConflict"].(map[string]any)["resolutionApproval"] = "adjudicator"
	return q
}

var mergeRows = []mergeRow{
	{name: "union"},
	{name: "allow-sets", loosen: []string{"loosen"}},
	{name: "maxima", loosen: []string{"loosen"}, loosenPlatform: withoutFrontier},
	{name: "minima", loosen: []string{"loosen"}},
	{name: "durations", loosen: []string{"loosen"}},
	{name: "ordered-enums", loosen: []string{"loosen"}},
	{name: "definitions", loosen: []string{"loosen", "loosen-catchall"}},
}

func assertMerged(t *testing.T, eff Effective, is []Issue, want map[string]any) {
	t.Helper()
	if len(is) != 0 {
		t.Fatalf("tightening fixture rejected: %v", is)
	}
	if len(want) == 0 {
		t.Fatal("empty expectation")
	}
	for ptr, w := range want {
		got, ok := eff.Values[ptr]
		if !ok {
			t.Errorf("no merged value at %s", ptr)
			continue
		}
		if canon(got) != canon(w) {
			t.Errorf("merged %s = %s, want %s", ptr, canon(got), canon(w))
		}
	}
}

func assertRejected(t *testing.T, eff Effective, is []Issue, want []string) {
	t.Helper()
	got := issueKeys(is)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("issues:\n  got  %v\n  want %v", got, want)
	}
	if eff.Values != nil {
		t.Errorf("a rejected repository policy produced a partial effective policy: %v", eff.Values)
	}
}

// 22-AC-2: every section 9.4 row has a tightening fixture (merged value asserted) and, except the union row
// ("n/a"), a loosening fixture (POLICY_LOOSENING with the expected JSON pointers; REPO_POLICY_INVALID for the
// schema-forbidden ordered enums and catch-all definitions).
func TestMergePolicy_Rows_22AC2(t *testing.T) {
	base := basePlatform(t)
	for _, row := range mergeRows {
		plat := base
		if row.platform != nil {
			plat = row.platform(t, base)
		}
		t.Run(row.name+"/tighten", func(t *testing.T) {
			repo := mergeDoc(t, td("merge", row.name, "tighten.yaml"))
			eff, is := MergePolicy(opts(t, false), plat, repo)
			assertMerged(t, eff, is, readMerged(t, td("merge", row.name, "tighten.merged")))
		})
		for _, l := range row.loosen {
			t.Run(row.name+"/"+l, func(t *testing.T) {
				p := plat
				if row.loosenPlatform != nil {
					p = row.loosenPlatform(t, plat)
				}
				repo := mergeDoc(t, td("merge", row.name, l+".yaml"))
				eff, is := MergePolicy(opts(t, false), p, repo)
				assertRejected(t, eff, is, readExpectLines(t, td("merge", row.name, l+".expect")))
			})
		}
	}
}

// 22-AC-2 (ordered enums): the stricter value wins when the platform value is the looser one; the schema
// forbids the looser repository value, so the result is the stricter repository value.
func TestMergePolicy_OrderedEnumsStricterWins_22AC2(t *testing.T) {
	loose := looserPlatform(t, basePlatform(t))
	repo := mergeDoc(t, td("merge", "ordered-enums", "tighten.yaml"))
	eff, is := MergePolicy(opts(t, false), loose, repo)
	assertMerged(t, eff, is, map[string]any{
		"/spec/testProtection/existingTestModification": "deny",
		"/spec/testSpecConflict/resolutionApproval":     "human",
	})
}

// 22-AC-2 (maxima/minima/durations): a value equal to the platform value is not loosening.
func TestMergePolicy_EqualToPlatformIsNotLoosening_22AC2(t *testing.T) {
	repo := mergeDoc(t, td("merge", "maxima", "tighten.yaml"))
	eff, is := MergePolicy(opts(t, false), basePlatform(t), repo)
	if len(is) != 0 {
		t.Fatalf("maxReviewExecutions equal to the platform value was rejected: %v", is)
	}
	if got := eff.Values["/spec/retry/maxReviewExecutions"]; canon(got) != "3" {
		t.Errorf("merged maxReviewExecutions = %v, want 3", got)
	}
}

// 22-AC-2 (union): the repository can add but never remove a platform entry.
func TestMergePolicy_UnionKeepsPlatformEntries_22AC2(t *testing.T) {
	plat := basePlatform(t)
	repo := mergeDoc(t, td("merge", "union", "tighten.yaml"))
	eff, is := MergePolicy(opts(t, false), plat, repo)
	if len(is) != 0 {
		t.Fatalf("rejected: %v", is)
	}
	var got []string
	switch l := eff.Values["/spec/protectedPaths"].(type) {
	case []string:
		got = l
	case []any:
		for _, e := range l {
			s, _ := e.(string)
			got = append(got, s)
		}
	}
	have := map[string]bool{}
	for _, g := range got {
		have[g] = true
	}
	for _, p := range strList(plat.Verification, "spec", "protectedPaths") {
		if !have[p] {
			t.Errorf("platform protected path %q missing from the effective set", p)
		}
	}
	if len(got) == 0 {
		t.Error("no effective protectedPaths")
	}
}

// 22-AC-2 / 26-AC-4: the shared fixture repo-policy-loosen.yaml is rejected with POLICY_LOOSENING and the
// pointer of the loosened field; the file is invalid as a whole (the tightening field next to it yields nothing).
func TestMergePolicy_SharedLoosenFixture_22AC2(t *testing.T) {
	repo := mergeDoc(t, td("repo-policy-loosen.yaml"))
	eff, is := MergePolicy(opts(t, false), basePlatform(t), repo)
	assertRejected(t, eff, is, readExpectLines(t, td("repo-policy-loosen.expect")))
}

// 26-AC-4: a repository policy that loosens a platform limit (repo-policy-loosen.yaml) is rejected by the merge
// function with POLICY_LOOSENING.
func TestMergePolicy_RejectsLooseningFixture_26AC4(t *testing.T) {
	repo := mergeDoc(t, td("repo-policy-loosen.yaml"))
	eff, is := MergePolicy(opts(t, false), basePlatform(t), repo)
	if !hasCode(is, "POLICY_LOOSENING") {
		t.Fatalf("want POLICY_LOOSENING, got %v", is)
	}
	if eff.Values != nil {
		t.Errorf("partial effective policy: %v", eff.Values)
	}
	for _, i := range is {
		if i.Code == "POLICY_LOOSENING" && i.Pointer == "" {
			t.Errorf("POLICY_LOOSENING without a JSON pointer: %v", i)
		}
	}
}

// 22-AC-2: a platform document is never modified by a merge.
func TestMergePolicy_DoesNotMutatePlatform_22AC2(t *testing.T) {
	plat := basePlatform(t)
	before := canon(plat.Verification)
	repo := mergeDoc(t, td("merge", "union", "tighten.yaml"))
	eff, is := MergePolicy(opts(t, false), plat, repo)
	if len(is) != 0 {
		t.Fatalf("rejected: %v", is)
	}
	if len(eff.Values) == 0 {
		t.Fatal("no merged values")
	}
	if canon(plat.Verification) != before {
		t.Error("MergePolicy modified the platform document")
	}
}

// 22-AC-2 (guard, review N1): an optional platform value (a watchdog group) that is absent keeps the repository value.
func TestMergePolicy_AbsentOptionalWatchdogGroupAccepted_22AC2(t *testing.T) {
	plat := clonePlatform(t, basePlatform(t))
	groups := plat.Verification["spec"].(map[string]any)["watchdog"].(map[string]any)["groups"].(map[string]any)
	delete(groups, "approval")
	repo := mergeDoc(t, td("merge", "durations", "tighten.yaml"))
	eff, is := MergePolicy(opts(t, false), plat, repo)
	if len(is) != 0 {
		t.Fatalf("absent optional watchdog group rejected: %v", is)
	}
	if got := eff.Values["/spec/watchdog/groups/approval/alertAfter"]; canon(got) != canon("24h") {
		t.Errorf("merged approval alertAfter = %v, want 24h", got)
	}
}
