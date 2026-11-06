package ovh_test

import (
	"bytes"
	"context"
	"testing"

	"strange.company/sc/internal/provider/ovh"
)

// 04-AC-4 (fail closed): without the catalogue plan codes the order-based detection cannot tell this install's orders
// from foreign ones and would silently find nothing, so the scan is an error before any request.
func TestOrphanScan_EmptyPlanCodesIsAnError_04AC4_FailClosed(t *testing.T) {
	for _, plans := range [][]string{nil, {}} {
		cfg := scanCfg()
		cfg.PlanCodes = plans
		f := newFakeOVH(t, loadRoutes(t, "orphan/none.json"))
		got, err := ovh.NewScanner(f.client(), cfg).Scan(context.Background())
		if err == nil {
			t.Fatalf("Scan with PlanCodes %#v = %+v, nil; want an error", plans, got)
		}
		if len(f.calls) != 0 {
			t.Errorf("the scan issued %v before rejecting its configuration", f.calls)
		}
	}
}

// 04-AC-3 (plan 03 section 14.8 step 4): a VPS booted into OVH's rescue system is "unknown", never "provisioning"
// (that would pin the rescue system's host key); maintenance stays provisioning.
func TestNormalize_RescuedIsUnknown_04AC3(t *testing.T) {
	for _, c := range []struct{ state, want string }{
		{"rescued", "unknown"},
		{"maintenance", "provisioning"},
	} {
		t.Run(c.state, func(t *testing.T) {
			in := baseInput(t)
			in.Nodes[0].VPS = bytes.Replace(in.Nodes[0].VPS, []byte(`"state": "running"`), []byte(`"state": "`+c.state+`"`), 1)
			if !bytes.Contains(in.Nodes[0].VPS, []byte(`"state": "`+c.state+`"`)) {
				t.Fatalf("fixture has no state to replace")
			}
			doc, _ := normalize(t, in)
			if got := node0(t, doc)["state"]; got != c.want {
				t.Errorf("node state for OVH state %s = %v, want %s", c.state, got, c.want)
			}
		})
	}
}
