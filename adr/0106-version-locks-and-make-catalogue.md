# ADR-0106: One lock location per pinned artifact kind; the Make target catalogue is the CI interface

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 01

## Context
SPEC §9 requires tool versions under version control; §37 and §116 require pinned dependencies and immutable references. ADR-0018 requires CI gates to be containerized Make targets that run identically on GitHub Actions and Woodpecker. Parallel plans had started naming different lock files for overlapping content (`versions.lock`, `versions/k3s.lock.yaml`, `toolbox/tools.lock.yaml`).

## Decision
- Each pinned artifact kind has exactly one lock location (plan 01 §6.3): `versions/platform.lock.yaml` (Flux, charts, upstream images, Kubernetes minor), `versions/k3s.lock.yaml`, `versions/compat.yaml`, `versions/internal-images.lock.yaml`, `versions/lab-images.lock.yaml`, `toolbox/tools.lock.yaml`, `toolbox/requirements.txt`, `toolbox/image.lock`, OpenTofu `.terraform.lock.hcl`, `src/go.mod`/`go.sum`, harness digests in `policy/harnesses.yaml`, and SHA-pinned CI actions/images in the CI definitions.
- `make locks-check` validates every lock file against its kind schema, rejects floating references anywhere in deployable or executable definitions, and checks cross-file consistency (Flux CLI = Flux controllers; kubectl minor = k3s minor = platform Kubernetes minor; compatibility matrix covers the pinned minor). Online re-verification runs nightly and never blocks merges.
- The Make target names in plan 01 §7.1 are the only interface CI products call. Every target runs inside the pinned `ci` image (the Makefile re-executes itself through `./bootstrap exec --image ci` when not already in a container). Tiers: V (every PR, offline after cache warm-up), E (container socket or KVM), O (credentials or money; never a merge gate).

## Consequences
- Renovate needs custom managers for each lock kind; every update is an ordinary PR running the full catalogue.
- Plans 18/19's `versions.lock` is read as `versions/platform.lock.yaml`.
- Developers and CI need only Make, Git and a container engine; `./bootstrap exec --image ci -- make <t>` works without host Make.

## Alternatives rejected
- A single monolithic lock file: mixes owners and update cadences, and conflicts constantly.
- CI-product-specific pipeline logic: two definitions of "green" (ADR-0018).
- Pinning in-place in manifests without a lock index: no cross-file consistency checks and no single Renovate surface.

## Verification
F01 gate (plan 01): 01-AC-3..7 — `make verify` identical on laptop and GitHub Actions; offline run with `--network none`; `locks-check` mutation fixtures each fail with a specific rule ID.
