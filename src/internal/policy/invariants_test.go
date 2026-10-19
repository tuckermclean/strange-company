package policy

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Fixture layout of slice S2 (F04):
//
//	testdata/invalid/invariants/<case>/    the complete valid/ tree with exactly one change (cross-file invariants,
//	                                       plan 26 section 9.2 step 3)
//	testdata/invalid/verification/<case>/  plan 22 section 9.5; repository-side cases keep the repository documents
//	                                       under <case>/.strange-company/{policy,validation}.yaml next to a
//	                                       verification.yaml
//	testdata/accepted/<case>/              near-misses that must stay valid
//
// A sidecar <case>.expect has the form
//
//	CODE[:/json/pointer]     the primary error code (and, optionally, the pointer of one of its issues)
//	also CODE                a further code that the same change necessarily raises (repeatable)
//	msg TEXT                 text that must appear in the message of an issue with the primary code (repeatable)
//
// The set of codes Check reports is exactly {CODE} plus the `also` codes.

type fixtureExpect struct {
	code, pointer string
	also, msgs    []string
}

func readFixtureExpect(t testing.TB, path string) fixtureExpect {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ex fixtureExpect
	for i, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		switch {
		case i == 0:
			ex.code, ex.pointer, _ = strings.Cut(strings.TrimSpace(l), ":")
		case strings.HasPrefix(l, "also "):
			ex.also = append(ex.also, strings.TrimSpace(strings.TrimPrefix(l, "also ")))
		case strings.HasPrefix(l, "msg "):
			ex.msgs = append(ex.msgs, strings.TrimPrefix(l, "msg "))
		default:
			t.Fatalf("%s:%d: unrecognised line %q", path, i+1, l)
		}
	}
	if ex.code == "" {
		t.Fatalf("%s: empty primary code", path)
	}
	return ex
}

func assertFixture(t *testing.T, dir string, ex fixtureExpect, runner bool) {
	t.Helper()
	_, issues := Check(dir, opts(t, runner))
	got := map[string]bool{}
	for _, i := range issues {
		got[i.Code] = true
		if i.File == "" || i.Message == "" {
			t.Errorf("issue %+v lacks file or message", i)
		}
	}
	want := map[string]bool{ex.code: true}
	for _, a := range ex.also {
		want[a] = true
	}
	if !sameKeys(got, want) {
		t.Errorf("runner=%v: issue codes = %v, want exactly %v; issues:\n%s", runner, keys(got), keys(want), joinIssues(issues))
	}
	found := false
	for _, i := range issues {
		if i.Code == ex.code && (ex.pointer == "" || i.Pointer == ex.pointer) {
			found = true
		}
	}
	if !found {
		t.Errorf("runner=%v: want an issue %s%s; issues:\n%s", runner, ex.code, map[bool]string{true: ":" + ex.pointer}[ex.pointer != ""], joinIssues(issues))
	}
	for _, m := range ex.msgs {
		ok := false
		for _, i := range issues {
			if i.Code == ex.code && strings.Contains(i.Message, m) {
				ok = true
			}
		}
		if !ok {
			t.Errorf("runner=%v: no %s issue whose message contains %q; issues:\n%s", runner, ex.code, m, joinIssues(issues))
		}
	}
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sameKeys(a, b map[string]bool) bool { return equalStrings(keys(a), keys(b)) }

func joinIssues(is []Issue) string {
	var sb strings.Builder
	for _, i := range is {
		sb.WriteString("  " + i.String() + "\n")
	}
	if sb.Len() == 0 {
		return "  (none)\n"
	}
	return sb.String()
}

func fixtureNames(t testing.TB, group string) []string {
	t.Helper()
	expects, _ := filepath.Glob(td("invalid", group, "*.expect"))
	var out []string
	for _, e := range expects {
		out = append(out, strings.TrimSuffix(filepath.Base(e), ".expect"))
	}
	return out
}

func requireNames(t *testing.T, group string, want []string) {
	t.Helper()
	have := map[string]bool{}
	for _, n := range fixtureNames(t, group) {
		have[n] = true
		if st, err := os.Stat(td("invalid", group, n)); err != nil || !st.IsDir() {
			t.Errorf("%s/%s.expect has no fixture directory", group, n)
		}
	}
	for _, n := range want {
		if !have[n] {
			t.Errorf("missing fixture invalid/%s/%s (+ .expect)", group, n)
		}
	}
}

// invariantCases: one fixture per cross-file invariant of plan 26 section 9.2 step 3 (and the profile invariants of
// resource-profile.schema.yaml). The error code of each is in its .expect sidecar.
var invariantCases = []string{
	"role-harness-unknown",
	"role-harness-role-unsupported",
	"role-profile-unknown",
	"profile-missing-from-constrained",
	"role-default-tier-not-allowed",
	"role-tier-mismatch-default",
	"role-tier-mismatch-allowed",
	"tier-statically-empty",
	"harness-production-repo-config-not-ignored",
	"harness-production-worker-timeout-disabled",
	"profile-memory-overcommit",
	"profile-cpu-request-exceeds-limit",
	"profile-deadline-too-short",
	"profile-harness-reserve-too-large",
	"profile-set-mismatch",
	"role-missing",
	"role-sixth",
	"verification-producer-role-undefined",
}

// 26-AC-1 (plan 26 section 9.2 step 3, D1): each cross-file invariant has a fixture; the fixture raises exactly
// the codes in its sidecar, in CI mode and in Runner mode.
func TestInvariantFixtures_26AC1(t *testing.T) {
	requireNames(t, "invariants", invariantCases)
	for _, name := range fixtureNames(t, "invariants") {
		t.Run(name, func(t *testing.T) {
			ex := readFixtureExpect(t, td("invalid", "invariants", name+".expect"))
			for _, runner := range []bool{false, true} {
				assertFixture(t, td("invalid", "invariants", name), ex, runner)
			}
		})
	}
}

// treeFiles maps a relative path to the file bytes (hidden directories included).
func treeFiles(t testing.TB, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// 26-AC-1: every cross-file fixture is the valid tree with exactly one file changed, added or removed, so the
// invariant named by its sidecar is the only thing that differs from a passing tree.
func TestInvariantFixturesAreOneChangeFromValid_26AC1(t *testing.T) {
	valid := treeFiles(t, td("valid"))
	for _, name := range fixtureNames(t, "invariants") {
		t.Run(name, func(t *testing.T) {
			fx := treeFiles(t, td("invalid", "invariants", name))
			var diff []string
			for p, b := range fx {
				if v, ok := valid[p]; !ok || v != b {
					diff = append(diff, p)
				}
			}
			for p := range valid {
				if _, ok := fx[p]; !ok {
					diff = append(diff, p)
				}
			}
			sort.Strings(diff)
			if len(diff) != 1 {
				t.Errorf("fixture differs from valid/ in %d files %v, want exactly 1", len(diff), diff)
			}
		})
	}
}

// 26-AC-5: the loader rejects a role referencing a harness that does not list the role in supportedRoles, a
// profile missing from the constrained set, a production harness with ignoresRepoConfig: false, and
// disableWorkerTimeout on a production harness: four fixtures, four distinct error codes.
func TestFourNamedRunnerFixturesGiveFourDistinctCodes_26AC5(t *testing.T) {
	names := []string{
		"role-harness-role-unsupported",
		"profile-missing-from-constrained",
		"harness-production-repo-config-not-ignored",
		"harness-production-worker-timeout-disabled",
	}
	primary := map[string]string{}
	for _, n := range names {
		ex := readFixtureExpect(t, td("invalid", "invariants", n+".expect"))
		if prev, dup := primary[ex.code]; dup {
			t.Errorf("fixtures %s and %s share the code %s; want 4 distinct", prev, n, ex.code)
		}
		primary[ex.code] = n
		for _, runner := range []bool{false, true} {
			_, issues := Check(td("invalid", "invariants", n), opts(t, runner))
			if !hasCode(issues, ex.code) {
				t.Errorf("%s (runner=%v): no %s issue; got:\n%s", n, runner, ex.code, joinIssues(issues))
			}
			if hasCode(issues, "POLICY_SCHEMA_INVALID") {
				t.Errorf("%s (runner=%v): rejected by the schema, want the loader's dedicated code; got:\n%s", n, runner, joinIssues(issues))
			}
		}
	}
	if len(primary) != 4 {
		t.Errorf("distinct codes = %d, want 4: %v", len(primary), primary)
	}
}

// 26-AC-1: near-misses stay valid. disableWorkerTimeout is allowed on a test-only harness.
func TestAcceptedNearMisses_26AC1(t *testing.T) {
	for _, name := range []string{"test-only-worker-timeout"} {
		t.Run(name, func(t *testing.T) {
			for _, runner := range []bool{false, true} {
				if _, issues := Check(td("accepted", name), opts(t, runner)); len(issues) != 0 {
					t.Errorf("runner=%v: accepted fixture has issues:\n%s", runner, joinIssues(issues))
				}
			}
		})
	}
}

// 26-AC-1 (D1): with no RoleDefinition present, CI mode does not demand the five roles.
func TestFiveRolesOnlyOnceARoleIsPresent_26AC1(t *testing.T) {
	dir := copyTree(t, td("valid"), false)
	if err := os.RemoveAll(filepath.Join(dir, "roles")); err != nil {
		t.Fatal(err)
	}
	_, issues := Check(dir, opts(t, false))
	for _, i := range issues {
		if strings.HasPrefix(i.Code, "ROLE_") {
			t.Errorf("CI mode without any role file reported %s", i)
		}
	}
	if len(issues) != 0 {
		t.Errorf("CI mode without any role file: want no issues, got:\n%s", joinIssues(issues))
	}
}

// verificationCases: one fixture per invariant of plan 22 section 9.5. The first ten hold only a verification.yaml;
// the five `repo-` cases also hold the repository documents.
var verificationCases = []string{
	"duplicate-class-name",
	"auto-class-judge-not-deterministic",
	"catch-all-class-auto",
	"auto-class-overlaps-human-class",
	"allowed-glob-inside-protected-set",
	"regex-not-re2-lookahead",
	"regex-not-re2-lookbehind-adapter",
	"required-status-contexts-missing-review",
	"retry-policy-violations-nonzero",
	"adapter-assertion-types-empty",
	"repo-test-paths-in-protected-set",
	"repo-fixture-paths-in-protected-set",
	"repo-test-infra-paths-in-repo-protected-set",
	"repo-stub-paths-in-protected-set",
	"repo-no-red-and-green-suite",
}

// 22-AC-1 (plan 22 section 9.5): every loader invariant has a fixture under invalid/verification/ that Check
// rejects with the sidecar's code.
func TestVerificationInvariantFixtures_22AC1(t *testing.T) {
	requireNames(t, "verification", verificationCases)
	for _, name := range fixtureNames(t, "verification") {
		t.Run(name, func(t *testing.T) {
			ex := readFixtureExpect(t, td("invalid", "verification", name+".expect"))
			assertFixture(t, td("invalid", "verification", name), ex, false)
		})
	}
}

// 22-AC-1: the repository documents of a fixture are checked against the fixture's own VerificationPolicy, and a
// well-formed pair is accepted. The documents are found under <dir>/.strange-company/.
func TestRepositorySideAcceptedWhenWellFormed_22AC1(t *testing.T) {
	dir := td("accepted", "repo-side")
	for _, f := range []string{"verification.yaml", ".strange-company/policy.yaml", ".strange-company/validation.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("fixture file missing: %v", err)
		}
	}
	if _, issues := Check(dir, opts(t, false)); len(issues) != 0 {
		t.Errorf("well-formed repository documents rejected:\n%s", joinIssues(issues))
	}
}

// 22-AC-1: the repository-side cases differ from the accepted pair in one file only.
func TestRepositorySideFixturesAreOneChangeFromAccepted_22AC1(t *testing.T) {
	base := treeFiles(t, td("accepted", "repo-side"))
	for _, name := range verificationCases {
		if !strings.HasPrefix(name, "repo-") {
			continue
		}
		t.Run(name, func(t *testing.T) {
			fx := treeFiles(t, td("invalid", "verification", name))
			var diff []string
			for p, b := range fx {
				if v, ok := base[p]; !ok || v != b {
					diff = append(diff, p)
				}
			}
			for p := range base {
				if _, ok := fx[p]; !ok {
					diff = append(diff, p)
				}
			}
			if len(diff) != 1 {
				t.Errorf("differs from accepted/repo-side in %v, want exactly one file", diff)
			}
		})
	}
}

// 22-AC-1: the platform VerificationPolicy of every verification fixture is the valid one with a single
// contiguous edit of at most three lines, so each of the ten policy cases tests one invariant.
func TestVerificationFixturesAreOneEditFromValid_22AC1(t *testing.T) {
	valid, err := os.ReadFile(td("valid", "verification.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	a := strings.Split(string(valid), "\n")
	for _, name := range verificationCases {
		if strings.HasPrefix(name, "repo-") {
			continue
		}
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(td("invalid", "verification", name, "verification.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			c := strings.Split(string(b), "\n")
			pre := 0
			for pre < len(a) && pre < len(c) && a[pre] == c[pre] {
				pre++
			}
			suf := 0
			for suf < len(a)-pre && suf < len(c)-pre && a[len(a)-1-suf] == c[len(c)-1-suf] {
				suf++
			}
			da, dc := len(a)-pre-suf, len(c)-pre-suf
			if da == 0 && dc == 0 {
				t.Fatal("fixture equals valid/verification.yaml")
			}
			if da > 3 || dc > 3 {
				t.Errorf("fixture replaces %d lines with %d, want a single small edit", da, dc)
			}
		})
	}
}
