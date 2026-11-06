package guard_test

import (
	"strings"
	"testing"

	"strange.company/sc/internal/provider/guard"
)

// CD-73 review 3 of the S2b guard (plan 03 section 14.4 rule 6, contracts TofuPlanGuard).

// 03-AC-3: YAML merge keys must not carry a value past the schema type checks (!!bool, !!str). Decision: merge keys
// and aliases are rejected in these typed fields; an alias to a valid scalar is rejected too (the guard is small and
// has no use for either), so the type check never has to chase a reference.
func TestParseGuard_MergeKeysAndAliasesCannotBypassTypes_03AC3(t *testing.T) {
	cases := []struct{ name, from, to string }{
		{"merge paid no", "      paid: true\n", "      <<: {paid: \"no\"}\n"},
		{"merge paid off", "      paid: true\n", "      <<: {paid: off}\n"},
		{"merge paid yes", "      paid: true\n", "      <<: {paid: yes}\n"},
		{"merge forbidReplace on", "  forbidReplace: true\n", "  <<: {forbidReplace: on}\n"},
		{"merge metadata.name number", "  name: generic-paid\n", "  <<: {name: 123}\n"},
		{"alias paid to no", "      paid: true\n      allowedActions: [no-op, read, create, update]\n      maxCount: expectedNodeCount\n      priceProductFrom: plan[0].plan_code\n",
			"      paid: true\n      allowedActions: [no-op, read, create, update]\n      maxCount: expectedNodeCount\n      priceProductFrom: &p \"no\"\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := strings.Replace(baseGuard, c.from, c.to, 1)
			if doc == baseGuard {
				t.Fatalf("mutation %q did not apply", c.name)
			}
			if c.name == "alias paid to no" {
				doc = strings.Replace(doc, "      paid: false\n", "      paid: *p\n", 1)
			}
			if _, err := guard.ParseGuard([]byte(doc)); err == nil {
				t.Fatalf("ParseGuard accepted a guard the schema rejects:\n%s", doc)
			}
		})
	}
}

// 03-AC-3: an alias to a valid scalar is rejected (documented decision above).
func TestParseGuard_AliasToTrueRejected_03AC3(t *testing.T) {
	doc := strings.Replace(baseGuard, "      paid: true\n", "      paid: &p true\n", 1)
	doc = strings.Replace(doc, "      paid: false\n", "      paid: *p\n", 1)
	if _, err := guard.ParseGuard([]byte(doc)); err == nil {
		t.Fatalf("ParseGuard accepted an alias in a typed field:\n%s", doc)
	}
}

const unpaidGuard = `apiVersion: strange.company/v1alpha1
kind: TofuPlanGuard
metadata:
  name: generic-unpaid
spec:
  rootModule: infra/tofu/compute/generic-unpaid
  allowedProviders:
    - registry.opentofu.org/example/free
  resources:
    - type: test_server
      paid: false
      allowedActions: [no-op, read, create, update]
      maxCount: expectedNodeCount
    - type: test_ssh_key
      paid: false
      allowedActions: [no-op, read, create, update]
      maxCount: 1
`

func unpaidPlan(region, key string) []byte {
	return []byte(`{
  "format_version": "1.2",
  "resource_changes": [
    {"address": "test_ssh_key.main", "mode": "managed", "type": "test_ssh_key", "name": "main",
     "provider_name": "registry.opentofu.org/example/free",
     "change": {"actions": ["create"], "before": null, "after": {"name": "` + key + `"}, "after_unknown": {}, "after_sensitive": {}}},
    {"address": "test_server.node[0]", "mode": "managed", "type": "test_server", "name": "node", "index": 0,
     "provider_name": "registry.opentofu.org/example/free",
     "change": {"actions": ["create"], "before": null, "after": {"region": "` + region + `"}, "after_unknown": {}, "after_sensitive": {}}}
  ],
  "configuration": {"provider_config": {"test": {"name": "test", "full_name": "registry.opentofu.org/example/free"}}}
}`)
}

// 03-AC-3 (CD-73): the paid-type requirement of rule 6 applies when the guard has a paid type. A guard with no paid
// type may declare Region and SSHKey types that exist in the guard; they are judged, not called undeclared.
func TestGuardEvaluate_Rule6UnpaidGuardDeclaredTypes_03AC3(t *testing.T) {
	g, err := guard.ParseGuard([]byte(unpaidGuard))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	e := guard.Expect{
		ExpectedNodeCount: 1,
		Region:            guard.Attr{Type: "test_server", Path: "region", Want: "REG-1"},
		SSHKey:            guard.Attr{Type: "test_ssh_key", Path: "name", Want: "sc-prod-key"},
	}
	if v := g.Evaluate(unpaidPlan("REG-1", "sc-prod-key"), e); !v.Pass {
		t.Fatalf("want pass, got %+v", v)
	}
	if v := g.Evaluate(unpaidPlan("REG-2", "sc-prod-key"), e); v.Pass || v.Code != "GUARD_REGION_MISMATCH" {
		t.Fatalf("want GUARD_REGION_MISMATCH, got %+v", v)
	}
	if v := g.Evaluate(unpaidPlan("REG-1", "other"), e); v.Pass || v.Code != "GUARD_SSH_KEY_MISMATCH" {
		t.Fatalf("want GUARD_SSH_KEY_MISMATCH, got %+v", v)
	}
}
