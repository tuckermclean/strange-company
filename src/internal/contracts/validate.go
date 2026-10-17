// Package contracts implements sc-contracts, the contract validator behind `make contracts`
// (plan 01 section 8.3, CD-60).
package contracts

import (
	"embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed oas31-schema-2022-10-07.json
var oasFS embed.FS

const (
	dialect2020 = "https://json-schema.org/draft/2020-12/schema"
	idPrefix    = "https://strange.company/contracts/"
	oasURL      = "https://spec.openapis.org/oas/3.1/schema/2022-10-07"
	apiVersion  = "strange.company/v1alpha1"
)

// Check IDs. Failing lines carry the ID and the file.
const (
	CheckYAMLParse       = "CONTRACT_YAML_INVALID"
	CheckDuplicateKey    = "CONTRACT_YAML_DUPLICATE_KEY"
	CheckDialect         = "CONTRACT_SCHEMA_DIALECT"
	CheckIDInvalid       = "CONTRACT_ID_INVALID"
	CheckTitle           = "CONTRACT_TITLE_MISSING"
	CheckDescription     = "CONTRACT_DESCRIPTION_MISSING"
	CheckExamplesMissing = "CONTRACT_EXAMPLES_MISSING"
	CheckSchemaInvalid   = "CONTRACT_SCHEMA_INVALID"
	CheckExampleInvalid  = "CONTRACT_EXAMPLE_INVALID"
	CheckInvalidPasses   = "CONTRACT_INVALID_EXAMPLE_PASSES"
	CheckInvalidReason   = "CONTRACT_INVALID_EXAMPLE_REASON_MISSING"
	CheckDefsExample     = "CONTRACT_DEFS_EXAMPLE_INVALID"
	CheckOpenAPIInvalid  = "CONTRACT_OPENAPI_INVALID"
	CheckOpenAPIRef      = "CONTRACT_OPENAPI_REF_UNRESOLVED"
	CheckOpenAPIExample  = "CONTRACT_OPENAPI_EXAMPLE_INVALID"
	CheckKindUnknown     = "CONTRACT_KIND_UNKNOWN"
	CheckInstanceInvalid = "CONTRACT_INSTANCE_INVALID"
)

type runner struct {
	root string
	rows []Row
	c    *jsonschema.Compiler
}

func (r *runner) add(file, check string, err error, detail string) {
	res := "pass"
	if err != nil {
		res = "fail"
		detail = trim(oneLine(err.Error()))
	}
	r.rows = append(r.rows, Row{File: file, Check: check, Result: res, Detail: detail})
}

func (r *runner) fail(file, check, detail string) {
	r.rows = append(r.rows, Row{File: file, Check: check, Result: "fail", Detail: trim(detail)})
}

func (r *runner) pass(file, check, detail string) {
	r.rows = append(r.rows, Row{File: file, Check: check, Result: "pass", Detail: detail})
}

func trim(s string) string {
	if len(s) > 600 {
		return s[:600] + "..."
	}
	return s
}

// load reads and parses one file; on failure it records the fail row and returns ok=false.
func (r *runner) load(rel string) (any, bool) {
	b, err := os.ReadFile(filepath.Join(r.root, rel))
	if err != nil {
		r.fail(rel, CheckYAMLParse, err.Error())
		return nil, false
	}
	doc, err := parseYAML(b)
	var dk *dupKeyError
	switch {
	case errors.As(err, &dk):
		r.fail(rel, CheckDuplicateKey, dk.Error())
		return nil, false
	case err != nil:
		r.fail(rel, CheckYAMLParse, oneLine(err.Error()))
		return nil, false
	}
	r.pass(rel, CheckDuplicateKey, "no duplicate keys")
	return doc, true
}

func glob(root, pattern string) []string {
	m, _ := filepath.Glob(filepath.Join(root, pattern))
	var out []string
	for _, p := range m {
		rel, err := filepath.Rel(root, p)
		if err == nil {
			out = append(out, filepath.ToSlash(rel))
		}
	}
	sort.Strings(out)
	return out
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

type schemaFile struct {
	rel  string
	stem string
	doc  map[string]any
	id   string
}

// Run validates every contract under root and returns the report rows.
func Run(root string) []Row {
	r := &runner{root: root}
	r.c = jsonschema.NewCompiler()
	r.c.DefaultDraft(jsonschema.Draft2020)
	r.c.AssertFormat()
	r.c.UseRegexpEngine(compileECMA)

	var schemas []*schemaFile
	for _, rel := range glob(root, "contracts/*.schema.yaml") {
		doc, ok := r.load(rel)
		if !ok {
			continue
		}
		m, isMap := doc.(map[string]any)
		if !isMap {
			r.fail(rel, CheckSchemaInvalid, "schema document is not a mapping")
			continue
		}
		sf := &schemaFile{rel: rel, stem: strings.TrimSuffix(filepath.Base(rel), ".schema.yaml"), doc: m, id: str(m, "$id")}
		schemas = append(schemas, sf)
		if sf.id != "" {
			if err := r.c.AddResource(sf.id, m); err != nil {
				r.add(rel, CheckSchemaInvalid, err, "")
				sf.id = ""
			}
		}
	}
	oasDoc := r.loadOAS()

	compiled := map[string]*jsonschema.Schema{}
	compile := func(loc string) (*jsonschema.Schema, error) {
		if s, ok := compiled[loc]; ok {
			return s, nil
		}
		s, err := r.c.Compile(loc)
		if err == nil {
			compiled[loc] = s
		}
		return s, err
	}

	for _, sf := range schemas {
		r.checkSchema(sf, compile)
	}
	openapis := map[string]map[string]any{}
	for _, rel := range glob(root, "contracts/*.openapi.yaml") {
		r.checkOpenAPI(rel, schemas, oasDoc, openapis, compile)
	}
	r.checkInstances(schemas, compile)
	return r.rows
}

func (r *runner) loadOAS() *jsonschema.Schema {
	b, err := oasFS.ReadFile("oas31-schema-2022-10-07.json")
	if err != nil {
		r.fail("(embedded)", CheckOpenAPIInvalid, err.Error())
		return nil
	}
	doc, err := parseYAML(b) // JSON is a YAML subset
	if err == nil {
		err = r.c.AddResource(oasURL, doc)
	}
	var s *jsonschema.Schema
	if err == nil {
		s, err = r.c.Compile(oasURL)
	}
	if err != nil {
		r.fail("(embedded)", CheckOpenAPIInvalid, "embedded OAS 3.1 schema: "+oneLine(err.Error()))
		return nil
	}
	return s
}

func (r *runner) checkSchema(sf *schemaFile, compile func(string) (*jsonschema.Schema, error)) {
	rel, m := sf.rel, sf.doc
	if str(m, "$schema") == dialect2020 {
		r.pass(rel, CheckDialect, "")
	} else {
		r.fail(rel, CheckDialect, fmt.Sprintf("$schema is %q, want %s", str(m, "$schema"), dialect2020))
	}
	want := idPrefix + sf.stem + "/v1alpha1"
	if sf.id == want {
		r.pass(rel, CheckIDInvalid, want)
	} else {
		r.fail(rel, CheckIDInvalid, fmt.Sprintf("$id is %q, want %q", str(m, "$id"), want))
	}
	if str(m, "title") != "" {
		r.pass(rel, CheckTitle, "")
	} else {
		r.fail(rel, CheckTitle, "title is missing")
	}
	if str(m, "description") != "" {
		r.pass(rel, CheckDescription, "")
	} else {
		r.fail(rel, CheckDescription, "description is missing")
	}
	examples, _ := m["examples"].([]any)
	if len(examples) > 0 {
		r.pass(rel, CheckExamplesMissing, strconv.Itoa(len(examples))+" examples")
	} else {
		r.fail(rel, CheckExamplesMissing, "schema has no examples")
	}
	if sf.id == "" {
		return
	}
	sch, err := compile(sf.id)
	if err != nil {
		r.add(rel, CheckSchemaInvalid, err, "")
		return
	}
	r.pass(rel, CheckSchemaInvalid, "meta-validation")
	for i, ex := range examples {
		r.add(rel, CheckExampleInvalid, sch.Validate(ex), fmt.Sprintf("examples[%d]", i))
	}
	invalid, _ := m["x-invalid-examples"].([]any)
	for i, e := range invalid {
		item, _ := e.(map[string]any)
		detail := fmt.Sprintf("x-invalid-examples[%d]", i)
		if str(item, "reason") == "" {
			r.fail(rel, CheckInvalidReason, detail+" has no reason")
		} else {
			r.pass(rel, CheckInvalidReason, detail)
		}
		if item == nil {
			r.fail(rel, CheckInvalidPasses, detail+" is not a mapping with reason and value")
			continue
		}
		if err := sch.Validate(item["value"]); err == nil {
			r.fail(rel, CheckInvalidPasses, detail+" validates but must fail")
		} else {
			r.pass(rel, CheckInvalidPasses, detail)
		}
	}
	defs, _ := m["x-defs-examples"].(map[string]any)
	names := make([]string, 0, len(defs))
	for n := range defs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		list, _ := defs[name].([]any)
		ds, err := compile(sf.id + "#/$defs/" + name)
		if err != nil {
			r.fail(rel, CheckDefsExample, fmt.Sprintf("x-defs-examples.%s: %s", name, oneLine(err.Error())))
			continue
		}
		for i, ex := range list {
			r.add(rel, CheckDefsExample, ds.Validate(ex), fmt.Sprintf("x-defs-examples.%s[%d]", name, i))
		}
	}
}

func (r *runner) checkOpenAPI(rel string, schemas []*schemaFile, oas *jsonschema.Schema, docs map[string]map[string]any, compile func(string) (*jsonschema.Schema, error)) {
	doc, ok := r.load(rel)
	if !ok {
		return
	}
	m, isMap := doc.(map[string]any)
	if !isMap {
		r.fail(rel, CheckOpenAPIInvalid, "document is not a mapping")
		return
	}
	if oas != nil {
		r.add(rel, CheckOpenAPIInvalid, oas.Validate(m), "OpenAPI 3.1 structure")
	}
	byID := map[string]any{}
	for _, sf := range schemas {
		if sf.id != "" {
			byID[sf.id] = sf.doc
		}
	}
	bad := 0
	walkRefs(m, "", func(path, ref string) {
		if err := resolveRef(m, byID, ref); err != nil {
			bad++
			r.fail(rel, CheckOpenAPIRef, fmt.Sprintf("%s: $ref %q: %v", path, ref, err))
		}
	})
	if bad == 0 {
		r.pass(rel, CheckOpenAPIRef, "every $ref resolves")
	}
	if bad > 0 {
		return
	}
	// Schema examples of components.schemas validate against their schema.
	base := idPrefix + strings.TrimSuffix(filepath.Base(rel), ".openapi.yaml") + "/openapi"
	if err := r.c.AddResource(base, m); err != nil {
		r.add(rel, CheckOpenAPIInvalid, err, "")
		return
	}
	comps, _ := m["components"].(map[string]any)
	sm, _ := comps["schemas"].(map[string]any)
	names := make([]string, 0, len(sm))
	for n := range sm {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		s, _ := sm[n].(map[string]any)
		ex, _ := s["examples"].([]any)
		if len(ex) == 0 {
			continue
		}
		sch, err := compile(base + "#/components/schemas/" + n)
		if err != nil {
			r.fail(rel, CheckOpenAPIExample, fmt.Sprintf("components.schemas.%s: %s", n, oneLine(err.Error())))
			continue
		}
		for i, e := range ex {
			r.add(rel, CheckOpenAPIExample, sch.Validate(e), fmt.Sprintf("components.schemas.%s.examples[%d]", n, i))
		}
	}
}

func walkRefs(v any, path string, fn func(path, ref string)) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if s, ok := t[k].(string); ok && k == "$ref" {
				fn(path, s)
				continue
			}
			walkRefs(t[k], path+"/"+k, fn)
		}
	case []any:
		for i, e := range t {
			walkRefs(e, path+"/"+strconv.Itoa(i), fn)
		}
	}
}

// resolveRef resolves a $ref against the document itself or a registered contract schema by $id.
func resolveRef(self any, byID map[string]any, ref string) error {
	base, frag, _ := strings.Cut(ref, "#")
	target := self
	if base != "" {
		t, ok := byID[base]
		if !ok {
			return fmt.Errorf("no contract with $id %s", base)
		}
		target = t
	}
	frag, err := url.PathUnescape(frag)
	if err != nil {
		return err
	}
	if frag == "" {
		return nil
	}
	if !strings.HasPrefix(frag, "/") {
		if hasAnchor(target, frag) {
			return nil
		}
		return fmt.Errorf("anchor %q not found", frag)
	}
	cur := target
	for _, tok := range strings.Split(frag[1:], "/") {
		tok = strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
		switch t := cur.(type) {
		case map[string]any:
			next, ok := t[tok]
			if !ok {
				return fmt.Errorf("pointer segment %q not found", tok)
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(t) {
				return fmt.Errorf("pointer segment %q not found", tok)
			}
			cur = t[i]
		default:
			return fmt.Errorf("pointer segment %q not found", tok)
		}
	}
	return nil
}

func hasAnchor(v any, name string) bool {
	switch t := v.(type) {
	case map[string]any:
		if t["$anchor"] == name || t["$dynamicAnchor"] == name {
			return true
		}
		for _, e := range t {
			if hasAnchor(e, name) {
				return true
			}
		}
	case []any:
		for _, e := range t {
			if hasAnchor(e, name) {
				return true
			}
		}
	}
	return false
}

// instanceGlobs are the governed instance files validated by sc-contracts so far (version locks, plan 01 8.2);
// the remaining plan 01 section 8.3 globs are still validated by test/contracts/validate_instances.py.
var instanceGlobs = []string{"versions/*.yaml", "toolbox/tools.lock.yaml", "infra/lab/lab.yaml"}

func kindConst(s any) string {
	m, _ := s.(map[string]any)
	props, _ := m["properties"].(map[string]any)
	k, _ := props["kind"].(map[string]any)
	c, _ := k["const"].(string)
	return c
}

func (r *runner) checkInstances(schemas []*schemaFile, compile func(string) (*jsonschema.Schema, error)) {
	type target struct {
		prio int
		file string
		ref  string
	}
	byKind := map[string]target{}
	for _, sf := range schemas {
		if sf.id == "" {
			continue
		}
		consider := func(prio int, kind, ref string) {
			if kind == "" {
				return
			}
			t := target{prio, sf.rel, ref}
			if old, ok := byKind[kind]; !ok || t.prio < old.prio || (t.prio == old.prio && t.file < old.file) {
				byKind[kind] = t
			}
		}
		consider(0, kindConst(sf.doc), sf.id)
		defs, _ := sf.doc["$defs"].(map[string]any)
		for name, d := range defs {
			consider(1, kindConst(d), sf.id+"#/$defs/"+name)
		}
	}
	for _, g := range instanceGlobs {
		for _, rel := range glob(r.root, g) {
			doc, ok := r.load(rel)
			if !ok {
				continue
			}
			m, _ := doc.(map[string]any)
			kind := str(m, "kind")
			t, known := byKind[kind]
			if m == nil || str(m, "apiVersion") != apiVersion || !known {
				r.fail(rel, CheckKindUnknown, fmt.Sprintf("apiVersion=%q kind=%q has no schema", str(m, "apiVersion"), kind))
				continue
			}
			sch, err := compile(t.ref)
			if err != nil {
				r.fail(rel, CheckInstanceInvalid, oneLine(err.Error()))
				continue
			}
			if err := sch.Validate(m); err != nil {
				r.fail(rel, CheckInstanceInvalid, fmt.Sprintf("kind %s does not validate against %s: %s", kind, t.file, oneLine(err.Error())))
			} else {
				r.pass(rel, CheckInstanceInvalid, fmt.Sprintf("kind %s validated against %s", kind, t.file))
			}
		}
	}
}
