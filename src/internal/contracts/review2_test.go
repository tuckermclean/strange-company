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

const kindSchemaTail = `examples:
  - {name: a}
`

// 01-AC-24 / plan 52 D6: kind resolution must be deterministic; ambiguity is a failure.
func TestReview2_KindAmbiguousWithinFile_01AC24(t *testing.T) {
	f := "contracts/fx-kinddup.schema.yaml"
	body := strings.Replace(schemaHead, "ID", "fx-kinddup", 1) + kindSchemaTail + `$defs:
  A:
    type: object
    properties:
      kind: {const: FxDupKind}
  B:
    type: object
    properties:
      kind: {const: FxDupKind}
`
	expectFail(t, map[string]string{f: body}, "CONTRACT_KIND_AMBIGUOUS", "fx-kinddup.schema.yaml")
}

func TestReview2_KindAmbiguousAcrossFiles_01AC24(t *testing.T) {
	mk := func(id string) string {
		return strings.Replace(schemaHead, "ID", id, 1) + `  kind: {const: FxCrossKind}
` + kindSchemaTail
	}
	expectFail(t, map[string]string{
		"contracts/fx-kinda.schema.yaml": mk("fx-kinda"),
		"contracts/fx-kindb.schema.yaml": mk("fx-kindb"),
	}, "CONTRACT_KIND_AMBIGUOUS", "fx-kind")
}

// 8.3: a wrongly shaped container is a failure, not silently skipped.
func TestReview2_WrongShapedContainers_01AC21(t *testing.T) {
	cases := map[string]string{
		"invalid-as-mapping": "x-invalid-examples:\n  reason: nope\n",
		"defs-as-list":       "x-defs-examples:\n  - {name: a}\n",
		"defs-entry-scalar":  "x-defs-examples:\n  T: 5\n",
	}
	for name, extra := range cases {
		extra := extra
		t.Run(name, func(t *testing.T) {
			f := "contracts/fx-shape.schema.yaml"
			body := strings.Replace(schemaHead, "ID", "fx-shape", 1) + kindSchemaTail + extra
			expectFail(t, map[string]string{f: body}, "CONTRACT_SCHEMA_INVALID", "fx-shape.schema.yaml")
		})
	}
}

func openapiWith(schemas, extra string) string {
	return `openapi: 3.1.0
info:
  title: Fixture
  version: v1alpha1
` + extra + `paths:
  /v1/things:
    get:
      operationId: listThings
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/T"
components:
  schemas:
    T:
      type: object
      properties:
` + schemas
}

// 8.3: every schema example validates, even for properties named like keywords.
func TestReview2_OpenAPIKeywordNamedProperties_01AC21(t *testing.T) {
	for _, name := range []string{"default", "examples", "enum"} {
		name := name
		t.Run(name, func(t *testing.T) {
			f := "contracts/fx-kwprop.openapi.yaml"
			body := openapiWith("        "+name+":\n          type: integer\n          examples: [\"x\"]\n", "")
			expectFail(t, map[string]string{f: body}, "CONTRACT_OPENAPI_EXAMPLE_INVALID", "fx-kwprop.openapi.yaml")
		})
	}
}

// An extension object is not a schema: its examples/type members are not checked.
func TestReview2_OpenAPIExtensionNotASchema_01AC21(t *testing.T) {
	dir := treeCopy(t)
	body := openapiWith("        n: {type: integer, examples: [1]}\n", "x-notes:\n  examples: [1, 2]\n  type: 7\n")
	writeIn(t, dir, "contracts/fx-ext.openapi.yaml", body)
	code, out := makeContracts(t, dir)
	if code != 0 {
		t.Errorf("`make contracts` exited %d, want 0:\n%s", code, out)
	}
	for _, r := range readRows(t, dir) {
		if strings.HasSuffix(r.File, "fx-ext.openapi.yaml") && r.Check == "CONTRACT_OPENAPI_EXAMPLE_INVALID" && r.Result == "fail" {
			t.Errorf("unexpected row: %+v", r)
		}
	}
	if hasLine(out, "CONTRACT_OPENAPI_EXAMPLE_INVALID", "fx-ext.openapi.yaml") {
		t.Errorf("x-notes treated as a schema:\n%s", out)
	}
}
