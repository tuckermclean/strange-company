package contracts

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ParseYAML decodes one YAML document into JSON-compatible values; duplicate keys and extra documents are
// errors.
func ParseYAML(b []byte) (any, error) { return parseYAML(b) }

type compiledSet struct {
	sum     [sha256.Size]byte
	schemas map[string]*jsonschema.Schema
}

var (
	compiledMu    sync.Mutex
	compiledCache = map[string]compiledSet{} // contracts dir -> last compiled set
)

// CompileSchemas compiles every contracts/*.schema.yaml under dir (the contracts directory) with the same
// dialect, format and ECMA-262 regexp settings that sc-contracts uses, and returns the compiled schema of each
// file keyed by its stem (for example "harness" for harness.schema.yaml). The result is cached per directory
// and reused while the bytes of the schema files are unchanged; the returned map must not be modified.
func CompileSchemas(dir string) (map[string]*jsonschema.Schema, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.schema.yaml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	h := sha256.New()
	contents := make([][]byte, len(files))
	for i, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		contents[i] = b
		fmt.Fprintf(h, "%s\x00%d\x00", f, len(b))
		h.Write(b)
	}
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	abs, aerr := filepath.Abs(dir)
	if aerr != nil {
		abs = dir
	}
	compiledMu.Lock()
	defer compiledMu.Unlock()
	if c, ok := compiledCache[abs]; ok && c.sum == sum {
		return c.schemas, nil
	}
	out, err := compileSchemas(files, contents)
	if err != nil {
		return nil, err
	}
	compiledCache[abs] = compiledSet{sum: sum, schemas: out}
	return out, nil
}

func compileSchemas(files []string, contents [][]byte) (map[string]*jsonschema.Schema, error) {
	c := newCompiler()
	ids := map[string]string{}
	for i, f := range files {
		doc, err := parseYAML(contents[i])
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
