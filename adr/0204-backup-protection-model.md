# ADR-0204: Backup protection model — versioning everywhere, no version deletion from the cluster, Object Lock where available, fail closed when weak

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 14

## Context
SPEC §29 says the cluster must not be the sole failure domain for critical backup data. A compromised node or namespace holds object-storage credentials (CNPG, Longhorn, k3s, bundle jobs). If those credentials can permanently delete object versions or rewrite bucket lifecycle, the cluster *is* the failure domain for its own backups. S3-compatible providers differ widely in what their credentials can express (R2 has no versioning; B2 keys that may delete can delete versions; generic providers may offer only one key).

## Decision
- **Versioning `Enabled` on every bucket.** A provider without versioning is unsupported.
- **In-cluster credentials** never hold `DeleteObjectVersion`, bucket-configuration writes or governance bypass. Writers that never need deletion (bundles, evidence, restore-test results) also lack `DeleteObject`.
- **Retention is enforced only by bucket lifecycle** (noncurrent windows: 14 d for pg/Longhorn/bundles, 30 d for etcd and Forgejo objects), set with the off-cluster `admin` credential.
- **Object Lock** with default retention (14 d; 7 d for etcd; 365 d for recovery bundles) on backup buckets where the provider supports it: `GOVERNANCE` when credentials are scoped, `COMPLIANCE` when the cluster must share the admin key.
- The capability suite computes a **protection level** for every system-of-record bucket (`scoped`, `locked`, `scoped+locked`, `separate-account`, `weak`). `weak` **fails bootstrap closed** unless the operator explicitly sets `S3_ALLOW_WEAK_PROTECTION=true`, which is reported and permanently alerted.

## Consequences
- In `scoped` mode a fully compromised cluster can at worst add delete markers and new versions; history survives at least the noncurrent window, and locked versions survive their lock period.
- In `shared` mode the cluster holds the account key. It can then rewrite lifecycle rules and purge every version older than the lock period, and delete unlocked, reconstructible buckets (`bootstrap-seed`, `telemetry`). Therefore (revised after review STO-2/BOOT-3): every in-cluster-written system-of-record bucket, including `forgejo-objects` and `evidence`, is COMPLIANCE-locked in shared mode, and `tofu-state` and `recovery` must live in a separate account whose key never enters the cluster, or bootstrap fails closed (`OBJSTORE_STATE_UNPROTECTED`). Protection in shared mode is the lock period (14 d), not the noncurrent window.
- Some providers are excluded (R2) or need more operator work (B2, existing mode).
- COMPLIANCE locks cost storage for at most their lock period; nobody can shorten them, which is the point.

## Alternatives rejected
- Trusting provider credentials as configured by the operator: untestable and usually over-privileged.
- Client-side deletion protection (e.g. append-only proxies): new software on the critical path.
- A second replication bucket at another provider: doubles cost and configuration; may be added by the operator later.

## Verification
Plan 14 14-AC-2, 14-AC-3, 14-AC-5, 14-AC-6 (negative permission tests), 14-AC-12 (drift detection).
