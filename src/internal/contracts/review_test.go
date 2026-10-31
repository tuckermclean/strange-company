package contracts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const schemaHead = `$schema: https://json-schema.org/draft/2020-12/schema
$id: https://strange.company/contracts/ID/v1alpha1
title: Fixture
description: Round-2 review fixture for sc-contracts.
type: object
required: [name]
additionalProperties: false
properties:
  name: {type: string, minLength: 1}
`

func writeIn(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hasLine(out string, subs ...string) bool {
	for _, l := range strings.Split(out, "\n") {
		ok := true
		for _, s := range subs {
			if !strings.Contains(l, s) {
				ok = false
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func expectFail(t *testing.T, files map[string]string, subs ...string) (string, string) {
	t.Helper()
	dir := treeCopy(t)
	for rel, c := range files {
		writeIn(t, dir, rel, c)
	}
	code, out := makeContracts(t, dir)
	if code == 0 {
		t.Errorf("`make contracts` exited 0, want non-zero:\n%s", out)
	}
	if !hasLine(out, subs...) {
		t.Errorf("no output line containing all of %q:\n%s", subs, out)
	}
	return dir, out
}

// 01-AC-21: failing example lines carry the example index.
func TestReview_ExampleIndexInOutput_01AC21(t *testing.T) {
	t.Parallel()
	f := "contracts/fx-idx.schema.yaml"
	body := strings.Replace(schemaHead, "ID", "fx-idx", 1) + `examples:
  - {name: a}
  - {name: b}
  - {name: ""}
x-invalid-examples:
  - reason: empty
    value: {}
`
	expectFail(t, map[string]string{f: body}, "CONTRACT_EXAMPLE_INVALID", "fx-idx.schema.yaml", "examples[2]")
}

func TestReview_DefsExampleIndexInOutput_01AC21(t *testing.T) {
	t.Parallel()
	f := "contracts/fx-defidx.schema.yaml"
	body := strings.Replace(schemaHead, "ID", "fx-defidx", 1) + `$defs:
  Thing: {type: string, minLength: 1}
examples:
  - {name: a}
x-invalid-examples:
  - reason: empty
    value: {}
x-defs-examples:
  Thing:
    - ""
`
	expectFail(t, map[string]string{f: body}, "CONTRACT_DEFS_EXAMPLE_INVALID", "fx-defidx.schema.yaml", "x-defs-examples.Thing[0]")
}

// 01-AC-20: an x-invalid-examples entry with a reason but no value fails and gets no pass row.
func TestReview_InvalidExampleWithoutValue_01AC20(t *testing.T) {
	t.Parallel()
	f := "contracts/fx-novalue.schema.yaml"
	body := strings.Replace(schemaHead, "ID", "fx-novalue", 1) + `examples:
  - {name: a}
x-invalid-examples:
  - reason: has no value key
`
	dir, _ := expectFail(t, map[string]string{f: body}, "CONTRACT_", "fx-novalue.schema.yaml")
	rows := readRows(t, dir)
	for _, r := range rows {
		if strings.HasSuffix(r.File, "fx-novalue.schema.yaml") && r.Result == "pass" && strings.Contains(r.Check, "INVALID_EXAMPLE") {
			t.Errorf("pass row for value-less invalid example: %+v", r)
		}
	}
}

// 8.3: every schema example validates, including inline OpenAPI response schemas and nested properties.
func TestReview_OpenAPIInlineExampleInvalid_01AC21(t *testing.T) {
	t.Parallel()
	f := "contracts/fx-inline.openapi.yaml"
	body := `openapi: 3.1.0
info:
  title: Fixture
  version: v1alpha1
paths:
  /v1/things:
    get:
      operationId: listThings
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  n: {type: integer}
                examples:
                  - {n: not-a-number}
components:
  schemas:
    Thing:
      type: object
`
	expectFail(t, map[string]string{f: body}, "CONTRACT_OPENAPI_EXAMPLE_INVALID", "fx-inline.openapi.yaml")
}

func TestReview_OpenAPINestedPropertyExampleInvalid_01AC21(t *testing.T) {
	t.Parallel()
	f := "contracts/fx-nested.openapi.yaml"
	body := `openapi: 3.1.0
info:
  title: Fixture
  version: v1alpha1
paths:
  /v1/things:
    get:
      operationId: listThings
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/Thing"
components:
  schemas:
    Thing:
      type: object
      properties:
        n:
          type: integer
          examples:
            - not-a-number
`
	expectFail(t, map[string]string{f: body}, "CONTRACT_OPENAPI_EXAMPLE_INVALID", "fx-nested.openapi.yaml")
}

// 8.3: parse YAML; a second document is rejected.
func TestReview_SecondYAMLDocument_01AC21(t *testing.T) {
	t.Parallel()
	f := "contracts/fx-twodocs.schema.yaml"
	body := strings.Replace(schemaHead, "ID", "fx-twodocs", 1) + `examples:
  - {name: a}
x-invalid-examples:
  - reason: empty
    value: {}
---
extra: document
`
	expectFail(t, map[string]string{f: body}, "CONTRACT_YAML_INVALID", "fx-twodocs.schema.yaml")
}

// 01-AC-24 (6.3 lock files): versions/*.yaml and toolbox/image.lock validate against their kind.
func TestReview_VersionsLockFilePasses_01AC24(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "contracts", "version-locks.schema.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Examples []any `yaml:"examples"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil || len(doc.Examples) == 0 {
		t.Fatalf("cannot read examples: %v", err)
	}
	inst, err := yaml.Marshal(doc.Examples[0])
	if err != nil {
		t.Fatal(err)
	}
	dir := treeCopy(t)
	writeIn(t, dir, "versions/x.yaml", string(inst))
	if code, out := makeContracts(t, dir); code != 0 {
		t.Fatalf("`make contracts` exited %d, want 0:\n%s", code, out)
	}
	if countFile(readRows(t, dir), "versions/x.yaml", "pass") < 1 {
		t.Errorf("no pass row for versions/x.yaml")
	}
}

func TestReview_ToolboxImageLockViolation_01AC24(t *testing.T) {
	t.Parallel()
	body := `apiVersion: strange.company/v1alpha1
kind: ToolboxImageLock
inputsId: ''
toolboxImage: ghcr.io/example-org/strange-company-toolbox
toolboxDigest: sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee
ciImage: ghcr.io/example-org/strange-company-ci
ciDigest: ''
`
	expectFail(t, map[string]string{"toolbox/image.lock": body}, "toolbox/image.lock")
}
