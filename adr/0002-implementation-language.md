# ADR-0002: Go for Strange Company services and CLIs; POSIX sh bootstrap wrapper

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13

## Context
Strange Company builds the Agent Runner, Model Gateway, bootstrap orchestrator, verifier (`sc-verify`), worker adapter (`sc-worker`) and fake harness. These are Kubernetes controllers and API clients that must be small, static and easy to test deterministically. SPEC §7 forbids requiring local tool installs beyond Git and Docker or Podman.

## Decision
- All Strange Company–authored services and CLIs are written in **Go**, in one module rooted at `src/` with multiple `cmd/` binaries.
- `./bootstrap` is a small **POSIX sh** wrapper. It resolves the pinned toolbox image (pull by digest, or build locally per ADR-0013) and runs `sc-bootstrap` inside it with the repository mounted.
- Ansible playbooks and OpenTofu modules are declarative assets driven by `sc-bootstrap`. They are not reimplemented in Go.

## Consequences
One toolchain, static binaries in distroless or minimal images, first-class `client-go`, and well-understood testing (`go test`, envtest, k3d). The prior implementation also used Go (`eb02a6c~1:control-plane/`), so the team knows it.

## Alternatives rejected
- Python everywhere: Ansible is already Python, but distributing services and the orchestrator in Python adds runtime weight and packaging drift.
- Rust: stronger guarantees, but slower iteration and a smaller Kubernetes ecosystem for this scope.

## Verification
F01 gate: `make test` runs `go test ./...` in the toolbox or CI image with no host Go install.
