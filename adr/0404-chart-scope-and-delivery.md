# ADR-0404: The `strange-company` chart carries workloads, not enforcement; Flux installs it from Git and releases are published as signed OCI artifacts

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 54

## Context
SPEC §115 lists the likely chart scope, including NetworkPolicies, ResourceQuota, LimitRange and worker policies, and says platform dependencies should stay separate releases. SPEC §85 says Runner validation must not be the only defense; Kubernetes must independently enforce worker restrictions. Plans 36, 37 and 51 already render NetworkPolicies, ValidatingAdmissionPolicies (with generated parameters) and quotas in the `namespaces`/`policies` Flux layers, which are in the seed set. SPEC §116 requires chart uninstall and reinstall tests. Plan 18 sources the chart for the cluster from the `flux-system` GitRepository; plan 23 publishes signed artifacts.

## Decision
1. **Scope.** The chart renders Strange Company workloads and their wiring: Agent Runner, Model Gateway, egress proxy, fake model upstream (until R02), Hermes integration (Deployment, PVC, skills, dashboard route), ServiceAccounts (including `agent-worker`), Roles/RoleBindings for its own components, Services, configuration ConfigMaps and the Runner JWKS ConfigMap.
2. **Exclusions.** Enforcement objects (NetworkPolicies, VAPs and bindings and their parameters, ResourceQuota, LimitRange, PSA namespace labels, PriorityClasses), scrape/alert objects (ADR-0550), Secrets (plan 35), the emergency-stop ConfigMap (ADR-0452), `sc-pg` (plan 12) and every PersistentVolumeClaim (Hermes mounts plan 13's stable-named `hermes-data` triplet as an existing claim; review STO-5, ADR-0203) are outside the chart. Uninstalling or breaking the chart therefore never weakens worker restrictions or deletes data.
3. **Delivery.** The cluster's HelmRelease takes the chart from the `flux-system` GitRepository at the reconciled revision (`reconcileStrategy: Revision`), so the running chart is exactly the merged commit and black start needs no chart registry. Trusted CI additionally packages each new chart `version`, pushes it to the Forgejo OCI registry and signs it (plan 23) as the release record.
4. **Policy files** reach the Runner and Gateway as generated, committed ConfigMaps (`sc-policy` with `__`-encoded paths, `sc-company-context`), checked for freshness in CI, in the same Flux layer as the chart.

## Consequences
- SPEC §115's list is satisfied by the system as a whole, not by the chart alone; recorded as an interpretation for `spec-issues.md`.
- Chart tests must install the enforcement layers first (k3d lane), which also proves the chart's pods comply with them.
- Two artifacts exist per release (Git tree and OCI package); CI asserts they are identical for the tagged version.

## Alternatives rejected
- Chart renders its own NetworkPolicies/VAPs/quotas: uninstall, a failed upgrade or a rollback could remove enforcement; duplicates plans 36/37/51.
- Cluster consumes the OCI chart: adds a registry dependency to black start and a promotion step for every chart change without improving explainability.

## Verification
54-AC-4 (no enforcement kinds rendered), T9 uninstall test (worker pod still denied after uninstall), 54-AC-21 (signed artifact for the deployed version).
