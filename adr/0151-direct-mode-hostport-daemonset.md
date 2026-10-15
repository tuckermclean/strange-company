# ADR-0151: Direct-ingress mode runs Traefik as a hostPort DaemonSet

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 09

## Context
SPEC §23 requires a direct-ingress mode and defined behavior without Cloudflare. ADR-0010 left the mechanism to plan 09 ("host ports 80/443 or a k3s-native load-balancer replacement"). k3s ServiceLB is disabled (ADR-0010), there is no cloud load balancer on a plain VPS, and MetalLB/kube-vip need L2 or BGP features most VPS providers do not offer.

## Decision
- In direct mode the kustomize component `ingress-direct` turns Traefik into a **DaemonSet** with `hostPort: 80 → 8000` and `hostPort: 443 → 8443` (and `2222` only when Forgejo SSH is opted in). The container binds unprivileged ports; no `NET_BIND_SERVICE`, no `hostNetwork`.
- Public DNS has one A record per node public IPv4 for every host (DNS-only, no proxy). Node failure requires a DNS update (accepted limitation; documented in `lost-node.md`).
- PSA baseline forbids hostPort, so namespace `ingress` is labeled `enforce: privileged` in direct mode only. Compensating control: VAP `sc-hostport-traefik-only` (plan 37) allows hostPort only on pods of DaemonSet `traefik` in `ingress`, only values {80, 443, 2222}, and forbids `hostNetwork`, `privileged`, `hostPath` and added capabilities there. Tunnel mode keeps `ingress` at `baseline`.
- Mode switching is a Git change plus `sc-bootstrap dns apply`; the certificate is the same in both modes (ADR-0152).

## Consequences
- No extra component; hostPort DNAT bypasses the host input chain, so exposure is governed by admission, and the external port scan (plan 08) is the check.
- The master plan's `baseline` level for `ingress` holds only in tunnel mode (recorded for consolidation).

## Alternatives rejected
- MetalLB / kube-vip: need L2 adjacency or BGP; not available on typical VPS.
- `hostNetwork: true` Traefik: exposes every Traefik port on the host and widens the pod's privileges.
- Re-enabling k3s ServiceLB: k3s-managed DaemonSets outside GitOps, and NodePort-style exposure of every LoadBalancer Service.

## Verification
P04 (direct-mode fixture and installations): 09-AC-13 (DaemonSet hostPorts, PSA label, VAP denial of another hostPort), 08-AC-13 (external scan shows 22/80/443 only).
