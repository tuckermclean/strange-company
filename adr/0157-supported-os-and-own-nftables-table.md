# ADR-0157: Debian 13 and Ubuntu 24.04 LTS (amd64) are the supported node OSes; the host firewall is Strange Company's own nftables table

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 08

## Context
SPEC §17 requires supported distributions and a firewall policy, and SPEC §18 says nodes expose only required services. k3s (kube-proxy, kube-router, flannel) programs netfilter itself through iptables-nft. Distribution firewall tooling interferes: Debian's default `/etc/nftables.conf` starts with `flush ruleset`, which removes every Kubernetes chain when `nftables.service` restarts; ufw and firewalld rewrite chains of their own.

## Decision
- Supported: **Debian 13 (trixie)** and **Ubuntu 24.04 LTS**, amd64, kernel ≥ 6.1, cgroup v2, AppArmor enabled. Preflight refuses anything else (`NODE_OS_UNSUPPORTED`). A new release is added only after the pinned k3s and Longhorn support it and the lab suite passes; support ends 90 days before the distribution's standard support ends.
- The firewall is one table **`inet sc_host`**, loaded atomically (`table … {}` + `delete table` + definition in one `nft -f`) by `sc-firewall.service`. The input hook uses priority `filter + 10` and policy drop; the forward hook is not filtered (pod and hostPort DNAT traffic is governed by NetworkPolicy and admission). The ruleset never flushes the global ruleset. `nftables.service` is masked; ufw and firewalld must be absent or inactive.
- Allowed inbound: loopback, established/related, essential and rate-limited ICMP/ICMPv6, `cni0`/`flannel-wg` and the pod CIDR, SSH from `ssh.allowedCidrs` (10 new connections per minute per source), and from peer nodes only: 6443, 10250, 9100, 2379–2381, 51820/udp.
- A loading safety timer removes the table if SSH breaks within 120 s.

## Consequences
- k3s's own rules are never disturbed; the host filter only narrows exposure.
- DNAT'd traffic (hostPort, NodePort) bypasses the input chain, so admission must forbid NodePort/LoadBalancer Services and restrict hostPort (plan 37).

## Alternatives rejected
- ufw/firewalld: rewrite chains, conflict with kube-proxy/kube-router, not declarative enough for drift checks.
- Raw iptables rules: legacy/nft backend mismatches across distributions.
- Provider firewalls only: not uniform across providers and invisible to `existing` nodes.
- More distributions (RHEL family, SELinux): doubles the test matrix for no SPEC requirement.

## Verification
F10: 08-AC-2 (renders validate), 08-AC-4 (external scan), 08-AC-6 (lockout rollback), 08-AC-7 (restarting the firewall keeps the `KUBE-*` chains). P03: 08-AC-11, 08-AC-13.
