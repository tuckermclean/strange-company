# ADR-0102: Paid-resource guard by plan-JSON allowlist, version-controlled price catalogue and digest-bound confirmation

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 03

## Context
SPEC §14 requires validation of configuration, credentials, products, node count, region, SSH material and expected cost before creating paid infrastructure; the intended paid footprint printed before first creation; and fail-closed behavior on unexpected paid-resource creation. Provider code paths and OpenTofu modules change over time, and a bug in either could order more or different machines than intended. OVH VPS orders carry a monthly commitment and are not instantly reversible (plan 04).

## Decision
- The **only** mutation path for compute is `tofu apply <saved planfile>`. Before apply, the plan's machine-readable form (`tofu show -json`) is evaluated by a guard against a per-module allowlist (`TofuPlanGuard`: allowed providers, resource types, actions, maximum counts, product attribute matched against the catalogue, region and SSH-key attribute rules, replace forbidden for paid types). Anything not allowed fails closed (exit 4).
- Expected cost comes from a **version-controlled price catalogue** (`InfraPriceCatalog`, with `verifiedAt` freshness rules, no currency conversion, `max(catalogue, live)` when a live price exists). Unknown or stale prices fail closed. The check is `monthly_total ≤ MAX_MONTHLY_COST`; the committed minimum is printed.
- The operator confirms the footprint by typing a token containing the first 8 hex digits of the plan digest (`create <cluster> <D8>`); the digest is recomputed immediately before apply and must match.
- An orphan scan before every apply blocks if provider resources with this installation's naming exist outside state, so a crash can never lead to a duplicate order.
- Object storage counts as paid infrastructure for SPEC §14 (usage-billed, and Object Lock makes objects undeletable for their lock period): the bucket set, lock periods and principals are printed and confirmed with `storage <cluster>` before the first bucket is created (`plan.storage-confirm`, BOOT-12).
- Storage and destroy modes use their own guards: the storage guard never allows `delete`; the destroy guard allows only `delete` on compute types.

## Consequences
- Adding a provider means adding guard files and catalogue entries; guard fixtures recorded from real plans are part of CI.
- A provider plugin upgrade can change plan JSON shapes and break the guard (fail closed) until fixtures are updated.
- Prices must be maintained by PR; a stale catalogue blocks paid installs after 180 days unless explicitly accepted per run.

## Alternatives rejected
- Trusting provider code to create only what it should: no independent check.
- Provider-side budget alerts: after-the-fact and not uniform.
- A cost API at runtime only: unavailable for some providers and not reproducible.
- A `--yes` flag: removes the digest binding between what was shown and what is applied.

## Verification
F09 gate (plan 03): 03-AC-3..11 — table-driven guard fixtures each yield their specific code; wrong or missing D8 exits 5 with zero fake creates; orphan scenario yields no duplicate order. P02 gate: footprint printed before the first apply event; journal confirmation digest equals the applied plan digest.
