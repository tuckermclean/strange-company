package contracts_test

import (
	"strings"
	"testing"
)

// 01-AC-23: instance validation covers every glob in the plan 01 8.3 table. Decisions where the plan is
// silent: `**` also matches zero directories (policy/ci.yaml is a policy/**/*.yaml member);
// policy/** and context/** kinds are selected by their kind const (the schema's top level or $defs entry).

var newGlobRows = []instRow{
	{"infra/profiles/lab.yaml", "substrate.schema.yaml", "BootstrapSubstrate"},
	{"infra/profiles/cluster.yaml", "cluster-profile.schema.yaml", "ClusterProfile"},
	{"infra/catalog/prices/hetzner.yaml", "provider.schema.yaml", "InfraPriceCatalog"},
	{"infra/tofu/guard.yaml", "provider.schema.yaml", "TofuPlanGuard"},
	{"infra/tofu/modules/lab/guard-plan.yaml", "provider.schema.yaml", "TofuPlanGuard"},
	{"policy/budget.yaml", "budget-policy.schema.yaml", "BudgetPolicy"},
	{"policy/sub/dir/egress.yaml", "egress-policy.schema.yaml", "EgressPolicy"},
	{"policy/verification.yaml", "verification.schema.yaml", "VerificationPolicy"},
	{"context/company/ctx.yaml", "context.schema.yaml", "RepositoryContext"},
	{"context/projects/a/b/ctx.yaml", "context.schema.yaml", "RepositoryContext"},
	{"gitops/clusters/lab/bootstrap-config.yaml", "bootstrap-config.schema.yaml", "BootstrapConfig"},
}

// 01-AC-23: valid instances at the version-lock globs (S1) pass naming file and schema.
func TestInstances_LockGlobsPass_01AC23(t *testing.T) { checkPlaced(t, lockRows) }

// 01-AC-23: a valid instance at every other table glob passes, naming file and schema.
func TestInstances_NewGlobsPass_01AC23(t *testing.T) { checkPlaced(t, newGlobRows) }

// 01-AC-23: a violating instance at the same globs fails, naming the file.
func TestInstances_NewGlobsViolationFails_01AC23(t *testing.T) { checkViolating(t, newGlobRows) }

// 01-AC-23: the real governed files each get a pass row and a file+schema output line.
func TestInstances_RealFilesPass_01AC23(t *testing.T) {
	real := []instRow{
		{".strange-company/repo.yaml", "context.schema.yaml", ""},
		{".strange-company/policy.yaml", "repo-policy.schema.yaml", ""},
		{".strange-company/validation.yaml", "repo-validation.schema.yaml", ""},
		{"policy/ci.yaml", "ci-policy.schema.yaml", ""},
		{"policy/test-quarantine.yaml", "test-quarantine.schema.yaml", ""},
		{"contracts/alert-catalog.yaml", "alert-catalog.schema.yaml", ""},
		{"contracts/secret-classes.yaml", "secret-classes.schema.yaml", ""},
		{"contracts/state-machine.yaml", "state-machine.schema.yaml", ""},
		{"toolbox/tools.lock.yaml", "version-locks.schema.yaml", ""},
	}
	dir := treeCopy(t)
	code, out := makeContracts(t, dir)
	if code != 0 {
		t.Errorf("`make contracts` exited %d, want 0:\n%s", code, out)
	}
	rows := readRows(t, dir)
	for _, r := range real {
		if !passRow(rows, r.path) {
			t.Errorf("%s: no pass row in report.json", r.path)
		}
		if failRow(rows, r.path) {
			t.Errorf("%s: fail row in report.json", r.path)
		}
		if !hasLine(out, r.path, r.schema) {
			t.Errorf("%s: no output line naming the file and %s", r.path, r.schema)
		}
	}
}

// 01-AC-23: the register files validate against their companion schema; breaking one fails, naming it.
func TestInstances_RegisterViolationFails_01AC23(t *testing.T) {
	regs := []struct{ path, schema, kind string }{
		{"contracts/alert-catalog.yaml", "alert-catalog.schema.yaml", "AlertCatalog"},
		{"contracts/secret-classes.yaml", "secret-classes.schema.yaml", "SecretClassRegister"},
		{"contracts/state-machine.yaml", "state-machine.schema.yaml", "TaskStateMachine"},
	}
	dir := treeCopy(t)
	root := repoRoot(t)
	for _, r := range regs {
		doc, _ := readYAMLAny(t, root+"/"+r.path).(map[string]any)
		_, node := schemaExample(t, root, r.schema, r.kind)
		violate(doc, node)
		putYAML(t, dir, r.path, doc)
	}
	code, out := makeContracts(t, dir)
	if code == 0 {
		t.Errorf("`make contracts` exited 0 with broken registers:\n%s", out)
	}
	for _, r := range regs {
		if !hasLine(out, r.path, "FAIL") {
			t.Errorf("%s: no FAIL output line naming the file", r.path)
		}
	}
}

// 01-AC-23: a profile file with `memory: 8GB` fails, naming substrate.schema.yaml.
func TestInstances_ProfileMemory8GBFails_01AC23(t *testing.T) {
	dir := treeCopy(t)
	doc, _ := schemaExample(t, repoRoot(t), "substrate.schema.yaml", "BootstrapSubstrate")
	spec := doc["spec"].(map[string]any)
	nodes := spec["nodes"].(map[string]any)
	nodes["minimumSupportedPerNode"].(map[string]any)["memory"] = "8GB"
	putYAML(t, dir, "infra/profiles/small.yaml", doc)
	code, out := makeContracts(t, dir)
	if code == 0 {
		t.Errorf("`make contracts` exited 0 with memory: 8GB:\n%s", out)
	}
	if !hasLine(out, "infra/profiles/small.yaml", "FAIL", "substrate.schema.yaml") {
		t.Errorf("no FAIL line naming infra/profiles/small.yaml and substrate.schema.yaml:\n%s", out)
	}
	if !failRow(readRows(t, dir), "infra/profiles/small.yaml") {
		t.Errorf("no fail row for infra/profiles/small.yaml")
	}
}

// 01-AC-23 (decision: missing kind is a kind with no schema, CONTRACT_KIND_UNKNOWN): a file without kind
// under each kind-selected glob fails.
func TestInstances_NoKindUnderKindGlobs_01AC23(t *testing.T) {
	body := "apiVersion: strange.company/v1alpha1\nmetadata: {name: x}\nspec: {}\n"
	for _, p := range []string{"infra/profiles/nokind.yaml", "policy/deep/nokind.yaml", "context/nokind.yaml"} {
		p := p
		t.Run(strings.ReplaceAll(p, "/", "_"), func(t *testing.T) {
			expectFail(t, map[string]string{p: body}, "CONTRACT_KIND_UNKNOWN", p)
		})
	}
}

// 01-AC-23 (plan: files without kind are mapped by the fixed globs): a kind-less file at a fixed-schema glob
// is validated against that schema and fails, naming file and schema.
func TestInstances_NoKindFixedGlobsFail_01AC23(t *testing.T) {
	fixed := []instRow{
		{"infra/catalog/prices/nokind.yaml", "provider.schema.yaml", "InfraPriceCatalog"},
		{"infra/tofu/x/guard-nokind.yaml", "provider.schema.yaml", "TofuPlanGuard"},
		{"gitops/clusters/lab/bootstrap-config.yaml", "bootstrap-config.schema.yaml", "BootstrapConfig"},
	}
	dir := treeCopy(t)
	for _, r := range fixed {
		doc, _ := schemaExample(t, repoRoot(t), r.schema, r.kind)
		delete(doc, "kind")
		putYAML(t, dir, r.path, doc)
	}
	code, out := makeContracts(t, dir)
	if code == 0 {
		t.Errorf("`make contracts` exited 0:\n%s", out)
	}
	for _, r := range fixed {
		if !hasLine(out, r.path, "FAIL", r.schema) {
			t.Errorf("%s: no FAIL line naming the file and %s:\n%s", r.path, r.schema, out)
		}
	}
}

func oasResponses(responses string) string {
	return `openapi: 3.1.0
info:
  title: Fixture
  version: v1alpha1
paths:
  /v1/tasks:
    get:
      operationId: listTasks
      responses:
` + responses + `components:
  schemas:
    T:
      type: object
`
}

// 01-AC-21 (S1 follow-up a): x- keys directly under responses are extensions, not responses.
func TestInstances_OpenAPIResponsesExtensionIgnored_01AC21(t *testing.T) {
	dir := treeCopy(t)
	writeIn(t, dir, "contracts/fx-respext.openapi.yaml", oasResponses(`        "200":
          description: ok
        x-sc-note:
          $ref: "#/not/a/real/ref"
        x-sc-sample:
          type: integer
          examples: ["text"]
`))
	code, out := makeContracts(t, dir)
	if code != 0 {
		t.Errorf("`make contracts` exited %d, want 0:\n%s", code, out)
	}
	for _, r := range readRows(t, dir) {
		if strings.HasSuffix(r.File, "fx-respext.openapi.yaml") && r.Result == "fail" {
			t.Errorf("unexpected fail row: %+v", r)
		}
	}
}

// 01-AC-21 (S1 follow-up b): the pointer in a CONTRACT_OPENAPI_REF_UNRESOLVED line is JSON-pointer escaped.
func TestInstances_OpenAPIRefPointerEscaped_01AC21(t *testing.T) {
	_, out := expectFail(t, map[string]string{"contracts/fx-ptr.openapi.yaml": oasResponses(`        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/Nope"
`)}, "CONTRACT_OPENAPI_REF_UNRESOLVED", "fx-ptr.openapi.yaml",
		"/paths/~1v1~1tasks/get/responses/200/content/application~1json/schema")
	if strings.Contains(out, "/paths//v1/tasks") {
		t.Errorf("unescaped pointer in output:\n%s", out)
	}
}

// 01-AC-21 (S1 follow-up c): an empty x-defs-examples list declares no example and is a wrong shape.
func TestInstances_EmptyDefsExamplesList_01AC21(t *testing.T) {
	body := strings.Replace(schemaHead, "ID", "fx-emptydefs", 1) + kindSchemaTail + `$defs:
  T: {type: object}
x-defs-examples:
  T: []
`
	expectFail(t, map[string]string{"contracts/fx-emptydefs.schema.yaml": body}, "CONTRACT_SCHEMA_INVALID", "fx-emptydefs.schema.yaml")
}
