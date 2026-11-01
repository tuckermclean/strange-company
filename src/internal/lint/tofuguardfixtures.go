package lint

import (
	"bytes"
	"os"
	"path"
	"path/filepath"
	"sort"

	"go.yaml.in/yaml/v3"

	"strange.company/sc/internal/provider/guard"
)

const (
	ruleGuardFixtures = "tofu-guard-fixtures"
	tofuPlansDir      = "test/fixtures/tofu-plans"
)

type guardExpectAttr struct {
	Type string `yaml:"type"`
	Path string `yaml:"path"`
	Want string `yaml:"want"`
}

type guardExpectFile struct {
	Module      string          `yaml:"module"`
	ProductCode string          `yaml:"productCode"`
	Region      guardExpectAttr `yaml:"region"`
	SSHKey      guardExpectAttr `yaml:"sshKey"`
	Cases       []struct {
		File    string `yaml:"file"`
		Nodes   int    `yaml:"nodes"`
		Destroy bool   `yaml:"destroy"`
		Want    string `yaml:"want"`
	} `yaml:"cases"`
}

// checkGuardFixtures is the table test of 04-AC-2 inside `make tofu-validate`: every
// test/fixtures/tofu-plans/<dir>/expect.yaml whose module directory exists is judged by the module's own
// guard.yaml and guard-destroy.yaml. Directories without expect.yaml and modules that do not exist are skipped.
func checkGuardFixtures(res *Result, root string) {
	plans := filepath.Join(root, filepath.FromSlash(tofuPlansDir))
	entries, err := os.ReadDir(plans)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dirRel := path.Join(tofuPlansDir, e.Name())
		expRel := path.Join(dirRel, "expect.yaml")
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(expRel)))
		if err != nil {
			continue
		}
		var ex guardExpectFile
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true)
		if err := dec.Decode(&ex); err != nil {
			res.add(ruleGuardFixtures, expRel, "expect.yaml is not valid: %v", err)
			continue
		}
		if ex.Module == "" {
			res.add(ruleGuardFixtures, expRel, "expect.yaml has no module")
			continue
		}
		modDir := filepath.Join(root, filepath.FromSlash(ex.Module))
		if st, err := os.Stat(modDir); err != nil || !st.IsDir() {
			continue
		}
		guards := map[bool]*guard.Guard{}
		for destroy, name := range map[bool]string{false: "guard.yaml", true: "guard-destroy.yaml"} {
			rel := path.Join(ex.Module, name)
			b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
			if err != nil {
				res.add(ruleGuardFixtures, rel, "the module owns its guard file but it cannot be read: %v", err)
				continue
			}
			g, err := guard.ParseGuard(b)
			if err != nil {
				res.add(ruleGuardFixtures, rel, "%v", err)
				continue
			}
			guards[destroy] = &g
		}
		covered := map[string]bool{}
		for _, c := range ex.Cases {
			covered[c.File] = true
			rel := path.Join(dirRel, c.File)
			plan, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
			if err != nil {
				res.add(ruleGuardFixtures, rel, "fixture named by expect.yaml cannot be read: %v", err)
				continue
			}
			g := guards[c.Destroy]
			if g == nil {
				continue // the missing or invalid guard file is already reported
			}
			v := g.Evaluate(plan, guard.Expect{
				ExpectedNodeCount: c.Nodes,
				ProductCode:       ex.ProductCode,
				Region:            guard.Attr{Type: ex.Region.Type, Path: ex.Region.Path, Want: ex.Region.Want},
				SSHKey:            guard.Attr{Type: ex.SSHKey.Type, Path: ex.SSHKey.Path, Want: ex.SSHKey.Want},
				Destroy:           c.Destroy,
			})
			got := v.Code
			if v.Pass {
				got = "pass"
			}
			if got != c.Want {
				res.add(ruleGuardFixtures, rel, "expected %s, got %s (%s)", c.Want, got, v.Message)
			}
		}
		jsons, _ := filepath.Glob(filepath.Join(plans, e.Name(), "*.json"))
		sort.Strings(jsons)
		for _, j := range jsons {
			name := filepath.Base(j)
			if !covered[name] {
				res.add(ruleGuardFixtures, path.Join(dirRel, name), "fixture is not covered by a case in expect.yaml")
			}
		}
	}
}
