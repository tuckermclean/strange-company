# Procedure: Release signing key rotation and emergency revocation

**Owning plan:** plans/23-supply-chain.md (class `SC-RELEASE-SIGNING`, registered by plan 35; ADR-0502)
**SPEC references:** §37, §38, §113
**Severity / urgency:** routine (yearly) · sev1 for `#emergency-revocation`
**Expected duration:** routine 30–60 min plus one release; emergency 1–3 h including re-signing
**Requires break-glass:** no (PR flow and `SC-WOODPECKER-ADMIN` minted by the toolbox); emergency may require the Runner emergency stop (`SC-RUNNER-ADMIN`)

## When to use

- Routine: yearly (`SCSecretRotationDue` for `SC-RELEASE-SIGNING`), custodian change.
- `#emergency-revocation`: the private key or its password may be exposed (Woodpecker database or backup leak, build-lane agent compromise, `SC-WOODPECKER-AGENT` leak, operator workstation compromise), or an unexpected signature by the active key is found.
- `#investigate`: `SCUnsignedImageRunning` fired.

## Preconditions and required material

- Toolbox; recovery passphrase (tier-1b SOPS files under `secrets/ci/<cluster>/`); your Forgejo credentials.
- Woodpecker admin access through `./bootstrap exec -- sc-bootstrap ci-secrets apply` (mints `SC-WOODPECKER-ADMIN` for ≤ 1 h).
- Current keys: `security/cosign/trusted-keys.yaml` (exactly one `active`).

## Safety checks before acting

1. No release workflow running (Woodpecker UI, build lane idle); do not tag during the rotation.
2. Routine rotation never marks the old key `revoked`, only `retired` (deployed images signed by it must keep verifying).
3. A key must be committed to `trusted-keys.yaml` on `main` **before** anything it signs is promoted (verification uses base-revision keys).

## Steps

### Routine rotation

1. `./bootstrap exec -- sc-bootstrap supply-chain rotate-signing-key --stage add` → generates key `sc-release-<YYYY><letter>` (cosign ECDSA P-256, random 32-byte password) in the toolbox tmpfs; writes the new key into `secrets/ci/<cluster>/release-signing.sops.yaml`; adds `security/cosign/<id>.pub`; in `trusted-keys.yaml` adds the new key with `status: active` and changes the old key to `status: retired` (both effective at merge); opens PR `rotate/release-signing-<ts>`.
   *Expected:* exit 75 with the PR URL.
2. Review: the PR diff touches only `security/cosign/**` and `secrets/ci/<cluster>/release-signing.sops.yaml`. CI green; merge (one approval from `sc-admins`, `security-review` label).
3. `./bootstrap exec -- sc-bootstrap ci-secrets apply` → updates Woodpecker secrets `sc_cosign_key`/`sc_cosign_password` to the new key. *Expected:* `updated: sc_cosign_key, sc_cosign_password`.
4. Cut a release candidate tag (`v<next>-rc.1`). *Expected:* `sc-release publish` signs with the new key id (pipeline log `signed with key_id=sc-release-<new>`); promotion PR CI `verify-promotion` passes with the new key; Runner harness check passes for any new harness digest after merge.
5. Confirm that previously deployed digests still verify: `./bootstrap exec -- sc-release verify-running` exits 0 (retired keys are accepted).
6. Ledger entry: `./bootstrap exec -- sc-bootstrap secrets record SC-RELEASE-SIGNING --reason schedule`.

### emergency-revocation

1. **Stop the use of the key:** `./bootstrap exec -- sc-bootstrap ci-secrets apply --delete sc_cosign_key,sc_cosign_password` (no release can sign). If a malicious harness image may already be promoted, engage the Runner emergency stop `workers` (`budget-emergency-stop.md`).
2. **Revoke in Git:** PR setting the compromised key `status: revoked` with `revokedAt` = the start of the exposure window and `reason`; same PR adds a new key as `active` (generated as in routine step 1). Merge with `sc-admins` approval. From the merge on, the Runner (ConfigMap `sc-cosign-trusted-keys` reload ≤ 60 s), promotion CI and the daily check reject signatures by the revoked key.
3. **Find what the key signed:** `./bootstrap exec -- sc-release audit-registry --key <revoked id> --since <window start>` lists digests and tags; compare with `security/attestations/**` (digests signed before the window and committed through reviewed promotion PRs are expected).
4. **Re-sign or rebuild:** for every digest currently referenced in Git (`versions/internal-images.lock.yaml`, `toolbox/image.lock`, `policy/harnesses.yaml`):
   - built and committed **before** the exposure window: re-sign the same digest with the new key (`sc-release resign --digest <d>` from a protected release workflow `release.yaml` with `action: resign`, tag `v<current>-resign.1`) and commit the new `signature.json` by the generated promotion PR;
   - built **during** the window or of uncertain origin: rebuild from `main` with a new release tag; the promotion PR replaces the digest.
   Merge the promotion PR(s); Flux rolls workloads to verified digests.
5. **Clean the registry:** delete tags/digests signed by the revoked key during the window that are not referenced in Git (record them first in the incident evidence).
6. Rotate co-located credentials if the leak source was the build lane or Woodpecker: `SC-REGISTRY-PUSH`, `SC-FORGEJO-BOT-TOKENS/sc-release`, `SC-GHCR-PUSH`, `SC-WOODPECKER-AGENT` (`credential-compromise.md#ci-and-supply-chain`).
7. Lift the emergency stop after `verify-running` passes.

### investigate

For `SCUnsignedImageRunning`: read the check evidence (`s3://<prefix>-evidence/checks/supply-chain-signatures/<ts>.json`): it lists the pod, namespace and digest. 1. Is the digest in a Git lock? No → something deployed outside GitOps: treat as an incident (`credential-compromise.md#cluster-and-node`), record and delete the workload. 2. In Git but signature invalid → was its key revoked? Follow `#emergency-revocation` step 4 for that digest. 3. Signature verification failed transiently (registry down) → rerun the check job after `SCForgejoDown` clears.

## Verification

- `trusted-keys.yaml` on `main`: exactly one `active` key; the compromised key `revoked` (emergency) or old key `retired` (routine).
- `./bootstrap exec -- sc-release verify-running` exits 0.
- Promotion CI on a test branch fails for a fixture signature by the revoked key (`sc-release verify-promotion` exit 10).
- `kubectl -n renovate create job --from=cronjob/supply-chain-signatures scs-$(date +%s)` exits 0; `SCUnsignedImageRunning` resolved.

## Rollback / abort

- Routine, before step 3: close the PR; nothing changed. After step 3 but before any release: re-apply the old key with `ci-secrets apply --from-ref <previous commit>` and revert the PR.
- Emergency: no rollback of a revocation.

## Evidence to record

Key ids (old, new), PR URLs, exposure window, the audit-registry listing, re-signed and rebuilt digests, deleted registry tags, release tags used.

## Follow-ups

- Emergency: post-incident review of how the key leaked; update plan 34 residual risks R-14/R-15 if needed.
- Routine: calendar the next rotation (≤ 1 year).
