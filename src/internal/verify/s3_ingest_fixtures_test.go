package verify

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// CheckIngestion fixtures (plan 22 section 14.6, 22-AC-8 and 22-AC-22). A case is a directory under
// testdata/ingest/<sub_code or clean>/<case>/ with
//
//	base/**    files written on top of the standard repository (pkg/core.go, and pkg/core_test.go, which is always a
//	           manifest file) and committed as the red commit,
//	head/**    files written (after ops) and committed as the implementer's commit,
//	ops        optional lines: `delete P`, `rename A B`, `copy A B`, `chmod+x P`, `symlink P TARGET`,
//	expect.json {"manifest": [extra manifest paths], "violations": [{code, sub_code, path, line?}], "allow_extra": [keys]}.
//
// Every fixture file carries a `.fx` suffix that the runner strips, so that configuration files (go.mod,
// package.json, jest.config.js, ...) and test files never act as real ones inside this repository. Git facts (the
// diff, blob ids, modes, the manifest blobs) are derived from the scratch repository the runner builds, never
// written into the fixture. Violations are compared as sets of (code, sub_code, path); a `line`, when given, must
// equal the violation's line.

type fixtureViolation struct {
	Code    string `json:"code"`
	SubCode string `json:"sub_code"`
	Path    string `json:"path"`
	Line    int    `json:"line"`
}

type ingestExpect struct {
	Manifest   []string           `json:"manifest"`
	Violations []fixtureViolation `json:"violations"`
	AllowExtra []string           `json:"allow_extra"`
}

const s3CoreTest = "package pkg\n\nimport \"testing\"\n\nfunc TestCore(t *testing.T) {\n\tif Core() != 1 {\n\t\tt.Fatal(\"want 1\")\n\t}\n}\n"

// ingestSubCodeDirs are the fixture directories of the tamper sub-codes; clean holds the passing twins.
var ingestSubCodeDirs = []string{"manifest_file_changed", "test_file_deleted", "existing_test_modified", "test_dependency_changed",
	"skip_marker_added", "discovery_config_changed", "test_env_detection", "clean"}

// ingestAC22Cases are the cases of 22-AC-22: golden_modified, test_helper_modified, assertion_lib_replaced, a vendor/
// edit, nested_go_mod and a tsconfig.json paths change. 22-AC-8 covers every other case.
var ingestAC22Cases = []string{
	"existing_test_modified/golden_modified", "existing_test_modified/test_helper_modified",
	"test_dependency_changed/assertion_lib_replaced", "test_dependency_changed/vendor_edited",
	"discovery_config_changed/nested_go_mod", "discovery_config_changed/tsconfig_paths_changed",
}

func ingestCaseNames(t *testing.T) []string {
	t.Helper()
	root := filepath.Join("testdata", "ingest")
	var names []string
	for _, d := range ingestSubCodeDirs {
		ents, err := os.ReadDir(filepath.Join(root, d))
		if err != nil {
			t.Fatalf("fixture directory %s: %v", d, err)
		}
		if len(ents) == 0 {
			t.Fatalf("fixture directory %s is empty", d)
		}
		for _, e := range ents {
			if e.IsDir() {
				names = append(names, d+"/"+e.Name())
			}
		}
	}
	return names
}

func writeFixtureTree(t *testing.T, s *scratch, dir string) {
	t.Helper()
	if _, err := os.Stat(dir); err != nil {
		return
	}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if !strings.HasSuffix(rel, ".fx") {
			t.Fatalf("fixture file without .fx suffix: %s", p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		s.writeBytes(strings.TrimSuffix(filepath.ToSlash(rel), ".fx"), b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func applyOps(t *testing.T, s *scratch, file string) {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) == 2 && f[0] == "delete":
			s.remove(f[1])
		case len(f) == 3 && f[0] == "rename":
			s.rename(f[1], f[2])
		case len(f) == 3 && f[0] == "copy":
			s.copyFile(f[1], f[2])
		case len(f) == 2 && f[0] == "chmod+x":
			s.chmod(f[1], 0o755)
		case len(f) == 3 && f[0] == "symlink":
			s.symlink(f[1], f[2])
		default:
			t.Fatalf("%s: unknown op %q", file, line)
		}
	}
}

// runIngestFixture builds the repository of a case and returns the violations CheckIngestion reports for
// red..head together with the expectation.
func runIngestFixture(t *testing.T, name string) ([]Violation, ingestExpect) {
	t.Helper()
	dir := filepath.Join("testdata", "ingest", filepath.FromSlash(name))
	var exp ingestExpect
	readJSONFile(t, &exp, dir, "expect.json")
	s := newScratch(t)
	s.write("pkg/core.go", "package pkg\n\nfunc Core() int { return 0 }\n")
	s.write("pkg/core_test.go", s3CoreTest)
	writeFixtureTree(t, s, filepath.Join(dir, "base"))
	red := s.commit("red")
	applyOps(t, s, filepath.Join(dir, "ops"))
	writeFixtureTree(t, s, filepath.Join(dir, "head"))
	head := s.commit("implementation")
	man := s.s3Manifest(red, append([]string{"pkg/core_test.go"}, exp.Manifest...)...)
	got, err := CheckIngestion(s.ingestionInput(t, "implementer", red, head, man))
	if err != nil {
		t.Fatalf("CheckIngestion: %v", err)
	}
	return got, exp
}

func compareFixtureViolations(t *testing.T, got []Violation, exp ingestExpect) {
	t.Helper()
	gotSet := map[string]Violation{}
	for _, v := range got {
		gotSet[violationKey(v)] = v
	}
	wantSet := map[string]fixtureViolation{}
	for _, v := range exp.Violations {
		wantSet[v.Code+"/"+v.SubCode+" "+v.Path] = v
	}
	allow := map[string]bool{}
	for _, a := range exp.AllowExtra {
		allow[a] = true
	}
	var missing, extra []string
	for k, w := range wantSet {
		g, ok := gotSet[k]
		if !ok {
			missing = append(missing, k)
			continue
		}
		if w.Line > 0 && g.Line != w.Line {
			t.Errorf("%s: line %d, want %d", k, g.Line, w.Line)
		}
	}
	for k := range gotSet {
		if _, ok := wantSet[k]; !ok && !allow[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("not flagged: %q", missing)
	}
	if len(extra) > 0 {
		t.Errorf("flagged but not expected: %q", extra)
	}
}

// 22-AC-8: every tamper sub-code is flagged on its fixtures (manifest_file_changed, test_file_deleted,
// existing_test_modified, skip_marker_added for Go, Python, JavaScript, TypeScript and Rust,
// discovery_config_changed for jest.config.js and package.json scripts.test, test_env_detection for
// PYTEST_CURRENT_TEST and JEST_WORKER_ID, ...), and nothing is flagged on the clean implementation fixture and the
// other passing twins.
func TestCheckIngestion_Fixtures_22AC8(t *testing.T) {
	checkIngestFixtureLayout(t)
	skip := map[string]bool{}
	for _, c := range ingestAC22Cases {
		skip[c] = true
	}
	n := 0
	for _, name := range ingestCaseNames(t) {
		if skip[name] {
			continue
		}
		n++
		t.Run(name, func(t *testing.T) {
			got, exp := runIngestFixture(t, name)
			compareFixtureViolations(t, got, exp)
		})
	}
	if n < 100 {
		t.Fatalf("only %d fixtures found under testdata/ingest", n)
	}
}

// 22-AC-22: test-protection gaps closed (TDD-1/TDD-4): existing_test_modified for golden_modified and
// test_helper_modified, test_dependency_changed for assertion_lib_replaced (go.mod replace of testify) and a vendor/
// edit, discovery_config_changed for nested_go_mod and a tsconfig.json paths change.
func TestCheckIngestion_Fixtures_22AC22(t *testing.T) {
	for _, name := range ingestAC22Cases {
		t.Run(name, func(t *testing.T) {
			got, exp := runIngestFixture(t, name)
			if len(exp.Violations) != 1 {
				t.Fatalf("fixture %s must expect exactly one violation", name)
			}
			compareFixtureViolations(t, got, exp)
		})
	}
}

// checkIngestFixtureLayout: every directory under testdata/ingest is one of the named sub-code directories (so a
// stray directory is not silently ignored), every case has an expect.json, only clean twins expect nothing, and a
// sub-code case expects its own sub-code.
func checkIngestFixtureLayout(t *testing.T) {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join("testdata", "ingest"))
	if err != nil {
		t.Fatal(err)
	}
	want := sortedCopy(ingestSubCodeDirs)
	var got []string
	for _, e := range ents {
		got = append(got, e.Name())
	}
	if !equalStrings(sortedCopy(got), want) {
		t.Fatalf("testdata/ingest holds %q, want %q", got, want)
	}
	for _, name := range ingestCaseNames(t) {
		dir := filepath.Join("testdata", "ingest", filepath.FromSlash(name))
		if _, err := os.Stat(filepath.Join(dir, "expect.json")); err != nil {
			t.Errorf("%s: no expect.json", name)
		}
		var exp ingestExpect
		b, _ := os.ReadFile(filepath.Join(dir, "expect.json"))
		if err := json.Unmarshal(b, &exp); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if strings.HasPrefix(name, "clean/") != (len(exp.Violations) == 0) {
			t.Errorf("%s: only clean twins expect no violation", name)
		}
		if !strings.HasPrefix(name, "clean/") {
			sub := strings.SplitN(name, "/", 2)[0]
			found := false
			for _, v := range exp.Violations {
				found = found || v.SubCode == sub
			}
			if !found {
				t.Errorf("%s: no expected violation carries its sub-code %s", name, sub)
			}
		}
	}
}
