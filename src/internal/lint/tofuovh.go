package lint

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

const (
	ruleOVHSecretVars = "tofu-ovh-secret-vars"
	ruleOVHNoPassword = "tofu-ovh-no-password"
)

var (
	secretVarRE = regexp.MustCompile(`(?i)(secret|password|token|key)`)
	publicVarRE = regexp.MustCompile(`(?i)public`)
)

// checkOVHModules is the static HCL check of 04-AC-6 over infra/tofu/compute/ovh-*/*.tf: secret-looking variables
// are literally sensitive and ephemeral, and every ovh_vps sets do_not_send_password to the literal true.
func checkOVHModules(res *Result, root string) {
	files, _ := filepath.Glob(filepath.Join(root, "infra", "tofu", "compute", "ovh-*", "*.tf"))
	sort.Strings(files)
	for _, f := range files {
		rel, err := filepath.Rel(root, f)
		if err != nil {
			continue
		}
		rel = path.Clean(filepath.ToSlash(rel))
		src, err := os.ReadFile(f)
		if err != nil {
			res.add(ruleOVHSecretVars, rel, "cannot read: %v", err)
			continue
		}
		file, diags := hclsyntax.ParseConfig(src, rel, hcl.InitialPos)
		if diags.HasErrors() {
			res.add(ruleOVHSecretVars, rel, "cannot parse HCL: %s", diags.Error())
			continue
		}
		body, ok := file.Body.(*hclsyntax.Body)
		if !ok {
			continue
		}
		for _, blk := range body.Blocks {
			switch {
			case blk.Type == "variable" && len(blk.Labels) == 1:
				name := blk.Labels[0]
				if !secretVarRE.MatchString(name) || publicVarRE.MatchString(name) {
					continue
				}
				var missing []string
				for _, attr := range []string{"sensitive", "ephemeral"} {
					if !literalTrue(blk.Body, attr) {
						missing = append(missing, attr+" = true")
					}
				}
				if len(missing) > 0 {
					res.add(ruleOVHSecretVars, rel, "variable %s matches the secret-name pattern and must set %s (literals)", name, strings.Join(missing, " and "))
				}
			case blk.Type == "resource" && len(blk.Labels) == 2 && blk.Labels[0] == "ovh_vps":
				if !literalTrue(blk.Body, "do_not_send_password") {
					res.add(ruleOVHNoPassword, rel, "ovh_vps.%s must set do_not_send_password = true (a literal)", blk.Labels[1])
				}
			}
		}
	}
}

func literalTrue(b *hclsyntax.Body, name string) bool {
	attr, ok := b.Attributes[name]
	if !ok {
		return false
	}
	lit, ok := attr.Expr.(*hclsyntax.LiteralValueExpr)
	if !ok {
		return false
	}
	return lit.Val.IsKnown() && !lit.Val.IsNull() && lit.Val.Type().FriendlyName() == "bool" && lit.Val.True()
}
