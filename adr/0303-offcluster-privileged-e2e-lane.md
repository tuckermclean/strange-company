# ADR-0303: Tier-E end-to-end tests run in an off-cluster lane; their result gates releases, not merges

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 52 (consumed by 19, 20, 23, 54, and every plan with an `e2e-*` target)
**Relation to ADR-0018:** refines its sentence "GitHub Actions workflows remain only as advisory checks on the mirror" for one lane. Recorded as a deviation for consolidation.

## Context
Several required tests need a container runtime socket or `/dev/kvm` (plan 01 tier E): the k3d chart install/upgrade/uninstall/reinstall gates [SPEC §116], the seed-to-Flux transfer test [SPEC §34], NetworkPolicy enforcement for [SPEC §105], the component suites (`e2e-cnpg`, `e2e-forgejo`, `e2e-authentik`, `e2e-mirror`), `e2e-bootstrap-fake` and the QEMU lab (`e2e-lab`).

Woodpecker's pipelines run as pods in `ci-jobs` on the production node, under PSA `restricted`. Running k3d or QEMU there needs privileged pods, a nested container runtime and several extra GiB of memory on a node that SI-01/ADR-0020 already show to be tight. SPEC §38 forbids privileged DinD for workers, and the same reasoning applies to CI on the production node.

## Decision
1. **No privileged CI on the production cluster.** No Woodpecker lane ever runs tier-E targets. `ci-jobs` stays `restricted`. The `ci-build` exception (plan 23) is for image builds only.
2. **Off-cluster e2e lane** = GitHub Actions hosted Linux runners executing the same containerized Make targets (ADR-0018 execution model) in `.github/workflows/e2e.yaml`, against:
   - the GitHub repository while it is the origin (Phase 1 until P06);
   - the read-only GitHub mirror afterwards. Plan 17 mirrors `main`, `release/*` and tags only.
3. **Triggers:**
   - Phase 1: PRs (the aggregate job `e2e-gate` is a required check) and pushes to `main`.
   - After P06: pushes of mirrored tags; a daily schedule at 03:17 UTC that runs only when `main` changed since the last successful run; `workflow_dispatch`.
4. **Authority:**
   - Before P08 the lane is part of the interim trusted CI (ADR-0018).
   - After P08 its results are **release-gating evidence**. Plan 23's release pipeline on a protected tag (Woodpecker, build lane) polls the GitHub Actions API for an `e2e` run on the tag's commit with conclusion `success`. It waits up to 90 minutes, then fails the release. It uses the read-only `SC-GITHUB-PROBE` credential, which plan 17 is asked to extend with `Actions: read`.
   - A failed or stale lane on `main` raises `SCCIOffclusterLaneFailing` through the plan 17 platform probe (§21 of plan 52).
   - Merges are **not** gated by the lane after P08. Its trust domain is external, and PR branches are not mirrored.
5. **Optional in-house lane.** An operator may register an off-cluster Woodpecker agent (docker backend, label `sc-lane=e2e`, on hardware they provide) that runs the same targets from a protected-mode `e2e.yaml`. It replaces the GitHub lane as the release gate when `policy/ci.yaml` `offclusterLane.provider: woodpecker-agent`. This is post-MVP and not required.

## Consequences
- Regressions caught only by tier-E tests surface after merge (within about 24 h on `main`), and always before a release. That is the accepted cost of keeping the production node unprivileged.
- GitHub Actions minutes are consumed: about 45 min per run. Daily-if-changed plus tags is roughly 1,400 minutes per month in the worst case. Private repositories on the free plan have 2,000 (cost risk, plan 52 §25).
- `/dev/kvm` availability on hosted runners is version-sensitive (verify). If it is absent, `e2e-lab` falls back to a documented operator-run lane before each release (plan 52 §14.5).

## Alternatives rejected
- Privileged `ci-e2e` namespace on the production node: memory contention with the platform and a privileged surface next to production.
- vcluster/envtest for everything: envtest covers admission and RBAC (and is used in tier V), but not NetworkPolicy enforcement, Longhorn, CNPG or Helm lifecycle behavior on a real kubelet.
- Making the GitHub lane merge-gating after transfer: PR refs are deliberately not mirrored (plan 17), and doing so would make an external service part of the merge path (SPEC §32 spirit).

## Verification
P08 gate (plan 52): `.github/workflows/e2e.yaml` runs green on the cutover commit. A release dry run (`sc-release gate --dry-run <sha>`) against a commit without a successful e2e run exits non-zero with `OFFCLUSTER_LANE_MISSING`.
