package ovh_test

import (
	"context"
	"reflect"
	"testing"

	"strange.company/sc/internal/provider/ovh"
)

const (
	svcNode01  = "vps-0a1b2c3d.vps.ovh.net"
	svcUnnamed = "vps-4b5c6d7e.vps.ovh.net"
)

func scanCfg(stateIDs ...string) ovh.ScanConfig {
	return ovh.ScanConfig{
		Cluster:          "prod",
		InstallCreatedAt: "2026-11-01T08:00:00Z",
		PlanCodes:        []string{"vps-2025-model1", "vps-2025-model2", "vps-2025-model3"},
		StateIDs:         stateIDs,
	}
}

// 04-AC-4 (plan 04 section 14.4, 22): the orphan-scan table, driven through a signed client against the derived
// fixtures, no network.
func TestOrphanScan_Table_04AC4(t *testing.T) {
	cases := []struct {
		name, fixture string
		state         []string
		want          []ovh.Orphan
	}{
		{"delivered orphan", "orphan/delivered-orphan.json", nil, []ovh.Orphan{{Code: "ORPHAN_PAID_RESOURCE", ProviderID: svcNode01}}},
		{"pending order", "orphan/pending-order.json", nil, []ovh.Orphan{{Code: "ORPHAN_PENDING_ORDER", OrderID: "9001001"}}},
		{"foreign vps", "orphan/foreign-vps.json", nil, nil},
		{"none", "orphan/none.json", nil, nil},
		{"delivered unnamed order (BOOT-28)", "orphan/delivered-unnamed-order.json", nil, []ovh.Orphan{{Code: "ORPHAN_PAID_RESOURCE", ProviderID: svcUnnamed, OrderID: "9001002"}}},
		{"delivered orphan already in state", "orphan/delivered-orphan.json", []string{svcNode01}, nil},
		{"state, cancelled and foreign-product orders", "orphan/known-in-state.json", []string{svcNode01}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeOVH(t, loadRoutes(t, c.fixture))
			got, err := ovh.NewScanner(f.client(), scanCfg(c.state...)).Scan(context.Background())
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if len(got) == 0 && len(c.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Scan = %+v, want %+v", got, c.want)
			}
		})
	}
}

// 04-AC-4: the scan lists services first and reads the orders since the install's created_at.
func TestOrphanScan_ReadsListsOnly_04AC4(t *testing.T) {
	f := newFakeOVH(t, loadRoutes(t, "orphan/none.json"))
	if _, err := ovh.NewScanner(f.client(), scanCfg()).Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	var vps, orders bool
	for _, c := range f.calls {
		vps = vps || c == "GET /1.0/vps"
		orders = orders || len(c) >= len("GET /1.0/me/order?") && c[:len("GET /1.0/me/order?")] == "GET /1.0/me/order?"
	}
	if !vps || !orders {
		t.Errorf("calls %v: want GET /vps and GET /me/order?date.from=...", f.calls)
	}
}
