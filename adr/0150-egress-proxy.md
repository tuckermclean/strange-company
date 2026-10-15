# ADR-0150: Workers and CI reach package registries only through a Strange Company egress proxy

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 36

## Context
SPEC §88 lets workers reach DNS, Forgejo, the Agent Runner, approved model APIs and "necessary package/source registries", and requires broader access to come from explicit task or repository policy. ADR-0011 chose flannel + kube-router, whose NetworkPolicy is L3/L4 only: it cannot express "allow `proxy.golang.org`". Registry IPs are CDN-backed and change constantly, so ipBlock allowlists are either useless or open to the whole Internet. Model APIs must stay reachable only through the Model Gateway (ADR-0004).

## Decision
- A small Go component, `egress-proxy` (`src/cmd/egress-proxy`), runs as a 2-replica Deployment in `strange-company`, delivered by the `strange-company` chart. It is an HTTP/1.1 forward proxy: `CONNECT` tunnels for TLS (no interception) and absolute-URI requests for plain HTTP to explicitly allowed port-80 hosts.
- Listener `:3128` serves `agent-jobs` and requires Basic auth `sc:<worker task token>`. The proxy validates the Runner-signed JWS (audience `sc-egress-proxy`) and its `egress_allow` claim, which the Runner computes from the platform `agent-base` profile plus the repository's `spec.network.egressHosts`, limited to platform-delegated bounds.
- Listener `:3129` serves `ci-jobs` only, unauthenticated, limited to the `ci-base` profile. Listener `:3130` serves the release lane `ci-build` only (NetworkPolicy), with its own `ci-build` profile (GitHub release hosts, Debian snapshot, scan and attestation hosts), so untrusted CI never gets those hosts (review SEC-7/GC-04). Listener `:3131` serves `dr-tests` restore testers (S3 endpoint and the GitHub black-start check only; review SEC-23), and listener `:3132` serves Renovate with its own profile (review SEC-B13), so no credential-holding batch workload has general Internet egress.
- Policy lives in `policy/egress.yaml` (kind `EgressPolicy`, `contracts/egress-policy.schema.yaml`): default deny, `forbiddenHosts` (model provider APIs) always win, IP literals are rejected, and any hostname that resolves into private, cluster, metadata or node ranges is rejected (DNS-rebinding defense).
- NetworkPolicy gives `agent-jobs` no Internet route at all; its only external path is the proxy. `ci-jobs` uses direct Internet egress from P08 until GATE-C02, because the proxy image is built by trusted CI (P11) and deployed with the chart (C01); from C02 the proxy is mandatory.

## Consequences
- FQDN-level control and per-task, per-repository allowlists without changing the CNI.
- One more critical-path component for workers and CI (fail closed: no proxy, no package downloads, failure class infrastructure).
- Exfiltration through allowed registries remains possible where a repository delegates a writable registry; that is controlled by human review of `.strange-company/policy.yaml`.

## Alternatives rejected
- Cilium FQDN policies: a CNI change with more memory and operational weight (ADR-0011).
- Squid/tinyproxy: no native verification of Runner-signed per-task tokens; per-task allowlists would need an external helper anyway.
- Open Internet egress for workers: violates SPEC §88.
- Package mirrors (Athens, Verdaccio, devpi) per ecosystem: several stateful services; may be added behind the proxy later.

## Verification
F12: 36-AC-5 (proxy unit/integration tests), 36-AC-6 (contract). C02: 36-AC-11, 36-AC-13 to 36-AC-15 (live worker matrix, allow/deny/407 behavior, CI through 3129).
