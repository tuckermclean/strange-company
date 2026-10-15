# ADR-0100: OpenTofu state topology — separate storage and compute root modules, local→remote migration, off-cluster restore test

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 06

## Context
SPEC §15 requires OpenTofu state that survives the bootstrap machine, lives outside the cluster in S3-compatible storage, and has versioning, locking, encryption, restricted credentials and a recovery procedure, with encryption material treated as black-start material. Temporary local state is allowed only until remote state can be initialized. SPEC §114 separates compute lifecycle from durable recovery lifecycle, and §109 requires repeatable, alertable restore tests. The state bucket does not exist on a fresh install, so something must create it before remote state can be used.

## Decision
- Two root modules with separate state objects: `infra/tofu/storage` (all object-storage buckets of plan 14, each with `prevent_destroy`) and `infra/tofu/compute/<module>` (provider compute only). An optional `edge` module exists only if plan 09 chooses OpenTofu for DNS/tunnel.
- In `S3_BUCKET_MODE=create`, the storage module runs first with an **encrypted local** state, creates the buckets including `<prefix>-tofu-state`, then migrates its own state into that bucket (`tofu init -migrate-state`) and deletes the local file in the same run. The compute module only ever uses the remote backend.
- Backend: S3 with native lockfiles (`use_lockfile`, OpenTofu ≥ 1.10; verify). State and plan encryption (`pbkdf2` + `aes_gcm`, `enforced = true`) with the passphrase `SC-TOFU-ENCRYPTION` supplied only through the subprocess environment.
- Locking is proven per installation by a conformance test (conditional `PUT If-None-Match: *` semantics, concurrency, versioning, a hand-written lock honored by `tofu`). An S3 provider that fails it is unsupported for state; there is no override.
- The `tofu-state` credential is never the `admin` key and never a key the cluster holds; `shared` credential mode requires a second object-storage account for the `tofu-state` and `recovery` buckets (`S3_STATE_*`, plan 14 14-R-2a), or an explicit, permanently reported and alerted `S3_ALLOW_UNPROTECTED_STATE=true` (BOOT-3, SPEC §15 "restricted credentials").
- Only modules that exist are tested and verified (`storage` iff bucket mode `create`, `compute` iff the provider uses OpenTofu).
- The full decrypting restore test (`./bootstrap state verify`) runs off-cluster. The cluster receives only a non-secret result object and HEAD metadata through a read-only credential, so an overdue or failed test is alertable without any state material in the cluster.

## Consequences
- "Destroy compute" plans only the compute module, so a bucket can never appear in a destroy plan (plus the storage guard forbids `delete`, ADR-0102).
- An S3 provider without conditional writes cannot host state; the operator must choose another one (or another provider for the `tofu-state` bucket, if plan 14 allows per-purpose endpoints).
- A monthly operator action (`state verify`) is required; the in-cluster alert reminds the operator.

## Alternatives rejected
- One root module for buckets and compute: a destroy plan would include buckets, protected only by `prevent_destroy`.
- Operator pre-creates the state bucket by hand: an extra manual step contrary to SPEC §130; still supported as `bucketMode: existing`.
- DynamoDB-style lock tables or HTTP/Kubernetes backends: unavailable on most S3-compatible providers, or inside the cluster being recovered.
- Running the decrypting restore test in-cluster: would place the state passphrase in the cluster.

## Verification
P01 gate (plan 06): 06-AC-1..12 — conformance passes on the SeaweedFS `s3-emulator` (ADR-0205) and on the real provider; an If-None-Match-stripping proxy makes bootstrap fail closed with no remote state written; the raw state object is an encrypted envelope; migration leaves no local file; `tofu-state` credential cannot delete object versions.
