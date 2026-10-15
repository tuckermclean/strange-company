# 36 — Network policy and egress control

**Status:** Stage-0 plan (draft)
**DAG nodes:** F12 (co-owned with 37: the NetworkPolicy half), P03 (live enforcement probe), P04 (`policies` layer applied in the seed), C02 (live worker egress matrix and egress proxy); all listed in `plans/dag.yaml`
**Related plans:** [07](07-k3s.md), [08](08-node-security.md), [09](09-networking.md), [10](10-tls-certificates.md), [12](12-cnpg.md), [13](13-longhorn.md), [15](15-authentik.md), [16](16-forgejo.md), [18](18-flux.md), [20](20-woodpecker.md), [22](22-verification-policy.md) (RepositoryPolicy), [23](23-supply-chain.md), [24](24-hermes.md), [26](26-agent-runner.md), [27](27-worker-contract.md), [34](34-security.md), [37](37-admission-policy.md), [38](38-prometheus.md), [40](40-opentelemetry.md), [41](41-logging-tracing.md), [44](44-backup.md), [47](47-ha-expansion.md), [53](53-model-gateway.md), [54](54-strange-company-chart.md)
**Contracts:** owns `contracts/egress-policy.schema.yaml` (`policy/egress.yaml`, kind `EgressPolicy`); consumes `repo-policy.schema.yaml` (`spec.network.egressHosts`, plan 22), `gateway-token.schema.yaml` (JWS conventions, plan 53)
**ADRs:** relies on ADR-0011, ADR-0014, ADR-0015, ADR-0017, ADR-0550; introduces ADR-0150 (egress proxy)

## 1. Purpose

Define every permitted network flow inside the cluster and out of it, and deny everything else. This plan owns the per-namespace default-deny NetworkPolicies, the complete allow matrix for every component in master plan §4, the egress proxy that gives workers and CI controlled access to package registries, the egress policy file and its contract, and how all of this is tested offline in k3d and live.

## 2. Requirements derived from SPEC.md

1. Network-policy enforcement stays enabled. [SPEC §18]
2. Agent namespaces are protected by explicit NetworkPolicy. [SPEC §18]
3. The agent environment default-denies unnecessary communication. [SPEC §88]
4. Workers normally reach only DNS, Forgejo, Agent Runner, approved model APIs and necessary package/source registries. [SPEC §88]
5. Workers do not normally reach the Kubernetes API, CNPG databases, Longhorn management, Flux or production app databases. [SPEC §88]
6. Broader worker access requires explicit task/repository policy. [SPEC §88]
7. Runner validation is not the only defense; NetworkPolicy enforces independently. [SPEC §85]
8. CI verifies that workers cannot bypass network policy. [SPEC §105]
9. Hermes has no general Kubernetes access. [SPEC §4.6, §59]
10. Important policy lives in version-controlled files. [SPEC §4.7]

Derived requirements:

- **36-R-1** Every namespace that can run pods, including `kube-system`, has `default-deny` (ingress and egress) plus explicit allows. No policy allows "all ingress" or "all egress" except the documented internet egress rules (§9.4), and those always exclude private, cluster and metadata ranges.
- **36-R-2** Workers reach model providers only through the Model Gateway and package registries only through the egress proxy. Workers have no direct internet route (ADR-0004, ADR-0150).
- **36-R-3** Kubernetes API egress is allowed only to the server node IPs on 6443 and only from namespaces/pods that need it. `agent-jobs`, `ci-jobs`, `ci-build`, `hermes`, `authentik`, `apps` and `renovate` have none.
- **36-R-4** The allow matrix exists as a machine-readable file that drives both the rendered policies' test harness and the documentation in §9 (one source; drift fails CI).
- **36-R-5** Every allow policy selects at least one running pod in the live cluster once its component is deployed (catches label drift in third-party charts).

## 3. Inputs

| Input | Source | Used for |
|---|---|---|
| Namespace list, PSA and tier labels | master plan §5, ADR-0015, ADR-0550 (`monitoring-node`), plan 20 (`ci-build`, owned by plan 23) | policy targets |
| Component ports | owning plans (16, 15, 20, 26, 38, 53, …), summarized in §8.1 | port numbers |
| Server node IPs (`internal_ip`) | plan 50 writes `cluster-vars.SC_NODE_IP_1` … `SC_NODE_IP_5` (unused slots repeat `SC_NODE_IP_1`) | API server, kubelet, etcd, node-exporter ipBlocks |
| Pod / service CIDR | `cluster-vars.SC_POD_CIDR` (`10.42.0.0/16`), `SC_SERVICE_CIDR` (`10.43.0.0/16`) | private-range exclusions, webhook sources |
| Ingress mode | `cluster-vars.INGRESS_MODE` | Traefik ingress rule variant |
| `policy/egress.yaml` | Git, schema `egress-policy.schema.yaml` | egress proxy |
| `RepositoryPolicy.spec.network.egressHosts` | each repo's `.strange-company/policy.yaml` at the task base revision (plan 22, ADR-0017) | per-task proxy allow list (via the Runner) |
| Runner JWKS (public keys of `SC-RUNNER-SIGNING`) | ConfigMap `strange-company/sc-runner-jwks` (plan 26/35) | task-token validation in the proxy |

## 4. Outputs

- `gitops/platform/network-policy/` (component of layer `policies`, seed set): one directory per namespace with `default-deny.yaml`, `allow-dns.yaml` and the allow policies of §9, plus `matrix.yaml` (the machine-readable allow matrix, 36-R-4).
- Policy variants via kustomize components `ingress-tunnel` / `ingress-direct` (Traefik ingress) and `ci-egress-direct` / `ci-egress-proxy` (§9.5).
- The egress proxy (`egress-proxy`, ADR-0150): Go binary `src/cmd/egress-proxy`, image built by trusted CI, deployed by the `strange-company` chart (plan 54) in namespace `strange-company`.
- `policy/egress.yaml` (kind `EgressPolicy`) and `contracts/egress-policy.schema.yaml`.
- Test assets: `tests/netpol/` k3d connectivity harness (§22).

## 5. Dependencies

**Hard (DAG):** F12 needs F11 (rendered GitOps tree). Live checks need P03 (k3s with the kube-router policy controller) and C02 (worker Jobs).

**Soft / runtime and assumed interfaces:**
- **Plan 18:** the `policies` layer carries this plan's component. Plan 18 §8.7 must **delete** Flux's generated NetworkPolicies `allow-egress`, `allow-scraping` and `allow-webhooks` (kustomize `$patch: delete`). They are additive allow-alls (for example `allow-scraping` admits 8080 from every namespace, including `agent-jobs`), and NetworkPolicies cannot be overridden by a deny. This plan renders explicit replacements (§9.3 rows F-*). Plan 18 §5 currently says they are kept; this plan supersedes that (Q-36-01).
- **Plan 50:** writes `SC_NODE_IP_1..5`, `SC_POD_CIDR`, `SC_SERVICE_CIDR` into `cluster-vars`. **Plan 47:** updates them by PR **before** a new server joins (otherwise the `kubernetes` Service endpoints include an IP the policies do not yet allow, and API calls from pods fail intermittently).
- **Plan 26:** worker credentials follow `contracts/task-token.schema.yaml` (plan 26, RA-8). The proxy accepts only the **egress token**: `token_use: egress`, `aud: [sc-egress-proxy]`, delivered to the `harness` container only as file `egress-token`; the runner token (`token_use: runner`, `aud: [sc-agent-runner-worker]`) is never accepted here, and the egress token is never accepted by the Runner. Claim `egress_allow` uses `egress-policy.schema.yaml#/$defs/EgressAllowClaim`, computed at dispatch from `agent-base` ∪ the repository's delegated `egressHosts` (only entries inside `delegation.repositoryAllowed`); `execution_id` is present. The public JWKS ConfigMap `strange-company/sc-runner-jwks` is **Git-rendered** from plan 35's committed JWKS (SEC-14); the Runner never writes it.
- **Plan 27:** the `harness` container builds `HTTPS_PROXY`/`HTTP_PROXY=http://sc:<egress token>@egress-proxy.strange-company.svc.cluster.local:3128` from the mounted token file at runtime (never as a pod-spec env value) and `NO_PROXY=.svc,.svc.cluster.local,10.42.0.0/16,10.43.0.0/16`.
- **Plan 20:** Woodpecker sets `HTTPS_PROXY`/`HTTP_PROXY=http://egress-proxy.strange-company.svc.cluster.local:3129` and the same `NO_PROXY` for every step pod (`WOODPECKER_ENVIRONMENT`, verify) once `ci-egress-proxy` is active (§9.5).
- **Plan 22:** `RepositoryPolicy.spec.network.egressHosts: [HostPatternWithPort]` (max 20).
- **Plan 38:** each scrape target's port as in §8.1; `blackbox-public` uses public resolvers (plan 09).
- **Plan 37:** VAPs that make the selectors trustworthy: only the Runner creates pods in `agent-jobs` (so `app.kubernetes.io/managed-by=agent-runner` cannot be forged by others), only Woodpecker creates pods in `ci-jobs`.

## 6. Owned state

| State | Location | System of record | Writers |
|---|---|---|---|
| NetworkPolicies | `gitops/platform/network-policy/**` | Git | PR |
| Allow matrix | `gitops/platform/network-policy/matrix.yaml` | Git | PR |
| Egress policy | `policy/egress.yaml` | Git (protected path, ADR-0008) | PR, human merge |
| Egress decisions | egress-proxy logs (pod logs; Loki at small-ha) and metrics | non-authoritative telemetry | egress-proxy |

## 7. External interfaces

| Interface | Consumer | Definition |
|---|---|---|
| `egress-proxy.strange-company.svc.cluster.local:3128` (HTTP proxy, Basic auth `sc:<task token>`) | worker Jobs (`agent-jobs`) | §9.9 |
| `egress-proxy.strange-company.svc.cluster.local:3129` (HTTP proxy, no auth, `ci-base` only) | Woodpecker step pods (`ci-jobs`) | §9.9 |
| Deny response | harnesses, `sc-worker`, CI logs | HTTP 403, header `X-SC-Egress-Deny: <reason>`, JSON body `{"decision":"deny","reason":"<reason>","host":"<host>","port":<n>}`; 407 for missing/invalid credentials |
| `policy/egress.yaml` | egress proxy, Runner (claim computation), plan 22 loader (bounds check) | `contracts/egress-policy.schema.yaml` |
| Policy labels | every component plan | §8.2 |
| Metrics `:9090/metrics` (container port named `metrics`, CD-02) | Prometheus (plan 38 selects it by name) | §21 |

## 8. Internal interfaces

### 8.1 Port reference (pod ports after Service DNAT)

| Component (namespace) | Pod label selector (verify against pinned charts; 36-AC-12 catches drift) | Ports |
|---|---|---|
| CoreDNS (`kube-system`) | `k8s-app=kube-dns` | 53/udp, 53/tcp, 9153 metrics |
| metrics-server (`kube-system`) | `k8s-app=metrics-server` | 10250 (verify on k3s) |
| local-path-provisioner, `heavy-slot-advertiser`, kured (`kube-system`) | `app=local-path-provisioner`, `app.kubernetes.io/name=heavy-slot-advertiser`, `app.kubernetes.io/name=kured` | kured metrics 8080 |
| Kubernetes API (host) | ipBlock `SC_NODE_IP_*/32` | 6443 |
| kubelet, node-exporter, etcd metrics (host) | ipBlock `SC_NODE_IP_*/32` | 10250, 9100, 2381 |
| Flux (`flux-system`) | `app in (source-controller, kustomize-controller, helm-controller, notification-controller)` | source-controller artifacts 9090; metrics 8080 |
| cert-manager (`cert-manager`) | `app.kubernetes.io/name in (cert-manager, webhook, cainjector)` | controller metrics 9402; webhook 10250 |
| CNPG operator, barman-cloud plugin (`cnpg-system`) | `app.kubernetes.io/name=cloudnative-pg`, `app.kubernetes.io/name=plugin-barman-cloud` | webhook 9443, metrics 8080, plugin gRPC 9090 |
| CNPG instances (`forgejo`, `authentik`, `woodpecker`, `strange-company`, `dr-tests`) | `cnpg.io/podRole=instance`, `cnpg.io/cluster=<name>` | 5432, status 8000, metrics 9187 |
| Longhorn (`longhorn-system`) | namespace-wide | manager 9500, webhooks 9501/9502 (verify), data plane dynamic |
| Traefik (`ingress`) | `app.kubernetes.io/name=traefik` | 8000, 8443, 9100, 2222 (direct opt-in) |
| cloudflared (`ingress`) | `app.kubernetes.io/name=cloudflared` | 2000 |
| Forgejo (`forgejo`) | `app.kubernetes.io/name=forgejo` | 3000 http, 2222 ssh |
| repo-backup CronJobs, mirror watch (`forgejo`) | `app.kubernetes.io/name in (repo-backup, github-mirror-watch)` | mirror watch metrics 9464 |
| Authentik (`authentik`) | `app.kubernetes.io/name=authentik`, `app.kubernetes.io/component in (server, worker)` | 9000, 9443, 9300 metrics |
| Woodpecker (`woodpecker`) | `app.kubernetes.io/name=woodpecker`, `app.kubernetes.io/component in (server, agent)`; `app.kubernetes.io/name in (sc-ci-config, sc-ci-maint)` | server 8000 http, 9000 gRPC, 9001 metrics; `sc-ci-config` 8080, metrics 9464 (plan 20) |
| Woodpecker step pods (`ci-jobs`, `ci-build`) | namespace-wide (`app.kubernetes.io/managed-by=woodpecker`) | — |
| Prometheus stack (`monitoring`) | `app.kubernetes.io/name in (prometheus, alertmanager, kube-state-metrics, prometheus-operator, blackbox-exporter, grafana, opentelemetry-collector, platform-probes, loki, tempo)`; `blackbox-public` | 9090, 9093, 8080, 10250 (operator webhook, verify), 9115, 3000, 4317/4318/8888/8889, 9102, 3100, 3200 |
| otel-agent (`monitoring-node`, H03) | `app.kubernetes.io/name=otel-agent` | 8888 |
| Agent Runner (`strange-company`) | `app.kubernetes.io/name=agent-runner` | 8081 MCP, 8082 worker, 8083 hooks/CI intents (CD-03), `metrics` (8090) |
| Model Gateway (`strange-company`) | `app.kubernetes.io/name=model-gateway` | 8080 API; `metrics` (9090, also serves `GET /sc/v1/status`) |
| Egress proxy (`strange-company`) | `app.kubernetes.io/name=egress-proxy` | 3128, 3129, `metrics` (9090) |
| Hermes (`hermes`) | `app.kubernetes.io/name=hermes` | 9119 dashboard, 8642 gateway API |
| Worker Jobs (`agent-jobs`) | `app.kubernetes.io/managed-by=agent-runner` | none inbound |
| Renovate (`renovate`), restore tests (`dr-tests`) | namespace-wide | none inbound |

Metrics ports of Strange Company services are selected **by name** (`port: metrics`) per CD-02; the numbers are the owning plans' (Runner 8090, Gateway 9090, egress-proxy 9090). kube-router supports named ports (verify, 36-AC-3).

### 8.2 Label conventions this plan relies on

- Namespace labels `kubernetes.io/metadata.name` (automatic) are used for every `namespaceSelector`.
- Pods in `agent-jobs` carry `app.kubernetes.io/managed-by=agent-runner` (plan 26 Job builder); plan 37 guarantees only the Runner can create them.
- "Internet" means `ipBlock: {cidr: 0.0.0.0/0, except: [10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16, 172.16.0.0/12, 192.168.0.0/16, 224.0.0.0/4, 240.0.0.0/4, ${SC_NODE_IP_1}/32 … ${SC_NODE_IP_5}/32]}`. The pod and service CIDRs fall inside `10.0.0.0/8`. Rendered once as a kustomize component `internet-egress` and referenced by name in the matrix.
- "S3" (SEED-3) means "Internet" on TCP 443 **plus**, when the object-storage endpoint is private or not on 443, the component `s3-egress`: `ipBlock {cidr: ${SC_S3_CIDR}}` on port `${SC_S3_PORT}`. Plan 50 writes `SC_S3_CIDR`/`SC_S3_PORT` into `cluster-vars` at render time from `S3_ENDPOINT` (a private or non-443 endpoint requires the operator to supply `S3_PRIVATE_CIDR`; preflight fails otherwise); for a public 443 endpoint the component is not included. Rows F-1, N-5, L-2, FJ-2, FJ-6, SC-6, SC-10, MO-13, MO-8 (object-storage probe) and D-1 use "S3" wherever they say "S3". The k3d/CI harness sets `SC_S3_CIDR` to the `s3-emulator` network (CD-12).
- "DNS upstream" (SEED-3) means "Internet" **plus** `ipBlock` `${SC_DNS_UPSTREAM_1..3}/32` (the nameservers in the nodes' effective resolv.conf, recorded by plan 07's `sc-node-facts` and written to `cluster-vars` by plan 50; unused slots repeat slot 1), so private resolvers on `existing` nodes keep working after the `policies` layer applies.
- "Nodes" means `ipBlock` `${SC_NODE_IP_n}/32` for n = 1..5.
- "Webhook sources" means "Nodes" plus `${SC_POD_CIDR}` (the API server reaches webhooks on other nodes from its flannel address, which is inside the pod CIDR).

## 9. Configuration

### 9.1 Baseline policies in every namespace

| Policy | Spec |
|---|---|
| `default-deny` | `podSelector: {}`, `policyTypes: [Ingress, Egress]`, no rules |
| `allow-dns` | egress to `kube-system` pods `k8s-app=kube-dns` on 53/udp and 53/tcp (every namespace except `kube-system`, which has its own rule K-1) |

kube-router accepts traffic whose source is the local node (`--src-type LOCAL`), so kubelet probes and same-node API server → webhook calls pass without a rule (verify, 36-AC-4).

### 9.2 Ingress-side summary (who may connect *into* each namespace)

| Namespace | Allowed ingress (all others denied) |
|---|---|
| `kube-system` | CoreDNS 53 from every namespace; CoreDNS 9153, kured 8080 from Prometheus; metrics-server 10250 from webhook sources |
| `flux-system` | source-controller 9090 from kustomize/helm controllers; 8080 from Prometheus |
| `cert-manager` | webhook 10250 from webhook sources; 9402 from Prometheus |
| `cnpg-system` | operator webhook 9443 from webhook sources; plugin 9090 from operator and CNPG instance pods; 8080 from Prometheus |
| `longhorn-system` | intra-namespace all ports; webhooks 9501/9502 from webhook sources; 9500 from Prometheus |
| `ingress` | Traefik per §9.6; cloudflared 2000 and Traefik 9100 from Prometheus |
| `forgejo` | per matrix rows FJ-* |
| `authentik` | per AU-* |
| `woodpecker` | per WP-* |
| `ci-jobs`, `ci-build` | intra-namespace only (step ↔ service containers) |
| `monitoring` | per MO-* |
| `monitoring-node` | otel-agent 8888 from Prometheus |
| `strange-company` | per SC-* |
| `hermes` | per HE-* |
| `agent-jobs` | **none** |
| `renovate` | none (egress only through R-1) |
| `dr-tests` | intra-namespace (scratch CNPG clusters); 9187 from Prometheus |
| `apps` | Traefik on any port; intra-namespace; per-app additions by PR in `gitops/apps/<app>/` |

### 9.3 Complete allow matrix

Each row is rendered as an egress rule in the source namespace **and** an ingress rule in the destination namespace (both sides, so a single mistake does not open a path). "Prom" = `monitoring` pods `app.kubernetes.io/name=prometheus`. Ports are TCP unless marked.

**Cluster services and control plane**

| ID | From | To | Port | Purpose |
|---|---|---|---|---|
| K-1 | every namespace | `kube-system` CoreDNS | 53/udp, 53/tcp | DNS |
| K-2 | `kube-system` CoreDNS | DNS upstream + `SC_NODE_IP_*` | 53/udp, 53/tcp | upstream resolution via the node's resolv.conf servers (SEED-3) |
| K-3 | `kube-system` (CoreDNS, local-path-provisioner, metrics-server, heavy-slot-advertiser, kured) | Nodes | 6443 | API |
| K-4 | `kube-system` metrics-server | Nodes | 10250 | kubelet metrics |
| K-5 | Webhook sources | `kube-system` metrics-server | 10250 | aggregated API |
| API-1 | pods listed in §9.7 | Nodes | 6443 | Kubernetes API (36-R-3) |
| API-2 | pods labeled `strange.company/upgrade-gate=true` in every gated namespace (`forgejo`, `longhorn-system`, `authentik`, `woodpecker`, `strange-company`, `monitoring`, `hermes`) | Nodes | 6443 | upgrade-gate Jobs (plan 48 §8.3; SEED-2: the `forgejo-upgrade-gate` seed layer deadlocks without it) |

**Flux**

| ID | From | To | Port | Purpose |
|---|---|---|---|---|
| F-1 | `flux-system` source-controller | Internet; S3 | 443; S3 port | chart registries (OCI/HTTP); S3 `Bucket` source during seed |
| F-2 | `flux-system` source-controller | `forgejo` Forgejo | 2222 | Git over SSH after control transfer |
| F-3 | `flux-system` kustomize/helm controllers | `flux-system` source-controller | 9090 | artifact download |
| F-4 | Prom | `flux-system` controllers | 8080 | metrics |

**Certificates, operators, storage**

| ID | From | To | Port | Purpose |
|---|---|---|---|---|
| C-1 | `cert-manager` controller | Internet | 443 | ACME, DNS provider API |
| C-2 | `cert-manager` controller | ipBlock `1.1.1.1/32`, `9.9.9.9/32` | 53/udp, 53/tcp | DNS-01 propagation checks (plan 10) |
| C-3 | Webhook sources | `cert-manager` webhook | 10250 | admission |
| C-4 | Prom | `cert-manager` controller | 9402 | metrics |
| N-1 | Webhook sources | `cnpg-system` operator | 9443 | admission |
| N-2 | `cnpg-system` operator | CNPG instances (all DB namespaces) | 8000, 5432 | instance status, management |
| N-3 | `cnpg-system` operator, CNPG instances | `cnpg-system` plugin | 9090 | barman-cloud plugin gRPC |
| N-4 | CNPG instances | same cluster's instances (`cnpg.io/cluster` match) | 5432, 8000 | replication, status |
| N-5 | CNPG instances, `cnpg-system` plugin | S3 | S3 port | WAL archive and base backups |
| N-6 | Prom | CNPG instances; `cnpg-system` operator | 9187; 8080 | metrics |
| L-1 | `longhorn-system` | `longhorn-system` | all | Longhorn manager/engine/replica/CSI data plane |
| L-2 | `longhorn-system` | S3 | S3 port | backup target |
| L-3 | Webhook sources | `longhorn-system` | 9501, 9502 | admission/conversion webhooks (verify ports) |
| L-4 | Prom | `longhorn-system` manager | 9500 | metrics |

**Ingress and edge** (Traefik ingress variants in §9.6)

| ID | From | To | Port | Purpose |
|---|---|---|---|---|
| I-1 | `ingress` cloudflared | Internet | 7844/udp, 7844/tcp, 443 | Cloudflare edge (tunnel mode only) |
| I-2 | `ingress` cloudflared | `ingress` Traefik | 8443 | origin requests |
| I-3 | `ingress` Traefik | `forgejo` Forgejo | 3000 (+ 2222 direct opt-in) | `git.` route (+ SSH entrypoint) |
| I-4 | `ingress` Traefik | `authentik` server | 9000 | `auth.` route and forward-auth outpost |
| I-5 | `ingress` Traefik | `woodpecker` server | 8000 | `ci.` route |
| I-6 | `ingress` Traefik | `monitoring` Grafana, Prometheus, Alertmanager | 3000, 9090, 9093 | `grafana.`, `prometheus.`, `alertmanager.` routes |
| I-7 | `ingress` Traefik | `hermes` Hermes | 9119 | `hermes.` route |
| I-8 | `ingress` Traefik | `apps` | any | app routes |
| I-9 | `ingress` Traefik | `ingress` cert-manager HTTP-01 solver pods (`acme.cert-manager.io/http01-solver=true`) | 8089 | direct mode with `http01` only |
| I-10a | In-cluster OIDC clients of `auth.` only: `forgejo` (Forgejo), `authentik`, `monitoring` (Grafana, platform-probes), `hermes` | `ingress` Traefik | 8444 (`oidc` entrypoint, Service `traefik-oidc`) | `auth.<domain>` resolved by the CoreDNS rewrite; the `oidc` entrypoint routes only Authentik (plan 09 §9.1; SEC-16) |
| I-10b | Clients that need other public hosts: `woodpecker` server (`git.` OAuth/API), `monitoring` platform-probes (`git.` checks) | `ingress` Traefik | 8443 | all public hostnames. `hermes` and `ci-jobs` are **not** listed (SEC-16, plan 24 24-AC-7); protected builds reach the registry from `ci-build` (CB-2) |
| I-11 | Prom | `ingress` Traefik; cloudflared | 9100; 2000 | metrics |

**Forgejo**

| ID | From | To | Port | Purpose |
|---|---|---|---|---|
| FJ-1 | `forgejo` Forgejo | `forgejo` `forgejo-pg` instances | 5432 | database |
| FJ-2 | `forgejo` Forgejo | Internet | TCP 443 | S3 objects/LFS/packages; push mirror to GitHub (plan 17) |
| FJ-3 | `forgejo` Forgejo | `strange-company` agent-runner | 8083 | webhooks to the Runner (hooks listener, CD-03) |
| FJ-4 | `forgejo` Forgejo | `woodpecker` server | 8000 | Woodpecker webhooks |
| FJ-5 | `forgejo` Forgejo | Internet | 587, 465 | SMTP (only if a mail transport is configured) |
| FJ-6 | `forgejo` repo-backup | `forgejo` Forgejo; S3 | 3000, 2222; S3 port | bundle creation (plan 44), upload |
| FJ-7 | `forgejo` github-mirror-watch | Internet | 443 | `api.github.com` (plan 17) |
| FJ-8 | `strange-company` agent-runner | `forgejo` Forgejo | 3000 | Runner API calls (PRs, statuses, deploy keys) |
| FJ-9 | `woodpecker` server | `forgejo` Forgejo | 3000 | OAuth token exchange, API |
| FJ-10 | `ci-jobs` | `forgejo` Forgejo | **3000 only** | clone over HTTP with the Woodpecker netrc. No SSH (2222): submodule recursion and LFS are forced off by `sc-ci-config` (SEC-B4, plan 20) |
| FJ-11 | `renovate` | `forgejo` Forgejo | 3000 | PRs |
| FJ-12 | `monitoring` Prom, blackbox, platform-probes | `forgejo` Forgejo | 3000, 2222 | metrics, health and SSH probes |
| FJ-13 | `dr-tests` | `forgejo` Forgejo | 3000 | repository restore tests (plan 44) |
| FJ-14 | Prom | `forgejo` mirror watch | `metrics` | metrics |

**Authentik**

| ID | From | To | Port | Purpose |
|---|---|---|---|---|
| AU-1 | `authentik` server, worker | `authentik` `authentik-pg` instances | 5432 | database |
| AU-2 | `authentik` worker | Internet | 587, 465 | SMTP (if configured) |
| AU-3 | `monitoring` platform-probes, `authentik` config-check Job | `authentik` server | 9000 | probes, readiness check (plan 15) |
| AU-4 | Prom | `authentik` server | 9300 | metrics |

**Woodpecker and CI**

| ID | From | To | Port | Purpose |
|---|---|---|---|---|
| WP-1 | `woodpecker` server | `woodpecker` `woodpecker-pg` instances | 5432 | database |
| WP-2 | `woodpecker` agent | `woodpecker` server | 9000 | gRPC |
| WP-3 | `woodpecker` agent | Nodes | 6443 | Kubernetes backend creates step pods in `ci-jobs` (no pod-network path to `ci-jobs` is needed: logs and status go through the API) |
| WP-4 | `strange-company` agent-runner | `woodpecker` server | 8000 | trigger pipelines, pull evidence (ADR-0008) |
| WP-5 | Prom | `woodpecker` server; `sc-ci-config` | 9001; `metrics` | metrics (plan 20 dedicated listener) |
| WP-6 | `woodpecker` server | `woodpecker` `sc-ci-config` | 8080 | configuration extension (ADR-0301) |
| WP-7 | `woodpecker` `sc-ci-config` | `forgejo` Forgejo; `strange-company` agent-runner; `woodpecker` server | 3000; 8083; 8000 | trusted config fetch; verification intents and `GET /v1/ci/push-records` audit (plan 20 §8.2, SEC-1); Woodpecker API including the 60 s agent audit loop |
| WP-8 | `woodpecker` `sc-ci-maint` | Nodes; `woodpecker` server | 6443; 8000 | CI garbage collection (plan 20) |
| WP-9 | `woodpecker` agent-build | Nodes | 6443 | build lane creates pods in `ci-build` |
| CI-1 | `ci-jobs` | `ci-jobs` | all | step ↔ service containers |
| CI-2 | `ci-jobs` | `strange-company` egress-proxy | 3129 | packages (`ci-egress-proxy` mode, §9.5) |
| CI-3 | `ci-jobs` | Internet | 443, 80 | packages (`ci-egress-direct` mode only, before C02) |
| CB-1 | `ci-build` | `ci-build` | all | step ↔ service containers (release lane, plan 23) |
| CB-2 | `ci-build` | `forgejo` Forgejo; `ingress` Traefik | 3000, 2222; 8443 | clone; OCI registry push to `git.<domain>` (plan 23) |
| CB-3 | `ci-build` | `strange-company` egress-proxy | 3130 | base images, packages and release hosts through the `build` listener (`ci-build` profile; `proxy` mode; SEC-7/GC-04) |
| CB-5 | `ci-build` | `strange-company` agent-runner | 8083 | `GET /v1/ci/push-records` (SEC-1; the `woodpecker` side is WP-7: `sc-ci-config` → 8083) |
| CB-4 | `ci-build` | Internet | 443, 80 | `ci-egress-direct` mode only; plan 23 MAY narrow further |

**Monitoring**

| ID | From | To | Port | Purpose |
|---|---|---|---|---|
| MO-1 | Prom | Nodes | 10250, 9100, 2381, 6443 | kubelet/cAdvisor, node-exporter, etcd, apiserver |
| MO-2 | Prom | `monitoring` (KSM 8080, Alertmanager 9093, operator 8080, blackbox 9115, Grafana 3000, OTel 8888/8889, platform-probes 9102, Loki 3100, Tempo 3200) | listed | scrapes, alert delivery |
| MO-3 | `monitoring` Alertmanager | Internet | 443, 587, 465 | notification transports (webhook/ntfy/Slack/Discord/Matrix bridge, SMTP) and the heartbeat URL (ADR-0009, plan 42) |
| MO-4 | `strange-company` agent-runner | `monitoring` Alertmanager | 9093 | Runner-posted alerts (ADR-0009) |
| MO-5 | `ingress` Traefik | `monitoring` Grafana/Prometheus/Alertmanager | I-6 | UI routes |
| MO-6 | `monitoring` Grafana | `monitoring` Prometheus, Loki, Tempo | 9090, 3100, 3200 | data sources |
| MO-7 | `monitoring` Grafana | `strange-company` `sc-pg` instances (`cnpg.io/cluster=sc-pg`, via Service `sc-pg-r`) | 5432 | `grafana_ro` read-only role (plan 39 §13, plan 12) |
| MO-8 | `monitoring` blackbox (in-cluster) | `forgejo` 3000/2222, `hermes` 8642/9119, `authentik` server 9000, `woodpecker` server 8000, `monitoring` Grafana 3000, `strange-company` agent-runner and model-gateway `metrics` port (`/readyz`), `kube-system` CoreDNS 53, S3 | listed | every Probe CR target of plan 38 §7.8 (GC-03), DNS-rewrite probe, object-storage probe |
| MO-8a | `monitoring` pods labeled `app.kubernetes.io/name=sc-verify-otel` (plan 40 V-23 probe) | `monitoring` OTel Collector | 4317 | synthetic span (GC-18) |
| MO-9 | `monitoring` blackbox-public | Internet; ipBlock `1.1.1.1/32`, `9.9.9.9/32` | 443; 53/udp+tcp | public edge probes (plan 09) |
| MO-10 | `strange-company` agent-runner, model-gateway | `monitoring` OTel Collector (`otel-collector`) | 4317 | OTLP/gRPC spans (ADR-0551, plan 40 §13) |
| MO-11 | `monitoring-node` otel-agent (H03) | `monitoring` Loki | 3100 | logs |
| MO-12 | `monitoring` OTel Collector | `monitoring` Tempo (H03) | 4317 | traces |
| MO-12a | `monitoring` OTel Collector (`otel-collector` only, not `otel-collector-workers`) | Nodes | 6443 | `k8sattributes` processor (plan 40); no Internet egress |
| MO-12c | `monitoring` `otel-collector-workers` | `monitoring` `otel-collector` | 4317 | forwards worker spans after its own privacy pipeline; this is its **only** egress besides DNS (SEC-B5: a hostile worker that compromises this receiver reaches no API and no pod read) |
| MO-12b | Prom | `monitoring` OTel Collector | 8888, 8889, 13133 | self-metrics, span metrics, health (plan 40) |
| MO-13 | `monitoring` Loki, Tempo | S3 | S3 port | `telemetry` bucket |
| MO-14 | `monitoring` platform-probes | `forgejo` 3000/2222, `authentik` 9000, Internet 443 (`api.github.com`, plan 17 mirror module) | listed | plan 15 §21.2 |
| MO-16 | `monitoring` `silence-audit`, (H03) Loki and Tempo rulers | `monitoring` Alertmanager | 9093 | plan 42 §5 ingress list. **Not** Grafana: its Alertmanager datasource was removed (AUTH-8, plan 39), so Grafana cannot create silences |
| MO-17 | Prom, `monitoring` Grafana | `monitoring` Loki, Tempo (H03) | 3100; 3200 | scrapes and data sources (plan 41) |
| MO-15 | Prom | every Strange Company service and Hermes | `metrics` | scrapes (plan 38 §7.2) |

**Strange Company control plane**

| ID | From | To | Port | Purpose |
|---|---|---|---|---|
| SC-1 | `hermes` Hermes | `strange-company` agent-runner | 8081 | MCP (ADR-0003) |
| SC-2 | `hermes` Hermes | `strange-company` model-gateway | 8080 | model calls (ADR-0004) |
| SC-3 | `strange-company` agent-runner | `strange-company` `sc-pg` | 5432 | task state |
| SC-4 | `strange-company` model-gateway | `strange-company` `sc-pg` | 5432 | ledger |
| SC-5 | `strange-company` agent-runner | `strange-company` model-gateway | 9090 (`metrics` port) | `GET /sc/v1/status` (plan 53 §7; the Runner calls no other Gateway endpoint) |
| SC-5a | `strange-company` model-gateway | Nodes | 6443 | GET ConfigMap `sc-emergency-stop` (plan 53 RBAC; plan 33 §14.6) |
| SC-6 | `strange-company` agent-runner | S3 | S3 port | `evidence` bucket |
| SC-7 | `strange-company` model-gateway | Internet | 443 | provider APIs (the Gateway pins upstream hosts itself, plan 53 53-R-7) |
| SC-8 | `strange-company` egress-proxy | Internet | 443, 80 | proxied package traffic (policy-checked, §9.9) |
| SC-9 | `strange-company` agent-runner | `hermes` Hermes | 8642 | Hermes gateway API (consumer of `SC-HERMES-API`; plan 24 confirms) |
| SC-10 | `strange-company` `sc-pg` instances | Internet | 443 | WAL/base backups (= N-5) |
| SC-11 | `dr-tests` restore-tester | `strange-company`, `forgejo`, `authentik`, `woodpecker` CNPG `*-rw` | 5432 | restore verification (plan 12) |
| HE-1 | `ingress` Traefik | `hermes` | 9119 | dashboard (= I-7) |
| HE-2 | `hermes` | `ingress` Traefik | 8444 only | dashboard OIDC discovery/token/JWKS calls to `auth.` (= I-10a); `git.`, `ci.` and every other host behind Traefik are unreachable from Hermes (plan 24 §13, SEC-16) |

**Workers** (`agent-jobs`, pods `app.kubernetes.io/managed-by=agent-runner`)

| ID | From | To | Port | Purpose |
|---|---|---|---|---|
| W-1 | `agent-jobs` | `forgejo` Forgejo | **2222 only** | clone/push with the per-execution deploy key (ADR-0007). Workers have no HTTP path to Forgejo's web/API surface (SEC-B10; LFS smudge is disabled in workers, plan 27) |
| W-2 | `agent-jobs` | `strange-company` agent-runner | 8082 | task packet, result, heartbeat |
| W-3 | `agent-jobs` | `strange-company` model-gateway | 8080 | model calls |
| W-4 | `agent-jobs` | `strange-company` egress-proxy | 3128 | package/source registries (authenticated) |
| W-5 | `agent-jobs` pods labeled `app.kubernetes.io/name=agent-worker` | `monitoring` pods `app.kubernetes.io/name=otel-collector-workers` (a **separate** Collector Deployment with `automountServiceAccountToken: false`, no `k8sattributes`, no API egress; plan 40) via Service 4318 → pod port 4319 | 4319 | OTLP/HTTP spans (plan 40 §13 explicit exception to SPEC §88's list; the only monitoring endpoint workers reach) |
| — | `agent-jobs` | **anything else** (API server, every CNPG instance, Longhorn, Flux, Woodpecker, Hermes, Alertmanager, Traefik, internet) | — | denied [SPEC §88] |

**Other**

| ID | From | To | Port | Purpose |
|---|---|---|---|---|
| R-1 | `renovate` | `strange-company` egress-proxy | 3132 | upstream registries, chart repositories and release metadata through the `renovate` listener and profile (SEC-B13; supersedes GC-04's permanent direct egress). No direct Internet. |
| D-1 | `dr-tests` scratch CNPG instance pods (`cnpg.io/podRole=instance`) | S3 | S3 port | barman restore of scratch clusters (they hold only the scoped backup read credential) |
| D-1a | `dr-tests` restore-tester and check pods (every pod that is not a scratch instance or scratch app) | `strange-company` egress-proxy | 3131 | S3 endpoint and, for pods labeled `strange.company/check=restore-test-github-black-start`, `github.com`/`api.github.com`, through the `dr` listener and `dr-tests` profile (SEC-23: credential-rich pods processing restored data get no general Internet egress) |
| D-2 | `dr-tests` | `dr-tests` | all | scratch CNPG clusters |
| AP-1 | `apps` | `apps` | all | intra-namespace |
| KU-1 | `kube-system` kured | Nodes | 6443 | drain/lock (small-ha) |

### 9.4 Internet egress holders (complete list)

Only these pods have an Internet rule: CoreDNS (53), Flux source-controller, cert-manager controller, CNPG instances and plugin, Longhorn, cloudflared, Forgejo (+ repo-backup, mirror watch), Authentik worker (SMTP), Alertmanager, blackbox, blackbox-public, platform-probes, Loki, Tempo, agent-runner, model-gateway, egress-proxy, `dr-tests` scratch CNPG instances (S3 only), and `ci-jobs`/`ci-build` only in `ci-egress-direct` mode. Plan 20 §13.3 assumed permanent direct `0.0.0.0/0:443` egress for `ci-jobs`; §9.5 supersedes it from GATE-C02. Plan 53's statement that the Gateway is the only internet-egress workload "among `agent-jobs`, `hermes` and `strange-company`" is refined: within `strange-company`, the Runner (S3 only, not FQDN-enforceable) and the egress proxy also hold it (Q-36-04). `agent-jobs` and `hermes` never do.

### 9.5 CI egress modes

| Mode (`cluster-vars.SC_CI_EGRESS`) | When | ci-jobs egress |
|---|---|---|
| `direct` | P08 (Woodpecker cutover) until GATE-C02 | rows CI-1, CI-3, CB-1, CB-2, CB-4, FJ-10, I-10a/b: direct Internet 443/80. Only human-authored code runs in CI in this period, and the egress-proxy image does not exist yet (it is built by trusted CI at P11 and deployed with the chart at C01). |
| `proxy` | from GATE-C02, permanently | rows CI-1, CI-2, CB-1..CB-3, FJ-10, I-10a/b; Internet denied |

GATE-C02 requires `proxy` (36-AC-15). F11 check `ci-egress-mode` fails if `SC_CI_EGRESS=direct` in a cluster directory whose `runner.enabled` value is true, **except** when the commit carries the trailer `SC-Emergency-Merge: <incident-id>` (plan 55, `procedures/emergency-merge.md`; NC-2) (GC-02): a broken egress-proxy release would otherwise block the CI run of its own revert. Such a commit is merged through plan 55's `emergency-merge.md` and must be followed within 24 h by a PR restoring `proxy`. This transition is a deviation from the G2 brief, which assumed the proxy at P08 (Q-36-02).

### 9.6 Traefik ingress variants

| Variant | Traefik ingress allowed from |
|---|---|
| `ingress-tunnel` | `ingress` cloudflared → 8443; I-10b clients → 8443; I-10a clients → 8444; Prom → 9100 |
| `ingress-direct` | Internet (`ipBlock 0.0.0.0/0`, no except: hostPort DNAT keeps the client source address) → 8000, 8443, and 2222 if `FORGEJO_SSH_PUBLIC`; I-10b clients → 8443; I-10a clients → 8444; Prom → 9100 |

### 9.7 Kubernetes API access (36-R-3)

API egress (row API-1) is rendered for exactly: `kube-system` (K-3), `flux-system` (all controllers), `cert-manager` (all three), `cnpg-system` (operator, plugin), CNPG instance pods in `forgejo`, `authentik`, `woodpecker`, `strange-company`, `dr-tests` (the instance manager watches its `Cluster`), `longhorn-system`, `ingress` (Traefik CRD/Ingress providers), `monitoring` (Prometheus service discovery, operator, KSM, Grafana sidecar if enabled, OTel Collector `k8sattributes`; **never** `otel-collector-workers`, SEC-B5), `monitoring-node` (otel-agent k8sattributes, H03), `strange-company` (agent-runner, model-gateway per plan 53), `woodpecker` (both agents, `sc-ci-maint`), `dr-tests` (restore-test orchestration), and upgrade-gate Job pods in gated namespaces (API-2). **Never** `agent-jobs`, `ci-jobs`, `ci-build`, `hermes`, `authentik` (non-instance, non-gate pods), `forgejo` (non-instance, non-gate pods), `renovate`, `apps`.

### 9.8 Knobs

| Knob | Git path | Default | Who may change |
|---|---|---|---|
| `SC_NODE_IP_1..5`, `SC_POD_CIDR`, `SC_SERVICE_CIDR` | `cluster-vars` | from provider output | plan 50/47 only |
| `SC_CI_EGRESS` | `cluster-vars` | `direct` at install, `proxy` from C02 | PR |
| Allow matrix | `gitops/platform/network-policy/matrix.yaml` | §9.3 | PR (security review label required, plan 55) |
| Egress policy | `policy/egress.yaml` | §9.9.3 | PR, human merge (protected path) |

### 9.9 Egress proxy (ADR-0150)

#### 9.9.1 Behavior

- **Protocol:** HTTP/1.1 forward proxy. `CONNECT host:port` tunnels TLS end to end (no interception, no CA injection). Absolute-form `http://` requests are proxied only to patterns that list port 80. Other methods to the proxy itself return 405. `Upgrade`/WebSocket inside CONNECT tunnels is opaque and therefore allowed.
- **Listeners:** `:3132` renovate (no auth, `renovate` profile, reachable only from `renovate`; SEC-B13); `:3131` dr (no auth, `dr-tests` profile, reachable only from `dr-tests`; SEC-23); `:3128` agent (auth required), `:3129` ci (no auth, `ci-base` only, reachable only from `ci-jobs`), `:3130` build (no auth, `ci-build` profile, reachable only from `ci-build` by NetworkPolicy; the proxy additionally maps the listener to its profile, so a `ci-jobs` pod can never select the build profile), `:9090` (port name `metrics`, CD-02) metrics/health (`/healthz`, `/readyz`, `/metrics`).
- **Authentication (agent):** `Proxy-Authorization: Basic base64("sc:<jws>")`. The JWS is the per-execution **egress token** (`contracts/task-token.schema.yaml`). Verify: EdDSA signature against `sc-runner-jwks` (`kid` match; the ConfigMap holds current and previous keys), `aud` = `[sc-egress-proxy]` and `token_use` = `egress`, `exp` > now − 30 s skew, `nbf` ≤ now + 30 s, `execution_id` present, `egress_allow` present (may be empty). Failure → 407 `Proxy-Authenticate: Basic realm="sc-egress"` and `sc_egress_auth_failures_total{reason}`.
- **Decision** for host `h`, port `p` (host lowercased, trailing dot removed, IDNA → punycode):
  1. `h` is an IP literal (v4 or v6, including decimal/octal forms) → deny `IP_LITERAL`.
  2. `h` matches any `spec.forbiddenHosts` pattern → deny `FORBIDDEN_HOST` (model provider APIs live here so that workers cannot bypass the Gateway, 36-R-2).
  3. Agent listener: allow set = `profiles[listeners.agent.baseProfile]` ∪ { claim entries that are each inside `delegation.repositoryAllowed` }. A claim entry outside the bounds is ignored and counted (`CLAIM_OUT_OF_BOUNDS`), never honored. CI listener: allow set = `profiles[listeners.ci.baseProfile]`.
  4. No pattern in the allow set matches `h` with port `p` → deny `NOT_ALLOWED`.
  5. Resolve `h` (A records only, via cluster DNS; IPv6 is not used, plan 09). If **any** returned address is inside the built-in deny list or `spec.denyNetworks` → deny `RESOLVES_TO_DENIED_NETWORK` (DNS-rebinding defense). Built-in deny list: `0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16, 172.16.0.0/12, 192.0.0.0/24, 192.168.0.0/16, 198.18.0.0/15, 224.0.0.0/4, 240.0.0.0/4`, the pod and service CIDRs, and `SC_NODE_IP_*`.
  6. Connect to the first resolved address (the checked address; no re-resolution), `connect` timeout `timeouts.connect`.
- **Pattern semantics:** `example.org` matches exactly that name; `*.example.org` matches one or more labels to the left (`a.example.org`, `a.b.example.org`) but not `example.org` itself. Entries are `pattern` or `pattern:port`; no port means 443.
- **Limits:** per `execution_id` at most `listeners.agent.maxConcurrentTunnelsPerClient` (default 32) open tunnels; per listener 512; idle timeout `timeouts.idle` (300 s); tunnel lifetime `min(token exp, timeouts.maxTunnelLifetime)` (2 h); request header size 16 KiB.
- **Responses on deny:** 403 with `X-SC-Egress-Deny: <reason>` and the JSON body in §7. `sc-worker` counts denies by reason in the worker result (plan 27) so that a task failing on a missing registry is diagnosable.
- **Logging:** one JSON line per decision: `ts`, `listener`, `decision`, `reason`, `host`, `port`, `execution_id` (agent only), `client_ip`, `bytes_up`, `bytes_down`, `duration_ms` (at close). No paths, headers, or bodies (CONNECT exposes none).
- **Policy loading:** the policy file is mounted from a Flux-generated ConfigMap (ADR-0017). Loaded at start and on change (file watch). Invalid at start → readiness false, no proxying (fail closed). Invalid on reload → keep last good, increment `sc_egress_policy_load_errors_total`. The active digest is exposed in `sc_egress_policy_info{digest}`.
- **Revocation:** the proxy does not query the Runner. Tokens expire at Job deadline + 300 s (plan 26), and cancellation deletes the Job (the pod's network namespace disappears). Residual risk: a token exfiltrated from a still-running worker can be used from another `agent-jobs` pod until it expires; only Runner-created pods exist there (plan 37), so the risk is bounded to other workers.

#### 9.9.2 Deployment

Deployment `strange-company/egress-proxy`, 2 replicas (both profiles; `topologySpreadConstraints` on hostname at small-ha, `ScheduleAnyway`), PDB `minAvailable: 1`, `priorityClassName: platform`, resources 50m / 64Mi request, 256Mi limit, PSA restricted (non-root, read-only root fs, drop ALL), `automountServiceAccountToken: false` (no API access), Service `egress-proxy` (ClusterIP) ports `agent` 3128, `ci` 3129, `build` 3130, `dr` 3131, `renovate` 3132, `metrics` 9090. Delivered by the `strange-company` chart (plan 54) with values `egressProxy.enabled: true`. Image built by trusted CI (plan 23), digest-pinned.

#### 9.9.3 Default policy file (initial `policy/egress.yaml`)

```yaml
apiVersion: strange.company/v1alpha1
kind: EgressPolicy
metadata: {name: platform}
spec:
  defaultAction: deny
  denyNetworks: []
  forbiddenHosts:
    - api.anthropic.com
    - api.openai.com
    - api.deepseek.com
    - "*.googleapis.com"
    - api.z.ai
    - open.bigmodel.cn
    - "*.openai.azure.com"
  profiles:
    agent-base:
      description: Language package registries and checksum databases needed for builds and tests.
      hosts: [proxy.golang.org, sum.golang.org, storage.googleapis.com, registry.npmjs.org, pypi.org, files.pythonhosted.org, index.crates.io, static.crates.io, crates.io]
    ci-base:
      description: Same registries plus upstream base-image registries for trusted builds.
      hosts: [proxy.golang.org, sum.golang.org, storage.googleapis.com, registry.npmjs.org, pypi.org, files.pythonhosted.org, index.crates.io, static.crates.io, crates.io, registry-1.docker.io, auth.docker.io, production.cloudflare.docker.com, ghcr.io, pkg-containers.githubusercontent.com, quay.io, "*.quay.io", registry.k8s.io, "*.pkg.dev"]
    ci-build:
      description: Release lane (protected refs only). ci-base plus release-signing, SBOM/scan and attestation hosts.
      hosts: [proxy.golang.org, sum.golang.org, storage.googleapis.com, registry-1.docker.io, auth.docker.io, production.cloudflare.docker.com, ghcr.io, pkg-containers.githubusercontent.com, quay.io, "*.quay.io", registry.k8s.io, "*.pkg.dev", github.com, api.github.com, objects.githubusercontent.com, snapshot.debian.org, deb.debian.org, mirror.gcr.io, rekor.sigstore.dev, fulcio.sigstore.dev, tuf-repo-cdn.sigstore.dev]
    dr-tests:
      description: Restore tests (SEC-23). The object-storage endpoint plus GitHub for the black-start check only.
      includeObjectStorageEndpoint: true
      hosts: [github.com, api.github.com, codeload.github.com]
    renovate:
      description: Renovate (SEC-B13). Registries and release metadata for the pinned dependency types only.
      hosts: [github.com, api.github.com, codeload.github.com, objects.githubusercontent.com, raw.githubusercontent.com, "*.github.io", ghcr.io, pkg-containers.githubusercontent.com, registry-1.docker.io, auth.docker.io, hub.docker.com, quay.io, "*.quay.io", registry.k8s.io, "*.pkg.dev", proxy.golang.org, sum.golang.org, update.k3s.io, charts.longhorn.io, registry.npmjs.org, pypi.org]
  listeners:
    agent: {port: 3128, auth: task-token, baseProfile: agent-base, maxConcurrentTunnelsPerClient: 32}
    ci: {port: 3129, auth: none, baseProfile: ci-base, maxConcurrentTunnelsPerClient: 64}
    build: {port: 3130, auth: none, baseProfile: ci-build, maxConcurrentTunnelsPerClient: 64}
    dr: {port: 3131, auth: none, baseProfile: dr-tests, maxConcurrentTunnelsPerClient: 32}
    renovate: {port: 3132, auth: none, baseProfile: renovate, maxConcurrentTunnelsPerClient: 32}
  delegation:
    repositoryAllowed: ["*.npmjs.org", "*.pythonhosted.org", "*.crates.io", "repo.maven.apache.org", "repo1.maven.org", "plugins.gradle.org", "services.gradle.org", "rubygems.org", "*.rubygems.org", "api.nuget.org", "packagist.org", "repo.packagist.org", "github.com", "codeload.github.com", "objects.githubusercontent.com"]
    maxHostsPerRepository: 20
  timeouts: {connect: 10s, idle: 300s, maxTunnelLifetime: 2h}
```

`storage.googleapis.com` appears because the Go module proxy redirects downloads there; `*.googleapis.com` is forbidden. Loader rule: an exact allow entry wins over a forbidden wildcard only when the profile entry is an **exact host** and the forbidden entry is a wildcard; otherwise forbidden wins. The policy loader rejects any profile or delegation entry that equals a forbidden exact host (`FORBIDDEN_IN_PROFILE`).

#### 9.9.4 Loader invariants (beyond the schema)

- `profiles` contains the `baseProfile` of every listener.
- No profile or delegation entry equals an exact `forbiddenHosts` entry.
- Listener ports are pairwise distinct and ∉ {9090} (metrics).
- A profile with `includeObjectStorageEndpoint: true` adds the host of the installation's S3 endpoint (proxy env `SC_S3_ENDPOINT` from `cluster-vars`) with its port; if that host resolves inside `SC_S3_CIDR` (private object storage, SEED-3), that range is exempt from the built-in deny list **for this host only**.
- The `dr` listener is reachable only from `dr-tests` by NetworkPolicy (D-1a); within it, `github.com`/`api.github.com` are allowed only for clients whose pod carries the black-start check label (the proxy reads the client pod's labels via a `pods` `get` permission in `dr-tests` only).
- **Cross-file (F04, COST-18):** every host of every `providers.*.upstream.baseUrl` in `policy/model-routing.yaml` is matched by a `forbiddenHosts` pattern; a routing PR that adds a provider without the matching forbidden entry fails CI.
- `RepositoryPolicy.spec.network.egressHosts` entries outside `delegation.repositoryAllowed` are rejected when the plan 22 loader reads the repository policy (task creation fails `POLICY_EGRESS_OUT_OF_BOUNDS`), so the claim never contains them; the proxy's own check (step 3) is defense in depth.

## 10. Secrets

| Secret class | Role | Notes |
|---|---|---|
| `SC-WORKER-TASK-TOKEN` (plan 26) | egress token (`token_use: egress`, `aud: [sc-egress-proxy]`) consumed by the egress proxy (validation only) | no revocation list in MVP: a revoked egress token stays usable for package fetches until `exp` (≤ deadline + 5 min; residual risk RA-m14, Q-36-09) |
| `SC-RUNNER-SIGNING` (plan 26) | public half consumed via ConfigMap `sc-runner-jwks` | no private key in the proxy |

No new secret class.

## 11. Storage

None. Policies are in Git; the proxy is stateless.

## 12. Resource requirements

| Component | self-hosting-minimum | small-ha |
|---|---|---|
| egress-proxy | 2 × (50m / 64Mi req, 256Mi limit) | same |
| kube-router policy controller | inside the k3s process | same |

NetworkPolicy count ≈ 120 objects; kube-router iptables/ipset rule growth is modest at this scale (no measurable impact expected; P12 memory gate covers k3s).

## 13. Security boundary

- NetworkPolicy is L3/L4. FQDN filtering is not possible with kube-router (ADR-0011); the egress proxy supplies FQDN control for workers and CI, and Internet ipBlocks for trusted platform pods exclude private, cluster and metadata ranges.
- The egress proxy is a trusted component (`platform` tier) but holds no credential beyond the public JWKS. Compromise of the proxy allows arbitrary egress for workers; it does not grant access to cluster services, because worker NetworkPolicies still allow only W-1..W-5.
- Selectors are trustworthy only together with admission (plan 37): only the Runner creates pods in `agent-jobs`, only Woodpecker in `ci-jobs`, and no workload outside `agent-jobs` may carry `app.kubernetes.io/managed-by=agent-runner` (VAP request to plan 37).
- Cross-namespace label spoofing: ingress rules match `namespaceSelector` **and** `podSelector`; pods cannot change their namespace, so a pod in `apps` cannot impersonate Forgejo.
- Residual risks (accepted, recorded in plan 34 R-9, restated per SEC-17): internet ipBlocks allow any public destination for the listed trusted pods; **by default** workers and untrusted CI can exfiltrate through writable endpoints already in `agent-base`/`ci-base` (`storage.googleapis.com`, `registry.npmjs.org` and `crates.io` publish APIs; Docker Hub and GHCR pushes with attacker-supplied credentials) and through DNS tunnelling via CoreDNS from every namespace. Controls: no credential for those services exists in the cluster, repository delegation needs human review of `.strange-company/**`, and egress decisions are logged per execution. Read-only mirrors or a Go-proxy-only default are post-MVP options (Q-36-07).
- Header spoofing (SEC-16): pods allowed by I-10b can send `X-Forwarded-For` to Traefik's `websecure` entrypoint, which trusts the pod CIDR in tunnel mode (the `oidc` entrypoint trusts nothing). Accepted for MVP (I-10b holds only the Woodpecker server and platform-probes); a dedicated tunnel entrypoint that alone trusts forwarded headers is Q-36-08.

## 14. Happy path

1. F11/F12: `sc-gitops render` produces the policies; `make e2e-netpol` passes in k3d.
2. Seed (P04): the `policies` layer applies all NetworkPolicies before any component layer (plan 18 `dependsOn`), including namespaces whose components arrive later.
3. P08: Woodpecker runs with `SC_CI_EGRESS=direct`.
4. C01: the chart deploys `egress-proxy`. C02: a PR sets `SC_CI_EGRESS=proxy`; Woodpecker step env carries the proxy; the C02 gate runs the live worker matrix.
5. Steady state: repository owners add registries via `.strange-company/policy.yaml` within the delegation bounds; platform owners widen bounds by PR to `policy/egress.yaml`.

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Policy controller not enforcing (disabled flag, crash) | `SCNetworkPolicyControllerDown`: the `netpol-canary` check CronJob (ADR-0553) in `monitoring` tries a connection that must be denied (to `forgejo` `forgejo-pg-rw:5432`, which exists from P04; GC-18) and exits 0 only if it times out; a DNS failure or connection refusal is reported as inconclusive (exit 2, metric only), never as success or as a policy failure | isolation lost | none | re-run configure; plan 07 `disable-network-policy` must be absent |
| Missing allow (new chart port, label change) | component Not Ready; Flux health fails; 36-AC-12 selector check | component outage | none | add the row by PR |
| Node IPs stale after expansion | intermittent API timeouts from pods | controllers flap | none | `add-node.md` step "update node IPs" before join |
| egress-proxy down | `SCEgressProxyDown` | workers and CI cannot fetch packages (fail as infrastructure, not merit) | Deployment restart; 2 replicas | `agent-runner-failure.md` §Egress |
| Policy file invalid | `SCEgressPolicyLoadFailed`; readiness false at start | old policy stays (reload) or proxy not ready (start) | fail closed | revert PR |
| Burst of denies | `SCEgressDeniedSpike` | possible exfiltration attempt or missing allow | none | inspect proxy logs by `execution_id`; plan 34 incident triage |
| Worker tries direct internet / API | connection timeout in worker; kube-router drop counters | task fails | Runner classifies as policy/infra per plan 26 | none |

## 16. Retry behavior

The proxy never retries upstream connections on behalf of clients (clients retry). Policy reload retries on the next file change. Flux retries policy application every 2 min.

## 17. Idempotency behavior

NetworkPolicies are declarative. The proxy's decisions are pure functions of (policy digest, token claims, DNS answer). Re-applying the same policy file is a no-op (same digest).

## 18. Bootstrap behavior

The `policies` layer is in the seed set (plan 18 #3), so default-deny is active before Forgejo or any data store starts. Seed-time egress needs (Flux source-controller to S3 and chart registries, CNPG to S3, Longhorn to S3, cert-manager to ACME, cloudflared to the edge) are all rows above; a seed in k3d (plan 19 harness) with the policies applied is part of 36-AC-9.

## 19. Upgrade behavior

- Component upgrades that change ports or labels must update `matrix.yaml` in the same PR; CI's k3d connectivity test (F12) and the live selector check (36-AC-12) catch omissions.
- egress-proxy upgrades ship with the chart (plan 48 SC components); policy schema changes bump `v1alpha1` → `v1alpha2` with a migration note.
- k3s upgrades re-run 36-AC-4 (local-node exemption) and 36-AC-3 (named ports) in the lab as part of plan 48's k3s upgrade gate, because those behaviors are kube-router-version-sensitive.

## 20. Recovery behavior

Policy mistakes are reverted by Git. If a policy mistake blocks Flux itself (for example source-controller egress removed), Flux cannot apply the fix: the operator applies the previous `flux-system` policies directly with `./bootstrap exec -- kubectl apply -f <rendered file from the last good commit>`, then lets Flux reconcile the fixed commit (documented in `failed-flux-handoff.md`, plan 19, and `upgrade-platform-component.md` §Rollback).

## 21. Observability

Metrics from the egress proxy:

| Metric | Labels |
|---|---|
| `sc_egress_requests_total` | `listener`, `decision` (`allow`/`deny`), `reason` |
| `sc_egress_bytes_total` | `listener`, `direction` (`up`/`down`) |
| `sc_egress_active_tunnels` | `listener` |
| `sc_egress_auth_failures_total` | `listener`, `reason` |
| `sc_egress_policy_info` | `digest` |
| `sc_egress_policy_load_errors_total` | — |

No `execution_id` or host label (cardinality, SPEC §91); those are in logs.

**Alerts introduced (plan 42 to register):**

| alertname | Expression sketch | for | Severity / class | Runbook |
|---|---|---|---|---|
| `SCEgressProxyDown` | `sum(up{job="egress-proxy"}) == 0` (after C01) | 5m | critical / page | `agent-runner-failure.md` §Egress |
| `SCEgressDeniedSpike` | `sum(increase(sc_egress_requests_total{decision="deny"}[15m])) > 200` | 0m | warning / notify | plan 34 triage |
| `SCEgressPolicyLoadFailed` | `increase(sc_egress_policy_load_errors_total[10m]) > 0` | 0m | warning / notify | revert PR |
| `SCNetworkPolicyControllerDown` | check CronJob `netpol-canary` failed or stale (ADR-0553 `source.kind: check-cronjob`, `failAfter: 15m`, `maxAge: 30m`) | — | critical / page | `lost-node.md` §NetPol (re-run configure) |

## 22. Testing strategy

- **Static (F11/F12):** every namespace in the master plan list has `default-deny` and `allow-dns`; no policy has an empty `from`/`to` with all ports (allow-all) except rows CI-1, D-2, AP-1, L-1 (intra-namespace) and the documented Internet ipBlocks; every Internet ipBlock carries the full `except` list; `matrix.yaml` ↔ rendered policies are consistent (a generator check renders policies from the matrix and diffs). The egress-policy schema and loader invariants are tested with the contract's examples and invalid examples (F02).
- **k3d (F12, `make e2e-netpol`):** k3s with its default kube-router policy controller. The harness creates every namespace with the rendered policies, deploys a probe pod per (namespace, selector) from `matrix.yaml` (labels copied from the matrix, ports served by a tiny TCP/UDP echo server), and runs: every allowed row connects within 3 s; a generated sample of denied pairs (all `agent-jobs` → every other target; every namespace → `strange-company` `sc-pg`; every namespace except allowed → Nodes:6443 emulated by the k3d server IP) times out within 3 s. Results are written as JUnit. Deterministic, no paid APIs.
- **egress-proxy unit and integration (Go):** pattern matching table tests, IP-literal forms, IDNA, DNS rebinding (a fake resolver returning a private address), token validation (wrong audience, expired, wrong kid, out-of-bounds claim), CONNECT to a local TLS echo server.
- **Live:** gates below.

## 23. Acceptance criteria

### GATE-F12

- **36-AC-1** `make test-netpol-static` passes: every Probe CR target in `gitops/platform/monitoring/scrape/` is allowed by a matrix row for the `blackbox` pods (GC-03); every namespace in `plans/00-master-plan.md` §5 plus `monitoring-node` and `ci-build` has `default-deny` and `allow-dns` in the render; the allow-all lint and Internet `except` lint report zero findings; the matrix ↔ render diff is empty.
- **36-AC-2** `make e2e-netpol` (k3d) reports 100 % of matrix rows connecting and 0 of the denied sample connecting; JUnit file shows ≥ 1 test per matrix row. The denied sample includes (SEC-5) connects from probe pods in `agent-jobs`, `ci-jobs`, `ci-build` and `hermes` to **every node IP** on TCP 22, 2379, 2380, 2381, 6443, 9100 and 10250 (all must time out), and an allowed pod (Prometheus label) to node IP 10250 and 6443 succeeds.
- **36-AC-3** In the same k3d run, a policy that references the target port by name (`port: metrics`) admits the connection and an unnamed port is denied (re-verifies named-port support in the pinned kube-router).
- **36-AC-4** In k3d, a pod under `default-deny` with an HTTP readiness probe becomes Ready (re-verifies the local-node exemption; on failure, an `allow-kubelet-probes` rule is added per namespace and this plan is amended).
- **36-AC-5** `make test-egress-proxy` passes, including: `CONNECT 10.0.0.1:443` → 403 `IP_LITERAL`; `CONNECT api.anthropic.com:443` with a claim listing it → 403 `FORBIDDEN_HOST`; a host whose fake DNS answer is `169.254.169.254` → 403 `RESOLVES_TO_DENIED_NETWORK`; a token with `aud: ["agent-runner"]` only → 407; an allowed host → tunnel established.
- **36-AC-6** `make contracts` validates `contracts/egress-policy.schema.yaml` (meta-schema, examples pass, invalid examples fail) and `policy/egress.yaml` against it.

### GATE-P03

- **36-AC-7** Live enforcement probe: `./bootstrap exec -- kubectl apply -f tests/netpol/live-probe.yaml` creates namespace `sc-netpol-probe` with `default-deny`, a server pod and two clients (one allowed by a policy, one not). `kubectl exec allowed -- nc -z -w 3 <server-ip> 8080` exits 0 and `kubectl exec denied -- nc -z -w 3 <server-ip> 8080` exits non-zero. The namespace is deleted afterwards (also V-12 in plan 50).

### GATE-P04

- **36-AC-8** After seed, `kubectl get networkpolicy -A -o json | jq '[.items[] | select(.metadata.name=="default-deny")] | length'` equals the number of namespaces in the master plan list plus `monitoring-node` and `ci-build`, and `kubectl -n flux-system get networkpolicy allow-egress allow-scraping allow-webhooks` returns NotFound for all three.
- **36-AC-9** Every seed-set layer is Ready with the policies applied (plan 19 seed-complete), which proves rows F-1..F-4, C-1..C-4, N-1..N-6, L-1..L-4, I-1, I-2, I-11, FJ-1, FJ-2 are sufficient for the seed.
- **36-AC-10** From a throwaway pod in `apps` (`kubectl run -n apps np-test --image=<curl digest> --rm -i --restart=Never -- curl -m 3 -s http://forgejo-http.forgejo.svc:3000/api/healthz`) the request times out (exit 28).

### GATE-C02

- **36-AC-11** A fake-harness worker execution (scenario `fake-netprobe`, plan 28) reports in its result: Forgejo SSH 2222 reachable, Runner 8082 reachable, Gateway 8080 reachable, egress proxy 3128 reachable; Kubernetes API (`kubernetes.default.svc:443`), `sc-pg-rw.strange-company:5432`, `longhorn-backend.longhorn-system:9500`, `source-controller.flux-system:80`, `woodpecker-server.woodpecker:8000`, `alertmanager-operated.monitoring:9093`, `1.1.1.1:443` and **every node IP** on TCP 22, 2379, 2380, 2381, 6443, 9100 and 10250 (SEC-5, GC-17) all time out within 3 s. This item is the live counterpart of SPEC §105 "bypass network policy" (SEC-21; plan 34 maps to it).
- **36-AC-12** `make live-netpol-selectors` (toolbox): for every NetworkPolicy outside `agent-jobs`/`ci-jobs`/`dr-tests`, its `podSelector` (with namespace) matches ≥ 1 running pod once the owning component's layer is Ready. Zero violations.
- **36-AC-13** Through the proxy with a valid task token whose claim is empty: `curl -x http://sc:$TOKEN@egress-proxy.strange-company.svc:3128 -sSf https://proxy.golang.org/` succeeds; `https://example.com/` returns 403 with `X-SC-Egress-Deny: NOT_ALLOWED`; `https://api.anthropic.com/` returns 403 `FORBIDDEN_HOST`; without credentials → 407.
- **36-AC-14** `kubectl -n strange-company get deploy egress-proxy -o jsonpath='{.status.readyReplicas}'` = 2 and `sc_egress_policy_info{digest}` equals the sha256 of `policy/egress.yaml` at the deployed revision.
- **36-AC-15** `kubectl get cm -n flux-system cluster-vars -o jsonpath='{.data.SC_CI_EGRESS}'` = `proxy`, and a CI step pod in `ci-jobs` running `curl -m 5 -s https://example.com` (fixture pipeline) fails, while `go mod download` of a fixture module succeeds through 3129.
- **36-AC-16** `kubectl auth can-i --list --as=system:serviceaccount:agent-jobs:agent-worker` shows no rules beyond the defaults, and a worker pod's `/var/run/secrets/kubernetes.io` does not exist (cross-check with plan 37; network denial of the API is 36-AC-11).

## 24. Explicit non-goals

- L7 policy, FQDN-aware NetworkPolicy, Cilium/Calico (ADR-0011).
- TLS interception or content inspection in the proxy.
- Per-task bandwidth quotas (metrics only).
- Egress control for host processes (plan 08 §13).
- An egress proxy for platform components (their dependencies, such as backups, must not depend on a Strange Company component).

## 25. Open questions

- **Q-36-01** (blocking: no) Plan 18 §5 says Flux's generated NetworkPolicies are kept; this plan requires deleting them because they are additive allow-alls (36-AC-8). *Proposed:* plan 18 adds `$patch: delete` for the three objects. *Owner:* 18.
- **Q-36-02** (blocking: no) The G2 brief assumed the egress proxy exists at P08 for `ci-jobs`. Its image is built by trusted CI (P11) and deployed by the chart (C01), so `ci-jobs` uses direct Internet egress (`SC_CI_EGRESS=direct`) from P08 until GATE-C02, while only human-authored code runs. *Proposed:* accept; C02 gate enforces `proxy`. Alternative if rejected: publish the egress-proxy image from the Phase-1 interim CI to GHCR like the toolbox (ADR-0013 exception). *Owner:* 36/23.
- **Q-36-03** (resolved, CD-02/CD-03/CD-08) Metrics ports are selected by name; the webhook port is 8083; the JWKS ConfigMap is `sc-runner-jwks`; the Runner reaches the Gateway only on 9090 for `/sc/v1/status`. *Owner:* —.
- **Q-36-04** (blocking: no) Plan 53's "Gateway is the only workload with Internet egress among agent-jobs, hermes and strange-company" conflicts with the Runner's S3 access (evidence bucket) and the egress proxy. *Proposed:* reword per §9.4. *Owner:* 53.
- **Q-36-05** (resolved) Workers send spans to `otel-collector-workers` only (plan 40 §13 records it as explicit platform policy; plan 34 security review confirms). Row W-5. *Owner:* 40/34.
- **Q-36-07** (blocking: no) Default `agent-base`/`ci-base` contain writable registries (SEC-17). *Proposed:* accept for MVP with the controls in §13; evaluate read-only mirrors post-MVP. *Owner:* 36/34.
- **Q-36-08** (partly resolved, SEC-16) Implemented: the `oidc` entrypoint (8444, `auth.` only) for OIDC clients and Hermes, satisfying plan 24's requirement; `ci-jobs` and Hermes have no 8443 access. Deferred: a tunnel-only entrypoint so `websecure` stops trusting forwarded headers from the pod CIDR; it would require every app plan's IngressRoute to change. *Owner:* 09/36.
- **Q-36-09** (blocking: no) Optional jti deny-list for egress tokens (RA-m14). *Proposed:* not in MVP (task-token contract states no revocation list); reserved design: the proxy may later poll a Runner-published ConfigMap `sc-egress-revoked` (jti list, Runner RBAC write on that name only) every 15 s. Accepted residual risk until then. *Owner:* 36/26.
- **Q-36-10** (rejected, SEC-25) Restricting CI-1 to pods of the same Woodpecker workflow is not expressible: NetworkPolicy selectors are static and cannot match "the same label value as the source", and per-workflow policies would need a controller creating them per pipeline. Mitigations: `CI_MAX_CONCURRENT=1` on self-hosting-minimum (no concurrent workflows); at small-ha residual risk R-27 (plan 34) is accepted, and GREEN verification pipelines should avoid networked service containers holding secrets (they hold none, ADR-0008). *Owner:* 36/20/34.
- **Q-36-06** (resolved) `dag.yaml` now lists plan 36 under F12, P03, P04 and C02.
