# ADR-0503: ValidatingAdmissionPolicy parameters are Git-generated ConfigMaps; fail closed; staged enforcement for upstream-controlled namespaces

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 37 (consumed by 18, 23, 26, 28, 48, 54)
**Refines:** ADR-0014.

## Context
ADR-0014 chooses PSA + native VAP. The worker policy needs data that changes with ordinary releases: the harness image allowlist, the largest resource profile, identities, CI limits and per-namespace image lists for privileged/baseline namespaces. Hard-coding them in CEL would make every harness release a policy edit; reading them from a mutable object without integrity would let a mistake or an attacker loosen admission. Upstream charts (Longhorn, monitoring) may create pods whose shape changes between versions, and a Deny-mode surprise during seed would block control transfer.

## Decision
1. **Namespace-bound** policies take parameters from ConfigMaps `flux-system/sc-admission-<family>` **generated** by `sc-gitops admission-params` from `policy/harnesses.yaml`, `policy/resource-profiles.yaml`, `versions/*.lock.yaml` and `gitops/platform/admission/params.yaml`. They are committed; F11 fails if a fresh generation differs. Promotion PRs regenerate them in the same commit as a harness digest change.
2. Every policy has `failurePolicy: Fail`; every binding with parameters uses `parameterNotFoundAction: Deny`. Lists are newline-separated strings split in CEL. **Cluster-wide policies take no parameter** (review SEC-6): the generator writes their constants into the policy CEL, so a missing ConfigMap can never deny node, controller or Flux writes cluster-wide (which would also block Flux from re-creating the ConfigMap). Cluster-wide policies exclude `system:node:*`, `kube-node-lease` and finalizer/status-only updates by the garbage collector, TTL and Job controllers through `matchConditions`. Parameter ConfigMaps are applied in a Flux layer (`policies-params`) before the bindings.
3. Strange Company–controlled namespaces (`agent-jobs`, `ci-jobs`, `ci-build`, `hermes`, `strange-company`) bind with `[Deny, Audit]` from the first apply. Policies that constrain pods created by upstream controllers (`sc-images-digest-pinned` outside SC namespaces, `sc-longhorn-images`) start `[Warn, Audit]` and are promoted to `[Deny, Audit]` by PR after 7 days without audit hits.
4. Policy messages carry stable codes `ADM-<NN>`; tests assert codes.
5. Defense in depth over RBAC: identity-scoped policies (`sc-runner-scope`, `sc-untrusted-sa-no-mutation`, `sc-woodpecker-agent-scope`, `sc-zero-rights-subjects`) duplicate what RBAC already denies.

## Consequences
- A harness release cannot be admitted before its parameters are merged; skew between layers causes fail-closed infrastructure failures, never a bypass.
- Deleting or renaming a parameter ConfigMap denies matched requests only in that policy's namespace; nodes stay Ready and Flux restores it (tested, 37-AC-5).
- `sc-namespace-guard` and Flux drift detection protect parameters and labels from non-Flux edits.

## Alternatives rejected
- A custom parameter CRD: typed, but adds a CRD to the seed set for little benefit.
- Kyverno/Gatekeeper: ADR-0014.
- Hand-maintained parameters: drift between the Runner's registry and admission.

## Verification
37-AC-1 (CEL features on the pinned minor), 37-AC-2 (generation drift), 37-AC-4 (escape matrix), 37-AC-5 (missing params deny), 37-AC-8, 37-AC-40 (promotion gate).
