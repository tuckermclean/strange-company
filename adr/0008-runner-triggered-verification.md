# ADR-0008: The Runner triggers RED/GREEN verification pipelines and pulls evidence; protected paths are immutable on agent branches

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13

## Context
SPEC §50 and §54 require *trusted CI* to verify RED and GREEN. Woodpecker runs pipeline definitions taken from the commit under test, so an agent branch could edit `.woodpecker/*.yaml` or `.strange-company/validation.yaml` (for example `test: true`) and manufacture evidence. Pushing evidence from CI to the Runner would also need a secret inside a pipeline that runs untrusted code.

## Decision
1. **Protected paths.** The verification policy defines paths an agent branch may never change: at minimum `.woodpecker/**`, `.strange-company/**`, `AGENTS.md`, `policy/**`, `contracts/**`, CODEOWNERS-equivalents and the repository's declared test-infrastructure paths. Before triggering any verification, the Runner diffs `base_commit..head` and refuses evidence for any branch touching a protected path (`PROTECTED_PATH_VIOLATION`). Changes to protected paths can only arrive through a human-reviewed PR, never an autonomous task.
2. **Trusted pipeline config.** Verification uses the pipeline and validation configuration **from the task's base revision**. Item 1 guarantees it is byte-identical at the head. Plan 20 additionally evaluates Woodpecker's configuration extension, which serves pipeline config from the base revision, as defense in depth.
3. **Runner-triggered.** The Runner (Woodpecker identity `sc-runner`) triggers the verification pipeline through the Woodpecker API for a specific commit SHA, with variables `SC_TASK_ID`, `SC_PHASE` (`red` or `green`) and `SC_EXPECTED_SHA`. Verification pipelines receive **no secrets**.
4. **Pulled evidence.** The verification step runs `sc-verify` (a digest-pinned image) around the repository's declared test command and emits a schema-valid `red-result` or `green-result` document between markers in its own step log. The Runner fetches that log through the Woodpecker API, checks that the pipeline was created by `sc-runner`, ran the expected SHA, came from the expected repository and used the trusted config, then validates the document. It stores the document in S3 `evidence` and records its digest in `sc-pg`. The Runner, not the worker, records the state transition, with actor `ci:woodpecker/<pipeline-number>`.
5. **Residual risk.** Code under test (tests or implementation) runs inside the verifier step and could in principle forge output. This is the inherent limit of running untrusted code. It is mitigated by: an immutable test manifest and hashes (plan 22); classification computed from structured reports plus exit codes; independent review that sees red and green evidence and the diff (SPEC §55); and protected-path and test-tamper detection. It is documented as accepted residual risk in plan 34.

## Consequences
- Woodpecker push-triggered pipelines on `agent/*` may still run as ordinary CI. They are advisory and never count as RED or GREEN evidence.
- The Runner needs a Woodpecker API token with permission to create and read pipelines on managed repositories (`SC-WOODPECKER-RUNNER`, registered in plan 35).

## Alternatives rejected
- CI pushes evidence to the Runner with a pipeline secret: untrusted code in the same pipeline could exfiltrate the secret.
- Trusting Woodpecker commit statuses as evidence: statuses carry no RED-validity semantics, and a status alone cannot prove a *meaningful* failure.

## Verification
C04 gate: the fake scenarios `fake-test-tamper`, a protected-path edit and a pipeline-config edit are all rejected; `fake-red-valid` yields `RED_VERIFIED` with an evidence digest; a manually triggered pipeline by a non-Runner identity is ignored.

## Amendments (Stage-0 consolidation)
- **2026-10-13, refined by ADR-0301 and ADR-0302.** The Woodpecker API cannot create a pipeline for a specific commit; it only accepts a branch plus variables (verified against 3.x). Item 3 is therefore realized as follows. The Runner registers a verification *intent* (expected SHA, base SHA, nonce) and triggers a manual pipeline on `agent/<task-id>`. Evidence from a pipeline whose commit differs from the expected SHA is refused (`EVIDENCE_COMMIT_MISMATCH`). Item 2 ("trusted pipeline config") is realized by the **exclusive** Woodpecker configuration extension `sc-ci-config`, which serves the `verify` workflow from the task's base commit. It is no longer only "evaluated as defense in depth".
