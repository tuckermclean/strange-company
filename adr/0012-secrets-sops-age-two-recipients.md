# ADR-0012: SOPS + age with two recipients and a passphrase-encrypted recovery bundle

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13

## Context
SPEC §98 requires SOPS + age for Git-resident secrets, with the age identity held outside the cluster as black-start material. SPEC §111 lists the off-cluster material needed after total loss. A single age key held only in-cluster plus on one laptop is a single point of loss.

## Decision
- Two age identities are generated at bootstrap:
  - `SC-AGE-CLUSTER`: installed as `flux-system/sops-age` for kustomize-controller decryption, and also inside the recovery bundle.
  - `SC-AGE-RECOVERY`: an **offline** identity held only by the operator (printed and written to the local black-start directory). It is never placed in the cluster.
- `.sops.yaml` encrypts every Git-resident secret to **both** recipients. Rotating the cluster key never risks losing decryptability, because the recovery key still decrypts everything.
- A **recovery bundle** (`contracts/recovery-bundle.schema.yaml`) is an age passphrase-encrypted (scrypt) archive. It holds the cluster age identity, the OpenTofu state passphrase, break-glass credentials, the k3s token, provider and DNS credentials and the object-storage recovery credentials. It is stored (a) in the operator's local black-start directory, (b) in S3 `<prefix>-recovery` with object versioning, and (c) wherever else the operator chooses. The passphrase (`SC-RECOVERY-PASSPHRASE`) is never stored by Strange Company. Bootstrap refuses to continue until the operator confirms the passphrase has been recorded (re-entry check).
- The minimal operator-held material for black start is therefore: `bootstrap.env` (or equivalent credentials), the recovery passphrase, and either the recovery age identity or the bundle.

## Consequences
Black start works with GitHub plus object storage plus the passphrase. Losing the cluster key is a rotation, not a disaster. The operator burden is explicit and enforced at bootstrap.

## Alternatives rejected
- Sealed Secrets: the key lives in-cluster, and recovery requires the controller's key backup. That is weaker.
- External Secrets with a cloud KMS or Vault: a new external dependency and a black-start chicken-and-egg problem.

## Verification
P10 gate: the restore-test decrypts every SOPS file in the repository using only the recovery bundle and passphrase in a clean container, and using only the offline recovery identity.
