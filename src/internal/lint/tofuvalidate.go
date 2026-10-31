package lint

import (
	"bytes"
	"encoding/json"
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
		if d.IsDir() && d.Name() == ".terraform" {
			return filepath.SkipDir
		}
		if !d.IsDir() && isTofuConfigFile(d.Name()) {
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
	if isTofuJSON(rel) {
		checkBucketsJSON(res, rel, src)
		return
	}
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

// isTofuConfigFile reports whether OpenTofu 1.13.1 loads the file name as module configuration.
func isTofuConfigFile(name string) bool {
	for _, suf := range []string{".tf", ".tofu", ".tf.json", ".tofu.json"} {
		if strings.HasSuffix(name, suf) {
			return true
		}
	}
	return false
}

func isTofuJSON(name string) bool {
	return strings.HasSuffix(name, ".tf.json") || strings.HasSuffix(name, ".tofu.json")
}

// jsonObjects reads a JSON-syntax block value: one object, or an array of objects (OpenTofu accepts both).
func jsonObjects(v any) ([]map[string]any, bool) {
	switch x := v.(type) {
	case map[string]any:
		return []map[string]any{x}, true
	case []any:
		var out []map[string]any
		for _, e := range x {
			m, ok := e.(map[string]any)
			if !ok {
				return nil, false
			}
			out = append(out, m)
		}
		return out, true
	}
	return nil, false
}

// checkBucketsJSON is checkBuckets for the JSON configuration syntax (*.tf.json, *.tofu.json). It reads the
// document with encoding/json (hcl/v2/json would link a module whose licence the policy does not allow); anything
// it cannot read fails closed.
func checkBucketsJSON(res *Result, rel string, src []byte) {
	if key, err := repeatedJSONKey(src); err != nil {
		res.add(ruleTofuPreventDestroy, rel, "cannot parse JSON configuration: %v", err)
		return
	} else if key != "" {
		res.add(ruleTofuPreventDestroy, rel, "repeated key %q in a JSON object: OpenTofu loads every repeated block while a reader keeps one, so the file cannot be checked (fail closed)", key)
		return
	}
	var doc map[string]any
	if err := json.Unmarshal(src, &doc); err != nil {
		res.add(ruleTofuPreventDestroy, rel, "cannot parse JSON configuration: %v", err)
		return
	}
	rawRes, found := doc["resource"]
	if !found {
		return
	}
	unreadable := func(what string) {
		res.add(ruleTofuPreventDestroy, rel, "cannot read the JSON configuration: %s", what)
	}
	types, ok := jsonObjects(rawRes)
	if !ok {
		unreadable("\"resource\" is not an object")
		return
	}
	for _, byType := range types {
		bucketsRaw, has := byType["aws_s3_bucket"]
		if !has {
			continue
		}
		groups, ok := jsonObjects(bucketsRaw)
		if !ok {
			unreadable("\"aws_s3_bucket\" is not an object")
			continue
		}
		for _, group := range groups {
			names := make([]string, 0, len(group))
			for n := range group {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, name := range names {
				addr := "aws_s3_bucket." + name
				bodies, ok := jsonObjects(group[name])
				if !ok {
					res.add(ruleTofuPreventDestroy, rel, "%s: cannot read the resource body", addr)
					continue
				}
				protected := false
				for _, body := range bodies {
					lcs, ok := jsonObjects(body["lifecycle"])
					if !ok {
						continue
					}
					for _, lc := range lcs {
						if v, has := lc["prevent_destroy"]; has {
							b, isBool := v.(bool)
							protected = isBool && b
							break
						}
					}
				}
				if !protected {
					res.add(ruleTofuPreventDestroy, rel, "%s must have lifecycle { prevent_destroy = true } (a literal true)", addr)
				}
			}
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

// repeatedJSONKey scans every object of the document token by token and returns the first key that appears twice
// in the same object ("" when none does). encoding/json alone keeps only the last value.
func repeatedJSONKey(src []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(src))
	var walk func() (string, error)
	walk = func() (string, error) {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		d, ok := tok.(json.Delim)
		if !ok {
			return "", nil
		}
		switch d {
		case '[':
			for dec.More() {
				if k, err := walk(); err != nil || k != "" {
					return k, err
				}
			}
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return "", err
				}
				key, _ := kt.(string)
				if seen[key] {
					return key, nil
				}
				seen[key] = true
				if k, err := walk(); err != nil || k != "" {
					return k, err
				}
			}
		}
		_, err = dec.Token() // the closing delimiter
		return "", err
	}
	return walk()
}
