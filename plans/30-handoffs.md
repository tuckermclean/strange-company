# 30 — Handoffs

**Status:** Stage-0 plan (draft)
**DAG nodes:** none owned. Contributes to F07 (schema embedded in `sc-worker`, plan 27), C02 (ingestion checks, plan 26), C07 (context projection, plan 25), C03/C09 (PR body, plan 55) and C10 (plan 21 suite).
**Related plans:** [25 context](25-context.md), [26 Agent Runner](26-agent-runner.md), [27 worker contract](27-worker-contract.md), [21 TDD workflow](21-tdd-workflow.md), [24 Hermes](24-hermes.md), [29 state machine](29-task-state-machine.md), [34 security](34-security.md), [55 forge workflow](55-forge-workflow.md)
**Contracts:** owns `contracts/handoff.schema.yaml` (`$id https://strange.company/contracts/handoff/v1alpha1`); consumed by `worker-result.schema.yaml` (27), `mcp-surface.schema.yaml` (26), `context.schema.yaml` sections `tester_handoff`/`implementer_handoff` (25)
**ADRs:** relies on 0003 (Hermes untrusted), 0008, 0017; introduces none

> Every section below is mandatory (SPEC §121).

## 1. Purpose

Workers collaborate through durable artifacts, not live chat [SPEC §70]. The **handoff** is the structured note every worker leaves for whoever comes next: what it did, what remains, which role it recommends, its open questions and the risks it sees [SPEC §69]. This plan defines the handoff's fields and limits, how it is sanitized and scrubbed, how it flows into later workers' context and into Hermes, how it is persisted (sc-pg, S3, PR body), and what it can never do (drive a state transition or override authoritative context).

## 2. Requirements derived from SPEC.md

1. Every worker result supports a structured handoff with at least `what_i_did`, `what_remains`, `recommended_next_role`, `questions`, `known_risks` [SPEC §68, §69]. Handoffs are MVP [SPEC §69, §126].
2. Workers collaborate through Git, commits, PRs, task state, results, handoffs, review comments and Hermes; no direct worker chat [SPEC §70, §127].
3. The reviewer receives the implementation handoff [SPEC §55]; reviewers receive handoffs [SPEC §66]; PRs contain the handoff [SPEC §100].
4. Workers do not receive unrestricted Hermes history [SPEC §66]; the tester does not receive a proposed implementation [SPEC §48].
5. Telemetry does not capture full prompts, completions or secret-bearing output [SPEC §94]; no secret in task records or handoffs (master plan §16.4).
6. A model saying something works is not evidence [SPEC §4.3].

Derived requirements:

- **30-R-1** A handoff is non-authoritative: it is labeled as such in every context packet, never parsed for commands, and never changes task state.
- **30-R-2** Every handoff is stored scrubbed; the original unscrubbed text is never persisted anywhere.
- **30-R-3** Handoffs are bounded in size per field and in total, and truncation is explicit.

## 3. Inputs

| Input | Source |
|---|---|
| Harness claim `handoff` | the model writes `claim.json` (plan 27 §14.9) |
| Synthesized handoff | `sc-worker` when the claim is unusable (plan 27 §14.9) |
| Worker result | `POST /v1/worker/result` (plan 26) |
| Known-secret set | Runner (issued tokens, deploy key, gateway token; plan 26 step 7) |

## 4. Outputs

- The scrubbed, normalized handoff stored with the result (`runner.results.body.handoff`, plus `runner.handoffs` row, §6).
- Context sections for later executions (plan 25): `tester_handoff`, `implementer_handoff`, and handoff excerpts inside `prior_attempt_evidence`.
- `agent_result.handoff` for Hermes (MCP).
- The PR body "Handoff" section and the review summary comment (plan 55).
- Metrics (§21).

## 5. Dependencies

**Hard:** none of its own; the schema must exist before F07 (plan 27 embeds it, 27-AC-14).

**Interfaces assumed:**
- Plan 27: `sc-worker` validates the claim's handoff against this schema and `max_handoff_bytes`, and synthesizes the fallback (`what_i_did: "sc-worker: harness ended with <verdict>; no valid claim"`, …), which is valid under this schema.
- Plan 26: ingestion step 7 secret scan covers the handoff; `handoff_redacted` is exposed.
- Plan 25: projection rules (which roles receive which handoffs) and the `non_authoritative` authority label; budgets `tester_handoff`/`implementer_handoff` 32768 bytes each.
- Plan 55: PR body template has a handoff section with the rendering rules of §14.4.

## 6. Owned state

The handoff content is part of the worker result; plan 26 owns `runner.results`. This plan defines one additional logical table (DDL by plan 26):

| Table | Key | Columns |
|---|---|---|
| `runner.handoffs` | `execution_id` | `task_id`, `role`, `generation`, `normalized jsonb` (§14.2), `digest` (sha256 of canonical normalized JSON), `bytes`, `redactions int`, `truncated_fields text[]`, `synthesized bool`, `created_at` |

Written once per execution by the Runner in the ingestion transaction; never updated. The task record points to the latest relevant one (`task.latest_handoff`, plan 29). Retention: indefinite with the task (provenance of what workers claimed; plan 43 may archive >2 years to S3).

## 7. External interfaces

- `contracts/handoff.schema.yaml` (owned).
- MCP `agent_result.handoff` (normalized form) and `handoff_redacted`.
- Context sections (plan 25) and PR body (plan 55).

## 8. Internal interfaces

`sc-worker` → Runner (inside the result) → normalizer (§14.2) → `runner.handoffs` → context compiler / MCP / Forgejo renderer.

## 9. Configuration

| Setting | Source | Default | Notes |
|---|---|---|---|
| `limits.max_handoff_bytes` | worker packet, from Runner config `results.maxHandoffBytes` | 32768 | schema max 65536 (worker-input) |
| per-field maxima | `handoff.schema.yaml` | `what_i_did`/`what_remains` 4000 chars; ≤ 20 questions and risks of ≤ 1000 chars; claims as in schema | changing them is a contract change |
| Context budgets | `policy/roles/*.yaml` `sectionBudgets` (plan 25) | 32768 per handoff section | |
| PR rendering cap | plan 55 | 8192 bytes per handoff in the PR body | longer → truncated with a link to `agent_result` via Hermes |

## 10. Secrets

N/A — no secret class. Handoffs are a *secret-leak surface*: they are scrubbed (§14.3) and a detected secret revokes the execution's credentials (`SECRET_DETECTED`, policy class, plan 26/29).

## 11. Storage

`sc-pg` (`runner.results`, `runner.handoffs`): ≤ 64 KiB per execution; at MVP volumes < 100 MiB/year. The raw result object in S3 (`results/<task>/<execution>/result.json`, plan 26) holds the same scrubbed handoff.

## 12. Resource requirements

N/A — processing happens inside the Runner during ingestion (≤ 5 ms per handoff).

## 13. Security boundary

- **Untrusted content.** A handoff is text written by a model that has read hostile repository content (L-2). It may contain prompt injections aimed at the next worker, at Hermes, or at the reviewer. Controls: it is never executed or parsed for commands; it is delivered in a fenced, labeled `non_authoritative` section (plan 25); role instructions state that non-authoritative content never overrides authoritative content; the tester never receives implementer handoffs (plan 25 forbid list), so an implementer cannot steer test writing.
- **Rendering.** In Forgejo (PR body, comments) handoff text is rendered inside fenced code blocks (no Markdown/HTML interpretation), `@` mentions are neutralized (U+200B after `@`), and URLs appear only as code — so a handoff cannot ping humans, embed images (tracking) or produce clickable links. Hermes receives the JSON (its own injection handling is plan 24's).
- **No state authority.** `recommended_next_role` and claims are advice; the Runner never transitions on them (29-R-1).
- **Size bound** prevents context flooding.

## 14. Happy path

### 14.1 Production

1. The harness writes `claim.json` with `handoff` (role instructions explain each field; plan 25).
2. `sc-worker` validates it (schema, `max_handoff_bytes` on canonical JSON) or synthesizes the fallback; it scrubs known secrets it holds and counts redactions (plan 27).
3. The result is posted; the Runner validates the whole result (worker-result schema with this handoff schema).

### 14.2 Normalization (Runner, ingestion transaction)

1. **Sanitize** every string: Unicode NFC; remove C0/C1 controls except `\n` and `\t`; remove ANSI escape sequences; remove bidirectional override/isolate characters (U+202A–U+202E, U+2066–U+2069) and zero-width joiners except inside code points needed for scripts (only U+200B–U+200D, U+FEFF are removed); trim trailing whitespace; collapse > 3 consecutive blank lines.
2. **Scrub** with the plan 26 step 7 scanner (exact issued secrets + pattern set); each hit → `[REDACTED:<kind>]`; any hit → `SECRET_DETECTED` for the execution (policy), credentials revoked.
3. **Normalize shorthand:** a string question becomes `{text, blocking: (result.state == blocked)}`; a string risk becomes `{text, severity: medium}`.
4. **Consistency checks** (warnings stored on the row, never failures, except where noted): `state: blocked` without `claims.blocked` → `claims.blocked = {reason: missing_information, detail: <first blocking question or what_remains>}` (synthesized, flagged); `state: test_spec_conflict` with `claims.test_spec_conflict` absent → allowed (result `conflict` is authoritative); `recommended_next_role` that the state machine would not allow now → kept, flagged `advice_not_actionable`; `claims.references.commits` not in the task's history → flagged `unknown_commit_reference`.
5. **Truncate** fields over a stricter Runner limit if `results.maxHandoffBytes` was lowered after dispatch (suffix `… [truncated by runner]`, field listed in `truncated_fields`).
6. Store `runner.handoffs` (digest of canonical normalized JSON) and update `task.latest_handoff` when the execution is the latest producing or reviewing one.

### 14.3 Flow into later context (plan 25 projection)

| Handoff of | Delivered to | As |
|---|---|---|
| accepted tester execution (current generation) | implementer, reviewer | `tester_handoff` |
| latest implementer execution | next implementer (retry), reviewer, adjudicating reviewer | `implementer_handoff` |
| failed attempts of the same role and generation | that role's next attempt | inside `prior_attempt_evidence` (what_remains, questions, risks only) |
| reviewer (final review) | next implementer after CHANGES_REQUESTED | via `reviewer_findings` (findings authoritative as review output; handoff text non-authoritative) |
| implementer | **never** to a tester | — |
| any | Hermes | `agent_result.handoff` |

Every delivered handoff section records its source execution and digest (`ContextPacket.sections[].sources`), so a reviewer can see which claim came from which worker.

### 14.4 Persistence in Forgejo (SPEC §100)

The PR body (plan 55 §14.3) has a **Handoff** section: the latest implementer handoff (`what_i_did`, `what_remains`, risks with severity, open questions) and the tester handoff summary, rendered per §13, ≤ 8192 bytes each. The review result is posted as a PR comment containing the reviewer verdict, checks and the reviewer handoff. Earlier handoffs are listed by execution id in the provenance table; the full history is in sc-pg and via `agent_result`.

### 14.5 Use by Hermes

Hermes reads `agent_result` after every execution: `questions` with `blocking: true` and `addressed_to: human` are relayed to the human; `recommended_next_role` informs (but does not determine) the next `agent_start`; `known_risks` with `severity: high` are surfaced to the human and the reviewer (Hermes skill `sc-handle-outcomes`, plan 24).

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Claim handoff invalid or oversized | `sc-worker` validation | — | synthesized handoff (plan 27) | — |
| Result handoff schema-invalid at the Runner | result validation | result rejected | `RESULT_SCHEMA_INVALID` (infrastructure) | — |
| Secret in handoff | scrubber | redacted; credentials revoked | `SECRET_DETECTED` (policy) → NEEDS_HUMAN | `quarantine-worker.md` |
| Prompt injection in handoff | not deterministically detectable | next worker may be misled | containment: projection rules, non-authoritative label, deterministic gates downstream | review |
| Handoff contradicts Git facts | consistency check warnings | none | flagged for reviewer | — |
| Worker floods with near-limit handoffs | size limits | bounded context use | — | — |

## 16. Retry behavior

N/A — a handoff is produced once per execution and never retried on its own; a retried execution produces a new handoff. Normalization is deterministic, so re-ingesting the same result yields the same digest.

## 17. Idempotency behavior

One `runner.handoffs` row per execution (PK); an identical result replay is a no-op; a different body for the same execution is rejected with 409 by plan 26.

## 18. Bootstrap behavior

N/A — no runtime component; the schema ships with F02/F07 and is embedded in `sc-worker` (27-AC-14).

## 19. Upgrade behavior

`v1alpha1`: required fields may not be added without coordinating `sc-worker`'s synthesized handoff (plan 27). New optional fields are additive. The Runner accepts the previous minor schema for one release (workers run older images during rollout).

## 20. Recovery behavior

Handoffs live in sc-pg and in the S3 result object; after sc-pg PITR the Runner can re-ingest result objects newer than the restore point (`results/<task>/<execution>/result.json`) to rebuild `runner.handoffs` rows (procedure step in `stuck-task-recovery.md` §recovery).

## 21. Observability

Metrics (Runner): `sc_handoff_bytes` (histogram, label `role`), `sc_handoff_redactions_total{role}`, `sc_handoff_synthesized_total{role}`, `sc_handoff_warnings_total{code}` (`advice_not_actionable`, `unknown_commit_reference`, `blocked_without_claim`, `truncated`). Handoff text is never logged, traced or put into metrics [SPEC §94]; logs carry only execution id, digest, bytes and redaction count.

## 22. Testing strategy

- **Contract (F02):** schema meta-validation, examples, invalid examples; every `worker-result.schema.yaml` example validates with this `$id` resolved (done at authoring time: 2/2 valid, 5/5 invalid rejected).
- **Unit (C02):** normalizer fixtures — bidi override removal, ANSI removal, NFC, shorthand normalization, secret redaction (gateway token, PEM key), truncation, each consistency warning.
- **Rendering (C03):** Forgejo renderer golden tests — a handoff containing `<img src=…>`, `@admin`, `[link](http://…)` and a fenced-code breakout attempt (```` ``` ````) renders as inert code (fence length chosen longer than any backtick run in the text).
- **Projection (C07):** plan 25 25-AC-3 covers the tester never receiving implementer handoffs.
- **E2E (C10):** `fake-secret-in-handoff` → `handoff_redacted: true`, no token bytes in stored result/log (26-AC-27); `fake-blocked` → Hermes receives blocking questions; `fake-malformed-handoff` → synthesized handoff stored with `synthesized = true`.

## 23. Acceptance criteria

### GATE-C02 (contributions; gate owned by plan 26)

- [ ] **30-AC-1** `make contracts` validates `handoff.schema.yaml` and every `worker-result` and `mcp-surface` example that embeds a handoff.
- [ ] **30-AC-2** Normalizer unit tests pass, including: input `"what_i_did": "done‮ txt.exe"` stored without U+202E; a string question in a `blocked` result stored as `{text, blocking: true}`.
- [ ] **30-AC-3** After `fake-secret-in-handoff`, `SELECT redactions FROM runner.handoffs WHERE execution_id=$1` > 0, the stored JSON contains `[REDACTED:` and not the issued gateway token, and the execution is `FAILED` with code `SECRET_DETECTED`.
- [ ] **30-AC-4** After `fake-malformed-handoff`, the stored handoff validates against `handoff.schema.yaml` and `synthesized = true`.

### GATE-C10 (contributions; needs a full fake task flow — moved from C09, GC-01)

- [ ] **30-AC-5** A PR for a task whose implementer handoff contains `@admin <img src=x> [x](http://evil)` shows that text inside a code block: the PR HTML (`GET /repos/{o}/{r}/pulls/{n}` body rendered through `POST /markdown`) contains no `<img`, no `href="http://evil"` and no mention link.

## 24. Explicit non-goals

- Live worker-to-worker messaging or threads (SPEC §70, §127).
- Using handoffs as evidence or as a transition trigger.
- Semantic prompt-injection detection (best effort is not a control; containment is).
- Free-form attachments in handoffs (files belong in Git; reports in `role_output`).

## 25. Open questions

1. **`claims.blocked` requiredness.** Making it schema-required when `state: blocked` would require a cross-schema rule in `worker-result` (plan 27). Resolution: Runner synthesizes and flags (§14.2 step 4). Blocking: no. Owner: 27/30.
2. **Hermes-side handling of injection in handoffs** is plan 24's. Blocking: no.
