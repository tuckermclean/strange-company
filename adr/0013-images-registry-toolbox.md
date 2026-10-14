# ADR-0013: Internal images in the Forgejo OCI registry; locally buildable, checksum-pinned toolbox

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13

## Context
Strange Company images (Runner, Gateway, verifier, worker and harness images, toolbox) need a registry. SPEC §38 forbids workers holding registry-push credentials, and §37 requires immutable references. The bootstrap toolbox is needed *before* any Strange Company–operated registry exists, including during black start.

## Decision
- Internal images are pushed to the **Forgejo OCI registry** (`git.<domain>/strange-company/<image>@sha256:...`), whose blobs live in S3 `forgejo-objects`, **only** by trusted CI on protected refs (plan 23).
- The **toolbox** image is defined by `toolbox/Containerfile`. Every downloaded tool is pinned by version **and sha256**, and the base image by digest. `./bootstrap` first tries the digest recorded in `toolbox/image.lock` from the configured public registry (GHCR by default; publication is a release step of plan 23). If that is unavailable, it **builds locally** from the Containerfile and verifies the tool checksums. The tool versions are the same either way. A locally built image's digest may differ, and the bootstrap report records which path was used.
- Upstream third-party images are referenced by digest from their upstream registries. Plan 23 decides whether to mirror critical ones into the Forgejo registry; that would not help black start.

## Consequences
Black start depends only on GitHub plus upstream registries plus object storage. During a full outage, internal images come back once Forgejo's S3-backed packages are restored (plan 45 orders this).

## Alternatives rejected
- A separate in-cluster registry (Harbor or Zot): another stateful service. Forgejo already provides OCI packages.
- Requiring the operator to install tools: forbidden by SPEC §7.

## Verification
F03 gate: the toolbox builds from scratch in CI, and every tool's checksum is verified; the pull path and the build path produce identical `tool --version` output.
