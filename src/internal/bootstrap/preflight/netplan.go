// Package preflight holds the pure preflight computations of plan 50 section 14.2 (PF-28 egress placement,
// PF-30 service CIDR and pinned ClusterIPs).
package preflight

import (
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
)

// Error is a preflight refusal with a stable code (SERVICE_CIDR_TOO_SMALL, SERVICE_CIDR_IP_COLLISION,
// S3_ENDPOINT_PLACEMENT_UNSUPPORTED).
type Error struct {
	Code    string
	Message string
}

// Error implements error.
func (e *Error) Error() string { return e.Code + ": " + e.Message }

func fail(code, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...)}
}

// minServiceAddrs is the smallest accepted service CIDR (CD-37).
const minServiceAddrs = 64

// nth returns the base address of cidr plus n (host bits of the written CIDR are ignored).
func nth(serviceCIDR string, n int) (netip.Addr, error) {
	p, err := netip.ParsePrefix(serviceCIDR)
	if err != nil {
		return netip.Addr{}, fail("SERVICE_CIDR_INVALID", "SERVICE_CIDR %q is not a CIDR", serviceCIDR)
	}
	p = p.Masked()
	hostBits := p.Addr().BitLen() - p.Bits()
	if hostBits < 6 { // fewer than 64 addresses
		return netip.Addr{}, fail("SERVICE_CIDR_TOO_SMALL", "SERVICE_CIDR %s holds fewer than %d addresses", p, minServiceAddrs)
	}
	a := p.Addr()
	for i := 0; i < n; i++ {
		a = a.Next()
	}
	return a, nil
}

// TraefikClusterIP derives the Traefik ClusterIP: the 20th address of the service CIDR (network address plus 20),
// CD-37. A CIDR with fewer than 64 addresses fails with SERVICE_CIDR_TOO_SMALL.
func TraefikClusterIP(serviceCIDR string) (ip netip.Addr, err error) { return nth(serviceCIDR, 20) }

// ClusterDNSIP is the k3s default cluster DNS address: the 10th address of the service CIDR (network address plus 10).
func ClusterDNSIP(serviceCIDR string) (ip netip.Addr, err error) { return nth(serviceCIDR, 10) }

// CheckPinnedIPs derives the Traefik ClusterIP and fails with SERVICE_CIDR_IP_COLLISION if it equals any address of
// pinned (the cluster DNS IP and every other pinned ClusterIP). The set is an input so a fixture can inject one (D3).
func CheckPinnedIPs(serviceCIDR string, pinned []netip.Addr) (traefik netip.Addr, err error) {
	traefik, err = TraefikClusterIP(serviceCIDR)
	if err != nil {
		return netip.Addr{}, err
	}
	for _, p := range pinned {
		if p == traefik {
			return netip.Addr{}, fail("SERVICE_CIDR_IP_COLLISION", "the derived Traefik ClusterIP %s is already pinned", traefik)
		}
	}
	return traefik, nil
}

func privateAddr(a netip.Addr) bool {
	return a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsUnspecified()
}

// EgressVars computes the render-time egress variables of PF-28 for cluster-vars: SC_S3_CIDR and SC_S3_PORT, present
// only when the endpoint resolves to a private address or uses a port other than 443. Such an endpoint without
// privateCIDR fails with S3_ENDPOINT_PLACEMENT_UNSUPPORTED. resolve maps a host name to its addresses; it is not
// called for an IP literal.
func EgressVars(endpoint, privateCIDR string, resolve func(host string) ([]netip.Addr, error)) (vars map[string]string, err error) {
	u, perr := url.Parse(endpoint)
	if perr != nil || u.Hostname() == "" {
		return nil, fail("S3_ENDPOINT_INVALID", "S3_ENDPOINT is not a URL with a host")
	}
	port := 443
	if u.Scheme == "http" {
		port = 80
	}
	if ps := u.Port(); ps != "" {
		n, cerr := strconv.Atoi(ps)
		if cerr != nil || n < 1 || n > 65535 {
			return nil, fail("S3_ENDPOINT_INVALID", "S3_ENDPOINT port %q is not valid", ps)
		}
		port = n
	}
	host := u.Hostname()
	var addrs []netip.Addr
	if a, aerr := netip.ParseAddr(host); aerr == nil {
		addrs = []netip.Addr{a}
	} else {
		if resolve == nil {
			return nil, fail("S3_ENDPOINT_UNRESOLVABLE", "no resolver to look up %s", host)
		}
		addrs, err = resolve(host)
		if err != nil {
			return nil, fail("S3_ENDPOINT_UNRESOLVABLE", "cannot resolve %s: %v", host, err)
		}
	}
	private := false
	for _, a := range addrs {
		if privateAddr(a.Unmap()) {
			private = true
		}
	}
	if !private && port == 443 {
		return map[string]string{}, nil
	}
	if privateCIDR == "" {
		return nil, fail("S3_ENDPOINT_PLACEMENT_UNSUPPORTED",
			"S3_ENDPOINT %s resolves to a private address or uses port %d; set S3_PRIVATE_CIDR", host, port)
	}
	p, perr := netip.ParsePrefix(privateCIDR)
	if perr != nil {
		return nil, fail("S3_PRIVATE_CIDR_INVALID", "S3_PRIVATE_CIDR %q is not a CIDR", privateCIDR)
	}
	return map[string]string{"SC_S3_CIDR": p.String(), "SC_S3_PORT": strconv.Itoa(port)}, nil
}
