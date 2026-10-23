package verify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Slice S6, 22-AC-3: "`go test ./src/verify/...` passes with >= 1 fixture per R0-R11, G0-G8, judge rule and tamper
// sub-code; classifying each fixture 100 times yields identical output bytes" (D1: ./src/internal/verify/...).
//
// The rule and sub-code lists are DERIVED, not typed here, so that a rule added later without a fixture fails the
// test:
//   - the red rules R0-R14 (with R1b and R11b) from the table of plan 22 section 14.3, the green rules G0-G9 (with
//     G4b, G4c, G5b) from section 14.7, the TEST_TAMPER sub-codes from the table of section 14.6 and the judge rules
//     from section 14.8 (its bold lead-ins, the codes listed after `judge_invalid`, and the `judge_` reason codes);
//   - every reason code of contracts/red-result.schema.yaml (RedClassification.reasons) and of
//     contracts/green-result.schema.yaml (GreenClassification.reasons), and every outcome of both.
// A plan row or an enum value without an entry in the reviewed tables below, or an entry without a plan row or an
// enum value, fails the test. The tables only say WHERE a rule is exercised: a fixture directory under testdata
// (its case directories are the fixtures) or, for a rule whose cases are built in Go, the named test functions.
// Go-built homes (a reviewed list, because those cases are constructed from code and have no directory): R11b and
// R14 (generation facts built in s2_generations_test.go) and protected_path_changed (CheckScope and CheckIngestion
// protected-path tests). The reason not_task_pull_request is a merge-time fact (plan 22 section 14.9, MergeFacts is
// C09's) and has no F06 classification rule.
// The completeness test reads the fixture directories and never writes to them.

type s6Home struct {
	Dir     string   // directory under testdata whose case subdirectories are the fixtures
	GoBuilt []string // names of Go test functions that build the cases
}

var s6Homes = map[string]s6Home{
	"R0": {Dir: "red/R0"}, "R1": {Dir: "red/R1"}, "R1b": {Dir: "gen/R1b"}, "R2": {Dir: "red/R2"}, "R3": {Dir: "red/R3"},
	"R4": {Dir: "red/R4"}, "R5": {Dir: "red/R5"}, "R6": {Dir: "red/R6"}, "R7": {Dir: "red/R7"}, "R8": {Dir: "red/R8"},
	"R9": {Dir: "red/R9"}, "R10": {Dir: "red/R10"}, "R11": {Dir: "red/R11"},
	"R11b": {GoBuilt: []string{"TestClassifyRedGen_R11bNondeterministic_22AC24", "TestClassifyRedGen_R11bMarkerConditional_22AC24"}},
	"R12":  {Dir: "gen/R12"}, "R13": {Dir: "gen/R13"},
	"R14": {GoBuilt: []string{"TestClassifyRedGen_R14ManifestTooLarge_22AC32"}},

	"G0": {Dir: "green/G0"}, "G1": {Dir: "green/G1"}, "G2": {Dir: "green/G2"}, "G3": {Dir: "green/G3"}, "G4": {Dir: "green/G4"},
	"G4b": {Dir: "green/G4b"}, "G4c": {Dir: "green/G4c"}, "G5": {Dir: "green/G5"}, "G5b": {Dir: "green/G5b"}, "G6": {Dir: "green/G6"},
	"G7": {Dir: "green/G7"}, "G8": {Dir: "green/G8"}, "G9": {Dir: "green/G9"},

	"manifest_file_changed": {Dir: "ingest/manifest_file_changed"}, "test_file_deleted": {Dir: "ingest/test_file_deleted"},
	"existing_test_modified": {Dir: "ingest/existing_test_modified"}, "test_dependency_changed": {Dir: "ingest/test_dependency_changed"},
	"skip_marker_added": {Dir: "ingest/skip_marker_added"}, "discovery_config_changed": {Dir: "ingest/discovery_config_changed"},
	"test_env_detection":     {Dir: "ingest/test_env_detection"},
	"protected_path_changed": {GoBuilt: []string{"TestCheckScope_TesterAndImplementerProtectedPaths_22AC33", "TestStrangeCompanyPolicyFileDoesNotListE2EProtected_22AC33"}},

	"judge:validation": {Dir: "judge/validation"}, "judge:judging": {Dir: "judge/judging"}, "judge:characterization": {Dir: "judge/characterization"},
	"judge:rubric": {Dir: "judge/rubric"}, "judge:schema": {Dir: "judge/schema"}, "judge:ac_not_covered": {Dir: "judge/ac_not_covered"},
	"judge:judge_check_error": {Dir: "judge/judge_check_error"}, "judge:judge_passes_on_base": {Dir: "judge/judge_passes_on_base"},
	"judge:judge_check_failed": {Dir: "judge/judge_check_failed"}, "judge:judge_artifact_changed": {Dir: "judge/judge_artifact_changed"},
}

// s6RedReasonHome maps every reason of RedClassification.reasons to the rule whose fixtures exercise it.
var s6RedReasonHome = map[string]string{
	"pipeline_identity_failed": "R0", "validation_config_mismatch": "R0", "scope_violation": "R1", "stub_malformed": "R1",
	"panic_stub_not_allowed": "R1", "tester_deletion": "R1", "existing_test_weakened": "R1b", "baseline_failing": "R2", "flaky_baseline": "R2",
	"build_failed": "R3", "report_missing": "R3", "suite_timed_out": "R3", "no_new_tests": "R4",
	"declared_test_not_discovered": "R5", "declared_test_not_new": "R5", "new_test_skipped": "R5", "ac_not_covered": "R6",
	"error_class_failure": "R7", "preexisting_test_broken": "R8", "preexisting_test_removed": "R8", "preservation_ac_not_passing": "R9",
	"change_ac_not_failing": "R10", "tests_truncated": "R11", "nondeterministic_test": "R11b", "marker_conditional": "R11b",
	"manifest_not_superset": "R12", "unapproved_test_change": "R13", "manifest_too_large": "R14",
	"judge_check_error": "judge:judge_check_error", "judge_passes_on_base": "judge:judge_passes_on_base",
}

// s6GreenReasonHome maps every reason of GreenClassification.reasons to the rule whose fixtures exercise it.
var s6GreenReasonHome = map[string]string{
	"pipeline_identity_failed": "G0", "validation_config_mismatch": "G0", "not_descendant_of_red": "G1", "foreign_commit": "G2",
	"build_failed": "G3", "suite_timed_out": "G3", "report_missing": "G3",
	"protected_test_failing": "G4", "protected_test_skipped": "G4", "protected_test_missing": "G4",
	"protected_pass_depends_on_new_test_files": "G4b", "nondeterministic_test": "G4c",
	"existing_test_failing": "G5", "new_skip_vs_base": "G5", "preexisting_test_removed": "G5b", "required_check_failed": "G6",
	"stub_not_replaced": "G7", "tests_truncated": "G8", "red_not_reproducible": "G9", "marker_conditional": "G9",
	"manifest_file_changed": "manifest_file_changed", "discovery_config_changed": "discovery_config_changed",
	"skip_marker_added": "skip_marker_added", "test_env_detection": "test_env_detection", "test_file_deleted": "test_file_deleted",
	"existing_test_modified": "existing_test_modified", "test_dependency_changed": "test_dependency_changed",
	"judge_check_failed": "judge:judge_check_failed", "judge_artifact_changed": "judge:judge_artifact_changed",
	"not_task_pull_request": "exempt:merge-time fact (plan 22 section 14.9, MergeFacts, C09)",
}

// s6ExemptReasons are the only reasons allowed to have no fixtures.
var s6ExemptReasons = []string{"not_task_pull_request"}

// s6TwinDirs are the fixture directories that hold passing twins of a rule family, not a rule.
var s6TwinDirs = map[string][]string{"red": {"valid"}, "green": {"ok"}, "ingest": {"clean"}}

var (
	s6RedRowRE    = regexp.MustCompile(`(?m)^\| (R\d+b?) \|`)
	s6GreenRowRE  = regexp.MustCompile(`(?m)^\| (G\d+[a-z]?) \|`)
	s6SubCodeRE   = regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\|")
	s6BoldLeadRE  = regexp.MustCompile(`(?m)^- \*\*([^*]+)\*\*`)
	s6InvalidList = regexp.MustCompile("`judge_invalid` \\(([^)]*)\\)")
)

// s6Section returns the text of the plan 22 section whose heading starts with "### <num> ".
func s6Section(t testing.TB, plan, num string) string {
	t.Helper()
	i := strings.Index(plan, "\n### "+num+" ")
	if i < 0 {
		t.Fatalf("plan 22 has no section %s", num)
	}
	rest := plan[i+1:]
	if j := strings.Index(rest[4:], "\n### "); j >= 0 {
		rest = rest[:j+4]
	}
	return rest
}

func s6Matches(re *regexp.Regexp, s string) []string {
	var out []string
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

func s6Dig(t testing.TB, v any, path ...string) any {
	t.Helper()
	for _, k := range path {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("contract path %v: %q is not under a mapping", path, k)
		}
		v = m[k]
	}
	return v
}

func s6StringEnum(t testing.TB, schema string, path ...string) []string {
	t.Helper()
	m := readYAMLMap(t, filepath.Join(repoRoot(t), "contracts", schema))
	list, ok := s6Dig(t, m, path...).([]any)
	if !ok || len(list) == 0 {
		t.Fatalf("%s %v: not a non-empty enum", schema, path)
	}
	var out []string
	for _, x := range list {
		out = append(out, x.(string))
	}
	sort.Strings(out)
	return out
}

func s6Keys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func s6Diff(have, want []string) (missing, extra []string) {
	h, w := map[string]bool{}, map[string]bool{}
	for _, x := range have {
		h[x] = true
	}
	for _, x := range want {
		w[x] = true
	}
	for x := range w {
		if !h[x] {
			missing = append(missing, x)
		}
	}
	for x := range h {
		if !w[x] {
			extra = append(extra, x)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return
}

// s6CaseDirsOf lists the case directories (each holding at least one file) under testdata/<dir>.
func s6CaseDirsOf(dir string) ([]string, error) {
	ents, err := os.ReadDir(filepath.Join("testdata", filepath.FromSlash(dir)))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join("testdata", filepath.FromSlash(dir), e.Name()))
		if err != nil {
			return nil, err
		}
		if len(files) > 0 {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// s6TestFuncs are the names of the Test functions of this package.
func s6TestFuncs(t testing.TB) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("*_test.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no test files: %v", err)
	}
	re := regexp.MustCompile(`(?m)^func (Test\w+)\(`)
	out := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range s6Matches(re, string(b)) {
			out[n] = true
		}
	}
	return out
}

func s6Expect2(t testing.TB, dir string) (outcome string, mentioned map[string]bool, isErr bool) {
	t.Helper()
	b := mustRead(t, filepath.Join("testdata", filepath.FromSlash(dir), "expect.json"))
	var e struct {
		Error    string    `json:"error"`
		Outcome  string    `json:"outcome"`
		Reasons  *[]string `json:"reasons"`
		Contains []string  `json:"contains"`
	}
	if err := json.Unmarshal(b, &e); err != nil {
		t.Fatalf("%s/expect.json: %v", dir, err)
	}
	mentioned = map[string]bool{}
	for _, r := range e.Contains {
		mentioned[r] = true
	}
	if e.Reasons != nil {
		for _, r := range *e.Reasons {
			mentioned[r] = true
		}
	}
	return e.Outcome, mentioned, e.Error != ""
}

// 22-AC-3: at least one fixture per rule R0-R11 (and R1b, R11b, R12, R13, R14), G0-G9 (and G4b, G4c, G5b), judge rule
// and tamper sub-code, derived from the plan tables and the contract enums.
func TestFixtureCompleteness_22AC3(t *testing.T) {
	root := repoRoot(t)
	plan := string(mustRead(t, root, "plans", "22-verification-policy.md"))

	// Derived rule lists.
	redRules := s6Matches(s6RedRowRE, s6Section(t, plan, "14.3"))
	greenRules := s6Matches(s6GreenRowRE, s6Section(t, plan, "14.7"))
	subCodes := s6Matches(s6SubCodeRE, s6Section(t, plan, "14.6"))
	if len(redRules) < 16 || len(greenRules) < 13 || len(subCodes) < 8 {
		t.Fatalf("plan tables parsed to %d red rules, %d green rules, %d sub-codes; want at least 16, 13 and 8", len(redRules), len(greenRules), len(subCodes))
	}
	redReasons := s6StringEnum(t, "red-result.schema.yaml", "$defs", "RedClassification", "properties", "reasons", "items", "enum")
	greenReasons := s6StringEnum(t, "green-result.schema.yaml", "$defs", "GreenClassification", "properties", "reasons", "items", "enum")
	redOutcomes := s6StringEnum(t, "red-result.schema.yaml", "$defs", "RedClassification", "properties", "outcome", "enum")
	greenOutcomes := s6StringEnum(t, "green-result.schema.yaml", "$defs", "GreenClassification", "properties", "outcome", "enum")

	// Judge rules: section 14.8 lead-ins, the codes after judge_invalid, and every judge_ reason of the contracts.
	sec := s6Section(t, plan, "14.8")
	leadDir := map[string]string{"Judge validation": "validation", "Judging": "judging", "Characterization judges": "characterization", "Rubric judges": "rubric"}
	if miss, extra := s6Diff(s6Matches(s6BoldLeadRE, sec), s6Keys(leadDir)); len(miss)+len(extra) > 0 {
		t.Fatalf("plan 22 section 14.8 lead-ins changed: missing %q, new %q: map them to judge fixture directories", miss, extra)
	}
	judgeDirs := map[string]bool{}
	for _, d := range leadDir {
		judgeDirs[d] = true
	}
	m := s6InvalidList.FindStringSubmatch(sec)
	if m == nil {
		t.Fatalf("plan 22 section 14.8 no longer lists the judge_invalid reasons")
	}
	for _, c := range strings.Split(m[1], ",") {
		judgeDirs[strings.Trim(strings.TrimSpace(c), "`")] = true
	}
	for _, r := range append(append([]string{}, redReasons...), greenReasons...) {
		if strings.HasPrefix(r, "judge_") {
			judgeDirs[r] = true
		}
	}
	var judgeRules []string
	for d := range judgeDirs {
		judgeRules = append(judgeRules, "judge:"+d)
	}

	// 1. The reviewed home table covers exactly the derived rules.
	derived := append(append(append(append([]string{}, redRules...), greenRules...), subCodes...), judgeRules...)
	sort.Strings(derived)
	if miss, extra := s6Diff(s6Keys(s6Homes), derived); len(miss)+len(extra) > 0 {
		t.Errorf("rules without a reviewed fixture home: %q; homes without a rule in the plan: %q", miss, extra)
	}

	// 2. Every red and green reason (and so every tamper sub-code of the enums) has a home.
	for _, tc := range []struct {
		name  string
		enum  []string
		table map[string]string
	}{{"red-result reasons", redReasons, s6RedReasonHome}, {"green-result reasons", greenReasons, s6GreenReasonHome}} {
		if miss, extra := s6Diff(s6Keys(tc.table), tc.enum); len(miss)+len(extra) > 0 {
			t.Errorf("%s without a fixture home: %q; table entries not in the contract enum: %q", tc.name, miss, extra)
		}
		for reason, home := range tc.table {
			if strings.HasPrefix(home, "exempt:") {
				if !s6Has(s6ExemptReasons, reason) {
					t.Errorf("%s: reason %q is exempt from fixtures but is not on the reviewed exempt list %q", tc.name, reason, s6ExemptReasons)
				}
				continue
			}
			if _, ok := s6Homes[home]; !ok {
				t.Errorf("%s: reason %q maps to %q, which is not a rule", tc.name, reason, home)
			}
		}
	}

	// 3. Every home exists: a fixture directory with at least one case, or Go test functions that are present.
	funcs := s6TestFuncs(t)
	for _, key := range s6Keys(s6Homes) {
		h := s6Homes[key]
		switch {
		case h.Dir != "" && len(h.GoBuilt) == 0:
			cases, err := s6CaseDirsOf(h.Dir)
			if err != nil || len(cases) == 0 {
				t.Errorf("rule %s: no fixture case under testdata/%s (%v)", key, h.Dir, err)
			}
		case h.Dir == "" && len(h.GoBuilt) > 0:
			for _, fn := range h.GoBuilt {
				if !funcs[fn] {
					t.Errorf("rule %s: Go-built home %s is not a test function of this package", key, fn)
				}
			}
		default:
			t.Errorf("rule %s: a home is either a fixture directory or Go test functions", key)
		}
	}

	// 4. No stray fixture directory: every directory of the classification fixture trees belongs to a rule or is a
	// named twin directory.
	known := map[string]bool{}
	for _, h := range s6Homes {
		if h.Dir != "" {
			known[h.Dir] = true
		}
	}
	for _, tree := range []string{"red", "gen", "green", "ingest", "judge"} {
		ents, err := os.ReadDir(filepath.Join("testdata", tree))
		if err != nil {
			t.Errorf("testdata/%s: %v", tree, err)
			continue
		}
		for _, e := range ents {
			if e.IsDir() && !known[tree+"/"+e.Name()] && !s6Has(s6TwinDirs[tree], e.Name()) {
				t.Errorf("testdata/%s/%s is a fixture directory of no rule and not a twin directory", tree, e.Name())
			}
		}
	}

	// 5. Judge fixtures: each case is runnable, each judge outcome and reason has a case that expects it.
	outcomes := map[string]bool{}
	mentioned := map[string]map[string]bool{}
	errCases := 0
	for d := range judgeDirs {
		cases, err := s6CaseDirsOf("judge/" + d)
		if err != nil || len(cases) == 0 {
			t.Errorf("judge rule %s: no fixture case (%v)", d, err)
			continue
		}
		mentioned[d] = map[string]bool{}
		for _, c := range cases {
			dir := "judge/" + d + "/" + c
			for _, f := range []string{"overlay.json", "expect.json"} {
				if _, err := os.Stat(filepath.Join("testdata", filepath.FromSlash(dir), f)); err != nil {
					t.Errorf("%s: no %s", dir, f)
				}
			}
			out, ment, isErr := s6Expect2(t, dir)
			if isErr {
				errCases++
			}
			outcomes[out] = true
			for r := range ment {
				mentioned[d][r] = true
			}
		}
	}
	for _, d := range s6Keys(judgeDirs) {
		if (strings.HasPrefix(d, "judge_") || d == "ac_not_covered") && !mentioned[d][d] {
			t.Errorf("judge rule %s: no case of testdata/judge/%s expects the reason %s", d, d, d)
		}
	}
	judgeOutcomes := map[string]bool{}
	for _, o := range append(append([]string{}, redOutcomes...), greenOutcomes...) {
		if strings.HasPrefix(o, "judge_") || strings.HasPrefix(o, "judging_") || strings.Contains(sec, "`"+o+"`") {
			judgeOutcomes[o] = true
		}
	}
	if len(judgeOutcomes) < 7 {
		t.Errorf("judge outcomes derived from the plan and the contracts = %v, want at least judge_valid, judge_invalid, already_satisfied, partially_satisfied, judging_pass, judging_fail, tamper_detected", s6Keys(judgeOutcomes))
	}
	for _, o := range s6Keys(judgeOutcomes) {
		if !outcomes[o] {
			t.Errorf("no judge fixture expects the outcome %s", o)
		}
	}
	if errCases == 0 {
		t.Errorf("no judge fixture expects a *NotClassifiedError (fail closed)")
	}
}
