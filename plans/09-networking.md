# 09 — Networking and ingress

**Status:** Stage-0 plan (draft)
**DAG nodes:** P03 (co-owned with 07, 08: CNI, WireGuard, CoreDNS), P04 (co-owned: Traefik and cloudflared Ready, public edge serves Forgejo), C01 (contributes 09-AC-17: internal image pulls survive an edge outage; add `"09"` to C01 in `dag.yaml`)
**Related plans:** [07](07-k3s.md), [08](08-node-security.md) (host firewall), [10](10-tls-certificates.md) (certificates, ACME), [15](15-authentik.md) (forward-auth, OIDC hosts), [16](16-forgejo.md) (Forgejo SSH), [18](18-flux.md) (layers), [19](19-seed-handoff.md) (seed readiness), [35](35-secrets.md), [36](36-network-policy.md), [37](37-admission-policy.md) (hostPort VAP), [38](38-prometheus.md) (scrapes, blackbox), [42](42-alerting-notifications.md), [47](47-ha-expansion.md), [50](50-bootstrap-orchestrator.md) (Cloudflare preflight, DNS records)
**Contracts:** consumes `bootstrap-config.schema.yaml` (`ingress.*`), `provider.schema.yaml` (public IPs); produces the committed generated file kind `PublicHosts` (§9.3; schema embedded in `sc-gitops`, promoted to a contract if a second consumer appears)
**ADRs:** relies on ADR-0010, ADR-0011, ADR-0015; introduces ADR-0151 (direct mode = hostPort DaemonSet) and ADR-0153 (owned CoreDNS manifest + public-hostname rewrite)

## 1. Purpose

Define the cluster network: address plan, CNI and its encryption, cluster DNS (including how in-cluster clients reach public hostnames), the ingress controller, the two public edge modes (Cloudflare Tunnel by default, direct ingress as the alternative), how Forgejo SSH is or is not exposed, the IPv4/IPv6 stance, and the rule that the Kubernetes API is never public. Certificates are plan 10. NetworkPolicy is plan 36. Host firewall is plan 08.

## 2. Requirements derived from SPEC.md

1. Network-policy enforcement stays enabled. [SPEC §18]
2. Inter-node pod traffic over public VPS networks should be encrypted; simple native k3s/flannel WireGuard is preferred. [SPEC §18]
3. Infrastructure control interfaces are not exposed publicly without justification. [SPEC §18]
4. Traefik is the ingress controller; Cloudflare Tunnel is the external ingress. [SPEC §20]
5. Cloudflare Tunnel MAY terminate public TLS; a direct-ingress mode and behavior without Cloudflare are defined. [SPEC §23]
6. The seed includes required ingress/tunnel pieces. [SPEC §34]
7. The one-node system is reduced redundancy, not a separate architecture. [SPEC §12]

Derived requirements:

- **09-R-1** IPv4 single-stack cluster. Pod CIDR `10.42.0.0/16`, service CIDR `10.43.0.0/16`, cluster DNS `10.43.0.10`, domain `cluster.local`. Immutable after install.
- **09-R-2** Every public hostname is served by Traefik in namespace `ingress` with the `public-wildcard` certificate. No other workload terminates public traffic.
- **09-R-3** Tunnel mode exposes no public HTTP(S) port on nodes. Direct mode exposes exactly 80/443 (and 2222 only if opted in) via hostPort on a Traefik DaemonSet.
- **09-R-4** In-cluster clients resolve every platform public hostname to Traefik's ClusterIP, so OIDC discovery and token exchange never hairpin through the public edge (ADR-0153).
- **09-R-5** The Kubernetes API (6443), etcd, kubelet, Longhorn UI, Prometheus/Alertmanager without forward-auth, the Runner API, the Model Gateway and Flux are never routed by Traefik or the tunnel.
- **09-R-6** The public host list has one source of truth in Git, and every derived artifact (tunnel ingress rules, DNS records, CoreDNS rewrite, certificate SANs where per-host) is generated from it and checked for drift in CI.

## 3. Inputs

| Input | Source | Used for |
|---|---|---|
| `ingress.mode` (`tunnel`/`direct`) | `bootstrap-config.yaml` (`INGRESS_MODE`) → `cluster-vars.INGRESS_MODE` | component selection `components/ingress-tunnel` or `ingress-direct` (plan 18 §8.1) |
| Base domain | `cluster.baseDomain` → `cluster-vars.SC_BASE_DOMAIN` | hostnames |
| Cloudflare account/zone, API token (`SC-DNS-API`) | `bootstrap.env` | tunnel creation, DNS records (bootstrap time only) |
| Tunnel token (`SC-DNS-TUNNEL`) | created by `sc-bootstrap` (§14.1), SOPS in Git | cloudflared |
| Node public IPv4 addresses | plan 03 output | direct-mode A records |
| Public host list | `gitops/clusters/<cluster>/config/public-hosts.yaml` (§9.3) | everything in 09-R-6 |
| CoreDNS image and version | `versions/k3s.lock.yaml` `coredns:` entry (the version the pinned k3s bundles; a new field in plan 01's `K3sVersionLock`, because CoreDNS is Ansible-placed, not a GitOps image) | owned CoreDNS manifest |
| Traefik and cloudflared chart/image pins | `versions/platform.lock.yaml` (plan 01), referenced from `gitops/platform/{traefik,cloudflared}/base/` (digest-pinned) | HelmRelease / Deployment |

## 4. Outputs

- flannel `wireguard-native` overlay (configured by plan 07's cluster file) verified by this plan.
- CoreDNS: Deployment `kube-system/coredns`, Service `kube-system/kube-dns` (`10.43.0.10`), ConfigMap `kube-system/coredns` (Corefile, Ansible-owned) and ConfigMap `kube-system/coredns-custom` (rewrite, Flux-owned).
- Traefik: HelmRelease `ingress/traefik`, Service `ingress/traefik` (**pinned ClusterIP `10.43.0.20`**, CD-21; ports 80→8000, 443→8443), metrics Service `ingress/traefik-metrics` (9100), IngressClass `traefik` (default), TLSOption `default`, TLSStore `default`.
- Tunnel mode: Deployment `ingress/cloudflared`, ConfigMap `ingress/cloudflared-config`, Secret `ingress/cloudflared-tunnel` (`SC-DNS-TUNNEL`, key `token`; name per `contracts/secret-classes.yaml`), PodMonitor target port 2000.
- Public DNS records (Cloudflare): tunnel mode `CNAME <host> → <tunnel-id>.cfargotunnel.com` (proxied); direct mode `A <host> → <every node public IPv4>` (DNS-only, not proxied).
- Generated committed files (§9.3): `cloudflared-config.yaml`, `coredns-custom.yaml`, `dns-records.yaml`.

## 5. Dependencies

**Hard (DAG):** P03 needs P02 and F10. P04 needs P03, F11, F12.

**Soft / runtime:**
- Plan 07 renders `flannel-backend`, CIDRs, `disable: [traefik, servicelb]` and places this plan's CoreDNS files in `/var/lib/rancher/k3s/server/manifests/`.
- Plan 08 opens 51820/udp between peers and leaves forward unfiltered for hostPort DNAT.
- Plan 10 provides `ClusterIssuer letsencrypt-prod` and Certificate `ingress/public-wildcard` (Secret `public-wildcard-tls`) before Traefik serves public traffic. Traefik starts without it and serves its self-signed default until the Secret exists.
- Plan 18 must add layer `coredns-config` (seed set: yes; `dependsOn: [namespaces]`; path `gitops/platform/coredns-config/overlays/${SC_PROFILE}`; namespace `kube-system`). **Assumed interface.**
- Plan 37 VAP `sc-hostport-traefik-only` (ADR-0151) and `sc-no-nodeport-lb` (plan 08 §13).
- Plan 38 runs the public blackbox probes from a separate `blackbox-public` Deployment with `dnsPolicy: None` and public resolvers (§9.5), so they test the real edge despite the rewrite. **Assumed interface.**
- Plan 50: preflight PF-15/PF-20 check the Cloudflare token against §10's permission set; the seed preparation step calls `sc-bootstrap tunnel ensure` and `sc-bootstrap dns apply` (§14.1). **Assumed interface;** plan 50 owns the CLI.

## 6. Owned state

| State | Location | System of record | Writers |
|---|---|---|---|
| Public host list | `gitops/clusters/<cluster>/config/public-hosts.yaml` | Git | PR |
| Cloudflare tunnel object and DNS records | Cloudflare account | Cloudflare; desired state = Git generated files | `sc-bootstrap tunnel ensure`, `sc-bootstrap dns apply` |
| Tunnel token | SOPS file `gitops/clusters/<cluster>/secrets/ingress/cloudflared-tunnel.sops.yaml` + recovery bundle | Git (SOPS) | `sc-bootstrap` (create/rotate) |
| CoreDNS Deployment and Corefile | `/var/lib/rancher/k3s/server/manifests/sc-coredns.yaml` | Git (`infra/ansible/roles/network/templates/`) | Ansible |

## 7. External interfaces

| Interface | Consumer | Definition |
|---|---|---|
| `https://<host>.<domain>` for `auth`, `git`, `ci`, `grafana`, `hermes`, `prometheus`, `alertmanager` (+ app hosts) | humans, OIDC clients | Traefik `websecure`, TLS 1.2+, `public-wildcard` cert |
| `IngressRoute` convention | plans 15, 16, 20, 24, 38, 39, 42, apps | §8.2 |
| Traefik Service `ingress/traefik:443` | cloudflared; in-cluster clients via the rewrite | ClusterIP |
| Forgejo SSH | Flux, workers, Runner: `forgejo-ssh.forgejo.svc.cluster.local:22` (internal); humans: HTTPS only in tunnel mode, `git.<domain>:2222` in direct mode if opted in | plan 16 |
| Cluster DNS `10.43.0.10:53` | every pod | CoreDNS |
| Kubernetes API | operators only via SSH forward `127.0.0.1:16443` (plan 50) | never routed publicly |
| Metrics | Traefik `:9100`, cloudflared `:2000`, CoreDNS `:9153` | plan 38 §7.2 |

## 8. Internal interfaces

### 8.1 Address plan and stance

| Item | Value |
|---|---|
| Pod CIDR | `10.42.0.0/16` (per-node /24) |
| Service CIDR | `10.43.0.0/16`; `kubernetes` = `10.43.0.1`; `kube-dns` = `10.43.0.10` |
| Cluster domain | `cluster.local` |
| Node IP | provider `internal_ip` (`node-ip`); `node-external-ip` = `external_ip` or `internal_ip` |
| Overlay | flannel `wireguard-native`, UDP 51820, interface `flannel-wg`; MTU = uplink MTU − 80 (flannel computes; 1420 on a 1500 uplink, verify 09-AC-3) |
| IPv6 | **not used by the cluster.** Nodes may have public IPv6; it is firewalled (plan 08) except optional SSH. No dual-stack: kube-router netpol, flannel WireGuard and every chart are tested IPv4-only here, and dual-stack CIDRs cannot be added later without rebuild. Public AAAA records are not created in direct mode; in tunnel mode Cloudflare serves IPv6 to clients at its edge. |
| Preflight | `NODE_CIDR_OVERLAP` (plan 08) if pod or service CIDR overlaps any host address, route, or the provider private network |

### 8.2 IngressRoute convention

- Each exposed app owns an `IngressRoute` (`traefik.io/v1alpha1`) in **its own namespace**, `entryPoints: [websecure]`, `routes[].match: Host(\`<host>.${SC_BASE_DOMAIN}\`)`, `tls: {}` (no `secretName`, no `certResolver`: the TLSStore default certificate serves it).
- Middlewares: `ingress/sc-secure-headers` (HSTS `max-age=31536000; includeSubDomains`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: strict-origin-when-cross-origin`, `frameDeny` except where an app needs framing) is referenced cross-namespace by name `ingress-sc-secure-headers@kubernetescrd`. Forward-auth middleware is plan 15's.
- `providers.kubernetesCRD.allowCrossNamespace: false`, so a route can only target Services in its own namespace. `allowExternalNameServices: false`. Middleware references to `ingress/*` use the provider-qualified name, which Traefik allows (verify, 09-AC-12).
- `providers.kubernetesIngress.enabled: true` for namespaces `apps` and `ingress` (IngressClass `traefik`). `ingress` is watched only so cert-manager's HTTP-01 solver Ingress is served (AUTH-12); F11 check `ingress-ns-ingresses` allows in `ingress` only Ingress objects labeled `acme.cert-manager.io/http01-solver: "true"` (cert-manager sets the label, verify). Platform components use `IngressRoute`.
- Forbidden (F11 render check `ingress-allowlist`): any route whose host is not in `public-hosts.yaml`; any route targeting Services in `kube-system`, `default`, `flux-system`, `longhorn-system`, `cnpg-system`, `strange-company`, `agent-jobs`, `ci-jobs`.

### 8.3 Public host list and generators

`gitops/clusters/<cluster>/config/public-hosts.yaml`:

```yaml
apiVersion: strange.company/v1alpha1
kind: PublicHosts
metadata: {name: <cluster>}
spec:
  baseDomain: example.com
  hosts:                          # label only; FQDN = <name>.<baseDomain>
    - {name: auth, owner: "15", namespace: authentik}
    - {name: git, owner: "16", namespace: forgejo}
    - {name: ci, owner: "20", namespace: woodpecker}
    - {name: grafana, owner: "39", namespace: monitoring}
    - {name: hermes, owner: "24", namespace: hermes}
    - {name: prometheus, owner: "38", namespace: monitoring, forwardAuth: true}
    - {name: alertmanager, owner: "42", namespace: monitoring, forwardAuth: true}
  apps: []                        # {name: <label>, namespace: apps}
```

Rules: labels match `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`; single-label hosts only (the wildcard certificate covers one level); `kubernetes`, `api`, `etcd`, `longhorn`, `flux`, `runner`, `gateway` are reserved and rejected.

`sc-gitops gen public-hosts --cluster <c>` (plan 18's CLI, new subcommand) writes three committed files; CI (F11 check `public-hosts-generated`) regenerates and fails on any diff:
- `gitops/clusters/<c>/config/cloudflared-config.yaml`: ConfigMap `ingress/cloudflared-config` (§9.2);
- `gitops/clusters/<c>/config/coredns-custom.yaml`: ConfigMap `kube-system/coredns-custom` (§9.1);
- `gitops/clusters/<c>/config/dns-records.yaml`: desired DNS record set consumed by `sc-bootstrap dns apply` (a record file, not a manifest).

The two ConfigMap manifests are applied through plan 18's cluster-directory mechanism (SEED-1: a seed-set layer such as `cluster-config` that depends on `namespaces`), never by the root Kustomization directly; `cloudflared` and `coredns-config` depend on it.

## 9. Configuration

### 9.1 CoreDNS (ADR-0153)

k3s's bundled CoreDNS addon is suppressed by the marker file `/var/lib/rancher/k3s/server/manifests/coredns.yaml.skip` (k3s skips any packaged manifest that has a `.skip` sibling; verify, 09-AC-5). The same directory receives `sc-coredns.yaml`, applied by k3s's deploy controller, containing:
- ServiceAccount `coredns`, ClusterRole `system:coredns` (endpoints, endpointslices, services, pods, namespaces: list/watch), ClusterRoleBinding;
- ConfigMap `coredns` with key `Corefile`:
  ```text
  .:53 {
      errors
      health { lameduck 5s }
      ready
      kubernetes cluster.local in-addr.arpa ip6.arpa {
          pods insecure
          fallthrough in-addr.arpa ip6.arpa
          ttl 30
      }
      prometheus :9153
      import /etc/coredns/custom/*.override
      forward . /etc/resolv.conf { max_concurrent 1000 }
      cache 30
      loop
      reload
      loadbalance
  }
  import /etc/coredns/custom/*.server
  ```
- Deployment `coredns`: replicas `min(2, server count)` (rendered by Ansible from inventory), `priorityClassName: system-cluster-critical`, `podAntiAffinity` **required** on `kubernetes.io/hostname`, `maxUnavailable: 1` rolling update, `readinessProbe` `:8181/ready`, `livenessProbe` `:8080/health`, resources 50m/70Mi request, 170Mi limit, image = the CoreDNS image the pinned k3s bundles (`rancher/mirrored-coredns-coredns:<v>@sha256:<d>`, from `versions/k3s.lock.yaml`), volumes `config-volume` (ConfigMap `coredns`) and `custom-config-volume` (ConfigMap `coredns-custom`, `optional: true`) mounted at `/etc/coredns/custom`; security context non-root, `NET_BIND_SERVICE` only, read-only root fs;
- Service `kube-dns`: ClusterIP `10.43.0.10`, ports 53/udp, 53/tcp, 9153/tcp, label `k8s-app: kube-dns`;
- PodDisruptionBudget `coredns` (`minAvailable: 1`), rendered only when server count ≥ 2.

**Public-hostname rewrite** (`coredns-custom`, key `sc-public-hosts.override`, generated by §8.3):

```text
rewrite stop {
    name regex ^auth\.example\.com\.$ traefik-oidc.ingress.svc.cluster.local.
    answer auto
}
rewrite stop {
    name regex ^(git|ci|grafana|hermes|prometheus|alertmanager)\.example\.com\.$ traefik.ingress.svc.cluster.local.
    answer auto
}
```

`auth.` resolves to a separate Service `ingress/traefik-oidc` (ClusterIP, port 443 → Traefik container port **8444**, entrypoint `oidc`; SEC-16, plan 24 §13). The `oidc` entrypoint serves only the Authentik route: plan 15's `IngressRoute authentik` lists `entryPoints: [websecure, oidc]`, every other IngressRoute lists `websecure` only (F11 check `oidc-entrypoint-auth-only`), and `oidc` trusts no forwarded headers. A pod allowed to reach only port 8444 (Hermes) can therefore reach `auth.` and nothing else behind Traefik, while public traffic is unchanged.

`answer auto` restores the original name in responses (available in the CoreDNS versions bundled by k3s ≥ 1.30, verify, 09-AC-6). If unavailable, the generator emits one `rewrite stop { name exact <fqdn>. traefik.ingress.svc.cluster.local. ; answer name traefik.ingress.svc.cluster.local. <fqdn>. }` block per host instead (generator flag `--rewrite-style=exact`). The rewrite matches only listed labels, so `_acme-challenge.<domain>` and unrelated hosts under the domain resolve normally.

Consequences that other plans must honor:
- **Public probes** (plan 38) run in `blackbox-public` with `dnsPolicy: None`, `dnsConfig.nameservers: [1.1.1.1, 9.9.9.9]`, otherwise they would test Traefik in-cluster instead of the edge.
- **cert-manager** (plan 10) sets `--dns01-recursive-nameservers-only` and `--dns01-recursive-nameservers=1.1.1.1:53,9.9.9.9:53`.
- In-cluster clients get Traefik's ClusterIPs and must be allowed to reach Traefik pods on 8444 (`auth.` only) and, where they need other hosts, 8443 by NetworkPolicy (plan 36 rows I-10a/I-10b).

### 9.2 Cloudflare Tunnel (tunnel mode, default)

- **Tunnel:** one named tunnel `sc-<cluster>` per installation, `config_src: local` (ingress rules live in Git, not in the Cloudflare dashboard). Created by `sc-bootstrap tunnel ensure` with `SC-DNS-API` (Cloudflare API `POST /accounts/{account_id}/cfd_tunnel` with `config_src: local`, then `GET .../cfd_tunnel/{id}/token`; verify endpoints at implementation, 09-AC-14). The tunnel UUID is written to `cluster-vars.SC_TUNNEL_ID`; the token to the SOPS file.
- **Required token permissions** (PF-15): Account › Cloudflare Tunnel › Edit; Zone › DNS › Edit; Zone › Zone › Read. Direct mode with DNS-01 needs only the zone permissions.
- **Deployment `ingress/cloudflared`:** replicas 1 (self-hosting-minimum), 2 (small-ha, `podAntiAffinity` preferred on hostname, PDB `minAvailable: 1`); image `cloudflare/cloudflared@sha256:<d>` (selection rule: newest release at least 14 days old; Renovate weekly, because Cloudflare supports connector versions for about a year); args `tunnel --no-autoupdate --config /etc/cloudflared/config.yaml run`; env `TUNNEL_TOKEN` from Secret `cloudflared-tunnel` key `token` (verify env name, 09-AC-14); readiness `GET :2000/ready` (200 when at least one edge connection is registered, verify); liveness same path, `failureThreshold: 6`, `periodSeconds: 10`; resources 20m/48Mi request, 128Mi limit; `priorityClassName: platform-critical`; runAsNonRoot uid 65532, read-only root fs, drop ALL, seccomp RuntimeDefault (PSA restricted-compatible).
- **Config (`cloudflared-config`, generated):**
  ```yaml
  tunnel: ${SC_TUNNEL_ID}
  protocol: auto              # QUIC on 7844/udp, falls back to HTTP/2 on 7844/tcp
  metrics: 0.0.0.0:2000
  no-autoupdate: true
  originRequest:
    connectTimeout: 10s
    tlsTimeout: 10s
    keepAliveTimeout: 90s
    http2Origin: true
  ingress:
    - hostname: git.example.com
      service: https://traefik.ingress.svc.cluster.local:443
      originRequest: {originServerName: git.example.com}
    # … one rule per host in public-hosts.yaml (platform + apps)
    - service: http_status:404
  ```
  `noTLSVerify` is never set (F11 check). cloudflared verifies Traefik's `public-wildcard` certificate against public roots, which is why the public certificate is ACME-issued in both modes (plan 10).
- **Egress** (plan 36): Cloudflare edge on 7844/udp and 7844/tcp and 443/tcp (`ipBlock 0.0.0.0/0` minus private ranges; FQDN filtering is not available, ADR-0011), Traefik pods 8443, DNS.
- **DNS records:** proxied `CNAME <host> → <SC_TUNNEL_ID>.cfargotunnel.com` for every host in `public-hosts.yaml`, applied by `sc-bootstrap dns apply` (idempotent upsert, never deletes records it does not own; ownership = a record comment `managed-by: strange-company/<cluster>`, verify comment support).
- **Headers:** Traefik's `websecure` entrypoint trusts `X-Forwarded-*` only from the pod CIDR (`forwardedHeaders.trustedIPs=10.42.0.0/16`) in tunnel mode. Client IP for logs is `Cf-Connecting-Ip`.
- **Cloudflare features not used:** Cloudflare Access, WAF rules, tunnel remote management. The operator MAY add Access policies in front of hosts; nothing here depends on them.

### 9.3 Traefik

HelmRelease `ingress/traefik` (remediation class `controller`, plan 18 §8.5), chart: official Traefik chart (selection rule: latest Traefik v3 chart release ≥ 14 days old whose CRDs support the pinned Kubernetes minor; OCI `oci://ghcr.io/traefik/helm/traefik` if published, else `https://traefik.github.io/charts`; verify). Values (abridged; key names verify against the pinned chart, 09-AC-12):

```yaml
deployment:
  kind: Deployment                 # ingress-direct component patches to DaemonSet
  replicas: 1                      # small-ha overlay: 2 (tunnel mode)
priorityClassName: platform-critical
ingressClass: {enabled: true, isDefaultClass: true, name: traefik}
providers:
  kubernetesCRD: {enabled: true, allowCrossNamespace: false, allowExternalNameServices: false}
  kubernetesIngress: {enabled: true, allowExternalNameServices: false, namespaces: [apps, ingress]}   # ingress: HTTP-01 solver only (AUTH-12)
ports:
  web:       {port: 8000, expose: {default: true}, exposedPort: 80}
  websecure: {port: 8443, expose: {default: true}, exposedPort: 443, http3: {enabled: false}}
  oidc:      {port: 8444, expose: {default: false}}   # in-cluster `auth.` only; Service traefik-oidc (extraObjects) maps 443 → 8444 (SEC-16)
  metrics:   {port: 9100, expose: {default: false}}
service: {type: ClusterIP, spec: {clusterIP: 10.43.0.20}}   # pinned (CD-21); reserved: no other Service may request it (F11 check)
tlsOptions:
  default: {minVersion: VersionTLS12, sniStrict: true}
tlsStore:
  default: {defaultCertificate: {secretName: public-wildcard-tls}}
logs:
  access: {enabled: true, format: json, fields: {headers: {defaultMode: drop}}}
metrics: {prometheus: {entryPoint: metrics}}
resources: {requests: {cpu: 50m, memory: 96Mi}, limits: {memory: 256Mi}}
securityContext: {runAsNonRoot: true, runAsUser: 65532, readOnlyRootFilesystem: true, allowPrivilegeEscalation: false, capabilities: {drop: [ALL]}, seccompProfile: {type: RuntimeDefault}}
podDisruptionBudget: {enabled: false}   # small-ha overlay: enabled, minAvailable 1
```

Mode-specific patches:
- `components/ingress-tunnel`: `ports.websecure.forwardedHeaders.trustedIPs: [10.42.0.0/16]`; `web` entrypoint stays internal (cloudflared uses https only).
- `components/ingress-direct` (ADR-0151): `deployment.kind: DaemonSet`; `ports.web.hostPort: 80`, `ports.websecure.hostPort: 443`; `ports.web.redirections.entryPoint: {to: websecure, scheme: https, permanent: true}` (except the ACME HTTP-01 path, which cert-manager's solver route takes precedence over, verify); `updateStrategy: {type: RollingUpdate, rollingUpdate: {maxUnavailable: 1}}`; namespace `ingress` labeled PSA `enforce: privileged` (PSA baseline forbids hostPort), with VAP `sc-hostport-traefik-only` (plan 37) as the compensating control: in `ingress`, only pods labeled `app.kubernetes.io/name=traefik` owned by DaemonSet `traefik` may set `hostPort`, only the values {80, 443, 2222}, never `hostNetwork`, `privileged`, `hostPath`, or added capabilities beyond `NET_BIND_SERVICE` (not needed: the container binds 8000/8443).
- Optional Forgejo SSH (direct mode, `cluster-vars.FORGEJO_SSH_PUBLIC: "true"`, plan 16): entrypoint `ssh: {port: 2222, hostPort: 2222, expose: {default: false}, protocol: TCP}` and `IngressRouteTCP forgejo/forgejo-ssh-public` (`entryPoints: [ssh]`, `routes: [{match: HostSNI(\`*\`), services: [{name: forgejo-ssh, port: 22}]}]`). In tunnel mode this is impossible by construction (no hostPort; cloudflared has no TCP rule).

### 9.4 Direct-ingress mode (ADR-0151)

- **When:** the operator has no Cloudflare account, wants no third-party TLS termination, or Cloudflare is down for a long period (switch procedure in plan 10 §20.3).
- **Traffic path:** client → `A <host>` (each node's public IPv4; small-ha lists three records) → node 80/443 → CNI portmap DNAT → Traefik pod on that node (DaemonSet: one per node) → backend Service.
- **DNS:** Cloudflare DNS-only records by default (`sc-bootstrap dns apply`). Without Cloudflare (`ingress.dnsProvider: manual`, plan 50 assumed field), the operator creates the A records shown by `./bootstrap exec -- sc-bootstrap dns plan`, and preflight checks resolution instead of the API (PF-20 variant).
- **Certificates:** ACME DNS-01 (Cloudflare, rfc2136 or route53 solver) or HTTP-01 with per-host SANs (plan 10 §9.2).
- **Node failure at small-ha:** clients using a dead node's A record fail until DNS is changed (`remove-node.md` / `lost-node.md` run `sc-bootstrap dns apply` with the surviving nodes). Cloudflare DNS-only offers no health checks; this is the accepted direct-mode limitation.

### 9.5 Kubernetes API, control interfaces and SSH

- The API is reachable only per plan 08 §9.4 (peers, pod CIDR, loopback). No Service of type LoadBalancer/NodePort exists for it; Traefik cannot route to `default/kubernetes` (`allowCrossNamespace: false`, plus the F11 check in §8.2).
- Operators use `./bootstrap shell` or `./bootstrap exec -- kubectl …`, which go through the SSH forward `127.0.0.1:16443` (plan 50 §14.4).
- Longhorn UI, Flux, the Runner admin API (loopback-only), the Model Gateway and the egress proxy are never exposed. Prometheus and Alertmanager are exposed only behind Authentik forward-auth (plan 15 §7.2).
- Forgejo SSH: internal Service `forgejo-ssh:22` → pod 2222 for Flux, the Runner and workers (plan 16). Public SSH only in direct mode when opted in (§9.3).

### 9.6 Knobs

| Knob | Git path | Default | Who may change |
|---|---|---|---|
| `INGRESS_MODE` | `cluster-vars` (from `bootstrap.env`) | `tunnel` | PR + mode-switch procedure (plan 10 §20.3) |
| Public hosts | `config/public-hosts.yaml` | 7 platform hosts | PR (+ `sc-bootstrap dns apply`) |
| `FORGEJO_SSH_PUBLIC` | `cluster-vars` | `"false"` | PR; direct mode only |
| Traefik / cloudflared replicas | profile overlays | 1 / 1 (minimum); 2 / 2 (small-ha, tunnel) | plan 47 |
| CoreDNS replicas | Ansible inventory | `min(2, servers)` | automatic |
| `rewriteStyle` | `sc-gitops gen` flag recorded in `public-hosts.yaml` `spec.rewriteStyle` | `regex` | PR |

## 10. Secrets

| Secret class | Role | Notes |
|---|---|---|
| `SC-DNS-API` (owner plan 10) | consumed by `sc-bootstrap tunnel ensure` and `dns apply` (toolbox only; never in-cluster; the in-cluster ACME token is the separate class `SC-DNS-ACME`, plan 10, AUTH-13) | Permissions in §9.2. Plan 50 currently labels `CLOUDFLARE_API_TOKEN` as `SC-DNS-TUNNEL`; the register (`SC-DNS-API`) wins (open question Q-09-03) |
| `SC-DNS-TUNNEL` (owner plan 09) | produced by `sc-bootstrap`, consumed by cloudflared (Secret `ingress/cloudflared-tunnel`, key `token`) | Rotation: `sc-bootstrap tunnel rotate-token` (Cloudflare API token reset, verify), SOPS update PR, rollout; old connectors drop. Fencing at black start (ADR-0554) rotates it |

No new secret class.

## 11. Storage

None. CoreDNS, Traefik and cloudflared are stateless. Traefik keeps no ACME storage (cert-manager does issuance).

## 12. Resource requirements

| Component | self-hosting-minimum | small-ha |
|---|---|---|
| CoreDNS | 1 × (50m / 70Mi req, 170Mi limit) | 2 × same |
| Traefik | 1 × (50m / 96Mi req, 256Mi limit) | tunnel: 2 × same; direct: 3 (DaemonSet) |
| cloudflared | 1 × (20m / 48Mi req, 128Mi limit) | 2 × same |
| flannel/WireGuard | inside the k3s process (plan 07 kube-reserved) | same |

## 13. Security boundary

- **Public surface:** only Traefik `websecure` (and `web` for redirects/ACME in direct mode, and opt-in SSH 2222). Everything else is internal.
- **Tunnel mode trust:** Cloudflare terminates client TLS and re-encrypts to Traefik with full verification (`originServerName`, public CA). Cloudflare can see plaintext traffic: accepted per SPEC §23 ("MAY terminate public-facing TLS"); operators who reject this use direct mode.
- **Direct mode:** `ingress` PSA is `privileged` (deviation from master plan §5 `baseline`, recorded in ADR-0151 and open question Q-09-01). The VAP (plan 37) narrows it to Traefik hostPorts only. Tunnel mode keeps `ingress` at `baseline`.
- **Header trust:** `X-Forwarded-*` trusted only from the pod CIDR in tunnel mode and from nobody in direct mode. Authentik forward-auth (plan 15) takes identity headers only from its in-cluster outpost.
- **NetworkPolicy** (plan 36): Traefik ingress from cloudflared (tunnel) or `ipBlock 0.0.0.0/0` (direct), from in-cluster OIDC clients; egress only to backend Services listed in `public-hosts.yaml` namespaces, Authentik outpost, DNS and the API server. cloudflared egress to the Cloudflare edge and Traefik only.
- **CoreDNS** is reachable on 53 from every pod (it must be); its upstream is the node resolver. Rewrite cannot redirect names outside the listed labels.
- **WireGuard** keys are generated and rotated by flannel per node start (verify); no secret class.

## 14. Happy path

### 14.1 Install

1. **configure (P03):** plan 07's cluster file (wireguard-native, CIDRs) and the CoreDNS `.skip` + `sc-coredns.yaml` are placed before k3s starts. Ansible `network` role verifies (§14.3).
2. **Seed preparation (plan 50, toolbox):** tunnel mode → `sc-bootstrap tunnel ensure` (idempotent: finds the tunnel by name `sc-<cluster>`, creates it if absent, fetches the token, writes `SC_TUNNEL_ID` and the SOPS file). Both modes → `sc-gitops gen public-hosts`, then `sc-bootstrap dns apply` (records for every host). All written files are part of the seeded commit.
3. **Seed (P04):** layers `coredns-config`, `traefik`, then `cloudflared` (tunnel mode) reconcile (plan 19 steps 7–8). Traefik serves its default self-signed certificate until plan 10's `public-wildcard` is Ready, then picks it up from the TLSStore without restart.
4. **Edge check:** plan 19's seed-complete condition 5: `curl -fsS https://git.<domain>/api/healthz` from the toolbox returns 200 with a publicly valid certificate.

### 14.2 Steady state

Adding an app host: PR edits `public-hosts.yaml` + generated files (CI enforces), merge, Flux reconciles cloudflared and CoreDNS ConfigMaps (cloudflared restarts via a config-hash annotation; CoreDNS `reload` picks up the ConfigMap within ≈ 2 min), operator runs `./bootstrap exec -- sc-bootstrap dns apply`.

### 14.3 Verification (`network` role and P03/P04 gates)

- `ip -d link show flannel-wg` shows `wireguard`; `wg show flannel-wg peers | wc -l` = servers − 1;
- `kubectl -n kube-system get deploy coredns -o jsonpath='{.metadata.labels}'` shows no `objectset.rio.cattle.io/hash` from the packaged addon (it is ours), and the ConfigMap Corefile contains `import /etc/coredns/custom/*.override`;
- from a probe pod: `getent hosts git.<domain>` returns the ClusterIP of `ingress/traefik`; `getent hosts kubernetes.default.svc.cluster.local` returns `10.43.0.1`.

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| All cloudflared connections down (Cloudflare outage, token revoked, egress blocked) | `SCCloudflaredTunnelDown`; `SCEdgeDown` | humans cannot reach UIs; in-cluster operation continues: Flux, Runner and workers use internal names, in-cluster OIDC uses the CoreDNS rewrite, and kubelet image pulls from `git.<domain>` go to the pinned Traefik ClusterIP through the node `/etc/hosts` entry (CD-21, plan 08), never through the edge | kubelet restarts on liveness | `edge-ingress-failure.md` (catalogue reference; see Q-09-04); prolonged → switch to direct mode (plan 10 §20.3) |
| One of two cloudflared replicas down (small-ha) | `kube_deployment_status_replicas_unavailable` | none | restart | — |
| Traefik down | `SCTraefikDown`; `SCEdgeDown` | public and in-cluster OIDC calls fail (Authentik SSO breaks; break-glass local logins via port-forward still work) | restart; HelmRelease rollback (controller class) | `edge-ingress-failure.md` |
| CoreDNS down | `SCCoreDNSDown` | cluster-wide name resolution fails; reconcile and app traffic break | Deployment restart; at small-ha the second replica serves | §20.2 |
| Rewrite broken (ConfigMap typo, `answer auto` unsupported) | blackbox in-cluster DNS probe `probe_success{probe="dns-rewrite"}` → `SCDNSRewriteBroken` | in-cluster OIDC goes out through the edge (works in tunnel mode via hairpin, slower) or fails (direct mode without hairpin NAT) | CoreDNS keeps last good config on parse error (`reload` rejects invalid, verify) | revert PR |
| WireGuard peer stale (small-ha) | `SCWireGuardPeerStale` | cross-node pod traffic to that node fails | none | `lost-node.md` |
| DNS records drift (manual change in Cloudflare) | `blackbox-public` probes fail for one host → `SC*PublicEndpointDown` | host unreachable | none | `sc-bootstrap dns apply` |
| Direct-mode node lost | public probes for that node's IP fail intermittently | ≈ 1/3 of new client connections fail | none | `lost-node.md` (DNS update step) |
| hostPort conflict (another pod takes 80/443) | VAP denies at admission | — | admission denial | — |

## 16. Retry behavior

- cloudflared reconnects to the edge with its own exponential backoff; the liveness probe restarts it after 60 s without a ready connection.
- `sc-bootstrap tunnel ensure` and `dns apply`: Cloudflare API calls retry 3 times with 2/4/8 s backoff on 429/5xx; 4xx (permission) fails immediately with the missing permission named.
- Flux retries layers every 2 min (plan 18).

## 17. Idempotency behavior

- `tunnel ensure` looks up by name before creating. Two tunnels with the same name → fails `TUNNEL_AMBIGUOUS` (never guesses).
- `dns apply` computes a diff against records it owns (comment marker) and only upserts. Records without the marker that collide with a desired name → `DNS_RECORD_CONFLICT` (operator decides).
- Generated files are deterministic (sorted hosts); regeneration with unchanged input is byte-identical.
- Ansible CoreDNS render: unchanged input → no file change → no k3s re-apply.

## 18. Bootstrap behavior

Covered in §14.1. Black start (plan 45): tunnel credentials are **rotated** (fencing, ADR-0554) by `sc-bootstrap tunnel rotate-token` before seed; DNS records are re-applied (direct mode: new node IPs).

## 19. Upgrade behavior

- Traefik, cloudflared: Renovate PR → CI (render, F11 checks, k3d smoke) → merge → Flux (plan 48). Traefik CRD changes ship with the chart (`crds: CreateReplace`).
- CoreDNS: follows the pinned k3s version's bundled CoreDNS (plan 48 k3s upgrade updates `versions/k3s.lock.yaml` `coredns:` and Ansible re-renders `sc-coredns.yaml`).
- Mode switch tunnel ↔ direct: plan 10 §20.3 and ADR-0151.

## 20. Recovery behavior

### 20.1 Edge down

Tunnel mode, short outage: wait; internal operations continue (including image pulls, CD-21). **Human access while the edge is down (AUTH-11a):** `./bootstrap edge-bypass` (plan 50 adds the command) runs, inside the toolbox, a port-forward of `svc/traefik` in `ingress` to local port 443 (published to the operator host) and prints temporary `/etc/hosts` lines mapping every host of `public-hosts.yaml` to `127.0.0.1`. The browser then reaches every UI under its real hostname, with the real public certificate and SNI, so Authentik SSO, the `sc` brand and every OIDC redirect work unchanged. A plain port-forward to an application Service is **not** equivalent (wrong Host header, wrong brand). The command removes nothing on the operator host by itself; the procedure tells the operator to delete the lines afterwards.

Long Cloudflare outage (> 4 h): switch to direct mode (plan 10 §20.3) **if** the DNS zone is still manageable. If Cloudflare also hosts the DNS zone and the zone or account is unusable (AUTH-11b), the only path is a delegation change at the domain **registrar** to another DNS provider (rfc2136 or Route 53 solver), described in `edge-ingress-failure.md` §F and plan 10 §20.2; propagation can take up to 48 h, and the remaining validity of `public-wildcard` is the budget. The registrar account is operator-held, off-cluster recovery material (plan 35 to register a class; SI proposed).

### 20.2 CoreDNS loss

`kubectl -n kube-system rollout restart deploy/coredns`. If the Deployment is gone (manifest deleted by mistake): `./bootstrap configure --tags network` re-places `sc-coredns.yaml`; k3s re-applies it within 1 min.

## 21. Observability

Scrapes (plan 38): `traefik` (9100), `cloudflared` (2000), `coredns` (9153). Blackbox: `blackbox-public` HTTPS probes per public host (`probe_kind="public"`), in-cluster DNS probe `dns-rewrite` (query `git.<domain>` from `monitoring`, expect Traefik ClusterIP).

Textfile metrics (added to plan 08's `sc-node-facts`): `sc_flannel_wg_peers` (gauge), `sc_flannel_wg_latest_handshake_seconds{peer_node}` (peer node name; ≤ 4 series per node).

**Alerts introduced (plan 42 to register):**

| alertname | Expression sketch | for | Severity / class | Runbook |
|---|---|---|---|---|
| `SCCloudflaredTunnelDown` | `sum(cloudflared_tunnel_ha_connections{job="cloudflared"}) == 0 or absent(up{job="cloudflared"} == 1)` (metric name verify) | 5m | critical / page (tunnel mode only) | `edge-ingress-failure.md` |
| `SCTraefikDown` | `sum(up{job="traefik"}) == 0` | 3m | critical / page | `edge-ingress-failure.md` |
| `SCCoreDNSDown` | `sum(up{job="coredns"}) == 0` | 2m | critical / page | `lost-node.md` §CoreDNS (this plan §20.2) |
| `SCDNSRewriteBroken` | `probe_success{probe="dns-rewrite"} == 0` | 15m | warning / notify | revert last `public-hosts` PR |
| `SCWireGuardPeerStale` | `time() - sc_flannel_wg_latest_handshake_seconds > 600` (small-ha) | 10m | critical / page | `lost-node.md` |

Existing catalogue alerts whose signal this plan owns: `SCEdgeDown`, `SC*PublicEndpointDown`.

## 22. Testing strategy

- **Static (F11/F12):** render both ingress components; F11 checks `ingress-allowlist`, `public-hosts-generated`, `no-noTLSVerify`, `tunnel-catchall-404` (last cloudflared rule is `http_status:404`); golden test of the generator output for a fixture host list (regex and exact styles).
- **k3d (F12, plan 19 harness):** Traefik + a fake backend + the CoreDNS rewrite ConfigMap applied to k3d's CoreDNS (k3d ships k3s CoreDNS with the custom import): a pod resolves `git.example.test` to Traefik and gets the backend response with SNI `git.example.test`.
- **Lab (plan 05 VMs):** three VMs, wireguard verification (08-AC-9), direct mode with a lab DNS zone (rfc2136 to a lab BIND in the lab host network, plan 10), hostPort reachability on every node.
- **Live (P03/P04):** gate items below.

## 23. Acceptance criteria

### GATE-P03

- **09-AC-1** `./bootstrap exec -- ssh node-01 'ip -d link show flannel-wg'` output contains `wireguard`. `kubectl get nodes -o jsonpath='{.items[*].metadata.annotations.flannel\.alpha\.coreos\.com/backend-type}'` prints `extension` or `wireguard-native` (value verify on the pinned k3s).
- **09-AC-2** `kubectl get node node-01 -o jsonpath='{.spec.podCIDR}'` is inside `10.42.0.0/16`, and `kubectl get svc -n default kubernetes -o jsonpath='{.spec.clusterIP}'` = `10.43.0.1`, and `kubectl -n kube-system get svc kube-dns -o jsonpath='{.spec.clusterIP}'` = `10.43.0.10`.
- **09-AC-3** A probe pod reports `cat /sys/class/net/eth0/mtu` = uplink MTU − 80 (1420 on a 1500 uplink).
- **09-AC-4** `kubectl -n kube-system get deploy coredns -o jsonpath='{.spec.replicas}'` = `min(2, servers)` and `kubectl -n kube-system get cm coredns -o jsonpath='{.data.Corefile}'` contains `import /etc/coredns/custom/*.override`.
- **09-AC-5** `./bootstrap exec -- ssh node-01 'ls /var/lib/rancher/k3s/server/manifests/coredns.yaml.skip'` exits 0, and after `sudo systemctl restart k3s` the CoreDNS Deployment's `metadata.uid` is unchanged (re-verifies that k3s honors the `.skip` marker and does not re-apply its packaged manifest).
- **09-AC-6** After the seed applies `coredns-custom`: `kubectl run dns-probe --rm -i --restart=Never --image=<busybox digest> -- nslookup git.<domain>` returns the ClusterIP of `ingress/traefik` and the answer section's name is `git.<domain>` (re-verifies `answer auto`; on failure the generator switches to `--rewrite-style=exact` and the check is repeated).
- **09-AC-7** `nslookup _acme-challenge.<domain>` from the same pod is **not** rewritten (returns the public answer or NXDOMAIN, never the Traefik IP).

### GATE-P04

- **09-AC-8** `kubectl -n ingress get helmrelease traefik -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}'` = `True`; Service `ingress/traefik` has ≥ 1 ready endpoint; `kubectl get ingressclass traefik -o jsonpath='{.metadata.annotations.ingressclass\.kubernetes\.io/is-default-class}'` = `true`.
- **09-AC-9** Tunnel mode: `kubectl -n ingress get deploy cloudflared -o jsonpath='{.status.availableReplicas}'` ≥ 1, and `kubectl -n ingress exec deploy/cloudflared -- wget -qO- http://127.0.0.1:2000/ready` returns HTTP 200 (or the equivalent check through a port-forward when the image has no shell).
- **09-AC-10** From the toolbox: `curl -fsS -o /dev/null -w '%{http_code} %{ssl_verify_result}' https://git.<domain>/api/healthz` prints `200 0`, and `openssl s_client -connect git.<domain>:443 -servername git.<domain> </dev/null 2>/dev/null | openssl x509 -noout -issuer` names the public ACME CA in direct mode or a Cloudflare edge certificate in tunnel mode.
- **09-AC-11** `curl -s -o /dev/null -w '%{http_code}' https://nonexistent-host.<domain>/` returns `404` in tunnel mode (catch-all) or fails DNS resolution.
- **09-AC-12** `kubectl get cm -n ingress cloudflared-config -o yaml | grep -c noTLSVerify` = 0, and the rendered Traefik Deployment/DaemonSet args contain `--providers.kubernetescrd.allowCrossNamespace=false` (re-verifies chart key names).
- **09-AC-13** Direct mode only: `kubectl -n ingress get ds traefik -o jsonpath='{.spec.template.spec.containers[0].ports[*].hostPort}'` contains `80 443`; `kubectl get ns ingress -o jsonpath='{.metadata.labels.pod-security\.kubernetes\.io/enforce}'` = `privileged`; a test Deployment in `ingress` with `hostPort: 8080` is denied by VAP `sc-hostport-traefik-only` (`kubectl apply --dry-run=server` returns the VAP message).
- **09-AC-14** `sc-bootstrap tunnel ensure` run twice reports `unchanged` on the second run, and the Cloudflare API shows exactly one tunnel named `sc-<cluster>` with `config_src: local` (re-verifies the API endpoints and token flow).
- **09-AC-15** From outside the cluster: `curl -sk --max-time 5 https://<node-public-ip>:6443/` fails to connect, and no public hostname routes to the API (`curl -s https://kubernetes.<domain>/` → 404 or DNS failure).
- **09-AC-16** `sc-gitops gen public-hosts --cluster <c> --check` exits 0 on the seeded commit (generated files match).
- **09-AC-18** `kubectl -n ingress get svc traefik -o jsonpath='{.spec.clusterIP}'` = `10.43.0.20` (CD-21).
- **09-AC-19** (SEC-16) From a probe pod: `nslookup auth.<domain>` returns the ClusterIP of `ingress/traefik-oidc`; `curl --resolve git.<domain>:443:<traefik-oidc IP> https://git.<domain>/` returns 404 from Traefik (no route on `oidc`), while `curl https://auth.<domain>/-/health/live/` returns 200.

### GATE-C01 (contributed; first internal image exists only after P11)

- **09-AC-17** Tunnel mode (CD-21, GC-05): with `kubectl -n ingress scale deploy/cloudflared --replicas=0` (Flux reconciliation of `cloudflared` suspended for the test), delete the Runner pod after removing its image from the node cache (`sudo k3s crictl rmi <runner image digest>`); the new pod pulls the image from `git.<domain>` and becomes Ready within 5 min. Afterwards cloudflared is resumed. Lab variant of the same test also runs `./bootstrap edge-bypass` and completes an Authentik admin login and a Forgejo SSO login through it (AUTH-11a).

## 24. Explicit non-goals

- IPv6 or dual-stack cluster networking; AAAA records in direct mode.
- Cloudflare Access/WAF configuration, Cloudflare load balancing or health-checked DNS.
- MetalLB, kube-vip or any LoadBalancer implementation; a floating API address.
- Public SSH in tunnel mode (`cloudflared access ssh`).
- Service mesh, mTLS between pods (internal TLS where needed is plan 10).
- Cilium/Calico (ADR-0011).

## 25. Open questions

- **Q-09-01** (blocking: no) Direct mode needs PSA `privileged` on `ingress` (hostPort), deviating from master plan §5 (`baseline`). *Proposed:* accept with VAP compensation (ADR-0151); master plan §5 notes "baseline (tunnel) / privileged + VAP (direct)" at consolidation. *Owner:* 09/37.
- **Q-09-02** (blocking: no) Plan 18's layer table needs `coredns-config` (seed set). *Proposed:* plan 18 adds it at consolidation. *Owner:* 18.
- **Q-09-03** (blocking: no) Plan 50 maps `CLOUDFLARE_API_TOKEN` to `SC-DNS-TUNNEL`; the register defines that as the tunnel token and uses `SC-DNS-API` for the API token. *Proposed:* plan 50 renames to `SC-DNS-API`. Plan 50 also needs `ingress.dnsProvider` (`cloudflare|manual`) and `ingress.acme.solver` (plan 10) so that "behavior without Cloudflare" is configurable; today `ingress.cloudflare` is schema-required. *Owner:* 50.
- **Q-09-04** (blocking: no) **Resolved at consolidation:** `procedures/edge-ingress-failure.md` now exists and is owned by this plan.
- **Q-09-06** (partly resolved, SEC-16) The `oidc` entrypoint (8444, `auth.` only, no forwarded-header trust) satisfies plan 24's Hermes requirement. Still deferred: a tunnel-only entrypoint so `websecure` stops trusting `X-Forwarded-*` from the pod CIDR (plan 36 Q-36-08). Plan 15 must add `oidc` to the Authentik IngressRoute. *Owner:* 09/15/36.
- **Q-09-05** (blocking: no) The plan 50 final report lists 5 public hostnames; plan 15 adds `prometheus` and `alertmanager`. *Proposed:* the report prints the `public-hosts.yaml` list. *Owner:* 50.
