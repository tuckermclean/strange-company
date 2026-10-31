package preflight_test

import (
	"errors"
	"net/netip"
	"testing"

	"strange.company/sc/internal/bootstrap/preflight"
)

func code(err error) string {
	var pe *preflight.Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

func addr(s string) netip.Addr { return netip.MustParseAddr(s) }

// 50-AC-34: the Traefik ClusterIP is the 20th address of SERVICE_CIDR.
func TestTraefikClusterIP_50AC34(t *testing.T) {
	for _, c := range []struct{ cidr, want string }{
		{"10.43.0.0/16", "10.43.0.20"},
		{"10.96.0.0/12", "10.96.0.20"},
		{"10.43.0.0/26", "10.43.0.20"}, // 64 addresses is the smallest accepted block
		{"10.43.0.7/16", "10.43.0.20"}, // a host bit set in the written CIDR does not move the base
	} {
		got, err := preflight.TraefikClusterIP(c.cidr)
		if err != nil || got != addr(c.want) {
			t.Errorf("TraefikClusterIP(%s) = %v, %v; want %s", c.cidr, got, err, c.want)
		}
	}
}

// 50-AC-34: fewer than 64 addresses is SERVICE_CIDR_TOO_SMALL.
func TestTraefikClusterIP_TooSmall_50AC34(t *testing.T) {
	for _, cidr := range []string{"10.43.0.0/27", "10.43.0.0/28", "10.43.0.0/32"} {
		_, err := preflight.TraefikClusterIP(cidr)
		if code(err) != "SERVICE_CIDR_TOO_SMALL" {
			t.Errorf("TraefikClusterIP(%s) error = %v, want code SERVICE_CIDR_TOO_SMALL", cidr, err)
		}
	}
}

func TestTraefikClusterIP_NotACIDR_50AC34(t *testing.T) {
	if _, err := preflight.TraefikClusterIP("not-a-cidr"); err == nil {
		t.Error("a malformed CIDR must be an error")
	}
}

// 50-AC-34: the k3s default cluster DNS IP is the 10th address.
func TestClusterDNSIP_50AC34(t *testing.T) {
	for _, c := range []struct{ cidr, want string }{{"10.43.0.0/16", "10.43.0.10"}, {"10.96.0.0/12", "10.96.0.10"}} {
		got, err := preflight.ClusterDNSIP(c.cidr)
		if err != nil || got != addr(c.want) {
			t.Errorf("ClusterDNSIP(%s) = %v, %v; want %s", c.cidr, got, err, c.want)
		}
	}
}

// 50-AC-34: production pins only the default DNS IP, which never collides; an injected pin equal to the derived
// address is SERVICE_CIDR_IP_COLLISION (D3).
func TestCheckPinnedIPs_50AC34(t *testing.T) {
	got, err := preflight.CheckPinnedIPs("10.43.0.0/16", []netip.Addr{addr("10.43.0.10")})
	if err != nil || got != addr("10.43.0.20") {
		t.Fatalf("default DNS pin: got %v, %v; want 10.43.0.20 and no error", got, err)
	}
	for _, pinned := range [][]netip.Addr{
		{addr("10.43.0.20")},
		{addr("10.43.0.10"), addr("10.43.0.20")},
		{addr("10.43.0.11"), addr("10.43.0.20"), addr("10.43.0.30")},
	} {
		if _, err := preflight.CheckPinnedIPs("10.43.0.0/16", pinned); code(err) != "SERVICE_CIDR_IP_COLLISION" {
			t.Errorf("pinned %v: error = %v, want code SERVICE_CIDR_IP_COLLISION", pinned, err)
		}
	}
	if _, err := preflight.CheckPinnedIPs("10.43.0.0/27", nil); code(err) != "SERVICE_CIDR_TOO_SMALL" {
		t.Errorf("a /27 must be SERVICE_CIDR_TOO_SMALL, got %v", err)
	}
}

func resolver(m map[string]string) func(string) ([]netip.Addr, error) {
	return func(host string) ([]netip.Addr, error) {
		if a, ok := m[host]; ok {
			return []netip.Addr{addr(a)}, nil
		}
		return nil, errors.New("no such host " + host)
	}
}

// 50-AC-35 (PF-28): a public endpoint on 443 yields no SC_S3_CIDR and no SC_S3_PORT.
func TestEgressVars_PublicEndpointHasNone_50AC35(t *testing.T) {
	for _, ep := range []string{"https://s3.example.net", "https://s3.example.net:443"} {
		vars, err := preflight.EgressVars(ep, "", resolver(map[string]string{"s3.example.net": "203.0.113.9"}))
		if err != nil {
			t.Fatalf("%s: %v", ep, err)
		}
		if _, ok := vars["SC_S3_CIDR"]; ok {
			t.Errorf("%s: SC_S3_CIDR must be absent, got %v", ep, vars)
		}
		if _, ok := vars["SC_S3_PORT"]; ok {
			t.Errorf("%s: SC_S3_PORT must be absent, got %v", ep, vars)
		}
	}
}

// 50-AC-35 (PF-28): a private endpoint with S3_PRIVATE_CIDR yields both variables.
func TestEgressVars_PrivateEndpointWithCIDR_50AC35(t *testing.T) {
	vars, err := preflight.EgressVars("https://10.20.0.5:9000", "10.20.0.0/24", nil)
	if err != nil {
		t.Fatal(err)
	}
	if vars["SC_S3_CIDR"] != "10.20.0.0/24" || vars["SC_S3_PORT"] != "9000" {
		t.Errorf("got %v, want SC_S3_CIDR=10.20.0.0/24 and SC_S3_PORT=9000", vars)
	}
}

// 50-AC-35 (PF-28): a private address or a port other than 443 without S3_PRIVATE_CIDR is unsupported.
func TestEgressVars_PlacementUnsupportedWithoutCIDR_50AC35(t *testing.T) {
	res := resolver(map[string]string{"s3.example.net": "203.0.113.9", "s3.internal.example.net": "10.20.0.5"})
	for _, ep := range []string{
		"https://10.20.0.5:9000",
		"https://10.20.0.5",
		"https://s3.example.net:9000",
		"https://s3.internal.example.net",
	} {
		vars, err := preflight.EgressVars(ep, "", res)
		if code(err) != "S3_ENDPOINT_PLACEMENT_UNSUPPORTED" {
			t.Errorf("%s: error = %v (vars %v), want code S3_ENDPOINT_PLACEMENT_UNSUPPORTED", ep, err, vars)
		}
	}
}

// 50-AC-35 (PF-28): S3_PRIVATE_CIDR must contain the endpoint address, else the placement is unsupported.
func TestEgressVars_CIDRMustContainEndpoint_50AC35(t *testing.T) {
	res := resolver(map[string]string{"s3.internal.example.net": "10.20.0.5", "s3.example.net": "203.0.113.9"})
	for _, tc := range []struct{ ep, cidr string }{
		{"https://10.20.0.5:9000", "10.99.0.0/24"},
		{"https://s3.internal.example.net", "10.30.0.0/16"},
		{"https://s3.example.net:9000", "10.20.0.0/24"},
	} {
		vars, err := preflight.EgressVars(tc.ep, tc.cidr, res)
		if code(err) != "S3_ENDPOINT_PLACEMENT_UNSUPPORTED" {
			t.Errorf("%s with %s: error = %v (vars %v), want code S3_ENDPOINT_PLACEMENT_UNSUPPORTED", tc.ep, tc.cidr, err, vars)
		}
	}
}

// 50-AC-35 (PF-28): SC_S3_CIDR is the canonical network, never the host address as written.
func TestEgressVars_CIDRIsCanonical_50AC35(t *testing.T) {
	vars, err := preflight.EgressVars("https://10.20.0.5:9000", "10.20.0.5/24", nil)
	if err != nil {
		t.Fatal(err)
	}
	if vars["SC_S3_CIDR"] != "10.20.0.0/24" {
		t.Errorf("SC_S3_CIDR = %q, want 10.20.0.0/24", vars["SC_S3_CIDR"])
	}
}
