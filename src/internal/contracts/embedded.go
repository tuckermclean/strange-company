package contracts

import (
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
