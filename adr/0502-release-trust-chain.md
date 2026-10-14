# ADR-0502: Release trust chain — protected-tag releases, plugin-only release credentials, key-based cosign, Git-committed signatures, human-merged promotion

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 23 (consumed by 20, 26, 28, 34, 35, 37, 50, 52, 54)

## Context
SPEC §37 requires signing, a verification policy and traceability from running digest to acceptance criteria; SPEC §38 and §4.2 forbid workers from holding push or signing credentials and CI from deploying. ADR-0301 already guarantees that only protected-mode configurations reach the build lane and can reference secrets. Remaining questions: when to publish, how to confine release credentials inside the build lane, which signing scheme to use for a private registry, and how promotion PRs are verified by CI that has no secrets.

## Decision
1. **Releases run only on protected tags matching `releaseTagPattern`** whose commit is reachable from `main` (or a `forge.yaml`-listed `release/*` branch) and **which were pushed by an allowed human or bot identity** (activity feed or Runner push record; deploy keys never qualify), after the off-cluster gate (ADR-0303). Release configuration is served from `main`. Cron workflows on `main` never publish. P11 fails if Forgejo lets a deploy key create `release/*` or push a protected tag (review SEC-1).
2. **Credential confinement:** release credentials are Woodpecker secrets with events `[tag]` and image filter `sc-release`; build steps receive none. `sc-release` refuses (exit 13) unless the Forgejo API confirms the tag is protected, points to the pipeline commit and is reachable from a protected branch. Admission (`sc-ci-build-release-steps`) allows release-secret env only on the `sc-release` image without an entrypoint override.
3. **Signing:** cosign **key-based** (ECDSA P-256, `SC-RELEASE-SIGNING`), no upload to the public transparency log; public keys and their status (active/retired/revoked) in `security/cosign/trusted-keys.yaml`. Each image gets a signature plus SPDX SBOM, SLSA v1 provenance, vulnerability-report and trace attestations.
4. **Signatures are also committed to Git** (`security/attestations/<image>/<digest>/`) by the promotion PR, so verification works offline and from Git alone (black start, trace queries).
5. **Promotion:** `sc-release` (bot) opens `promote/<tag>` changing only lock files, harness digests, generated admission params and attestations. CI (untrusted mode, no secrets) runs `sc-release verify-promotion` using the `sc-release` image pinned at `main` and keys from the base revision. A human merges; Flux deploys. CI never deploys.
6. **Verification policy:** MVP at promotion CI, by the Runner for harness images before dispatch, by V-26 (bootstrap mode at P12, strict after P11, CD-19) and by a daily check; admission-time verification is post-MVP. The Runner additionally posts a required `strange-company/release-provenance` status on promotion PRs, checking that the provenance names a real tag pipeline on the registered build agent (review SEC-11), because signatures alone cannot distinguish a stolen-key promotion PR.
7. **Credential exposure:** if Woodpecker inlines `from_secret` values into Pod specs (23-AC-3), release credentials move to an out-of-band Kubernetes Secret mounted only into the `sc-release` pod (review SEC-3); tier-1b SOPS files are encrypted to `SC-AGE-CI`, not to the cluster key (review SEC-4).

## Consequences
- Every deployed internal digest is signed, attested and traceable through committed provenance, promotion PR, merged PRs and task records.
- Merged changes reach production only at tag time (deliberate human release decision).
- A compromised Woodpecker server or build-time dependency can still produce signed malicious images (plan 34 R-14, R-15); human review of promotion PRs and the vulnerability gate are the compensating controls.

## Alternatives rejected
- Keyless (Fulcio/OIDC) signing: needs an OIDC identity for Woodpecker and public log uploads that leak private image metadata.
- Building and publishing on every push to `main`: more load on a single node and no off-cluster e2e gate per build.
- Direct deployment from CI (image update automation or `kubectl`): violates SPEC §4.2/§33.
- Kubernetes-Secret-mounted release credentials: depends on unverified Woodpecker backend features; kept as the documented fallback (plan 23 open question 1).

## Verification
23-AC-3, 23-AC-4, 23-AC-5, 23-AC-8, 23-AC-9, 23-AC-11, 23-AC-17.
