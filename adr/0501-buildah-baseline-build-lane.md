# ADR-0501: Trusted image builds use buildah (chroot isolation, vfs storage) in a `baseline` build lane

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 23 (consumed by 20, 37, 51)

## Context
Trusted CI must build container images (SPEC §36) without Docker/containerd sockets, privileged DinD or privileged pods (SPEC §38, ADR-0303 rule 1), on a production node whose namespaces run PSA `restricted` (ADR-0014). Options checked on 2026-10-13 (re-verify at implementation):
- rootless BuildKit needs `Unconfined` seccomp and AppArmor plus `--oci-worker-no-process-sandbox` (PSA `privileged`); rootful BuildKit needs `privileged: true`, even inside a user namespace;
- kaniko was archived upstream in 2025; maintained forks (chainguard-dev, osscontainertools) exist but publish no freely available official image;
- buildah (maintained by Red Hat) supports `--isolation=chroot` with `vfs` storage as root in an unprivileged container with default capabilities.

## Decision
- Builds run in namespace **`ci-build`** (Woodpecker build lane, ADR-0301) at PSA **`baseline`** (enforce) with `restricted` audit/warn.
- The builder is **buildah**, digest-pinned, `buildah build --isolation=chroot --storage-driver=vfs --layers=false --platform=linux/amd64`, output to an OCI archive in the workflow's `ci-workspace` PVC. The step runs as uid 0, without `privileged`, with seccomp `RuntimeDefault` and capabilities limited to CHOWN, DAC_OVERRIDE, FOWNER, FSETID, KILL, SETGID, SETUID, SETFCAP, SYS_CHROOT. It receives no credentials.
- Plan 37 VAPs (`sc-ci-build-pod`, `sc-ci-build-release-steps`, `sc-ci-workspace`) bound the lane: only allowlisted images, root only for the builder, release-secret env only on the `sc-release` image without entrypoint override.
- **Fallback (pre-approved):** if 23-AC-1 fails on the pinned k3s/containerd/kernel, the osscontainertools kaniko fork, built from source in the interim lane and digest-pinned, replaces buildah under the same namespace and rules.
- **Hardening:** `hostUsers: false` is required for build pods once 23-AC-6 proves it works.

## Consequences
- No privileged pod exists on the production node for builds; the build lane is still a weaker zone than `ci-jobs` (root in container), reachable only by protected-mode workflows.
- vfs storage costs disk (≈ 2–3× image size): within the 10Gi build-lane cap (CD-06) the release workflow builds and publishes one image at a time and frees the vfs storage between images (plan 23 §14.3, 23-AC-19).
- Builds are amd64 only (no emulation).

## Alternatives rejected
- BuildKit (rootless or rootful): needs `privileged` PSA or privileged containers.
- kaniko from an unofficial image: unclear provenance of the builder itself.
- `ko`-only builds: covers Go images but not the harness and toolbox images that need `RUN` steps.
- Building off-cluster on GitHub Actions: an external trust domain would hold the signing key.

## Verification
23-AC-1 (RUN-step build under baseline with the exact security context; privileged variant denied), 23-AC-6 (user-namespace probe), 37-AC-4 escape cases E34/E35.
