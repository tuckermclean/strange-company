package guard_test

import (
	"strings"
	"testing"

	"strange.company/sc/internal/provider/guard"
)

// CD-72 hardening of the S2 guard (plan 03 section 14.4 rules 3, 6, 7, 9; contracts TofuPlanGuard).

// 03-AC-3: a resource-change mode other than managed/data fails closed. The code is GUARD_UNEXPECTED_RESOURCE:
// an entry that is neither a managed resource nor a data read is not on the allowlist (rule 3 / rule 7 allows
// only mode = "data" reads besides managed changes). A data entry with any action but read/no-op fails
// GUARD_ACTION_FORBIDDEN (rule 7: data sources cannot create resources).
func TestGuardEvaluate_ResourceModeFailsClosed_03AC3(t *testing.T) {
	cases := []struct{ fixture, code string }{
		{"mode-missing.json", "GUARD_UNEXPECTED_RESOURCE"},
		{"mode-unknown.json", "GUARD_UNEXPECTED_RESOURCE"},
		{"data-create.json", "GUARD_ACTION_FORBIDDEN"},
		{"data-delete.json", "GUARD_ACTION_FORBIDDEN"},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			v := load(t, "guard.yaml").Evaluate(read(t, c.fixture), expect(false))
			if v.Pass || v.Code != c.code || v.Address == "" {
				t.Fatalf("want %s with an address, got %+v", c.code, v)
			}
		})
	}
}

// 03-AC-3: mode guard also applies in destroy mode.
func TestGuardEvaluate_ResourceModeFailsClosedInDestroy_03AC3(t *testing.T) {
	for _, f := range []string{"mode-missing.json", "mode-unknown.json", "data-create.json"} {
		t.Run(f, func(t *testing.T) {
			if v := load(t, "guard-destroy.yaml").Evaluate(read(t, f), expect(true)); v.Pass {
				t.Fatalf("destroy guard accepted %s: %+v", f, v)
			}
		})
	}
}

const baseGuard = `apiVersion: strange.company/v1alpha1
kind: TofuPlanGuard
metadata:
  name: generic-paid
spec:
  rootModule: infra/tofu/compute/generic-paid
  allowedProviders:
    - registry.opentofu.org/example/paid
  forbidReplace: true
  resources:
    - type: test_server
      paid: true
      allowedActions: [no-op, read, create, update]
      maxCount: expectedNodeCount
      priceProductFrom: plan[0].plan_code
    - type: test_ssh_key
      paid: false
      allowedActions: [no-op, read, create, update]
      maxCount: 1
`

// 03-AC-3: ParseGuard is as strict as contracts/provider.schema.yaml $defs/TofuPlanGuard.
func TestParseGuard_SchemaStrictness_03AC3(t *testing.T) {
	if _, err := guard.ParseGuard([]byte(baseGuard)); err != nil {
		t.Fatalf("control guard must parse: %v", err)
	}
	cases := []struct{ name, from, to string }{
		{"paid missing on test_ssh_key", "      paid: false\n", ""},
		{"paid missing on test_server", "      paid: true\n", ""},
		{"rootModule outside infra/tofu", "rootModule: infra/tofu/compute/generic-paid", "rootModule: src/compute"},
		{"rootModule upper case", "rootModule: infra/tofu/compute/generic-paid", "rootModule: infra/tofu/Compute"},
		{"rootModule bare infra/tofu/", "rootModule: infra/tofu/compute/generic-paid", "rootModule: infra/tofu/"},
		{"provider not fully qualified", "registry.opentofu.org/example/paid", "example/paid"},
		{"provider wrong registry", "registry.opentofu.org/example/paid", "registry.terraform.io/example/paid"},
		{"provider upper case", "registry.opentofu.org/example/paid", "registry.opentofu.org/Example/paid"},
		{"provider with trailing path", "registry.opentofu.org/example/paid", "registry.opentofu.org/example/paid/x"},
		{"type upper case", "type: test_server", "type: Test_Server"},
		{"type one character", "type: test_ssh_key", "type: t"},
		{"type starts with digit", "type: test_ssh_key", "type: 1test_key"},
		{"type with dash", "type: test_ssh_key", "type: test-ssh-key"},
		{"duplicate allowed action", "allowedActions: [no-op, read, create, update]\n      maxCount: expectedNodeCount", "allowedActions: [create, create]\n      maxCount: expectedNodeCount"},
		{"maxCount missing", "      maxCount: 1\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := strings.Replace(baseGuard, c.from, c.to, 1)
			if doc == baseGuard {
				t.Fatalf("mutation %q did not apply", c.name)
			}
			if _, err := guard.ParseGuard([]byte(doc)); err == nil {
				t.Fatalf("ParseGuard accepted a guard the schema rejects:\n%s", doc)
			}
		})
	}
}

// 03-AC-3: Attr.Path supports [?key=="value"] selection in lists (CD-72 e).
func TestGuardEvaluate_SelectorPaths_03AC3(t *testing.T) {
	const sel = `plan[0].configuration[?label=="%s"].value`
	mk := func(label string) string { return strings.Replace(sel, "%s", label, 1) }
	cases := []struct {
		name, fixture string
		region, ssh   guard.Attr
		code          string // empty means pass
	}{
		{"region match not at index 0", "selector-valid.json",
			guard.Attr{Type: "test_server", Path: mk("vps_datacenter"), Want: "REG-1"},
			guard.Attr{Type: "test_server", Path: mk("ssh_key"), Want: "sc-prod-key"}, ""},
		{"region selected value differs", "selector-valid.json",
			guard.Attr{Type: "test_server", Path: mk("vps_datacenter"), Want: "REG-2"},
			guard.Attr{Type: "test_server", Path: "ssh_key_name", Want: "x"}, "GUARD_REGION_MISMATCH"},
		{"ssh selected value differs", "selector-valid.json",
			guard.Attr{Type: "test_server", Path: mk("vps_datacenter"), Want: "REG-1"},
			guard.Attr{Type: "test_server", Path: mk("ssh_key"), Want: "other-key"}, "GUARD_SSH_KEY_MISMATCH"},
		{"region no element matches", "selector-valid.json",
			guard.Attr{Type: "test_server", Path: mk("absent"), Want: "REG-1"},
			guard.Attr{Type: "test_server", Path: "ssh_key_name", Want: "x"}, "GUARD_REGION_MISMATCH"},
		{"ssh no element matches", "selector-valid.json",
			guard.Attr{Type: "test_server", Path: mk("vps_datacenter"), Want: "REG-1"},
			guard.Attr{Type: "test_server", Path: mk("absent"), Want: "sc-prod-key"}, "GUARD_SSH_KEY_MISMATCH"},
		{"selected element value unknown", "selector-unknown.json",
			guard.Attr{Type: "test_server", Path: mk("vps_datacenter"), Want: "REG-1"},
			guard.Attr{Type: "test_server", Path: "ssh_key_name", Want: "x"}, "GUARD_REGION_MISMATCH"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := expect(false)
			e.Region, e.SSHKey = c.region, c.ssh
			v := load(t, "guard.yaml").Evaluate(read(t, c.fixture), e)
			if c.code == "" {
				if !v.Pass {
					t.Fatalf("want pass, got %+v", v)
				}
				return
			}
			if v.Pass || v.Code != c.code {
				t.Fatalf("want %s, got %+v", c.code, v)
			}
		})
	}
}

// 03-AC-3: a malformed selector fails closed with the rule's code, never a pass.
func TestGuardEvaluate_MalformedSelectorFailsClosed_03AC3(t *testing.T) {
	bad := []string{
		`plan[0].configuration[?label=vps_datacenter].value`,
		`plan[0].configuration[?label=="vps_datacenter"].value[?`,
		`plan[0].configuration[?label=="vps_datacenter".value`,
		`plan[0].configuration[?=="vps_datacenter"].value`,
		`plan[0].configuration[?label==""].value`,
		`plan[0].configuration[?label=="vps_datacenter"]`,
		`plan[0].configuration[?label=='vps_datacenter'].value`,
		`plan[0].configuration[?label=="vps_datacenter"]x.value`,
		`plan[0][?label=="vps_datacenter"].value`,
	}
	for _, p := range bad {
		t.Run(p, func(t *testing.T) {
			e := expect(false)
			e.Region = guard.Attr{Type: "test_server", Path: p, Want: "REG-1"}
			v := load(t, "guard.yaml").Evaluate(read(t, "selector-valid.json"), e)
			if v.Pass || v.Code != "GUARD_REGION_MISMATCH" {
				t.Fatalf("want GUARD_REGION_MISMATCH, got %+v", v)
			}
		})
	}
}

// 03-AC-3: rule 6 attribute paths are declared by the provider package. A guard with a paid type and no declared
// Region or SSHKey path must not silently pass; no plan code names this, so the verdict is GUARD_RULE_UNDECLARED.
func TestGuardEvaluate_UndeclaredRule6FailsClosed_03AC3(t *testing.T) {
	ok := guard.Attr{Type: "test_server", Path: "region", Want: "REG-1"}
	okSSH := guard.Attr{Type: "test_server", Path: "ssh_key_name", Want: "sc-prod-key"}
	cases := []struct {
		name        string
		region, ssh guard.Attr
	}{
		{"region zero", guard.Attr{}, okSSH},
		{"ssh zero", ok, guard.Attr{}},
		{"both zero", guard.Attr{}, guard.Attr{}},
		{"region path empty", guard.Attr{Type: "test_server", Want: "REG-1"}, okSSH},
		{"ssh path empty", ok, guard.Attr{Type: "test_server", Want: "sc-prod-key"}},
		{"region type empty", guard.Attr{Path: "region", Want: "REG-1"}, okSSH},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := expect(false)
			e.Region, e.SSHKey = c.region, c.ssh
			v := load(t, "guard.yaml").Evaluate(read(t, "valid-create.json"), e)
			if v.Pass || v.Code != "GUARD_RULE_UNDECLARED" {
				t.Fatalf("want GUARD_RULE_UNDECLARED, got %+v", v)
			}
		})
	}
}

// 03-AC-3 (regression coverage of behavior S2 already has): destroy mode forbids create/update even when the
// guard's allowedActions would allow them, a destroy plan may contain a data read, and the count rule counts
// prior-state instances.
func TestGuardEvaluate_DestroyAndPriorStateCoverage_03AC3(t *testing.T) {
	if v := load(t, "guard.yaml").Evaluate(read(t, "valid-create.json"), expect(true)); v.Pass || v.Code != "GUARD_ACTION_FORBIDDEN" {
		t.Fatalf("destroy mode with a create-permitting guard: %+v", v)
	}
	if v := load(t, "guard-destroy.yaml").Evaluate(read(t, "destroy-with-data-read.json"), expect(true)); !v.Pass {
		t.Fatalf("destroy plan with a data read: %+v", v)
	}
	e := expect(false)
	e.Region, e.SSHKey = guard.Attr{Type: "test_server", Path: "region", Want: "REG-1"}, guard.Attr{Type: "test_server", Path: "ssh_key_name", Want: "sc-prod-key"}
	if v := load(t, "guard.yaml").Evaluate(read(t, "expand-prior-state.json"), e); v.Pass || v.Code != "GUARD_COUNT_EXCEEDED" {
		t.Fatalf("2 no-op + 1 create with expected 2: %+v", v)
	}
	e.ExpectedNodeCount = 3
	if v := load(t, "guard.yaml").Evaluate(read(t, "expand-prior-state.json"), e); !v.Pass {
		t.Fatalf("2 no-op + 1 create with expected 3: %+v", v)
	}
}
