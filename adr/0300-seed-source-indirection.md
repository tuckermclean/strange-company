# ADR-0300: Layer Kustomizations reference their source through root-substituted variables

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 18 (used by 19)

## Context
ADR-0005 seeds the cluster through Flux from a `Bucket` snapshot of commit `S`, then switches the source to the Forgejo `GitRepository` at the same commit. SPEC §34 requires that Flux take over seeded resources "without duplicate-resource chaos". If each layer `Kustomization` named its source literally, the seed tree and the steady tree would need two copies of every layer object. Those copies could drift, and the switch would then change more than the source.

Two facts constrain the design:
- A Flux `Bucket` artifact keeps the full object key, including the configured prefix, under the artifact root. This was verified in source-controller `fetchIndexFiles`. Paths inside a Bucket artifact therefore begin `./<cluster>/<S>/`, while the same paths inside a `GitRepository` artifact begin `./`.
- The root `Kustomization` can substitute variables into the objects it applies (`postBuild.substitute`).

## Decision
- Every file in `gitops/layers/` is a single `Kustomization` whose source and path are written with three **root-only variables**:
  - `spec.sourceRef: {kind: ${SC_SOURCE_KIND}, name: flux-system}`
  - `spec.path: ${SC_SOURCE_ROOT}/gitops/platform/<component>/overlays/${SC_PROFILE}`
- The root `Kustomization` `flux-system` is the only object that sets these variables:
  - steady (committed `gotk-sync.yaml`): `SC_SOURCE_KIND=GitRepository`, `SC_SOURCE_ROOT=.`, `SC_PROFILE=<profile>`;
  - seed (created imperatively by `sc-bootstrap`, plan 19): `SC_SOURCE_KIND=Bucket`, `SC_SOURCE_ROOT=./<cluster>/<S>`, same `SC_PROFILE`.
- The seed path `gitops/clusters/<cluster>/seed` is a strict subset of the steady path. It includes `flux-system/` but removes the committed root `Kustomization` object with a kustomize `$patch: delete`. Otherwise the Bucket-sourced root would overwrite itself with the Git-sourced definition before transfer.
- The root-only variables may appear only in `gitops/layers/*.yaml` (F11 check `root-vars-scope`). Seed-set charts never come from the `flux-system` source (F11 check `seed-chart-source`), so the switch cannot change a chart artifact.

## Consequences
- Control transfer is a change to three substituted values plus the root's `sourceRef` and `path`. The rendered content of every layer is byte-identical in both modes (F11 check `seed-steady-identity`, 18-R-2).
- Rollback before the Bucket is deleted is the same operation in reverse (plan 19 §14.3).
- Profile expansion (plan 47) changes `SC_PROFILE` in the same three places as the profile include paths. F11 check `profile-consistency` enforces this.

## Alternatives rejected
- Duplicate layer files for seed and steady: two rendering paths, which is the drift ADR-0005 exists to avoid.
- A `GitRepository` pointing at an in-cluster file server during seed: custom software for a one-time step (ADR-0005).
- Repackaging the snapshot as an OCI artifact (`OCIRepository`): it removes the prefix problem, but it needs a registry before Forgejo exists, or a push to an external registry at bootstrap.

## Verification
- F11: 18-AC-4 (`seed-steady-identity`) and 18-AC-2 (a negative fixture that uses `${SC_SOURCE_ROOT}` outside `gitops/layers`).
- P04: 19-AC-4 (the Bucket artifact keeps the prefix; re-verified for each Flux minor).
- P06: 19-AC-8 and 18-AC-11 (every seed layer ends at `sourceRef.kind == GitRepository`, path `./gitops/...`, with unchanged UIDs).
