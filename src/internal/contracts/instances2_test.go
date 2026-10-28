package contracts_test

import (
	"strings"
	"testing"
)

// 01-AC-23 (plan 01 8.3): a kind-selected row accepts only the schema files its table row names; a file whose
// kind belongs to another schema fails CONTRACT_KIND_UNKNOWN naming the file.
func TestInstances2_CrossSchemaKindRejected_01AC23(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	quar, _ := schemaExample(t, root, "test-quarantine.schema.yaml", "TestQuarantine")
	lock, _ := schemaExample(t, root, "version-locks.schema.yaml", "PlatformVersionLock")
	cases := []struct {
		path string
		doc  map[string]any
	}{
		{"versions/cross.yaml", quar},
		{"toolbox/tools.lock.yaml", quar},
		{"toolbox/image.lock", quar},
		{"infra/lab/lab.yaml", quar},
		{"infra/profiles/p.yaml", lock},
	}
	for _, c := range cases {
		c := c
		t.Run(strings.ReplaceAll(c.path, "/", "_"), func(t *testing.T) {
			t.Parallel()
			dir := treeCopy(t)
			putYAML(t, dir, c.path, c.doc)
			code, out := makeContracts(t, dir)
			if code == 0 {
				t.Errorf("`make contracts` exited 0:\n%s", out)
			}
			if !hasLine(out, "CONTRACT_KIND_UNKNOWN", c.path) {
				t.Errorf("no CONTRACT_KIND_UNKNOWN line naming %s:\n%s", c.path, out)
			}
		})
	}
}

// 01-AC-23: a fixed-schema row validates the whole document, so a list or scalar fails with a type mismatch.
func TestInstances2_FixedRowNonObjectTypeMismatch_01AC23(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{"list": "- a\n- b\n", "scalar": "just text\n"} {
		body := body
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := "infra/catalog/prices/list.yaml"
			dir := treeCopy(t)
			writeIn(t, dir, p, body)
			code, out := makeContracts(t, dir)
			if code == 0 {
				t.Errorf("`make contracts` exited 0:\n%s", out)
			}
			if !hasLine(out, p, "type") {
				t.Errorf("no output line naming %s and a type mismatch:\n%s", p, out)
			}
		})
	}
}

func oasComponentResponse(resp string) string {
	return `openapi: 3.1.0
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
components:
  schemas:
    T:
      type: object
  responses:
    x-err:
` + resp
}

// 01-AC-21 (plan 01 8.3): only an operation's responses skip x- keys; a components.responses entry named
// x-err is a Response Object whose schema examples are validated.
func TestInstances2_ComponentResponseXNameExampleInvalid_01AC21(t *testing.T) {
	t.Parallel()
	body := oasComponentResponse(`      description: error
      content:
        application/json:
          schema:
            type: integer
            examples: ["text"]
`)
	expectFail(t, map[string]string{"contracts/fx-xresp.openapi.yaml": body},
		"CONTRACT_OPENAPI_EXAMPLE_INVALID", "fx-xresp.openapi.yaml")
}

// 01-AC-21: and its $refs are resolved.
func TestInstances2_ComponentResponseXNameRefUnresolved_01AC21(t *testing.T) {
	t.Parallel()
	body := oasComponentResponse(`      description: error
      content:
        application/json:
          schema:
            $ref: "#/components/schemas/Nope"
`)
	expectFail(t, map[string]string{"contracts/fx-xresp2.openapi.yaml": body},
		"CONTRACT_OPENAPI_REF_UNRESOLVED", "fx-xresp2.openapi.yaml")
}
