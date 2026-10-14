# Procedure: Seed-to-Flux handoff (seed → forge-init → source switch)

**Owning plan:** plans/19-seed-handoff.md (mechanics also in plans/18-flux.md, plans/16-forgejo.md, plans/50-bootstrap-orchestrator.md)
**SPEC references:** §8, §34, §35, §113 ("seed-to-Flux handoff")
**Severity / urgency:** routine. It is part of every fresh install and black start.
**Expected duration:** seed 20–45 min, forge-init 5–20 min, source switch 5–30 min
**Requires break-glass:** no. It uses the bootstrap kubeconfig and the staged Forgejo admin credential from `.bootstrap/` or the recovery bundle.

## When to use
- During `./bootstrap` (fresh install) or `./bootstrap recover` (black start). The phases `seed`, `forge-init` and `flux-bootstrap` run automatically. Use this procedure to **watch** them, to **resume** them after an interruption, or to **run them one at a time** when the operator wants a checkpoint between phases.
- When `./bootstrap status` shows `seed`, `forge-init` or `flux-bootstrap` as `pending`, `running` or `failed`.
- Not for failures after the source switch has started. If the journal shows `TRANSFER_*` codes or `transfer.switched`, go to `failed-flux-handoff.md`.

## Preconditions and required material
- A clean working tree at the commit to be seeded (`git status --porcelain` is empty). The cluster directory `gitops/clusters/<cluster>/` is committed (plan 50 generates it).
- `bootstrap.env` (or the recovery bundle and passphrase for black start). It provides the object-storage write credential for `<prefix>-bootstrap-seed` (stays on this machine) and the read-only `seed-read` credential.
- The recovery bundle and passphrase. `sc-bootstrap` stages the cluster age identity in tmpfs only, at `/run/sc/secrets/age-cluster.agekey` inside the toolbox (ADR-0101, CD-08); it is never written to `.bootstrap/`.
- The kubeconfig from `configure` (P03). `./bootstrap status` shows `configure: succeeded`.
- Docker or Podman. All tools come from the toolbox (`./bootstrap shell` or `./bootstrap exec -- <cmd>`).

## Safety checks before acting
1. `./bootstrap status`. Expected: every phase before `seed` is `succeeded`, and no other run holds the run lock. If another run holds the lock, **stop**. Two concurrent runs against one installation are unsafe. Follow `bootstrap-resume.md`.
2. `git status --porcelain` prints nothing, and `git rev-parse HEAD` is the commit you intend to install. The seed refuses a dirty tree (`SEED_DIRTY_TREE`).
3. `./bootstrap exec -- kubectl get nodes` shows all expected nodes `Ready`.
4. **Do not**:
   - `helm install` any component by hand;
   - `kubectl apply` any `gitops/` manifest by hand;
   - create Secrets other than the two out-of-band ones;
   - push to Forgejo `main` yourself between `seed` and `flux-bootstrap` (the transfer requires Forgejo `main == S`).
   Each of these breaks the "nothing seeded is recreated" guarantee.
5. Blast radius: seed creates only objects in the seed set (plan 18 §8.3). A failed seed leaves a partially reconciled cluster that Flux keeps reconciling. It does not affect anything outside the new cluster.

## Steps

1. **Start or resume the seed.**
   `./bootstrap --from-phase seed` (or plain `./bootstrap`, which resumes from the journal).
   Expected console output:
   - `U1..U6` upload lines;
   - `artifact-integrity: pass`;
   - per-layer progress every 30 s (`layer=<name> ready=<bool> reason=<r> elapsed=<t>`);
   - finally `SEED COMPLETE <S>`, where `<S>` equals `git rev-parse HEAD`.
   - If the output is `SEED_SNAPSHOT_CONFLICT` or `SEED_ARTIFACT_MISMATCH`, stop and go to `failed-flux-handoff.md` §A.
   - If it is `SEED_UPLOAD_FAILED` or the Bucket is not Ready, check object storage (`object-storage-outage.md`), then re-run this step. It is idempotent and skips already-uploaded identical snapshots.
   - If it is `SEED_LAYER_TIMEOUT:<layer>`, go to **Diagnose** below.

2. **Check the seed gate independently.**
   - `./bootstrap exec -- flux get kustomizations -n flux-system`. Expected: every seed layer `Ready True` with a revision of the form `sha256:…` (Bucket).
   - `./bootstrap exec -- kubectl get secrets -A -l strange.company/out-of-band=true -o name`. Expected: exactly `secret/sops-age` and `secret/seed-bucket-credentials`.
   - `./bootstrap exec -- curl -fsS https://git.<BASE_DOMAIN>/api/healthz`. Expected: HTTP 200 with `"status":"pass"`.
   If any of these differs although step 1 printed `SEED COMPLETE`, record it as a defect of `sc-bootstrap` (evidence below) and go to **Diagnose**.

3. **Run forge-init.**
   `./bootstrap --from-phase forge-init`.
   Expected: every step `S00..S19` `created`, `verified` or `skipped`; `result: succeeded`; `mirror` `synced` or `pending` (`pending` only if GitHub is unreachable, see `github-mirror-failure.md`).
   - On `FORGE_MAIN_DIVERGED`, see `restore-forgejo.md` §Diverged. **Never force-push.**
   - On `FORGE_HOSTKEY_MISMATCH`, stop. The Forgejo pod is not using the committed host key. See plan 16 F8 and `rotate-forge-credentials.md` §E.

4. **Confirm the transfer preconditions** (read-only).
   - `./bootstrap exec -- flux get sources git -n flux-system`. Expected: `flux-system` `Ready True`, revision `main@sha1:<S'>`. `<S'>` is the forge-init outputs commit (a descendant of `S`), or `S` itself when forge-init committed nothing.
   - `./bootstrap exec -- sc-bootstrap forge verify`. Expected: exit 0, and the deploy key `flux-readonly` has `read_only: true`.
   - `./bootstrap exec -- flux get all -A --status-selector suspended=true`. Expected: no rows.
   If the GitRepository is not Ready after 5 min, go to `failed-flux-handoff.md` §C. Do not continue.

5. **Run the source switch.**
   `./bootstrap --from-phase flux-bootstrap`.
   Expected: T0..T7 progress, then `SOURCE SWITCH COMPLETE <S'>`.
   - If you see `TRANSFER_ROLLED_BACK`, the automatic rollback already ran. Go to `failed-flux-handoff.md` §C or §E according to the code in the journal.
   - If you see `TRANSFER_PRECONDITION:<check>` or `TRANSFER_FORGE_SHA_MISMATCH`, nothing was changed. Go to `failed-flux-handoff.md` §B or §C.

6. **Let the post-transfer layers converge.** `./bootstrap` continues with `sso-init`, `ci-init` and `verify` automatically. To watch:
   `./bootstrap exec -- flux get kustomizations -n flux-system --watch`.
   Expected: `authentik*`, `woodpecker*`, `monitoring*`, `backups`, `renovate`, `sc-db` and `strange-company` become Ready over 10–40 min. Failures there are not handoff failures. Use `flux-reconciliation-failure.md`.

### Diagnose (seed layer timeout or seed gate failure)
D1. `./bootstrap exec -- flux get kustomization <layer> -n flux-system` and `./bootstrap exec -- kubectl -n flux-system describe kustomization <layer>`. Read the `Ready` condition's reason and message:
- `BuildFailed` / `DecryptionFailed`: a problem in commit `S`. Fix it in a new commit, then **re-seed with the new commit** (D4).
- `HealthCheckFailed` / `DependencyNotReady`: continue with D2.

D2. Find the unhealthy object: `./bootstrap exec -- flux get helmreleases -A` and `./bootstrap exec -- kubectl get pods -n <component namespace>`. Typical causes:
| Cause | Where |
|---|---|
| Admission denial (`ValidatingAdmissionPolicy … denied`, `violates PodSecurity`) | plan 37 rule or component values; fix by new commit |
| `forgejo-upgrade-gate` / `longhorn-upgrade-gate` Job stuck or failed (`dial tcp …:6443: i/o timeout`, or `403 forbidden`) | the gate's API egress row (plan 36) or RBAC (plan 48) is missing; fix by new commit (SEED-2) |
| Bucket turns `Ready=False` right after `policies` (S3 timeout), or CoreDNS cannot resolve external names | the `s3-egress` / `dns-upstream` components (plan 36) do not match the configured S3 endpoint or node DNS; fix `cluster-vars` / the component by new commit (SEED-3) |
| `cluster-config` layer `namespaces "…" not found`, or a component waits for a Secret that does not exist | a cluster-dir manifest is missing from the generated `cluster-config/kustomization.yaml`; run `./bootstrap exec -- sc-gitops gen cluster-config --cluster <c>`, commit, re-seed (SEED-1) |
| `ImagePullBackOff` | pinned digest wrong or registry down (plan 23 / locks); fix by new commit or wait |
| Longhorn node not Ready (iSCSI missing) | plan 13 / plan 08 baseline; re-run `./bootstrap --from-phase configure` |
| CNPG `forgejo-pg` not Ready | `kubectl -n forgejo describe cluster forgejo-pg`; plan 12 |
| cert-manager issuer not Ready | plan 10 `certificate-failure.md` |
| cloudflared not registered | tunnel credential (plan 09) |

D3. If the cause is transient (registry hiccup, slow image pull), simply re-run `./bootstrap --from-phase seed`. Flux has kept retrying, and the phase picks up where the cluster is.

D4. **Re-seed with a new commit** (allowed only before the source switch): commit the fix locally, confirm `git status --porcelain` is empty, and run `./bootstrap --from-phase seed`. The orchestrator uploads snapshot `S2`, patches the Bucket prefix and the root path, and Flux applies only the differences (plan 19 §17). Expected: `SEED COMPLETE <S2>`.

D5. If D1–D4 do not resolve it within 2 hours, collect evidence and stop. A cluster stuck mid-seed is safe to leave. Nothing outside it depends on it yet.

## Verification
- `./bootstrap exec -- sc-bootstrap verify control-transfer` prints G1–G7 `pass` (plan 19 §8.6).
- `./bootstrap exec -- kubectl get buckets.source.toolkit.fluxcd.io -A` returns `No resources found`.
- `./bootstrap exec -- kubectl get secrets -A -l strange.company/out-of-band=true -o name` returns only `secret/sops-age`.
- The journal has `seed.complete`, `forge_init.result: succeeded` and the `source-switch` report with `result: pass` (`./bootstrap status --json | jq '.phases'`).
- At P12, `./bootstrap` prints `CONTROL TRANSFER COMPLETE` (C1–C8). That is the final proof. This procedure ends at `SOURCE SWITCH COMPLETE`.

## Rollback / abort
- **Before step 5:** abort by simply not continuing. The cluster stays sourced from the Bucket and keeps reconciling. Resume later with `./bootstrap`.
- **During or after step 5, before the Bucket is deleted (T6):** `./bootstrap handoff-rollback` returns the root to the Bucket source without deleting anything (plan 19 §14.3). See `failed-flux-handoff.md`.
- **After T6:** there is no rollback to the Bucket. Repair forward (`failed-flux-handoff.md` §D/§F).
- Destroying a half-seeded cluster is `safe-destruction.md`. It never touches the seed bucket or other recovery data.

## Evidence to record
- The seed commit `S`, the Bucket revision `R`, the forge-init outputs commit `S'`.
- The journal excerpts for `seed`, `forge-init` and `flux-bootstrap` (`./bootstrap status --json`).
- The `source-switch` report: `s3://<prefix>-bootstrap-seed/<cluster>/reports/control-transfer-source-switch-<ts>.json`.
- For any Diagnose path: the layer name, the reason and message, the fix commit, and the re-seed commit.

## Follow-ups
- If a re-seed was needed, open an issue against the owning plan of the failing layer, so the defect gets a test in `make e2e-handoff` or the component's e2e target.
- Confirm that the `SCFluxSeedSourcePresent` alert never fires after monitoring arrives (P09).
- Continue with `bootstrap-verify.md` (plan 50) for the `verify` and `control-transfer` phases.
