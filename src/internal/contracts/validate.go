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
	CheckKindAmbiguous   = "CONTRACT_KIND_AMBIGUOUS"
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
		msg := oneLine(err.Error())
		if detail != "" {
			msg = detail + ": " + msg
		}
		detail = trim(msg)
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
	invalid, invalidIsList := m["x-invalid-examples"].([]any)
	if _, present := m["x-invalid-examples"]; present && !invalidIsList {
		r.fail(rel, CheckSchemaInvalid, "x-invalid-examples must be a list of {reason, value} mappings")
	}
	for i, e := range invalid {
		item, _ := e.(map[string]any)
		detail := fmt.Sprintf("x-invalid-examples[%d]", i)
		if item == nil {
			r.fail(rel, CheckInvalidPasses, detail+" is not a mapping with reason and value")
			continue
		}
		val, hasValue := item["value"]
		if !hasValue {
			r.fail(rel, CheckInvalidPasses, detail+" has no value")
			if str(item, "reason") == "" {
				r.fail(rel, CheckInvalidReason, detail+" has no reason")
			}
			continue
		}
		if str(item, "reason") == "" {
			r.fail(rel, CheckInvalidReason, detail+" has no reason")
		} else {
			r.pass(rel, CheckInvalidReason, detail)
		}
		if err := sch.Validate(val); err == nil {
			r.fail(rel, CheckInvalidPasses, detail+" validates but must fail")
		} else {
			r.pass(rel, CheckInvalidPasses, detail)
		}
	}
	defs, defsIsMap := m["x-defs-examples"].(map[string]any)
	if _, present := m["x-defs-examples"]; present && !defsIsMap {
		r.fail(rel, CheckSchemaInvalid, "x-defs-examples must be a mapping of $defs name to a list of examples")
	}
	names := make([]string, 0, len(defs))
	for n := range defs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		list, listOK := defs[name].([]any)
		if !listOK {
			r.fail(rel, CheckSchemaInvalid, fmt.Sprintf("x-defs-examples.%s must be a list of examples", name))
			continue
		}
		if len(list) == 0 {
			r.fail(rel, CheckSchemaInvalid, fmt.Sprintf("x-defs-examples.%s is an empty list; declare at least one example", name))
			continue
		}
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
	var ptrs []string
	collectExamples(m, "", &ptrs)
	for _, ptr := range ptrs {
		node := ptrNode(m, ptr)
		ex, _ := node["examples"].([]any)
		sch, err := compile(base + "#" + escapeFrag(ptr))
		if err != nil {
			r.fail(rel, CheckOpenAPIExample, fmt.Sprintf("%s: %s", ptr, oneLine(err.Error())))
			continue
		}
		for i, e := range ex {
			r.add(rel, CheckOpenAPIExample, sch.Validate(e), fmt.Sprintf("%s/examples[%d]", ptr, i))
		}
	}
}

// nameMapKey reports whether the member key of a map at ptr holds a map from names to schemas, so that
// its own member keys are names and not keywords.
func nameMapKey(ptr, k string) bool {
	switch k {
	case "properties", "patternProperties", "$defs", "dependentSchemas", "responses":
		return true
	case "schemas":
		return ptr == "/components"
	}
	return false
}

// collectExamples gathers the JSON pointers of every Schema Object position carrying an examples array.
// In a name map (names true: property names and the like) every child is visited, and the map itself is
// not a schema.
func collectExamples(v any, ptr string, out *[]string) { collectExamplesIn(v, ptr, false, out) }

func collectExamplesIn(v any, ptr string, names bool, out *[]string) {
	switch t := v.(type) {
	case map[string]any:
		if !names {
			if ex, ok := t["examples"].([]any); ok && len(ex) > 0 {
				*out = append(*out, ptr)
			}
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !names && isDataOrExtension(k) {
				continue
			}
			if names && isResponsesMap(ptr) && strings.HasPrefix(k, "x-") {
				continue // OAS: ^x- extensions are allowed directly under responses
			}
			collectExamplesIn(t[k], ptr+"/"+escapeToken(k), !names && nameMapKey(ptr, k), out)
		}
	case []any:
		for i, e := range t {
			collectExamplesIn(e, ptr+"/"+strconv.Itoa(i), false, out)
		}
	}
}

func escapeToken(k string) string {
	return strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1")
}

// isResponsesMap reports whether the name map at ptr is an operation's responses object.
func isResponsesMap(ptr string) bool {
	return strings.HasSuffix(ptr, "/responses") && !strings.HasSuffix(ptr, "/components/responses")
}

// isDataOrExtension reports keys whose values are instance data or extension objects, not schemas.
func isDataOrExtension(k string) bool {
	switch k {
	case "example", "examples", "default", "const", "enum":
		return true
	}
	return strings.HasPrefix(k, "x-")
}

func ptrNode(doc any, ptr string) map[string]any {
	cur := doc
	if ptr != "" {
		for _, tok := range strings.Split(ptr[1:], "/") {
			tok = strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
			switch t := cur.(type) {
			case map[string]any:
				cur = t[tok]
			case []any:
				i, _ := strconv.Atoi(tok)
				if i >= 0 && i < len(t) {
					cur = t[i]
				}
			}
		}
	}
	m, _ := cur.(map[string]any)
	return m
}

func escapeFrag(ptr string) string {
	return (&url.URL{Path: ptr}).EscapedPath()
}

// walkRefs calls fn for every $ref string. It does not look inside instance data (example, default, const,
// enum, a list-valued examples) or x- extensions; the member keys of name maps are always visited.
func walkRefs(v any, path string, fn func(path, ref string)) { walkRefsIn(v, path, false, fn) }

func walkRefsIn(v any, path string, names bool, fn func(path, ref string)) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if names && isResponsesMap(path) && strings.HasPrefix(k, "x-") {
				continue
			}
			if !names {
				if s, ok := t[k].(string); ok && k == "$ref" {
					fn(path, s)
					continue
				}
				_, isList := t[k].([]any)
				if strings.HasPrefix(k, "x-") || k == "example" || k == "default" || k == "const" || k == "enum" || (k == "examples" && isList) {
					continue
				}
			}
			walkRefsIn(t[k], path+"/"+escapeToken(k), !names && nameMapKey(path, k), fn)
		}
	case []any:
		for i, e := range t {
			walkRefsIn(e, path+"/"+strconv.Itoa(i), false, fn)
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
