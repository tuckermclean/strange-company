# ADR-0500: Two-tier SOPS layout, a recovery-bundle content rule, and recovery checks that never decrypt

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 35 (consumed by 06, 07, 14, 15, 16, 19, 23, 42, 44, 45, 50)
**Refines:** ADR-0012 (two recipients, recovery bundle).

## Context
ADR-0012 encrypts every Git-resident secret to the cluster age key and the offline recovery key, and keeps a passphrase-encrypted recovery bundle. Some secrets in Git are not consumed by the cluster at all: provider API credentials, the OpenTofu passphrase, object-storage admin/state/recovery keys, the k3s token, break-glass credentials, the Runner admin token. If those were also encrypted to the cluster key, stealing `flux-system/sops-age` would grant authority *outside* the cluster (paid resources, DNS, backups, recovery). Several plans also asked for "SOPS + bundle" copies of ordinary cluster secrets, which would force a new bundle version on every routine rotation. Finally, the alert catalogue expects weekly bundle verification, but the passphrase is never in the cluster.

## Decision
1. **Tier 1** `gitops/clusters/<cluster>/**/*.sops.yaml`: Kubernetes Secret manifests, recipients {cluster, recovery}, `encrypted_regex: ^(data|stringData)$`. **Tier 1b** `secrets/ci/<cluster>/*.sops.yaml`: CI release credentials applied into Woodpecker by the toolbox, recipients {`SC-AGE-CI`, recovery} where `SC-AGE-CI` is held only in the bundle (review SEC-4), never referenced by a kustomization. **Tier 2** `secrets/offcluster/<cluster>/*.sops.yaml`: off-cluster credentials, recipient {recovery} only, never referenced by a kustomization. `.sops.yaml` has per-cluster rules and **no catch-all** (SOPS fails on unmatched paths).
2. **Bundle content rule:** the bundle holds exactly the register classes marked `inRecoveryBundle: true`: the cluster age key, tier-2 material, and credentials needed by pre-Flux phases of `./bootstrap` and `./bootstrap recover` (k3s token, Forgejo admin, seed credentials). Tier-1 secrets are not copied into the bundle; the bundled cluster key and the offline recovery key both decrypt them.
3. **Generation ledger:** every toolbox-performed rotation appends to `s3://<prefix>-recovery/<cluster>/generations.json` (no values).
4. **In-cluster checks never decrypt.** CronJobs in `dr-tests` verify pointer, object digest, age header (one scrypt stanza, work factor ≥ 18), a plaintext inventory (classes and generations, no values or value digests), ledger/inventory agreement, rotation age and attestation freshness. The **decrypt** test is the operator's quarterly `sc-bootstrap recovery-bundle verify --full` (clean-room container, passphrase + offline recovery identity), which writes an attestation object whose age is alerted.

## Consequences
- A leaked cluster age key exposes only in-cluster (tier-1) credentials; off-cluster and release credentials are independent. The cluster key is itself readable by upstream operators with cluster-wide Secret read (plan 34 R-22), which is why nothing beyond tier 1 is encrypted to it.
- Routine rotations of tier-1 secrets need no new bundle version; bundled rotations are tracked and alerted when unsealed.
- Some plans' text ("SOPS + bundle", `infra/secrets/k3s-token.sops.yaml` with two recipients, local staging of the cluster key) is reconciled to this layout at consolidation (plan 35 §10.3).

## Alternatives rejected
- One tier with two recipients for everything: cluster-key compromise would leak provider and recovery authority.
- Storing the passphrase in the cluster to allow automated decrypt tests: it would make the bundle as weak as the cluster.
- Putting every secret in the bundle: bundle churn on every rotation and a larger blast radius per bundle copy.

## Verification
35-AC-5..8 (bundle inventory, scrypt header, clean-room decryptability with each key, exact recipients), 35-AC-13..17 (checks and induced failures), `make secrets-lint` S-3/S-9.
