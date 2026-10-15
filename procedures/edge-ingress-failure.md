# Procedure: edge ingress failure (cloudflared, Traefik, public DNS)

**Owning plan:** plans/09-networking.md (assigned at consolidation; Q-09-04)
**SPEC references:** §18, §20, §23, §97
**Severity / urgency:** outage for humans. Internal operation continues: Flux, the Runner, workers and CI all use in-cluster names, and kubelet image pulls from `git.<domain>` go to Traefik's pinned ClusterIP through the node `/etc/hosts` entry (CD-21), not through the edge.
**Expected duration:** 10–30 min for a pod or config fault; up to DNS TTL plus 30 min for a switch to direct mode.
**Requires break-glass:** no for diagnosis (`./bootstrap shell` uses the operator SSH key). Logging into UIs while the edge is down uses `./bootstrap edge-bypass` (plan 09 §20.1), which keeps real hostnames, certificates and SSO; local break-glass accounts (`break-glass-access.md`) are needed only if Authentik itself is down. §F needs the operator-held **domain registrar** account.

## When to use
- `SCEdgeDown`: every public blackbox probe is failing.
- `SCCloudflaredTunnelDown` (tunnel mode), `SCTraefikDown`.
- `SC*PublicEndpointDown` for one host, e.g. `SCForgejoPublicEndpointDown`.
- Symptoms: browsers get Cloudflare error 1033/530 (tunnel has no connector), 502/504 (origin unreachable), or a connection refused or timeout (direct mode).

Certificate errors are handled by `certificate-failure.md`, not here; for the edge certificate see its §Edge. Cluster DNS failures (`SCCoreDNSDown`, `SCDNSRewriteBroken`) follow plan 09 §20.2.

## Preconditions and required material
- Operator workstation with the repository, `bootstrap.env` and the operator SSH key. `./bootstrap shell` reaches the API through the SSH forward (plan 50).
- For a mode switch: `SC-DNS-API` (Cloudflare API token, toolbox only, or the operator's DNS provider access) and a PR path to Forgejo. Use `./bootstrap edge-bypass` or the internal path below if Forgejo's public URL is down.
- For §F: the registrar account and an account at the replacement DNS provider (rfc2136 server or Route 53), plus its ACME credential (`SC-DNS-ACME`).

## Safety checks before acting
1. Confirm the problem is the **edge**, not the backend. In-cluster probes must be green:
   `./bootstrap exec -- kubectl -n monitoring exec deploy/blackbox-exporter -- wget -qO- 'http://localhost:9115/probe?module=http_2xx&target=http://forgejo-http.forgejo.svc:3000/api/healthz'` returns `probe_success 1`.
   If internal probes fail too, follow the component's own failure procedure (e.g. `restore-forgejo.md`).
2. Do **not** delete the Cloudflare tunnel or rotate its token as a first step. A token rotation also needs a SOPS commit and a Flux reconcile.
3. Do **not** expose Traefik with a NodePort or LoadBalancer Service as a shortcut. VAP `sc-no-nodeport-lb` denies it, and it would bypass the firewall design (plan 08).

## Steps

### §A Classify
1. `./bootstrap exec -- kubectl -n ingress get pods -o wide` and `./bootstrap exec -- kubectl -n ingress get events --sort-by=.lastTimestamp | tail -30`.
   - Expected: `traefik-*` Running, plus `cloudflared-*` Running in tunnel mode.
   - Traefik not Running → §B.
   - cloudflared not Running, or Running but `SCCloudflaredTunnelDown` → §C.
   - All Running → §D.

### §B Traefik down
1. `./bootstrap exec -- flux -n ingress get helmreleases traefik`. If `Ready=False` after an upgrade, the controller-class remediation rolls back automatically (plan 18). Wait 5 min.
2. Still failing: inspect `kubectl -n ingress logs deploy/traefik --previous` (DaemonSet `traefik` in direct mode). Common causes:
   - Invalid `IngressRoute` or TLSStore after a merge: revert the offending PR in Forgejo. Use the in-cluster path if `git.` is down: `./bootstrap exec -- sc-bootstrap forge api` or a port-forward to `forgejo-http:3000`. Then `flux reconcile kustomization <layer> --with-source`.
   - Missing `public-wildcard-tls` Secret: run `certificate-failure.md` §A/§C.
   - OOMKilled: raise the limit by PR (plan 51 budget). As a stopgap, `flux suspend helmrelease traefik -n ingress` and `kubectl -n ingress set resources …`, then resume after the PR merges.
3. Expected result: `SCTraefikDown` resolves, and in-cluster OIDC calls succeed again (Authentik SSO works).

### §C cloudflared down or disconnected (tunnel mode)
1. `./bootstrap exec -- kubectl -n ingress logs deploy/cloudflared --tail=100`.
   - `Unauthorized` / `invalid token`: the token was revoked or rotated outside Git. Run `./bootstrap exec -- sc-bootstrap tunnel rotate-token`. It writes a new SOPS file; commit and push via Forgejo's in-cluster path, then `flux reconcile kustomization ingress --with-source`.
   - `failed to dial … 7844` / timeouts: egress is blocked. Check that the cloudflared egress rule I-1 from plan 36 §8 is present (`kubectl -n ingress get networkpolicy`) and the host firewall forward chain (plan 08). If the Cloudflare status page shows an incident, wait (§E).
   - Crash loop on config: the `cloudflared-config` ConfigMap is invalid. Revert the last `public-hosts.yaml` PR.
2. `kubectl -n ingress rollout restart deploy/cloudflared`, then `kubectl -n ingress exec deploy/cloudflared -- wget -qO- localhost:2000/ready` should return HTTP 200 within 2 min.
3. Expected result: `cloudflared_tunnel_ha_connections ≥ 1` and the public probes recover.

### §D Pods healthy, public access failing
1. DNS: `./bootstrap exec -- sc-bootstrap dns plan`. It must report no drift. If records drifted (manual edits), run `./bootstrap exec -- sc-bootstrap dns apply`. It only upserts records it owns; a `DNS_RECORD_CONFLICT` needs an operator decision.
2. Direct mode: from outside, `curl -sv https://git.<domain>/api/healthz --resolve git.<domain>:443:<node-ip>` against each node IP. A failing node points to a node or firewall problem: run `./bootstrap exec -- ssh <node> 'sudo nft list table inet sc_host'` and compare with plan 08 §9.4. Lost node → `lost-node.md` (it includes the DNS update).
3. Tunnel mode: check the Cloudflare dashboard. The tunnel `sc-<cluster>` should show healthy connectors and the hostnames should be routed. If the dashboard shows the tunnel as deleted, re-run `./bootstrap exec -- sc-bootstrap tunnel ensure` (idempotent; a new tunnel ID is written to `cluster-vars.SC_TUNNEL_ID` by PR), then `dns apply`.

### §E Prolonged Cloudflare outage (> 4 h, or the operator decides), DNS zone still manageable
1. Human access meanwhile: `./bootstrap edge-bypass`, then add the printed `/etc/hosts` lines on the operator machine. Expected: `https://auth.<domain>/` and `https://git.<domain>/` open with a valid certificate, and Authentik SSO into Forgejo works. Remove the lines when done.
2. Follow plan 10 §20.3, "Switching tunnel ↔ direct", with `INGRESS_MODE=direct`. The DNS-01 certificate is unchanged, so this is a routing change only. If no human can merge in Forgejo (Authentik also down), use plan 15's identity-outage change path or plan 55's `emergency-merge.md`.
3. `sc-bootstrap dns apply` must succeed against the zone. If the Cloudflare API is unreachable or the account is lost, go to §F.

### §F Cloudflare hosts the zone and the zone or account is unusable (AUTH-11b)
1. Note the remaining validity of `public-wildcard` (`kubectl -n ingress get certificate public-wildcard -o jsonpath='{.status.notAfter}'`). That is the time budget; renewal needs the new DNS provider live.
2. Create the zone at the replacement provider (rfc2136-capable server or Route 53). Load the records printed by `./bootstrap exec -- sc-bootstrap dns plan --provider manual --mode direct` (A records for every public host pointing to every node IPv4).
3. At the **registrar**, change the NS delegation of `<domain>` to the new provider. Expected propagation: minutes to 48 h. Check with `dig NS <domain> @1.1.1.1`.
4. PR: `INGRESS_MODE=direct`, `SC_ACME_SOLVER=dns01-rfc2136` (or `dns01-route53`) with its variables, and the new `SC-DNS-ACME` SOPS secret (plan 10 §8.1). Merge (use the identity-outage path if needed).
5. Once `dig NS` shows the new provider from 1.1.1.1, run `./bootstrap exec -- cmctl renew -n ingress public-wildcard` once. Expected: `Ready=True` within 15 min.
6. Until step 5, browsers keep working through the direct-mode A records with the existing certificate; humans can also use `./bootstrap edge-bypass`.

## Verification
- All `probe_success{probe_kind="public"} == 1` for 10 min; `SCEdgeDown`, `SCTraefikDown` and `SCCloudflaredTunnelDown` resolved.
- `./bootstrap verify --only V-17` passes (public hostnames through the edge, plan 50).
- Authentik login to Forgejo and Grafana succeeds through the public URL.

## Rollback / abort
- Any suspended HelmRelease is resumed (`flux resume helmrelease traefik -n ingress`) once the PR fix is merged.
- A direct-mode switch can be reverted with the same plan 10 §20.3 procedure. A registrar delegation change (§F) is reverted only by another registrar change; keep the old zone until the new one has served for 7 days.

## Evidence to record
- Alert timeline, root cause, the PR(s) or commands used, and the outage duration. Record it in the incident log (`sc-bootstrap evidence put`).

## Follow-ups
- If the cause was a manual change in Cloudflare, add the drifted object to the `dns plan` drift check.
- If the edge was down for more than 1 h, review whether to add a second cloudflared replica (small-ha) or move to direct mode permanently.
