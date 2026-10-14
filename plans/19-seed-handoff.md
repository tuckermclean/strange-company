# 19 — Seed and control transfer

**Status:** Stage-0 plan (draft)
**DAG nodes:** P04 (primary owner of the seed gate; co-owners 12, 13, 16, 09, 10), P06 (primary owner of the control-transfer gate; plan 17 adds the mirror, plan 18 the steady-state graph), P12 (co-owner with plan 50: control-transfer verification), H04 (contributes 19-AC-14, black-start transfer)
**Related plans:** [18 Flux/GitOps tree](18-flux.md), [50 bootstrap orchestrator](50-bootstrap-orchestrator.md), [16 Forgejo](16-forgejo.md), [17 GitHub mirror](17-github-mirror.md), [14 object storage](14-object-storage.md), [12 CNPG](12-cnpg.md), [13 Longhorn](13-longhorn.md), [09 networking](09-networking.md), [10 TLS](10-tls-certificates.md), [35 secrets](35-secrets.md), [37 admission](37-admission-policy.md), [43 retention](43-retention-gc.md), [45 recovery](45-recovery.md), [52 CI strategy](52-ci-strategy.md)
**Contracts:** owns `contracts/seed-manifest.schema.yaml` and `contracts/control-transfer-report.schema.yaml`; consumes `bootstrap-journal.schema.yaml` (50) and `bootstrap-config.schema.yaml` (50).
**ADRs:** ADR-0005 (binding), ADR-0012, ADR-0016, [ADR-0300](../adr/0300-seed-source-indirection.md) (introduced by this group).
**Procedures:** [`seed-flux-handoff.md`](../procedures/seed-flux-handoff.md), [`failed-flux-handoff.md`](../procedures/failed-flux-handoff.md).

## 1. Purpose

Flux cannot source Forgejo before Forgejo exists [SPEC §34]. This plan defines:
- the **seed**: Flux, sourced from an object-storage snapshot of the exact local commit (ADR-0005), installs the minimum platform needed to run Forgejo;
- the **control transfer**: a source switch to the Forgejo `GitRepository` at the same commit, with proof that nothing seeded was recreated or reinstalled.

It also defines:
- the SPEC §35 completion checklist and its commands;
- the definition of "seeded releases are represented in normal GitOps state";
- the seed→Forgejo→Flux integration test;
- failure handling, rollback and the black-start variant.

## 2. Requirements derived from SPEC.md

1. Bootstrap includes a minimal seed phase. Stage 0 determines its exact ordering [SPEC §34].
2. Seeded resources are installable so that Flux assumes management without duplicate-resource chaos [SPEC §34].
3. Seed-to-Flux control transfer has an integration test [SPEC §34].
4. Bootstrap is complete only when all eight conditions of SPEC §35 hold. Then the program reports `CONTROL TRANSFER COMPLETE` [SPEC §35].
5. The pipeline phases `seed → forge-init → flux-bootstrap → verify → control-transfer` are preserved [SPEC §8]. Bootstrap is idempotent, resumable, observable and conservative [SPEC §8].
6. After bootstrap, Flux sources Forgejo [SPEC §6, §32, §33]. Forgejo does not depend on GitHub [SPEC §32].
7. Black start seeds from the GitHub clone [SPEC §112] (plan 45 owns the overall DR).
8. Procedures exist for seed-to-Flux handoff and failed Flux handoff [SPEC §113].
9. **19-R-1:** The seed snapshot is the `gitops/` subtree of exactly one commit `S`. A manifest records `S` and the git tree hash of `gitops/`. Before any layer is applied, the Flux artifact is proven byte-identical to that tree.
10. **19-R-2:** Exactly two secrets are created out-of-band: `flux-system/sops-age` and `flux-system/seed-bucket-credentials`; plus, only when `S3_CA_BUNDLE` is set, `flux-system/seed-bucket-ca` (the public CA certificate, `ca.crt`, for the Bucket's `certSecretRef`; not a secret value). Every other secret arrives through SOPS in commit `S`.
11. **19-R-3:** Control transfer starts only when Forgejo `main` equals `F`. `F` is the seed commit `S`, or a descendant of `S` whose commits `S..F` were made by forge-init and touch only the forge-init output paths (plan 16 §4.4). Transfer changes no seeded object's UID and adds no Helm release revision.
12. **19-R-4:** Until the Bucket source is deleted, rollback to it is a single idempotent command, and it never deletes a seeded object.

## 3. Inputs

| Input | From | Notes |
|---|---|---|
| Clean local commit `S` containing `gitops/clusters/<cluster>/` | plan 50 (cluster-dir generation + local commit) | A dirty tree fails with `SEED_DIRTY_TREE` |
| Pre-generated key material, SOPS-encrypted in `S` | plan 50 generation step, specified here (§8.2) | Flux deploy key, Forgejo SSH host keys, agent secret, etc. |
| Cluster age identity `SC-AGE-CLUSTER` (private) | staged by `sc-bootstrap` from the recovery bundle into **tmpfs only**: `/run/sc/secrets/age-cluster.agekey` inside the toolbox (ADR-0101, CD-08); never written to `.bootstrap/` or any persistent disk | Becomes `sops-age` |
| Object storage endpoint, region, bucket `<prefix>-bootstrap-seed`, write credential (operator-side) and read credential (`seed-read`) | plan 14 | Bucket versioning enabled |
| Kubeconfig for the new cluster | plan 07 (P03 output) | Toolbox-local |
| Forge-init result: repo exists, `main == F` (§8.5 T0), read-only deploy key registered | plan 16 (P05) | Journal entry `forge_init` |

## 4. Outputs

1. A Flux-managed seed set that is Ready (P04).
2. The seed snapshot at `s3://<prefix>-bootstrap-seed/<cluster>/<S>/` with `.sc-seed/manifest.json` (contract `seed-manifest.schema.yaml`).
3. After transfer: root and all layers sourced from `GitRepository/flux-system` at `main@sha1:<F>`. The `Bucket` and its credential Secret are deleted (P06).
4. The control-transfer report (contract `control-transfer-report.schema.yaml`):
   - phase `source-switch` at P06;
   - phase `final` at P12;
   - stored in the bootstrap journal and at `s3://<prefix>-bootstrap-seed/<cluster>/reports/control-transfer-<phase>-<UTC timestamp>.json`.
5. The console lines `SEED COMPLETE <S>`, `SOURCE SWITCH COMPLETE <F>` and, at P12 only, `CONTROL TRANSFER COMPLETE`.
6. The integration test suite `make e2e-handoff`.

## 5. Dependencies

**Hard (DAG):**
- P04 depends on P03 (k3s up, plan 07), F11 (tree renders, plan 18) and F12 (admission/netpol bundle validated, plans 36/37).
- P06 depends on P05 (forge-init, plan 16).
- P12 depends on P07–P11.

**Interfaces assumed from other plans:**
- **Plan 50:** runs `seed` and `flux-bootstrap` as orchestrator phases implemented by the Go packages this plan specifies (`src/internal/bootstrap/seed`, `.../handoff`). It owns journal persistence and the CLI. CLI forms used here: `./bootstrap` (resume from the journal), `./bootstrap status`, `./bootstrap verify`, `./bootstrap --from-phase <phase>`, `./bootstrap handoff-rollback`. The last is an assumed subcommand that plan 50 exposes and that maps to §14.3.
- **Plan 50:** generates the cluster dir from `gitops/clusters/_template/`, including the §8.2 key material and the generated `cluster-config/kustomization.yaml` (plan 18 §8.1), and commits it locally as `bootstrap(<cluster>): installation config` (plan 01 §6.4 form with `SC-Install-Uid`/`SC-Run-Id` trailers; BOOT-20) before the seed phase.
- **Plan 50 (BOOT-2):** phase input digests use recorded inputs, not the moving HEAD: seed = journal `seed.commit`, forge-init = `seed.commit`, flux-bootstrap = journal `forge_init.pushed_sha`. After the journal step `flux-bootstrap.switched`, the phases `seed`, `forge-init` and `flux-bootstrap` are frozen (§17).
- **Plan 50 (SEED-10):** V-01…V-08 are thin aliases that call this plan's C1–C8 code; `report.json` embeds this plan's `final` report; there is one §35 implementation.
- **Plan 16 (forge-init):**
  - creates org `strange-company` and repo `strange-company`;
  - pushes commit `S` to `main`, then N ≥ 0 outputs commits with subject `bootstrap(<cluster>): forge-init outputs` (plan 01 §6.4; plan 16 S15/S16) whose diffs touch only **forge-init output paths**: the plan 16 §4.4 list, plan 17 M5's `config/github-mirror.yaml` (written in S15, SEED-9) and the regenerated `gitops/clusters/<cluster>/cluster-config/kustomization.yaml` (plan 18 §8.1, SEED-1). The resulting head is `F` (`F == S` when nothing changed);
  - registers the public key from `flux-source-auth.sops.yaml` as a deploy key titled `flux-readonly` with `read_only: true`;
  - configures Forgejo to use the pre-generated SSH host keys (Secret `forgejo/forgejo-ssh-host-keys`);
  - exposes SSH Service `forgejo-ssh` (port 22) and HTTP Service `forgejo-http` (port 3000) in `forgejo`;
  - records the **final** pushed SHA as `forge_init.pushed_sha` (`F`) after its last push (S19; SEED-9).
- **Plan 14:** provides bucket `<prefix>-bootstrap-seed` (versioned) and a read-only credential scoped to it. The k3d test uses the SeaweedFS emulator `s3-emulator` (ADR-0205, CD-12) at `http://s3-emulator:9000` on the k3d Docker network.
- **Plans 12, 13, 16, 09, 10:** supply the seed-layer contents and their individual readiness checks (P04 co-owners).
- **Plan 37:** admission policies in the `policies` layer must admit every seed-set workload. P04 fails if they do not.
- **Plan 36 (SEED-3):** seed-time egress after the `policies` layer allows (a) the S3 endpoint actually configured (an `s3-egress` component rendered from `cluster-vars` `S3_ENDPOINT`: resolved host/CIDR and port, including private addresses and non-443 ports) for source-controller, CNPG, Longhorn and Forgejo, and (b) the node DNS upstreams (`dns-upstream` ipBlock from the node `resolv.conf`, plan 07 fact) for CoreDNS. Without this, the k3d harness (private `s3-emulator:9000`) and `existing` installations with LAN S3 or private DNS deadlock after `policies`.
- **Plans 48 and 36 (SEED-2):** every `<app>-upgrade-gate` Job (seed: `longhorn-upgrade-gate`, `forgejo-upgrade-gate`) has API egress (plan 36 row for pods labeled `strange.company/upgrade-gate`) and the RBAC verbs its first-install detection needs (`get` HelmRelease, `get/create/update` ConfigMap `upgrade-gate-<app>`, `list volumes.longhorn.io`, discovery). The k3d handoff test asserts that both gates complete with `policies` applied (A10).
- **Plan 45:** owns the recovery overlay contents and the black-start procedure. This plan provides the seed and transfer mechanics (§18.3).
- **Plan 43:** retention of seed snapshots, at least 90 days after transfer and never fewer than the last 3 snapshots per cluster.

## 6. Owned state

| State | Location | System of record | Writers |
|---|---|---|---|
| Seed snapshots + manifests | `s3://<prefix>-bootstrap-seed/<cluster>/<S>/` | object storage (versioned) | `sc-bootstrap` only (write credential stays operator-side) |
| Control-transfer reports | `s3://<prefix>-bootstrap-seed/<cluster>/reports/` + journal | object storage | `sc-bootstrap` |
| Seed/transfer phase progress | bootstrap journal (plan 50) | journal | `sc-bootstrap` |
| `Bucket/flux-system`, seed root, `Secret/seed-bucket-credentials`, `Secret/sops-age` | `flux-system` | cluster (imperative, not in Git) | `sc-bootstrap` |

Snapshots are immutable. A complete prefix (manifest present) is never overwritten.

## 7. External interfaces

- **Seed snapshot layout** (contract `seed-manifest.schema.yaml`):
  ```text
  s3://<prefix>-bootstrap-seed/<cluster>/<S>/gitops/...            # every file of `git archive <S> gitops/`
  s3://<prefix>-bootstrap-seed/<cluster>/<S>/.sc-seed/manifest.json  # uploaded last = completion marker
  ```
- **Bucket object** (Flux `source.toolkit.fluxcd.io/v1`, fields verified against Flux docs on 2026-10-13: `.spec.prefix` is supported for `generic`, `aws`, `gcp`; `.spec.secretRef` keys are `accesskey` and `secretkey`; `.spec.ignore` overrides the default exclusion list; artifact revision is `sha256:<digest over keys and etags>`):
  ```yaml
  apiVersion: source.toolkit.fluxcd.io/v1
  kind: Bucket
  metadata:
    name: flux-system
    namespace: flux-system
    labels: {strange.company/seed: "true"}
  spec:
    provider: generic
    bucketName: <prefix>-bootstrap-seed
    endpoint: <host[:port]>          # no scheme
    region: <region>
    insecure: false                  # true only for the CI emulator
    prefix: <cluster>/<S>/
    interval: 1m
    timeout: 5m
    secretRef: {name: seed-bucket-credentials}
    certSecretRef: {name: seed-bucket-ca}   # only when S3_CA_BUNDLE is set (private CA; SEED-3)
    ignore: |
      # sc-seed: no exclusions; integrity is verified against the git tree (19-R-1)
  ```
  The prefix holds only `gitops/` plus `.sc-seed/manifest.json`, and the steady `GitRepository` uses `ignore: "/*\n!/gitops/"` (plan 18 §8.4), so both artifacts carry the same `gitops/` file set with no Flux default exclusions (SEED-18).
- **Seed root** = plan 18 §8.4 root with `sourceRef: {kind: Bucket, name: flux-system}`, `path: ./<cluster>/<S>/gitops/clusters/<cluster>/seed`, `postBuild.substitute: {SC_SOURCE_KIND: Bucket, SC_SOURCE_ROOT: ./<cluster>/<S>, SC_PROFILE: <profile>, SC_CLUSTER: <cluster>}`. The Bucket artifact keeps the full object key, including the prefix, under the artifact root; that was verified in source-controller `fetchIndexFiles`.
- **Control-transfer report** (contract `control-transfer-report.schema.yaml`): consumed by plan 50 `verify`, plan 46 (threshold evidence) and humans.
- **Failure codes** (exit reasons in the journal):
  - `SEED_DIRTY_TREE`, `SEED_RENDER_FAILED`, `SEED_SNAPSHOT_CONFLICT`, `SEED_UPLOAD_FAILED`;
  - `SEED_ARTIFACT_MISMATCH`, `SEED_LAYER_TIMEOUT:<layer>`, `SEED_HEALTH_FAILED:<check>`;
  - `TRANSFER_PRECONDITION:<check>`, `TRANSFER_FORGE_SHA_MISMATCH`, `TRANSFER_NOT_READY:<object>`;
  - `TRANSFER_UID_CHANGED`, `TRANSFER_HELM_REVISION_CHANGED`, `TRANSFER_BUCKET_REFERENCED`, `TRANSFER_ROLLED_BACK`, `TRANSFER_PRE_SNAPSHOT_MISSING`.
  The `:<detail>` suffix form needs plan 50's journal `error.code` pattern to allow `^[A-Z][A-Z0-9_]+(:[a-z0-9-]+)?$` (SEED-19).

## 8. Internal interfaces

### 8.1 Seed ordering

The layers and `dependsOn` edges are authoritative in plan 18 §8.3. Flux enforces the order through `dependsOn`. `sc-bootstrap` only observes it and reports progress.

| Step | Layer(s) | Gate (all must hold) | Timeout |
|---|---|---|---|
| 1 | `namespaces` | Namespaces exist with PSA labels; PriorityClasses exist | 2m |
| 1b | `cluster-config` | every cluster-dir manifest applied (plan 18 §8.1, SEED-1): `kubectl get -k`-equivalent check that each object listed in `cluster-config/kustomization.yaml` exists | 3m |
| 1a | `coredns-config` | ConfigMap `kube-system/coredns-custom` applied; CoreDNS resolves `git.<BASE_DOMAIN>` in-cluster to the Traefik ClusterIP once Traefik exists (plan 09) | 3m |
| 2 | `crds` | `prometheus-operator-crds` HR Ready; CRD `servicemonitors.monitoring.coreos.com` Established | 5m |
| 3 | `policies` | VAPs + bindings exist; default-deny NetworkPolicies present in every seed namespace | 3m |
| 4 | `cert-manager` → `cert-manager-config` | HR Ready; ClusterIssuers `Ready=True` | 10m + 5m |
| 5 | `cnpg` | operator and barman-cloud plugin HRs Ready; CNPG webhook answers (layer `wait`) | 10m |
| 6 | `longhorn-upgrade-gate` → `longhorn` → `longhorn-config` | HR Ready; `nodes.longhorn.io` all `Ready`; StorageClass `longhorn` present | 15m + 5m |
| 7 | `traefik` | HR Ready; Service `ingress/traefik` has endpoints | 10m |
| 8 | `cloudflared` (tunnel mode) | Deployment Available; tunnel registered (plan 09 readiness probe on `/ready`) | 5m |
| 9 | `forgejo-db` → `forgejo-upgrade-gate` | CNPG `Cluster forgejo/forgejo-pg` Ready; gate Job complete (first install: records the version, takes no backup; plan 48) | 15m + 30m |
| 10 | `forgejo` | HR Ready; Forgejo `GET /api/healthz` → 200 and `"status":"pass"` | 20m |

Steps 5–8 run in parallel as soon as their dependencies allow. The overall seed timeout `seed.timeout` defaults to **45m**, measured from Bucket Ready.

**Seed complete** is defined as all of the following, evaluated in order and recorded in the journal as `seed.complete`:
1. `Bucket/flux-system` is `Ready=True`. Its `status.artifact.revision` is recorded as `R`, and the artifact integrity check (§8.3) passed for `S`.
2. Every seed layer is `Ready=True` with `status.lastAppliedRevision == R`.
3. Every HelmRelease in seed layers is `Ready=True` with `status.history[0].status == "deployed"`.
4. Forgejo answers `GET http://forgejo-http.forgejo.svc:3000/api/healthz` (via `kubectl port-forward`) with 200.
5. Tunnel mode: `curl -fsS https://git.<BASE_DOMAIN>/api/healthz` from the toolbox returns 200. Direct mode: the same URL resolves to the node and returns 200 with a valid certificate.
6. No Pod in a seed namespace has `restartCount > 3` or a status of `CrashLoopBackOff`/`ImagePullBackOff`.
7. **Ownership (SEED-12).** Seed namespaces are exactly the namespaces of the seed-set layers' objects (`cert-manager`, `cnpg-system`, `longhorn-system`, `ingress`, `forgejo`, `kube-system` for `coredns-config`, `flux-system`). For every object of the kinds Pod, Deployment, StatefulSet, DaemonSet, Job, CronJob, Service, ConfigMap, Secret, PVC and ServiceAccount in those namespaces, `sc-bootstrap` follows `ownerReferences` to the top-level owner. The check passes when the top-level owner carries a `kustomize.toolkit.fluxcd.io/name` or `helm.toolkit.fluxcd.io/name` label, **or** matches an entry of the committed allowlist `gitops/policy/ownership-allowlist.yaml` (`{namespace, kind, nameRegex, manager|ownerKind, ownerPlan}` entries). Component plans own their entries (13: Longhorn manager-created objects and CSI workloads; 10: cert-manager certificate Secrets; 48: `upgrade-gate-*` ConfigMaps and Jobs; plus Kubernetes-created `kube-root-ca.crt`, `default` ServiceAccounts and Leases, owned by this plan). Anything else fails `SEED_HEALTH_FAILED:ownership`.

`sc-bootstrap` prints `SEED COMPLETE <S>`.

### 8.2 Pre-seed key material (generated by plan 50, required by this plan)

All of the following are generated locally in the toolbox before commit `S`. They are written as SOPS-encrypted (two recipients) manifests in the cluster dir, so no forge-created secret is needed before transfer:

| Object | File | Class | Why pre-generated |
|---|---|---|---|
| `flux-system/flux-system` (`identity`, `identity.pub`, `known_hosts`) | `flux-system/flux-source-auth.sops.yaml` | `SC-FLUX-DEPLOY-KEY` | The GitRepository must authenticate the moment Forgejo serves the repo, without an imperative secret |
| `forgejo/forgejo-ssh-host-keys` (`ssh_host_ed25519_key`, `.pub`) | `forgejo/forgejo-ssh-host-key.sops.yaml` (plan 35 register path; delivered by layer `cluster-config`) | `SC-FORGEJO-SSH-HOST-KEY` | `known_hosts` must be known before Forgejo starts. It also stays stable across black start, because restoring from Git yields the same host key. |
| Forgejo admin bootstrap credential | per plan 16 | `SC-FORGEJO-ADMIN` | Forgejo first start |
| S3 credentials for CNPG/Longhorn backups | per plans 12/13/14 | `SC-OBJECT-STORAGE` | Seed layers reference them |
| Tunnel credential | per plan 09 | `SC-DNS-TUNNEL` | `cloudflared` |

`known_hosts` line format: `forgejo-ssh.forgejo.svc.cluster.local ssh-ed25519 <base64>`.

> **Deviation note.** ADR-0016 says the known_hosts entry is "pinned from the Forgejo SSH host key that forge-init generates and records". This plan moves host-key *generation* to pre-seed generation so that the seed commit is self-sufficient and black start keeps a stable host key. Forge-init still records the key (it verifies the served key equals the committed one). This is listed in Open questions Q1 for confirmation.

### 8.3 Snapshot upload and artifact integrity

**Upload (`sc-bootstrap seed`, steps U1–U6):**
- **U1.** `git status --porcelain` must be empty, otherwise `SEED_DIRTY_TREE`. Set `S = git rev-parse HEAD` and `T = git rev-parse HEAD:gitops`.
- **U2.** `sc-gitops validate --cluster <cluster> --mode seed,steady`. Any failure is `SEED_RENDER_FAILED`.
- **U3.** Build the manifest. For each blob in `git ls-tree -r S gitops/`, record path, size, sha256 and git blob id. F11 forbids executable files and symlinks under `gitops/` (check `gitops-file-modes`, plan 18), so every entry has mode `100644`.
- **U4.** If `<cluster>/<S>/.sc-seed/manifest.json` exists, download and compare it with the computed manifest (all fields except `created_at`/`created_by`). Equal means skip to U6 (resume). Different means `SEED_SNAPSHOT_CONFLICT`: stop, because someone wrote a different tree under the same SHA.
- **U5.** Upload every file with an `x-amz-checksum-sha256` header (fallback `Content-MD5` if the provider lacks SHA-256 checksums; plan 14 decides). Use up to 8 concurrent PUTs. Upload `manifest.json` last.
- **U6.** List the prefix. The object count must be `file_count + 1`, and every key must be in the manifest. Otherwise `SEED_UPLOAD_FAILED`.

**Artifact integrity (after `Bucket` Ready, before releasing the seed root to apply anything):**

The seed root is created with `spec.suspend: true` and resumed only after this check passes.

1. Read `Bucket.status.artifact.{revision,digest,url}`.
2. Port-forward `svc/source-controller` (port 80) and download the artifact tarball. Its sha256 must equal `status.artifact.digest`.
3. Extract it. Recompute the git tree hash of `<cluster>/<S>/gitops/` (git tree object format, all modes `100644`). It must equal `manifest.gitops_tree_sha` and `T`. The extracted `<cluster>/<S>/.sc-seed/manifest.json` must equal the local manifest.
4. Pass: resume the root (`kubectl -n flux-system patch kustomization flux-system --type merge -p '{"spec":{"suspend":false}}'`). Fail: `SEED_ARTIFACT_MISMATCH`, and the root stays suspended. Nothing is applied.

### 8.4 Out-of-band secrets (complete list)

```sh
./bootstrap exec -- kubectl -n flux-system create secret generic sops-age \
  --from-file=age.agekey=/run/sc/secrets/age-cluster.agekey --dry-run=client -o yaml \
  | ./bootstrap exec -- kubectl apply --server-side --field-manager=sc-bootstrap -f -
./bootstrap exec -- kubectl -n flux-system create secret generic seed-bucket-credentials \
  --from-literal=accesskey=<seed-read access key> --from-literal=secretkey=<seed-read secret> \
  --dry-run=client -o yaml | ./bootstrap exec -- kubectl apply --server-side --field-manager=sc-bootstrap -f -
```

The orchestrator performs these through client-go. The commands above are the documented manual equivalent. Both Secrets (and `seed-bucket-ca` when `S3_CA_BUNDLE` is set) are labeled `strange.company/out-of-band: "true"`. P04 check: `kubectl get secrets -A -l strange.company/out-of-band=true` lists exactly these. After transfer it lists only `sops-age`.

**Field managers (SEED-17).** The out-of-band Secrets use field manager `sc-bootstrap` (Flux never manages them). Every imperative apply of a **Flux object** (the four controllers, the PriorityClass pre-apply, `Bucket/flux-system`, the seed root, the T2 switch, rollback, suspend and resume) uses field manager `kubectl`, which kustomize-controller removes when it takes the object over through SSA, so no imperatively-set field survives self-management.

### 8.5 Control transfer (`flux-bootstrap` phase, steps T0–T8)

- **T0 Preconditions.** All must hold, otherwise `TRANSFER_PRECONDITION:<check>` with no change made:
  - (a) the journal has `seed.complete` for `S`;
  - (b) the journal has `forge_init.pushed_sha == F`, `git merge-base --is-ancestor S F` holds, every commit in `S..F` has subject `bootstrap(<cluster>): forge-init outputs` (any number of them, SEED-9), and every path in `git diff --name-only S F` is a forge-init output path (§5: plan 16 §4.4, `config/github-mirror.yaml`, `cluster-config/kustomization.yaml`). Otherwise `TRANSFER_PRECONDITION:forge-outputs-scope`;
  - (c) `git ls-remote <forgejo>/strange-company/strange-company.git refs/heads/main` (through port-forward to `forgejo-http`) returns `F` = the journal `forge_init.pushed_sha` (the final SHA forge-init pushed), otherwise `TRANSFER_FORGE_SHA_MISMATCH`;
  - (d) `GitRepository/flux-system` is `Ready=True` with `status.artifact.revision == main@sha1:<F>`. Wait up to 5m and force with `flux reconcile source git flux-system`.
  - (e) the Forgejo deploy key titled `flux-readonly` has `read_only: true`;
  - (f) no Kustomization or HelmRelease in the cluster has `spec.suspend: true`. Exception: Kustomizations labeled `strange.company/suspended-by: handoff-rollback` (left by a previous rollback, §14.3) are resumed by T0 itself first (`kubectl patch … suspend:false` with field manager `kubectl` and the label removed), because a `suspend` set by another field manager is not removed by Flux SSA (SEED-14).
- **T1 Pre-transfer snapshot (SEED-6, SEED-7).** Record `{id, uid, resourceVersion}` for:
  - every entry of every seed layer's `status.inventory.entries[]`;
  - every entry of the **root** Kustomization's inventory (Flux CRDs and controllers, `GitRepository`, `Secret flux-system`, `cluster-vars`, every layer Kustomization object);
  - **every** CustomResourceDefinition in the cluster;
  - every PVC and PV in seed namespaces;
  - every Pod in seed namespaces, with `restartCount` per container (reported, and asserted by the e2e A9 only).

  For each HelmRelease, record `status.history[0].version` and the count of `sh.helm.release.v1.<release>.*` Secrets. Write `transfer-pre-<S>.json`, upload it to `s3://<prefix>-bootstrap-seed/<cluster>/reports/transfer-pre-<S>.json` **before T2**, and record it in the journal as an `outputRef` with its sha256. On resume after `flux-bootstrap.switched`, T5 uses only that object (verified by sha256); if it is missing or does not match, stop with `TRANSFER_PRE_SNAPSHOT_MISSING` and never re-snapshot post-switch state.
- **T2 Switch.** Apply the committed root from `F:gitops/clusters/<cluster>/flux-system/gotk-sync.yaml` (Kustomization only) with `kubectl apply --server-side --field-manager=kubectl --force-conflicts`. The root now has `sourceRef GitRepository`, `path ./gitops/clusters/<cluster>`, and `SC_SOURCE_KIND=GitRepository`, `SC_SOURCE_ROOT=.`. Journal step: `flux-bootstrap.switched`.
- **T3 Root reconcile.** `flux reconcile kustomization flux-system`. Wait for root `Ready=True` with `lastAppliedRevision == main@sha1:<F>`. Timeout 10m.
- **T4 Layer convergence.** Wait for every seed layer `Ready=True` with `lastAppliedRevision == main@sha1:<F>` and `spec.sourceRef.kind == GitRepository`, observed on **3 consecutive polls** (5 s apart) within the 20m timeout; a transient not-ready (for example a Forgejo pod rescheduled under memory pressure while post-transfer layers start) does not fail T4 (SEED-14). Post-transfer layers (`authentik*`, `woodpecker*`, `monitoring*`, …) start reconciling now; T4 ignores them. They are gated at P07–P10 and finally at P12.
- **T5 Compare.** For every T1 entry except Pods (seed-layer and root inventories, CRDs, PVCs, PVs), the current UID must equal the recorded UID, otherwise `TRANSFER_UID_CHANGED`. Pod UID and `restartCount` changes are listed in the report. Every HelmRelease's `history[0].version` must be unchanged and its release-Secret count unchanged, otherwise `TRANSFER_HELM_REVISION_CHANGED`. ResourceVersion changes are reported but not failures, because status updates change them. Objects that `S..F` adds (forge-init output Secrets and ConfigMaps) are new and are not compared. The scope rule of T0(b) guarantees that `S..F` changes no seeded HelmRelease's values or chart, so T5 holds across `S → F`.
- **T6 Retire Bucket.** Check that no Kustomization references a Bucket (`kubectl get ks -A -o json | jq '[.items[]|select(.spec.sourceRef.kind=="Bucket")]|length' == 0`), otherwise `TRANSFER_BUCKET_REFERENCED`. Then delete `Bucket/flux-system` and `Secret/seed-bucket-credentials`.
- **T7 Report.** Write a `control-transfer-report` with phase `source-switch` to the journal and S3. Print `SOURCE SWITCH COMPLETE <F>`.
- **T8 Retention.** The snapshot stays in place (plan 43 lifecycle).

If anything fails between T2 and T5, `sc-bootstrap` runs automatic rollback (§14.3) once, records `TRANSFER_ROLLED_BACK`, and stops for the operator.

### 8.6 "Seeded releases are represented in normal GitOps state" (definition, SPEC §35)

The condition holds when every check below passes. `sc-bootstrap verify control-transfer` implements them, and the same checks appear in the report as `G1`–`G7`:

| ID | Check | Command (manual equivalent) |
|---|---|---|
| G1 | Every seeded object is in the inventory of a Kustomization whose `sourceRef` is `GitRepository/flux-system` | For each T1 id, find its owning layer; `kubectl get ks <layer> -n flux-system -o jsonpath='{.spec.sourceRef.kind}'` = `GitRepository` |
| G2 | Each seed layer's `lastAppliedRevision` equals the GitRepository artifact revision | `flux get kustomizations` revision column equals `main@sha1:<Forgejo main>` |
| G3 | Applying Git again would change nothing | `flux diff kustomization <layer> --path <checkout>/gitops/platform/<c>/overlays/<profile>` exits 0 for every seed layer (run from a checkout of Forgejo `main`). Version-sensitive: SOPS Secrets are skipped, exit code 1 means drift, cluster-side `substituteFrom` is applied; re-verified at each Flux minor by 19-AC-18 |
| G4 | Every Helm release in seed namespaces is owned by a HelmRelease | Every `sh.helm.release.v1.*` Secret in seed namespaces maps to a `HelmRelease` of the same `releaseName` (`helm list -A` vs `kubectl get hr -A`) |
| G5 | No source other than Git and upstream chart sources exists | `kubectl get buckets.source.toolkit.fluxcd.io -A` returns none |
| G6 | No object in seed namespaces is managed only by an imperative field manager | Same kinds, namespaces and ownership walk as seed-complete #7: every top-level object's `metadata.managedFields[].manager` contains `kustomize-controller` or `helm-controller`, or it matches `gitops/policy/ownership-allowlist.yaml`; the only imperative exceptions are the out-of-band Secrets |
| G7 | Out-of-band Secrets are exactly `{flux-system/sops-age}` | `kubectl get secrets -A -l strange.company/out-of-band=true` |

### 8.7 SPEC §35 checklist (implemented item by item)

C1–C8 are the **only** implementation of SPEC §35 (SEED-10). Plan 50's V-01…V-08 call this code, and its `report.json` embeds the `final` report; there is no second report. `CONTROL TRANSFER COMPLETE` is printed by plan 50 at the end of `./bootstrap` (phase `control-transfer`, after `verify`) only when C1–C8 all pass **within one `control-transfer` evaluation** (no result older than that run is reused) and the `final` report validates. Per CD-19 this happens unattended in one `./bootstrap` run; C7 covers P04–P10 (P11 runs after transfer). Plan 19 provides C1–C6 and C8's hook. Plan 50 provides C7. Plan 17 provides C8's mirror check.

| ID | SPEC §35 item | Check | Command (manual equivalent) |
|---|---|---|---|
| C1 | Forgejo is healthy | HR `forgejo` Ready; healthz 200 in-cluster and on the public hostname | `./bootstrap exec -- curl -fsS https://git.<BASE_DOMAIN>/api/healthz` |
| C2 | `strange-company` exists there | API returns the repo, its default branch is `main`, and Forgejo `main` **descends from** `F` (it moves after transfer with the `sso-init`/`ci-init` commits) | `./bootstrap exec -- curl -fsS -H "Authorization: token <admin>" https://git.<BASE_DOMAIN>/api/v1/repos/strange-company/strange-company` |
| C3 | repository push succeeds | (a) `forge_init.pushed_sha == F`; (b) at `verify` time, `sc-bootstrap` pushes probe branch `sc-verify/push-probe-<run-id>` (pointing at Forgejo `main`) as the bootstrap admin, sees it in `git ls-remote`, then deletes it. `sc-ci-config` serves no runnable workflow for an unprotected-branch push (plan 20 §8.2), so the probe starts no pipeline inside the V-30 memory window (SEED-23) | `git push <forgejo> <main sha>:refs/heads/sc-verify/push-probe-<run-id>` then `git push <forgejo> :refs/heads/sc-verify/push-probe-<run-id>` |
| C4 | Flux sources Forgejo | root `sourceRef == GitRepository/flux-system`; GitRepository URL host `forgejo-ssh.forgejo.svc.cluster.local`; `Ready=True` | `./bootstrap exec -- flux get sources git -n flux-system` |
| C5 | Flux reconciles successfully | Every Kustomization and HelmRelease cluster-wide is Ready at the current `main@sha1` (at P06 the seed layers; at P12 all) | `./bootstrap exec -- flux get all -A --status-selector ready=false` prints nothing |
| C6 | seeded releases represented in normal GitOps state | G1–G7 (§8.6) | `./bootstrap exec -- sc-bootstrap verify control-transfer` |
| C7 | platform verification passes | plan 50 `./bootstrap verify` aggregate (all P07–P11 gate checks) | `./bootstrap verify` |
| C8 | GitHub mirror works | plan 17 §14.3 `mirror-verify`: GitHub `main` equals Forgejo `main`, and a probe **tag** `sc-mirror-probe/<run-id>` created on Forgejo appears on GitHub within 300 s and is then deleted (no commit lands on `main`; branches outside the mirror filter are never mirrored, 17-R-1) | `./bootstrap exec -- sc-bootstrap mirror verify --run-id <id>` |

## 9. Configuration

| Knob | Source | Default | Who may change |
|---|---|---|---|
| `seed.timeout` | `bootstrap.env` `SC_SEED_TIMEOUT` (plan 50 schema) | `45m` | operator |
| Per-layer timeouts | plan 18 layer files | plan 18 §8.3 | PR |
| `transfer.layerTimeout` | `SC_TRANSFER_TIMEOUT` | `20m` | operator |
| Upload concurrency | `SC_SEED_UPLOAD_CONCURRENCY` | `8` | operator |
| Bucket `insecure` | derived: `true` only if the S3 endpoint scheme is `http` **and** `SC_ALLOW_INSECURE_S3=true` | `false` | operator (CI fixture only) |
| Snapshot retention | plan 43 lifecycle on `<prefix>-bootstrap-seed` | ≥90 days, ≥ last 3 per cluster | PR |

## 10. Secrets

| Class | Use here | Lifetime |
|---|---|---|
| `SC-AGE-CLUSTER` | becomes `flux-system/sops-age` | permanent (rotation per plan 35) |
| `SC-OBJECT-STORAGE` subclass `seed-read` | `flux-system/seed-bucket-credentials` | the in-cluster copy is deleted at T6. Plan 35 rotates the underlying key after transfer; the **new** key is kept only in the recovery bundle, ready for a black-start seed (SEED-23). |
| `SC-OBJECT-STORAGE` subclass `seed-write` (operator-side) | upload from the toolbox | never enters the cluster |
| `SC-FLUX-DEPLOY-KEY` | pre-generated, SOPS in Git (§8.2) | permanent |
| `SC-FORGEJO-ADMIN` | forge-init push, C2/C3 probes | per plan 16 |

**New secret material** for plan 35 to register: the **Forgejo SSH host key** (`forgejo/forgejo-ssh-host-keys`). Proposed as its own class `SC-FORGEJO-SSH-HOST-KEY`:
- creator: `sc-bootstrap` pre-seed;
- consumer: Forgejo;
- storage: SOPS in Git plus the recovery bundle;
- rotation: a regenerate-and-update-known_hosts PR;
- recovery: from Git.

## 11. Storage

- S3 seed prefix: about 1–5 MiB per snapshot (the `gitops/` subtree only).
- No cluster storage is owned. The seed set's PVCs belong to their plans (13, 16).

## 12. Resource requirements

The seed and transfer logic runs in the toolbox on the operator machine, and needs about 200 MiB RAM. In-cluster consumption is the Flux controllers (plan 18 §12) plus the seed-set components (their own plans). The seed peak on a 12 GiB node is about 4 GiB working set, which plan 51 measures. Upload is limited to 8 concurrent PUTs.

## 13. Security boundary

- The **seed write credential** never leaves the operator machine. The cluster gets a **read-only** credential scoped to the seed bucket, and deletes it at transfer.
- **Integrity:** the artifact is proven equal to the git tree of `S` before anything applies (§8.3). A tampered object in the bucket halts the seed (`SEED_ARTIFACT_MISMATCH`).
- **Authority:** `sc-bootstrap` uses the cluster-admin kubeconfig (plan 07) only for:
  - applying Flux controllers;
  - the two out-of-band Secrets;
  - Bucket and root objects;
  - suspend/resume;
  - reads.

  Everything else is applied by Flux from Git.
- **Forgejo trust at transfer:** Flux trusts the Forgejo host key pinned in Git (§8.2), and the deploy key is read-only (ADR-0016).
- Admission policies are active from step 3 of the seed, so every later seed workload is admitted under them (plan 37). The layers before them (namespaces, crds) create no Pods.

## 14. Happy path

### 14.1 Seed (phase `seed`)

1. U1–U6 (upload).
2. Pre-apply the PriorityClasses rendered from the `namespaces` layer (`sc-gitops render --cluster <c> --layer namespaces --kind PriorityClass`), then apply `sc-gitops render --cluster <c> --flux-system --exclude-sops` at commit `S` (the `flux-system/` kustomization with its patches, digest-pinned images and substitute-disabled annotations, **without** the SOPS-encrypted `flux-source-auth.sops.yaml` and without the root Kustomization), both with `kubectl apply --server-side --field-manager=kubectl`. Using the patched build avoids a controller roll at the first root reconcile (SEED-16). Wait for the four controller Deployments to be Available (5m).
3. Create the out-of-band Secrets (§8.4).
4. Create `Bucket/flux-system` and the seed root with `suspend: true`. Wait for the Bucket to be Ready (5m).
5. Artifact integrity check. On pass, resume the root.
6. Wait for the §8.1 gates, printing per-layer progress every 30s (`layer`, `ready`, `reason`, `elapsed`).
7. Evaluate seed complete (§8.1). Print `SEED COMPLETE <S>`.

### 14.2 Forge-init (plan 16, P05)

Forge-init pushes `S`, registers the deploy key and pushes its outputs commit, so Forgejo `main` is `F`. The GitRepository (already applied in the seed, not Ready until now) becomes Ready at `main@sha1:<F>`.

### 14.3 Control transfer (phase `flux-bootstrap`)

T0–T8 (§8.5). Rollback, which runs automatically once on T2–T5 failure, or manually with `./bootstrap handoff-rollback`:

1. **Suspend every non-seed Kustomization** that exists (post-transfer layers created during the attempt): `spec.suspend: true` with field manager `kubectl`, label `strange.company/suspended-by: handoff-rollback`. This stops the mixed-authority state in which they keep reconciling from the `GitRepository` (SEED-14). Their names go into the report (`suspended_kustomizations`).
2. Re-apply the seed root (Bucket source, seed path, Bucket substitutions) with **`prune: false`** explicitly in the applied object and `kubectl apply --server-side --field-manager=kubectl --force-conflicts`. `prune: false` is mandatory: the post-transfer root inventory contains every post-transfer layer, and pruning them would cascade (`MirrorPrune`) into Helm uninstalls.
3. `flux reconcile kustomization flux-system`. Wait for all seed layers Ready at Bucket revision `R`. The layers' `sourceRef` returns to `Bucket` through substitution.
4. Verify UIDs against `transfer-pre-<S>.json`.

Post-transfer layer objects created during the failed attempt remain, suspended. A later transfer resumes them in T0(f). Rollback is impossible after T6. To re-create the Bucket in that case, see `failed-flux-handoff.md` §D/§F.

### 14.4 Verify and control-transfer (phases `verify`, `control-transfer`; P12)

Plan 50 runs `./bootstrap verify`, then evaluates C1–C8. It writes the `final` report and prints `CONTROL TRANSFER COMPLETE`.

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Dirty working tree / render fails | U1/U2 | Seed not started | none (exit) | fix, commit, `./bootstrap` |
| Snapshot conflict (different content under same SHA) | U4 | Seed halted | none | `failed-flux-handoff.md` §A (investigate bucket tampering) |
| S3 unreachable / credential wrong | U5/U6 errors, or Bucket `Ready=False` | Seed halted | U5 retries 5× with exponential backoff (1s → 16s) per object | `object-storage-outage.md` (plan 14) |
| Artifact mismatch | §8.3 | Nothing applied (root suspended) | none | `failed-flux-handoff.md` §A |
| Seed layer timeout | `SEED_LAYER_TIMEOUT:<layer>` | Partial seed | Flux keeps retrying in-cluster | `seed-flux-handoff.md` §Diagnose; resume `./bootstrap` |
| Admission policy rejects seed workload | HR/Deployment events `ValidatingAdmissionPolicy ... denied` | Layer blocked | none | fix the policy via PR to the local commit → re-seed with new `S` (§17) |
| Longhorn unhealthy (iscsi missing) | `nodes.longhorn.io` not Ready | `forgejo` blocked | none | plan 13 / plan 08 baseline |
| Forgejo `main` ≠ `S` | T0(c) | Transfer refused, nothing changed | none | `failed-flux-handoff.md` §B (re-seed at new HEAD or reset Forgejo main) |
| GitRepository not Ready (host key, deploy key, DNS) | T0(d) | Transfer refused | none | `failed-flux-handoff.md` §C |
| Root/layers not Ready after switch | T3/T4 timeout | Mixed state | automatic rollback to Bucket | `failed-flux-handoff.md` §C |
| UID changed / Helm revision changed | T5 | Possible recreation happened (data at risk if a PVC changed) | automatic rollback; report lists objects | `failed-flux-handoff.md` §E; **stop for human** |
| Bucket still referenced | T6 | Bucket kept | none | `failed-flux-handoff.md` §D |
| `transfer-pre-<S>.json` missing or altered on resume after the switch | resume reads the outputRef | Transfer cannot be proven | stop with `TRANSFER_PRE_SNAPSHOT_MISSING` (never re-snapshot) | `failed-flux-handoff.md` §E |
| Toolbox/operator machine lost mid-phase | journal shows a phase in progress | Cluster continues reconciling | none | `bootstrap-resume.md` (plan 50); every step is idempotent (§17) |
| Bucket present long after bootstrap | `SCFluxSeedSourcePresent` (plan 18) | Seed source still active | none | `failed-flux-handoff.md` §D |

## 16. Retry behavior

- Upload: per-object retries (5 attempts, exponential backoff 1s → 16s, jitter ±20%).
- Waits poll every 5s up to their timeouts. They do not retry Flux operations themselves, because Flux retries in-cluster on `retryInterval`.
- Transfer: automatic rollback happens **at most once** per `flux-bootstrap` attempt. A second failure leaves the system in rollback state and exits non-zero. The operator decides next steps (procedure). `./bootstrap` resume re-runs T0 from scratch.
- Rollback itself is retried by re-running `./bootstrap handoff-rollback`, which is idempotent.

## 17. Idempotency behavior

| Step | Re-run behavior |
|---|---|
| U1–U6 | A complete identical prefix is detected and the upload skipped. A partial prefix is re-uploaded. A conflicting prefix is a hard stop. |
| Controller apply | SSA, no-op if unchanged |
| Out-of-band Secrets | SSA apply, no-op if unchanged |
| Bucket/root creation | SSA apply (field manager `kubectl`). If the root already uses the GitRepository source, the seed phase never runs (frozen rule below). |
| Integrity check | Pure read |
| T0–T5 | T2 is an SSA of the committed root, a no-op if already applied. T1 is recorded only if `flux-bootstrap.switched` is absent from the journal. Otherwise the uploaded `transfer-pre-<S>.json` is reused (sha256-checked), or the run stops with `TRANSFER_PRE_SNAPSHOT_MISSING`. |
| T6 | Delete-if-exists |
| Rollback | SSA of the seed root, no-op if already in place |

**Resume and re-run rules (BOOT-2, SEED-20).** These are the rules plan 50's engine applies to this plan's phases:
1. **Digests use recorded inputs.** The seed phase's digest is the journal `seed.commit`, not HEAD. forge-init moving HEAD to `F` does not make `seed` pending. The flux-bootstrap digest is the journal `forge_init.pushed_sha`; later `sso-init`/`ci-init` commits do not make it pending.
2. **Check() wins over a digest change** once a later phase has consumed the phase's output.
3. **Frozen after the switch.** Once the journal has `flux-bootstrap.switched`, the phases `seed`, `forge-init` and `flux-bootstrap` are never re-run, by resume or by `--from-phase`. A resume after a failed `verify` reports them `skipped: frozen`, and the root `sourceRef` stays `GitRepository`. Drift in what they produced is reported with the procedure to follow (`failed-flux-handoff.md`), never auto-repaired. The only exception is a resume *inside* `flux-bootstrap` between T2 and T7, which continues from the recorded step.
4. Before the switch, a resume repeats `seed` only if `seed.complete` is absent; between forge-init and flux-bootstrap it continues with flux-bootstrap at the recorded `F`.

**Re-seed with a new commit** (allowed only before `flux-bootstrap.switched`): upload a new snapshot `S'`, patch the Bucket `prefix` to `<cluster>/<S'>/`, and patch the root `path`/`SC_SOURCE_ROOT` accordingly. The objects are unchanged except where `S'` differs, and Flux applies only the differences. The journal records `seed.commit = S'`.

## 18. Bootstrap behavior

### 18.1 Fresh install

As in §14. Phase mapping to SPEC §8:
- `seed` = §14.1;
- `forge-init` = plan 16;
- `flux-bootstrap` = §8.5;
- `verify` = plan 50;
- `control-transfer` = the C1–C8 evaluation.

### 18.2 Why Authentik is not in the seed

Forgejo starts with local accounts plus break-glass (plan 15/16). SSO is added after transfer (P07). This avoids a seed dependency on a second database and identity configuration (SI-05).

### 18.3 Black-start variant (plan 45 owns the overall procedure)

1. The operator clones GitHub `main` (commit `G`). The cluster dir exists in Git from the original install. `sc-bootstrap recover` (plan 45/50) checks that the age identity decrypts every `*.sops.yaml` in `gitops/clusters/<cluster>/`, using the recovery bundle (plan 35).
2. The seed runs exactly as in §14.1 with `S = G`, using the **recovery overlay selection** defined by plan 45. Plan 45 sets the root substitution `SC_PROFILE` to the cluster's profile, and the cluster dir carries the recovery patches (CNPG `bootstrap.recovery`, Longhorn restored PVC binding) committed by `sc-bootstrap recover` as a local commit `G'` before the snapshot. `S = G'`.
3. Forge-init in recovery mode (plan 16/45) restores repositories from Longhorn backup or `repo-backups` bundles. It then makes Forgejo `main == S`:
   - if Forgejo's restored `main` is an ancestor of `S`, push `S`;
   - if it is a descendant (GitHub mirror lag), follow **plan 45 §14 step 7.2**: rebuild the recovery commit on the restored head (`G'' = restored main + recovery commit`: `cnpg-recovery` component, `config/recovery.yaml`, generation bump, `emergencyStop: workers`) and re-seed at `G''`. Re-seeding at the bare restored head would drop the recovery commit (SEED-15);
   - if the two diverged, stop for the operator.
4. Transfer is T0–T8 unchanged. **Requirement on plan 45:** the steady path at `S` must render the recovered CNPG `Cluster` and PVC objects identically to the recovery seed. The recovery patches stay committed in the cluster dir until a later reviewed PR removes them, and plan 45 must show that removing them does not trigger re-bootstrap. The black-start test case (19-AC-14, gated at H04 with plan 45's fixture; DR-17) asserts unchanged UIDs. The identity-after-Flux ordering is the recorded interpretation SI-05.

## 19. Upgrade behavior

- Seed and transfer run once per installation, or per black start. Upgrading the tooling changes future runs only.
- Changing the seed layer set is a PR to plan 18 §8.3. F11 `seed-closure` validates it. The k3d handoff test runs on every change to `gitops/profiles/*/seed/**`, `gitops/layers/**` or `src/internal/bootstrap/{seed,handoff}/**` (plan 52).
- The Flux version used by the seed is the committed one (ADR-0016). A Flux upgrade does not alter the procedure, unless the Bucket or Kustomization API version changes. 19-AC-4 re-verifies the Bucket semantics for each Flux minor in the k3d test.

## 20. Recovery behavior

- **Mid-seed loss of the operator machine:** the cluster keeps reconciling from the Bucket. On a new machine, restore the journal from S3 (plan 50) and re-run `./bootstrap`. Steps are idempotent.
- **Cluster loss during seed:** reprovision (plan 45 or a fresh install). Snapshots in S3 are reused by U4.
- **Post-transfer loss of Forgejo:** Flux keeps the last applied state, and `SCFluxSourceFailing` fires. Restore Forgejo (plan 16 `restore-forgejo.md`). Do not switch back to a Bucket unless the operator chooses the emergency path in `failed-flux-handoff.md` §F: a temporary re-seed from GitHub `main` while Forgejo is restored. That path is break-glass and recorded.

## 21. Observability

- **Console:** per-layer progress lines and the final lines (§4).
- **Journal:** each step's start, end, result and error code (plan 50 contract).
- **Reports:** `control-transfer-report` JSON (S3 + journal).
- **Metrics/alerts:** none from `sc-bootstrap` itself, because it is off-cluster. In-cluster: `SCFluxSeedSourcePresent`, `SCFluxReconciliationFailing`, `SCFluxSourceFailing` (plan 18; catalogue names, CD-01). Monitoring arrives at P09, so P12 verification re-checks that these alerts are inactive.
- **Bootstrap report** (plan 50) includes the seed commit, the Bucket revision, the transfer time and the snapshot location.

## 22. Testing strategy

**Unit** (`go test ./src/internal/bootstrap/seed/... ./handoff/...`, PR):
- manifest building;
- git tree hash recomputation against `git mktree` fixtures;
- U4 conflict logic;
- the T5 comparison (UID map, Helm history);
- rollback object generation;
- failure-code mapping.

These use a fake Kubernetes client (`client-go` fake) and a fake S3 client.

**Contract:** `seed-manifest.schema.yaml` and `control-transfer-report.schema.yaml` examples validate. Generated manifests and reports from unit tests are validated against them.

**Integration — seed→Forgejo→Flux handoff test** (`make e2e-handoff`; SPEC §34). It runs on k3d in the off-cluster e2e lane (plan 52 §8.1/§8.4, ADR-0303): in Phase 1 on PRs touching the paths in §19 and on every `main` push; after P06 daily when `main` changed, on release tags (release-gating), and on dispatch.

Environment:
- k3d cluster `sc-handoff`, 1 server; k3s image digest = the production k3s version (`versions/k3s.lock.yaml`, plan 01);
- `--k3s-arg=--disable=traefik@server:0 --k3s-arg=--disable=servicelb@server:0`; network policy enabled (k3s default); kustomize-controller with `StrictPostBuildSubstitutions` on (as in production);
- the SeaweedFS `s3-emulator` container (ADR-0205) on the k3d network (`http://s3-emulator:9000`, bucket `ci-bootstrap-seed`);
- fixture cluster `gitops/clusters/ci-handoff` (committed, hand-maintained, encrypted to the committed test key `test/fixtures/age/ci-test.agekey`, plan 01 §6.5): the **seed layers plus one cheap post-transfer canary layer** `ci-canary` (a ConfigMap and a pinned `traefik/whoami` Deployment in `apps`, dependsOn `policies`). No other post-transfer layer is rendered, so A4 is satisfiable.

**Fixture divergences from production (SEED-13)** — each one says what this test therefore does *not* prove and where that is proven instead:

| Divergence | Not proven here | Proven by |
|---|---|---|
| `longhorn`/`longhorn-config` replaced by StorageClass `longhorn` on `rancher.io/local-path` (no iSCSI in k3d); `longhorn-upgrade-gate` runs in its first-install branch | Longhorn install, volumes, replica sync | lab-VM e2e (gating for P04, 19-AC-19) and live P04 |
| ACME ClusterIssuers replaced by a CA ClusterIssuer (`sc-internal-ca` fixture CA); seed-complete #5 checks `https://git.<fixture domain>` through the Traefik ClusterIP with that CA | public certificates, ACME, the edge | live P04 (19-AC-6), plan 10 gates |
| direct ingress mode; no cloudflared | tunnel mode | lab-VM e2e runs tunnel-less too; live P04 in tunnel mode |
| `s3-egress` rendered for `s3-emulator:9000` (plan 36 component, SEED-3) | public S3 egress | live P04 |
| halved resource requests | memory budget | P12 V-30 (plan 50) |

Test flow (A-run):
1. Create a temporary clone at the commit under test; the fixture cluster dir is already committed. Commit nothing else (`S = HEAD`).
2. Run `sc-bootstrap seed` (production code path) against k3d.
3. Run `sc-bootstrap forge-init` (plan 16 code path) against the in-cluster Forgejo through port-forward.
4. Run `sc-bootstrap flux-bootstrap`.

Assertions:
- **A1** `SEED COMPLETE` with all seed layers Ready at the Bucket revision; the Bucket stays `Ready=True` for 3 consecutive intervals after the `policies` layer is Ready (SEED-3).
- **A2** After transfer, every T1 object UID is unchanged: seed-layer inventories, the root inventory, all CRDs, PVCs and PVs (SEED-6).
- **A3** No HelmRelease gained a revision, and no `install` action exists after T2: `helm history` per release is unchanged, and the release-Secret count is unchanged.
- **A4** Every seed layer and `ci-canary` is Ready at `main@sha1:<F>`; `flux get all -A --status-selector ready=false` prints nothing.
- **A5** The Bucket and `seed-bucket-credentials` are absent.
- **A6** G1–G7 pass.
- **A7** The report validates against `control-transfer-report.schema.yaml` with `result: pass`.
- **A8** Pushing a no-op commit to Forgejo `main` is applied within 3m, with no HelmRelease revision change.
- **A9** Pod UIDs and `restartCount` in seed namespaces are unchanged from T1 to T5, and the k3s audit log (fixture audit policy at `Metadata` level for writes) shows **zero** `create` or `delete` requests by `system:serviceaccount:flux-system:kustomize-controller` or `…:helm-controller` on objects in seed namespaces between T2 and T5.
- **A10** `forgejo-upgrade-gate` and `longhorn-upgrade-gate` Jobs complete with the `policies` layer applied (SEED-2).
- **A11** With strict substitution on, the root reconciles at both sources, and every rendered layer object keeps the escaped layer variables (`$${VAR}` → `${VAR}`) intact (SEED-4).

Negative cases:
- **N1** An extra commit on Forgejo `main` before transfer: `TRANSFER_FORGE_SHA_MISMATCH`, and the root is unchanged.
- **N2** Overwrite one object in the bucket after U6: `SEED_ARTIFACT_MISMATCH`, the root stays suspended, and no seed-namespace Pods exist.
- **N3** Corrupt `known_hosts` in the committed source-auth Secret: `TRANSFER_PRECONDITION:gitrepository-ready`, and the root is unchanged.
- **N4** Fault hook `SC_TEST_FAULT=after:T3` (compiled only with build tag `e2efault`): automatic rollback. All seed layers are Ready at Bucket revision `R`, UIDs are unchanged, `ci-canary` is suspended with label `strange.company/suspended-by: handoff-rollback`, and the journal has `TRANSFER_ROLLED_BACK`. A second `flux-bootstrap` then resumes `ci-canary` in T0(f) and passes.
- **N5** **Positive control (mutation test):** fixture `ci-handoff-mutant`, whose steady path differs from the seed path in one seeded HelmRelease value and renames one generated ConfigMap. The transfer must end in `TRANSFER_HELM_REVISION_CHANGED` or `TRANSFER_UID_CHANGED` followed by rollback. This proves the comparator is not vacuous.
- **N6** **Crash after T2:** fault hook `after:T2` kills `sc-bootstrap`; a resume from a **fresh workspace** (same S3, journal restored) completes T3–T7 using the uploaded `transfer-pre-<S>.json` (SEED-7). Deleting that object before the resume yields `TRANSFER_PRE_SNAPSHOT_MISSING`.
- **N7** **Emergency source (failed-flux-handoff §F):** after a successful transfer, `seed --emergency-source` at the same commit suspends all non-seed Kustomizations, sets `prune: false`, and `ci-canary`'s objects survive (UIDs unchanged); `--resume-after-emergency` restores the GitRepository source and resumes `ci-canary` (SEED-14).
- **N8** **Frozen phases (BOOT-2):** after a successful transfer and a forced `verify` failure, rerunning `./bootstrap` reports `seed`, `forge-init` and `flux-bootstrap` as `skipped: frozen`, and the root `sourceRef.kind` stays `GitRepository`.

Budget: A-run plus N1–N8 as parallel jobs (one k3d cluster each), ≤ 60 min wall clock per job on a 4 vCPU / 16 GiB runner. No paid calls; network only to the pinned registries and the in-lane S3 emulator.

**Lab-VM e2e** (plan 52, `e2e-lab` with `LAB_SUBSTRATE=lab-seed`, plan 05): the full seed including real Longhorn and the first-install `longhorn-upgrade-gate`, direct mode, on a disposable lab VM (F10 environment), never the production node. Assertions A1–A7, A9, A10. It is **gating for P04** (19-AC-19).

**Lab-VM fault cases on real storage (CD-31, external review #5)** — `make e2e-lab LAB_SUBSTRATE=lab-seed LAB_SCENARIO=handoff-faults`. Each case runs on a fresh disposable `lab-seed` VM with real Longhorn, CNPG and Forgejo. Before T1 the harness writes two markers: repository `sc-e2e/marker` with a known commit `M` on `main` (in `forgejo-data`), and an issue titled `sc-marker-<run-id>` (a row in `forgejo-pg`). It records the UIDs of PVC `forgejo/forgejo-data`, its PV, Longhorn `Volume` `forgejo-data`, CNPG `Cluster forgejo-pg` and its instance PVC, plus `helm history` of every seeded release. Expected observable results:
- **N4-lab** (fault after T3, automatic rollback): `TRANSFER_ROLLED_BACK`; seed layers Ready at Bucket revision `R`; every recorded UID unchanged; `GET /api/v1/repos/sc-e2e/marker/branches/main` returns `M`; the marker issue exists; every seeded release's `helm history` length is unchanged; then a second `flux-bootstrap` passes with the same results.
- **N5-lab** (mutant steady path: one changed value of the `traefik` release, a `controller`-class release, plus one renamed generated ConfigMap in `forgejo`): `TRANSFER_HELM_REVISION_CHANGED` or `TRANSFER_UID_CHANGED`, followed by rollback. The `forgejo`, `longhorn` and `cnpg` releases' histories are unchanged; `traefik` gains at most two revisions (mutant and restore). The storage UIDs and both markers are unchanged.
- **N6-lab** (crash after T2, resume from a fresh workspace using the uploaded `transfer-pre-<S>.json`): `SOURCE SWITCH COMPLETE <F>`; storage UIDs, both markers and every release history unchanged.

**Live gates:** P04/P06 run the same checks on the real node.

## 23. Acceptance criteria

### GATE-P04

- [ ] **19-AC-1** `SC_TEST_STOP_AFTER=seed ./bootstrap --from-phase seed` on the real node (stops after the seed phase; SEED-20) prints `SEED COMPLETE <S>`, where `<S>` equals `git rev-parse HEAD`, and the journal entry `seed.complete` validates against `bootstrap-journal.schema.yaml`.
- [ ] **19-AC-2** `./bootstrap exec -- aws s3api head-object --bucket <prefix>-bootstrap-seed --key <cluster>/<S>/.sc-seed/manifest.json` succeeds (or the equivalent plan 14 tool). The downloaded manifest validates against `seed-manifest.schema.yaml`, and `gitops_tree_sha == git rev-parse <S>:gitops`.
- [ ] **19-AC-3** Every seed layer reports `Ready=True` with `lastAppliedRevision` equal to `Bucket.status.artifact.revision`: `kubectl -n flux-system get ks -o json | jq` returns zero violations.
- [ ] **19-AC-4** The Bucket semantics are re-verified: the artifact downloaded from source-controller contains paths beginning `<cluster>/<S>/gitops/` (prefix retained), and the recomputed tree hash equals `S:gitops`. The integrity check log line `artifact-integrity: pass` is in the journal.
- [ ] **19-AC-5** `kubectl get secrets -A -l strange.company/out-of-band=true -o name` returns exactly `secret/sops-age` and `secret/seed-bucket-credentials` in `flux-system`, plus `secret/seed-bucket-ca` if and only if `S3_CA_BUNDLE` is set.
- [ ] **19-AC-6** Forgejo healthz returns 200 in-cluster and at `https://git.<BASE_DOMAIN>/api/healthz`.
- [ ] **19-AC-19** The lab-VM e2e (`make e2e-lab LAB_SUBSTRATE=lab-seed`, plan 52/05) passed A1–A7, A9 and A10 for the commit seeded on the node (real Longhorn, first-install `longhorn-upgrade-gate`); its JUnit is attached to the P04 evidence (SEED-12).
- [ ] **19-AC-20** Cluster-directory delivery (SEED-1): `kubectl -n flux-system get ks cluster-config -o jsonpath='{.status.inventory.entries[*].id}'` contains every object listed in `gitops/clusters/<c>/cluster-config/kustomization.yaml` (compared by `sc-gitops inventory --cluster <c> --layer cluster-config`), and the root's inventory contains no object outside `flux-system` except layer Kustomizations.
- [ ] **19-AC-7** Re-running `SC_TEST_STOP_AFTER=seed ./bootstrap --from-phase seed` after completion makes no change: the journal shows `skipped: already-complete`, and no object in seed namespaces changes `resourceVersion` during the run, except status-only updates, identified via `managedFields` subresource `status`.

### GATE-P06

- [ ] **19-AC-8** `./bootstrap --from-phase flux-bootstrap` prints `SOURCE SWITCH COMPLETE <F>`. The `source-switch` report validates against the schema with `result: pass` (which the contract only allows with T0–T7 and G1–G7 all `pass`), `uid_comparison.changed: []` covering the root inventory, all CRDs, seed PVCs and PVs, and every `helm_releases[].revision_before == revision_after`. `s3://<prefix>-bootstrap-seed/<cluster>/reports/transfer-pre-<S>.json` exists and its sha256 equals the journal `outputRef`.
- [ ] **19-AC-9** `./bootstrap exec -- kubectl get buckets.source.toolkit.fluxcd.io -A` returns no resources, and `secret/seed-bucket-credentials` is absent.
- [ ] **19-AC-10** `./bootstrap exec -- sc-bootstrap verify control-transfer` reports G1–G7 pass.
- [ ] **19-AC-11** `make e2e-handoff` passes on the off-cluster e2e lane for the commit that implements P06 (A1–A11, N1–N8). The JUnit report is attached to the P06 evidence.
- [ ] **19-AC-12** Rollback and fault drills never run on the production node (destructive). Instead, N4, N5 and N6 passed in 19-AC-11 (k3d) **and** N4-lab, N5-lab and N6-lab passed in the lab-VM gate (19-AC-21) for the exact `sc-bootstrap` binary digest used on the node, which the report field `tool.sc_bootstrap_digest` records.
- [ ] **19-AC-21** Lab-VM fault gate (CD-31): `make e2e-lab LAB_SUBSTRATE=lab-seed LAB_SCENARIO=handoff-faults` passed N4-lab, N5-lab and N6-lab with the expected results above (PVC, PV, Longhorn `Volume`, CNPG `Cluster` and instance-PVC UIDs unchanged; marker commit `M` and marker issue present; release histories as stated) for the commit and `sc-bootstrap` digest that implement P06. Its JUnit is attached to the P06 evidence. Required for GATE-P06.
- [ ] **19-AC-13** A no-op commit merged to Forgejo `main` after transfer is applied within 3m with no Helm revision change (also covered by 18-AC-13).
- [ ] **19-AC-17** Version re-verification of strict substitution and escaping (SEED-4): A11 passed in 19-AC-11 at the pinned Flux version, and `sc-gitops validate` reports `root-substitution-scope` pass for `ci-blackstart`.
- [ ] **19-AC-18** `flux diff` semantics used by G3 (SEED-12) are re-verified at the pinned Flux version in the k3d run: on an unchanged layer it exits 0; after a manual `kubectl annotate` on a layer object it exits 1; a SOPS Secret in the layer is skipped, not reported as drift.

### GATE-P12 (co-owned with plan 50)

- [ ] **19-AC-15** `./bootstrap verify` runs C1–C8 and writes a `final` report with `result: pass` that validates against `control-transfer-report.schema.yaml` (the contract requires exactly C1–C8, each `pass`, for a `final` pass). Only then does `./bootstrap` print `CONTROL TRANSFER COMPLETE`. A test with C8 forced to fail (plan 50 fault hook) prints no such line and exits non-zero.
- [ ] **19-AC-16** At P12, `flux get all -A --status-selector ready=false` prints nothing (all post-transfer layers Ready). Prometheus `ALERTS{alertname=~"SCFlux.*",alertstate="firing"}` returns no series.

### GATE-H04 (black start; plan 45's fixture and `recover` pipeline exist; DR-17)

- [ ] **19-AC-14** Black-start variant: `make e2e-handoff BLACKSTART=1` passes. That run seeds from a fixture "GitHub" clone with a recovery-patched cluster dir (plan 45 fixture, the same as F11's `ci-blackstart`), restores a fixture Forgejo repo, rebuilds the recovery commit on a descendant restored head (§18.3), transfers, and asserts A2–A6. Unconditional at this gate; if the lead adds an earlier recover-pipeline node (DR-17), this AC moves there.

## 24. Explicit non-goals

- `helm install` of seed components from the operator machine, followed by adoption (ADR-0005 rejects it).
- Sourcing GitHub directly from the cluster during seed or normal operation.
- A temporary in-cluster Git server.
- Seeding Authentik, Woodpecker, monitoring or the `strange-company` chart.
- Automatic re-transfer after a Forgejo outage (handled by Forgejo restore, not a source switch).

## 25. Open questions

| # | Question | Blocking? | Proposed resolution | Owner |
|---|---|---|---|---|
| Q1 | The Forgejo SSH host key is pre-generated by `sc-bootstrap` instead of by forge-init (ADR-0016 wording). | no | Adopt pre-generation (§8.2). Ask the lead to amend ADR-0016's sentence during consolidation. Plan 35 registers `SC-FORGEJO-SSH-HOST-KEY`. | lead / 16 / 35 |
| Q2 | The snapshot uploads the `gitops/` subtree, not the whole repository tree (ADR-0005 says "the `git archive` tree of `HEAD`"). | no | Upload `gitops/` only. Flux needs nothing else in the seed. A whole-repo upload multiplies the object count by about 10× against the Bucket's 5m fetch timeout. Exact identity is preserved by `commit_sha` + `gitops_tree_sha`. | lead |
| Q3 | Does CNPG accept a steady-state `Cluster` whose `bootstrap` stanza differs from the one it was created with (black start)? | no (blocks 19-AC-14 only) | Plan 45/12 decide. The default is to keep the recovery stanza committed (§18.3). | 45 / 12 |
| Q4 | S3 providers without `x-amz-checksum-sha256`. | no | Fall back to `Content-MD5`. Integrity is ultimately proven by the artifact tree-hash check (§8.3), so the upload checksum is only an early-fail optimization. | 14 |
| Q5 | Flux source Secret naming differed between plan 16 and plans 18/19. | no | **Resolved by CD-08:** Secret `flux-system/flux-system`, SOPS file `flux-system/flux-source-auth.sops.yaml`, pre-generated before seed; deploy-key title `flux-readonly`. | — |
| Q6 | SPEC §29 allows any S3-compatible store, possibly on a private network or non-443 port, and `existing` nodes may use private DNS upstreams. Plan 36's "Internet egress excludes private ranges" rule then blocks the seed after `policies` (SEED-3). | no | Plan 36 renders `s3-egress` and `dns-upstream` components from `cluster-vars`/node facts; plan 50 preflight rejects or flags unsupported endpoint placements. Proposed SPEC issue for `spec-issues.md` (interpretation). | 36 / 50 / lead |
