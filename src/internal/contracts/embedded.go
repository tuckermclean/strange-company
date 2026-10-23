package contracts

import (
	"fmt"
	"sort"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// CompileSchemaBytes compiles in-memory schema documents (keyed by file name, for example
// "worker-result.schema.yaml") with the same settings as CompileSchemas and returns the compiled schemas keyed
// by stem. sc-worker uses it for the schema copies it embeds (plan 27 section 14.12).
func CompileSchemaBytes(docs map[string][]byte) (map[string]*jsonschema.Schema, error) {
	names := make([]string, 0, len(docs))
	for n := range docs {
		names = append(names, n)
	}
	sort.Strings(names)
	contents := make([][]byte, len(names))
	for i, n := range names {
		contents[i] = docs[n]
	}
	return compileSchemas(names, contents)
}

// CompileSchemaBytesRef compiles the in-memory schema documents like CompileSchemaBytes and returns the compiled
// schema that the absolute reference ref denotes, for example
// "https://strange.company/contracts/worker-result/v1alpha1#/$defs/HarnessClaim". sc-worker validates the
// harness claim with it.
func CompileSchemaBytesRef(docs map[string][]byte, ref string) (*jsonschema.Schema, error) {
	names := make([]string, 0, len(docs))
	for n := range docs {
		names = append(names, n)
	}
	sort.Strings(names)
	c := newCompiler()
	for _, n := range names {
		doc, err := parseYAML(docs[n])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		m, _ := doc.(map[string]any)
		id := str(m, "$id")
		if id == "" {
			return nil, fmt.Errorf("%s: schema has no $id", n)
		}
		if err := c.AddResource(id, doc); err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
	}
	return c.Compile(ref)
}
