# ADR-0153: Strange Company owns the CoreDNS manifest; a rewrite sends platform public hostnames to Traefik in-cluster

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 09

## Context
In-cluster clients need the public hostnames: Forgejo, Grafana, Woodpecker and Hermes perform OIDC discovery and token exchange against `https://auth.<domain>`, Woodpecker talks to `https://git.<domain>`, and CI pushes images to the registry at `git.<domain>`. Without help these calls leave the cluster and hairpin through Cloudflare (slow, and dependent on the edge) or fail in direct mode where hairpin NAT is not guaranteed. k3s's bundled CoreDNS runs one replica with no PDB, which is a single point of failure at small-ha.

## Decision
- The k3s packaged CoreDNS manifest is suppressed with a `coredns.yaml.skip` marker, and Ansible places `sc-coredns.yaml` in the k3s manifests directory: CoreDNS image as bundled by the pinned k3s, `min(2, servers)` replicas, required hostname anti-affinity, PDB at ≥ 2 servers, `system-cluster-critical`, Service `kube-dns` at `10.43.0.10`.
- Its Corefile imports `/etc/coredns/custom/*.override` from ConfigMap `kube-system/coredns-custom`, which Flux delivers (layer `coredns-config`, generated from `public-hosts.yaml`). The override rewrites exactly the listed labels (`auth`, `git`, `ci`, `grafana`, `hermes`, `prometheus`, `alertmanager`, app hosts) under the base domain to `traefik.ingress.svc.cluster.local.` with `answer auto` (or per-host exact rules if unsupported).
- Clients connect to Traefik's ClusterIP with the public name as SNI and get the public certificate, so TLS verification is unchanged.
- Public probes and cert-manager's DNS-01 checks use public resolvers explicitly so the rewrite cannot mask edge failures.

## Consequences
- OIDC and registry traffic stays inside the cluster in both ingress modes.
- The rewrite list is GitOps-managed; CoreDNS itself is node-managed (Ansible), because the cluster needs DNS before Flux runs.
- Plans 36 and 38 must allow in-cluster clients to reach Traefik and run `blackbox-public` with `dnsPolicy: None`.

## Alternatives rejected
- `hostAliases` per pod: static IPs in every chart, breaks when the Traefik Service changes.
- Split-horizon DNS at the provider: not available or not controllable everywhere; nothing for direct mode.
- Keeping bundled CoreDNS with only `coredns-custom`: solves the rewrite but not the single replica.
- Using internal Service URLs for OIDC: the issuer URL in tokens must equal the public URL browsers use.

## Verification
P03: 09-AC-4 to 09-AC-7 (owned Deployment survives k3s restart; rewrite answers Traefik's ClusterIP with the original name; `_acme-challenge` not rewritten).
