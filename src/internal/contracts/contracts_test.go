package contracts_test

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// Row shape asserted (plan 01 8.3: "one row per (file, check, result)"): out/contracts/report.json is
// either a JSON array of rows or an object whose "rows" member is that array; each row has string
// members "file", "check" and "result" ("pass" or "fail"). Nothing else is asserted about rows.
type row struct {
	File   string `json:"file"`
	Check  string `json:"check"`
	Result string `json:"result"`
}

func repoRoot(t *testing.T) string {
	t.Helper()
	d, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(d, "SPEC.md")); err == nil {
			if _, err := os.Stat(filepath.Join(d, "plans", "dag.yaml")); err == nil {
				return d
			}
		}
		p := filepath.Dir(d)
		if p == d {
			t.Fatal("repository root not found")
		}
		d = p
	}
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

// treeCopy copies the tracked and untracked (not ignored) working tree into a fresh directory.
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
		if _, err := os.Lstat(filepath.Join(root, rel)); err != nil {
			continue
		}
		copyFile(t, filepath.Join(root, rel), filepath.Join(dst, rel))
	}
	return dst
}

func makeContracts(t *testing.T, dir string) (int, string) {
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

func readRows(t *testing.T, dir string) []row {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "out", "contracts", "report.json"))
	if err != nil {
		t.Fatalf("out/contracts/report.json not written: %v", err)
	}
	var rows []row
	if json.Unmarshal(b, &rows) != nil {
		var obj struct {
			Rows []row `json:"rows"`
		}
		if err := json.Unmarshal(b, &obj); err != nil {
			t.Fatalf("report.json is neither a row array nor {\"rows\": [...]}: %v", err)
		}
		rows = obj.Rows
	}
	return rows
}

func countFile(rows []row, file, result string) int {
	n := 0
	for _, r := range rows {
		if (r.File == file || strings.HasSuffix(r.File, "/"+file)) && r.Result == result {
			n++
		}
	}
	return n
}

func seq(m map[string]any, key string) int {
	if v, ok := m[key].([]any); ok {
		return len(v)
	}
	return 0
}

// expectedChecks is the minimum number of pass rows a schema file must have: the schema itself plus
// one per example, per x-invalid-examples entry and per x-defs-examples entry.
func expectedChecks(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	n := 1 + seq(doc, "examples") + seq(doc, "x-invalid-examples")
	if defs, ok := doc["x-defs-examples"].(map[string]any); ok {
		for _, v := range defs {
			if l, ok := v.([]any); ok {
				n += len(l)
			}
		}
	}
	return n
}

// 01-AC-20: `make contracts` exits 0 on the real contracts; report.json has a pass row for every schema
// and for each of its examples, invalid examples and x-defs-examples entries, and no fail row.
func TestMakeContracts_ReportHasRowPerCheck_01AC20(t *testing.T) {
	dir := treeCopy(t)
	code, out := makeContracts(t, dir)
	if code != 0 {
		t.Fatalf("`make contracts` exited %d, want 0:\n%s", code, out)
	}
	rows := readRows(t, dir)
	for _, r := range rows {
		if r.Result == "fail" {
			t.Errorf("fail row in report.json: %+v", r)
		}
	}
	schemas, err := filepath.Glob(filepath.Join(dir, "contracts", "*.schema.yaml"))
	if err != nil || len(schemas) == 0 {
		t.Fatalf("no schemas found: %v", err)
	}
	for _, s := range schemas {
		base := "contracts/" + filepath.Base(s)
		if got, want := countFile(rows, base, "pass"), expectedChecks(t, s); got < want {
			t.Errorf("%s: %d pass rows, want at least %d (schema + examples + invalid + defs examples)", base, got, want)
		}
	}
	if countFile(rows, "contracts/runner-api.openapi.yaml", "pass") < 1 {
		t.Errorf("no pass row for contracts/runner-api.openapi.yaml")
	}
}

// 52-AC-10 (and 01-AC-20): junit.xml has one testcase per report row.
func TestMakeContracts_JUnitMatchesReport_52AC10(t *testing.T) {
	dir := treeCopy(t)
	if code, out := makeContracts(t, dir); code != 0 {
		t.Fatalf("`make contracts` exited %d, want 0:\n%s", code, out)
	}
	rows := readRows(t, dir)
	b, err := os.ReadFile(filepath.Join(dir, "out", "contracts", "junit.xml"))
	if err != nil {
		t.Fatalf("out/contracts/junit.xml not written: %v", err)
	}
	cases := 0
	dec := xml.NewDecoder(bytes.NewReader(b))
	for {
		tok, err := dec.Token()
		if err != nil {
			if err != io.EOF {
				t.Fatalf("junit.xml is not well-formed XML: %v", err)
			}
			break
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "testcase" {
			cases++
		}
	}
	if len(rows) == 0 || cases != len(rows) {
		t.Errorf("junit.xml has %d testcases, report.json has %d rows; want equal and non-zero", cases, len(rows))
	}
}

// 01-AC-21: each negative fixture fails `make contracts` with its check ID on a line naming the file.
func TestMakeContracts_NegativeFixtures_01AC21(t *testing.T) {
	cases := []struct{ dir, file, id string }{
		{"wrong-id", "fx-wrong-id.schema.yaml", "CONTRACT_ID_INVALID"},
		{"examples-missing", "fx-examples-missing.schema.yaml", "CONTRACT_EXAMPLES_MISSING"},
		{"invalid-example-passes", "fx-invalid-example-passes.schema.yaml", "CONTRACT_INVALID_EXAMPLE_PASSES"},
		{"format-violation", "fx-format-violation.schema.yaml", "CONTRACT_EXAMPLE_INVALID"},
		{"duplicate-key", "fx-duplicate-key.schema.yaml", "CONTRACT_YAML_DUPLICATE_KEY"},
		{"openapi-unresolved-ref", "fx-openapi-unresolved-ref.openapi.yaml", "CONTRACT_OPENAPI_REF_UNRESOLVED"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.dir, func(t *testing.T) {
			dir := treeCopy(t)
			copyFile(t, filepath.Join("testdata", c.dir, c.file), filepath.Join(dir, "contracts", c.file))
			code, out := makeContracts(t, dir)
			if code == 0 {
				t.Errorf("`make contracts` exited 0 with fixture %s:\n%s", c.file, out)
			}
			found := false
			for _, l := range strings.Split(out, "\n") {
				if strings.Contains(l, c.id) && strings.Contains(l, c.file) {
					found = true
				}
			}
			if !found {
				t.Errorf("no output line naming both %s and %s:\n%s", c.id, c.file, out)
			}
		})
	}
}

// 01-AC-21 control: a well-formed fixture of the same shape passes and gets pass rows, so the
// negative results above are attributable to the single defect.
func TestMakeContracts_ValidFixturePasses_01AC21(t *testing.T) {
	dir := treeCopy(t)
	copyFile(t, filepath.Join("testdata", "valid", "fx-valid.schema.yaml"), filepath.Join(dir, "contracts", "fx-valid.schema.yaml"))
	if code, out := makeContracts(t, dir); code != 0 {
		t.Fatalf("`make contracts` exited %d, want 0:\n%s", code, out)
	}
	if got := countFile(readRows(t, dir), "contracts/fx-valid.schema.yaml", "pass"); got < 3 {
		t.Errorf("fx-valid.schema.yaml has %d pass rows, want at least 3 (schema, example, invalid example)", got)
	}
}

// 01-AC-24: version-locks.schema.yaml passes the schema checks, and the one §6.3 lock file present
// today (toolbox/tools.lock.yaml) validates against its kind (a pass row naming it).
func TestMakeContracts_VersionLocks_01AC24(t *testing.T) {
	dir := treeCopy(t)
	if code, out := makeContracts(t, dir); code != 0 {
		t.Fatalf("`make contracts` exited %d, want 0:\n%s", code, out)
	}
	rows := readRows(t, dir)
	if got := countFile(rows, "contracts/version-locks.schema.yaml", "pass"); got < 2 {
		t.Errorf("version-locks.schema.yaml has %d pass rows, want at least 2", got)
	}
	if countFile(rows, "toolbox/tools.lock.yaml", "pass") < 1 {
		t.Errorf("no pass row for toolbox/tools.lock.yaml")
	}
	if countFile(rows, "toolbox/tools.lock.yaml", "fail") != 0 {
		t.Errorf("fail row for toolbox/tools.lock.yaml")
	}
}
