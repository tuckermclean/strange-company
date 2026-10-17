package contracts

import (
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// glob rows of the plan 01 section 8.3 table. A kind row selects its schema from apiVersion + kind; a fixed
// row validates every member against one schema (or $defs entry); a register row is a fixed row on the
// file's companion schema.
type globRow struct {
	pattern string
	schema  string // fixed rows: schema file name; empty for kind rows
	def     string // fixed rows: optional $defs entry
}

var instanceRows = []globRow{
	{pattern: "infra/profiles/*.yaml"},
	{pattern: "versions/*.yaml"},
	{pattern: "toolbox/tools.lock.yaml"},
	{pattern: "toolbox/image.lock"},
	{pattern: "infra/lab/lab.yaml"},
	{pattern: "policy/**/*.yaml"},
	{pattern: ".strange-company/*.yaml"},
	{pattern: "context/**/*.yaml"},
	{pattern: "infra/catalog/prices/*.yaml", schema: "provider.schema.yaml", def: "InfraPriceCatalog"},
	{pattern: "infra/tofu/**/guard*.yaml", schema: "provider.schema.yaml", def: "TofuPlanGuard"},
	{pattern: "gitops/clusters/*/bootstrap-config.yaml", schema: "bootstrap-config.schema.yaml"},
	{pattern: "contracts/alert-catalog.yaml", schema: "alert-catalog.schema.yaml"},
	{pattern: "contracts/secret-classes.yaml", schema: "secret-classes.schema.yaml"},
	{pattern: "contracts/state-machine.yaml", schema: "state-machine.schema.yaml"},
}

// matchSegs matches path segments against pattern segments; "**" matches zero or more directories.
func matchSegs(pat, name []string) bool {
	if len(pat) == 0 {
		return len(name) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(name); i++ {
			if matchSegs(pat[1:], name[i:]) {
				return true
			}
		}
		return false
	}
	if len(name) == 0 {
		return false
	}
	if ok, _ := path.Match(pat[0], name[0]); !ok {
		return false
	}
	return matchSegs(pat[1:], name[1:])
}

// globStar returns the sorted regular files under root whose slash path matches pattern.
func globStar(root, pattern string) []string {
	segs := strings.Split(pattern, "/")
	base := ""
	for _, s := range segs {
		if strings.ContainsAny(s, "*?[") {
			break
		}
		base = path.Join(base, s)
	}
	var out []string
	start := filepath.Join(root, filepath.FromSlash(base))
	_ = filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if matchSegs(segs, strings.Split(rel, "/")) {
			out = append(out, rel)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func kindConst(s any) string {
	m, _ := s.(map[string]any)
	props, _ := m["properties"].(map[string]any)
	k, _ := props["kind"].(map[string]any)
	c, _ := k["const"].(string)
	return c
}

func (r *runner) checkInstances(schemas []*schemaFile, compile func(string) (*jsonschema.Schema, error)) {
	type target struct {
		file string
		ref  string
	}
	byKind := map[string][]target{}
	byFile := map[string]*schemaFile{}
	for _, sf := range schemas { // schemas arrive sorted by file
		byFile[filepath.Base(sf.rel)] = sf
		if sf.id == "" {
			continue
		}
		consider := func(kind, ref string) {
			if kind != "" {
				byKind[kind] = append(byKind[kind], target{sf.rel, ref})
			}
		}
		consider(kindConst(sf.doc), sf.id)
		defs, _ := sf.doc["$defs"].(map[string]any)
		names := make([]string, 0, len(defs))
		for n := range defs {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, name := range names {
			consider(kindConst(defs[name]), sf.id+"#/$defs/"+name)
		}
	}
	kinds := make([]string, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	ambiguous := map[string]bool{}
	for _, kind := range kinds {
		ts := byKind[kind]
		if len(ts) < 2 {
			continue
		}
		ambiguous[kind] = true
		locs := make([]string, len(ts))
		for i, t := range ts {
			locs[i] = t.ref
		}
		detail := fmt.Sprintf("kind %q is claimed by %d schema locations: %s", kind, len(ts), strings.Join(locs, ", "))
		last := ""
		for _, t := range ts {
			if t.file != last {
				r.fail(t.file, CheckKindAmbiguous, detail)
				last = t.file
			}
		}
	}
	seen := map[string]bool{}
	for _, row := range instanceRows {
		for _, rel := range globStar(r.root, row.pattern) {
			if seen[rel] {
				continue
			}
			seen[rel] = true
			doc, ok := r.load(rel)
			if !ok {
				continue
			}
			m, _ := doc.(map[string]any)
			var t target
			label := ""
			if row.schema != "" {
				sf := byFile[row.schema]
				if sf == nil || sf.id == "" {
					r.fail(rel, CheckInstanceInvalid, fmt.Sprintf("schema %s is not available", row.schema))
					continue
				}
				t = target{sf.rel, sf.id}
				if row.def != "" {
					t.ref += "#/$defs/" + row.def
				}
				label = "validated against"
			} else {
				kind := str(m, "kind")
				if ambiguous[kind] {
					continue // CONTRACT_KIND_AMBIGUOUS already failed for this kind
				}
				ts := byKind[kind]
				if m == nil || str(m, "apiVersion") != apiVersion || len(ts) != 1 {
					r.fail(rel, CheckKindUnknown, fmt.Sprintf("apiVersion=%q kind=%q has no schema", str(m, "apiVersion"), kind))
					continue
				}
				t = ts[0]
				label = "kind " + kind + " validated against"
			}
			sch, err := compile(t.ref)
			if err != nil {
				r.fail(rel, CheckInstanceInvalid, oneLine(err.Error()))
				continue
			}
			if err := sch.Validate(m); err != nil {
				r.fail(rel, CheckInstanceInvalid, fmt.Sprintf("does not validate against %s: %s", t.file, oneLine(err.Error())))
			} else {
				r.pass(rel, CheckInstanceInvalid, label+" "+t.file)
			}
		}
	}
}
