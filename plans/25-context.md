# 25 — Context

**Status:** Stage-0 plan (draft)
**DAG nodes:** C07 (primary: context compiler)
**Related plans:** [21](21-tdd-workflow.md), [22](22-verification-policy.md), [24](24-hermes.md), [26](26-agent-runner.md), [27](27-worker-contract.md), [28](28-harnesses.md), [29](29-task-state-machine.md), [30](30-handoffs.md), [32](32-cost-accounting.md), [34](34-security.md), [55](55-forge-workflow.md)
**Contracts:** owns `context.schema.yaml` (`RepositoryContext`, `ContextPacket`), `role.schema.yaml` (`policy/roles/*.yaml`); consumes `worker-input`/`worker-result` (27), `handoff` (30), `red-result`/`green-result`/`test-manifest`/`repo-policy`/`repo-validation` (22), `mcp-surface` `ContextRef` (26)
**ADRs:** relies on 0003, 0008, 0017; SI-03 (Runner compiles context deterministically; Hermes contributes bounded notes)

> Every section below is mandatory (SPEC §121).

## 1. Purpose

Workers receive the *smallest useful projection* of a layered context [SPEC §40] for their role [SPEC §65, §66]. This plan defines:

1. the five context **layers** and where each lives (company, repository, task, worker, handoff) [SPEC §40–§44];
2. the **context compiler** inside the Agent Runner, which builds one `ContextPacket` per execution deterministically from Git at the task's base revision (ADR-0017), the Runner's task record and evidence store, and handoffs — never from Hermes' memory [SI-03];
3. **role projection rules** in `policy/roles/*.yaml`, e.g. the tester never receives implementation diffs or implementer handoffs [SPEC §48, §66];
4. size budgets, deterministic truncation, provenance (source refs + digests) and secret scrubbing;
5. bounded, labelled, non-authoritative `orchestrator_notes` from Hermes;
6. the path from Hermes' learned context to proposed repository-context changes [SPEC §43];
7. the five **initial roles** (researcher, planner, tester, implementer, reviewer): instructions, projection, allowed actions, expected output, default model tier and verification expectations [SPEC §65].

## 2. Requirements derived from SPEC.md

1. Context is layered: company → repository → task → worker → handoff [SPEC §40].
2. Company context holds durable organization-wide rules and lives in Git [SPEC §41].
3. Every managed repository supports `AGENTS.md` and `.strange-company/{repo.yaml, architecture.md, policy.yaml, validation.yaml, decisions/, roles/}`; Git context is authoritative; `AGENTS.md` may instruct but never substitutes for enforceable policy [SPEC §42].
4. Hermes may keep learned context; it never silently overrides Git; important learning should become a proposed repository-context change [SPEC §43].
5. Every task includes objective, source, repository, acceptance criteria, exact base revision, constraints, relevant context references and the required verification mode [SPEC §44].
6. Workers receive the smallest useful projection for their role [SPEC §40]; the tester gets requirement, ACs, architecture, current behavior and validation rules; the implementer gets requirement, ACs, red test commit, red evidence, relevant architecture and constraints; the reviewer gets requirement, red evidence, implementation diff, green evidence and handoffs [SPEC §66, §55].
7. The tester SHALL NOT receive a proposed implementation [SPEC §48]; workers SHALL NOT receive unrestricted Hermes history [SPEC §66].
8. Role determines instructions, context projection, allowed actions, expected output, default model tier and verification expectations; role and harness are independent [SPEC §65].
9. Prompt text reinforces policy but is never the sole enforcement [SPEC §4.7].
10. No secrets in context, logs or telemetry [SPEC §94].

Derived requirements:

- **25-R-1** The compiler is a pure function of (task record, execution record, role definition, platform policy revision, company-context revision, Git content at fixed commits, evidence documents, stored handoffs, `orchestrator_notes`, `context_refs`). Two compilations of the same inputs produce byte-identical `sections` and therefore the same `digest`.
- **25-R-2** Repository context and policy are read only at the task's recorded `base_commit` (ADR-0017). Agent-branch content reaches a packet only as an *evidence* diff (`red_test_diff`, `implementation_diff`), never as rules.
- **25-R-3** A forbidden section kind for the role is never emitted. If a requested `context_ref` would resolve to a forbidden kind, dispatch fails with `CONTEXT_PROJECTION_VIOLATION`; if the compiler itself would emit one (programming error), it fails closed with the same code.
- **25-R-4** Every section records its sources with the sha256 of the full source bytes (before truncation) and an authority label: `authoritative`, `evidence` or `non_authoritative`.
- **25-R-5** Everything Hermes contributes is `non_authoritative`, bounded and labelled; the reviewer receives none of it.
- **25-R-6** Every section is scrubbed with the shared scrubber (`src/scrub`, plan 27 §13.4). A hit withholds the section (`secret_detected`) and raises `SCContextSecretDetected`.
- **25-R-7** Mandatory sections (`role_instructions`, `task_spec`, `acceptance_criteria`) are never truncated; task creation rejects tasks whose mandatory sections cannot fit (§9.4).

## 3. Inputs

| Input | Source | Read how |
|---|---|---|
| Company context `context/company/*.md` | `strange-company` Git | ConfigMap `sc-company-context` (Flux-generated, plan 54) mounted at `/etc/strange-company/context/company/`; revision = sha256 of (path, bytes) like plan 26 §9.2 step 4; commit resolved like policy (path `context/company`) |
| Role definitions `policy/roles/<role>.yaml` | `strange-company` Git | ConfigMap `sc-policy` (plan 26 loader) |
| `.strange-company/repo.yaml`, `architecture.md`, `decisions/*.md`, `roles/<role>.md`, `AGENTS.md`, `policy.yaml`, `validation.yaml` | task repository at `base_commit` | Forgejo API as `sc-runner`: `GET /repos/{o}/{r}/raw/{path}?ref={sha}`, `GET /repos/{o}/{r}/contents/{dir}?ref={sha}` (plan 26 §7.3) |
| Files named by `context_refs` (`repo_path`) | task repository at `base_commit` | same |
| Task record: objective, source, constraints, ACs, verification, attempts | `sc-pg` `runner.tasks`, `acceptance_criteria` | SQL |
| Source issue (`source.kind: forgejo_issue`, `context_refs[].kind: issue`) | Forgejo issue API | `GET /repos/{o}/{r}/issues/{n}` (title + body) |
| Red/green evidence documents | S3 `evidence/<task>/<phase>/<pipeline>.json` + `runner.evidence` | plan 22 schemas |
| Diffs `base..red_commit`, `red_commit..implementation head` | Forgejo compare API | `GET /repos/{o}/{r}/compare/{base}...{head}` with patch content (verify the diff-content endpoint at C07; fallback `GET /repos/{o}/{r}/git/commits/{sha}.diff`) |
| Handoffs, reviewer findings, conflict reports | `runner.results` (sanitized bodies) | SQL |
| Conflict decisions | `runner.transitions` decision payloads (plan 21/29) | SQL |
| Research/plan outputs of this task or `task_output` refs | `runner.results` of researcher/planner executions | SQL |
| `orchestrator_notes`, `context_refs` | `agent_start` input (stored on the execution row) | SQL |

## 4. Outputs

- `ContextPacket` (`context.schema.yaml#/$defs/ContextPacket`) embedded in the worker packet (`worker-input.context`), stored at S3 `<prefix>-evidence/context/<task>/<execution>/packet.json`, digest in `runner.executions.context_digest`.
- Materialized files under `/workspace/context/` (done by `sc-worker`, plan 27 §14.6).
- Forgejo issues labelled `sc/context-proposal` created from accepted researcher `context_proposals` (§14.6).
- Metrics and alerts (§21).

## 5. Dependencies

**Hard (DAG):** C07 ← C01 (Runner core, `sc-pg`, task/execution records), F04 (policy loader validates `policy/roles/*.yaml` with `role.schema.yaml` and cross-file invariants).

**Runtime:** Forgejo API (base-revision reads), S3 `evidence` (evidence documents), `sc-pg`.

**Interfaces assumed:**
- Plan 22: `red-result`/`green-result` documents expose classification, pipeline, commit, failing test identifiers and a bounded assertion-message excerpt; `RepositoryPolicy.testPaths`; the protected acceptance-test manifest.
- Plan 30: handoff schema (fields of SPEC §69 at least).
- Plan 21/29: the transition carrying a TEST_SPEC_CONFLICT decision has a structured `decision` payload (`test_change_approved`, `rationale`, `decided_by`) stored with the transition.
- Plan 55: Runner mechanics to create a labelled issue (`POST /repos/{o}/{r}/issues`, labels) — required by §14.6.

## 6. Owned state

- **Git (authoritative, this plan owns the content rules):** `context/company/*.md`, `policy/roles/*.yaml` in `strange-company`; the *format* of `.strange-company/repo.yaml` (`RepositoryContext`) in every managed repository.
- **Derived (Runner-owned rows/objects; written by the Runner on dispatch):** stored packets in S3 and `executions.context_digest`. Packets are reproducible evidence of what a worker saw, not a source of truth.
- Hermes' learned memory is **not** owned here and is not authoritative (plan 24).

## 7. External interfaces

| Interface | Consumer | Contract |
|---|---|---|
| `ContextPacket` | `sc-worker` (plan 27), auditors | `context.schema.yaml` |
| `RepositoryContext` manifest | repository maintainers; Runner (REPOSITORY_NOT_MANAGED if missing/invalid) | `context.schema.yaml` |
| `RoleDefinition` files | Runner policy loader; Hermes (via `agent_status.next_allowed_roles` only) | `role.schema.yaml` |
| `agent_start.context_refs`, `orchestrator_notes` | Hermes | `mcp-surface.schema.yaml` |
| Context-proposal issues | humans in Forgejo | §14.6 |

## 8. Internal interfaces

The compiler is a Runner component (plan 26 §8) with two entry points:

- `Check(task, role, refs, notes) → ok | CONTEXT_REF_INVALID | CONTEXT_PROJECTION_VIOLATION` — called synchronously by `agent_start` semantic check 10 (plan 26 §14.2). Resolves refs at the base commit (existence, kind, projection), without fetching large content.
- `Compile(execution) → ContextPacket | error` — called by the dispatcher (plan 26 §14.3 step 2). Errors: Forgejo/S3 unavailable → retried by the outbox for up to 10 min, then the execution fails `CONTEXT_COMPILE_FAILED` (infrastructure); a mandatory source unreadable → `CONTEXT_COMPILE_FAILED` with detail.

Content cache: in-memory LRU (64 MiB) keyed by (repository, commit, path) — Git content at a commit is immutable; evidence keyed by object key + sha256.

## 9. Configuration

### 9.1 Layers and section kinds

| Layer | Section kind(s) | Source | Authority |
|---|---|---|---|
| company | `company_rules` | `context/company/*.md` (concatenated in file-name order, each file headed by its name) | authoritative |
| repository | `repository_context` (rendered summary of `repo.yaml`), `agents_md`, `architecture`, `decisions` (titles of all `decisions/*.md`, bodies newest-first within budget) | repository at `base_commit` | authoritative |
| repository/worker | `validation_rules` (validation commands, test paths, protected-path summary from base `validation.yaml`/`policy.yaml`) | repository at `base_commit` | authoritative |
| repository/task | `context_files` (files named by `repo_path` refs) | repository at `base_commit` | authoritative (Git content; *descriptive*, not rules) |
| task | `task_spec` (objective, source, constraints, verification mode, exemption), `acceptance_criteria` (all ACs, scoped subset marked) | task record | authoritative |
| task | `task_spec` sub-section `source-issue` (issue title/body) | Forgejo issue | non_authoritative |
| worker | `role_instructions` (platform `instructions` + additive `.strange-company/roles/<role>.md`), `prior_attempt_evidence` | Git (base) / Runner records | authoritative / evidence |
| evidence | `red_test_diff`, `red_evidence`, `implementation_diff`, `green_evidence` | Forgejo compare / evidence store | evidence |
| handoff | `tester_handoff`, `implementer_handoff`, `reviewer_findings`, `conflict_report`, `conflict_decision`, `research_reports` | Runner results / transitions | non_authoritative (`conflict_decision`: authoritative when `decided_by` is `human:*`, else non_authoritative) |
| orchestrator | `orchestrator_notes` | `agent_start` | non_authoritative (schema-enforced) |

Precedence stated in `INDEX.md` and every role's instructions: (1) company rules and platform role instructions; (2) task spec and acceptance criteria; (3) repository context (`AGENTS.md`, architecture, decisions, repository role notes); (4) evidence; (5) non-authoritative material. A conflict between (1)/(2) and (3) is reported as `blocked` with a question, never silently resolved.

### 9.2 Role projection matrix

`I` = included (if available), `F` = forbidden (never emitted; listed in `withheld` with `forbidden_for_role` when the material exists), `–` = not included (listed with `not_included_for_role` only if it exists and was requested via a ref).

| Section kind | researcher | planner | tester | implementer | reviewer |
|---|---|---|---|---|---|
| `company_rules` | I | I | I | I | I |
| `repository_context` | I | I | I | I | I |
| `agents_md` | I | I | I | I | I |
| `architecture` | I | I | I | I | I |
| `decisions` | I | I | I | – | I |
| `role_instructions` | I | I | I | I | I |
| `task_spec` | I | I | I | I | I |
| `acceptance_criteria` | I | I | I | I | I |
| `validation_rules` | – | I | I | I | I |
| `context_files` | I | I | I | I | I |
| `research_reports` | I | I | I | I | – |
| `red_test_diff` | I | – | – | I | I |
| `red_evidence` | I | – | I | I | I |
| `implementation_diff` | – | – | **F** | – | I |
| `green_evidence` | – | – | **F** | – | I |
| `tester_handoff` | – | – | – | I | I |
| `implementer_handoff` | – | – | **F** | – | I |
| `prior_attempt_evidence` | I | I | I | I | I |
| `reviewer_findings` | – | – | **F** | I | – |
| `conflict_report` | – | – | **F** | – | I |
| `conflict_decision` | – | – | I | I | I |
| `orchestrator_notes` | I | I | I | I | **F** |

Rationale for the non-obvious cells:
- Researchers get `red_test_diff`/`red_evidence` because ALREADY_SATISFIED / NEEDS_INVESTIGATION investigations (SPEC §51) need them.
- The tester gets `red_evidence` only of its *own* generation (TEST_NEEDS_WORK retries); it never gets anything produced after RED by other roles. `conflict_report` (the implementer's argument, which may describe the implementation) is forbidden; the tester gets the adjudicated `conflict_decision` instead (SPEC §70).
- Implementers see their own previous attempts only through `prior_attempt_evidence`; `implementation_diff`/`green_evidence` as standalone sections are for the reviewer.
- The reviewer gets no `orchestrator_notes` so that Hermes cannot steer the legitimacy review (SPEC §55 independence).

**Post-RED material is never delivered to a tester** (review TDD-14): any `research_reports` or `task_output` produced by an execution whose `start_commit` is not an ancestor of (or equal to) red(g) of the tester's generation — i.e. written after implementation work existed — is withheld with reason `post_red_material`; a `context_ref` that would deliver it fails `CONTEXT_PROJECTION_VIOLATION`.

`prior_attempt_evidence` is role-filtered: only executions of the **same role** in the **same generation** (plan 21), each summarized as `{execution_id, attempt, outcome, failure_class, failure_code, violations[], verification classification + failing test ids + ≤ 2 KiB assertion excerpt, diffstat of unpushed partial work (plan 27 §14.13), handoff.what_remains, handoff.known_risks}`. It never includes another role's diff or handoff.

### 9.3 Initial roles (`policy/roles/<role>.yaml`)

Common to all roles: `allowedHarnesses` lists `claude` plus `codex`/`opencode` (disabled in MVP, plan 28) and the generic fakes `fake-success`, `fake-failure`, `fake-timeout`, `fake-deadline-exceeded`, `fake-blocked`, `fake-malformed-result`, `fake-malformed-handoff`, `fake-malformed-stream`, `fake-claim-missing`, `fake-over-budget`, `fake-spend-loop`, `fake-model-switch`, `fake-ignore-cancel`, plus the role-specific fakes listed below. `toolPolicy.claude-code` narrows plan 28's defaults.

| Field | researcher | planner | tester | implementer | reviewer |
|---|---|---|---|---|---|
| purpose | investigate a question; produce a report (and optional context proposals) | turn intent into proposed ACs and subtasks | turn ACs into failing executable tests [SPEC §48–§50] | make the red tests pass without weakening them [SPEC §52–§54] | judge whether green was earned legitimately [SPEC §55] |
| `forgeAccess` | read | read | write | write | read |
| role-specific fakes | — | — | `fake-red-valid`, `fake-red-valid-v2`, `fake-red-compile-error`, `fake-red-with-stub`, `fake-judge-docs`, `fake-red-broken-test`, `fake-already-satisfied`, `fake-push`, `fake-push-no-result`, `fake-push-other-branch`, `fake-secret-in-handoff`, `fake-tester-touches-implementation`, `fake-wrong-commit-claim` | `fake-green`, `fake-produce-docs`, `fake-test-skip`, `fake-discovery-config-edit`, `fake-test-env-detection`, `fake-push`, `fake-push-no-result`, `fake-test-spec-conflict`, `fake-green-after-retry`, `fake-test-tamper`, `fake-test-delete`, `fake-protected-path-edit`, `fake-push-other-branch`, `fake-secret-in-handoff`, `fake-large-diff`, `fake-not-descendant`, `fake-wrong-commit-claim` | `fake-review-approve`, `fake-review-request-changes`, `fake-review-reject`, `fake-adjudicate-test-valid`, `fake-adjudicate-test-invalid` |
| `defaultModelTier` / `allowedModelTiers` | standard / bargain, standard, frontier | frontier / standard, frontier | standard / bargain, standard, frontier | bargain / bargain, standard, frontier | standard / standard, frontier |
| `defaultProfile` / `allowedProfiles` | tiny / tiny, reasoning | reasoning / tiny, reasoning | tests / tiny, tests, implementation | implementation / tests, implementation, reasoning | reasoning / tiny, reasoning |
| `accounting` (first / retry) | specification / specification | specification / specification | test_authoring / test_authoring | implementation / green_retry | review / review |
| `diffRules` | mode none (0 files, 0 bytes) | none | allowlist `[@testPaths, @fixturePaths, @testInfraPaths, @stubPaths]` (plans 21/22; stub hunks must also pass the declared-stub check, ADR-0352), deny `[@protectedPaths]`, ≤ 40 files, ≤ 256 KiB, no deletions | denylist `[@protectedPaths, @protectedTests]`, ≤ 60 files, ≤ 256 KiB per execution and ≤ 256 KiB cumulative `red..head` (= the reviewer's `implementation_diff` cap, TDD-13), deletions allowed | none |
| `toolPolicy.claude-code` | allowed: Read, Glob, Grep, `Bash(git log *)`, `Bash(git show *)`, `Write(/workspace/out/**)` | same as researcher | registry defaults | registry defaults | same as researcher plus `Bash(git diff *)` |
| `contextProjection.maxTotalBytes` | 262144 | 262144 | 262144 | 393216 | 524288 |
| `expectedOutput.allowedResultStates` | completed, failed, blocked | completed, failed, blocked | completed, failed, blocked, already_satisfied | completed, failed, blocked, test_spec_conflict | completed, failed, blocked |
| `commitRequiredOnCompleted` | false | false | true | true | false |
| `roleOutput` | research_report | plan | tester | implementer | reviewer |
| `verification.triggers` | none | none | red | green | none |
| verification expectations | report cites sources (paths/commits); context proposals target only `AGENTS.md`/`.strange-company/**` | every proposed AC is a single observable behavior; ambiguities listed, not resolved by invention | ≥ 1 new requirement-specific assertion fails on base for the right reason; every scoped AC mapped to ≥ 1 test (`ac_mapping`) [SPEC §50] | all task tests pass; no acceptance test modified/deleted/skipped; otherwise TEST_SPEC_CONFLICT [SPEC §53] | all five checks answered (`red_failed_for_right_reason`, `acceptance_tests_intact`, `green_not_by_bypass`, `all_ac_covered`, `no_out_of_scope_changes`); `approve` only if all `pass` |

Instruction texts (`spec.instructions`, ≤ 16 KiB each) are written at C07 and must contain, at minimum:

- **all roles:** the precedence rule (§9.1); "work only inside `/workspace/repo` and `/workspace/out`"; "do not commit, push, fetch, or change Git configuration — Strange Company does that"; "write `/workspace/out/claim.json` matching the HarnessClaim schema with `role_output.kind: <kind>`"; "if instructions conflict or information is missing, report `blocked` with concrete questions"; "never copy credentials, tokens or keys into any output".
- **researcher:** answer the stated question only; cite evidence; propose context changes only as `context_proposals`.
- **planner:** produce ACs in the stable `AC-<n>` form, one observable behavior each, phrased so a test can decide them; list ambiguities.
- **tester:** write tests only under the test paths; map each test to an AC id with a comment `AC-<n>`; tests must fail because the behavior is absent, not because of syntax errors, missing dependencies or impossible expectations; never implement the behavior; if the behavior seems to exist already, still commit the tests and report `already_satisfied` (CI decides) [SPEC §50, §51].
- **implementer:** start from the red commit; make the acceptance tests pass by implementing behavior; never delete, skip, weaken or re-baseline acceptance tests, change expected values, bypass discovery or mock away the tested behavior; if a test is wrong, stop and report `test_spec_conflict` with AC ids, test names and an explanation [SPEC §53].
- **reviewer:** compare red evidence, implementation diff and green evidence; check that the red failure was meaningful, acceptance tests are intact, green was not achieved by bypass, all ACs are covered and nothing out of scope changed; report findings with severities [SPEC §55].

Loader invariants (F04 additions owned here): `defaultModelTier` equals `ModelRouting.roles.<role>.defaultTier` and `allowedModelTiers` equals its `allowedTiers` (plan 31 31-R-9; mismatch → `ROLE_TIER_MISMATCH <role>`, policy set rejected); exactly these five files exist; `contextProjection.include ∩ forbid = ∅`; the tester's `forbid` ⊇ {`implementation_diff`, `implementer_handoff`, `green_evidence`, `conflict_report`, `reviewer_findings`}; the reviewer's `forbid` ∋ `orchestrator_notes`; every role includes `role_instructions`, `task_spec`, `acceptance_criteria`; `maxTotalBytes ≥ 196608` (mandatory sections fit, §9.4); `forgeAccess: read|none` ⇔ `diffRules.mode: none` ⇔ `commitRequiredOnCompleted: false`.

### 9.4 Budgets and truncation

Platform per-kind caps (bytes; a role's `sectionBudgets` and a repository's `contextBudgets` may only lower them):

| Kind | Cap | Kind | Cap |
|---|---|---|---|
| `company_rules` | 32768 | `red_test_diff` | 131072 |
| `repository_context` | 16384 | `red_evidence` | 16384 |
| `agents_md` | 16384 | `implementation_diff` | 262144 |
| `architecture` | 32768 | `green_evidence` | 16384 |
| `decisions` | 32768 | `tester_handoff`, `implementer_handoff` | 32768 each |
| `role_instructions` | 24576 (mandatory) | `prior_attempt_evidence` | 32768 |
| `task_spec` | 49152 (mandatory) | `reviewer_findings` | 32768 |
| `acceptance_criteria` | 114688 (mandatory) | `conflict_report` | 16384 |
| `validation_rules` | 8192 | `conflict_decision` | 8192 |
| `context_files` | 131072 | `orchestrator_notes` | 4096 |
| `research_reports` | 65536 | | |

Task creation (plan 26 §14.1) rejects with `VALIDATION_FAILED` (field `/task`) a task whose rendered `task_spec` + `acceptance_criteria` exceed 163840 bytes; with `role_instructions` ≤ 24576 the mandatory sections always fit in the 196608-byte minimum role budget (25-R-7).

Algorithm (deterministic):

1. Build candidate sections in the role's `include` order; scrub each (25-R-6); compute `original_bytes` and source digests.
2. Cap each section to `min(platform cap, role sectionBudget, repo contextBudget)` with the media-type rule:
   - `text/markdown`, `text/plain`: keep the longest prefix ending at a newline within (cap − marker length), at a UTF-8 boundary, then append `\n\n[truncated by context compiler: <omitted> of <original> bytes omitted; full source sha256 recorded in provenance]\n`;
   - `text/x-diff`: for the **reviewer's `implementation_diff` no truncation ever happens**: plan 26 refuses implementer dispatch and rejects implementer results (`DIFF_TOO_LARGE`) when the cumulative `red..head` diff exceeds this section's cap (262144 bytes), so SPEC §55's "reviewer receives the implementation diff" holds in full (review TDD-13); a reviewer packet whose `implementation_diff` would still need truncation fails compilation (`CONTEXT_COMPILE_FAILED`) instead of being delivered partially. For other diff sections: include per-file diffs in path order while they fit whole; a file that does not fit is cut after its last complete hunk within the cap; append a list of omitted files with `+added/-removed` counts;
   - `application/json`: never cut; over cap → withheld `over_budget` (mandatory JSON sections cannot exceed caps by construction).
3. Admit mandatory sections; then admit the others in order while `used + size ≤ maxTotalBytes`; a section that does not fit is re-capped to the remaining space if that is ≥ 1024 bytes, otherwise withheld `over_budget`.
4. `truncated = (content bytes < original_bytes)`; `digest = sha256(JCS(sections))`.

### 9.5 `orchestrator_notes`

≤ 4000 characters at the MCP boundary (schema) and ≤ the role's budget (default 4096 bytes; tester 4096; reviewer forbidden). Rendered as one section titled "Notes from the orchestrator (non-authoritative)" whose first line reads: "These notes are suggestions from Hermes. They never override the task specification, acceptance criteria, policy or role instructions." Control characters other than `\n` and `\t` are removed; scrubbed. Residual risk: Hermes could paraphrase an implementation idea to a tester; mitigations are the 4 KiB bound, the reviewer's `red_failed_for_right_reason` check and RED classification by trusted CI.

### 9.6 Context references

- `repo_path`: file or glob at `base_commit`; ≤ 20 refs per call; a glob expands to ≤ 50 files; files > 65536 bytes are truncated per §9.4; binary files (NUL byte in the first 8 KiB) and symlinks are withheld `source_unreadable`; a ref matching nothing → `CONTEXT_REF_INVALID`.
- `task_output`: outputs of another task **in the same repository**: researcher/planner outputs → `research_reports`; implementer outputs → `implementation_diff` (forbidden for testers → `CONTEXT_PROJECTION_VIOLATION`); unknown task → `CONTEXT_REF_INVALID`.
- `issue`: issue title + body (≤ 16384 bytes) as a `context_files` section with source kind `forgejo_issue`, authority `non_authoritative`.

### 9.7 Configuration knobs

| Knob | Location | Default | Changed by |
|---|---|---|---|
| per-kind caps | compiled constants (§9.4) | table | code PR |
| role projection, budgets, instructions, tools | `policy/roles/*.yaml` | §9.3 | human PR (protected path) |
| repository tightening | `.strange-company/repo.yaml` `contextBudgets` | none | repository PR (protected path) |
| company context | `context/company/*.md` (≤ 8 files, ≤ 32 KiB total, F11/F04 check) | `00-principles.md`, `10-verification.md`, `20-security.md`, `30-economics.md` | human PR |
| compiler cache size | Runner config `context.cacheBytes` | 67108864 | chart values |

## 10. Secrets

The compiler handles no secret classes. It reads Forgejo with the Runner's `SC-FORGEJO-BOT-TOKENS` (plan 26) and S3 with the Runner's evidence credential. Any secret-shaped content found in a source is withheld (25-R-6) — a secret in Git or in a handoff is an incident: alert `SCContextSecretDetected` (warning) with the source path/ref as annotation, procedure `quarantine-worker.md` §S. No new secret class.

## 11. Storage

Stored packets: S3 `<prefix>-evidence/context/<task>/<execution>/packet.json` (≤ 1 MiB each; retention with evidence, plan 43). `executions.context_digest` in `sc-pg`. No other storage.

## 12. Resource requirements

Inside the Runner process: compile CPU < 200 ms and peak memory < 8 MiB per packet (dominated by diffs ≤ 512 KiB) plus the 64 MiB cache. Covered by the Runner's limits (plan 26 §12); plan 51 measures at C07.

## 13. Security boundary

- The compiler runs in the trusted Runner. Its inputs include hostile text (repository files, handoffs, issue bodies, Hermes notes). It never interprets them; it only copies, truncates, labels and scrubs.
- Projection is enforced by the Runner, not by Hermes [SI-03]. The tester's clone itself is narrowed by `fetch_ref` (plan 27 §14.4), so its working copy contains no implementation either.
- Repository rules come only from `base_commit` (ADR-0017); agent branches cannot alter the context that judges them, and `.strange-company/**`/`AGENTS.md` are protected paths (ADR-0008).
- Residual risk: a harness with the deploy key could fetch other refs of the same repository (plan 27 §10). Mitigated by tool deny-lists (`git fetch`), no tags or other branches in the clone, and the reviewer's legitimacy checks; documented for plan 34.

## 14. Happy path

### 14.1 Task creation (agent_start with `task`)

The Runner validates objective/constraints/ACs sizes (§9.4) and reads `.strange-company/repo.yaml` at the resolved `base_commit`; it must validate as `RepositoryContext` (else `REPOSITORY_NOT_MANAGED`, detail `repo_context_invalid`). `contextBudgets` above platform caps are rejected at load (`VALIDATION_FAILED`).

### 14.2 Dispatch check (synchronous)

`Check()` resolves `context_refs` and verifies projection (§8). Example: tester with `{kind: task_output, task_id: sc-170}` where `sc-170` is an implemented task → `CONTEXT_PROJECTION_VIOLATION`, no execution created.

### 14.3 Compile (dispatcher)

For implementer `sc-184-x2` (attempt 1, red commit `r`):

1. Load role `implementer`; candidate kinds per §9.2.
2. Read company rules (ConfigMap snapshot + revision); read `repo.yaml`, `AGENTS.md`, `architecture.md`, `validation.yaml`, `policy.yaml`, `roles/implementer.md` at `base_commit`.
3. Task record → `task_spec`, `acceptance_criteria` (scoped subset marked `"scoped": true`).
4. Evidence: compare `base...r` → `red_test_diff`; `red-result` for `r` → `red_evidence` (classification `expected_failure`, failing tests `TestAddAC1`, `TestAddAC2`, excerpts).
5. Handoffs: accepted tester execution's handoff → `tester_handoff`.
6. `orchestrator_notes` from the `agent_start` call.
7. Withheld: none forbidden for implementer exist yet; `decisions` listed as `not_included_for_role` only if requested.
8. Truncate/admit (§9.4), digest, store S3, record digest; continue dispatch.

### 14.4 Delivery

The packet goes inline in `GET /v1/worker/packet` (plan 26/27); `sc-worker` verifies the digest and materializes files outside the working tree (plan 27 §14.6).

### 14.5 Retry with evidence

On implementer attempt 2 after a GREEN failure, `prior_attempt_evidence` contains attempt 1's summary (green classification `failure`, failing tests, excerpt, `what_remains`). This is what makes retries "incorporate actual evidence" [SPEC §74]; `fake-green-after-retry` (plan 28) asserts the section exists.

### 14.6 Learned context → repository-context proposals [SPEC §43]

1. Hermes keeps learned observations in its own memory (non-authoritative, plan 24). They never reach workers except through bounded `orchestrator_notes`.
2. When Hermes judges an observation durable (Hermes skill `sc-learned-context`, plan 24), it starts a **researcher** task (verification-first, exemption class for research per plan 22) whose objective asks for a context proposal, with `context_refs` to the tasks/evidence that support it.
3. The researcher returns `role_output.kind: research_report` with `context_proposals[]` (`worker-result.schema.yaml`): target `path` (only `AGENTS.md` or `.strange-company/**`), `rationale`, `proposed_content`, `evidence_refs`.
4. On an accepted researcher result, the Runner (outbox, plan 26) creates one Forgejo issue per proposal in the task repository: title `Context proposal: <path>`, labels `sc/context-proposal`, body = rationale, fenced proposed content, provenance (task id, execution id, evidence refs) and a hidden marker `<!-- sc-proposal sha256:<sha256(path + content)> -->`. Before creating, it searches open issues with the label for the same marker (dedup).
5. A human turns the proposal into a PR (protected paths change only through human-reviewed PRs, ADR-0008) or closes it. Nothing learned silently overrides Git.

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Forgejo unavailable at compile | API errors | execution waits in QUEUED/STARTING | outbox retry ≤ 10 min, then FAILED `CONTEXT_COMPILE_FAILED` (infra) | `restore-forgejo.md` |
| Evidence object missing / digest mismatch | S3 read, sha256 compare | section cannot be built | mandatory? no → withheld `source_unreadable`, alert `SCContextEvidenceMissing` (warning); RED/GREEN evidence for an implementer/reviewer is required → FAILED `CONTEXT_COMPILE_FAILED` | `tdd-task-recovery.md` |
| `repo.yaml` missing/invalid at base | schema validation | task cannot be created | `REPOSITORY_NOT_MANAGED` | fix repository context by PR |
| Secret in a source | scrubber | section withheld | `SCContextSecretDetected` | `quarantine-worker.md` §S |
| Forbidden kind requested via ref | `Check()` | dispatch refused | `CONTEXT_PROJECTION_VIOLATION` | Hermes adjusts refs |
| Over budget | §9.4 | sections truncated/withheld | listed in `withheld`; metric | tune role budgets by PR |
| Role file invalid | F04 loader | dispatch disabled | `SCRunnerPolicyInvalid` | fix PR |
| Packet digest mismatch at the worker | `sc-worker` | execution fails `context_invalid` | infra retry | bug report |
| Hermes notes used to steer reviewer | projection | impossible (forbidden) | — | — |
| Duplicate context proposal | marker search | no duplicate issue | — | — |

## 16. Retry behavior

Compilation retries only on transient dependency errors (outbox, 10 min). Each retry of an execution recompiles a new packet; the new packet differs only by new evidence (`prior_attempt_evidence`), never by changed rules (same `base_commit`).

## 17. Idempotency behavior

Compile is a pure function (25-R-1); re-running it for the same execution after a Runner crash yields the same digest, and the S3 object key is per execution (overwrite with identical content is harmless). Context-proposal issues are deduplicated by marker.

## 18. Bootstrap behavior

Company context and role files are part of the `strange-company` repository from F01/F04 onward (ConfigMaps rendered by Flux with the strange-company layer, plan 54). Each managed repository needs `.strange-company/repo.yaml` before tasks can target it; `strange-company` itself ships one from F01 (plan 01), and `sc-e2e/fake-target` from its fixture (plan 28 §14.5).

## 19. Upgrade behavior

- Changing role projection or budgets is a policy PR; executions record the policy revision used, so packets remain explainable.
- New section kinds require a `context.schema.yaml` change (closed enum), a compiler release and role-file updates in one release; the compiler fails closed on unknown kinds.
- `compiler_version` is recorded in every packet.

## 20. Recovery behavior

Packets are reproducible from Git + `sc-pg` + evidence; after an `sc-pg` PITR, stored packets in S3 remain as evidence of what was delivered. No compiler state to recover.

## 21. Observability

- Metrics: `sc_context_compile_seconds` (histogram, label `role`), `sc_context_bytes` (histogram, `role`), `sc_context_sections_withheld_total{reason}` (counter), `sc_context_compile_failures_total{reason}` (counter), `sc_context_proposals_total{outcome}` (counter: created, deduplicated).
- Spans: child span `context.compile` of the Runner's dispatch span with attributes `sc.role`, section count, bytes, withheld count (no content).
- Alerts (IDs for plan 42): `SCContextSecretDetected` (warning, any increase of `sc_context_sections_withheld_total{reason="secret_detected"}`), `SCContextEvidenceMissing` (warning), `SCContextCompileFailures` (warning, > 3 in 1 h).

## 22. Testing strategy

All deterministic, no model calls.

- **Unit:** projection matrix (every role × kind cell, generated from the role files); truncation per media type (golden outputs); admission order; digest stability (compile twice, reorder map iteration); scrubber integration; `Check()` error codes.
- **Golden packets:** fixture task + fixture repository (`src/runner/context/testdata/`) → committed golden `ContextPacket` per role; CI diff must be empty.
- **Contract:** every produced packet validates against `context.schema.yaml`; role files against `role.schema.yaml`.
- **Integration (k3d, C07):** Runner + Forgejo test instance: base-revision isolation, issue refs, context proposals.
- **e2e (C10):** `fake-green-after-retry` asserts `prior_attempt_evidence`; `fake-success` reviewer packet audited for withheld `orchestrator_notes`.

## 23. Acceptance criteria

### GATE-C07

GATE-C07 runs against the Runner's Forgejo stub (plan 26 §22) because C03 is not upstream of C07 (review RA-11); live variants run in the C10 suite (plan 46).

- [ ] **25-AC-1** `make policy-check` validates the five committed `policy/roles/*.yaml` and rejects fixtures: an implementer `defaultModelTier: standard` while `ModelRouting.roles.implementer.defaultTier` is `bargain` (`ROLE_TIER_MISMATCH implementer`), a sixth role file, a tester without `implementation_diff` in `forbid`, a reviewer without `orchestrator_notes` in `forbid`, `include ∩ forbid ≠ ∅`, `maxTotalBytes: 65536`, and a read-only role with `commitRequiredOnCompleted: true` (7 fixtures, 7 distinct errors).
- [ ] **25-AC-2** Golden packets: compiling the fixture task for each of the five roles yields `sections` byte-identical to `testdata/golden/<role>.json`; compiling twice yields the same `digest`.
- [ ] **25-AC-3** Tester isolation: with the fixture task in state TEST_SPEC_CONFLICT (an implementer result, implementation diff, green evidence and a conflict report all present in the test DB), the tester packet contains no section of kind `implementation_diff`, `implementer_handoff`, `green_evidence`, `conflict_report` or `reviewer_findings`, and `withheld` lists each existing one with reason `forbidden_for_role`.
- [ ] **25-AC-4** `agent_start` for role tester with `context_refs: [{kind: task_output, task_id: <implemented fixture task>}]` returns `CONTEXT_PROJECTION_VIOLATION`; no execution row and no Job are created.
- [ ] **25-AC-5** Base-revision isolation (Forgejo stub): with the stub's `agent/<task>` head containing a commit that changes `.strange-company/architecture.md`, a newly compiled implementer packet's `architecture` section has `sources[0].commit == base_commit` and the base content digest.
- [ ] **25-AC-6** Orchestrator notes: an implementer packet built with notes contains exactly one `orchestrator_notes` section with `authority: non_authoritative`, `layer: orchestrator`, content starting with the fixed disclaimer, ≤ 4096 bytes; a reviewer packet built with notes contains none and lists it in `withheld`.
- [ ] **25-AC-7** Truncation: a 300 KiB `architecture.md` produces a section of ≤ 32768 bytes ending with the truncation marker, `truncated: true`, `original_bytes: 307200`, and a source digest equal to `sha256` of the full file.
- [ ] **25-AC-8** Secret handling: a fixture `AGENTS.md` containing a canary `sk-ant-…` string yields `withheld: [{kind: agents_md, reason: secret_detected}]`, no section contains the canary, and `sc_context_sections_withheld_total{reason="secret_detected"}` increases by 1 (the `SCContextSecretDetected` alert path is asserted by plan 42's catalogue test at C11).
- [ ] **25-AC-9** A repository `contextBudgets.decisions: 300000` is rejected at task creation with `VALIDATION_FAILED`; a task whose objective + ACs render > 163840 bytes is rejected with `VALIDATION_FAILED` field `/task`.
- [ ] **25-AC-10** (moved to GATE-C10 as 46-AC-7: it needs a live researcher execution and Forgejo issues, C02/C03.)
- [ ] **25-AC-12** Post-RED filtering: a tester packet compiled for a task whose researcher report came from an execution started at the implementation head lists that report in `withheld` with reason `post_red_material`; the reviewer packet of a task whose cumulative implementation diff is 262144 bytes contains the whole diff with `truncated: false`.
- [ ] **25-AC-11** Every packet stored in S3 during the C07 integration run validates against `context.schema.yaml` and its object sha256 equals the `executions.context_digest`-referenced packet bytes (script over all executions of the run).

## 24. Explicit non-goals

- No semantic retrieval, embeddings or relevance ranking in MVP: projection is rule-based and refs are explicit.
- No delivery of Hermes conversation history to workers [SPEC §66].
- No autonomous edits of protected context files; proposals only (§14.6).
- No per-repository custom section kinds.

## 25. Open questions

1. **Exemption class for researcher-only tasks** (blocking: no). Context-proposal tasks are verification-first with an exemption such as `research`/`documentation-only`; the class names are plan 22's. Owner: 22.
2. **Forgejo diff-content endpoint** (blocking: no). Compare API patch output vs commit `.diff` endpoint is verified at C07 (25-AC-2 integration variant); fallback documented in §3.
3. **Issue creation in Runner mechanics** (blocking: no). Plan 26 §7.3 lists no issue-creation call; plan 55 must include `POST /repos/{o}/{r}/issues` with labels for §14.6. Owner: 55/26.
4. **Conflict decision payload shape** (blocking: no). Assumed fields `test_change_approved`, `rationale`, `decided_by` (plan 21/29). Owner: 21.
