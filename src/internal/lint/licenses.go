package lint

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/google/licensecheck"
	"go.yaml.in/yaml/v3"
)

// Licence compliance (plan 23 sections 9 and 14.11, CD-42): classify the licence files of every linked
// third-party module version and judge the result against security/license-policy.yaml.

const (
	ruleLicenseDenied  = "LICENSE_DENIED"
	ruleLicenseUnknown = "LICENSE_UNKNOWN"
	noAssertion        = "NOASSERTION"
	minCoveragePercent = 90.0
)

// LicenseRow is one linked third-party module version in out/licenses/report.json.
type LicenseRow struct {
	Module  string `json:"module"`
	Version string `json:"version"`
	SPDX    string `json:"spdx"`
	Verdict string `json:"verdict"`
	Rule    string `json:"rule,omitempty"`
}

type licensePolicy struct {
	Spec struct {
		Allow      []string `yaml:"allow"`
		Deny       []string `yaml:"deny"`
		Exceptions []struct {
			Module  string `yaml:"module"`
			Version string `yaml:"version"`
		} `yaml:"exceptions"`
	} `yaml:"spec"`
}

// spdxAlias maps the classifier's legacy ids to the SPDX ids the policy uses.
var spdxAlias = map[string]string{
	"GPL-2.0":  "GPL-2.0-only",
	"LGPL-2.1": "LGPL-2.1-only",
}

func licensesMain(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("licenses", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", ".", "repository root")
	policyPath := fs.String("policy", "", "license-policy.yaml (default <root>/security/license-policy.yaml)")
	modules := fs.String("modules", "", "file holding a `go list -deps -json` stream (default: run it in <root>/src)")
	modcache := fs.String("modcache", "", "module cache directory (default: go env GOMODCACHE)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *policyPath == "" {
		*policyPath = filepath.Join(*root, "security", "license-policy.yaml")
	}
	_, findings, err := LicensesCheck(*root, *policyPath, *modules, *modcache)
	if err != nil {
		fmt.Fprintf(stderr, "sc-lint licenses: %v\n", err)
		return 2
	}
	for _, f := range findings {
		fmt.Fprintln(stdout, f.String())
	}
	if len(findings) > 0 {
		return 1
	}
	return 0
}

// LicensesCheck classifies every linked third-party module and writes <root>/out/licenses/report.json.
func LicensesCheck(root, policyPath, modulesFile, modcache string) ([]LicenseRow, []Finding, error) {
	pb, err := os.ReadFile(policyPath)
	if err != nil {
		return nil, nil, err
	}
	var pol licensePolicy
	if err := yaml.Unmarshal(pb, &pol); err != nil {
		return nil, nil, fmt.Errorf("%s: %v", policyPath, err)
	}
	allow, deny := toSet(pol.Spec.Allow), toSet(pol.Spec.Deny)
	excepted := map[string]bool{}
	for _, e := range pol.Spec.Exceptions {
		excepted[e.Module+"@"+e.Version] = true
	}

	var stream []byte
	if modulesFile != "" {
		stream, err = os.ReadFile(modulesFile)
	} else {
		stream, err = goListDeps(filepath.Join(root, "src"))
	}
	if err != nil {
		return nil, nil, err
	}
	mods, err := thirdPartyModules(stream)
	if err != nil {
		return nil, nil, err
	}
	if modcache == "" {
		out, err := exec.Command("go", "env", "GOMODCACHE").Output()
		if err != nil {
			return nil, nil, fmt.Errorf("go env GOMODCACHE: %v", err)
		}
		modcache = strings.TrimSpace(string(out))
	}

	rows := []LicenseRow{}
	var findings []Finding
	for _, m := range mods {
		dir := filepath.Join(modcache, escapeModPath(m[0])+"@"+m[1])
		spdx, pass, rule, msg := judgeModule(dir, allow, deny)
		row := LicenseRow{Module: m[0], Version: m[1], SPDX: spdx, Verdict: "pass"}
		if !pass && excepted[m[0]+"@"+m[1]] {
			pass = true
		}
		if !pass {
			row.Verdict, row.Rule = "fail", rule
			findings = append(findings, Finding{Rule: rule, Path: m[0] + "@" + m[1], Message: msg, Severity: "error"})
		}
		rows = append(rows, row)
	}

	rep := struct {
		Rows     []LicenseRow `json:"rows"`
		Findings []Finding    `json:"findings"`
	}{rows, append([]Finding{}, findings...)}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	dir := filepath.Join(root, "out", "licenses")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), append(b, '\n'), 0o644); err != nil {
		return nil, nil, err
	}
	return rows, findings, nil
}

func toSet(l []string) map[string]bool {
	s := map[string]bool{}
	for _, v := range l {
		s[v] = true
	}
	return s
}

func goListDeps(srcDir string) ([]byte, error) {
	cmd := exec.Command("go", "list", "-deps", "-json", "./cmd/...")
	cmd.Dir = srcDir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list -deps: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// thirdPartyModules returns the sorted, de-duplicated [path, version] pairs, minus the standard library and
// the main module.
func thirdPartyModules(stream []byte) ([][2]string, error) {
	dec := json.NewDecoder(bytes.NewReader(stream))
	seen := map[[2]string]bool{}
	for dec.More() {
		var p struct {
			Standard bool
			Module   *struct {
				Path, Version string
				Main          bool
			}
		}
		if err := dec.Decode(&p); err != nil {
			return nil, fmt.Errorf("go list stream: %v", err)
		}
		if p.Standard || p.Module == nil || p.Module.Main {
			continue
		}
		seen[[2]string{p.Module.Path, p.Module.Version}] = true
	}
	out := make([][2]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out, nil
}

// escapeModPath applies Go's module-cache case escaping (an upper-case letter becomes "!" and its lower case).
func escapeModPath(p string) string {
	var b strings.Builder
	for _, r := range p {
		if unicode.IsUpper(r) {
			b.WriteByte('!')
			b.WriteRune(unicode.ToLower(r))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isLicenceFile(name string) bool {
	u := strings.ToUpper(name)
	if ext := filepath.Ext(u); ext == ".GO" || ext == ".MOD" || ext == ".SUM" {
		return false
	}
	for _, p := range []string{"LICENSE", "LICENCE", "COPYING", "UNLICENSE"} {
		if strings.HasPrefix(u, p) {
			return true
		}
	}
	return false
}

// classifyFile returns the SPDX ids found in one licence file (all of them apply: AND), or nil when the
// match coverage is below 90 % or nothing matched.
func classifyFile(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	cov := licensecheck.Scan(b)
	if len(cov.Match) == 0 || cov.Percent < minCoveragePercent {
		return nil
	}
	seen := map[string]bool{}
	var ids []string
	for _, m := range cov.Match {
		id := m.ID
		if a, ok := spdxAlias[id]; ok {
			id = a
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// judgeModule composes the module's SPDX expression (AND inside a file, OR between files) and decides
// whether it passes: some file must have every licence allowed and none denied.
func judgeModule(dir string, allow, deny map[string]bool) (spdx string, pass bool, rule, msg string) {
	entries, _ := os.ReadDir(dir)
	var exprs []string
	seenExpr := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !isLicenceFile(e.Name()) {
			continue
		}
		ids := classifyFile(filepath.Join(dir, e.Name()))
		if len(ids) == 0 {
			continue
		}
		expr := strings.Join(ids, " AND ")
		if !seenExpr[expr] {
			seenExpr[expr] = true
			exprs = append(exprs, expr)
		}
		ok := true
		for _, id := range ids {
			if !allow[id] || deny[id] {
				ok = false
			}
		}
		if ok {
			pass = true
		}
	}
	if len(exprs) == 0 {
		return noAssertion, false, ruleLicenseUnknown, "no licence file classified with at least 90 % coverage"
	}
	spdx = strings.Join(exprs, " OR ")
	if pass {
		return spdx, true, "", ""
	}
	return spdx, false, ruleLicenseDenied, fmt.Sprintf("licence %s is denied or not in the allow list", spdx)
}
