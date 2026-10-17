package contracts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func countCheck(rows []row, file, check, result string) int {
	n := 0
	for _, r := range rows {
		if (r.File == file || strings.HasSuffix(r.File, "/"+file)) && r.Check == check && r.Result == result {
			n++
		}
	}
	return n
}

// 01-AC-20: pass rows are accounted per check ID, not only in total.
func TestMakeContracts_PerCheckAccounting_01AC20(t *testing.T) {
	dir := treeCopy(t)
	if code, out := makeContracts(t, dir); code != 0 {
		t.Fatalf("`make contracts` exited %d, want 0:\n%s", code, out)
	}
	rows := readRows(t, dir)
	schemas, _ := filepath.Glob(filepath.Join(dir, "contracts", "*.schema.yaml"))
	if len(schemas) == 0 {
		t.Fatal("no schemas found")
	}
	for _, s := range schemas {
		b, err := os.ReadFile(s)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := yaml.Unmarshal(b, &doc); err != nil {
			t.Fatal(err)
		}
		defs := 0
		if m, ok := doc["x-defs-examples"].(map[string]any); ok {
			for _, v := range m {
				if l, ok := v.([]any); ok {
					defs += len(l)
				}
			}
		}
		base := "contracts/" + filepath.Base(s)
		wants := map[string]int{}
		wants["CONTRACT_EXAMPLE_INVALID"] = seq(doc, "examples")
		wants["CONTRACT_INVALID_EXAMPLE_PASSES"] = seq(doc, "x-invalid-examples")
		wants["CONTRACT_DEFS_EXAMPLE_INVALID"] = defs
		wants["CONTRACT_SCHEMA_INVALID"] = 1
		for check, want := range wants {
			if got := countCheck(rows, base, check, "pass"); got != want {
				t.Errorf("%s: %d pass rows for %s, want %d", base, got, check, want)
			}
		}
	}
}
