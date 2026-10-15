# Procedure: Rotate the age identities, attest recovery material, recover from a lost bundle

**Owning plan:** plans/35-secrets.md (ADR-0012, ADR-0500)
**SPEC references:** §98, §111, §112, §113 (rotate age identity)
**Severity / urgency:** routine (cluster key yearly; attestation quarterly) · disaster-adjacent (lost bundle, lost recovery key)
**Expected duration:** cluster key 45–90 min; recovery key 60–120 min; attestation 20 min
**Requires break-glass:** yes — `SC-K3S-ADMIN-KUBECONFIG` (out-of-band Secret `flux-system/sops-age`)

## When to use

- `#cluster-key`: annual schedule (`SCSecretRotationDue SC-AGE-CLUSTER`), custodian change, suspected exposure of `flux-system/sops-age` or of a bundle with its passphrase.
- `#recovery-key`: custodian change, suspected exposure, or loss of the offline recovery identity.
- `#attest`: `SCRecoveryKeyAttestationOverdue`; P10 gate; after any rotation of either key.
- `#lost-bundle`: the passphrase is forgotten or every bundle copy is lost, but the recovery identity survives and the cluster is alive; `#lost-bundle-black-start` for the same during black start; `#both-lost-cluster-alive` when the recovery identity is lost too.
- `#ci-key`: rotation of `SC-AGE-CI` (tier-1b release credentials).

## Preconditions and required material

- Toolbox (`./bootstrap shell`), admin kubeconfig, the recovery passphrase, your Forgejo credentials (PRs).
- For `#recovery-key` and `#attest`: the current offline recovery identity (`AGE-SECRET-KEY-1…`), typed or pasted only into the toolbox prompt.
- Paper or a hardware-backed store ready for any new identity.

## Safety checks before acting

1. All Kustomizations Ready (`flux get kustomizations -A`). A failing layer would hide rotation errors.
2. `recovery-bundle status` = `integrity: ok`, `currency: ok`.
3. **Never remove the old cluster identity from `sops-age` before the rotation PR is merged and every Kustomization is Ready at the merge commit.**
4. **Never destroy the old recovery identity before `#attest` succeeds with the new one.**
5. Do not run two age rotations concurrently (the tool holds the bootstrap run lock).

## Steps

### cluster-key

1. `./bootstrap rotate age-cluster`. The tool opens the bundle and generates K₂. *Expected:* prints the new public recipient `age1…`.
2. **Stage both identities.** The tool patches `flux-system/sops-age` so `age.agekey` contains K₁ and K₂ (two lines). *Check:* `kubectl -n flux-system get secret sops-age -o jsonpath='{.data.age\.agekey}' | base64 -d | grep -c AGE-SECRET-KEY` = 2 (run inside `./bootstrap shell`; do not print the values).
3. **Re-encrypt tier 1** (`gitops/clusters/<cluster>/**` only; tier 1b under `secrets/ci/` is encrypted to `SC-AGE-CI`, see `#ci-key`): `sops rotate --in-place --add-age K₂ --rm-age K₁` on each file and the `.sops.yaml` rule update. *Expected:* `make secrets-lint` passes (S-3 recipients = {K₂, R}).
4. **Seal bundle vN+1 before merging**: `rotated_classes: [SC-AGE-CLUSTER]`, uploaded, confirmed (you re-enter the passphrase), pointer updated.
5. **PR** `rotate/age-cluster-<ts>`: CI green, one approval, merge.
6. **Wait** until every Kustomization is Ready at the merge SHA (`flux get kustomizations -A`), and `kubectl get events -A --field-selector reason=DecryptionFailed` is empty.
7. **Retire K₁:** re-run `./bootstrap rotate age-cluster` (resumes) → `sops-age` contains only K₂; the tool force-reconciles one secret-bearing layer (`flux reconcile kustomization forgejo --with-source`) and checks Ready.
8. Ledger entry; `#attest` (below) within 7 days.
*If the trigger was compromise:* also rotate every tier-1 value (old ciphertexts in Git history decrypt with K₁): `credential-compromise.md#recovery-material`.

### recovery-key

1. `./bootstrap rotate age-recovery`. Prompts: passphrase, then R₁ (type `lost` if it is lost). Generates R₂, prints it once and writes `/blackstart/<cluster>/recovery-identity.txt` (0600).
2. Copy R₂ to paper and to your hardware/password store **now**. Confirm in the tool by typing the last 8 characters of R₂.
3. Tier 1: rewrap to {K, R₂} using K from the bundle. Tier 2 (`secrets/offcluster/<cluster>/`): decrypt with R₁ and re-encrypt to R₂; if R₁ is `lost`, the tool regenerates each tier-2 file from the bundled values (every tier-2 file has a bundled counterpart; the tool lists any that does not and stops).
4. `.sops.yaml` rules updated; PR; merge.
5. Seal bundle vN+1 with `age_recipients.recovery = R₂`.
6. Run `#attest` with R₂.
7. Only after `#attest` passes: destroy every copy of R₁ and delete `/blackstart/<cluster>/recovery-identity.txt` (PF-23 warns while it exists).
*If the trigger was compromise:* rotate every tier-2 and tier-1 value (`credential-compromise.md#recovery-material`).

### attest

1. `./bootstrap exec --clean-room -- sc-bootstrap recovery-bundle verify --full` (fresh toolbox container without `/blackstart` and without `bootstrap.env`).
2. Enter the `SC-OBJECT-STORAGE/recovery` key (from your records) when asked, then the passphrase, then the recovery identity.
3. *Expected:* `bundle: ok (vN)`, `tier1 decrypted with cluster key: n₁ = <count>`, `tier1+tier2 decrypted with recovery key: n₂, n₃`, `offcluster values match bundle: yes`, `attestation written: attestations/<ts>.json`, exit 0.
4. *If the passphrase fails:* go to `#lost-bundle`. *If the recovery identity fails:* go to `#recovery-key` (lost variant). *If a file fails to decrypt with the cluster key:* `sops-decryption-failure.md`.

### lost-bundle

The passphrase is forgotten or no bundle copy is readable, but the recovery identity works.

1. `./bootstrap exec -- sc-bootstrap recovery-bundle rebuild --from-recovery-identity` — prompts R; decrypts `secrets/offcluster/<cluster>/*` and tier 1.
2. The tool rotates the cluster key (the old K lived only in the lost bundle and in `sops-age`; treat it as unknown): it runs `#cluster-key` steps 1–7 using R to read tier 1.
3. It prompts for a **new** passphrase and seals a new bundle (`reason.trigger: passphrase-change`).
4. Run `#attest`.
5. Treat every bundled credential as potentially exposed if a bundle copy could have been stolen with its passphrase (`credential-compromise.md#recovery-material`); otherwise no further rotation is needed.

### ci-key

`SC-AGE-CI` (tier 1b, release credentials; held only in the bundle).
1. `./bootstrap rotate age-ci` (plan 50 target; until it exists: `./bootstrap exec -- sc-bootstrap secrets age-ci rotate`). Opens the bundle, generates C₂.
2. Rewrap every file under `secrets/ci/<cluster>/` to {C₂, R} (`sops rotate --add-age C₂ --rm-age C₁`), update the `.sops.yaml` tier-1b rule, PR (opened as `sc-admin`), merge.
3. Seal bundle vN+1 with C₂; confirm.
4. *If compromised:* the release credentials themselves are exposed — continue with `signing-key-rotation.md#emergency-revocation` and rotate `SC-REGISTRY-PUSH`, `SC-GHCR-PUSH`, `SC-FORGEJO-BOT-TOKENS/sc-release` (`rotate-secrets.md`), then `sc-bootstrap ci-secrets apply`.
*Expected:* `make secrets-lint` S-3 shows {C₂, R} on every tier-1b file.

### lost-bundle-black-start

Black start (`total-cluster-loss.md`) with no usable bundle or passphrase, but with the offline recovery identity R (review DR-15). No cluster exists, so `#cluster-key` cannot be used.
1. In the clean toolbox: `./bootstrap exec -- sc-bootstrap recovery-bundle rebuild --from-recovery-identity --black-start`. Prompts R; decrypts `secrets/offcluster/<cluster>/*` and tier 1 from the GitHub clone.
2. The tool generates a new cluster identity K₂ **offline**, rewraps every tier-1 file to {K₂, R} and updates `.sops.yaml`. These files and `.sops.yaml` are added to the **recovery commit** (plan 50 §13 commit scope must allow `.sops.yaml` and `gitops/clusters/<cluster>/**` during `recover`; plan 45 recovery commit).
3. It prompts for a **new** passphrase and seals a new bundle (with K₂ and the tier-2 values) **before** seed; `./bootstrap recover` then seeds with K₂ as `sops-age`.
4. Tier 1b (`secrets/ci/`) is re-encrypted later by `#ci-key` (needs only R and a new C).
5. After transfer: `#attest`.
*Expected:* seed reaches Ready with no `DecryptionFailed`; `recovery-bundle status` shows the new version.

### both-lost-cluster-alive

Neither bundle/passphrase nor recovery identity is available, but the cluster runs (its `sops-age` still decrypts tier 1).
1. Treat as a disaster-adjacent event: open an incident; engage the emergency stop `workers`.
2. Generate new identities K₂ and R₂ in the toolbox; print R₂ for offline storage.
3. Run a break-glass one-shot Job in `flux-system` (`./bootstrap exec -- sc-bootstrap sops-age rewrap-job --new-recipients K₂,R₂`, image = toolbox digest, mounts `sops-age`) that runs `sops updatekeys`/`rotate` on a checkout of `main` and pushes branch `rotate/age-both-lost` as `sc-admin`; open the PR, merge.
4. Tier 2 and tier 1b **cannot** be decrypted any more: regenerate every tier-2 and tier-1b value at its issuer (provider, DNS, object storage, OpenTofu passphrase via plan 06 fallback, k3s token, break-glass, Runner admin, release credentials) with `rotate-secrets.md`, writing new files to {R₂} / {C₂, R₂}.
5. Install K₂ in `sops-age` (`#cluster-key` steps 2 and 7), seal a new bundle under a new passphrase, then `#attest`.
*Expected:* `make secrets-lint` passes; all recipients are new; attestation written.

## Verification

- `recovery-bundle status`: new `bundle_version`, `age_recipients` equal to the current `.sops.yaml` rules, `currency: ok`.
- `make secrets-lint` passes on `main`.
- `kubectl -n dr-tests create job --from=cronjob/recovery-key-attestation rka-$(date +%s)` exits 0; `SCRecoveryKeyAttestationOverdue` resolved.
- No `DecryptionFailed` events (`kubectl get events -A --field-selector reason=DecryptionFailed`).

## Rollback / abort

- `#cluster-key` before step 5: `./bootstrap rotate age-cluster --abort` removes K₂ from `sops-age`, discards the branch; the sealed bundle vN+1 still contains K₁ as `previous`, so it remains usable; seal a clean version with `refresh-bundle`.
- After merge but before step 7: revert the PR; both identities remain in `sops-age`, so nothing breaks.
- `#recovery-key`: until R₁ is destroyed, abort by reverting the PR and re-sealing with R₁ as recipient.

## Evidence to record

New public recipients, PR URLs, bundle versions, the attestation object key, the `DecryptionFailed` event check output, and (recovery key) who holds the new offline copies (names, not locations of secrets).

## Follow-ups

- Update the operator custody record (who holds paper copies).
- If any step hit an unlisted failure, improve this procedure by PR.
