package guard_test

import (
	"fmt"
	"strings"
	"testing"

	"strange.company/sc/internal/provider/guard"
)

// F09 slice S8, 03-AC-12 (plan 03 section 20.1, DR-12): in recover mode only, guard rule 6 accepts a resource outside
// the configured region when it is fenced; unfenced resources of the old region still fail GUARD_REGION_MISMATCH;
// replace and delete stay forbidden.
//
// Seam the tests fix: Guard.EvaluateRecover(planJSON, Expect, RecoverMode{Fenced: <plan addresses of
// facts.fenced_nodes>}). Evaluate (apply mode) is unchanged.

const rcGuard = `apiVersion: strange.company/v1alpha1
kind: TofuPlanGuard
metadata:
  name: recover-fixture
spec:
  rootModule: infra/tofu/compute/fake
  allowedProviders:
    - registry.opentofu.org/strange-company/fake
  forbidReplace: true
  resources:
    - type: fake_node
      paid: true
      allowedActions: [no-op, read, create, update, delete]
      maxCount: expectedNodeCount
`

type rcRes struct {
	name, region string
	actions      []string
}

func rcAddr(name string) string { return fmt.Sprintf("fake_node.node[%q]", name) }

func rcPlan(rs ...rcRes) []byte {
	var ch []string
	for _, r := range rs {
		acts := `"` + strings.Join(r.actions, `","`) + `"`
		ch = append(ch, fmt.Sprintf(`{"address":%q,"mode":"managed","type":"fake_node","name":"node","index":%q,`+
			`"provider_name":"registry.opentofu.org/strange-company/fake","change":{"actions":[%s],"before":null,`+
			`"after":{"name":%q,"region":%q,"ssh_key":"key-1"},"after_unknown":{},"after_sensitive":{}}}`,
			rcAddr(r.name), r.name, acts, r.name, r.region))
	}
	return []byte(`{"format_version":"1.2","terraform_version":"1.13.1","resource_changes":[` + strings.Join(ch, ",") +
		`],"configuration":{"provider_config":{"fake":{"name":"fake","full_name":"registry.opentofu.org/strange-company/fake"}}}}`)
}

func rcExpect(n int) guard.Expect {
	return guard.Expect{
		ExpectedNodeCount: n,
		Region:            guard.Attr{Type: "fake_node", Path: "region", Want: "fake-region-b"},
		SSHKey:            guard.Attr{Type: "fake_node", Path: "ssh_key", Want: "key-1"},
	}
}

func rcLoad(t *testing.T) guard.Guard {
	t.Helper()
	g, err := guard.ParseGuard([]byte(rcGuard))
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// 03-AC-12: with node-01 fenced, a plan that still shows it in the old region and creates node-02 in the configured
// region passes in recover mode; apply mode refuses the same plan with GUARD_REGION_MISMATCH.
func TestGuardRecover_FencedOldRegionResourceIsAccepted_03AC12(t *testing.T) {
	g := rcLoad(t)
	plan := rcPlan(rcRes{"node-01", "fake-region-a", []string{"no-op"}}, rcRes{"node-02", "fake-region-b", []string{"create"}})
	v := g.EvaluateRecover(plan, rcExpect(2), guard.RecoverMode{Fenced: []string{rcAddr("node-01")}})
	if !v.Pass {
		t.Errorf("recover mode, node-01 fenced: verdict %+v, want pass", v)
	}
	if v := g.Evaluate(plan, rcExpect(2)); v.Pass || v.Code != guard.CodeRegionMismatch {
		t.Errorf("apply mode: verdict %+v, want GUARD_REGION_MISMATCH (the exception is recover-only)", v)
	}
}

// 03-AC-12: a resource in the old region that is not fenced still fails GUARD_REGION_MISMATCH, even when another
// resource of that region is fenced, and with no fenced list at all.
func TestGuardRecover_UnfencedOldRegionResourceStillFails_03AC12(t *testing.T) {
	g := rcLoad(t)
	plan := rcPlan(rcRes{"node-01", "fake-region-a", []string{"no-op"}}, rcRes{"node-03", "fake-region-a", []string{"no-op"}},
		rcRes{"node-02", "fake-region-b", []string{"create"}})
	for _, c := range []struct {
		name   string
		fenced []string
		addr   string
	}{
		{"one of two fenced", []string{rcAddr("node-01")}, rcAddr("node-03")},
		{"none fenced", nil, rcAddr("node-01")},
	} {
		t.Run(c.name, func(t *testing.T) {
			v := g.EvaluateRecover(plan, rcExpect(3), guard.RecoverMode{Fenced: c.fenced})
			if v.Pass || v.Code != guard.CodeRegionMismatch {
				t.Fatalf("verdict %+v, want GUARD_REGION_MISMATCH", v)
			}
			if v.Address != c.addr {
				t.Errorf("address = %q, want %q", v.Address, c.addr)
			}
		})
	}
}

// 03-AC-12: recover mode does not relax the other rules: a plain recover plan (nothing in the old region) passes, a
// resource in a region that is neither the configured nor a fenced one fails, and a fenced address does not excuse a
// resource named differently.
func TestGuardRecover_OnlyTheFencedAddressesAreExcused_03AC12(t *testing.T) {
	g := rcLoad(t)
	ok := g.EvaluateRecover(rcPlan(rcRes{"node-02", "fake-region-b", []string{"create"}}), rcExpect(1), guard.RecoverMode{Fenced: []string{rcAddr("node-01")}})
	if !ok.Pass {
		t.Errorf("replacement-only plan: verdict %+v, want pass", ok)
	}
	v := g.EvaluateRecover(rcPlan(rcRes{"node-02", "fake-region-c", []string{"create"}}), rcExpect(1), guard.RecoverMode{Fenced: []string{rcAddr("node-01")}})
	if v.Pass || v.Code != guard.CodeRegionMismatch {
		t.Errorf("new node in a third region: verdict %+v, want GUARD_REGION_MISMATCH", v)
	}
}

// 03-AC-12: replace and delete stay forbidden in recover mode, fenced or not, even where the guard lists delete as an
// allowed action for apply.
func TestGuardRecover_ReplaceAndDeleteStayForbidden_03AC12(t *testing.T) {
	g := rcLoad(t)
	fenced := guard.RecoverMode{Fenced: []string{rcAddr("node-01")}}
	replace := rcPlan(rcRes{"node-02", "fake-region-b", []string{"delete", "create"}})
	if v := g.EvaluateRecover(replace, rcExpect(1), fenced); v.Pass || v.Code != guard.CodeReplaceForbidden {
		t.Errorf("replace: verdict %+v, want GUARD_REPLACE_FORBIDDEN", v)
	}
	for _, name := range []string{"node-01", "node-02"} {
		del := rcPlan(rcRes{name, "fake-region-a", []string{"delete"}}, rcRes{"node-03", "fake-region-b", []string{"create"}})
		if v := g.EvaluateRecover(del, rcExpect(2), fenced); v.Pass || v.Code != guard.CodeActionForbidden {
			t.Errorf("delete of %s: verdict %+v, want GUARD_ACTION_FORBIDDEN", name, v)
		}
	}
}
