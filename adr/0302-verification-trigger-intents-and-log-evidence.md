# ADR-0302: Verification pipelines are triggered on the agent branch against a registered intent, and evidence travels as a framed step-log block

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 20 (consumed by 21, 22, 26)

## Context
ADR-0008 requires the Runner to trigger a verification pipeline "for a specific commit SHA" with variables, and to pull evidence from the step log. The Woodpecker API (3.x, verified 2026-10-13 against `server/model/pipeline.go`) creates manual pipelines from `PipelineOptions{branch, variables, message}`. **It cannot target a commit.** The pipeline runs the branch head as Woodpecker resolves it at creation time. The configuration-extension payload carries `pipeline.{event, branch, commit, sender, author}`. `variables` is serialized only when present (`omitempty`), and is not documented as part of the payload.

## Decision
1. **Intent registration.** Before triggering, the Runner records a verification **intent** in `sc-pg`: `intent_id`, repository, branch `agent/<task-id>`, `expected_sha`, `base_sha`, `phase` (`red`|`green`), `task_id`, `execution_id`, a 128-bit `nonce`, `created_at` and `expires_at = created_at + 15m`.
2. **Trigger on the agent branch.** The Runner checks through the Forgejo API that the head of `agent/<task-id>` equals `expected_sha`. It then calls `POST /api/repos/{repo_id}/pipelines` with `{branch: "agent/<task-id>", variables: {SC_TASK_ID, SC_PHASE, SC_EXPECTED_SHA, SC_INTENT_ID}}` as Woodpecker user `sc-runner` (`SC-WOODPECKER-RUNNER`). It records the returned pipeline `number` on the intent.
3. **The configuration comes from the intent, not from variables.** For `event == manual` on `agent/*`, `sc-ci-config` looks up the intent by `(repository, branch, commit)` on the Runner's internal endpoint. If the intent is present and unexpired, it serves the `verify` workflow from `base_sha` and injects `SC_PHASE`, `SC_TASK_ID`, `SC_EXPECTED_SHA`, `SC_BASE_SHA`, `SC_INTENT_ID` and `SC_EVIDENCE_NONCE` as step environment. It then reports `{intent_id, config_sha256, config_revision}` back to the Runner. Without an intent, a manual run gets the ordinary untrusted configuration and never the `verify` workflow.
4. **Commit mismatch refuses evidence.** If the pipeline's `commit` differs from `expected_sha` (the branch moved between the check and creation), the Runner cancels the pipeline, records `EVIDENCE_COMMIT_MISMATCH` (failure class `infrastructure`) and does not use its log.
5. **Evidence framing.** The verifier (`sc-verify`, plan 22) writes exactly one block to its step's stdout:
   ```text
   ::sc-evidence-begin v1 intent=<intent_id> nonce=<nonce> phase=<red|green> sha=<40hex> bytes=<n> sha256=<64hex>::
   <base64 (RFC 4648, standard alphabet, padded) of the JSON document, wrapped at 76 characters>
   ::sc-evidence-end v1 intent=<intent_id>::
   ```
   The decoded document is a `red-result` or `green-result` (plan 22 contracts). Its size is at most 262144 bytes.
6. **Pulled, identity-checked evidence.** The Runner fetches the pipeline and the `sc-verify` step log through the Woodpecker API. It accepts the block only if:
   - the pipeline number is the one it created and recorded;
   - `event == manual`, `branch == agent/<task-id>` and `commit == expected_sha`;
   - the intent was served with the expected `config_sha256`;
   - the step `sc-verify` of workflow `verify` exited 0;
   - exactly one framed block with the intent's nonce exists;
   - the length and sha256 match, and the document validates against its schema.
   The document is then stored in S3 `evidence` and its digest is recorded in `sc-pg`.

## Consequences
- No dependence on an undocumented "run commit X" API, or on `variables` being present in the extension payload.
- Branch races cost a retry, never wrong evidence.
- Pipelines that `sc-runner` did not create, or that no intent covers, can never produce evidence. A human who re-runs a verification pipeline gets advisory output only.
- Residual risk (ADR-0008 item 5): code under test runs with the same uid as `sc-verify` and can read the nonce. A forged block alongside the genuine one is refused as ambiguous. A forged block that replaces the genuine one, after killing the verifier, is the documented residual risk, mitigated by test-manifest hashing and review (plans 22, 34).

## Alternatives rejected
- A dedicated immutable `sc-verify/<task>/<n>` branch per run: Forgejo cannot delete protected branches, and unprotected ones give no extra guarantee over the commit check.
- Evidence through Woodpecker artifacts or a push to the Runner: Woodpecker 3 has no artifact store, and a push needs a pipeline secret (ADR-0008).
- Commit statuses as evidence: they carry no RED-validity semantics (ADR-0008).

## Verification
C04 gate (plans 20, 21, 22): `fake-red-valid` yields `RED_VERIFIED` with a stored evidence digest; a branch moved after the trigger yields `EVIDENCE_COMMIT_MISMATCH` and a retry; a manual pipeline created by a human on the same branch is ignored; a log with two blocks carrying the nonce yields `EVIDENCE_AMBIGUOUS`; a block whose sha256 does not match yields `EVIDENCE_CORRUPT`.
