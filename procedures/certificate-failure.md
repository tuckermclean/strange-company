# Procedure: Certificate failure

**Owning plan:** plans/10-tls-certificates.md
**SPEC references:** §23, §113 ("certificate failure")
**Severity / urgency:** degraded (renewal failing, cert still valid) / outage (expired)
**Expected duration:** 15–60 min (§C may need a DNS or mode switch: up to 2 h)
**Requires break-glass:** no; §K3S on a node without working SSH needs `SC-BREAK-GLASS-INFRA`

## When to use

| Alert / symptom | Section |
|---|---|
| `SCCertificateNotReady`, `SCCertificateExpiringSoon` on `ingress/public-wildcard` | §A (then §B if rate-limited) |
| `SCCertificateExpiryImminent` or an expired public certificate (browsers reject; cloudflared returns 502) | §C |
| `ClusterIssuer sc-internal-ca` not Ready; internal Certificates not Ready | §D |
| `SCInternalCAExpiringSoon`, or suspected internal CA key compromise | §E |
| `SCCertManagerDown`; Flux errors applying `Certificate` objects (webhook) | §F |
| `SCK3sCertificateExpiringSoon`; kubelet/API TLS errors | §K3S |
| `SCCnpgCertificateExpiringSoon`; PostgreSQL TLS errors | §CNPG |
| Cloudflare edge certificate problem (`probe_ssl_earliest_cert_expiry` from `blackbox-public`) | §Edge |

## Preconditions and required material
- `./bootstrap shell` (contains `kubectl`, `cmctl` (plan 02), `openssl`, `sops`).
- For §A/§C: access to the DNS provider console (Cloudflare dashboard or the rfc2136/Route 53 provider).
- For §E: recovery passphrase (to write a new SOPS secret) and Forgejo access to merge a PR.

## Safety checks before acting
1. Identify which certificate: `./bootstrap exec -- kubectl get certificate -A`, then `kubectl describe certificate -n <ns> <name>` and the newest `CertificateRequest`, `Order`, `Challenge` (`kubectl get order,challenge -A`).
2. Do not loop `cmctl renew` against `letsencrypt-prod`: each successful issuance counts against Let's Encrypt's limit of 5 duplicate certificates per week.
3. Never set `noTLSVerify` on cloudflared or skip verification in clients as a "fix" (F11 rejects it).

## Steps

### §A Public issuance or renewal failing
1. Read the Challenge status: `kubectl -n ingress describe challenge`. Common reasons:
   - `401/403` from the DNS API → the token in `cert-manager/dns-acme-token` (`SC-DNS-ACME`, plan 10 §8.1) lacks DNS › Edit on the challenge zone (or production zone) or was revoked. Rotate it (`rotate-secrets.md#sc-dns-acme`), merge the SOPS PR, wait for Flux.
   - `propagation check failed` → `dig +short TXT _acme-challenge.<domain> @1.1.1.1`; if empty, check the zone in the provider console (wrong zone, CNAME delegation of `_acme-challenge`). cert-manager uses only `1.1.1.1`/`9.9.9.9` (plan 10 §8.1); confirm the NetworkPolicy row C-2 exists (`kubectl -n cert-manager get networkpolicy`).
   - Connection errors to `acme-v02.api.letsencrypt.org` → egress problem (row C-1) or Let's Encrypt incident (status page).
2. After fixing: `./bootstrap exec -- cmctl renew -n ingress public-wildcard` **once**. Expected: `Ready=True` within 15 min; `SCCertificateNotReady` resolves.

### §B Rate limited
1. The Order shows `urn:ietf:params:acme:error:rateLimited` with a retry-after time.
2. Do not retry before that time. If the current certificate is still valid > 3 days, wait.
3. If it expires sooner: switch the public Certificate to per-host SANs (a different identifier set is a different "duplicate" bucket): PR setting `SC_PUBLIC_CERT_MODE=per-host` (the `public-hosts` Certificate of plan 10 §8.2, DNS-01), merge, `cmctl renew`. Revert after the rate-limit window.

### §C Public certificate expired or about to expire (< 3 days) and §A cannot fix it in time
1. Tunnel mode, Cloudflare healthy: the edge certificate is Cloudflare's, but cloudflared rejects the expired origin certificate. Fix issuance (§A/§B); there is no safe bypass. If issuance cannot be fixed within the remaining time, accept the outage and keep internal operations running (Flux, Runner and workers use internal names).
2. DNS provider unusable (credential lost, provider down) and the certificate must be renewed: if the installation runs direct mode, switch the solver to HTTP-01 by PR (`SC_ACME_SOLVER=http01`; plan 10 §20.3; Traefik serves the solver Ingress in namespace `ingress`, plan 09 AUTH-12) and renew. In tunnel mode HTTP-01 is not available; restore DNS API access, or if Cloudflare hosts a lost zone follow `edge-ingress-failure.md` §F.
3. After renewal, verify the served certificate: `openssl s_client -connect git.<domain>:443 -servername git.<domain> </dev/null | openssl x509 -noout -dates`.

### §D Internal CA issuer not Ready
1. `kubectl -n cert-manager get secret sc-internal-ca-root` → if missing, Flux failed to decrypt or apply it: `./bootstrap exec -- flux get kustomization cert-manager-config` and its events (SOPS decryption errors point to `sops-decryption-failure.md`, plan 35).
2. Compare fingerprints: `kubectl -n cert-manager get secret sc-internal-ca-root -o jsonpath='{.data.tls\.crt}' | base64 -d | openssl x509 -noout -fingerprint -sha256` vs `openssl x509 -in gitops/clusters/<c>/config/internal-ca.crt -noout -fingerprint -sha256`. Mismatch → someone changed the Secret by hand; Flux will restore it on the next reconcile (`flux reconcile kustomization cert-manager-config --with-source`).
3. Expected: `kubectl get clusterissuer sc-internal-ca` Ready; pending internal Certificates issue within 2 min.

### §E Internal CA rotation (planned or compromise)
1. `./bootstrap exec -- sc-bootstrap secrets gen internal-ca --rotate`. Expected: the SOPS file now holds root g<N+1>; `config/internal-ca.crt` contains g<N> and g<N+1>.
2. Commit on a branch, open a PR, merge. Wait for `cert-manager-config` Ready.
3. Reissue every certificate of the issuer:
   `kubectl get certificate -A -o json | jq -r '.items[] | select(.spec.issuerRef.name=="sc-internal-ca") | "\(.metadata.namespace) \(.metadata.name)"' | while read ns n; do cmctl renew -n "$ns" "$n"; done`
   Expected: each Ready again within 2 min; `openssl x509 -issuer` of each Secret names g<N+1>.
4. Second PR removing g<N> from `config/internal-ca.crt` (immediately on compromise; otherwise after 24 h). Merge.
5. Compromise only: also follow `credential-compromise.md#cluster-and-node` (plan 35).

### §F cert-manager down or webhook failing
1. `kubectl -n cert-manager get pods`; logs of the failing pod. `kubectl get validatingwebhookconfiguration cert-manager-webhook -o yaml | grep caBundle` must be non-empty.
2. Restart: `kubectl -n cert-manager rollout restart deploy/cert-manager deploy/cert-manager-webhook deploy/cert-manager-cainjector`.
3. If the HelmRelease is failed after an upgrade: `upgrade-platform-component.md` §Controller.
4. Existing certificates keep working while cert-manager is down; only issuance and object changes are blocked.

### §K3S k3s certificates near expiry
1. On each server: `./bootstrap exec -- ssh <node> 'for f in /var/lib/rancher/k3s/server/tls/*.crt; do echo "$f $(sudo openssl x509 -noout -enddate -in $f)"; done'`.
2. If any leaf expires within 90 days: k3s renews on restart. On single node, during a quiet period: `sudo systemctl restart k3s` (pods keep running). On small-ha: one server at a time, waiting for `sc-etcdctl endpoint health --cluster` between restarts.
3. If certificates already expired (API refuses): `sudo systemctl stop k3s && sudo k3s certificate rotate && sudo systemctl start k3s` per server (verify subcommand on the pinned k3s). Then re-fetch the admin kubeconfig: `./bootstrap configure --tags k3s` fetches it again (plan 50).
4. Check why the monthly restart did not happen: `journalctl -u sc-maintenance` (plan 08 §9.9).

### §CNPG CNPG certificate renewal failed
1. `kubectl -n <ns> get cluster <c> -o jsonpath='{.status.certificates.expirations}'` and the operator logs (`kubectl -n cnpg-system logs deploy/cnpg-controller-manager | grep -i cert`).
2. CNPG regenerates server and replication certificates from its CA; deleting the expiring leaf Secret (`<c>-server`, `<c>-replication`) makes the operator recreate it (verify on the pinned operator; plan 12). Do it for one Secret at a time and watch that instances reload (`kubectl cnpg status`).
3. If the CA itself is the problem, follow plan 12's procedure (`cnpg-primary-loss.md` references) — do not delete `<c>-ca` without plan 12 guidance.

### §Edge Cloudflare edge certificate
1. Check the Cloudflare dashboard (SSL/TLS › Edge Certificates) for the Universal SSL status of `<domain>` and `*.<domain>`.
2. If Cloudflare cannot issue (CAA records blocking, zone problem), fix CAA (`CAA 0 issue "letsencrypt.org"` must not exclude Cloudflare's CAs) or contact Cloudflare.
3. Prolonged edge problem: switch to direct mode (plan 10 §20.3); the origin certificate is already publicly trusted.

## Verification
- `kubectl get certificate -A` → all `Ready=True`; `SCCertificate*` alerts resolved.
- `./bootstrap verify --only V-16,V-17` passes.
- §E: every `sc-internal-ca` leaf issued by g<N+1>; §K3S: `SCK3sCertificateExpiringSoon` resolved; §CNPG: `SCCnpgCertificateExpiringSoon` resolved.

## Rollback / abort
- §B: revert the per-host PR after the rate-limit window.
- §E: before step 4, reverting the first PR restores the previous root; leaves already reissued under g<N+1> must be reissued again.
- Mode switches: plan 10 §20.3 in reverse.

## Evidence to record
Certificate names, failure reason (Challenge status), actions, timestamps, issuer and serial before/after. Attach to the incident issue.

## Follow-ups
- If the DNS token was the cause, schedule its rotation reminder (register `maxAge: P180D`).
- Add a lab regression if a new failure type was found.
