# ADR-0011: flannel `wireguard-native` with the k3s embedded network-policy controller

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13

## Context
SPEC §18 requires NetworkPolicy enforcement to stay on, and inter-node pod traffic over public VPS networks should be encrypted. A simple native k3s/flannel WireGuard configuration is preferred unless a concrete reason argues against it. OVH VPS instances generally have no private network, so node-to-node traffic crosses public IPs.

## Decision
- k3s `--flannel-backend=wireguard-native` and the embedded kube-router network-policy controller (enabled by default; must **not** be disabled with `--disable-network-policy`).
- `--node-ip` and `--node-external-ip` come from the provider's normalized node record. `--flannel-external-ip` is set when nodes have only public IPs.
- Control-plane traffic between servers (etcd peer TLS on 2379/2380, kube-apiserver on 6443, kubelet on 10250, WireGuard UDP 51820 and 51821 for IPv6) is restricted by the host firewall to peer node IPs only (plan 08).
- Every namespace gets a default-deny NetworkPolicy plus explicit allows (plan 36).

## Consequences
No extra CNI to operate. WireGuard keys are managed by flannel. etcd and the API server rely on their own TLS, so a host-firewall misconfiguration is detectable by the external port scan in gates.

## Alternatives rejected
- Cilium (with WireGuard or IPsec): more features (L7 policy, Hubble), but more memory, more moving parts on a single small VPS, and a bigger upgrade surface. Revisit post-MVP if L7 egress policy (FQDN-based model-API allowlists) proves necessary. Plan 36 records how FQDN egress is approximated without it.
- Calico: same reasoning.

## Verification
P03 gate: pod-to-pod traffic between nodes (H01) is observed as UDP 51820 only on the wire; a deny-all NetworkPolicy blocks a test connection; the external port scan matches the plan 08 allowlist.
