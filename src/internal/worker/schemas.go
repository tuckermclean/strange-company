// Package worker is the library behind sc-worker (plan 27).
package worker

import "embed"

// The embedded files are byte-identical copies of contracts/<name>.schema.yaml. TestEmbeddedSchemas_MatchContracts_27AC14
// compares their checksums; refresh them with `cp contracts/<name>.schema.yaml src/internal/worker/schemas/`.
//
//go:embed schemas/*.schema.yaml
var schemaFS embed.FS

// EmbeddedSchema returns the embedded contract schema <name>.schema.yaml, or nil when name is unknown.
func EmbeddedSchema(name string) []byte {
	b, err := schemaFS.ReadFile("schemas/" + name + ".schema.yaml")
	if err != nil {
		return nil
	}
	return b
}
