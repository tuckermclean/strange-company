# ADR-0010: Flux-managed Traefik in `ingress`; Cloudflare Tunnel default edge; direct-ingress alternative

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13

## Context
SPEC §20 names Traefik and Cloudflare Tunnel. SPEC §23 requires a defined direct-ingress mode and defined behavior without Cloudflare. k3s bundles Traefik through its HelmChart controller in `kube-system`, which Flux would not own.

## Decision
- Start k3s with `--disable=traefik --disable=servicelb`. Traefik is a Flux HelmRelease in namespace `ingress` (part of the seed set).
- **Tunnel mode (default):** `cloudflared` (Deployment, 2 replicas at small-ha) in `ingress` connects outbound to Cloudflare and forwards to Traefik's in-cluster Service. Nodes expose **no** public HTTP(S) ports. Cloudflare terminates public TLS. Traefik serves internal TLS with cert-manager certificates (plan 10).
- **Direct mode:** Traefik binds host ports 80/443 (or uses a k3s-native load-balancer replacement chosen in plan 09). cert-manager issues public certificates through ACME DNS-01. The firewall opens 80/443.
- The mode is a `bootstrap.env` choice, recorded in cluster config in Git. Switching modes is a documented procedure (plan 10).

## Consequences
Flux owns ingress from the first reconcile. Tunnel mode minimizes exposed surface, and Git over SSH is not exposed publicly in tunnel mode (HTTPS clone and push only).

## Alternatives rejected
- Keeping k3s bundled Traefik: it is managed by k3s and not GitOps-explainable.
- ingress-nginx: upstream ingress-nginx is retired, and SPEC names Traefik.

## Verification
P04 and P12 gates: in tunnel mode, an external scan of the node shows only SSH (restricted) plus the WireGuard and k3s ports, and those only to peer nodes; every public hostname answers through the tunnel with a valid certificate.
