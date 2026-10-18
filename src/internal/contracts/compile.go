package contracts

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ParseYAML decodes one YAML document into JSON-compatible values; duplicate keys and extra documents are
// errors.
func ParseYAML(b []byte) (any, error) { return parseYAML(b) }

// CompileSchemas compiles every contracts/*.schema.yaml under dir (the contracts directory) with the same
// dialect, format and ECMA-262 regexp settings that sc-contracts uses, and returns the compiled schema of each
// file keyed by its stem (for example "harness" for harness.schema.yaml).
func CompileSchemas(dir string) (map[string]*jsonschema.Schema, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.schema.yaml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	c.UseRegexpEngine(compileECMA)
	ids := map[string]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		doc, err := parseYAML(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		m, _ := doc.(map[string]any)
		id := str(m, "$id")
		if id == "" {
			return nil, fmt.Errorf("%s: schema has no $id", f)
		}
		if err := c.AddResource(id, doc); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		ids[strings.TrimSuffix(filepath.Base(f), ".schema.yaml")] = id
	}
	out := make(map[string]*jsonschema.Schema, len(ids))
	for stem, id := range ids {
		s, err := c.Compile(id)
		if err != nil {
			return nil, fmt.Errorf("%s.schema.yaml: %w", stem, err)
		}
		out[stem] = s
	}
	return out, nil
}
