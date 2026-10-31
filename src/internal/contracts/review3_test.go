package contracts_test

import "testing"

func oasWithDefaultResponse(resp string) string {
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
        default:
` + resp + `components:
  schemas:
    T:
      type: object
`
}

// 01-AC-21 (plan 01 8.3): responses.default is a Response Object; its $ref must resolve.
func TestReview3_OpenAPIDefaultResponseRefUnresolved_01AC21(t *testing.T) {
	t.Parallel()
	body := oasWithDefaultResponse("          $ref: \"#/components/responses/NoSuchError\"\n")
	expectFail(t, map[string]string{"contracts/fx-defresp.openapi.yaml": body},
		"CONTRACT_OPENAPI_REF_UNRESOLVED", "fx-defresp.openapi.yaml")
}

// 01-AC-21 (plan 01 8.3): schema examples under responses.default are validated.
func TestReview3_OpenAPIDefaultResponseExampleInvalid_01AC21(t *testing.T) {
	t.Parallel()
	body := oasWithDefaultResponse(`          description: error
          content:
            application/json:
              schema:
                type: integer
                examples: ["not-an-int"]
`)
	expectFail(t, map[string]string{"contracts/fx-defresp2.openapi.yaml": body},
		"CONTRACT_OPENAPI_EXAMPLE_INVALID", "fx-defresp2.openapi.yaml")
}

// 01-AC-21 regression guard: a `default` DATA value is not walked for $ref.
func TestReview3_OpenAPIDefaultDataNotWalked_01AC21(t *testing.T) {
	t.Parallel()
	body := openapiWith("        p:\n          type: object\n          default: {$ref: \"#/nope\"}\n", "")
	dir := treeCopy(t)
	writeIn(t, dir, "contracts/fx-defdata.openapi.yaml", body)
	_, out := makeContracts(t, dir)
	if hasLine(out, "CONTRACT_OPENAPI_REF_UNRESOLVED", "fx-defdata.openapi.yaml") {
		t.Errorf("default data value was walked for $ref:\n%s", out)
	}
}
