# 35 — Secrets

**Status:** Stage-0 plan (draft)
**DAG nodes:** P10 (co-owner with 44, 12, 13: recovery-bundle creation, recovery checks and the decryptability restore test). Contributes offline checks that F11 (plan 18) and F02 (plan 52) run.
**Related plans:** [06](06-opentofu-state.md), [07](07-k3s.md), [14](14-object-storage.md), [15](15-authentik.md), [16](16-forgejo.md), [18](18-flux.md), [19](19-seed-handoff.md), [23](23-supply-chain.md), [26](26-agent-runner.md), [34](34-security.md), [37](37-admission-policy.md), [42](42-alerting-notifications.md), [44](44-backup.md), [45](45-recovery.md), [50](50-bootstrap-orchestrator.md), [53](53-model-gateway.md)
**Contracts:** owns `contracts/secret-classes.yaml`, `contracts/secret-classes.schema.yaml` (including `$defs/SecretGenerationLedger`), `contracts/recovery-bundle.schema.yaml` (including `$defs/LatestPointer`, `$defs/PublicInventory`, `$defs/Attestation`); consumes `bootstrap-config.schema.yaml` (50), `alert-catalog.yaml` (42), `restore-test-result.schema.yaml` (44)
**ADRs:** relies on ADR-0012, ADR-0005, ADR-0013, ADR-0016, ADR-0553, ADR-0554; introduces **ADR-0500** (two-tier SOPS layout, bundle content rule, non-decrypting recovery checks)
**Procedures owned:** `rotate-secrets.md`, `rotate-storage-credentials.md`, `rotate-age-key.md`, `rotate-break-glass.md`, `credential-compromise.md`, `sops-decryption-failure.md`

> Every section below is mandatory (SPEC §121).

## 1. Purpose

This plan is the system of record for **every secret class** in Strange Company [SPEC §99] and for the mechanisms that keep secrets encrypted in Git, recoverable after total loss, rotatable without loss of decryptability, short-lived where possible, and out of logs, telemetry, task records and worker pods [SPEC §94, §98, §111]. It defines:

1. the complete secret register (§10, generated from `contracts/secret-classes.yaml`);
2. the SOPS layout and `.sops.yaml` rules with two age recipients (ADR-0012);
3. the age key lifecycle, including rotation of either key without losing decryptability;
4. the recovery bundle: contents, passphrase/scrypt format, storage, update triggers and verification;
5. the rotation framework, ephemeral-credential TTLs and revocation;
6. secret scanning (gitleaks in CI, Runner output scanning, log scrubbing) and the rule that no empty-string Secret may exist (lesson L-4).

The *mechanics* of individual credentials (how Forgejo mints a token, how Authentik stores a password) belong to the owning plan named in the register. This plan owns **where each secret lives, how it moves, when it rotates and what happens when it leaks**.

## 2. Requirements derived from SPEC.md

1. Git-resident Kubernetes secrets are encrypted with SOPS + age [SPEC §98].
2. The age identity is black-start material and lives outside the cluster as well [SPEC §98, §111].
3. Every secret class has a creator, consumer, storage, rotation and recovery [SPEC §99]. The classes named by SPEC §99 map as follows: k3s token → `SC-K3S-TOKEN`; infrastructure credentials → `SC-INFRA-PROVIDER`; OpenTofu state encryption → `SC-TOFU-ENCRYPTION`; object-storage credentials → `SC-OBJECT-STORAGE`; Authentik bootstrap admin → `SC-AUTHENTIK-BOOTSTRAP`; Forgejo admin → `SC-FORGEJO-ADMIN`; GitHub mirror token → `SC-GITHUB-MIRROR`; Runner authentication → `SC-RUNNER-SIGNING`, `SC-WORKER-TASK-TOKEN`, `SC-RUNNER-HERMES`, `SC-RUNNER-ADMIN`; model API keys → `SC-MODEL-API-KEYS`; SOPS/age → `SC-AGE-CLUSTER`, `SC-AGE-RECOVERY`, `SC-RECOVERY-PASSPHRASE`; signing keys → `SC-RELEASE-SIGNING`, `SC-RUNNER-SIGNING`; break-glass credentials → `SC-BREAK-GLASS-*`.
4. Off-cluster recovery material includes the age identity, provider credentials, OpenTofu backend access and state-encryption material, DNS/tunnel credentials, object-storage credentials and break-glass credentials [SPEC §111].
5. Break-glass credentials live outside the cluster, are documented, are periodically tested, and produce audit evidence [SPEC §22] (mechanics: plan 15; storage and rotation: here).
6. Workers never receive cluster-admin, Flux, CNPG admin, Longhorn admin, release-signing, registry-push, deployment or unrelated production credentials [SPEC §38, §86, §37].
7. Telemetry never captures environment variables or secret-bearing output [SPEC §94].
8. Procedures exist for rotating model, Forge, storage, age and break-glass credentials [SPEC §113].
9. Destroying compute never destroys recovery secrets or object-storage recovery data [SPEC §114].
10. Bootstrap requires no hand-created Secrets [SPEC §130; L-4].

Derived requirements:

- **35-R-1 Register completeness.** Every credential, key, password, token or passphrase used anywhere is a class (or an instance of a class) in `contracts/secret-classes.yaml`. `make secrets-lint` fails on any `SC-*` identifier in `plans/`, `adr/`, `contracts/` or `procedures/` that is neither a registered id nor a registered alias (the alias list is in the register header).
- **35-R-2 Labelled Secrets.** Every Kubernetes `Secret` rendered from Git carries `strange.company/secret-class: <id>` and, for instance classes, `strange.company/secret-instance: <name>`. Exempt types: `kubernetes.io/service-account-token`, `helm.sh/release.v1`, cert-manager-issued `kubernetes.io/tls` (annotation `cert-manager.io/certificate-name`), CNPG-managed certificates (label `cnpg.io/cluster`), `bootstrap.kubernetes.io/token`, and the two out-of-band Secrets of plan 19 (`flux-system/sops-age`, `flux-system/seed-bucket-credentials`, label `strange.company/out-of-band: "true"`).
- **35-R-3 Two tiers of SOPS.** In-cluster secrets are SOPS files under `gitops/clusters/<cluster>/` encrypted to **both** the cluster and the recovery recipient. Secrets that must never be decryptable by the cluster (provider, toolbox DNS API `SC-DNS-API`, object-storage admin/state/recovery/seed, OpenTofu passphrase, k3s token, break-glass, Runner admin) are SOPS files under `secrets/offcluster/<cluster>/` encrypted to the **recovery recipient only** (ADR-0500). **Tier 1b** (`secrets/ci/<cluster>/`, release credentials) is encrypted to the recovery recipient and **`SC-AGE-CI`**, an identity held only in the bundle (review SEC-4). A compromise of the cluster age key therefore yields neither authority outside the cluster nor release credentials. The in-cluster DNS-01 solver uses the narrower `SC-DNS-ACME` (review AUTH-13).
- **35-R-4 Bundle content rule.** The recovery bundle contains exactly the classes and instances whose register entry says `inRecoveryBundle: true`: material needed before Git can be decrypted (the cluster age key), material that is not in cluster Git (off-cluster credentials, break-glass), and material that pre-Flux phases of `./bootstrap` and `./bootstrap recover` need (k3s token, Forgejo admin for forge-init, seed credentials). SOPS-in-Git-only secrets are **not** duplicated into the bundle; they are recoverable through the bundled cluster key or the offline recovery key.
- **35-R-5 No empty secret.** A secret value is never the empty string or whitespace. Enforced at generation (`sc-bootstrap` refuses), in CI (`make secrets-lint` reads the ciphertext length in SOPS `ENC[...]` values; an empty `data:` field fails), at admission (VAP `sc-secret-nonempty`, plan 37), and at consumption (components fail closed naming the empty file, e.g. plan 26 §18).
- **35-R-6 Files, not environment.** Secret values are mounted as files. Environment variables are allowed only for upstream software that cannot read files, listed in `security/secret-env-exceptions.yaml` (initial list: Authentik `envFrom` of `authentik-sc-env` (plan 15), Woodpecker server/agent secrets (plan 20), cloudflared `TUNNEL_TOKEN` (plan 09)). `make secrets-lint` fails on any rendered `secretKeyRef`/`envFrom.secretRef` not in that list.
- **35-R-7 Overlap rotation.** No rotation removes the old value until every consumer has been verified on the new value, except emergency rotations under `credential-compromise.md`, which revoke first.
- **35-R-8 Generation ledger.** Every rotation performed through the toolbox appends an entry to the generation ledger (`s3://<prefix>-recovery/<cluster>/generations.json`, `$defs/SecretGenerationLedger`). The ledger contains no values.
- **35-R-9 One pattern source.** Secret detection patterns live in `security/secret-patterns.yaml`. The gitleaks configuration, the Runner result/log scrubber (plan 26/27), the `sc-bootstrap` redactor (plan 50) and the OTel redaction allowlist checks (plan 40) are generated from or tested against it.
- **35-R-10 Destroy safety.** No automated path deletes `<prefix>-recovery` objects, `secrets/offcluster/**`, the generation ledger or attestations (SPEC §114; plan 49).

## 3. Inputs

| Input | Source | Notes |
|---|---|---|
| Operator-supplied credentials | `bootstrap.env` (plan 50 §9.2) | `SC-INFRA-PROVIDER`, `SC-DNS-API` (`CLOUDFLARE_API_TOKEN`), `SC-OBJECT-STORAGE/admin`, `SC-GITHUB-MIRROR`, `SC-GITHUB-PROBE`, `SC-NOTIFY-TRANSPORT`, `SC-MODEL-API-KEYS` |
| Recovery passphrase | interactive prompt (twice at creation) or `SC_RECOVERY_PASSPHRASE_FILE` | never stored (ADR-0012) |
| Offline recovery identity | operator, pasted into the toolbox prompt during age-recovery rotation, attestation and black start | tmpfs only |
| Register | `contracts/secret-classes.yaml` | drives generation, labelling, bundle inventory and lint |
| SOPS rules | `.sops.yaml` (repo root) | §14.2 |
| Pattern set | `security/secret-patterns.yaml` | §14.8 |
| Env exceptions | `security/secret-env-exceptions.yaml` | 35-R-6 |

## 4. Outputs

- `contracts/secret-classes.yaml` (register) and its schema; `contracts/recovery-bundle.schema.yaml`.
- `.sops.yaml` creation rules; SOPS files under `gitops/clusters/<cluster>/**` and `secrets/offcluster/<cluster>/**`.
- Recovery bundles `sc-recovery-<cluster>-v<N>.tar.age`, their public inventories and the `LATEST.json` pointer, locally under `/blackstart/<cluster>/bundles/` and in `s3://<prefix>-recovery/<cluster>/bundles/`.
- Generation ledger `s3://<prefix>-recovery/<cluster>/generations.json`; attestation records `s3://<prefix>-recovery/<cluster>/attestations/<ts>.json`.
- Four check CronJobs in `dr-tests` (§14.6) and their alerts (catalogue entries owned by plan 42, signals owned here).
- `security/secret-patterns.yaml`, generated `security/gitleaks.toml`, `security/secret-env-exceptions.yaml`.
- Procedures listed in the header.

## 5. Dependencies

**Hard (DAG):** P10 ← P06 (Forgejo is the Git source for SOPS delivery; mirror), P09 (alerts route). Offline checks run from F02/F11 onward (they need only the contract tooling and rendered manifests).

**Interfaces assumed from other plans:**

| Plan | Assumed interface |
|---|---|
| 50 | Implements the commands of §7.1 inside `sc-bootstrap`; seals the bundle with the Go age library (`filippo.io/age` scrypt recipient) at `plan.install-identity`; enforces the passphrase policy (≥ 20 characters or ≥ 5 words); registers every secret with its redactor; writes `/blackstart/<cluster>/`. `./bootstrap rotate` targets `recovery-bundle`, `tofu-passphrase`, `age-cluster`, `age-recovery`, `refresh-bundle` exist (plan 50 §7.1); this plan additionally requires `./bootstrap rotate secret <CLASS>[/<instance>]` (generic rotation entry point) and `./bootstrap exec -- sc-bootstrap recovery-bundle verify [--full]`. |
| 14 | Creates bucket `<prefix>-recovery` with versioning enabled and, where the provider supports it, Object Lock in governance mode with 365-day default retention; creates the scoped credentials listed under `SC-OBJECT-STORAGE` instances. |
| 19 | Creates `flux-system/sops-age` from the bundle at seed (out-of-band) and deletes `seed-bucket-credentials` at T6. |
| 18 | kustomize-controller decrypts with `decryption: {provider: sops, secretRef: {name: sops-age}}` on every layer; `make secrets-lint` is part of the F11 check list. |
| 37 | VAP `sc-secret-nonempty` (35-R-5) and `sc-ci-build-release-steps` (release credentials, plan 23). |
| 42 | Catalogue entries `SCRecoveryBundleOutdated`, `SCRecoveryBundleVerifyFailed`, `SCRecoveryKeyAttestationOverdue` (exist) plus `SCSecretRotationOverdue`, `SCSecretRotationDue`, `SCSopsDecryptionFailing`, `SCSecretScanHit` (new, §21). |
| 44 | The `dr-tests` namespace, the `restore-tester` ServiceAccount pattern and the check-CronJob framework (ADR-0553); `SC-AGE-BACKUP` usage. |
| 02 | The toolbox contains `sops`, `age`, `gitleaks`, `cosign` at pinned versions (gitleaks and cosign are additions requested from plan 02). |
| 26 | Uses the pattern set for result/log scrubbing; publishes JWKS rotation per §14.5.4. |
| 45 | Consumes the bundle, the offline recovery identity and the off-cluster SOPS files for black start; fences an old installation by rotating `SC-OBJECT-STORAGE` write instances, `SC-GITHUB-MIRROR` and `SC-DNS-TUNNEL` (ADR-0554). |

## 6. Owned state

| State | System of record | Writers | Protection |
|---|---|---|---|
| Secret register | `contracts/secret-classes.yaml` (Git) | PR | protected path `contracts/**` (ADR-0008) |
| SOPS creation rules | `.sops.yaml` (Git) | `sc-bootstrap` at install (per-cluster rule), PR afterwards | protected path |
| In-cluster secret ciphertexts | `gitops/clusters/<cluster>/**/*.sops.yaml` | `sc-bootstrap` (pre-transfer commit), PR afterwards | branch protection on `main` |
| Off-cluster secret ciphertexts | `secrets/offcluster/<cluster>/*.sops.yaml` | `sc-bootstrap` | recovery recipient only |
| Recovery bundles, inventories, pointer | `s3://<prefix>-recovery/<cluster>/bundles/` (versioned, never auto-deleted) + `/blackstart/<cluster>/bundles/` | `sc-bootstrap` | passphrase (scrypt) |
| Generation ledger | `s3://<prefix>-recovery/<cluster>/generations.json` (versioned) | `sc-bootstrap` | no values |
| Attestations | `s3://<prefix>-recovery/<cluster>/attestations/` | `sc-bootstrap recovery-bundle verify --full` | no values |
| Pattern set and env exceptions | `security/**` | PR | protected path (`security/**` is added to the plan-22 protected-path list; interface) |

## 7. External interfaces

### 7.1 Commands (CLI surface owned by plan 50; semantics owned here)

| Command | Purpose | Needs |
|---|---|---|
| `./bootstrap rotate secret <CLASS>[/<instance>] [--emergency]` | Generic rotation (§14.5): generate or prompt, write SOPS, open a PR or (pre-transfer) commit, update ledger and bundle when `bundleUpdateRequired` | passphrase |
| `./bootstrap rotate age-cluster` / `age-recovery` | §14.3 | passphrase; recovery identity for `age-recovery` |
| `./bootstrap rotate recovery-bundle` | New passphrase; re-seal current contents as version N+1 | old and new passphrase |
| `./bootstrap rotate refresh-bundle` | Re-seal after operator-supplied values changed in `bootstrap.env` | passphrase |
| `./bootstrap exec -- sc-bootstrap recovery-bundle status` | Print LATEST pointer, inventory, ledger diff (no passphrase) | `SC-OBJECT-STORAGE/recovery` |
| `./bootstrap exec -- sc-bootstrap recovery-bundle verify [--full]` | Integrity check; `--full` = clean-room decrypt test + attestation (§14.6.4) | passphrase, recovery identity |
| `./bootstrap exec -- sc-bootstrap secrets lint` (= `make secrets-lint`) | Offline checks (§14.8.3) | none |
| `./bootstrap exec -- sc-bootstrap secrets scan [--since <rev>]` (= `make secrets-scan`) | gitleaks with the generated config | none |
| `./bootstrap exec -- sc-bootstrap secrets status` | Per class: generation, rotated_at, max age, due date | `SC-OBJECT-STORAGE/recovery` |

Exit codes follow plan 50 §7.2; additionally `9` = secret lint/scan finding.

### 7.2 Contracts

- `contracts/secret-classes.yaml` (kind `SecretClassRegister`).
- `contracts/recovery-bundle.schema.yaml`: bundle `manifest.json`, `LatestPointer`, `PublicInventory`, `Attestation`.
- `contracts/secret-classes.schema.yaml#/$defs/SecretGenerationLedger`: the ledger.

### 7.3 Kubernetes conventions

- Labels: `strange.company/secret-class`, `strange.company/secret-instance` (35-R-2). Annotation `strange.company/secret-generation: "<n>"` on Secrets rendered from SOPS.
- Consumers that do not hot-reload carry the pod-template annotation `strange.company/secret-generations: "<CLASS>=<n>[,…]"`; a rotation PR bumps it so the workload rolls (the rotation tool edits the consumer's values file listed in the class's procedure section).

## 8. Internal interfaces

- **Generator** (`sc-bootstrap secrets generate`): for each class with `creator.actor` starting `sc-bootstrap`, produce a value per the class `format` (random-token: 32 bytes hex or base64url as the owning plan specifies; password: 40 characters from `[A-Za-z0-9]`; passphrase: 32 bytes base64url; age-identity: X25519; ssh-keypair: ed25519; jwt-signing-key: Ed25519 PKCS#8 PEM plus a `kid` of `sc-<YYYYMMDD>-<4 hex>`; hmac-key: 32 bytes; cosign-keypair: via `cosign generate-key-pair` with a 32-byte random password). Values exist only in memory and `/run/sc` tmpfs.
- **Writer**: renders Kubernetes `Secret` manifests (labels per §7.3), encrypts them with `sops --encrypt` under the matching `.sops.yaml` rule, writes off-cluster files, and verifies each file by decrypting it with the key it was encrypted for before committing.
- **Sealer**: builds the tar (manifest first, then `entries/<CLASS>[/<instance>]/<file>`, mode 0600, mtime 0, sorted for reproducibility), encrypts with an age scrypt recipient at work factor 20, writes atomically, uploads with `If-None-Match: *`, reads back and compares sha256, writes the inventory and then the pointer (pointer last).
- **Redactor**: every value handled is registered before first use; output replaces it with `«redacted:<CLASS>»` (plan 50 §10.2).

## 9. Configuration

| Knob | Source of truth | Default | Who may change |
|---|---|---|---|
| Per-class `maxAge`, triggers, procedures | `contracts/secret-classes.yaml` | per register | PR |
| scrypt work factor | `infra/bootstrap/settings.yaml` `recoveryBundle.scryptWorkFactor` | 20 (≈1 GiB memory, ~1–3 s) | PR; schema enforces 18–22 |
| Local bundle versions kept in `/blackstart/<cluster>/bundles/` | same file `recoveryBundle.localKeep` | 3 | PR |
| Attestation maximum age | `alert-catalog.yaml` `SCRecoveryKeyAttestationOverdue` + check config | 90 d | PR |
| Rotation-due warning lead time | check config `secretRotation.warnBefore` | 14 d | PR |
| Check schedules | CronJob manifests (layer `backups`, plan 44) | §14.6 | PR |
| gitleaks version | `toolbox/tools.lock.yaml` (plan 02) | latest release at implementation time | Renovate PR |
| SOPS version | `toolbox/tools.lock.yaml` | latest release whose file format the pinned kustomize-controller decrypts (verify, 35-AC-11) | Renovate PR |

## 10. Secrets

### 10.1 Classes owned by this plan

`SC-AGE-CLUSTER`, `SC-AGE-RECOVERY`, `SC-RECOVERY-PASSPHRASE`. All others are owned by the plan in the `Owner` column; this plan registers them and owns their storage, rotation framework and compromise response.

### 10.2 Complete register

Generated from `contracts/secret-classes.yaml` (`make secrets-register-md` regenerates this section; CI fails on drift). `{cluster}` is shown as `<cluster>`. 69 classes.

| ID | Owner | Sens. | Lifetime | Creator (when) | Consumers | Storage (**authoritative**) | Bundle |
|---|---|---|---|---|---|---|---|
| `SC-K3S-TOKEN` | 07 | critical | long-lived | sc-bootstrap plan.install-identity (32 random bytes, hex) (before provision of the first install) | k3s servers and agents (join; Ansible role k3s via /run/sc/ansible/vars.json); sc-bootstrap expand / recover (etcd snapshot restore) | node-file `/var/lib/rancher/k3s/server/token`<br>offcluster-sops `secrets/offcluster/<cluster>/k3s-token.sops.yaml`<br>**recovery-bundle** | yes |
| `SC-K3S-SERVER-CA` | 07 | critical | long-lived | k3s server (first server start) | kube-apiserver; etcd; kube-controller-manager (SA token signing) | node-file `/var/lib/rancher/k3s/server/tls/`<br>node-file `/var/lib/rancher/k3s/server/db/etcd` | no |
| `SC-K3S-ENCRYPTION-CONFIG` | 07 | critical | long-lived | k3s server (secrets-encryption true) (first server start) | kube-apiserver | node-file `/var/lib/rancher/k3s/server/cred/encryption-config.json`<br>node-file `/var/lib/rancher/k3s/server/db/etcd` | no |
| `SC-K3S-ADMIN-KUBECONFIG` | 50 | critical | long-lived | sc-bootstrap kubeconfig refresh (CSR in toolbox tmpfs, signed on a server node over SSH) (configure phase and whenever < 7 days remain) | sc-bootstrap and operator procedures in the toolbox (./bootstrap shell / exec) | **local-bootstrap-dir `/blackstart/<cluster>/kubeconfig`** | no |
| `SC-SSH-OPERATOR` | 50 | critical | long-lived | operator (before bootstrap) | sc-bootstrap / Ansible / OpenTofu (public key injection) | **operator-offline**<br>bootstrap-env<br>node-file `~<admin user>/.ssh/authorized_keys` | no |
| `SC-LAB-SSH` | 50 | low | ephemeral (ttl PT6H) | CI lab job (ssh-keygen in the toolbox) (start of each lab run) | sc-bootstrap in the lab job; lab VMs (public key only) | process-memory | no |
| `SC-INFRA-PROVIDER` | 04 | critical | long-lived | operator (provider console) (before bootstrap) | OpenTofu in toolbox (subprocess environment only) | **bootstrap-env**<br>offcluster-sops `secrets/offcluster/<cluster>/infra-provider.sops.yaml`<br>recovery-bundle | yes |
| `SC-CI-OVH-READONLY` | 04 | medium | long-lived | operator (OVH console, GET-only access rules) (before F09) | provider-contract step of the protected cron workflow provider-contract (build lane, image filter provider-contract) | external-service<br>**sops-git `secrets/ci/<cluster>/ci-ovh-readonly.sops.yaml`**<br>woodpecker-secret | no |
| `SC-DNS-TUNNEL` | 09 | high | long-lived | sc-bootstrap via the Cloudflare API with SC-DNS-API (plan.install-identity) | cloudflared (ingress) | **sops-git `gitops/clusters/<cluster>/secrets/ingress/cloudflared-tunnel.sops.yaml`**<br>k8s-secret `ingress/cloudflared-tunnel`<br>recovery-bundle | yes |
| `SC-DNS-API` | 10 | high | long-lived | operator (DNS provider dashboard) (before bootstrap) | sc-bootstrap (tunnel and DNS records, preflight PF-15/PF-20) | **bootstrap-env**<br>offcluster-sops `secrets/offcluster/<cluster>/dns-api.sops.yaml`<br>recovery-bundle | yes |
| `SC-DNS-ACME` | 10 | medium | long-lived | operator (DNS provider dashboard; bootstrap.env second token, plan 50) (before bootstrap) | cert-manager ACME DNS-01 solver (cert-manager) | bootstrap-env<br>**sops-git `gitops/clusters/<cluster>/secrets/cert-manager/dns-api-token.sops.yaml`**<br>k8s-secret `cert-manager/dns-api-token` | no |
| `SC-DNS-REGISTRAR` | 09 | critical | long-lived | operator (registrar) (before bootstrap) | operator during DNS-provider loss (edge-ingress-failure.md, Cloudflare account or DNS loss branch) | **operator-offline** | no |
| `SC-TOFU-ENCRYPTION` | 06 | critical | long-lived | sc-bootstrap plan.install-identity (32 random bytes, base64url) (before remote state init) | OpenTofu in toolbox (TF_ENCRYPTION env in tmpfs) | offcluster-sops `secrets/offcluster/<cluster>/tofu-encryption.sops.yaml`<br>**recovery-bundle** | yes |
| `SC-OBJECT-STORAGE` | 14 | critical | long-lived | sc-bootstrap plan.storage-apply using SC-OBJECT-STORAGE/admin (P01) | see instances<br>Instances: `admin` (bundled), `tofu-state` (bundled), `tofu-state-ro`, `recovery` (bundled), `recovery-reader`, `seed-write` (bundled), `seed-read` (bundled), `pg-backups-forgejo-pg`, `pg-backups-authentik-pg`, `pg-backups-woodpecker-pg`, `pg-backups-sc-pg`, `longhorn-backups`, `etcd-snapshots` (bundled), `forgejo-objects`, `repo-backups`, `evidence`, `telemetry`, `capsule-writer`, `telemetry-debug`, `drill-reader` (bundled), `dr-tests-results`, `objstore-audit`, `dr-tests-reader` | sops-git `gitops/clusters/<cluster>/secrets/<ns>/s3-<instance>.sops.yaml`<br>offcluster-sops `secrets/offcluster/<cluster>/s3-<instance>.sops.yaml`<br>recovery-bundle | yes |
| `SC-AGE-CLUSTER` | 35 | critical | long-lived | sc-bootstrap plan.install-identity (age library in toolbox) (before provision of the first install) | Flux kustomize-controller (flux-system); sc-bootstrap (SOPS_AGE_KEY in tmpfs, opened from the bundle) | k8s-secret `flux-system/sops-age`<br>**recovery-bundle** | yes |
| `SC-AGE-RECOVERY` | 35 | critical | long-lived | sc-bootstrap plan.install-identity (printed and written once to /blackstart/<cluster>/recovery-identity.txt) (before provision of the first install) | operator during black start, key rotation and the quarterly attestation (toolbox, tmpfs only) | **operator-offline**<br>local-bootstrap-dir `/blackstart/<cluster>/recovery-identity.txt` | no |
| `SC-AGE-CI` | 35 | critical | long-lived | sc-bootstrap supply-chain init (generated if absent; sealed into the bundle) (P11 (after control transfer, CD-19)) | sc-bootstrap ci-secrets apply / supply-chain init / signing-key rotation (tmpfs only) | **recovery-bundle** | yes |
| `SC-RECOVERY-PASSPHRASE` | 35 | critical | long-lived | operator (prompted twice; ≥ 20 characters or ≥ 5 words, plan 50 §14.3) (first bundle creation) | sc-bootstrap recovery-bundle (interactive prompt, or SC_RECOVERY_PASSPHRASE_FILE for automation with warning PF-24) | **operator-offline** | no |
| `SC-AGE-BACKUP` | 44 | high | long-lived | sc-bootstrap plan.install-identity (before provision of the first install) | restore-tester (decrypt) (dr-tests); repo-backup CronJob (public recipient only) (forgejo) | **sops-git `gitops/clusters/<cluster>/secrets/dr-tests/backup-age.sops.yaml`**<br>k8s-secret `dr-tests/backup-age`<br>recovery-bundle | yes |
| `SC-INTERNAL-CA` | 10 | high | long-lived | sc-bootstrap secrets gen internal-ca (toolbox; idempotent, keeps an existing root) (plan.install-identity (before seed)) | cert-manager CA ClusterIssuer sc-internal-ca (cert-manager); every internal TLS client (public root via ConfigMap sc-internal-ca) | **sops-git `gitops/clusters/<cluster>/secrets/cert-manager/sc-internal-ca-root.sops.yaml`**<br>k8s-secret `cert-manager/sc-internal-ca-root` | no |
| `SC-ACME-ACCOUNT` | 10 | low | long-lived | cert-manager (first ClusterIssuer reconcile) | cert-manager (cert-manager) | k8s-secret `cert-manager/acme-account-letsencrypt-prod` | no |
| `SC-AUTHENTIK-BOOTSTRAP` | 15 | critical | ephemeral (ttl P1D) | sc-bootstrap sso-init (random 40 chars / 64 hex) (P07) | Authentik first start (env) (authentik); sc-bootstrap sso-init | ephemeral-k8s-secret `authentik/authentik-bootstrap`<br>local-bootstrap-dir `/run/sc/secrets/authentik-bootstrap` | no |
| `SC-AUTHENTIK-SECRET-KEY` | 15 | high | long-lived | sc-bootstrap pre-seed (50 random bytes, base64) (plan.install-identity) | Authentik server/worker (authentik) | **sops-git `gitops/clusters/<cluster>/authentik/authentik-secret-key.sops.yaml`**<br>k8s-secret `authentik/authentik-secret-key` | no |
| `SC-AUTHENTIK-AUDIT-TOKEN` | 15 | medium | long-lived | sc-bootstrap pre-seed (64 hex); materialised by an Authentik blueprint (!Env) (plan.install-identity) | Authentik blueprint (token key) (authentik); platform-probes (monitoring); authentik-config-check Job (authentik) | **sops-git `gitops/clusters/<cluster>/authentik/authentik-sc-env.sops.yaml`**<br>sops-git `gitops/clusters/<cluster>/secrets/monitoring/platform-probes-authentik.sops.yaml` | no |
| `SC-OIDC-CLIENTS` | 15 | high | long-lived | sc-bootstrap pre-seed (random); declared in Authentik blueprints (plan.install-identity) | see instances<br>Instances: `forgejo`, `grafana`, `hermes-dashboard` | **sops-git `gitops/clusters/<cluster>/authentik/authentik-sc-env.sops.yaml`**<br>sops-git `gitops/clusters/<cluster>/secrets/<ns>/oidc-<instance>.sops.yaml` | no |
| `SC-FORGEJO-ADMIN` | 16 | critical | long-lived | sc-bootstrap pre-seed (plan.install-identity) (before provision) | Forgejo chart init (initialOnlyNoReset: creates the account from the seed value only, AUTH-9) (forgejo); sc-bootstrap forge-init and forge apply | sops-git `gitops/clusters/<cluster>/forgejo/forgejo-admin.sops.yaml`<br>k8s-secret `forgejo/forgejo-admin`<br>forgejo-db<br>**recovery-bundle** | yes |
| `SC-FORGEJO-SIGNING-KEY` | 16 | high | long-lived | sc-bootstrap pre-seed (ed25519) (plan.install-identity) | Forgejo merge signing (forgejo); sc-bootstrap recover (public key only) | **sops-git `gitops/clusters/<cluster>/forgejo/forgejo-signing-key.sops.yaml`**<br>k8s-secret `forgejo/forgejo-signing-key` | no |
| `SC-FORGEJO-APP-SECRETS` | 16 | high | long-lived | sc-bootstrap pre-seed (64-byte random each) (plan.install-identity) | Forgejo (forgejo) | **sops-git `gitops/clusters/<cluster>/forgejo/forgejo-app-secrets.sops.yaml`**<br>k8s-secret `forgejo/forgejo-app-secrets` | no |
| `SC-FORGEJO-SSH-HOST-KEY` | 16 | high | long-lived | sc-bootstrap pre-seed (ed25519) (plan.install-identity) | Forgejo SSH server (forgejo); Flux source-controller, Runner and workers (public key only) | **sops-git `gitops/clusters/<cluster>/forgejo/forgejo-ssh-host-key.sops.yaml`**<br>k8s-secret `forgejo/forgejo-ssh-host-keys` | no |
| `SC-FORGEJO-METRICS-TOKEN` | 16 | low | long-lived | sc-bootstrap pre-seed (32 bytes base64url) (plan.install-identity) | Forgejo (forgejo); Prometheus ServiceMonitor bearerTokenSecret (monitoring) | **sops-git `gitops/clusters/<cluster>/forgejo/forgejo-metrics-token.sops.yaml`**<br>sops-git `gitops/clusters/<cluster>/secrets/monitoring/forgejo-metrics-token.sops.yaml` | no |
| `SC-FORGEJO-BOT-TOKENS` | 16 | high | long-lived | sc-bootstrap forge-init S10 (Forgejo admin API) or supply-chain init (plan 23) (P05 / P11) | see instances<br>Instances: `sc-runner`, `sc-backup`, `platform-probes`, `sc-release`, `sc-gc`, `sc-e2e-human`, `sc-ci-config` | **sops-git `gitops/clusters/<cluster>/secrets/<ns>/forgejo-<instance>.sops.yaml`**<br>forgejo-db | no |
| `SC-RUNNER-WEBHOOK` | 26 | medium | long-lived | sc-bootstrap pre-seed (32 bytes) (plan.install-identity) | Forgejo (signs) (forgejo); agent-runner (verifies; sets the secret on managed hooks) (strange-company) | **sops-git `gitops/clusters/<cluster>/secrets/strange-company/agent-runner-webhook.sops.yaml`**<br>forgejo-db | no |
| `SC-FLUX-DEPLOY-KEY` | 18 | high | long-lived | sc-bootstrap pre-seed; registered read-only by forge-init S11 (plan.install-identity / P05) | Flux source-controller (flux-system) | **sops-git `gitops/clusters/<cluster>/flux-system/flux-source-auth.sops.yaml`**<br>k8s-secret `flux-system/flux-system` | no |
| `SC-WORKER-DEPLOY-KEY` | 26 | medium | ephemeral (ttl PT1H20M) | agent-runner (execution dispatch) | worker Job (sc-worker git operations) (agent-jobs) | ephemeral-k8s-secret `agent-jobs/<execution-id>`<br>forgejo-db<br>sc-pg | no |
| `SC-GITHUB-MIRROR` | 17 | high | long-lived | operator (GitHub settings; expiry ≤ 180 days) (before bootstrap) | Forgejo push mirror (forgejo); sc-bootstrap (preflight PF-16, mirror create/verify/recreate, recover ref reconciliation, plan 49 freshness) | **bootstrap-env**<br>recovery-bundle<br>forgejo-db<br>pvc-file `<repo>.git/config` | yes |
| `SC-GITHUB-PROBE` | 17 | low | long-lived | operator (GitHub settings) (before bootstrap) | platform-probes (github-mirror module) (monitoring); sc-bootstrap mirror check-github; sc-release gate (release workflow, build lane; polls the GitHub Actions e2e run, ADR-0303) (ci-build) | bootstrap-env<br>**sops-git `gitops/clusters/<cluster>/secrets/github/platform-probes-github.sops.yaml`**<br>woodpecker-secret<br>k8s-secret `monitoring/platform-probes-github` | no |
| `SC-GITHUB-MIRROR-READ` | 17 | low | long-lived | operator (GitHub settings; bootstrap.env GITHUB_MIRROR_READ_TOKEN, CD-19) (before bootstrap) | restore-test github-black-start (RT-6) (dr-tests) | bootstrap-env<br>**sops-git `gitops/clusters/<cluster>/secrets/dr-tests/github-mirror-read.sops.yaml`**<br>k8s-secret `dr-tests/github-mirror-read` | no |
| `SC-WOODPECKER-OAUTH` | 20 | high | long-lived | sc-bootstrap forge-init S13 (Forgejo admin API) (P05) | Woodpecker server (woodpecker) | **sops-git `gitops/clusters/<cluster>/secrets/forge/woodpecker-oauth.sops.yaml`**<br>k8s-secret `woodpecker/woodpecker-forgejo-oauth`<br>forgejo-db | no |
| `SC-WOODPECKER-USER-TOKENS` | 20 | high | long-lived | Woodpecker server (OAuth login) (ci-init W01/W02 and every human login) | Woodpecker server (woodpecker); pinned clone plugin (netrc) (ci-jobs, ci-build) | external-service<br>forgejo-db | no |
| `SC-WOODPECKER-AGENT` | 20 | high | long-lived | sc-bootstrap ci-init W07 (per-agent tokens); plan.install-identity (server-only system value) (P08) | Woodpecker server (woodpecker); see instances<br>Instances: `ci`, `build` | **sops-git `gitops/clusters/<cluster>/secrets/woodpecker/agent-token-<instance>.sops.yaml`**<br>sops-git `gitops/clusters/<cluster>/secrets/woodpecker/woodpecker-agent-secret.sops.yaml` | no |
| `SC-WOODPECKER-RUNNER` | 20 | high | long-lived | sc-bootstrap ci-init W05 (P08) | agent-runner (strange-company) | **sops-git `gitops/clusters/<cluster>/secrets/strange-company/woodpecker-runner-token.sops.yaml`**<br>k8s-secret `strange-company/agent-runner-woodpecker` | no |
| `SC-WOODPECKER-MAINT` | 20 | medium | long-lived | sc-bootstrap ci-init (P08) | sc-ci-maint CronJob (woodpecker); sc-ci-config (agent audit loop; deletes release secrets on mismatch) (woodpecker) | **sops-git `gitops/clusters/<cluster>/secrets/woodpecker/sc-ci-maint-token.sops.yaml`**<br>k8s-secret `woodpecker/sc-ci-maint-token` | no |
| `SC-CI-INTENTS` | 20 | high | long-lived | sc-bootstrap pre-seed (32 random bytes, base64url) (plan.install-identity) | sc-ci-config (woodpecker); agent-runner (verifies) (strange-company) | **sops-git `gitops/clusters/<cluster>/secrets/woodpecker/sc-ci-config-intents.sops.yaml`**<br>sops-git `gitops/clusters/<cluster>/secrets/strange-company/agent-runner-ci-intents.sops.yaml` | no |
| `SC-WOODPECKER-ADMIN` | 20 | high | ephemeral (ttl PT1H) | sc-bootstrap (mechanism owned by plan 20) (P08/P11 and CI configuration changes) | sc-bootstrap ci apply | process-memory | no |
| `SC-WOODPECKER-SIGNING` | 20 | high | long-lived | Woodpecker server (first start (P08)) | Woodpecker server (sign) (woodpecker); sc-ci-config (public key only) (woodpecker) | external-service | no |
| `SC-WOODPECKER-METRICS-TOKEN` | 20 | low | long-lived | sc-bootstrap pre-seed (32 bytes base64url) (plan.install-identity) | Woodpecker server (woodpecker); Prometheus ServiceMonitor bearerTokenSecret (monitoring) | **sops-git `gitops/clusters/<cluster>/secrets/woodpecker/woodpecker-metrics-token.sops.yaml`**<br>sops-git `gitops/clusters/<cluster>/secrets/monitoring/woodpecker-metrics-token.sops.yaml` | no |
| `SC-RUNNER-SIGNING` | 26 | critical | long-lived | sc-bootstrap secrets generate (toolbox) (before C01 (PR)) | agent-runner (sign) (strange-company); model-gateway and agent-runner (public JWKS only) (strange-company) | **sops-git `gitops/clusters/<cluster>/secrets/strange-company/agent-runner-signing.sops.yaml`**<br>k8s-secret `strange-company/agent-runner-signing` | no |
| `SC-RUNNER-HERMES` | 26 | high | long-lived | agent-runner token issuer (ADR-0401) (first Runner start and every rotation) | Hermes (managed scope) (hermes); agent-runner (constant-time hash compare) (strange-company) | **k8s-secret `hermes/hermes-managed-env`**<br>sc-pg | no |
| `SC-RUNNER-ADMIN` | 26 | high | long-lived | sc-bootstrap access join (operator instances); sc-bootstrap plan.install-identity (breakglass) (join of each operator; before provision (breakglass)) | operator via port-forward (plan 50 CLI wrappers); agent-runner (hash compare; key id → actor) (strange-company)<br>Instances: `operator-login`, `breakglass` (bundled) | sops-git `secrets/strange-company/runner-admin-credentials.sops.yaml`<br>k8s-secret `strange-company/agent-runner-admin`<br>recovery-bundle<br>operator-offline | yes |
| `SC-WORKER-TASK-TOKEN` | 26 | medium | ephemeral (ttl PT1H20M) | agent-runner (execution dispatch) | worker Job (agent-jobs); agent-runner worker API (strange-company)<br>Instances: `runner`, `egress` | ephemeral-k8s-secret `agent-jobs/<execution-id>`<br>sc-pg | no |
| `SC-GATEWAY-TOKEN` | 53 | high | ephemeral (ttl PT1H20M) | agent-runner (execution dispatch (workers); Runner rotation timer (Hermes instance)) | worker harness (agent-jobs); model-gateway (strange-company)<br>Instances: `worker`, `hermes` | ephemeral-k8s-secret `agent-jobs/<execution-id>`<br>sc-pg | no |
| `SC-MODEL-API-KEYS` | 53 | critical | long-lived | operator (provider consoles) via bootstrap.env (bootstrap) | model-gateway (strange-company); sc-bootstrap preflight PF-19 (one non-billable call)<br>Instances: `anthropic`, `openai`, `deepseek`, `zai`, `google`, `openai-compatible` | bootstrap-env<br>**sops-git `gitops/clusters/<cluster>/secrets/strange-company/model-api-keys.sops.yaml`**<br>k8s-secret `strange-company/model-api-keys` | no |
| `SC-MODEL-USAGE-READ` | 32 | critical | long-lived | operator (provider organisation console; least scope the provider offers) via bootstrap.env (bootstrap, or when a provider is enabled) | ledger-reconcile CronJob (strange-company)<br>Instances: `anthropic`, `openai` | bootstrap-env<br>**sops-git `gitops/clusters/<cluster>/secrets/strange-company/model-usage-read.sops.yaml`**<br>k8s-secret `strange-company/model-usage-read` | no |
| `SC-HERMES-API` | 24 | medium | long-lived | agent-runner token issuer (ADR-0401) (first Runner start and every rotation) | Hermes gateway (hermes) | **k8s-secret `hermes/hermes-managed-env`**<br>sc-pg | no |
| `SC-PG-APP` | 12 | high | long-lived | sc-bootstrap pre-seed (32-byte random) (plan.install-identity (sc-pg sub-roles before C01)) | see instances<br>Instances: `forgejo-pg`, `authentik-pg`, `woodpecker-pg`, `sc-pg-runner`, `sc-pg-gateway`, `forgejo-pg-ro`, `sc-pg-grafana-ro` | **sops-git `gitops/clusters/<cluster>/secrets/<ns>/<cluster>-app.sops.yaml`**<br>k8s-secret `forgejo/<cluster>-app` | no |
| `SC-PG-DR-MARKER` | 12 | low | long-lived | sc-bootstrap pre-seed (32-byte random) (plan.install-identity) | CNPG managed role sc_dr (forgejo, authentik, woodpecker, strange-company); restore-tester (dr-tests) | **sops-git `gitops/clusters/<cluster>/secrets/<ns>/<cluster>-dr-marker.sops.yaml`**<br>sops-git `gitops/clusters/<cluster>/secrets/dr-tests/<cluster>-dr-marker.sops.yaml` | no |
| `SC-GRAFANA-ADMIN` | 39 | medium | long-lived | sc-bootstrap pre-seed (plan.install-identity) | Grafana (monitoring)<br>Instances: `admin`, `secret-key` | **sops-git `gitops/clusters/<cluster>/secrets/monitoring/grafana-admin.sops.yaml`** | no |
| `SC-NOTIFY-TRANSPORT` | 42 | medium | long-lived | operator via bootstrap.env (bootstrap) | Alertmanager (monitoring); sc-bootstrap preflight PF-17/PF-18<br>Instances: `receiver`, `heartbeat` | bootstrap-env<br>**sops-git `gitops/clusters/<cluster>/secrets/monitoring/alertmanager-receivers.sops.yaml`** | no |
| `SC-RELEASE-SIGNING` | 23 | critical | long-lived | sc-bootstrap supply-chain init (cosign generate-key-pair in the toolbox) (P11) | sc-release plugin step (sign) (ci-build); sc-release verify (promotion CI), agent-runner harness check, bootstrap verify V-26, operators (public key only) | **sops-git `secrets/ci/<cluster>/release-signing.sops.yaml`**<br>woodpecker-secret<br>external-service<br>pod-spec-env `ci-build/sc-release step pod` | no |
| `SC-REGISTRY-PUSH` | 23 | high | long-lived | sc-bootstrap supply-chain init (Forgejo admin API) (P11) | sc-release plugin step (ci-build) | **sops-git `secrets/ci/<cluster>/registry-push.sops.yaml`**<br>woodpecker-secret<br>external-service<br>pod-spec-env `ci-build/sc-release step pod`<br>forgejo-db | no |
| `SC-REGISTRY-PULL` | 23 | low | long-lived | sc-bootstrap supply-chain init (submitted by PR) (P11) | kubelet (imagePullSecrets) in strange-company, hermes, agent-jobs, ci-jobs, ci-build, renovate, dr-tests, monitoring, woodpecker | **sops-git `gitops/clusters/<cluster>/secrets/<ns>/sc-registry-pull.sops.yaml`**<br>k8s-secret `agent-jobs/sc-registry-pull` | no |
| `SC-GHCR-PUSH` | 23 | high | long-lived | operator (GitHub settings) (P11) | sc-release plugin step (publish-toolbox) (ci-build) | **sops-git `secrets/ci/<cluster>/ghcr-push.sops.yaml`**<br>woodpecker-secret<br>external-service<br>pod-spec-env `ci-build/sc-release step pod` | no |
| `SC-RENOVATE` | 23 | medium | long-lived | sc-bootstrap forge-init S10 (forgejo instance); operator (github-com instance) (P05 / P11) | renovate CronJob (renovate)<br>Instances: `forgejo`, `github-com` | **sops-git `gitops/clusters/<cluster>/secrets/forge/sc-renovate-token.sops.yaml`**<br>k8s-secret `renovate/forgejo-sc-renovate` | no |
| `SC-BREAK-GLASS-CLUSTER` | 15 | critical | long-lived | sc-bootstrap plan.install-identity (before provision) | operator during break-glass; node sshd (public key, infra/ansible/files/breakglass.pub) | **recovery-bundle**<br>offcluster-sops `secrets/offcluster/<cluster>/break-glass-cluster.sops.yaml`<br>node-file `/home/sc-breakglass/.ssh/authorized_keys` | yes |
| `SC-SYNTHETIC-USER` | 15 | medium | long-lived | sc-bootstrap pre-seed (plan.install-identity) | toolbox checks (./bootstrap verify, sc-dashcheck, drills); Authentik (password and TOTP set by sso-init A5b) (authentik) | **recovery-bundle**<br>offcluster-sops `secrets/offcluster/<cluster>/synthetic-user.sops.yaml` | yes |
| `SC-BREAK-GLASS-AUTHENTIK` | 15 | critical | long-lived | sc-bootstrap plan.install-identity (staged); user created by sso-init A4 (P07) | operator during break-glass | **recovery-bundle**<br>offcluster-sops `secrets/offcluster/<cluster>/break-glass-authentik.sops.yaml` | yes |
| `SC-BREAK-GLASS-FORGEJO` | 15 | critical | long-lived | sc-bootstrap plan.install-identity (staged); account created by forge-init S03 (P05) | operator during break-glass | **recovery-bundle**<br>offcluster-sops `secrets/offcluster/<cluster>/break-glass-forgejo.sops.yaml`<br>forgejo-db | yes |
| `SC-BREAK-GLASS-GRAFANA` | 15 | medium | long-lived | sc-bootstrap plan.install-identity (before provision) | operator during break-glass | **recovery-bundle**<br>sops-git `gitops/clusters/<cluster>/secrets/monitoring/grafana-admin.sops.yaml` | yes |
| `SC-BREAK-GLASS-INFRA` | 15 | critical | long-lived | operator (provider console) (before bootstrap) | operator during break-glass (console, rescue mode) | **operator-offline** | no |

| ID | Rotation: mode, max age, triggers | Procedure | Blast radius | Downtime | Recovery from | Compromise: severity, procedure, first actions |
|---|---|---|---|---|---|---|
| `SC-K3S-TOKEN` | on-compromise; compromise-suspected, operator-change; **bundle update** | `procedures/rotate-secrets.md#sc-k3s-token` | New node joins and etcd-snapshot restores need the new token; snapshots taken before rotation need the OLD token, so the bundle keeps the previous generation until the last old snapshot expires (30 d). | none (k3s token rotate plus a rolling restart of servers, verify) | recovery-bundle, offline-recovery-identity | sev1, `procedures/credential-compromise.md#cluster-and-node`: Rotate with k3s token rotate; Check the node list for unknown nodes and delete them; Rebuild the recovery bundle |
| `SC-K3S-SERVER-CA` | on-compromise; compromise-suspected | `procedures/rotate-secrets.md#sc-k3s-server-ca` | Every kubeconfig, every node certificate and every SA token becomes invalid; all clients need new credentials. | Control-plane restart; minutes of API unavailability per server. | etcd-snapshot, regenerate — Restored with an etcd snapshot (needs SC-K3S-TOKEN). On a rebuild from Git, a new CA is generated and all derived credentials are re-issued. | sev1, `procedures/credential-compromise.md#cluster-and-node`: Treat as full cluster compromise; Rebuild from Git per total-cluster-loss rather than rotate in place |
| `SC-K3S-ENCRYPTION-CONFIG` | scheduled, P1Y; schedule, compromise-suspected | `procedures/rotate-secrets.md#sc-k3s-encryption-config` | Confidentiality of every Secret in etcd and in etcd snapshots. | none (k3s secrets-encrypt rotate-keys plus a rolling restart, plan 07 §19.4) | etcd-snapshot, regenerate — Restored with the snapshot's bootstrap data (needs SC-K3S-TOKEN); a rebuild from Git generates new keys. | sev1, `procedures/credential-compromise.md#cluster-and-node`: Rotate keys; Treat every in-cluster Secret value as exposed and rotate the classes whose storage includes k8s-secret |
| `SC-K3S-ADMIN-KUBECONFIG` | automatic, P30D; key-expiry, compromise-suspected, operator-change | `procedures/rotate-secrets.md#sc-k3s-admin-kubeconfig` | Full cluster administration for ≤ 30 days; revocable for all operators at once by bumping k3s_operator_generation (plan 07). | none | regenerate — Re-issued over SSH from any server node with SC-SSH-OPERATOR or SC-BREAK-GLASS-CLUSTER. | sev1, `procedures/credential-compromise.md#cluster-and-node`: Bump k3s_operator_generation and run ./bootstrap configure --tags k3s (revokes every operator certificate of that generation); Re-issue kubeconfigs; Review the audit log for sc-operator:<name> |
| `SC-SSH-OPERATOR` | scheduled, P1Y; schedule, compromise-suspected, operator-change | `procedures/rotate-secrets.md#sc-ssh-operator` | Root-equivalent node administration on every node. | none | operator, provider-console — SC-BREAK-GLASS-CLUSTER (node user sc-breakglass) or the provider console / rescue mode re-installs a key. | sev1, `procedures/credential-compromise.md#cluster-and-node`: Replace authorized_keys on all nodes via the break-glass key; Audit auth.log; Treat nodes as suspect if logins are unexplained |
| `SC-LAB-SSH` | per-use; after-use | `procedures/rotate-secrets.md#sc-lab-ssh` | Disposable lab VMs of one run. | none | not-recoverable-by-design | sev3, `procedures/credential-compromise.md#cluster-and-node`: Destroy the lab VMs |
| `SC-INFRA-PROVIDER` | scheduled, P1Y; schedule, compromise-suspected, operator-change, provider-forced; **bundle update** | `procedures/rotate-secrets.md#sc-infra-provider` | Paid resource creation/destruction for the whole provider account. | none | recovery-bundle, offline-recovery-identity, provider-console | sev1, `procedures/credential-compromise.md#infrastructure-and-dns`: Revoke the consumer key in the provider console; Inspect billing and resource inventory against tofu state; Issue new credentials and rebuild the bundle |
| `SC-CI-OVH-READONLY` | scheduled, P1Y; schedule, compromise-suspected | `procedures/rotate-secrets.md#sc-ci-ovh-readonly` | Read-only view of the OVH account (catalogue, account metadata). | none | sops-git, provider-console | sev3, `procedures/credential-compromise.md#infrastructure-and-dns`: Revoke in the OVH console; Verify its access rules are GET-only; Re-issue |
| `SC-DNS-TUNNEL` | scheduled, P1Y; schedule, compromise-suspected, recovery-drill; **bundle update** | `procedures/rotate-secrets.md#sc-dns-tunnel` | Public ingress for all hostnames; a stolen token lets an attacker run a connector for the tunnel and receive a share of public traffic. | < 1 min (cloudflared rollout) | sops-git, recovery-bundle, regenerate | sev1, `procedures/credential-compromise.md#infrastructure-and-dns`: Rotate the tunnel token via the Cloudflare API; Remove unknown connectors from the tunnel; Reseal and merge |
| `SC-DNS-API` | scheduled, P180D; schedule, compromise-suspected, operator-change; **bundle update** | `procedures/rotate-secrets.md#sc-dns-api` | Tunnel configuration and every DNS record of the zone (hijack of every hostname). | none | recovery-bundle, offline-recovery-identity, provider-console | sev1, `procedures/credential-compromise.md#infrastructure-and-dns`: Roll the token at the provider; Diff the zone against the expected record set; Check CT logs for unexpected certificates |
| `SC-DNS-ACME` | scheduled, P180D; schedule, compromise-suspected, operator-change | `procedures/rotate-secrets.md#sc-dns-acme` | TXT records in the challenge zone (with CNAME delegation) or DNS records of the zone (without); certificate issuance for the domain. | none | sops-git, provider-console | sev2, `procedures/credential-compromise.md#infrastructure-and-dns`: Roll the token; Check CT logs for unexpected certificates; Diff the zone |
| `SC-DNS-REGISTRAR` | scheduled, P1Y; schedule, after-use, compromise-suspected, operator-change | `procedures/rotate-break-glass.md#infrastructure` | The whole domain (NS delegation, hence every hostname, certificate issuance and email). | none | operator, provider-console | sev1, `procedures/credential-compromise.md#infrastructure-and-dns`: Reset password and MFA at the registrar; Enable registrar lock; Verify NS records and DNSSEC |
| `SC-TOFU-ENCRYPTION` | on-compromise; compromise-suspected; **bundle update** | `procedures/rotate-secrets.md#sc-tofu-encryption` | Confidentiality of OpenTofu state (provider resource ids, possibly sensitive outputs). | none (re-encrypt with the OpenTofu fallback key provider, plan 06 §20.3) | recovery-bundle, offline-recovery-identity | sev2, `procedures/credential-compromise.md#infrastructure-and-dns`: Re-encrypt state with a new passphrase using the fallback provider; Rotate SC-OBJECT-STORAGE/tofu-state |
| `SC-OBJECT-STORAGE` | scheduled, P180D; schedule, compromise-suspected, operator-change, provider-forced, recovery-drill; **bundle update** | `procedures/rotate-storage-credentials.md` | Per instance, only its bucket or prefix; admin = all buckets. | none (overlapping old/new keys during rollout) | sops-git, recovery-bundle, provider-console | sev1, `procedures/credential-compromise.md#object-storage`: Revoke the key at the provider; Verify bucket versioning and object integrity for the affected bucket; Rotate per rotate-storage-credentials |
| `SC-AGE-CLUSTER` | scheduled, P1Y; schedule, compromise-suspected, operator-change, recovery-drill; **bundle update** | `procedures/rotate-age-key.md#cluster-key` | Decryptability of every cluster SOPS file; Flux reconciliation of secret-bearing Kustomizations. | none (old and new identities both installed during the overlap) | recovery-bundle — If lost, rotate it using SC-AGE-RECOVERY (no data loss). | sev1, `procedures/credential-compromise.md#recovery-material`: Rotate the cluster key; Rotate EVERY secret value stored in gitops/clusters/<cluster> (Git history keeps old ciphertexts); Rebuild the bundle |
| `SC-AGE-RECOVERY` | on-compromise; compromise-suspected, operator-change; **bundle update** | `procedures/rotate-age-key.md#recovery-key` | Black-start capability without the bundle; every SOPS file. | none | operator — If lost but the bundle survives, generate a new recovery key and re-encrypt using the cluster key and the bundled off-cluster values (rotate-age-key §recovery-key, lost variant). | sev1, `procedures/credential-compromise.md#recovery-material`: Rotate the recovery key; Rotate every secret value in secrets/offcluster and in the cluster SOPS files (Git history keeps old ciphertexts); Rebuild the bundle |
| `SC-AGE-CI` | on-compromise; compromise-suspected, operator-change; **bundle update** | `procedures/rotate-age-key.md#ci-key` | Confidentiality of tier-1b release credentials. | none | recovery-bundle — If lost, tier 1b is re-encrypted with the recovery identity to a new SC-AGE-CI. | sev1, `procedures/credential-compromise.md#recovery-material`: Rotate SC-AGE-CI; Rotate every tier-1b value (signing key emergency revocation; registry push; GHCR; sc-release token); Rebuild the bundle |
| `SC-RECOVERY-PASSPHRASE` | on-compromise; compromise-suspected, operator-change; **bundle update** | `procedures/rotate-secrets.md#sc-recovery-passphrase` | Every stored bundle version encrypted with it. | none | not-recoverable-by-design — Lost passphrase = the bundle is unusable; recovery falls back to SC-AGE-RECOVERY plus bootstrap.env, then a new bundle under a new passphrase. | sev1, `procedures/credential-compromise.md#recovery-material`: Create a new bundle under a new passphrase; Treat every bundled class as compromised if a bundle copy may have leaked; Delete old bundle versions only after the new one verifies |
| `SC-AGE-BACKUP` | scheduled, P1Y; schedule, compromise-suspected; **bundle update** | `procedures/rotate-secrets.md#sc-age-backup` | Confidentiality of repository backups in <prefix>-repo-backups. | none (new recipient for new backups; old identity kept until the last backup encrypted to it expires, 365 d) | recovery-bundle, sops-git | sev2, `procedures/credential-compromise.md#object-storage`: Rotate the identity; Re-encrypt retained backups to the new recipient; Review repo-backups bucket access logs |
| `SC-INTERNAL-CA` | on-compromise; compromise-suspected, key-expiry | `procedures/rotate-secrets.md#sc-internal-ca` | Impersonation of any internal TLS service to in-cluster clients. | seconds per service restart (new root distributed with overlap, plan 10) | sops-git | sev2, `procedures/credential-compromise.md#cluster-and-node`: Generate a new root by PR (plan 10 rotation); Distribute the new public root before switching the issuer; Re-issue all internal certificates |
| `SC-ACME-ACCOUNT` | on-compromise; compromise-suspected | `procedures/rotate-secrets.md#sc-acme-account` | Certificate issuance under the account (DNS control is still required). | none | regenerate | sev3, `procedures/credential-compromise.md#infrastructure-and-dns`: Deactivate the account; Delete the account Secret so that cert-manager re-registers |
| `SC-AUTHENTIK-BOOTSTRAP` | per-use; after-use | `procedures/rotate-secrets.md#sc-authentik-bootstrap` | Full Authentik admin until destroyed (SCAuthentikBootstrapAdminActive alerts if it survives). | none | not-recoverable-by-design — Not needed after sso-init; mid-bootstrap loss is handled by restore-authentik.md §Bootstrap recovery. | sev1, `procedures/credential-compromise.md#identity-and-forge`: Delete the token and deactivate akadmin (re-run sso-init A9); Revoke all Authentik sessions; Review Authentik events |
| `SC-AUTHENTIK-SECRET-KEY` | on-compromise; compromise-suspected | `procedures/rotate-secrets.md#sc-authentik-secret-key` | All Authentik sessions (forced re-login). | < 2 min (rollout) | sops-git | sev1, `procedures/credential-compromise.md#identity-and-forge`: Rotate; Force re-login |
| `SC-AUTHENTIK-AUDIT-TOKEN` | scheduled, P1Y; schedule, compromise-suspected | `procedures/rotate-secrets.md#sc-authentik-audit-token` | Read access to Authentik users, events and blueprint status. | one probe interval | sops-git | sev3, `procedures/credential-compromise.md#identity-and-forge`: Rotate both SOPS copies in one PR |
| `SC-OIDC-CLIENTS` | scheduled, P1Y; schedule, compromise-suspected | `procedures/rotate-secrets.md#sc-oidc-clients` | SSO login to one application. | < 2 min login unavailability for that application | sops-git | sev2, `procedures/credential-compromise.md#identity-and-forge`: Rotate the client secret on both sides in one PR; Revoke issued tokens for the client |
| `SC-FORGEJO-ADMIN` | scheduled, P1Y; schedule, compromise-suspected, operator-change; **bundle update** | `procedures/rotate-forge-credentials.md` | Full Forgejo administration (branch protection, deploy keys, all repositories). | none | sops-git, recovery-bundle, forgejo-restore | sev1, `procedures/credential-compromise.md#identity-and-forge`: Rotate; Audit branch-protection and deploy-key changes; Verify main heads against the GitHub mirror and repo bundles |
| `SC-FORGEJO-APP-SECRETS` | on-compromise; compromise-suspected | `procedures/rotate-forge-credentials.md` | SECRET_KEY decrypts stored push-mirror credentials and 2FA secrets; JWT secrets sign sessions and LFS tokens. | < 5 min (Forgejo restart) | sops-git | sev1, `procedures/credential-compromise.md#identity-and-forge`: Rotate INTERNAL_TOKEN and JWT secrets immediately; Plan SECRET_KEY replacement with push-mirror re-entry and 2FA re-enrolment; Rotate SC-GITHUB-MIRROR |
| `SC-FORGEJO-SSH-HOST-KEY` | on-compromise; compromise-suspected | `procedures/rotate-forge-credentials.md` | Impersonation of Forgejo SSH to Flux, the Runner and workers. | none (dual-key overlap, plan 16 rotate-forge-credentials §E) | sops-git — Restoring the same key avoids known_hosts churn after black start. | sev2, `procedures/credential-compromise.md#identity-and-forge`: Rotate host key and known_hosts in one PR |
| `SC-FORGEJO-METRICS-TOKEN` | scheduled, P1Y; schedule, compromise-suspected | `procedures/rotate-secrets.md#sc-forgejo-metrics-token` | Read access to Forgejo metrics. | one scrape gap (≤ 2 min) | sops-git | sev3, `procedures/credential-compromise.md#observability-and-notification`: Rotate both copies in one PR |
| `SC-FORGEJO-BOT-TOKENS` | scheduled, P90D; schedule, compromise-suspected | `procedures/rotate-forge-credentials.md` | Per instance; sc-runner = all Runner-posted statuses, PRs and deploy keys. | seconds (consumer reloads its token Secret) | sops-git, regenerate | sev1, `procedures/credential-compromise.md#identity-and-forge`: Delete the token in Forgejo; Re-verify all strange-company/* statuses posted since exposure; Rotate |
| `SC-RUNNER-WEBHOOK` | scheduled, P1Y; schedule, compromise-suspected | `procedures/rotate-secrets.md#sc-runner-webhook` | Forged webhooks (the Runner treats webhooks as hints and re-reads state through the API every 5 min). | none (missed deliveries recovered by the 5-min resync) | sops-git, regenerate | sev3, `procedures/credential-compromise.md#control-plane-tokens`: Rotate |
| `SC-FLUX-DEPLOY-KEY` | scheduled, P1Y; schedule, compromise-suspected | `procedures/rotate-forge-credentials.md` | Read access to strange-company (including SOPS ciphertexts). | none (add new key, switch Secret, remove old key) | sops-git, regenerate | sev2, `procedures/credential-compromise.md#identity-and-forge`: Delete the deploy key in Forgejo; Register a new key |
| `SC-WORKER-DEPLOY-KEY` | per-use; after-use | `procedures/rotate-secrets.md#sc-worker-deploy-key` | Push to unprotected refs of one repository during one execution window. | none | not-recoverable-by-design | sev3, `procedures/credential-compromise.md#ephemeral-execution-credentials`: Cancel the execution; Delete the key; Quarantine refs pushed with the key (POLICY_VIOLATION) |
| `SC-GITHUB-MIRROR` | scheduled, P180D; schedule, compromise-suspected, key-expiry, recovery-drill; **bundle update** | `procedures/rotate-forge-credentials.md` | Write access to the GitHub recovery seed (main and release/* are protected on GitHub by ruleset sc-mirror-only; tags can be force-pushed). | one mirror interval | recovery-bundle, provider-console | sev1, `procedures/credential-compromise.md#identity-and-forge`: Revoke in GitHub; Compare GitHub refs (main, release/*, tags) to Forgejo and repo bundles; Force a mirror push after rotation; Treat as exposed whenever SC-OBJECT-STORAGE/longhorn-backups or a Longhorn backup of forgejo-data leaked; Rotate |
| `SC-GITHUB-PROBE` | scheduled, P1Y; schedule, compromise-suspected, key-expiry | `procedures/rotate-forge-credentials.md` | Read access to the private mirror repository and its settings. | mirror-freshness observability gap until the PR merges | sops-git, provider-console | sev2, `procedures/credential-compromise.md#identity-and-forge`: Revoke in GitHub; Re-issue and replace the SOPS file by PR |
| `SC-GITHUB-MIRROR-READ` | scheduled, P1Y; schedule, compromise-suspected, key-expiry | `procedures/rotate-forge-credentials.md` | Read access to the private mirror repository. | one restore-test run | sops-git, provider-console | sev3, `procedures/credential-compromise.md#identity-and-forge`: Revoke in GitHub; Re-issue and replace the SOPS file by PR |
| `SC-WOODPECKER-OAUTH` | scheduled, P1Y; schedule, compromise-suspected | `procedures/rotate-forge-credentials.md` | Woodpecker login and repository hooks. | < 5 min (Woodpecker restart) | sops-git, regenerate | sev2, `procedures/credential-compromise.md#ci-and-supply-chain`: Regenerate the client secret; Revoke user grants for the app |
| `SC-WOODPECKER-USER-TOKENS` | automatic; key-expiry, compromise-suspected | `procedures/rotate-secrets.md#sc-woodpecker-user-tokens` | Forgejo access of the token's user; for sc-woodpecker, write on managed repositories (team `sc-automation`; transient admin only during ci-init W03, CD-38). | none (refresh tokens rotate access tokens) | regenerate — Re-login through ci-init. | sev1, `procedures/credential-compromise.md#ci-and-supply-chain`: Revoke the OAuth grants of the affected users in Forgejo; Re-run ci-init W02; Review statuses and webhooks created since exposure |
| `SC-WOODPECKER-AGENT` | scheduled, P1Y; schedule, compromise-suspected | `procedures/rotate-secrets.md#sc-woodpecker-agent` | A holder of either token can start an agent that declares sc-lane=build and receive protected release workflows with their Woodpecker secrets (labels are not bound to tokens; detected by sc-ci-maint, not prevented). | < 5 min for the rotated agent's lane | sops-git, regenerate | sev1, `procedures/credential-compromise.md#ci-and-supply-chain`: Delete the agent registration (revokes its token); Remove unknown agents in Woodpecker admin; Rotate SC-RELEASE-SIGNING SC-REGISTRY-PUSH SC-GHCR-PUSH and the sc-release token if any build-lane workflow ran on an unexpected agent |
| `SC-WOODPECKER-RUNNER` | scheduled, P90D; schedule, compromise-suspected | `procedures/rotate-secrets.md#sc-woodpecker-runner` | Trigger pipelines as sc-runner (the Runner rejects evidence from pipelines without a served intent, ADR-0302). | seconds | sops-git, regenerate | sev2, `procedures/credential-compromise.md#ci-and-supply-chain`: Revoke; Mark evidence from pipelines created after exposure for re-verification |
| `SC-WOODPECKER-MAINT` | scheduled, P180D; schedule, compromise-suspected | `procedures/rotate-secrets.md#sc-woodpecker-maint` | Repository-admin actions in Woodpecker on activated repositories (settings, logs, repository secrets); a holder can read pipeline metadata, change settings and delete or overwrite the release secrets (denial of releases; overwriting would let a forged secret value reach the next release, caught by signature verification against trusted keys). Held by two components (sc-ci-maint, sc-ci-config). | one CronJob run | sops-git, regenerate | sev2, `procedures/credential-compromise.md#ci-and-supply-chain`: Revoke; Audit repository settings and crons (sc-ci-maint drift report) |
| `SC-CI-INTENTS` | scheduled, P1Y; schedule, compromise-suspected | `procedures/rotate-secrets.md#sc-ci-intents` | Read pending verification intents and attest served configs (forged attestations are cross-checked by the Runner's own config digest, plan 20 I5). | < 5 min (both copies in one PR; verification pauses) | sops-git | sev2, `procedures/credential-compromise.md#ci-and-supply-chain`: Rotate both copies in one PR; Re-verify evidence accepted since exposure |
| `SC-WOODPECKER-ADMIN` | per-use; after-use | `procedures/rotate-secrets.md#sc-woodpecker-admin` | Woodpecker administration for at most one hour. | none | regenerate | sev2, `procedures/credential-compromise.md#ci-and-supply-chain`: Revoke the token; Audit repository settings; crons and agents |
| `SC-WOODPECKER-SIGNING` | on-compromise; compromise-suspected | `procedures/rotate-secrets.md#sc-woodpecker-signing` | Forged configuration-extension requests could make sc-ci-config serve configurations for a pipeline that does not exist; sc-ci-config still classifies trust from Forgejo state (ADR-0301). | < 5 min (server restart; sc-ci-config refetches the public key on verification failure) | cnpg-restore, regenerate | sev2, `procedures/credential-compromise.md#ci-and-supply-chain`: Regenerate the key (plan 20 mechanism; verify); Restart sc-ci-config; Review pipelines created since exposure |
| `SC-WOODPECKER-METRICS-TOKEN` | scheduled, P1Y; schedule, compromise-suspected | `procedures/rotate-secrets.md#sc-woodpecker-metrics-token` | Read access to Woodpecker metrics. | one scrape gap (≤ 2 min) | sops-git | sev3, `procedures/credential-compromise.md#observability-and-notification`: Rotate both copies in one PR |
| `SC-RUNNER-SIGNING` | scheduled, P180D; schedule, compromise-suspected | `procedures/rotate-secrets.md#sc-runner-signing` | Anyone holding it can mint task and gateway tokens (gateway tokens additionally need DB registration, ADR-0451). | none (two kids published for ≥ 24 h, longer than the worker token TTL; the Hermes gateway token is re-minted at the kid switch, COST-16) | sops-git, regenerate | sev1, `procedures/credential-compromise.md#control-plane-tokens`: Engage emergency stop level workers; Rotate without keeping the previous kid; Cancel running executions; Review the ledger for unknown jtis |
| `SC-RUNNER-HERMES` | automatic, P30D; schedule, compromise-suspected | `procedures/rotate-secrets.md#sc-runner-hermes` | Start/cancel tasks within budget and policy limits. | < 1 min (Hermes rollout; 24 h overlap) | regenerate | sev2, `procedures/credential-compromise.md#control-plane-tokens`: Force rotation without overlap; Review tasks started since exposure |
| `SC-RUNNER-ADMIN` | scheduled, P1Y; schedule, after-use, compromise-suspected, operator-change, key-expiry; **bundle update** | `procedures/rotate-break-glass.md#runner-admin` | Human-only task transitions, quarantine and emergency stop, attributed to the credential's identity (requires port-forward, i.e. cluster credentials); a forged actor header has no effect. | none (Runner reloads the hash file within 60 s) | recovery-bundle, offline-recovery-identity, regenerate — Operator instances are re-issued by access join; the breakglass instance comes from the bundle. | sev2, `procedures/credential-compromise.md#control-plane-tokens`: Revoke the kid (POST /v1/admin/credentials/{kid}:revoke or access leave); Review SCRunnerAdminAccess alerts and audit rows with that kid; Re-issue |
| `SC-WORKER-TASK-TOKEN` | per-use; after-use | `procedures/rotate-secrets.md#sc-worker-task-token` | One execution's result channel. | none | not-recoverable-by-design | sev3, `procedures/credential-compromise.md#ephemeral-execution-credentials`: Cancel the execution (revokes the jti) |
| `SC-GATEWAY-TOKEN` | per-use; after-use, schedule, compromise-suspected | `procedures/rotate-model-credentials.md` | Model spend within the token's budget scope and model allowlist. | none | regenerate | sev2, `procedures/credential-compromise.md#model-provider-keys`: Revoke the jti; For the Hermes instance force rotation and restart Hermes |
| `SC-MODEL-API-KEYS` | scheduled, P90D; schedule, compromise-suspected, provider-forced | `procedures/rotate-model-credentials.md` | Money (provider account spend limit). | none (Gateway hot-reloads the Secret) | sops-git, provider-console | sev1, `procedures/credential-compromise.md#model-provider-keys`: Engage emergency stop; Revoke at the provider; Compare provider usage to the Gateway ledger; Rotate |
| `SC-FORGEJO-SIGNING-KEY` | on-compromise; compromise-suspected | `procedures/rotate-forge-credentials.md` | Forged signed merges would pass the black-start merge-signature check. | none (new key plus public-key PR; old public key kept for history verification) | sops-git | sev2, `procedures/credential-compromise.md#identity-and-forge`: Rotate the key; Commit the new public key; Mark commits signed after the compromise time as untrusted for black start |
| `SC-MODEL-USAGE-READ` | scheduled, P90D; schedule, compromise-suspected, provider-forced | `procedures/rotate-secrets.md#sc-model-usage-read` | Read of the provider organisation's usage and cost; if the provider has no read-only scope, everything its organisation admin key allows (create or revoke API keys, change members or limits), which is why it never leaves the reconcile Job. | none (reconciliation skips one run and raises its job-failure alert) | sops-git, provider-console | sev1, `procedures/credential-compromise.md#model-provider-keys`: Revoke in the provider console; Review the provider organisation audit log for key and limit changes; Re-check the provider-side spend limit; Re-issue with the least scope |
| `SC-HERMES-API` | automatic, P30D; schedule, compromise-suspected | `procedures/rotate-secrets.md#sc-hermes-api` | Conversation with Hermes (can request work within Runner policy). | < 1 min | regenerate | sev2, `procedures/credential-compromise.md#control-plane-tokens`: Force rotation; Review Hermes sessions |
| `SC-PG-APP` | scheduled, P1Y; schedule, compromise-suspected | `procedures/rotate-secrets.md#sc-pg-app` | One application database. | < 2 min (application restart) | sops-git, cnpg-restore | sev1, `procedures/credential-compromise.md#cluster-and-node`: Rotate the role password via the SOPS file (CNPG reconciles the role); Review pg logs for foreign connections |
| `SC-PG-DR-MARKER` | scheduled, P1Y; schedule, compromise-suspected | `procedures/rotate-secrets.md#sc-pg-dr-marker` | The sc_dr database only (marker rows); WAL switch. | none | sops-git, cnpg-restore | sev3, `procedures/credential-compromise.md#cluster-and-node`: Rotate both copies in one PR |
| `SC-GRAFANA-ADMIN` | scheduled, P1Y; schedule, compromise-suspected, after-use | `procedures/rotate-break-glass.md#grafana` | Grafana dashboards and datasource configuration. | < 1 min | sops-git | sev3, `procedures/credential-compromise.md#observability-and-notification`: Rotate |
| `SC-NOTIFY-TRANSPORT` | on-compromise; compromise-suspected, operator-change | `procedures/rotate-secrets.md#sc-notify-transport` | Spoofed or suppressed notifications. | none | sops-git, operator | sev2, `procedures/credential-compromise.md#observability-and-notification`: Rotate at the transport provider; Send a test notification |
| `SC-RELEASE-SIGNING` | scheduled, P1Y; schedule, compromise-suspected, operator-change | `procedures/signing-key-rotation.md` | Anything signed is accepted at promotion and by the Runner's harness check (a deployment still needs a human-merged digest PR). | none (two trusted keys during the overlap) | sops-git, regenerate — Loss is a rotation: previously signed digests stay verifiable with the committed public key. | sev1, `procedures/signing-key-rotation.md#emergency-revocation`: Mark the key revoked in security/cosign/trusted-keys.yaml; Rebuild and re-promote all images from main; Audit the registry for digests signed after exposure |
| `SC-REGISTRY-PUSH` | scheduled, P90D; schedule, compromise-suspected | `procedures/rotate-secrets.md#sc-registry-push` | Push or overwrite tags in the internal registry (digests and signatures still protect deployments). | none | sops-git, regenerate | sev2, `procedures/credential-compromise.md#ci-and-supply-chain`: Delete the token; List packages and tags pushed since exposure and verify their signatures; Rotate |
| `SC-REGISTRY-PULL` | scheduled, P1Y; schedule, compromise-suspected | `procedures/rotate-secrets.md#sc-registry-pull` | Read access to internal images. | none | sops-git, regenerate | sev3, `procedures/credential-compromise.md#ci-and-supply-chain`: Rotate |
| `SC-GHCR-PUSH` | scheduled, P180D; schedule, compromise-suspected, key-expiry | `procedures/rotate-secrets.md#sc-ghcr-push` | Publish toolbox images under the operator's GHCR namespace (operators verify the cosign signature and the toolbox digest lock before use). | none | sops-git, provider-console | sev2, `procedures/credential-compromise.md#ci-and-supply-chain`: Revoke in GitHub; Delete unsigned or unexpected GHCR versions; Rotate |
| `SC-RENOVATE` | scheduled, P90D; schedule, compromise-suspected | `procedures/rotate-forge-credentials.md` | Branches and PRs as sc-renovate (cannot merge; protected branches block pushes). | none | sops-git, regenerate | sev2, `procedures/credential-compromise.md#ci-and-supply-chain`: Delete the token; Close PRs opened since exposure for re-review; Rotate |
| `SC-BREAK-GLASS-CLUSTER` | scheduled, P1Y; schedule, after-use, compromise-suspected, operator-change; **bundle update** | `procedures/rotate-break-glass.md#cluster` | Root on every node, hence full cluster administration. | none | recovery-bundle, offline-recovery-identity, provider-console | sev1, `procedures/credential-compromise.md#recovery-material`: Replace the public key on all nodes (Ansible); Review sshd journal and the audit log for system:admin; Issue a new key and bundle |
| `SC-SYNTHETIC-USER` | scheduled, P1Y; schedule, compromise-suspected; **bundle update** | `procedures/rotate-secrets.md#sc-synthetic-user` | Interactive SSO login as a read-only verification identity (read Forgejo, edit Grafana dashboards); every login visible in Authentik events. | none | recovery-bundle, offline-recovery-identity, regenerate | sev2, `procedures/credential-compromise.md#identity-and-forge`: Reset the password and TOTP seed via the admin channel; Revoke the user's sessions; Rebuild the bundle |
| `SC-BREAK-GLASS-AUTHENTIK` | scheduled, P1Y; schedule, after-use, compromise-suspected, operator-change; **bundle update** | `procedures/rotate-break-glass.md#authentik` | Full identity administration. | none | recovery-bundle, offline-recovery-identity — If lost: mint a recovery key through cluster break-glass (ak create_recovery_key). | sev1, `procedures/credential-compromise.md#identity-and-forge`: Rotate; Revoke all sessions; Review Authentik events |
| `SC-BREAK-GLASS-FORGEJO` | scheduled, P1Y; schedule, after-use, compromise-suspected, operator-change; **bundle update** | `procedures/rotate-break-glass.md#forgejo` | Full Forgejo administration. | none | recovery-bundle, offline-recovery-identity — If lost, reset through the sc-admin API. | sev1, `procedures/credential-compromise.md#identity-and-forge`: Rotate; Audit admin actions and branch protection |
| `SC-BREAK-GLASS-GRAFANA` | scheduled, P1Y; schedule, after-use, compromise-suspected, operator-change; **bundle update** | `procedures/rotate-break-glass.md#grafana` | Grafana administration. | none | recovery-bundle, sops-git | sev3, `procedures/credential-compromise.md#observability-and-notification`: Rotate |
| `SC-BREAK-GLASS-INFRA` | scheduled, P1Y; schedule, after-use, compromise-suspected, operator-change | `procedures/rotate-break-glass.md#infrastructure` | The entire provider account and all nodes. | none | operator, provider-console | sev1, `procedures/credential-compromise.md#infrastructure-and-dns`: Reset the console password and MFA; Review the provider audit log; Rotate SC-INFRA-PROVIDER |

### 10.3 Reconciliations recorded at Stage 0

| Name used elsewhere | Register entry | Note |
|---|---|---|
| `SC-OPERATOR-SSH` (plans 03, 49) | `SC-SSH-OPERATOR` | alias |
| `SC-BOOTSTRAP-KUBECONFIG` (plans 07, 49) | `SC-K3S-ADMIN-KUBECONFIG` | alias |
| `SC-FORGEJO-WEBHOOK` (16), `SC-FORGEJO-WEBHOOK-RUNNER` (earlier register) | `SC-RUNNER-WEBHOOK` | one HMAC key; plan 16's org-level hook and plan 26's per-repo hook must converge on one owner (open question 1) |
| `SC-METRICS-SCRAPE-TOKENS/{forgejo,woodpecker}` | `SC-FORGEJO-METRICS-TOKEN`, `SC-WOODPECKER-METRICS-TOKEN` | alias |
| `sc-model-api-keys` (plan 53 text) | Secret `strange-company/model-api-keys` of `SC-MODEL-API-KEYS` | the register's name is authoritative |
| `SC-OBJECT-STORAGE/bootstrap-admin` (plans 06, 49, 50), `SC-ETCD-S3`, `SC-OBJECT-STORAGE-PG-<ID>`, `SC-OBJECT-STORAGE-DR`, `SC-OBJECT-STORAGE-BOOTSTRAP`, `seed-read`/`seed-write` subclasses | `SC-OBJECT-STORAGE` instances | instance names follow plan 12 (`pg-backups-<cluster>`) and plan 19 |
| `SC-HERMES-DASHBOARD-OIDC` | `SC-OIDC-CLIENTS/hermes-dashboard` | plan 15 lists the client with the other OIDC clients |
| `SC-BREAK-GLASS-CLUSTER` as client-cert kubeconfig (plan 07 text) | SSH key for node user `sc-breakglass` | plan 15 ADR-0251 is authoritative |
| `SC-AUTHENTIK-BOOTSTRAP` as long-lived | ephemeral, destroyed at sso-init A9 | plan 15 is authoritative |
| `SC-PG-APP` as CNPG-generated | bootstrap-generated, SOPS | plan 12 is authoritative |
| k3s token at `infra/secrets/k3s-token.sops.yaml` (plan 07) | `secrets/offcluster/<cluster>/k3s-token.sops.yaml` | 35-R-3: the k3s token must not be decryptable with the cluster key |
| "SOPS + bundle" for `SC-FORGEJO-APP-SECRETS`, `SC-FORGEJO-SSH-HOST-KEY`, `SC-AUTHENTIK-SECRET-KEY` (plans 15, 16) | SOPS only | 35-R-4: recoverable through the bundled cluster key; avoids bundle churn |
| one `SC-DNS-API` for toolbox and cert-manager (plans 09, 10, 50) | `SC-DNS-API` (toolbox, tier 2 + bundle) and `SC-DNS-ACME` (cert-manager, zone- or challenge-zone-scoped, tier 1) | review AUTH-13; plan 50 adds a second `bootstrap.env` token, plan 10 uses `SC-DNS-ACME` |
| one shared `SC-RUNNER-ADMIN` token + `X-SC-Actor` header (plan 26) | `SC-RUNNER-ADMIN` instances `operator:<login>` (per operator, 365 d, plaintext only with the operator) and `breakglass` (bundle, actor `breakglass:runner-admin`, rotated after every use); hashes in `secrets/strange-company/runner-admin-credentials.sops.yaml` → Secret `agent-runner-admin`; revocation `POST /v1/admin/credentials/{kid}:revoke` | CD-30 |
| registrar account not registered (review AUTH-11) | `SC-DNS-REGISTRAR` (operator-offline) | needed for NS changes when the DNS provider is lost (SPEC §111) |
| `SC-FORGEJO-ADMIN` single storage | seed value in SOPS (creates the account only) + steady value in the bundle set by `forge finalize` | plan 16, review AUTH-9 |
| `dr-tests/blackstart-age` copy of `SC-AGE-CLUSTER` for RT-6 (plan 44 Q-44-1) | **rejected** | A second in-cluster copy of the cluster key in a namespace with S3 and GitHub egress and CNPG-create rights widens exposure for a check that is already covered: RT-6 keeps `sops-recipients` (structural) and gains S-13 (every tier-1 file is referenced, so Flux readiness proves decryptability at the mirrored SHA); real decryption with the bundled cluster key and with the offline recovery key is proven by the quarterly `verify --full` attestation (§14.6). Plan 44 drops `sops-decrypt` from RT-6. |
| `.bootstrap/secrets/age-cluster.agekey` (plan 19 §3) | bundle only; tmpfs `/run/sc` during runs | plan 50 §10.2 rule 1 (no generated secret at rest outside bundle and SOPS) |

## 11. Storage

**Git layout:**

```text
.sops.yaml                                   # creation rules (§14.2)
secrets/
└── offcluster/<cluster>/                    # recovery recipient ONLY; never referenced by any kustomization
    ├── k3s-token.sops.yaml  tofu-encryption.sops.yaml  infra-provider.sops.yaml
    ├── s3-{tofu-state,recovery,seed-read,seed-write,etcd-snapshots}.sops.yaml
    ├── break-glass-{cluster,authentik,forgejo}.sops.yaml  runner-admin.sops.yaml (breakglass plaintext)
└── ci/<cluster>/                            # cluster + recovery recipients; applied to Woodpecker secrets, never to the cluster
    ├── release-signing.sops.yaml  registry-push.sops.yaml  sc-release-forgejo.sops.yaml
    ├── ghcr-push.sops.yaml  ci-ovh-readonly.sops.yaml
security/
├── secret-patterns.yaml                     # kind SecretPatternSet (§14.8.1)
├── gitleaks.toml                            # generated from secret-patterns.yaml
└── secret-env-exceptions.yaml               # 35-R-6
gitops/clusters/<cluster>/**/*.sops.yaml     # cluster + recovery recipients
```

**Object storage** `s3://<prefix>-recovery/<cluster>/`:

```text
bundles/sc-recovery-<cluster>-v<N>.tar.age          # encrypted bundle (versioned; never auto-deleted)
bundles/sc-recovery-<cluster>-v<N>.inventory.json   # PublicInventory (plaintext, no values)
bundles/LATEST.json                                 # LatestPointer (written last)
generations.json                                    # SecretGenerationLedger
attestations/<YYYYMMDDTHHMMSSZ>.json                # Attestation
```

Lifecycle (plan 43): no expiration on `bundles/`, `generations.json` or `attestations/`; noncurrent versions retained ≥ 365 days. Size: a bundle is < 64 KiB.

**Operator machine** `/blackstart/<cluster>/`: `bundles/` (latest `localKeep` versions + `LATEST.json`), `recovery-identity.txt` (transient), `kubeconfig`, `README.txt` (plan 50 §11). Operator-chosen extra copies of the bundle (USB key, password manager attachment) are recommended in `fresh-install.md`.

**Cluster:** Kubernetes Secrets are encrypted at rest by k3s secrets-encryption (`SC-K3S-ENCRYPTION-CONFIG`, plan 07).

## 12. Resource requirements

| Workload | requests | limits | Profiles |
|---|---|---|---|
| Each `dr-tests` recovery check CronJob (§14.6) | cpu 10m, memory 32Mi | cpu 200m, memory 128Mi, ephemeral-storage 64Mi | both |
| `secret-scan-history` CronJob | cpu 50m, memory 128Mi | cpu 1, memory 512Mi, ephemeral-storage 2Gi | both |

Operator side: bundle open/seal needs ≈ 1 GiB RAM for scrypt at work factor 20 (within plan 50's 4 GiB). `activeDeadlineSeconds: 600` on every check Job; `concurrencyPolicy: Forbid`; `successfulJobsHistoryLimit: 1`, `failedJobsHistoryLimit: 3`.

## 13. Security boundary

- **Who can decrypt what.** Cluster key holders (kustomize-controller; the operator via the bundle) decrypt `gitops/clusters/<cluster>/**`. Only the offline recovery identity decrypts `secrets/offcluster/**`. Only the passphrase opens the bundle. Nothing in the cluster can open the bundle or the off-cluster files.
- **`flux-system/sops-age`** is readable by kustomize-controller, cluster-admin **and every upstream operator whose chart grants cluster-wide Secret read** (cert-manager, CNPG, Longhorn, Flux, possibly prometheus-operator; review SEC-4). Strange Company renders no Role or RoleBinding granting `secrets` read in `flux-system`; plan 37 37-AC-30 asserts the complete list of identities that can read it against a recorded allowlist, and plan 34 R-22 accepts the upstream-operator exposure. This is why tier 1b and tier 2 are not encrypted to the cluster key.
- **Secret read RBAC.** No Strange Company ServiceAccount holds cluster-wide `secrets` read. Namespaced exceptions: the Runner in `agent-jobs` (its own per-execution Secrets) and `hermes` (`hermes-managed-env`), per plan 26 §13.
- **Check CronJobs** run as ServiceAccount `recovery-checker` in `dr-tests` with `automountServiceAccountToken: false`, PSA restricted, mounting only `s3-recovery-reader` (`SC-OBJECT-STORAGE/recovery-reader`), `s3-dr-tests-results` (evidence writes) and the register ConfigMap `sc-secret-register` (Flux-generated from `contracts/secret-classes.yaml`). Egress: DNS and the object-storage endpoint only (plan 36).
- **Release credentials** (`SC-RELEASE-SIGNING`, `SC-REGISTRY-PUSH`, `SC-FORGEJO-BOT-TOKENS/sc-release`, `SC-GHCR-PUSH`) live as SOPS files under `secrets/ci/<cluster>/` (recipients `SC-AGE-CI` + recovery) and as Woodpecker secrets with events `[tag]` and image filter `sc-release` (review SEC-12); their exposure in `woodpecker-pg` and, if Woodpecker inlines them, in Pod specs is recorded in the register and in plan 34 R-7/R-24; only protected-mode configurations served by `sc-ci-config` can reference them (ADR-0301) and only the `sc-release` plugin step receives them (plan 23 ADR-0502; plan 37 `sc-ci-build-release-steps`).
- **Threats and mitigations** for secrets are part of plan 34 §14.

## 14. Happy path

### 14.1 Install-time generation (plan 50 `plan.install-identity`)

1. Read the register. Generate every class with `creator.actor` beginning `sc-bootstrap` and `when` = `plan.install-identity` or "before provision" (cluster age, recovery age, tofu passphrase, k3s token, Forgejo admin, Forgejo app secrets, Forgejo SSH host key, Flux deploy key, metrics tokens, OIDC clients, Authentik secret key and audit token, PG app and DR-marker passwords, Runner admin token, webhook HMAC key, backup age identity, break-glass Authentik/Forgejo/Grafana passwords, break-glass cluster SSH key). Operator values come from `bootstrap.env`. `SC-DNS-TUNNEL` is created through the Cloudflare API.
2. Prompt the passphrase twice; seal bundle **v1** (`reason.trigger: initial-bootstrap`); write `recovery-identity.txt` once (`SC-AGE-RECOVERY` private key) and print its public recipient.
3. Write `.sops.yaml` rules for `<cluster>` (§14.2), render in-cluster Secrets as SOPS files, write off-cluster SOPS files, verify each file decrypts, commit (plan 50 commit scope rules extended to `secrets/offcluster/<cluster>/**`; interface for plan 50 §13).
4. `plan.bundle-upload` → `plan.bundle-confirm` (plan 50 §14.3 steps 5–6), then write inventory v1, ledger (one entry per generated class/instance, generation 1), pointer.
5. Later phases that create credentials (forge-init bot tokens, Woodpecker OAuth, supply-chain init) write SOPS files encrypted to both recipients and append ledger entries; none of them changes bundle contents (plan 50 50-R-4), except P11's `SC-RELEASE-SIGNING`, which is SOPS-only.

### 14.2 SOPS layout and `.sops.yaml`

```yaml
# .sops.yaml — first matching rule wins; there is deliberately NO catch-all,
# so SOPS fails ("no matching creation rules") for any file outside these paths.
creation_rules:
  # Tier 2: off-cluster material, recovery recipient only (35-R-3).
  - path_regex: ^secrets/offcluster/acme-prod/[a-z0-9.-]+\.sops\.yaml$
    age: age1zvkyg2lqzraa2lnjvqej32nkuu0ues2s82hzrye869xeexvn73equnujwj   # SC-AGE-RECOVERY (public)
  # Tier 1: cluster secrets, two recipients (ADR-0012). Kubernetes Secret manifests only.
  - path_regex: ^gitops/clusters/acme-prod/.+\.sops\.yaml$
    encrypted_regex: ^(data|stringData)$
    age: >-
      age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p,
      age1zvkyg2lqzraa2lnjvqej32nkuu0ues2s82hzrye869xeexvn73equnujwj
  # Tier 1b: CI release credentials applied into Woodpecker by `sc-bootstrap ci-secrets apply`
  # (plan 23); recipients SC-AGE-CI (bundle only) + recovery — NOT the cluster key (review SEC-4);
  # never referenced by any kustomization.
  - path_regex: ^secrets/ci/acme-prod/[a-z0-9.-]+\.sops\.yaml$
    age: >-
      <SC-AGE-CI public recipient>,
      age1zvkyg2lqzraa2lnjvqej32nkuu0ues2s82hzrye869xeexvn73equnujwj
  # CI fixture clusters: TEST-ONLY recipients whose identities are committed under
  # src/internal/secrets/testdata/age/ (never used for real secrets).
  - path_regex: ^gitops/clusters/(ci-shm-tunnel|ci-sha-direct|ci-handoff)/.+\.sops\.yaml$
    encrypted_regex: ^(data|stringData)$
    age: <test recipient 1>,<test recipient 2>
```

Rules:
- Plan 18's request (`path_regex: ^gitops/.*\.sops\.yaml$`, `encrypted_regex: ^(data|stringData)$`) is satisfied by the per-cluster rule; a generic `^gitops/` rule is **not** used because it would silently encrypt a new cluster's files to another cluster's key.
- One installation per cluster directory; a second cluster in the same repository gets its own pair of rules and its own keys.
- Off-cluster files are flat YAML maps of `key: value`; every value is encrypted (no `encrypted_regex`).
- SOPS files never contain `sops.age[].enc` stanzas for any recipient other than those of their rule (`secrets lint` check S-3).

### 14.3 Age key lifecycle

**Generation:** §14.1. Public recipients are recorded in `.sops.yaml`, in `gitops/clusters/<cluster>/bootstrap-config.yaml` (plan 50) and in every bundle manifest (`age_recipients`).

**Cluster key rotation** (`./bootstrap rotate age-cluster`, procedure `rotate-age-key.md#cluster-key`; scheduled yearly, or on compromise/loss). Decryptability is never lost because the recovery recipient stays on every file and both cluster identities are installed during the overlap:
1. Open the bundle; generate K₂.
2. Stage: patch `flux-system/sops-age` so `age.agekey` contains **K₁ and K₂** (age identity files hold several identities; kustomize-controller tries each, verify 35-AC-12). Out-of-band, through the admin kubeconfig.
3. `sops rotate --in-place --add-age K₂ --rm-age K₁` on every file matched by the tier-1 rule (new data key per file; recipients become {K₂, R}). Update the rule in `.sops.yaml`.
4. Seal bundle v(N+1) with K₂ (`reason.trigger: rotation`, `rotated_classes: [SC-AGE-CLUSTER]`), upload, confirm, update inventory, ledger and pointer. **The bundle is updated before the PR merges**, so at every instant the bundle opens a key that decrypts `main`.
5. Open PR `rotate/age-cluster-<ts>`; CI `secrets-lint` verifies recipients = {K₂, R}; human merge; wait until every Kustomization is Ready at the merge SHA.
6. Remove K₁ from `sops-age`. Force-reconcile one secret-bearing layer to prove K₂ alone works.
7. On compromise (not schedule), additionally rotate every value in tier 1 (old ciphertexts in Git history decrypt with K₁): procedure `credential-compromise.md#recovery-material`.

**Recovery key rotation** (`./bootstrap rotate age-recovery`, `rotate-age-key.md#recovery-key`; on compromise or custodian change):
1. Open the bundle; prompt R₁ (if available); generate R₂ and print it (paper copy) — `recovery-identity.txt` written once.
2. Tier 1: `sops rotate --add-age R₂ --rm-age R₁` using K (bundle). Tier 2: decrypt with R₁ and re-encrypt to R₂; **if R₁ is lost**, regenerate tier-2 files from the bundled values (every tier-2 file has a bundled counterpart, 35-R-4).
3. Update `.sops.yaml`; PR; merge.
4. Seal bundle v(N+1) with `age_recipients.recovery = R₂`.
5. Operator stores R₂ offline, runs `recovery-bundle verify --full` (attestation proves R₂ decrypts every file), then destroys every copy of R₁.
6. On compromise, rotate every tier-2 value and every tier-1 value (Git history), as in the cluster-key case.

**`SC-AGE-CI`** (tier 1b): generated by `supply-chain init` at P11 and sealed into the bundle; rotated on compromise by `rotate-age-key.md#ci-key` (rewrap tier 1b to {new CI key, R}; rotate every tier-1b value if compromised). **Lost cluster key, bundle intact:** not possible (the bundle is its authoritative store). **Lost bundle and passphrase, recovery key intact:** decrypt tier-2 files with R for off-cluster values, decrypt tier-1 with R, then rotate the cluster key and create a new bundle under a new passphrase (`rotate-age-key.md#lost-bundle` with a live cluster; `rotate-age-key.md#lost-bundle-black-start` during black start, where the re-encrypted tier-1 files and `.sops.yaml` go into the recovery commit, review DR-15). **Both bundle and recovery key lost, cluster alive:** `rotate-age-key.md#both-lost-cluster-alive`. **Lost recovery key, bundle intact:** recovery-key rotation, lost variant (above). **Both lost:** the installation cannot be black-started from Git secrets; recovery means a fresh install with new credentials (documented as a disaster in `credential-compromise.md#recovery-material`).

### 14.4 Recovery bundle

**Format.** `tar` (ustar, deterministic order: `manifest.json` then entries sorted by path) encrypted with **one age scrypt recipient** (passphrase), work factor 20, no other recipients. Filename `sc-recovery-<cluster>-v<N>.tar.age`. `manifest.json` validates against `recovery-bundle.schema.yaml` and never contains a value; each entry file is non-empty (L-4).

**Contents** (every class with `inRecoveryBundle: true`, per instance where applicable):

| Entry path | Class / instance | Format |
|---|---|---|
| `entries/SC-AGE-CLUSTER/cluster.agekey` | cluster age identity (current; previous during rotation) | age-identity |
| `entries/SC-K3S-TOKEN/token.txt` (+ `token_previous.txt` for 30 d after rotation) | k3s token | text |
| `entries/SC-TOFU-ENCRYPTION/passphrase.txt` | OpenTofu passphrase | text |
| `entries/SC-INFRA-PROVIDER/credentials.env` | provider API credentials | env |
| `entries/SC-DNS-API/token.txt`, `entries/SC-DNS-TUNNEL/token.txt` | toolbox DNS/tunnel API token, tunnel token | text |
| `entries/SC-AGE-CI/ci.agekey` | tier-1b identity (from P11) | age-identity |
| `entries/SC-SYNTHETIC-USER/credentials.json` | synthetic verification user (plan 15) | json |
| `public/trusted-keys.yaml` (manifest `public_files`, not a secret) | copy of `security/cosign/trusted-keys.yaml` at the sealing commit, to verify bootstrap-capsule signatures offline (CD-33); a change to that file triggers `refresh-bundle` | yaml |
| `entries/SC-OBJECT-STORAGE/drill-reader/credentials.env` | drill reader of the source installation (review DR-20) | env |
| `entries/SC-OBJECT-STORAGE/<instance>/credentials.env` for `admin`, `tofu-state`, `recovery`, `seed-read`, `seed-write`, `etcd-snapshots` | S3 keys | env |
| `entries/SC-GITHUB-MIRROR/token.txt` | mirror PAT | text |
| `entries/SC-FORGEJO-ADMIN/credentials.json` | `sc-admin` | json |
| `entries/SC-BREAK-GLASS-CLUSTER/id_ed25519` | node `sc-breakglass` key | ssh-private-key |
| `entries/SC-BREAK-GLASS-AUTHENTIK/credentials.json`, `entries/SC-BREAK-GLASS-FORGEJO/credentials.json`, `entries/SC-BREAK-GLASS-GRAFANA/credentials.json` | break-glass logins | json |
| `entries/SC-RUNNER-ADMIN/breakglass/token.txt` | Runner admin break-glass bearer (actor `breakglass:runner-admin`; operator instances are never bundled, CD-30) | text |
| `entries/SC-AGE-BACKUP/backup.agekey` | repository-backup identity (current + previous still needed) | age-identity |

**Storage.** (a) `/blackstart/<cluster>/bundles/` on the operator machine; (b) `s3://<prefix>-recovery/<cluster>/bundles/` (versioned, Object Lock governance 365 d where supported; never deleted by automation); (c) operator-chosen offline copies. The passphrase and the recovery identity are never stored by Strange Company.

**Versioning.** `bundle_version` is strictly monotonic; `previous_bundle_sha256` chains versions. A new version is created only by `sc-bootstrap`; it is uploaded with `If-None-Match: *` so a concurrent writer cannot overwrite a version key.

**Update triggers** (each produces version N+1 with `reason.trigger`; a merged change to `security/cosign/trusted-keys.yaml` also requires `refresh-bundle` so the `public_files` copy stays current, CD-33): `initial-bootstrap`; `rotation` of any class with `bundleUpdateRequired: true` (listed in `rotated_classes`); `passphrase-change`; `recovery-drill` (fencing rotations in ADR-0554); `black-start` (new generation after recovery); `manual` (`refresh-bundle` after `bootstrap.env` changes).

**Creation steps** (`bundle.Seal`): open previous (if any) → apply changes → build manifest (generation +1 for changed entries, `rotated_at`) → tar → encrypt → write locally (tmp, fsync, rename) → upload → read back, sha256 compare → re-open the downloaded copy in a fresh decoder → write inventory → write ledger entries → write pointer. If any step after upload fails, the pointer still names the previous version, which remains valid.

### 14.5 Rotation framework

#### 14.5.1 Rotation kinds

| Kind | Classes | Flow |
|---|---|---|
| **Tier-1 SOPS, PR** | every class whose authoritative storage is `sops-git` | generate → write SOPS → bump consumer annotations → PR (`rotate/<class>-<ts>`) → CI (`secrets-lint`) → human merge → Flux → verify consumer → revoke old at the issuer (if any) → ledger |
| **Tier-2 / bundle** | classes with authoritative `recovery-bundle`, `bootstrap-env` or `offcluster-sops` | generate or prompt → apply at the issuer (provider console/API) with overlap → update tier-2 SOPS (PR) and node files (Ansible re-run where applicable) → seal bundle N+1 → verify → revoke old → ledger |
| **Issuer-side automatic** | `SC-K3S-ADMIN-KUBECONFIG`, `SC-INTERNAL-CA`, `SC-RUNNER-HERMES`, `SC-HERMES-API`, `SC-GATEWAY-TOKEN/hermes` | owned by the issuing software; alerts on failure (plan 07 cert expiry, plan 26 `SCHermesCredentialRotationFailed`) |
| **Per-use (ephemeral)** | §14.7 | minted per use; "rotation" = expiry + revocation |

Before transfer (P05–P12, `main` still writable by `sc-admin` in protection stage `bootstrap`), `sc-bootstrap` commits directly (CD-19: everything up to `CONTROL TRANSFER COMPLETE` is unattended; P11's tier-1b files and `SC-AGE-CI` are created later by `supply-chain init`); after transfer it pushes the branch and opens the PR as the automation account `sc-admin` (password from the bundle), so that the operator can approve and merge it without self-approval (review DR-26; the human approval and merge are the human action required by SPEC §21), and waits (`exit 75` until merged, plan 50 §7.2). **Exception, recovery fencing (ADR-0554, review DR-4):** during `./bootstrap recover` no Forgejo exists; fencing rotations write their tier-1 SOPS files (and, in direct mode, DNS records) locally and the files are committed **inside the recovery commit**, never by PR.

#### 14.5.2 Schedule

Scheduled maximum ages are in the register (`rotation.maxAge`): P90D for Forgejo bot tokens, Runner Woodpecker token, model API keys, registry push and Renovate; P180D for object storage, DNS API, GitHub mirror, Runner signing, GHCR push; P1Y for most others; P30D automatic for Runner-managed Hermes credentials. The `secret-rotation-age` check (§14.6.3) warns 14 days before and alerts after `maxAge`.

#### 14.5.3 Emergency rotation

`./bootstrap rotate secret <CLASS> --emergency` skips the overlap: it **revokes the old value at the issuer first** (instant containment), then delivers the new value by the normal PR. Consumers are expected to fail during the gap; the procedure lists the expected alerts. The severity and first actions per class are in the register's `compromise` block.

#### 14.5.4 Runner signing key (JWKS overlap)

`SC-RUNNER-SIGNING` rotation (plan 53 requirement: ≥ 24 h with two `kid`s): PR 1 adds the new public key to the committed JWKS ConfigMap `sc-runner-jwks` and the new private key as `next.pem` in `agent-runner-signing`; after Flux applies, the Gateway accepts both kids. PR 2 (≥ 24 h later, > max token TTL of 1 h 20 min) switches the Runner to sign with the new kid (`private.pem` ← new, `previous.pem` ← old). PR 3 (≥ 24 h later) removes the old kid from JWKS and `previous.pem`. Emergency: one PR that replaces the key and JWKS (all outstanding tokens become invalid; running executions fail as infrastructure failures and the Runner revokes them).

### 14.6 Recovery verification and checks

All four checks follow ADR-0553: CronJob labelled `strange.company/check: <id>`, exit 0 iff the invariant holds, evidence written as JSON to the Job log and to `s3://<prefix>-evidence/checks/<id>/<ts>.json` with the write-only credential `SC-OBJECT-STORAGE/dr-tests-results` (prefix `checks/`, plan 14 extends that instance; review DR-18/STO-17), in plan 44's `restore-test-result` format with `test_kind: recovery-check` (plan 44 adds the enum value). They never decrypt anything. Image: the toolbox image digest from `toolbox/image.lock` (as plan 15 does for probes), command `sc-bootstrap checks <id>`.

| # | CronJob (`dr-tests`) | Schedule (UTC) | Invariant | Alert (catalogue) |
|---|---|---|---|---|
| 14.6.1 | `restore-test-recovery-bundle` | Sun 03:30 | `LATEST.json` exists and validates; the named object exists; its sha256 equals the pointer; its age header has exactly one recipient stanza of type `scrypt` with work factor ≥ 18; the inventory validates, has the same `bundle_sha256`, and lists every class/instance with `inRecoveryBundle: true` in the mounted register; the bucket's versioning is `Enabled` | `SCRecoveryBundleVerifyFailed` (maxAge 8d) |
| 14.6.2 | `recovery-bundle-currency` | daily 04:10 | for every bundled class/instance, ledger generation == inventory generation (a rotation recorded in the ledger but not sealed → fail) | `SCRecoveryBundleOutdated` (maxAge 26h) |
| 14.6.3 | `secret-rotation-age` | daily 04:30 | for every scheduled class/instance in the ledger, `now − rotated_at ≤ maxAge`; exports nothing else. A second output line marks classes due within 14 days (exit 0 but the Job writes `due` evidence; the warning alert derives from the evidence metric below) | `SCSecretRotationOverdue`, `SCSecretRotationDue` (info) |
| 14.6.4 | `recovery-key-attestation` | daily 04:20 | the newest `attestations/*.json` validates, has `bundle_version` == LATEST, and `attested_at` ≤ 90 days ago | `SCRecoveryKeyAttestationOverdue` (maxAge 26h) |

`SCSecretRotationDue` uses the check's evidence count exported through the `platform-probes` collector (plan 15 §21.2 hosts probes; interface: a `recovery` probe module reads the newest `checks/secret-rotation-age/*.json` and exports `sc_secret_rotation_due{class}` (bounded: ≤ 60 classes)).

**Full verification** (`sc-bootstrap recovery-bundle verify --full`, quarterly and at P10; procedure `rotate-age-key.md#attest`): in a fresh toolbox container with no `/blackstart` mount and no `bootstrap.env` (`./bootstrap exec --clean-room`, interface for plan 50):
1. download `LATEST.json` and the bundle with `SC-OBJECT-STORAGE/recovery` entered at the prompt (or read from the bundle after step 2 for subsequent fetches);
2. prompt the passphrase; open; validate the manifest and every entry digest;
3. using **only** the bundled cluster key, decrypt every tier-1 file at `origin/main` (count `n₁`);
4. prompt the offline recovery identity; decrypt every tier-1 and tier-2 file (count `n₂`, `n₃`); compare each tier-2 value with its bundled counterpart (equal);
5. write `attestations/<ts>.json` (`Attestation`), exit 0. Any failure: exit 9 with the failing file named.

This is the ADR-0012 verification and the SPEC §109 restore test for recovery material.

### 14.7 Ephemeral credentials

| Credential | Minted by | TTL / expiry | Revocation | Evidence of cleanup |
|---|---|---|---|---|
| `SC-WORKER-TASK-TOKEN` | Runner at dispatch | Job deadline + 300 s | `runner.tokens.revoked_at` at terminal state or cancel | 26-AC-25 |
| `SC-GATEWAY-TOKEN/worker` | Runner at dispatch | deadline + 5 m + 10 m | `ledger.gateway_tokens.revoked_at` (checked per request) | 53-AC-13 |
| `SC-WORKER-DEPLOY-KEY` | Runner at dispatch | deleted at terminal state, at the latest deadline + 600 s (sweeper) | Forgejo key delete | `SCRunnerOrphanDeployKeys`, 26-AC-42 |
| per-execution Secret `agent-jobs/<execution-id>` | Runner | ownerReference to the Job; deleted with the Job after log archival; Job TTL 6 h fallback | Secret delete | 26-AC-22 |
| `SC-AUTHENTIK-BOOTSTRAP` | sso-init | until sso-init A9 (≤ 1 day) | token delete + akadmin deactivation | `SCAuthentikBootstrapAdminActive` (plan 15) |
| `SC-WOODPECKER-ADMIN` | sc-bootstrap | ≤ 1 h | token delete at command end | process exit |
| `SC-OBJECT-STORAGE/seed-read` in-cluster Secret | sc-bootstrap | until transfer T6 | Secret delete; key rotated after transfer (stays in bundle for black start) | plan 19 G7 |
| `SC-LAB-SSH` | CI lab | ≤ 6 h | VM destroyed | lab teardown |

### 14.8 Secret scanning, scrubbing and no empty secrets

#### 14.8.1 Pattern set

`security/secret-patterns.yaml` (kind `SecretPatternSet`, apiVersion `strange.company/v1alpha1`): a list of `{id, description, regex, entropyMin?, severity, appliesTo: [git, logs, results, telemetry]}`. Initial ids: `age-secret-key` (`AGE-SECRET-KEY-1[0-9A-Z]{58}`), `pem-private-key` (`-----BEGIN [A-Z ]*PRIVATE KEY-----`), `cosign-private-key` (`-----BEGIN ENCRYPTED (SIGSTORE|COSIGN) PRIVATE KEY-----`), `openssh-private-key`, `anthropic-key` (`sk-ant-[A-Za-z0-9_-]{20,}`), `openai-key` (`sk-(proj-)?[A-Za-z0-9_-]{20,}`), `google-api-key` (`AIza[0-9A-Za-z_-]{35}`), `github-token` (`gh[pousr]_[A-Za-z0-9]{36,}`, `github_pat_[A-Za-z0-9_]{50,}`), `jwt` (`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`), `s3-secret-assignment` (`(?i)(secret_access_key|SECRET_ACCESS_KEY)\s*[:=]\s*\S{20,}`), `generic-high-entropy-assignment` (`(?i)(password|passwd|token|secret|api_key)\s*[:=]\s*['"]?[A-Za-z0-9+/_=-]{24,}` with Shannon entropy ≥ 4.0), `forgejo-token` (40 hex in an `Authorization: token` context). Allowlist: SOPS `ENC[AES256_GCM,…]` values, files under `**/testdata/**` containing the marker `sc-test-fixture`, and public age recipients (`age1…`).

Each pattern has a positive and a negative fixture in `security/testdata/patterns/`; `make secrets-patterns-test` runs every consumer (gitleaks, Runner scrubber library, sc-bootstrap redactor) against them.

#### 14.8.2 Where scanning runs

| Point | Mechanism | Action on hit |
|---|---|---|
| Every PR and every push (Woodpecker main pipeline, verify pool, no secrets) | `make secrets-scan` = gitleaks over `base..head` with `--redact` and the generated config | pipeline fails → required status fails (merge blocked) |
| Agent results, handoffs, diffs, logs (Runner ingestion, plan 26 §14.4 step 7) | exact match against every value issued to the execution plus the pattern set | `SECRET_DETECTED`, redaction, credential revocation, `SCWorkerPolicyViolation` |
| Repository history (nightly) | CronJob `secret-scan-history` in `dr-tests`: clones every managed repository read-only with `SC-FORGEJO-BOT-TOKENS/sc-backup` (copy in `dr-tests`), runs gitleaks over full history; check-CronJob semantics | `SCSecretScanHit` |
| Bootstrap output | plan 50 redactor (exact values + patterns) | redaction |
| Telemetry | plan 40 redaction processor; plan 53 never logs bodies; ADR-0552 excludes `agent-jobs`, `ci-jobs`, `hermes` logs from Loki | redaction + `SCTelemetryForbiddenAttributeDropped` |
| Rendered manifests | `make secrets-lint` S-1 (§14.8.3) | CI failure |

A historical hit in Git cannot be removed by rewriting `main` (protected; mirrored to GitHub). The response is rotation of the leaked credential (`credential-compromise.md`), then an allowlist entry for the specific commit and fingerprint in `security/gitleaks-baseline.json` added by a reviewed PR.

#### 14.8.3 `make secrets-lint` checks (offline, deterministic, no keys)

| ID | Check |
|---|---|
| S-1 | No rendered or committed `kind: Secret` with `data`/`stringData` outside a `*.sops.yaml` file (except the exemptions of 35-R-2 that are never committed) |
| S-2 | Every `*.sops.yaml` has a `sops:` block and every value under `data`/`stringData` (tier 1) or every leaf (tier 2) is `ENC[…]` |
| S-3 | Each file's age recipients equal its `.sops.yaml` rule's recipients exactly |
| S-4 | No `ENC[AES256_GCM,data:,…]` (empty plaintext) and no `ENC[…,type:str]` whose data decodes to ≤ 0 bytes (35-R-5) |
| S-5 | Every Secret manifest carries a registered `strange.company/secret-class` (and instance where the class has instances) |
| S-6 | Every `SC-*` identifier in `plans/`, `adr/`, `contracts/`, `procedures/` resolves to a class, an alias (register header), or a family wildcard `SC-<PREFIX>-*` matching ≥ 1 class; non-secret markers are listed in `security/secret-id-ignore.yaml` (initial: `SC-CANARY-*`, `SC-STUB*`, `SC-PARTIAL-DIFF-*`, commit trailers `SC-Task`/`SC-Execution`/… which are mixed-case) (35-R-1) |
| S-7 | Every `procedure` path in the register exists, and every `#anchor` exists as a heading in that file |
| S-8 | `secretKeyRef`/`envFrom.secretRef` only for workloads in `security/secret-env-exceptions.yaml` (35-R-6) |
| S-9 | Files under `secrets/offcluster/` and `secrets/ci/` are not referenced by any `kustomization.yaml` |
| S-10 | No test recipient appears in a non-CI cluster rule, and no real recipient in a CI fixture rule |
| S-11 | `security/gitleaks.toml` equals the generation from `security/secret-patterns.yaml` |
| S-12 | `plans/35-secrets.md` §10.2 equals the generation from the register |
| S-13 | Every tier-1 `*.sops.yaml` under `gitops/clusters/<cluster>/` is referenced by a kustomization reachable from the cluster root, so "every Kustomization Ready" implies "every tier-1 file decrypts with the cluster key" (used instead of an in-cluster key copy, §10.3) |

### 14.9 Generation ledger

`s3://<prefix>-recovery/<cluster>/generations.json` (schema `SecretGenerationLedger`): `{install_id, updated_at, entries: [{class, instance?, generation, rotated_at, actor, reason, bundle_version?, commit?}]}`. Append-only in content (each write is a new object version containing all previous entries plus the new one; `sc-bootstrap` refuses to write a ledger that drops an entry).

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| SOPS file not decryptable by the cluster key (wrong recipients, stale rule, missing `sops-age`) | Flux Kustomization `DecryptionFailed`; `SCSopsDecryptionFailing`; `SCFluxReconciliationFailing` | that layer stops reconciling; running workloads keep old Secrets | Flux retries every `retryInterval` | `sops-decryption-failure.md` |
| `sops-age` Secret deleted | same | all secret-bearing layers stall | none | `sops-decryption-failure.md` §C (re-create from bundle) |
| Bundle older than a bundled rotation | `SCRecoveryBundleOutdated` | black start would restore stale credentials | none | `rotate-secrets.md#bundle-refresh` |
| Bundle missing, corrupt or pointer wrong | `SCRecoveryBundleVerifyFailed` | black start at risk | none | `rotate-secrets.md#bundle-refresh` |
| Attestation older than 90 d | `SCRecoveryKeyAttestationOverdue` | unknown possession of recovery material | none | `rotate-age-key.md#attest` |
| Scheduled rotation overdue | `SCSecretRotationOverdue` | longer exposure window | none | `rotate-secrets.md` (class section) |
| Passphrase forgotten | operator report; exit 8 | bundle unusable | none | `rotate-age-key.md#lost-bundle` (needs recovery identity) |
| Recovery identity lost | attestation fails at step 4 | black start without bundle impossible | none | `rotate-age-key.md#recovery-key` (lost variant) |
| Secret committed in plaintext | gitleaks on PR (blocked) or nightly `SCSecretScanHit` | credential exposure (mirrored to GitHub) | PR blocked | `credential-compromise.md` |
| Empty Secret attempted | VAP `sc-secret-nonempty` denial event; `secrets-lint` S-4 | apply fails (Flux layer not Ready) | none | fix the source; never patch around |
| Credential leaked from a worker | Runner `SECRET_DETECTED` | one execution's credentials | revoke all execution credentials | `credential-compromise.md#ephemeral-execution-credentials` |
| Check CronJob cannot reach S3 | Job fails → `…Failed`/stale alerts | detection gap | none | `object-storage-outage.md` (plan 14) |
| Concurrent bundle writers | `If-None-Match` 412 on upload | second writer fails | exit 7 | rerun after the first completes |
| Rotation PR merged before bundle sealed (operator error) | `SCRecoveryBundleOutdated` within 26 h | stale bundle | none | `rotate-secrets.md#bundle-refresh` |

## 16. Retry behavior

- Bundle upload: 3 attempts with 2/4/8 s backoff, then exit 7; the local copy remains and the pointer is unchanged.
- Flux decryption retries are Flux's (`retryInterval` 1 m, plan 18).
- Check CronJobs do not retry within a run (`backoffLimit: 0`); the next schedule re-evaluates. Staleness alerts cover repeated failure.
- Rotations are not retried automatically; the tool is idempotent and is re-run (§17).

## 17. Idempotency behavior

- `rotate secret` records a journal entry `rotation/<class>/<instance>` with phases `generated`, `issuer-applied`, `sops-written`, `pr-opened`, `merged`, `verified`, `old-revoked`, `ledger-written`, `bundle-sealed`. A rerun resumes at the first incomplete phase; the generated value is recovered from the staged bundle (`bundle.Stage` writes a candidate version locally, sealed, before any issuer change), never regenerated, so a crash cannot leave the issuer holding a value nobody has.
- Sealing a bundle with identical contents and reason is a no-op (manifest digest compared).
- Ledger writes are keyed by `(class, instance, generation)`.
- Re-running `secrets lint/scan` has no side effects.

## 18. Bootstrap behavior

Covered by §14.1. Ordering constraints: bundle v1 is sealed, uploaded and confirmed **before** `plan.compute-plan` (plan 50 §14.3), so no paid resource exists without recoverable credentials. `sops-age` is created at seed before any secret-bearing layer (plan 19). The four check CronJobs ship in the `backups` layer (post-transfer) and the first `recovery-key-attestation` evidence is produced by the P10 full verification.

## 19. Upgrade behavior

- SOPS, age and gitleaks versions move by Renovate PR; 35-AC-11 re-verifies that a file written by the new toolbox SOPS decrypts with the pinned kustomize-controller and vice versa.
- Register schema changes are `v1alpha1` until the Self-Hosting Threshold; adding fields is backward compatible; renaming a class requires keeping the old id as an alias for one release.
- Bundle format: `schema_version` in the manifest; `sc-bootstrap` reads the current and previous manifest versions.

## 20. Recovery behavior

- **Black start** (plan 45): operator supplies the bundle (local or S3 with `SC-OBJECT-STORAGE/recovery` from the operator's records) and passphrase → `./bootstrap recover` opens the bundle, seeds with `SC-AGE-CLUSTER`, uses bundled infra/state/k3s/forge-admin values. If the bundle is unusable, the recovery identity decrypts `secrets/offcluster/` and tier 1; a new cluster key and bundle are created before seeding.
- **Fencing** (ADR-0554): unless the operator attests the old installation is destroyed, recovery rotates every `SC-OBJECT-STORAGE` write instance (including `dr-tests-results` and `telemetry-debug`; in `shared` credential mode this means rotating `admin` itself), `SC-GITHUB-MIRROR`, `SC-DNS-TUNNEL`, `SC-NOTIFY-TRANSPORT` and the `SC-MODEL-API-KEYS` (no double spend), with `reason.trigger: recovery-drill`, before seeding. The new tier-1 SOPS files are written locally into the recovery commit (review DR-4, DR-30); plan 45 owns the step list.
- After recovery, a `black-start` bundle version is sealed and `verify --full` produces a fresh attestation.

## 21. Observability

**Metrics** (no values, bounded labels): kube-state-metrics CronJob series for the five CronJobs; `sc_secret_rotation_due{class}` and `sc_secret_rotation_overdue{class}` (platform-probes recovery module); `sc_recovery_bundle_version` and `sc_recovery_bundle_age_seconds` (same module, from the inventory).

**Alerts** (catalogue names, CD-01):

| Alert | Expression sketch | Severity / class |
|---|---|---|
| `SCRecoveryBundleVerifyFailed` (exists) | check-cronjob `restore-test-recovery-bundle`, maxAge 8d | warning, black-start-threat |
| `SCRecoveryBundleOutdated` (exists) | check-cronjob `recovery-bundle-currency`, maxAge 26h | warning, black-start-threat |
| `SCRecoveryKeyAttestationOverdue` (exists) | check-cronjob `recovery-key-attestation`, maxAge 26h | warning, black-start-threat |
| `SCSecretRotationOverdue` | check-cronjob `secret-rotation-age`, maxAge 26h | warning, security, notify |
| `SCSecretRotationDue` | `max(sc_secret_rotation_due) > 0` for 1h | info, silent |
| `SCSopsDecryptionFailing` | `gotk_reconcile_condition{kind="Kustomization",type="Ready",status="False"} == 1` joined with event reason `DecryptionFailed` (verify the metric/event surface of the pinned Flux; fallback: Flux notification event → Alertmanager with `reason` label) for 10m | critical, page |
| `SCSecretScanHit` | check-cronjob `secret-scan-history`, maxAge 26h | critical, security, page |

**Logs:** check Jobs print one JSON evidence line; no values.

## 22. Testing strategy

All deterministic; no paid APIs; test-only keys live in `src/internal/secrets/testdata/` with the `sc-test-fixture` marker.

- **Unit (Go):** generator formats and non-emptiness; deterministic tar; seal/open round trip; manifest validation; inventory derivation; ledger append-only guard; JWKS overlap logic; redactor and scrubber against the pattern fixtures.
- **Contract (F02):** register and bundle schemas; every `examples`/`x-invalid-examples`; the register file validates; `secrets-lint` S-1…S-12 each with a negative fixture.
- **k3d (offline cluster):** a fixture cluster applies SOPS Secrets through kustomize-controller with test keys; age cluster-key rotation end to end (§14.3) with zero `DecryptionFailed` events; VAP `sc-secret-nonempty` denies an empty Secret.
- **Live (P10):** full verification with the real bundle; check CronJobs pass; induced failures fire alerts.

## 23. Acceptance criteria

### GATE-P10

Offline (also required by F11; plan 18 lists `make secrets-lint`):

- [ ] **35-AC-1** `python3 -I` validation (or `make contracts`) of `contracts/secret-classes.yaml` against its schema exits 0; ids are unique (`yq '.spec.classes[].id' | sort | uniq -d` empty).
- [ ] **35-AC-2** `make secrets-lint` exits 0 on `main`; each negative fixture in `src/internal/secrets/testdata/lint/<check-id>/` makes it exit 9 naming exactly that check (13 fixtures).
- [ ] **35-AC-3** `make secrets-patterns-test` exits 0: every positive fixture is detected and every negative fixture is not, by gitleaks, the Runner scrubber library and the sc-bootstrap redactor.
- [ ] **35-AC-4** `make secrets-scan` on a branch adding `security/testdata/canary/leak.txt` containing a generated `AGE-SECRET-KEY-1…` value exits non-zero, and the Woodpecker main pipeline on that PR reports failure.

Bundle and keys:

- [ ] **35-AC-5** After a fresh install, `./bootstrap exec -- sc-bootstrap recovery-bundle status --json` shows `bundle_version ≥ 1`, the S3 object sha256 equal to `LATEST.json.sha256`, and the inventory class set equal to `yq '.spec.classes[] | select(.inRecoveryBundle) | .id' contracts/secret-classes.yaml` (instance granularity for `SC-OBJECT-STORAGE`).
- [ ] **35-AC-6** `age --decrypt` of the downloaded bundle **with a wrong passphrase** fails; `age` header inspection shows exactly one `scrypt` stanza with log₂N ≥ 18; the bundle has no X25519 stanza.
- [ ] **35-AC-7** **ADR-0012 decryptability test:** `./bootstrap exec --clean-room -- sc-bootstrap recovery-bundle verify --full` exits 0, decrypting every tier-1 file with only the bundled cluster key (`n₁` = `git ls-files 'gitops/clusters/<cluster>/*.sops.yaml' | wc -l`) and every tier-1 and tier-2 file with only the offline recovery identity; a valid `Attestation` object appears in `attestations/`.
- [ ] **35-AC-8** Every tier-1 file lists exactly the two recipients of `.sops.yaml`, every file under `secrets/offcluster/<cluster>/` lists exactly the recovery recipient, and every file under `secrets/ci/<cluster>/` lists exactly {`SC-AGE-CI`, recovery} and **not** the cluster recipient (`sops filestatus`/metadata inspection via `secrets-lint` S-3; review SEC-4).
- [ ] **35-AC-9** Cluster-key rotation drill on the live cluster: `./bootstrap rotate age-cluster` completes; during the run, `kubectl get events -A --field-selector reason=DecryptionFailed` stays empty; afterwards `flux-system/sops-age` holds exactly one identity whose public key is the new tier-1 recipient, and bundle `vN+1` lists it in `age_recipients.cluster`.
- [ ] **35-AC-10** `kubectl get secrets -A -l strange.company/secret-class -o json | jq '[.items[] | select(any(.data[]?; . == ""))] | length'` = 0, and `kubectl create secret generic t --from-literal=k= -n dr-tests` with label `strange.company/secret-class=SC-PG-DR-MARKER` is denied by `sc-secret-nonempty`.
- [ ] **35-AC-11** Version re-verification: a fixture Secret encrypted with the toolbox's pinned `sops` decrypts via the pinned kustomize-controller in k3d (Kustomization Ready), and `sops --decrypt` in the toolbox decrypts a fixture produced by the previous toolbox version.
- [ ] **35-AC-12** Version re-verification: with `sops-age` holding two identities (old and new), kustomize-controller decrypts files encrypted only to the old and only to the new recipient (k3d).

Checks and alerts:

- [ ] **35-AC-13** `kubectl -n dr-tests get cronjob -l strange.company/check -o name` includes `restore-test-recovery-bundle`, `recovery-bundle-currency`, `recovery-key-attestation`, `secret-rotation-age` and `secret-scan-history`; a manual `kubectl create job --from=cronjob/<each>` completes with exit 0 on a healthy install.
- [ ] **35-AC-14** Induced failure: appending a ledger entry `generation+1` for `SC-RUNNER-ADMIN` without sealing makes `recovery-bundle-currency` exit 1 and `SCRecoveryBundleOutdated` appear in `GET /api/v2/alerts` within `failAfter + 2m`; sealing a new bundle (`rotate refresh-bundle`) clears it on the next run.
- [ ] **35-AC-15** Induced failure: replacing `LATEST.json.sha256` with a wrong digest (test bucket) makes `restore-test-recovery-bundle` exit 1 with evidence naming `sha256_mismatch`.
- [ ] **35-AC-16** A ledger entry with `rotated_at` older than the class `maxAge` (test install) makes `secret-rotation-age` exit 1 and fires `SCSecretRotationOverdue`.
- [ ] **35-AC-17** No check Pod mounts any Secret other than `s3-recovery-reader`, `s3-dr-tests-results` (evidence writes) and, for `secret-scan-history`, `forgejo-sc-backup`: `kubectl -n dr-tests get cronjob -l strange.company/check -o json | jq` over `.spec.jobTemplate.spec.template.spec.volumes[].secret.secretName` returns only those names.

Destroy safety:

- [ ] **35-AC-18** After `./bootstrap destroy` in a lab, `s3://<prefix>-recovery/<cluster>/bundles/LATEST.json`, every bundle version, `generations.json` and `attestations/` still exist, and `git ls-files secrets/offcluster/<cluster>` is unchanged (SPEC §114).

## 24. Explicit non-goals

- HashiCorp Vault, cloud KMS, External Secrets Operator, Sealed Secrets (ADR-0012 alternatives rejected).
- Hardware security modules for the release key or age keys in MVP (an operator MAY keep the recovery identity on a hardware-backed store).
- Automatic rotation of operator-supplied third-party credentials (provider, Cloudflare, GitHub, model keys) — they are rotated by procedure with alerts.
- Rewriting Git history to remove leaked secrets.
- Shamir splitting of the passphrase among several custodians (possible later; single operator assumed in MVP).

## 25. Open questions

1. **Webhook secret ownership**: resolved by CD-18 — organization-level webhooks declared in `forge.yaml` with the register's `SC-RUNNER-WEBHOOK`; the Runner only verifies. Owner: 16/26.
2. **k3s token file path** (blocking: no). Plan 07 uses `infra/secrets/k3s-token.sops.yaml` with two recipients; 35-R-3 requires `secrets/offcluster/<cluster>/k3s-token.sops.yaml`, recovery recipient only. Owner: 07/35 at consolidation.
3. **Plans 15/16 "SOPS + bundle"** for `SC-AUTHENTIK-SECRET-KEY`, `SC-FORGEJO-APP-SECRETS`, `SC-FORGEJO-SSH-HOST-KEY` (blocking: no). Resolved by 35-R-4 (SOPS only). Owner: 35.
4. **Plan 19 local staging of the cluster age key** in `.bootstrap/secrets/` conflicts with plan 50 §10.2 rule 1 (blocking: no). **Resolved (CD-08):** tmpfs `/run/sc/secrets/` only; plan 19 aligned. Owner: 19/50.
5. **Per-agent Woodpecker credentials**: resolved — plan 20 registers per-agent tokens (`SC-WOODPECKER-AGENT/ci`, `/build`); the system value stays server-only. Residual: labels are not bound to tokens (plan 34 R-21). Owner: 20.
6. **Woodpecker server signing key**: registered as `SC-WOODPECKER-SIGNING` (plan 20 describes it as the "server signing key" without an ID; plan 20 should adopt the ID). Owner: 20.
7. **Distinct operator kubeconfig**: resolved (review AUTH-7) — plans 07/50 issue per-operator 30-day certificates; the register's `SC-K3S-ADMIN-KUBECONFIG` now describes that identity and `k3s.yaml` stays node-local. Owner: 07/50.
8. **Operator self-approval** (blocking: no; review DR-26): rotation PRs are opened by `sc-admin` (§14.5.1) so a single operator can approve them; if Forgejo branch protection is later changed to forbid admin-authored PRs, the procedure must change. Owner: 35/16.
9. **Catalogue summary of `SCRecoveryBundleVerifyFailed`** says "decrypt-and-inventory"; the in-cluster check cannot decrypt (the passphrase is never in the cluster). The decrypt test is the quarterly `verify --full` whose freshness is `SCRecoveryKeyAttestationOverdue`. Plan 42 should adjust the summary text. Blocking: no.
