package guard_test

import (
	"strings"
	"testing"

	"strange.company/sc/internal/provider/guard"
)

// CD-72 review 2 of the S2 guard (plan 03 section 14.4 rule 6, contracts TofuPlanGuard).

const regSel = `plan[0].configuration[?label=="vps_datacenter"].value`

// 03-AC-3: unknown shapes in selector lists fail closed with the rule's code.
func TestGuardEvaluate_SelectorUnknownShapes_03AC3(t *testing.T) {
	cases := []struct {
		fixture string
		code    string // empty means pass
	}{
		{"selector-unknown-key-omitted.json", "GUARD_REGION_MISMATCH"},
		{"selector-unknown-null-element.json", "GUARD_REGION_MISMATCH"},
		{"selector-unknown-list-omitted.json", "GUARD_REGION_MISMATCH"},
		{"selector-unknown-list.json", "GUARD_REGION_MISMATCH"},
		{"selector-unrelated-unknown.json", ""},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			e := expect(false)
			e.Region = guard.Attr{Type: "test_server", Path: regSel, Want: "REG-1"}
			e.SSHKey = guard.Attr{Type: "test_server", Path: `plan[0].configuration[?label=="ssh_key"].value`, Want: "sc-prod-key"}
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

// 03-AC-3: a declared rule 6 Type that is not a paid type of the guard is undeclared, not skipped.
func TestGuardEvaluate_Rule6TypeNotPaidIsUndeclared_03AC3(t *testing.T) {
	cases := []struct {
		name        string
		region, ssh guard.Attr
	}{
		{"region type typo", guard.Attr{Type: "test_srever", Path: "region", Want: "REG-1"}, guard.Attr{Type: "test_server", Path: "ssh_key_name", Want: "sc-prod-key"}},
		{"ssh type typo", guard.Attr{Type: "test_server", Path: "region", Want: "REG-1"}, guard.Attr{Type: "test_srever", Path: "ssh_key_name", Want: "sc-prod-key"}},
		{"region type unpaid", guard.Attr{Type: "test_ssh_key", Path: "region", Want: "REG-1"}, guard.Attr{Type: "test_server", Path: "ssh_key_name", Want: "sc-prod-key"}},
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

// 03-AC-3: ParseGuard types follow the schema: YAML 1.1 booleans, non-strings and extra documents are rejected.
// (name: "" and maxCount: 1.0 are valid per the schema, which has no minLength and accepts 1.0 as an integer, so
// they are deliberately not asserted here.)
func TestParseGuard_SchemaTypesAndDocuments_03AC3(t *testing.T) {
	cases := []struct{ name, from, to string }{
		{"paid yes", "      paid: true\n", "      paid: yes\n"},
		{"paid on", "      paid: true\n", "      paid: on\n"},
		{"forbidReplace on", "forbidReplace: true", "forbidReplace: on"},
		{"forbidReplace yes", "forbidReplace: true", "forbidReplace: yes"},
		{"metadata.name number", "name: generic-paid", "name: 123"},
		{"second document", "      maxCount: 1\n", "      maxCount: 1\n---\nkind: Other\n"},
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
