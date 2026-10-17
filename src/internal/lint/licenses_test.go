package lint_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"strange.company/sc/internal/lint"
)

// F02 slice S4, 23-AC-28: `sc-lint licenses` (make licenses-check), plan 23 section 14.11.
//
// CLI under test:
//
//	sc-lint licenses [--root DIR] [--policy FILE] [--modules FILE] [--modcache DIR]
//
// --root     repository root; the report is written to <root>/out/licenses/report.json (default ".").
// --policy   license-policy.yaml (default <root>/security/license-policy.yaml).
// --modules  a file holding the `go list -deps -json` stream (concatenated JSON objects) to use instead of
// running `go list -deps -json ./cmd/...` in <root>/src.
// --modcache a GOMODCACHE-shaped directory (<module>@<version>/LICENSE...) instead of `go env GOMODCACHE`.
//
// Findings are printed as "<severity>: <RULE>: <module>@<version>: <message>" and exit status is 1 when any
// module fails. report.json is {"rows":[{"module","version","spdx","verdict","rule"}], ...}: one row per linked
// third-party module version, verdict "pass" or "fail", rule only on a fail row (LICENSE_DENIED|LICENSE_UNKNOWN),
// spdx "NOASSERTION" for an unclassifiable or < 90 % licence.

type licRow struct {
	Module  string `json:"module"`
	Version string `json:"version"`
	SPDX    string `json:"spdx"`
	Verdict string `json:"verdict"`
	Rule    string `json:"rule"`
}

type licCase struct {
	root, policy, modules, modcache string
}

func fixtureModcache(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("testdata", "licenses", "modcache"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// newLicCase writes a go list stream naming the given "module@version" entries (plus standard-library and main
// module packages, and a second package of the first module) and a policy with the given exceptions YAML.
func newLicCase(t *testing.T, exceptions string, mods ...string) licCase {
	t.Helper()
	c := licCase{root: t.TempDir(), modcache: fixtureModcache(t)}
	pol := readFile(t, repoRoot(t), "security/license-policy.yaml")
	// The fixture policy keeps the real allow/deny lists but carries only the given exceptions, so these tests do
	// not depend on the real policy's exception list.
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(pol), &doc); err != nil {
		t.Fatal(err)
	}
	spec, ok := doc["spec"].(map[string]any)
	if !ok {
		t.Fatal("policy has no spec mapping")
	}
	var exc struct {
		Exceptions []any `yaml:"exceptions"`
	}
	if err := yaml.Unmarshal([]byte("exceptions:\n"+exceptions), &exc); err != nil {
		t.Fatal(err)
	}
	if exc.Exceptions == nil {
		exc.Exceptions = []any{}
	}
	spec["exceptions"] = exc.Exceptions
	pb, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	pol = string(pb)
	write(t, c.root, "policy.yaml", pol)
	c.policy = filepath.Join(c.root, "policy.yaml")
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	emit := func(v any) {
		if err := enc.Encode(v); err != nil {
			t.Fatal(err)
		}
	}
	type module struct {
		Path, Version string
		Main          bool `json:",omitempty"`
	}
	type pkg struct {
		ImportPath string
		Standard   bool `json:",omitempty"`
		Module     *module
	}
	emit(pkg{ImportPath: "fmt", Standard: true})
	emit(pkg{ImportPath: "strange.company/sc/cmd/sc-lint", Module: &module{Path: "strange.company/sc", Main: true}})
	for i, m := range mods {
		path, ver, _ := strings.Cut(m, "@")
		emit(pkg{ImportPath: path + "/sub", Module: &module{Path: path, Version: ver}})
		if i == 0 {
			emit(pkg{ImportPath: path, Module: &module{Path: path, Version: ver}})
		}
	}
	write(t, c.root, "modules.json", buf.String())
	c.modules = filepath.Join(c.root, "modules.json")
	return c
}

func (c licCase) run(t *testing.T) (code int, out string, rows []licRow) {
	t.Helper()
	var o, e bytes.Buffer
	code = lint.Main([]string{"licenses", "--root", c.root, "--policy", c.policy, "--modules", c.modules, "--modcache", c.modcache}, &o, &e)
	return code, o.String() + e.String(), readRows(t, c.root)
}

func readRows(t *testing.T, root string) []licRow {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "out", "licenses", "report.json"))
	if err != nil {
		t.Errorf("out/licenses/report.json not written: %v", err)
		return nil
	}
	var rep struct {
		Rows []licRow `json:"rows"`
	}
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Errorf("report.json: %v", err)
	}
	return rep.Rows
}

func findRow(rows []licRow, module, version string) (licRow, bool) {
	for _, r := range rows {
		if r.Module == module && r.Version == version {
			return r, true
		}
	}
	return licRow{}, false
}

// wantFail asserts a non-zero exit whose output names the module and the rule, and a matching fail row.
func wantFail(t *testing.T, c licCase, module, version, rule, spdx string) {
	t.Helper()
	code, out, rows := c.run(t)
	if code == 0 {
		t.Errorf("exit 0, want non-zero\n%s", out)
	}
	hit := false
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, module) && strings.Contains(l, rule) {
			hit = true
		}
	}
	if !hit {
		t.Errorf("no output line names %s and %s:\n%s", module, rule, out)
	}
	r, ok := findRow(rows, module, version)
	if !ok {
		t.Fatalf("no report row for %s@%s: %+v", module, version, rows)
	}
	if r.Verdict != "fail" || r.Rule != rule || r.SPDX != spdx {
		t.Errorf("row = %+v, want verdict fail, rule %s, spdx %s", r, rule, spdx)
	}
}

func wantPass(t *testing.T, c licCase, module, version string) licRow {
	t.Helper()
	code, out, rows := c.run(t)
	if code != 0 {
		t.Errorf("exit %d, want 0\n%s", code, out)
	}
	r, ok := findRow(rows, module, version)
	if !ok {
		t.Fatalf("no report row for %s@%s: %+v", module, version, rows)
	}
	if r.Verdict != "pass" {
		t.Errorf("row = %+v, want verdict pass", r)
	}
	return r
}

func TestLicenses_Allowed_23AC28(t *testing.T) {
	r := wantPass(t, newLicCase(t, "", "example.com/allowed@v1.0.0"), "example.com/allowed", "v1.0.0")
	if r.SPDX != "MIT" {
		t.Errorf("spdx = %q, want MIT", r.SPDX)
	}
}

func TestLicenses_ReportOneRowPerModuleVersion_ExcludesStdlibAndMain_23AC28(t *testing.T) {
	c := newLicCase(t, "", "example.com/allowed@v1.0.0", "example.com/dual@v1.0.0")
	_, _, rows := c.run(t)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want exactly the 2 third-party modules (one row each, no stdlib, no main module)", rows)
	}
	for _, r := range rows {
		if r.Module == "strange.company/sc" || r.Module == "std" || r.Module == "" || r.Version == "" || r.SPDX == "" || r.Verdict == "" {
			t.Errorf("bad row %+v", r)
		}
	}
}

func TestLicenses_GPL2OnlyDenied_23AC28(t *testing.T) {
	wantFail(t, newLicCase(t, "", "example.com/gplonly@v1.0.0"), "example.com/gplonly", "v1.0.0", "LICENSE_DENIED", "GPL-2.0-only")
}

func TestLicenses_SSPLDenied_23AC28(t *testing.T) {
	wantFail(t, newLicCase(t, "", "example.com/sspl@v1.0.0"), "example.com/sspl", "v1.0.0", "LICENSE_DENIED", "SSPL-1.0")
}

func TestLicenses_NoLicenceFileUnknown_23AC28(t *testing.T) {
	wantFail(t, newLicCase(t, "", "example.com/nolicense@v1.0.0"), "example.com/nolicense", "v1.0.0", "LICENSE_UNKNOWN", "NOASSERTION")
}

// plan 23 section 14.11: match coverage below 90 % is NOASSERTION and fails.
func TestLicenses_LowCoverageNoAssertion_23AC28(t *testing.T) {
	wantFail(t, newLicCase(t, "", "example.com/lowcoverage@v1.0.0"), "example.com/lowcoverage", "v1.0.0", "LICENSE_UNKNOWN", "NOASSERTION")
}

// A dual-licensed module (MIT OR GPL-2.0-only: LICENSE-MIT and COPYING) passes when any one licence is allowed.
func TestLicenses_DualLicensePasses_23AC28(t *testing.T) {
	r := wantPass(t, newLicCase(t, "", "example.com/dual@v1.0.0"), "example.com/dual", "v1.0.0")
	if !strings.Contains(r.SPDX, "MIT") || !strings.Contains(r.SPDX, "GPL-2.0-only") || !strings.Contains(r.SPDX, " OR ") {
		t.Errorf("spdx = %q, want an OR expression of MIT and GPL-2.0-only", r.SPDX)
	}
}

const licException = `    - module: example.com/excepted
      version: v1.0.0
      license: GPL-2.0-only
      reason: fixture
      approvedBy: reviewer
      created: "2026-10-17"
`

func TestLicenses_ExceptedVersionPasses_23AC28(t *testing.T) {
	r := wantPass(t, newLicCase(t, licException, "example.com/excepted@v1.0.0"), "example.com/excepted", "v1.0.0")
	if r.SPDX != "GPL-2.0-only" {
		t.Errorf("spdx = %q, want GPL-2.0-only (the exception does not change the classification)", r.SPDX)
	}
}

func TestLicenses_ExceptionForOtherVersionDenied_23AC28(t *testing.T) {
	wantFail(t, newLicCase(t, licException, "example.com/excepted@v1.1.0"), "example.com/excepted", "v1.1.0", "LICENSE_DENIED", "GPL-2.0-only")
}

// A failing module does not hide the others: every linked module version still has a row.
func TestLicenses_FailureKeepsAllRows_23AC28(t *testing.T) {
	c := newLicCase(t, "", "example.com/allowed@v1.0.0", "example.com/gplonly@v1.0.0")
	code, _, rows := c.run(t)
	if code == 0 {
		t.Error("exit 0, want non-zero")
	}
	if r, ok := findRow(rows, "example.com/allowed", "v1.0.0"); !ok || r.Verdict != "pass" {
		t.Errorf("allowed row = %+v, %v", r, ok)
	}
	if _, ok := findRow(rows, "example.com/gplonly", "v1.0.0"); !ok {
		t.Error("no row for the denied module")
	}
}

// goListThirdParty runs `go list -deps -json ./cmd/...` in src/ and returns the set of "module@version".
func goListThirdParty(t *testing.T, root string) []string {
	t.Helper()
	cmd := exec.Command("go", "list", "-deps", "-json", "./cmd/...")
	cmd.Dir = filepath.Join(root, "src")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, stderr.String())
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	set := map[string]bool{}
	for dec.More() {
		var p struct {
			Standard bool
			Module   *struct {
				Path, Version string
				Main          bool
			}
		}
		if err := dec.Decode(&p); err != nil {
			t.Fatal(err)
		}
		if p.Standard || p.Module == nil || p.Module.Main {
			continue
		}
		set[p.Module.Path+"@"+p.Module.Version] = true
	}
	var l []string
	for k := range set {
		l = append(l, k)
	}
	sort.Strings(l)
	return l
}

// 23-AC-28: exits 0 on the real tree and has one row per linked third-party module version.
func TestLicenses_RealTree_23AC28(t *testing.T) {
	root := repoRoot(t)
	want := goListThirdParty(t, root)
	if len(want) == 0 {
		t.Fatal("go list found no third-party module")
	}
	var o, e bytes.Buffer
	code := lint.Main([]string{"licenses", "--root", root}, &o, &e)
	if code != 0 {
		t.Errorf("sc-lint licenses on the real tree: exit %d\n%s%s", code, o.String(), e.String())
	}
	rows := readRows(t, root)
	// NOASSERTION is allowed only for a module@version the real policy names in an exception (plan 23 section 14.11).
	excepted := map[string]bool{}
	var pol struct {
		Spec struct {
			Exceptions []struct {
				Module  string `yaml:"module"`
				Version string `yaml:"version"`
			} `yaml:"exceptions"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(readFile(t, root, "security/license-policy.yaml")), &pol); err != nil {
		t.Fatal(err)
	}
	for _, x := range pol.Spec.Exceptions {
		excepted[x.Module+"@"+x.Version] = true
	}
	var got []string
	for _, r := range rows {
		key := r.Module + "@" + r.Version
		got = append(got, key)
		if r.Verdict != "pass" || r.SPDX == "" || (r.SPDX == "NOASSERTION" && !excepted[key]) {
			t.Errorf("row %+v: want a classified, passing module (NOASSERTION only for a policy exception)", r)
		}
	}
	sort.Strings(got)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("report modules differ from go list\n got  %v\n want %v", got, want)
	}
}

// 23-AC-28: `make licenses-check` exists and `make verify` runs it (plan 01 section 7.1: after secrets-scan).
func TestVerifyRunsLicensesCheck_23AC28(t *testing.T) {
	root := repoRoot(t)
	out, err := runCmd(t, root, 2*time.Minute, makeEnv(), "make", "-n", "licenses-check")
	if err != nil || !strings.Contains(out, "sc-lint licenses") {
		t.Errorf("`make -n licenses-check` (err %v) does not run sc-lint licenses:\n%s", err, out)
	}
	out, err = runCmd(t, root, 2*time.Minute, makeEnv(), "make", "-n", "--trace", "verify")
	if err != nil {
		t.Fatalf("make -n verify: %v\n%s", err, out)
	}
	var seq []string
	for _, m := range regexp.MustCompile(`update target '([^']+)'`).FindAllStringSubmatch(out, -1) {
		seq = append(seq, m[1])
	}
	idx := func(name string) int {
		for i, s := range seq {
			if s == name {
				return i
			}
		}
		return -1
	}
	l, s, c := idx("licenses-check"), idx("secrets-scan"), idx("contracts")
	if l < 0 {
		t.Fatalf("`make verify` does not run licenses-check: %s", fmt.Sprint(seq))
	}
	if !(s < l && l < c) {
		t.Errorf("licenses-check must run after secrets-scan and before contracts: %v", seq)
	}
}
