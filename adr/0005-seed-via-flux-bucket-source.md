# ADR-0005: Seed through Flux from an object-storage snapshot, then switch the source to Forgejo

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13

## Context
SPEC §34 says Flux cannot source Forgejo before Forgejo exists, so a minimal seed phase is needed, and seeded resources must be adoptable by Flux "without duplicate-resource chaos". §35 defines the completion conditions. The common approach is to `helm install` the seed set from the bootstrap machine and let Flux adopt it later. That relies on helm-controller release adoption semantics and on two code paths rendering identical values, and drift between them causes exactly the chaos SPEC warns about.

## Decision
- Seeding uses **Flux itself**. The bootstrap orchestrator:
  1. applies the committed, version-pinned Flux controller manifests (ADR-0016) with `kubectl apply --server-side`;
  2. creates the `sops-age` decryption Secret and a read-only object-storage credential Secret;
  3. uploads a snapshot of the **exact local commit** (the `git archive` tree of `HEAD`) to `s3://<prefix>-bootstrap-seed/<install>/<commit-sha>/`, plus a manifest file recording the SHA;
  4. creates a Flux `Bucket` source pointing at that prefix, and the root `Kustomization` `flux-system` with `path: ./gitops/clusters/<cluster>/seed`. That path is a kustomization listing **only the seed-set layer Kustomizations**, which are the very same objects used at steady state.
- **Seed set:** cert-manager, CNPG operator (+ barman-cloud plugin), Longhorn, Traefik, cloudflared (tunnel mode), `forgejo-pg`, Forgejo, plus the namespaces, PriorityClasses, admission policies and NetworkPolicies those depend on. Plan 19 is authoritative.
- **Control transfer** (`flux-bootstrap` phase): after forge-init has pushed the same commit (plus any forge-init commits) to `Forgejo/strange-company`, the orchestrator creates the Forgejo `GitRepository` (read-only deploy key) and patches the root `Kustomization` to `sourceRef: GitRepository/flux-system` and `path: ./gitops/clusters/<cluster>`. The layer objects are identical and the root's inventory is a superset, so nothing seeded is recreated. The `Bucket` source is then deleted, and the seed snapshot is retained for audit under the lifecycle in plan 43.
- The seed→Forgejo transfer test (SPEC §34) asserts that no seeded object's UID changes across the transfer and that no HelmRelease performs an install action after the switch.

## Consequences
- One reconciliation engine and one rendering path, and therefore no adoption step.
- Seeding requires object storage. That is acceptable because object storage is already mandatory at bootstrap (SPEC §29) and must work before any cluster exists (tofu state).
- Black start reuses the same path (seed from the GitHub clone's snapshot), and the recovery overlay selects restore-from-backup bootstraps for CNPG and Longhorn (plan 45).
- The seed-path Kustomization must not include components that need Forgejo-created credentials (Woodpecker OAuth, Runner bot token). Those follow after transfer.

## Alternatives rejected
- `helm install` seed plus helm-controller adoption: two rendering paths, plus adoption semantics that are version-sensitive.
- Sourcing GitHub directly during seed: this needs GitHub write access before Forgejo exists, puts installation commits on GitHub before the mirror is configured, and adds a GitHub availability dependency to bootstrap.
- A temporary in-cluster Git server: custom software for a one-time step.

## Verification
P04 and P06 gates (plan 19): a seed from Bucket reaches Ready; after the source switch, every seeded object's UID and every HelmRelease's revision history show upgrade or no-op only; `flux get all` reports Ready at the Forgejo commit SHA; the Bucket source is absent.

## Amendments (Stage-0 review fixes)
- **2026-10-14, SEED-8 (seed→Flux review).** The switch mechanics are now specified by ADR-0300 and plan 19 §8.5, which supersede the Decision text above where they differ:
  - The seed root path is `./<cluster>/<S>/gitops/clusters/<cluster>/seed`, with root-only substitutions.
  - Cluster-directory content reaches the cluster through the seed layer `cluster-config` (SEED-1), which the `namespaces`/`policies` layers depend on.
  - T2 server-side-applies the committed `gotk-sync.yaml` root. The orchestrator never applies the GitRepository or the deploy-key Secret imperatively (19-R-2).
  - Seed, forge-init and flux-bootstrap are frozen once the source switch has happened (BOOT-2). The pre-switch UID snapshot is uploaded to object storage before the switch (SEED-7).
  - Emergency rollback to the Bucket source suspends post-transfer layers and uses `prune: false` (SEED-14).
