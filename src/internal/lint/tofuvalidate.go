package lint

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"go.yaml.in/yaml/v3"
)

const (
	ruleTofuPreventDestroy = "tofu-prevent-destroy"
	ruleTofuGuardNoDelete  = "tofu-guard-no-delete"
	tofuDir                = "infra/tofu"
	tofuStorageGuard       = "infra/tofu/storage/guard.yaml"
)

// TofuValidate is the static HCL and guard check behind `make tofu-validate` (plan 06 section 14.2, 06-AC-9):
// every aws_s3_bucket under infra/tofu has prevent_destroy = true, and infra/tofu/storage/guard.yaml allows no
// delete action.
func TofuValidate(root string) (Result, error) {
	var res Result
	res.ran(ruleTofuPreventDestroy)
	res.ran(ruleTofuGuardNoDelete)
	base := filepath.Join(root, filepath.FromSlash(tofuDir))
	if st, err := os.Stat(base); err != nil || !st.IsDir() {
		return res, nil
	}
	var files []string
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".terraform" || d.Name() == "out") {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".tf") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	sort.Strings(files)
	for _, f := range files {
		rel, err := filepath.Rel(root, f)
		if err != nil {
			return res, err
		}
		src, err := os.ReadFile(f)
		if err != nil {
			return res, err
		}
		checkBuckets(&res, filepath.ToSlash(rel), src)
	}
	checkStorageGuard(&res, root)
	return res, nil
}

// checkBuckets reports each aws_s3_bucket resource whose lifecycle block does not set prevent_destroy to the
// literal true. Parsing the HCL (not matching text) keeps comments, strings and variables from counting.
func checkBuckets(res *Result, rel string, src []byte) {
	file, diags := hclsyntax.ParseConfig(src, rel, hcl.InitialPos)
	if diags.HasErrors() {
		res.add(ruleTofuPreventDestroy, rel, "cannot parse HCL: %s", diags.Error())
		return
	}
	body, ok := file.Body.(*hclsyntax.Body)
	if !ok {
		return
	}
	for _, blk := range body.Blocks {
		if blk.Type != "resource" || len(blk.Labels) != 2 || blk.Labels[0] != "aws_s3_bucket" {
			continue
		}
		addr := blk.Labels[0] + "." + blk.Labels[1]
		if !preventDestroy(blk.Body) {
			res.add(ruleTofuPreventDestroy, rel, "%s must have lifecycle { prevent_destroy = true } (a literal true)", addr)
		}
	}
}

func preventDestroy(body *hclsyntax.Body) bool {
	for _, lc := range body.Blocks {
		if lc.Type != "lifecycle" {
			continue
		}
		attr, ok := lc.Body.Attributes["prevent_destroy"]
		if !ok {
			continue
		}
		v, diags := attr.Expr.Value(nil)
		return !diags.HasErrors() && v.Type() == cty.Bool && v.IsKnown() && !v.IsNull() && v.True()
	}
	return false
}

type tofuGuard struct {
	Spec struct {
		Resources []struct {
			Type           string   `yaml:"type"`
			AllowedActions []string `yaml:"allowedActions"`
		} `yaml:"resources"`
	} `yaml:"spec"`
}

func checkStorageGuard(res *Result, root string) {
	if st, err := os.Stat(filepath.Join(root, "infra", "tofu", "storage")); err != nil || !st.IsDir() {
		return
	}
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(tofuStorageGuard)))
	if err != nil {
		res.add(ruleTofuGuardNoDelete, tofuStorageGuard, "the storage module needs a guard.yaml (TofuPlanGuard): %v", err)
		return
	}
	var g tofuGuard
	if err := yaml.Unmarshal(raw, &g); err != nil {
		res.add(ruleTofuGuardNoDelete, tofuStorageGuard, "guard.yaml is not valid YAML: %v", err)
		return
	}
	for _, r := range g.Spec.Resources {
		for _, a := range r.AllowedActions {
			if strings.EqualFold(a, "delete") {
				res.add(ruleTofuGuardNoDelete, tofuStorageGuard, "%s allows the delete action; the storage guard must never allow delete", r.Type)
			}
		}
	}
}
