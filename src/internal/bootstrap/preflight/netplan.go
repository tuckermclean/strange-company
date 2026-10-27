// Package preflight holds the pure preflight computations of plan 50 section 14.2 (PF-28 egress placement,
// PF-30 service CIDR and pinned ClusterIPs).
package preflight

import "net/netip"

// Error is a preflight refusal with a stable code (SERVICE_CIDR_TOO_SMALL, SERVICE_CIDR_IP_COLLISION,
// S3_ENDPOINT_PLACEMENT_UNSUPPORTED).
//
// SC-STUB(F08 50-AC-34 50-AC-35)
type Error struct {
	Code    string
	Message string
}

// Error implements error.
//
// SC-STUB(F08 50-AC-34 50-AC-35)
func (e *Error) Error() string { return "" }

// TraefikClusterIP derives the Traefik ClusterIP: the 20th address of the service CIDR (network address plus 20),
// CD-37. A CIDR with fewer than 64 addresses fails with SERVICE_CIDR_TOO_SMALL.
//
// SC-STUB(F08 50-AC-34)
func TraefikClusterIP(serviceCIDR string) (ip netip.Addr, err error) { return }

// ClusterDNSIP is the k3s default cluster DNS address: the 10th address of the service CIDR (network address plus 10).
//
// SC-STUB(F08 50-AC-34)
func ClusterDNSIP(serviceCIDR string) (ip netip.Addr, err error) { return }

// CheckPinnedIPs derives the Traefik ClusterIP and fails with SERVICE_CIDR_IP_COLLISION if it equals any address of
// pinned (the cluster DNS IP and every other pinned ClusterIP). The set is an input so a fixture can inject one (D3).
//
// SC-STUB(F08 50-AC-34)
func CheckPinnedIPs(serviceCIDR string, pinned []netip.Addr) (traefik netip.Addr, err error) { return }

// EgressVars computes the render-time egress variables of PF-28 for cluster-vars: SC_S3_CIDR and SC_S3_PORT, present
// only when the endpoint resolves to a private address or uses a port other than 443. Such an endpoint without
// privateCIDR fails with S3_ENDPOINT_PLACEMENT_UNSUPPORTED. resolve maps a host name to its addresses; it is not
// called for an IP literal.
//
// SC-STUB(F08 50-AC-35)
func EgressVars(endpoint, privateCIDR string, resolve func(host string) ([]netip.Addr, error)) (vars map[string]string, err error) {
	return
}
