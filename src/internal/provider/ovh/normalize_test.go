package ovh_test

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"strange.company/sc/internal/contracts"
	"strange.company/sc/internal/provider/ovh"
)

func repoContracts(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "..", "..", "contracts")
}

func baseInput(t *testing.T) ovh.NormalizeInput {
	t.Helper()
	return ovh.NormalizeInput{
		Cluster:               "prod",
		GeneratedAt:           "2026-11-01T12:00:00Z",
		ImplementationVersion: "0.1.0",
		ProductLine:           "vps",
		Region:                "GRA",
		MachineClass:          "ovh-vps-6c-12g-100nvme",
		PlanCode:              "vps-2025-model2",
		Currency:              "EUR",
		MonthlyTotal:          "9.99",
		MaxMonthlyCost:        "25",
		CatalogVersion:        "2026-11-01",
		StateSerial:           1,
		StateLineage:          "5d2a7b3e-1f64-4c58-9a10-3e6b0c2d7f91",
		Nodes: []ovh.NodeInput{{
			Name:     "node-01",
			VPS:      fixture(t, "vps/vps-get.json"),
			IPs:      fixture(t, "vps/vps-ips.json"),
			Hardware: ovh.HardwareSpec{CPUCount: 6, MemoryBytes: 12 << 30, DiskBytes: 100 << 30, DiskClass: "nvme"},
		}},
	}
}

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("Normalize output is not a JSON object: %v (%d bytes)", err, len(b))
	}
	return doc
}

func normalize(t *testing.T, in ovh.NormalizeInput) (map[string]any, []byte) {
	t.Helper()
	out, err := ovh.Normalize(in)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	return decode(t, out), out
}

func validate(t *testing.T, raw []byte) {
	t.Helper()
	schemas, err := contracts.CompileSchemas(repoContracts(t))
	if err != nil {
		t.Fatal(err)
	}
	sc := schemas["provider"]
	if sc == nil {
		t.Fatal("no provider schema")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var inst any
	if err := dec.Decode(&inst); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if err := sc.Validate(inst); err != nil {
		t.Errorf("ProviderOutput does not validate against provider.schema.yaml: %v", err)
	}
}

func node0(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	nodes, _ := doc["nodes"].([]any)
	if len(nodes) != 1 {
		t.Fatalf("nodes = %v, want exactly one", doc["nodes"])
	}
	n, _ := nodes[0].(map[string]any)
	return n
}

func sub(m map[string]any, k string) map[string]any { v, _ := m[k].(map[string]any); return v }

// 04-AC-3: the recorded (derived) GET /vps/{sn} + ips fixtures normalize to a valid ProviderOutput.
func TestNormalize_ValidatesAgainstProviderSchema_04AC3(t *testing.T) {
	_, raw := normalize(t, baseInput(t))
	validate(t, raw)
}

// 04-AC-3 and plan 04 section 7.2: internal_ip == public_ipv4, reversible_within_hours == false for vps.
func TestNormalize_AddressesAndCapabilities_04AC3(t *testing.T) {
	doc, _ := normalize(t, baseInput(t))
	n := node0(t, doc)
	addr := sub(n, "addresses")
	if addr["public_ipv4"] != "203.0.113.10" {
		t.Errorf("public_ipv4 = %v, want 203.0.113.10", addr["public_ipv4"])
	}
	if addr["internal_ip"] != addr["public_ipv4"] || addr["internal_ip"] == nil {
		t.Errorf("internal_ip = %v, want == public_ipv4 %v", addr["internal_ip"], addr["public_ipv4"])
	}
	if addr["public_ipv6"] != "2001:db8:100:200::10" {
		t.Errorf("public_ipv6 = %v, want 2001:db8:100:200::10", addr["public_ipv6"])
	}
	if v, present := addr["external_ip"]; !present || v != nil {
		t.Errorf("external_ip = %v (present %v), want null", v, present)
	}
	caps := sub(sub(doc, "provider"), "capabilities")
	if caps["reversible_within_hours"] != false {
		t.Errorf("capabilities.reversible_within_hours = %v, want false for vps", caps["reversible_within_hours"])
	}
	for k, want := range map[string]any{"provisions_compute": true, "paid": true, "supports_expand": true, "supports_destroy_compute": true, "host_key_source": "tofu"} {
		if caps[k] != want {
			t.Errorf("capabilities.%s = %v, want %v", k, caps[k], want)
		}
	}
	p := sub(doc, "provider")
	if p["name"] != "ovh" || p["product_line"] != "vps" {
		t.Errorf("provider = %v, want name ovh, product_line vps", p)
	}
}

// 04-AC-3 and plan 04 section 7.2: the field mapping table for vps.
func TestNormalize_NodeMapping_04AC3(t *testing.T) {
	doc, _ := normalize(t, baseInput(t))
	n := node0(t, doc)
	checks := []struct {
		name      string
		got, want any
	}{
		{"name", n["name"], "node-01"},
		{"provider_id", n["provider_id"], svcNode01},
		{"machine_class", n["machine_class"], "ovh-vps-6c-12g-100nvme"},
		{"ssh.host", sub(n, "ssh")["host"], "203.0.113.10"},
		{"ssh.port", sub(n, "ssh")["port"], json.Number("22")},
		{"ssh.user", sub(n, "ssh")["user"], "debian"},
		{"location.region", sub(n, "location")["region"], "GRA"},
		{"location.failure_domain", sub(n, "location")["failure_domain"], "GRA"},
		{"region.provider_region", sub(doc, "region")["provider_region"], "GRA"},
		{"state", n["state"], "running"},
		{"labels install", sub(n, "labels")["strange.company/install"], "prod"},
		{"labels product", sub(n, "labels")["strange.company/provider-product"], "vps-2025-model2"},
		{"tofu.root_module", sub(doc, "tofu")["root_module"], "infra/tofu/compute/ovh-vps"},
		{"tofu.backend_key", sub(doc, "tofu")["backend_key"], "prod/compute/terraform.tfstate"},
		{"cluster", doc["cluster"], "prod"},
		{"generated_at", doc["generated_at"], "2026-11-01T12:00:00Z"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	hw := sub(n, "hardware")
	if hw["cpu_count"] != json.Number("6") || hw["memory_bytes"] != json.Number("12884901888") {
		t.Errorf("hardware = %v, want the catalogue spec 6 cpu, 12 GiB (SSH discovery has not run)", hw)
	}
	disks, _ := hw["disks"].([]any)
	if len(disks) != 1 {
		t.Fatalf("disks = %v, want one root disk", hw["disks"])
	}
	d, _ := disks[0].(map[string]any)
	if d["class"] != "nvme" || d["class_source"] != "provider-catalog" || d["role"] != "root" {
		t.Errorf("disk = %v, want class nvme from provider-catalog, role root (plan 04 section 7.2)", d)
	}
}

// plan 04 section 7.2: a VPS that is still installing is "provisioning"; a VPS without IPv6 has a null public_ipv6.
func TestNormalize_InstallingAndV4Only_04AC3(t *testing.T) {
	in := baseInput(t)
	in.Nodes[0].VPS = fixture(t, "vps/vps-get-installing.json")
	in.Nodes[0].IPs = fixture(t, "vps/vps-ips-v4-only.json")
	doc, raw := normalize(t, in)
	validate(t, raw)
	n := node0(t, doc)
	if n["state"] != "provisioning" {
		t.Errorf("state = %v, want provisioning for OVH state installing", n["state"])
	}
	addr := sub(n, "addresses")
	if v := addr["public_ipv6"]; v != nil {
		t.Errorf("public_ipv6 = %v, want null or absent", v)
	}
	if addr["internal_ip"] != "203.0.113.10" {
		t.Errorf("internal_ip = %v, want 203.0.113.10", addr["internal_ip"])
	}
}

// Three nodes (expand): names stay node-NN and order is kept.
func TestNormalize_ThreeNodes_04AC3(t *testing.T) {
	in := baseInput(t)
	for _, name := range []string{"node-02", "node-03"} {
		n := in.Nodes[0]
		n.Name = name
		in.Nodes = append(in.Nodes, n)
	}
	doc, raw := normalize(t, in)
	validate(t, raw)
	nodes, _ := doc["nodes"].([]any)
	if len(nodes) != 3 {
		t.Fatalf("nodes = %d, want 3", len(nodes))
	}
	for i, want := range []string{"node-01", "node-02", "node-03"} {
		if got, _ := nodes[i].(map[string]any)["name"].(string); got != want {
			t.Errorf("nodes[%d].name = %q, want %q", i, got, want)
		}
	}
}

// Bad API material is an error, not a partial document.
func TestNormalize_RejectsBadInput_04AC3(t *testing.T) {
	in := baseInput(t)
	in.Nodes[0].IPs = []byte(`[]`)
	if _, err := ovh.Normalize(in); err == nil {
		t.Errorf("Normalize accepted a VPS without any IP")
	}
	in = baseInput(t)
	in.Nodes[0].VPS = []byte(`{not json`)
	if _, err := ovh.Normalize(in); err == nil {
		t.Errorf("Normalize accepted a malformed GET /vps response")
	}
}

// ovhKeys are OVH API field names (vps.VPSWithIAM, vps.Model, ip, billing.Order) that must not leak into the
// normalized document (plan 04 section 7.2: higher layers see no provider-specific concept).
var ovhKeys = map[string]bool{
	"displayName": true, "offerType": true, "netbootMode": true, "monitoringIpBlocks": true, "slaMonitoring": true,
	"zoneType": true, "keymap": true, "lockStatus": true, "memoryLimit": true, "iam": true, "serviceName": true,
	"vcore": true, "zone": true, "model": true, "availableOptions": true, "maximumAdditionnalIp": true,
	"diskLocationType": true, "planCode": true, "plan_code": true, "vps_datacenter": true, "vps_os": true,
	"ovhSubsidiary": true, "orderId": true,
}

var ovhValue = regexp.MustCompile(`(?i)\.ovh\.net|vps-20[0-9]{2}-model|os-gra|region openstack|ovh\.com`)

// 04-AC-3 (jq path scan): no field outside provider_id, machine_class, region.provider_region and labels carries an
// OVH-specific key or value. Exempt as provider identity: provider.name, provider.product_line, the tofu
// root_module and cost_estimate.items resource_type / product_id.
func TestNormalize_NoOVHSpecificKeysOutsideAllowedPaths_04AC3(t *testing.T) {
	doc, _ := normalize(t, baseInput(t))
	exempt := map[string]bool{
		"$.provider.name": true, "$.provider.product_line": true, "$.tofu.root_module": true,
		"$.nodes[0].provider_id": true, "$.nodes[0].machine_class": true, "$.region.provider_region": true,
	}
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case map[string]any:
			if path == "$.nodes[0].labels" {
				return
			}
			for k, c := range x {
				if ovhKeys[k] {
					t.Errorf("%s.%s: OVH-specific key outside provider_id, machine_class, region.provider_region, labels", path, k)
				}
				walk(path+"."+k, c)
			}
		case []any:
			for i, c := range x {
				walk(path+"["+itoa(i)+"]", c)
			}
		case string:
			if exempt[path] || strings.Contains(path, ".cost_estimate.items[") && (strings.HasSuffix(path, ".resource_type") || strings.HasSuffix(path, ".product_id")) {
				return
			}
			if ovhValue.MatchString(x) {
				t.Errorf("%s = %q: OVH-specific value outside the allowed fields", path, x)
			}
		}
	}
	walk("$", doc)
}

func itoa(i int) string { return strconv.Itoa(i) }
