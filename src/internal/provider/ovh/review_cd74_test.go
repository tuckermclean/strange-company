package ovh_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"strange.company/sc/internal/provider/ovh"
)

// 04-AC-5 (CD-74, plan 04 section 10): the orphan scan lists services with GET /vps, so the section 10 set includes
// the exact rule GET /vps; the fixture is the section 10 set plus that rule.
func TestCredentialScope_GetVpsListRulePasses_04AC5_CD74(t *testing.T) {
	f := ovh.CheckCredentialScope(credFixture(t, "auth/current-credential-vps-list.json"), "")
	if f.ID != "PF-07" || f.Status != "pass" || f.Code != "" {
		t.Errorf("finding = %+v, want PF-07 pass for the section 10 set plus GET /vps", f)
	}
}

// 04-AC-5 (CD-74): only the exact GET /vps is added; broader spellings of it stay too broad.
func TestCredentialScope_GetVpsBroaderFormsFail_04AC5_CD74(t *testing.T) {
	for _, path := range []string{"/vps*", "/*", "/vp*", "/vps/", "/vps/**", "/vps*/*"} {
		t.Run("GET "+path, func(t *testing.T) {
			f := ovh.CheckCredentialScope(ovh.CurrentCredential{
				Rules:  []ovh.AccessRule{{Method: "GET", Path: "/vps"}, {Method: "GET", Path: path}},
				Status: "validated",
			}, "")
			if f.ID != "PF-07" || f.Status != "fail" || f.Code != "OVH_CREDENTIAL_TOO_BROAD" {
				t.Errorf("finding = %+v, want PF-07 fail OVH_CREDENTIAL_TOO_BROAD", f)
			}
		})
	}
	for _, m := range []string{"POST", "PUT", "DELETE"} {
		f := ovh.CheckCredentialScope(ovh.CurrentCredential{Rules: []ovh.AccessRule{{Method: m, Path: "/vps"}}, Status: "validated"}, "")
		if f.Status != "fail" || f.Code != "OVH_CREDENTIAL_TOO_BROAD" {
			t.Errorf("%s /vps: finding = %+v, want fail OVH_CREDENTIAL_TOO_BROAD", m, f)
		}
	}
}

// mutateRoutes loads an orphan fixture and replaces route bodies (json text) before serving it.
func mutateRoutes(t *testing.T, rel string, replace map[string]string) map[string]json.RawMessage {
	t.Helper()
	routes := loadRoutes(t, rel)
	for k, v := range replace {
		if _, ok := routes[k]; !ok {
			t.Fatalf("fixture %s has no route %q", rel, k)
		}
		routes[k] = json.RawMessage(v)
	}
	return routes
}

// 04-AC-4 (plan 03 section 14.8 fail closed): a delivered order of a catalogue plan whose details carry no domain is
// still a paid resource; it is reported with the order id and no provider id.
func TestOrphanScan_DeliveredOrderWithEmptyDomain_04AC4_FailClosed(t *testing.T) {
	routes := mutateRoutes(t, "orphan/pending-order.json", map[string]string{"GET /me/order/9001001/status": `"delivered"`})
	f := newFakeOVH(t, routes)
	got, err := ovh.NewScanner(f.client(), scanCfg()).Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := []ovh.Orphan{{Code: "ORPHAN_PAID_RESOURCE", OrderID: "9001001"}}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("Scan = %+v, want %+v", got, want)
	}
}

// 04-AC-4 (fail closed): an order whose plan code is null cannot be shown to be foreign, so it is reported instead of
// skipped: not delivered is ORPHAN_PENDING_ORDER, delivered is ORPHAN_PAID_RESOURCE.
func TestOrphanScan_NullPlanCodeIsReported_04AC4_FailClosed(t *testing.T) {
	for _, c := range []struct{ name, status, extension, code string }{
		{"pending, plan null", `"delivering"`, `{"order": {"plan": null}}`, "ORPHAN_PENDING_ORDER"},
		{"pending, plan code null", `"delivering"`, `{"order": {"plan": {"code": null}}}`, "ORPHAN_PENDING_ORDER"},
		{"pending, no order", `"notPaid"`, `{}`, "ORPHAN_PENDING_ORDER"},
		{"delivered, plan null", `"delivered"`, `{"order": {"plan": null}}`, "ORPHAN_PAID_RESOURCE"},
		{"delivered, plan code null", `"delivered"`, `{"order": {"plan": {"code": null}}}`, "ORPHAN_PAID_RESOURCE"},
	} {
		t.Run(c.name, func(t *testing.T) {
			routes := mutateRoutes(t, "orphan/pending-order.json", map[string]string{
				"GET /me/order/9001001/status":              c.status,
				"GET /me/order/9001001/details/1/extension": c.extension,
			})
			f := newFakeOVH(t, routes)
			got, err := ovh.NewScanner(f.client(), scanCfg()).Scan(context.Background())
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if len(got) != 1 || got[0].Code != c.code || got[0].OrderID != "9001001" {
				t.Errorf("Scan = %+v, want one %s for order 9001001", got, c.code)
			}
		})
	}
}

// 04-AC-4 (fail closed): a scan without the cluster or the install's created_at cannot tell this install's resources
// from foreign ones (the name pattern would be sc--node-NN and date.from empty), so it is an error, not an empty scan.
func TestOrphanScan_IncompleteConfigIsAnError_04AC4_FailClosed(t *testing.T) {
	for _, c := range []struct {
		name string
		mut  func(*ovh.ScanConfig)
	}{
		{"empty cluster", func(c *ovh.ScanConfig) { c.Cluster = "" }},
		{"empty install created_at", func(c *ovh.ScanConfig) { c.InstallCreatedAt = "" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg := scanCfg()
			c.mut(&cfg)
			routes := loadRoutes(t, "orphan/none.json")
			routes["GET /me/order"] = json.RawMessage(`[]`)
			f := newFakeOVH(t, routes)
			got, err := ovh.NewScanner(f.client(), cfg).Scan(context.Background())
			if err == nil {
				t.Fatalf("Scan = %+v, nil; want an error for %s", got, c.name)
			}
			if len(f.calls) != 0 {
				t.Errorf("the scan issued %v before rejecting its configuration", f.calls)
			}
		})
	}
}

// 04-AC-3 (plan 04 section 14.5): public-cloud costs differ from the VPS cost model Normalize implements, so any other
// product line is refused until it is built.
func TestNormalize_OtherProductLineIsRefused_04AC3(t *testing.T) {
	for _, line := range []string{"public-cloud", "", "VPS", "dedicated"} {
		t.Run("line="+line, func(t *testing.T) {
			in := baseInput(t)
			in.ProductLine = line
			out, err := ovh.Normalize(in)
			if err == nil {
				t.Fatalf("Normalize with ProductLine %q succeeded (%d bytes), want an error", line, len(out))
			}
		})
	}
}

// 04-AC-3 (plan 03 section 17 determinism): nodes are sorted by name whatever the input order.
func TestNormalize_NodesSortedByName_04AC3(t *testing.T) {
	in := baseInput(t)
	one := in.Nodes[0]
	in.Nodes = nil
	for _, n := range []string{"node-03", "node-01", "node-02"} {
		x := one
		x.Name = n
		in.Nodes = append(in.Nodes, x)
	}
	doc, _ := normalize(t, in)
	nodes, _ := doc["nodes"].([]any)
	var got []string
	for _, n := range nodes {
		got = append(got, n.(map[string]any)["name"].(string))
	}
	if strings.Join(got, ",") != "node-01,node-02,node-03" {
		t.Errorf("node order = %v, want node-01,node-02,node-03", got)
	}
}

// 04-AC-3: the same nodes in two input orders give byte-identical output.
func TestNormalize_OutputIndependentOfInputOrder_04AC3(t *testing.T) {
	a := baseInput(t)
	one := a.Nodes[0]
	mk := func(names ...string) []ovh.NodeInput {
		var l []ovh.NodeInput
		for _, n := range names {
			x := one
			x.Name = n
			l = append(l, x)
		}
		return l
	}
	a.Nodes = mk("node-01", "node-02")
	b := a
	b.Nodes = mk("node-02", "node-01")
	oa, err := ovh.Normalize(a)
	if err != nil {
		t.Fatal(err)
	}
	ob, err := ovh.Normalize(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(oa) != string(ob) {
		t.Errorf("Normalize output depends on the order of the input nodes")
	}
}

// 04-AC-3: every value of the published vps.VpsStateEnum (fetched 2026-11-01 from https://eu.api.ovh.com/1.0/vps.json)
// maps to a defined ProviderOutput node state; anything else is unknown.
func TestNormalize_NodeStateCoversVpsStateEnum_04AC3(t *testing.T) {
	const (
		running      = "running"
		provisioning = "provisioning"
		stopped      = "stopped"
		unknown      = "unknown"
	)
	defined := map[string]bool{running: true, provisioning: true, stopped: true}
	cases := []struct {
		state string
		want  string // empty: any defined state other than unknown
	}{
		{"running", running}, {"stopped", stopped},
		{"installing", provisioning}, {"upgrading", provisioning},
		{"rebooting", ""}, {"stopping", ""}, {"backuping", ""}, {"maintenance", ""}, {"rescued", ""},
		// not in vps.VpsStateEnum
		{"reinstalling", unknown}, {"starting", unknown}, {"migrating", unknown}, {"Running", unknown},
		{"", unknown}, {"deleted", unknown},
	}
	for _, c := range cases {
		t.Run("state="+c.state, func(t *testing.T) {
			var v map[string]any
			if err := json.Unmarshal(fixture(t, "vps/vps-get.json"), &v); err != nil {
				t.Fatal(err)
			}
			v["state"] = c.state
			raw, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			in := baseInput(t)
			in.Nodes[0].VPS = raw
			doc, _ := normalize(t, in)
			got, _ := doc["nodes"].([]any)[0].(map[string]any)["state"].(string)
			switch {
			case c.want != "" && got != c.want:
				t.Errorf("state %q maps to %q, want %q", c.state, got, c.want)
			case c.want == "" && !defined[got]:
				t.Errorf("state %q maps to %q, want one of running, provisioning, stopped", c.state, got)
			}
		})
	}
}
