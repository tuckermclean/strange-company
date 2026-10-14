# 48 — Upgrades

**Status:** Stage-0 plan (draft)
**DAG nodes:** contributes criteria to F10 (k3s and OS upgrade playbooks in the lab), P04 (first-install gate behavior), P10 (backup-before-upgrade gates), P11 (Renovate PR → CI → merge → Flux path) and C10 (release regression gate); listed in `plans/dag.yaml` except P04 (GC-09: add `"48"` to P04).
**Related plans:** [01](01-repository.md) (version locks, `compat.yaml`), [02](02-bootstrap-toolbox.md), [07](07-k3s.md), [08](08-node-security.md), [09](09-networking.md), [10](10-tls-certificates.md), [12](12-cnpg.md), [13](13-longhorn.md), [15](15-authentik.md), [16](16-forgejo.md), [18](18-flux.md) (remediation classes, layers), [20](20-woodpecker.md), [23](23-supply-chain.md) (Renovate, signing), [24](24-hermes.md), [26](26-agent-runner.md), [28](28-harnesses.md), [44](44-backup.md), [45](45-recovery.md), [47](47-ha-expansion.md), [50](50-bootstrap-orchestrator.md), [53](53-model-gateway.md), [54](54-strange-company-chart.md)
**Contracts:** consumes the version locks of plan 01 (`versions/k3s.lock.yaml`, `versions/platform.lock.yaml`, `versions/compat.yaml`, `toolbox/tools.lock.yaml`); produces none
**ADRs:** relies on ADR-0005, ADR-0013, ADR-0016, ADR-0019, ADR-0304 (remediation classes); introduces ADR-0154 (k3s lifecycle via Ansible, no system-upgrade-controller)

## 1. Purpose

Define how every versioned part of Strange Company is upgraded after control transfer, and what rollback means for each: k3s, the node OS, Flux, every third-party HelmRelease, PostgreSQL majors under CNPG, Longhorn, the stateful applications with schema migrations (Forgejo, Authentik, Woodpecker), Strange Company's own components and database migrations, Hermes, harness images and the toolbox. The common rule is: a version changes only through a reviewed Git change, verified by CI first, applied by Flux (cluster workloads) or by `./bootstrap` (node-level software), with state protected before any irreversible step.

## 2. Requirements derived from SPEC.md

1. Production state originates from version-controlled desired state; routine platform management happens through Git after control transfer. [SPEC §4.2, §35]
2. Coding agents do not deploy production; CI does not normally deploy production directly; Flux is the deployment authority. [SPEC §4.2, §33]
3. Pinned production dependencies, immutable image references, automated dependency-update PRs. [SPEC §37, §116]
4. Chart releases pass lint, schema validation, template, k3d install, **upgrade**, uninstall and reinstall tests. [SPEC §116]
5. Critical k3s server configuration stays consistent across control-plane nodes. [SPEC §16]
6. Migrations have pre/post invariants defined first (verification-first). [SPEC §57]
7. Backups are restorable and restore tests are visible. [SPEC §26, §109]
8. Tool versions are version-controlled. [SPEC §9]

Derived requirements:

- **48-R-1** One component (or one tightly coupled set, such as a chart and its CRDs) per upgrade PR. No upgrade PR is merged while another upgrade's convergence is unfinished, during expansion (plan 47), or while a critical alert fires.
- **48-R-2** Every stateful application upgrade is preceded, inside the cluster, by a completed on-demand backup taken for that specific target version (backup-before-upgrade gate, §8.3). Flux cannot start the application upgrade until the gate succeeds.
- **48-R-3** Rollback is defined per class (§9). Where a downgrade is unsafe (minor Kubernetes, Longhorn, schema-migrated apps, PostgreSQL majors), rollback means restore-from-the-gate-backup, never an image downgrade.
- **48-R-4** Node-level software (k3s, OS packages) is upgraded by Ansible from the toolbox, one node at a time, with health gates between nodes (ADR-0154).
- **48-R-5** Security fixes for exposed components (Traefik, cloudflared, Forgejo, Authentik, k3s, the OS) are merged within 7 days of release, critical ones within 48 hours.

## 3. Inputs

| Input | Source |
|---|---|
| Version locks | `versions/k3s.lock.yaml`, `versions/platform.lock.yaml`, `toolbox/tools.lock.yaml`, `toolbox/image.lock`, `policy/harnesses.yaml` (harness digests) |
| Compatibility matrix | `versions/compat.yaml` (supported Kubernetes minors per component; recorded third-party facts) |
| Renovate PRs | `sc-renovate` bot (plan 23) on `Forgejo/strange-company` |
| Upstream release notes and advisories | humans reviewing the PR; Renovate attaches release notes |
| Backup state | plans 12, 13, 44 metrics |

## 4. Outputs

- Merged upgrade PRs; Flux-reconciled releases; node software at the locked versions.
- Gate Jobs `upgrade-gate-<app>-<version>` and their backups (§8.3).
- Upgrade evidence: CI runs, gate Job logs, and for node-level upgrades a journal pipeline `upgrade-k3s` / `upgrade-os` (plan 50).

## 5. Dependencies

- **Plan 01:** `make locks-check` enforces cross-file consistency (Kubernetes minor of `k3s.lock.yaml` = `platform.lock.yaml` `kubernetes.minor` = toolbox kubectl minor; every pinned component supports that minor in `compat.yaml`).
- **Plan 18:** remediation classes `controller` (auto rollback) and `stateful-app` (no auto rollback, ADR-0304); new layers `<app>-upgrade-gate` (§8.3) that the app layers depend on (Q-48-02).
- **Plan 23:** Renovate configuration (schedules, grouping, digest pinning), image signing and promotion checks on upgraded internal images.
- **Plan 50:** CLI `./bootstrap upgrade k3s --to <version>` and `./bootstrap configure --tags os-upgrade` (assumed; plan 50 to add), journal pipelines.
- **Plans 12/13/44:** on-demand backup mechanics (CNPG `Backup` with the barman-cloud plugin; Longhorn `Backup` of a snapshot) used by the gate Jobs.

## 6. Owned state

None durable. Version locks are owned by plan 01; this plan owns the procedures, the gate layers and the policy.

## 7. External interfaces

| Interface | Consumer | Definition |
|---|---|---|
| `./bootstrap upgrade k3s --to <version>` | operator | §14.1, `procedures/upgrade-k3s.md` |
| `./bootstrap configure --tags os-upgrade [--nodes …]` | operator | §14.2 |
| PR labels `upgrade/controller`, `upgrade/stateful`, `upgrade/node`, `upgrade/sc` | Renovate, reviewers, CI | §9.2 |
| Gate Job contract | plans 12, 13, 16, 15, 20, 24, 26 | §8.3 |
| `procedures/upgrade-platform-component.md` | operator | all Flux-delivered components |

## 8. Internal interfaces

### 8.1 Upgrade classes

| Class | Members | Path | Automatic rollback | Manual rollback |
|---|---|---|---|---|
| **node** | k3s, OS packages | PR → merge → `./bootstrap upgrade k3s` / `configure --tags os-upgrade` | no | patch: previous binary; minor: none in place (restore etcd snapshot with the old binary only if the API is broken) |
| **gitops-self** | Flux | PR regenerating committed manifests (ADR-0016) → Flux upgrades itself | no | revert PR; if Flux cannot reconcile, apply the previous `gotk-components.yaml` from the toolbox |
| **controller** | cert-manager, CNPG operator + barman plugin, Longhorn, Traefik, cloudflared, kube-prometheus-stack (+ CRDs), OTel Collector, kured, Loki, Tempo | Renovate PR → CI → merge → Flux | yes (`upgrade.remediation.strategy: rollback`, 3 retries) except Longhorn (no downgrade) | revert PR, except Longhorn |
| **stateful-app** | Forgejo, Authentik, Woodpecker, Grafana, Hermes | Renovate PR → CI → merge → **gate Job** → Flux | no (`retries: 0`) | PITR/volume restore to the gate backup + revert PR together |
| **sc** | `strange-company` chart: Runner, Gateway, egress-proxy, `sc-verify`, `sc-ci-config` | release PR from trusted CI (plan 23) → merge → gate (`sc-pg`) → Flux | no | revert PR (N-1 compatible migrations, §8.4) |
| **database-major** | PostgreSQL major in any CNPG cluster | dedicated PR per cluster | no | restore the pre-upgrade backup into the previous major (§14.5) |
| **workload-image** | harness images, `sc-verify`, toolchain images | PR to `policy/harnesses.yaml` or `.strange-company/validation.yaml` | n/a | revert PR (running Jobs keep their digest) |
| **toolbox** | `toolbox/tools.lock.yaml`, `toolbox/image.lock` | PR → trusted CI build/publish (plans 02/23) → operator pulls on next run | n/a | previous image digest from Git history |

### 8.2 CI checks on every upgrade PR

- `make locks-check` (plan 01) and `make gitops-validate` (plan 18 F11, including `remediation-class`, `seed-chart-source`, compat).
- k3d upgrade test (SPEC §116): install the **previous** pinned version of the changed component (from the PR's merge base), create fixture objects (e.g. a Certificate, a CNPG Cluster with a marker row, a Longhorn-less PVC), upgrade to the PR's version, assert fixtures intact and component Ready. For seed-set components the full seed→transfer test (plan 19) also runs.
- `make capacity-check` (plan 51) when requests/limits change.
- Security scan of new images (plan 23).
- For the `sc` class (and every upgrade that changes a Strange Company image, the `strange-company` chart, `sc-verify`, `sc-ci-config`, harness images or the fake harness): chart upgrade test from the last released chart with a fixture `sc-pg` database on every PR. **Release regression gate (GC-07, option a):** the complete C10 fake end-to-end suite (every SPEC §103 fake scenario and every SPEC §106 TDD acceptance test, plans 21/46) runs on the **off-cluster lane** (plan 52 §8.5, ADR-0303) against each release candidate, with no paid model API (SPEC §104). Plan 23's promotion check refuses a promotion PR whose candidate digests lack a passing C10 attestation; this runs inside the existing required CI context, so no new status context is introduced. A per-PR merge-blocking C10 status is **not** used: no lane can run a cluster-backed suite per PR.
- For Hermes: the L-1 behavior suite (ADR-0003 C08 tests) against the new digest in k3d.

### 8.3 Backup-before-upgrade gate (48-R-2)

For each stateful app, layer `<app>-upgrade-gate` (`forgejo-upgrade-gate`, `authentik-upgrade-gate`, `woodpecker-upgrade-gate`, `grafana-upgrade-gate` (no-op: Grafana is stateless), `hermes-upgrade-gate`, `sc-upgrade-gate`) sits between the app's database layer and the app layer: the app layer `dependsOn` it. It contains one Job whose **name embeds the target version**: `upgrade-gate-<app>-<version with dots replaced by dashes, truncated to 63 chars>`. A version bump therefore creates a new Job that must complete before Flux applies the app's new HelmRelease; an unchanged version keeps the old completed Job (no rerun).

The Job (image: the toolbox image digest, uid 10001; `ttlSecondsAfterFinished` unset so it stays as evidence until the next bump prunes it; `backoffLimit: 0`; `activeDeadlineSeconds: 2700`; PriorityClass `platform`):
1. creates CNPG `Backup` `gate-<app>-<version>` for the app's cluster (`method: plugin`, barman-cloud) and waits for `phase: completed`;
2. for Forgejo and Hermes, creates a Longhorn `Backup` of a fresh snapshot of the app's volume and waits for `state: Completed`;
3. writes a ConfigMap `upgrade-gate-<app>` with `version`, `backup_ids`, `completed_at` (the rollback anchor);
4. exits 0 only if every backup completed.

ServiceAccount `upgrade-gate` per namespace (SEED-2) with only: `get` on `helmreleases.helm.toolkit.fluxcd.io` (`resourceNames: [<app>]`, own namespace); `get,create,update,patch` on ConfigMaps (`resourceNames: [upgrade-gate-<app>]` for get/update/patch; `create` cannot be name-restricted, so a plan 37 VAP limits its creates to that name); `create,get,list,watch` on `backups.postgresql.cnpg.io`; for the Longhorn and Forgejo/Hermes gates a Role in `longhorn-system` with `list,get` on `volumes.longhorn.io` and `create,get,watch` on `backups.longhorn.io` and `snapshots.longhorn.io` (creates restricted by a plan 37 VAP to objects labeled `strange.company/upgrade-gate`). Gate Job pods carry label `strange.company/upgrade-gate=true`; plan 36 row **API-2** allows exactly them to reach the API server (default-deny would otherwise block the seed-set `forgejo-upgrade-gate` until its deadline and deadlock the seed).

If the gate fails, the app layer stays at the old version and `SCUpgradeGateFailed` fires. Nothing has changed in the app.

**First install (seed, fresh install, black start).** `forgejo-upgrade-gate` and `longhorn-upgrade-gate` are in the seed set (their app layers are; plan 18 seed-closure rule), so they run before any previous version exists. The Job first performs an authorized discovery call (`GET /apis/helm.toolkit.fluxcd.io/v2` and, for Longhorn, `/apis/longhorn.io/v1beta2`); a 404 for the API group or the object means "absent", while a 403 or any other error fails the gate (it never counts as absent). It then checks whether the guarded release is installed: HelmRelease `<app>` absent, or present with an empty `status.history` (for Longhorn: no `volumes.longhorn.io` with a `strange.company/state-id` label). If so it writes ConfigMap `upgrade-gate-<app>` with `version`, `backup_ids: []`, `first_install: "true"`, takes **no** backup, and exits 0. In every other case, including a missing ConfigMap while the release is installed, it takes the backups as above (it never skips because state merely looks new). The same rule applies to `authentik-`, `woodpecker-`, `sc-`, `grafana-` and `hermes-upgrade-gate` (plan 18 layers); `grafana-upgrade-gate` always records the version without a backup (Grafana is stateless), `hermes-upgrade-gate` backs up the `hermes-data` Longhorn volume.

### 8.4 Strange Company database migrations

- Per CD-04 (GC-08) the **Runner** applies every `sc-pg` migration (including the `ledger` schema) **in-process at startup**, under a PostgreSQL advisory lock, forward-only; there is no migration Job or Helm hook. Ordering: the `strange-company` layer `dependsOn` `sc-upgrade-gate`, so the gate backup completes before the new Runner starts. The Gateway does not migrate and stays not-Ready until `schema_version` ≥ its required version.
- **Expand/contract rule:** release N's migrations must leave the schema usable by release N−1's code (add columns/tables, backfill, dual-write). Destructive changes (drop/rename) ship no earlier than release N+1, after N has run in production. CI enforces it with the "N−1 compatibility test": run release N−1's integration tests against a database migrated to N.
- Each migration declares pre- and post-invariants (SQL assertions) checked by the Runner's migrator before and after applying (SPEC §57 verification-first).
- Rollback of the `sc` class: revert the PR (old images run against the expanded schema). A contract migration that went wrong is fixed forward or PITR-restored (`restore-cnpg.md`) to the gate backup.

## 9. Configuration

### 9.1 Version selection rules (summary; component plans hold the specifics)

| Component | Rule |
|---|---|
| k3s | plan 07 §19.1: latest patch of the newest minor on the k3s `stable` channel, GA ≥ 30 days, supported by every pinned component in `compat.yaml`, ≥ 1.30 |
| OS | Debian 13 / Ubuntu 24.04 security pocket automatically; point upgrades monthly; release upgrades per §14.2 |
| Flux | latest minor ≥ 14 days old supporting the pinned Kubernetes minor |
| Third-party charts | latest release ≥ 14 days old (Renovate `minimumReleaseAge: 14 days`), except security releases (no wait) |
| PostgreSQL | stay on a major until it has < 12 months of community support left, then move to the newest major CNPG supports and that has ≥ 1 minor release (x.1) |
| Longhorn | latest patch of the current minor; minors one at a time along Longhorn's documented upgrade path |
| Internal components | released by trusted CI from `main` (plan 23) |

### 9.2 Renovate policy (plan 23 renders `renovate.json`)

- Schedule: weekly window Monday 06:00–10:00 UTC for non-security updates; security updates immediately (`vulnerabilityAlerts`).
- One PR per component; CRD-bearing charts group their CRD chart with the controller (e.g. `prometheus-operator-crds` + kube-prometheus-stack).
- Labels by class (`upgrade/controller`, `upgrade/stateful`, `upgrade/node`, `upgrade/sc`, `upgrade/database-major`); PostgreSQL major and Kubernetes minor PRs are created but marked `do-not-merge/needs-procedure` until the operator schedules them.
- Auto-merge: none in MVP (ADR-0019). Reviewers see the class label, release notes and the CI upgrade test result.
- Renovate never touches `gitops/clusters/**` (generated), `secrets/**` or `policy/**` except `policy/harnesses.yaml` digests.

### 9.3 Upgrade windows and freezes

- Node-class upgrades and `database-major` upgrades run inside the maintenance window (plan 08, default Sunday 04:00–06:00 UTC) unless the operator overrides for a security fix.
- Freeze rule (operator-checked, GC-07: no `upgrade-freeze` status context exists): do not merge an `upgrade/*` PR while an expansion pipeline is running, an earlier upgrade PR's converge is not Ready (Flux object not Ready at its merge SHA), or `SCUpgradeGateFailed`, `SCFluxReconciliationFailing` or any critical alert fires. `upgrade-platform-component.md` lists the checks; `./bootstrap upgrade k3s` enforces them in its preflight.

## 10. Secrets

No new secret class. Gate Jobs use only their ServiceAccounts. `./bootstrap upgrade k3s` uses `SC-SSH-OPERATOR` and the k3s token from the bundle (unchanged by upgrades).

## 11. Storage

Gate backups land in the existing backup buckets under the current generation (`pg-backups/<cluster>/gen-<N>/`, `longhorn-backups`) and follow plan 43 retention; the newest gate backup per app is additionally protected for 30 days (backup label `strange.company/upgrade-gate`, plan 43 rule). Pre-upgrade etcd snapshots are local + S3 (plan 07; local pruning plan 08 §9.9a).

## 12. Resource requirements

Gate Jobs: 50m / 128Mi, minutes per upgrade. CNPG and Longhorn backups use their own components' resources. k3s upgrades: one server restart at a time.

## 13. Security boundary

- Only humans merge upgrade PRs; Renovate's identity can open PRs but not merge (plan 23/55).
- New images are verified by plan 23's promotion policy (signed internal images; digest-pinned upstream).
- The gate SA cannot delete backups or touch other apps' clusters.
- k3s binaries are checksum-verified (plan 07 07-AC-6).

## 14. Happy path

### 14.1 k3s (patch and minor) — `procedures/upgrade-k3s.md`

1. PR updates `versions/k3s.lock.yaml` (version + sha256), for a minor also `platform.lock.yaml` `kubernetes.minor`, `toolbox/tools.lock.yaml` kubectl (skew 0, plan 01) and `compat.yaml`. CI: locks-check, lab upgrade test (`make e2e-lab-k3s-upgrade`: install the previous version on 3 VMs, upgrade, run 07-AC-* and 36-AC-3/4, 51-AC-6/14 re-verifications). Merge.
2. The operator pulls `main` (new toolbox if kubectl changed) and runs `./bootstrap upgrade k3s --to <version>`:
   - preflight: lock matches `--to`; minor step ≤ 1 from the running version (`K3S_SKIP_MINOR_REFUSED`); all nodes Ready; etcd healthy; no expansion; no critical alert; backups fresh (as plan 47 47-R-4);
   - `k3s etcd-snapshot save --name pre-k3s-upgrade` and S3 confirmation;
   - Runner dispatch paused (minor upgrades only) and running workers allowed to finish;
   - per server, `serial: 1`: (small-ha) cordon + drain (PDBs, 15 min timeout); install the new binary (checksum); re-render the CoreDNS manifest for the bundled CoreDNS version (plan 09); restart k3s; wait Ready ≤ 300 s; `sc-etcdctl endpoint health --cluster`; `kubectl get --raw /readyz`; uncordon; soak 5 min with no new critical alert;
   - post: every node reports the new `kubeletVersion`; Flux all Ready; `./bootstrap verify --only V-10,V-12,V-13,V-22`; plan 11's `storage-audit` including SA-8 (every `ci-workspace` PV lies under `/var/lib/sc/ci-workspace/`; STO-8: the k3s upgrade may re-apply the local-path addon config); dispatch resumed.
3. Rollback: patch — reinstall the previous binary with the same procedure (the PR is reverted first). Minor — no in-place downgrade (Kubernetes does not support it). If the API is broken after upgrading the first server, stop and restore that server: `restore-etcd.md` §A with the **previous** binary and the `pre-k3s-upgrade` snapshot (single node), or remove and re-add the server (small-ha, quorum held by the others).

### 14.2 OS

- **Point updates:** `./bootstrap configure --tags os-upgrade` → `playbooks/os-upgrade.yml` (`serial: 1`): drain (small-ha), `apt-get update && apt-get -y dist-upgrade` with `--force-confold`, reboot if `/var/run/reboot-required`, wait Ready, uncordon, health gates. Single node: no drain, the reboot is the outage.
- **Release upgrades** (Debian 13 → 14, Ubuntu 24.04 → 26.04): small-ha by node replacement (`add-node.md` with the new OS, then `remove-node.md` of an old node, three times). Single node: in place (`apt full-upgrade` to the new release / `do-release-upgrade`) only after the OS pair passes `make e2e-lab-os-upgrade` and with fresh backups, a `pre-os-upgrade` etcd snapshot, and the Runner paused; rollback is recovery from backups (plan 45).
- OS selection additions follow plan 08 §9.2.

### 14.3 Flux

PR regenerates `gitops/clusters/*/flux-system/gotk-components.yaml` with the new Flux CLI (`flux install --export`) and bumps the toolbox Flux CLI (plan 01 consistency). CI: F11 regeneration diff, k3d seed→transfer test. Merge; Flux applies its own manifests (controllers restart). Check: `flux check` and every Kustomization Ready at the merge SHA within 15 min. Rollback: revert PR; if controllers cannot start, `./bootstrap exec -- kubectl apply --server-side -f <previous gotk-components.yaml>`.

### 14.4 HelmReleases (controller and stateful-app classes) — `procedures/upgrade-platform-component.md`

1. Renovate PR (chart version + digest in `platform.lock.yaml`, values changes if needed by the author). CI per §8.2. Human review of release notes for breaking changes; breaking values changes are made in the same PR.
2. Merge. Flux: `stateful-app` → gate Job → HelmRelease upgrade; `controller` → HelmRelease upgrade with automatic rollback on failure.
3. Converge check (≤ 30 min): HelmRelease `Ready=True` with `status.history[0].chartVersion` = new version; the component's plan gate checks (e.g. 16-AC-1 for Forgejo, 10-AC-2 for cert-manager); no new alerts.
4. Failure: controller class rolls back automatically and the PR is reverted; stateful class stays failed (`SCFluxReconciliationFailing`) → `upgrade-platform-component.md` §Stateful failure (restore to the gate backup + revert).

### 14.5 PostgreSQL major (per CNPG cluster; plan 12 owns mechanics)

Order: `woodpecker-pg` → `authentik-pg` → `forgejo-pg` → `sc-pg`, one per maintenance window. Method: CNPG's declarative in-place major upgrade (changing `imageName`/`imageCatalogRef` to the new major triggers `pg_upgrade`; available in CNPG ≥ 1.26, verify) when the pinned operator supports it; otherwise the import method (new cluster `<name>-pg<major>` with `bootstrap.initdb.import` from the old cluster, then a cut-over PR switching the app's Service references). In both methods: gate backup first; bump `CNPG_<ID>_GEN` (new backup path, ADR-0200, because WAL from different majors must not share a path); restore test of the new cluster within 24 h (plan 44). Rollback: restore the gate backup into a cluster on the previous major (`restore-cnpg.md`).

### 14.6 Longhorn

Rules: only Longhorn's supported path (one minor at a time, no skipping, **no downgrade**); before merging: all volumes `healthy`, gate-style Longhorn backups of every `strange.company/state-id` volume < 6 h old (`longhorn-upgrade-gate` layer, same pattern as §8.3), `pre-longhorn-upgrade` etcd snapshot (taken by the operator per procedure), no node in maintenance. After the manager upgrade, engine upgrades proceed automatically up to `concurrentAutomaticEngineUpgradePerNodeLimit` (plan 13); converge waits until every volume reports the new engine image. Rollback: none in place; recovery is volume restore from backups (`restore-longhorn.md`).

### 14.7 Forgejo, Authentik, Woodpecker

Stateful-app class with gates. Additional rules: Forgejo upgrades follow Forgejo's documented path (no skipping major versions whose migrations depend on intermediates, verify per release); Authentik upgrades one minor (year.month) at a time; Woodpecker majors require plan 20's agent/server version match (server and agents in the same PR).

### 14.8 Strange Company components, Hermes, harnesses, toolbox

- **sc:** release PR (plan 23) bumps the chart and image digests; the off-cluster C10 release regression (§8.2) must have passed for the promoted digests; §8.4 migrations; gate `sc-upgrade-gate`; Runner drains in-flight dispatch gracefully (plan 26 SIGTERM behavior); Gateway rolls with `maxUnavailable: 0` (plan 53).
- **Hermes:** digest bump PR with the L-1 behavior suite; `hermes-upgrade-gate` Longhorn backup of `/opt/data`; Hermes is non-authoritative, so rollback = revert + restore the volume backup if its state became unreadable.
- **Harness images:** PR to `policy/harnesses.yaml` (digest); CI runs the harness contract tests with recorded fixtures (plan 28); new executions use the new digest after Flux delivers the policy ConfigMap; running executions are unaffected; rollback = revert.
- **Toolbox:** PR to `toolbox/tools.lock.yaml`; trusted CI builds, tests (`02-AC-*`) and publishes, updating `toolbox/image.lock`; operators get it on the next `./bootstrap` run; installations that use a locally built toolbox re-import it into node containerd (plan 47 §14.3 C6 pattern).

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Gate backup fails | `SCUpgradeGateFailed` | app stays on old version | none (fail closed) | `upgrade-platform-component.md` §Gate |
| Controller upgrade fails | HelmRelease remediation; `SCFluxReconciliationFailing` | brief degradation | rollback (3 retries) | same §Controller |
| Stateful app migration fails | HelmRelease failed; app unhealthy | app outage | none | same §Stateful (restore + revert) |
| k3s server fails after binary swap | upgrade step timeout | one server down (small-ha: quorum held) | stop | `upgrade-k3s.md` §Rollback |
| Version skew left behind (partial upgrade) | `SCK3sVersionDrift` (plan 07) | mixed versions | none | resume `./bootstrap upgrade k3s` |
| Longhorn engine upgrade stuck | volume engine image mismatch > 1 h | old engine keeps working | none | plan 13 |
| Toolbox kubectl skew | `make locks-check` | PR blocked | — | — |

## 16. Retry behavior

Controller-class HelmReleases: 3 remediation retries (plan 18). Gate Jobs: `backoffLimit: 0`; a new attempt requires deleting the failed Job (procedure step) so that a human looks at the failure first. `./bootstrap upgrade k3s` resumes from the journal at the first unfinished server.

## 17. Idempotency behavior

Gate Jobs are keyed by version (re-applying the same version is a no-op). `./bootstrap upgrade k3s` skips servers already at the target version (binary sha256 + `kubeletVersion`). `os-upgrade.yml` is convergent (apt).

## 18. Bootstrap behavior

Before control transfer, versions are whatever the install commit pins; no upgrade flow applies. Black start installs the versions pinned in the GitHub `main` it clones; if those are newer than the backed-up data's application versions, the stateful apps migrate forward on start (the same as an upgrade), which plan 45 accounts for by restoring first and upgrading second (recovery overlay pins nothing; the recovery commit must not bump versions).

## 19. Upgrade behavior

This plan is the upgrade behavior. Changes to the upgrade machinery itself (gate layers, playbooks) follow the `sc` / node class rules.

## 20. Recovery behavior

Per-class rollback in §8.1; detailed steps in `procedures/upgrade-k3s.md` and `procedures/upgrade-platform-component.md`. The recovery anchors are: the gate backup (ConfigMap `upgrade-gate-<app>`), the `pre-k3s-upgrade` etcd snapshot, and the previous Git revision.

## 21. Observability

- Flux HelmRelease metrics and `gotk_resource_info` (plan 18/38); `sc_k3s_version_info` (plan 07).
- Gate Job status via KSM.
- **Alerts introduced (plan 42 to register):**

| alertname | Expression sketch | for | Severity / class | Runbook |
|---|---|---|---|---|
| `SCUpgradeGateFailed` | `kube_job_status_failed{job_name=~"upgrade-gate-.*"} > 0` | 0m | warning / notify | `upgrade-platform-component.md` §Gate |
| `SCK3sUpgradeIncomplete` | `count(count by (version) (sc_k3s_version_info)) > 1` for 6 h during and after an upgrade run (complements `SCK3sVersionDrift`) | 6h | warning / notify | `upgrade-k3s.md` |

## 22. Testing strategy

- **Lab:** `make e2e-lab-k3s-upgrade` (patch and minor, 1 and 3 VMs); `make e2e-lab-os-upgrade` (point; release pair when added).
- **k3d:** gate layer test: bump a fixture app's version; assert the app HelmRelease does not upgrade until the gate Job completes, and stays on the old version when the gate Job is forced to fail (fake backup CRD controller that marks the backup failed).
- **CI on every upgrade PR:** §8.2.
- **Live:** gates below.

## 23. Acceptance criteria

### GATE-F10 (contributed)

- **48-AC-1** `make e2e-lab-k3s-upgrade` passes on Debian 13 and Ubuntu 24.04: from the previous k3s patch to the pinned patch on 3 VMs, never more than one server NotReady at a time (Ansible log), `sc-etcdctl endpoint health --cluster` healthy between servers, final `kubeletVersion` uniform, and a minor-skip attempt fails with `K3S_SKIP_MINOR_REFUSED`.
- **48-AC-2** `make e2e-lab-os-upgrade` (point upgrade with a fixture package bump) reboots each VM in turn and leaves 07-AC-2's `changed=0` property intact.

### GATE-P10 (contributed)

- **48-AC-3** `make e2e-upgrade-gate` (k3d) passes both scenarios in §22.
- **48-AC-4** Live dry run: bumping a fixture annotation that changes the gate Job name for `woodpecker` (test PR on a scratch branch rendered with `--dry-run=server`) shows the Job manifest `upgrade-gate-woodpecker-<v>`; applying the real gate for the currently installed version produces a completed CNPG `Backup` and ConfigMap `upgrade-gate-woodpecker` with a non-empty `backup_ids`.

### GATE-P11 (contributed)

- **48-AC-5** One Renovate patch PR for a controller-class chart (e.g. cloudflared or Traefik) goes through CI (locks-check, render, k3d upgrade test) and, after human merge, the HelmRelease reports the new chart version within 30 min with no failed remediation (`kubectl get hr -A -o json | jq` shows `history[0].status == "deployed"`).
- **48-AC-6** `renovate.json` in the repository validates with `renovate-config-validator` and contains `minimumReleaseAge: "14 days"`, a weekly schedule, `vulnerabilityAlerts.enabled: true`, and package rules that label PostgreSQL major and Kubernetes minor updates `do-not-merge/needs-procedure`.

### GATE-P04 (contributed)

- **48-AC-8** After seed: `kubectl -n forgejo get cm upgrade-gate-forgejo -o jsonpath='{.data.first_install} {.data.backup_ids}'` prints `true []`, the same holds for `longhorn-system/upgrade-gate-longhorn`, both gate Jobs are `Complete`, and `kubectl get backups.postgresql.cnpg.io -n forgejo -l strange.company/upgrade-gate` returns no resources. A k3d variant (`make e2e-upgrade-gate`) also asserts that deleting the ConfigMap of an installed release and bumping the version makes the gate take a backup (no false first-install).

### GATE-C10 (contributed)

- **48-AC-7** (GC-07) Plan 23's promotion check, run on a fixture promotion PR whose candidate digest set has a **failing** off-cluster C10 attestation (fixture: `fake-red-valid` made to report `TEST_NEEDS_WORK`), fails the required CI context, and the same PR with a passing attestation succeeds. The off-cluster lane's C10 job for the current release candidate is green (plan 52 §8.5 evidence).

## 24. Explicit non-goals

- system-upgrade-controller (ADR-0154), unattended non-security OS upgrades, automatic merging of upgrade PRs (MVP).
- Downgrades of Kubernetes minors, Longhorn, PostgreSQL majors, or schema-migrated applications.
- Blue/green clusters.

## 25. Open questions

- **Q-48-01** (mostly resolved) `dag.yaml` lists plan 48 under F10, P10, P11 and C10; P04 (48-AC-8) is still missing. *Owner:* master plan.
- **Q-48-02** (blocking: no) New Flux layers `<app>-upgrade-gate` and `longhorn-upgrade-gate`, each `dependsOn` its database/storage layer and depended on by the app layer. *Owner:* 18.
- **Q-48-03** (blocking: no) CNPG's declarative in-place major upgrade availability depends on the operator version. *Proposed:* plan 12 verifies on the pinned version; the import method is the fallback. *Owner:* 12.
- **Q-48-04** (resolved, GC-07) The `upgrade-freeze` status context is deleted; the freeze is an operator-checked rule plus the `./bootstrap upgrade k3s` preflight. The C10 regression is a release gate on the off-cluster lane. *Owner:* —.
- **Q-48-05** (blocking: no) CLI subcommands `./bootstrap upgrade k3s --to <v>` and `./bootstrap configure --tags os-upgrade`. *Owner:* 50.
