package verify

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// schemaNode follows a JSON pointer through the decoded schema and resolves a final local $ref.
func schemaNode(t *testing.T, doc map[string]any, ptr string) map[string]any {
	t.Helper()
	var cur any = doc
	for _, tok := range strings.Split(strings.TrimPrefix(ptr, "/"), "/") {
		m, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("%s: %q is not inside an object", ptr, tok)
		}
		if cur, ok = m[tok]; !ok {
			t.Fatalf("%s: no %q in the schema", ptr, tok)
		}
	}
	m, ok := cur.(map[string]any)
	if !ok {
		t.Fatalf("%s is not an object", ptr)
	}
	if ref, ok := m["$ref"].(string); ok {
		return schemaNode(t, doc, strings.TrimPrefix(ref, "#"))
	}
	return m
}

// stringConstraintLocs lists the pointer of every schema node carrying a string constraint, outside the examples.
func stringConstraintLocs(v any, ptr string, out map[string]bool) {
	switch n := v.(type) {
	case map[string]any:
		for _, k := range []string{"pattern", "maxLength", "minLength", "format"} {
			if _, ok := n[k]; ok {
				out[ptr] = true
			}
		}
		for k, c := range n {
			if k == "examples" || k == "x-invalid-examples" {
				continue
			}
			stringConstraintLocs(c, ptr+"/"+k, out)
		}
	case []any:
		for i, c := range n {
			stringConstraintLocs(c, ptr+"/"+strconv.Itoa(i), out)
		}
	}
}

// The limit table of the library is the schema's: every entry equals the constraint at its location in
// contracts/test-manifest.schema.yaml, and every string constraint of the schema is an entry. The library reads no
// file at run time; this test keeps the two from drifting.
func TestManifestLimits_TableMatchesSchema(t *testing.T) {
	doc := readYAMLMap(t, filepath.Join(repoRoot(t), "contracts", "test-manifest.schema.yaml"))
	covered := map[string]bool{}
	check := func(name, loc string, l schemaString) {
		n := schemaNode(t, doc, loc)
		covered[loc] = true
		if p, _ := n["pattern"].(string); p != l.pattern {
			t.Errorf("%s (%s): pattern %q, the schema has %q", name, loc, l.pattern, p)
		}
		ml, _ := n["maxLength"].(int)
		if ml != l.maxLength {
			t.Errorf("%s (%s): maxLength %d, the schema has %d", name, loc, l.maxLength, ml)
		}
		if f, _ := n["format"].(string); f != l.format {
			t.Errorf("%s (%s): format %q, the schema has %q", name, loc, l.format, f)
		}
		if _, ok := n["minLength"]; ok {
			t.Errorf("%s (%s): the schema has a minLength the table lacks", name, loc)
		}
		if n["type"] != "string" {
			t.Errorf("%s (%s): the schema type is %v, not string", name, loc, n["type"])
		}
	}
	for _, name := range sortedLimitNames() {
		l := manifestLimits[name]
		check(name, l.loc, l)
		if name == limTestPathGlob {
			for _, loc := range testPathLimitLocs {
				check(name, loc, l)
			}
		}
	}
	locs := map[string]bool{}
	stringConstraintLocs(doc, "", locs)
	var missing []string
	for loc := range locs {
		if !covered[loc] {
			missing = append(missing, loc)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("string constraints of the schema missing from manifestLimits: %v", missing)
	}
	if _, err := compiledLimits(); err != nil {
		t.Fatalf("the limit table does not compile: %v", err)
	}
}
