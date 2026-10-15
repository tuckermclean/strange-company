# Agent instructions

Advisory instructions for any coding agent working in this repository (SPEC section 42). This file is itself
a protected path. Nothing here is policy that exists only here: every rule under "Enforced rules" maps to a
key in `.strange-company/policy.yaml` (or `policy/verification.yaml`), which the platform enforces.

## Commands

- `make verify` runs every tier-V check. On a host without the toolchain it re-executes inside the pinned `ci`
  image through `./bootstrap exec --image ci`. Run it before you hand work back.
- `make test` runs the Go tests. Narrow it with `make test GO_TEST_RUN=<regexp>` (test names) and
  `GO_TEST_PKGS=./src/internal/<area>/...` (repository-relative packages). Results land in `out/test/`.
- `make layout-check`, `make dag-check` and `make contracts` run the repository static checks alone.

## Where code lives

- `src/` is one Go module (`strange.company/sc`, standard library first). Binaries are `src/cmd/<name>/` (only
  `main` packages); library code is `src/internal/<area>/`. Test data sits next to the package in `testdata/`;
  cross-package fixtures are in `test/fixtures/`.
- `contracts/` holds JSON Schema and OpenAPI contracts, `plans/` the design, `adr/` the decisions,
  `procedures/` the operator runbooks, `mk/` the Makefile fragments, `toolbox/` the seed image definition.
- The layout is normative in `plans/01-repository.md` section 6.1. A directory exists only if a plan owns it.

## Test-first rule

Every behavior change starts with a RED commit `test(<task-id>): ...` that adds failing tests, then a
`feat(<task-id>): ...` or `fix(<task-id>): ...` commit makes them pass. History is linear. Commit subjects
follow plan 01 section 6.4.

## Never

- Never commit secrets, `bootstrap.env`, age keys or kubeconfigs.
- Never edit generated files by hand; regenerate them with the owning `make` target.
- Never add symlinks, executable bits (other than `bootstrap`, `toolbox/bin/*`, `test/e2e/**/run`) or tracked
  files over 2 MiB.

## Enforced rules

- Do not change the protected paths listed below; changes arrive only through human-reviewed pull requests. <!-- policy: protectedPaths -->
- A RED commit may touch only test paths, fixtures and test infrastructure. <!-- policy: testPaths -->
- Existing tests are never modified or deleted to make an implementation pass. <!-- policy: testProtection -->
- Declared stubs belong only in stub-capable source paths. <!-- policy: stubPaths -->
- Changes to the code that judges future work need elevated review. <!-- policy: elevatedPaths -->
- Do not push to or rewrite protected branches. <!-- policy: protectedBranches -->

## Protected paths

This list is enforced by platform policy (`.strange-company/policy.yaml`, plan 01 section 13.2), not by this
file. A branch whose diff touches any of these is rejected (`PROTECTED_PATH_VIOLATION`).

```text
SPEC.md  PLAN.md  STATUS.md  LICENSE
AGENTS.md  **/AGENTS.md  CLAUDE.md  **/CLAUDE.md  .claude/**  **/.claude/**  .codex/**  **/.codex/**  .opencode/**  **/.opencode/**
CODEOWNERS  **/CODEOWNERS  .forgejo/**  .gitea/**  .gitmodules  **/*.sops.yaml
.strange-company/**  .woodpecker/**  .github/**  .sops.yaml  .gitattributes  .gitignore
Makefile  mk/**  bootstrap  bootstrap.env.example
contracts/**  policy/**  context/company/**  plans/**  procedures/**  adr/**
versions/**  toolbox/**  harnesses/**  forge/**  tools/stage0/**
infra/tofu/**  infra/catalog/**  infra/profiles/**  infra/bootstrap/**  infra/objstore/**
gitops/clusters/**  gitops/layers/**  gitops/components/**  gitops/profiles/**
gitops/platform/strange-company/generated/**  gitops/platform/admission/**  gitops/clusters/*/flux-system/**
secrets/**  security/**
src/.golangci.yaml  test/fixtures/secrets-scan/**  test/fixtures/age/**  test/contracts/**
```
