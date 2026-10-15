# 22 — Verification Policy, RED/GREEN Semantics and Test Protection

**Status:** Stage-0 plan (draft)
**DAG nodes:** F06 verifier-lib (primary), F04 policy-lib (co-owner with 26: verification, repository-policy and repository-validation schemas and the tighten-only merge function), C04 woodpecker-adapter-red-green-evidence (co-owner with 21 and 20: classification), C09 merge-policy-enforcement (co-owner with 55: merge-time facts)
**Related plans:** [20 Woodpecker](20-woodpecker.md), [21 TDD workflow](21-tdd-workflow.md), [26 Agent Runner](26-agent-runner.md), [27 worker contract](27-worker-contract.md), [25 context](25-context.md), [29 state machine](29-task-state-machine.md), [31 routing](31-model-routing.md), [34 security](34-security.md), [52 CI strategy](52-ci-strategy.md), [55 forge workflow](55-forge-workflow.md), [01 repository](01-repository.md)
**Contracts:** owns `verification.schema.yaml` (sections `retry`, `watchdog`, `testSpecConflict`, `humanAuthority` owned by 29), `repo-policy.schema.yaml`, `repo-validation.schema.yaml`, `red-result.schema.yaml`, `green-result.schema.yaml`, `test-manifest.schema.yaml`; consumes `acceptance-criteria` (21), `worker-result` (27), `model-routing` `RepositoryRoutingOverride` (31), `task` (29)
**ADRs:** relies on 0008, 0017, 0019, 0301, 0302; introduces [ADR-0351](../adr/0351-runner-computed-red-green-classification.md) (the Runner computes RED/GREEN validity from sc-verify observations) and [ADR-0352](../adr/0352-declared-stubs-for-missing-symbols.md) (stub policy); co-owns [ADR-0353](../adr/0353-verification-first-judge-states.md)

> Every section below is mandatory (SPEC §121).

## 1. Purpose

This plan defines **what counts as evidence**: the exact meaning of valid RED [SPEC §50], already-satisfied [SPEC §51], valid GREEN [SPEC §54] and protected acceptance tests [SPEC §53]; the verification modes and exemption classes [SPEC §46, §57]; the protected paths; the merge-time provenance facts [SPEC §102]; and the repository policy files that configure them (`policy.yaml`, `validation.yaml`) with tighten-only semantics (ADR-0017).

It owns `sc-verify` (the static observer that runs inside the trusted `verify` workflow, plan 20) and the **verifier library** (F06), which the Runner uses to classify evidence, build test manifests, detect tampering and compute merge facts. Deterministic systems decide [SPEC §4.3]; this plan states exactly what they decide and where heuristics end.

## 2. Requirements derived from SPEC.md

1. Every work item has a machine-visible verification mode; `test-first` is the default for behavior-changing software; exemption classes are version-controlled; workers cannot exempt their own tasks [SPEC §46].
2. Valid RED: the relevant test runs; ≥ 1 new requirement-specific assertion fails; the failure corresponds to absent/incorrect behavior; it is not broken infrastructure, syntax error, missing dependency or a deliberately impossible test; a compile failure is not automatically valid [SPEC §50].
3. A test passing before implementation leads to an investigation state; no artificial failure is manufactured [SPEC §51].
4. The red commit contains only tests, fixtures, minimal test infrastructure and verification artifacts [SPEC §49].
5. Implementation cannot delete, skip, weaken, change expectations of, bypass discovery of, or mock away acceptance tests; a changed test needs a distinct reviewable action [SPEC §53].
6. GREEN reruns the task's tests, relevant existing tests and all required validation; GREEN_VERIFIED only when required deterministic validation succeeds [SPEC §54].
7. Verification-first work defines its judge before the answer [SPEC §57].
8. TDD is enforced by state machine, Runner policy, Git provenance, trusted CI and merge policy [SPEC §58]; merge policy verifies the nine provenance facts; missing provenance blocks merge unless an authorized exemption applies [SPEC §102].
9. CI tests that broken tests are not valid RED, that deletion/weakening are detected where policy permits, and that exemptions require approved policy [SPEC §106].
10. Rules live in files; prompt text is not enforcement [SPEC §4.7]; repository context cannot substitute for platform policy [SPEC §42].

Derived requirements:

- **22-R-1** No verdict computed inside CI is trusted. `sc-verify` reports observations; the Runner computes classification (ADR-0351).
- **22-R-2** Every rule that can change a verdict is version-controlled (`policy/verification.yaml`, base-revision `.strange-company/policy.yaml` and `validation.yaml`) and its revision is recorded on the evidence.
- **22-R-3** A failure the verifier cannot classify is classified as an error, never as an assertion (fail closed).
- **22-R-4** Each heuristic states its limits; heuristics produce warnings for the reviewer unless the rule is precise enough to be a violation.

## 3. Inputs

| Input | Source | Contract |
|---|---|---|
| Platform verification policy | `policy/verification.yaml` (ConfigMap `sc-policy`, ADR-0017) | `verification.schema.yaml` |
| Repository policy | `.strange-company/policy.yaml` at the task's base commit (Forgejo API) | `repo-policy.schema.yaml` |
| Repository validation | `.strange-company/validation.yaml` at the base commit | `repo-validation.schema.yaml` |
| Routing override | `spec.modelRouting` of repository policy | `model-routing.schema.yaml#/$defs/RepositoryRoutingOverride` |
| Observed evidence | framed block in the `sc-verify` step log (plan 20 §8.5) | `red-result` / `green-result` (`stage: observed`) |
| Git facts | Forgejo compare / contents / commits API as `sc-runner` (plan 26 §7.3) | upstream API |
| Tester claims | worker-result `role_output.ac_mapping`, `test_files` | `worker-result.schema.yaml` |
| Acceptance criteria | frozen version on the task | `acceptance-criteria.schema.yaml` |

## 4. Outputs

- Classified `red-result` / `green-result` documents (S3 `evidence/<task>/<phase>/<pipeline>.json`, digest in `sc-pg`).
- Test (or judge) manifests (`evidence/<task>/manifest/g<n>.json`).
- Events for plan 29: `red_classified`, `green_classified`, `judge_classified`, `judging_classified`, `verification_infra_failed`, `verification_evidence_forged`.
- Ingestion verdicts for plan 26 step 6: `TEST_TAMPER` (with sub-codes), `PROTECTED_PATH_VIOLATION`, `ROLE_DIFF_VIOLATION`.
- Merge-time fact sets for plan 55 (statuses and bypass detection).
- Metrics `sc_verification_runs_total`, `sc_verification_duration_seconds`, `sc_policy_violations_total` (§21).

## 5. Dependencies

**Hard (DAG):** F04 ← F02; F06 ← F02, F04; C04 ← C01, P08, F06; C09 ← C03, C04.

**Interfaces assumed from other plans:**
- **Plan 20 (ADR-0301/0302):** trusted `verify` workflow served from the base commit for a registered intent; steps `sc-verify-install` and `sc-verify`; environment `SC_PHASE` (`red`|`green`), `SC_TASK_ID`, `SC_EXPECTED_SHA`, `SC_BASE_SHA`, `SC_INTENT_ID`, `SC_EVIDENCE_NONCE`; clone depth 0. **New request:** the intent also carries `SC_SUBJECT` (`tests`|`judge`), `SC_RED_SHA` (GREEN: the generation's red commit, for the isolation run and RED replay), `SC_PROTECTED_TESTS` (newline-separated `<kind>\t<test id>` lines with kind `change`/`preservation`/`other`, ≤ 64 KiB, GREEN only) and `SC_BASELINE` (`run`|`skip`), injected the same way. Verification-first phases reuse `SC_PHASE`: judge validation = `red`, judging = `green`.
- **Plan 20 §8.5/§8.6:** evidence framing, identity checks I1–I7 and outcomes `EVIDENCE_*`; `EVIDENCE_AMBIGUOUS` is class policy.
- **Plan 26:** Forgejo adapter calls; ingestion order (step 6 calls this plan's tamper function); policy loader calls the merge function.
- **Plan 27:** tester `role_output.ac_mapping` test names; plan 25 role token `@testPaths`, `@protectedTests`.
- **Plan 01:** strange-company's `validation.yaml` (RED `make test GO_TEST_PKGS=… JUNIT=…`, GREEN checks `make lint`, `make contracts`) and `policy.yaml` protected list (§13.2).
- **Plans 26/27 (TDD-14, requested):** the harness container holds no Forgejo credential — a `git` sidecar running `sc-worker` mounts the deploy key and task token, the harness mounts only the gateway token, workspace shared through `emptyDir` — so a tester harness cannot fetch `sc-archive/*` (earlier implementations) or other agent branches. **Plan 25 (TDD-14):** testers never receive `research_reports`/`task_output` produced by an execution whose start commit is not an ancestor of red(g) (`forbidden_for_role: post_red_material`).
- **Plan 25:** role.schema tester `diffRules.allow` gains the token `@stubPaths` and `@fixturePaths`/`@testInfraPaths` (or `@testPaths` expands to all three; plan 25 decides the token, this plan requires the union to be allowed).
- **Plan 28:** fake scenarios used by §22 (listed in plan 21 §22).

## 6. Owned state

- No database tables of its own. The Runner persists evidence pointers (`runner.evidence`, plan 26), verifications and generations (plan 29). This plan owns the **content** of evidence documents and manifests.
- Evidence objects in S3 `<prefix>-evidence`: `evidence/<task>/red/<pipeline>.json`, `evidence/<task>/green/<pipeline>.json`, `evidence/<task>/judge/<pipeline>.json`, `evidence/<task>/manifest/g<n>.json`, and the baseline cache `baselines/<repo>/<base_sha>/<validation_blob>/<sc_verify_version>.json`. Immutable (object versioning on, no overwrite: keys include the pipeline number). Retention: indefinite for manifests and classified evidence (provenance), 90 days for baseline cache (plan 43).
- Only the Runner writes evidence objects. `sc-verify` writes only to its step log.

## 7. External interfaces

### 7.1 `sc-verify` CLI (image `sc-verify`, static Go binary, ADR-0002)

| Command | Use |
|---|---|
| `sc-verify install --dest <dir>` | copies itself into the workspace (plan 20 step 1) |
| `sc-verify run --validation .strange-company/validation.yaml --emit log` | the verification step (§14.1). Exit 0 iff exactly one framed block was emitted; it emits a block also when tests fail. Exit 2 = could not observe (no block) |
| `sc-verify classify --policy <file> --doc <file> --facts <file>` | offline classification with the F06 library (tests, procedures) |
| `sc-verify lint-policy <repo-root>` | validates `.strange-company/policy.yaml` and `validation.yaml` against schemas and the merge rules against a given platform policy (`make policy-check`, plan 26) |

### 7.2 Verifier library (F06, Go package used by the Runner)

| Function | Semantics |
|---|---|
| `MergePolicy(platform, repo) (Effective, error)` | §9.4; `POLICY_LOOSENING` errors carry JSON pointers |
| `ValidateExemption(effective, request, repo) (Decision, error)` | §9.2 |
| `ClassifyRed(observed, scope, declared, criteria, effective) RedClassification` | §14.3 |
| `BuildManifest(classified red, git facts, effective) Manifest` | §14.5 |
| `CheckIngestion(role, diff, manifest, effective) []Violation` | §14.6 (plan 26 step 6) |
| `ClassifyGreen(observed, integrity, manifest, baseline, effective) GreenClassification` | §14.7 |
| `ClassifyJudge(...)` / `ClassifyJudging(...)` | §14.8 |
| `MergeFacts(task, head, git, evidence) FactSet` | §14.9 |

All functions are pure over their inputs; Git facts are passed in by the Runner.

### 7.3 Policy files

Platform: `policy/verification.yaml`. Repository: `.strange-company/policy.yaml`, `.strange-company/validation.yaml` (examples in the schemas).

## 8. Internal interfaces

- `sc-verify` ↔ toolchain (run/emit split, blind TDD-B1; plan 20 owns the workflow shape): the verify workflow runs **`sc-verify run`** in step `sc-verify-run`, which receives **no** `SC_EVIDENCE_NONCE`, `SC_INTENT_ID` or other secret, and **`sc-verify emit`** in a separate step and container `sc-verify-emit` (own PID namespace and `/proc`), which alone receives the nonce, reads the sealed observation and prints the framed block. `sc-verify run` sets `PR_SET_CHILD_SUBREAPER`, executes each suite argv without a shell in its **own process group** with stdout/stderr redirected to `/woodpecker/.sc/out/<suite>-<tree>.log`, and after each suite exits sends SIGKILL to every remaining descendant (process group plus every reparented child) and reaps them **before** any report is read; it then copies each report into `/woodpecker/.sc/sealed/` (outside every test working directory), records its sha256, and parses only the copy. When `run` finishes it writes `observed.json` plus its sha256 and exits; the step container ends, so no process from the code under test survives into `emit`, which verifies the recorded sha256 before framing. **The child environment is built from scratch** (TDD-3): `PATH` (fixed toolchain value), `HOME` and `TMPDIR` (fresh per run), the suite's `env`, and `CI=true` — nothing inherited, no `SC_*` variable.
- Runner evidence poller → F06: after identity checks (plan 20 §8.6) the Runner fetches Git facts (compare `generation_start..head` for RED, `red..head` for GREEN; contents API for manifest blob SHAs and discovery-protected files) and calls the classifier.
- F06 → plan 29: outcome → event (`red_classified` with `classification.outcome`, etc.).

## 9. Configuration

### 9.1 Platform defaults (`policy/verification.yaml`)

Retry policy is the `spec.retry` section of this file (CD-11; no separate `policy/retry.yaml`). `spec.task.maxBaseLag` (default 20 commits and 24 h; repositories may only shorten it) bounds how stale a SHA `base_ref` may be (plan 29 `G_BASE_COMMIT_RESOLVED`, plan 26 §14.1). The full default document is the `examples[0]` of `verification.schema.yaml` and is committed as `policy/verification.yaml` at F04. Key values: `defaultMode: test-first`; protected paths (§9.3); `existingTestModification: deny`; discovery protection list; per-language static patterns; stub policy (`SC-STUB`, ≤ 12 added lines per stub, ≤ 20 stubs, panic stubs only for Rust); adapters; RED timeout 30 m, GREEN 60 m, judge 30 m, 2 infrastructure retriggers each; six exemption classes; merge contexts `ci/woodpecker/pr/ci`, `strange-company/tdd-provenance`, `strange-company/review`, one human approval; non-task PRs `success-human-authored`.

### 9.2 Verification modes and exemption classes

| Class | Allowed paths | Judge kind / source / executor | Approval | Mergeable | Producer |
|---|---|---|---|---|---|
| `documentation-only` | `docs/**`, `**/*.md` minus agent-instruction files, `charts/**`, `**/skills/**`, `context/**`, `**/SOUL.md` (prompt-bearing files) | required-facts / artifact / ci | auto | yes | implementer |
| `research` | none | rubric / acceptance-criteria / runner-local | auto | no | researcher |
| `infrastructure-config` | `gitops/**`, `charts/**`, `deploy/**`, `ansible/**`, `tofu/**` minus SOPS files and Flux system manifests | validation-scenario / artifact / ci | **human** | yes | implementer |
| `migration` | `**/migrations/**` | invariants / artifact / ci | **human** | yes | implementer |
| `benchmark` | `**` (minus protected) | benchmark / artifact / ci | **human** | yes | implementer |
| `metadata` | `**/*.yaml`, `**/*.yml`, `**/*.json` minus `gitops/**`, `charts/**`, `deploy/**`, `ansible/**`, `tofu/**`, `**/migrations/**`, package/tsconfig/go.mod/Cargo/pyproject manifests and CI config (behavior-bearing files belong to human classes) | schema-check / artifact / ci | auto | yes | implementer |
| `refactor` | `**` minus test-classified paths (catch-all, so always human) | characterization / artifact / ci — characterization tests must **pass** at base and at head (TDD-B9) | **human** | yes | implementer |

Researcher-only tasks (including context-change proposals, plan 25 §14.6) use the class **`research`**. Security policy is not an exemption class: security changes are test-first (security assertion tests), and `policy/**` changes are human PRs (protected path).

**Exemption validation at task creation** (`ValidateExemption`):
1. Mode `test-first` → no exemption. Mode `verification-first` requires `exemption_request {class, justification ≥ 20 chars}` from Hermes (MCP) or a human (admin API). Workers cannot create tasks.
2. The class exists in platform policy, is in the repository's `allowedExemptionClasses` (**absent or empty = none**, fail closed; TDD-12), and the repository matches the class's `repositories` → else `EXEMPTION_NOT_ALLOWED` (no fallback).
3. Approval = `human` if the platform class says so **or** the repository lists it in `requireHumanApproval`; else `auto`. Approval is re-computed **per changed path at every producer ingestion**: for each path, the strictest approval of every non-catch-all class whose effective paths match it (most-restrictive-wins). If any path needs `human` and the task's approval is `auto`, the result is rejected (`ROLE_DIFF_VIOLATION`, sub-code `exemption_escalation_required`) — Hermes cannot launder a human-class change through an overlapping auto class (TDD-12).
4. The decision is recorded on the task (`verification.exemption.status = granted` for auto, `pending_approval` for human) with the policy revision. Human approval happens at `JUDGE_AWAITING_APPROVAL` (ADR-0353).
5. At every producer ingestion the **actual diff** is checked against `allowedPaths`/`deniedPaths`/protected paths (`G_PRODUCTION_SCOPE_VALID`); a path outside → `ROLE_DIFF_VIOLATION` (policy). The exemption never covers files outside its class.

Self-exemption is impossible: the mode is fixed at creation by Hermes/human, validated by the Runner, and workers have no channel to change it (`worker-result` has no such field; extra fields are rejected).

### 9.3 Protected paths (default set)

`.woodpecker/**`, `.strange-company/**`, `AGENTS.md`, `**/AGENTS.md`, `CLAUDE.md`, `**/CLAUDE.md`, `.claude/**`, `**/.claude/**`, `.codex/**`, `**/.codex/**`, `.opencode/**`, `**/.opencode/**` (plan 27 Q2 nested instruction files), `policy/**`, `contracts/**`, `CODEOWNERS`, `**/CODEOWNERS`, `.forgejo/**`, `.gitea/**`, `.github/**`, `.gitmodules`, `.gitattributes`, `.sops.yaml`, `**/*.sops.yaml`, `forge/**` (forge desired state incl. branch protection, plan 16), `gitops/**/generated/**` (rendered configuration such as `gitops/platform/strange-company/generated/**`, plan 54) and `gitops/platform/admission/**` (admission policies, plan 37). Repositories add (union) — strange-company adds `Makefile`, `mk/**`, `toolbox/**` and the Flux system manifests (plan 01 §13.2 is authoritative for that repository). Any agent diff touching one → `PROTECTED_PATH_VIOLATION` (ADR-0008), evaluated at ingestion, at GREEN (integrity) and at merge (§14.9).

**Enforcement plane of `strange-company` (TDD-B4, SEC-B8).** Code that decides, enforces or tests the TDD and security gates must be in `strange-company`'s `merge.elevatedPaths`: `src/internal/{verify,statemachine,policy,runner,forge,gateway,ledger,jobbuilder,scrub,lint}/**` and `src/cmd/{sc-verify,sc-ci-config,agent-runner,sc-worker,model-gateway,sc-gitops,egress-proxy,sc-release,sc-e2e}/**`. The C10 self-test trajectories `test/e2e/**` are a **protected path** of `strange-company` (humans only), and its `testPaths` never include them. Loader invariant (F04): for the repository `strange-company` every glob of this list must be covered by `elevatedPaths` (or protected), else the repository policy is rejected (`ENFORCEMENT_PLANE_NOT_ELEVATED`). Gitlinks (mode `160000`) are denied in every autonomous diff unless the repository policy sets `allowGitlinks: true` (not in MVP).

### 9.4 Tighten-only merge (`MergePolicy`)

| Field kind | Examples | Merge | Loosening attempt |
|---|---|---|---|
| protected / denied sets | `protectedPaths`, `protectedBranches`, discovery `wholeFiles`/`semanticKeys`, static patterns, `machineAccounts` | union | n/a |
| allow sets | `allowedExemptionClasses`, `forgejoTeams`, `stubPanicLanguages`, routing `allowedProviders` | intersection | naming an item absent from platform → `POLICY_LOOSENING` |
| maxima | retry maxima, `maxConflictsPerTask`, diff limits, routing `maxTier` | min | value > platform → `POLICY_LOOSENING` |
| minima | `humanApprovalsRequired` | max | value < platform → `POLICY_LOOSENING` |
| durations | watchdog groups | min | longer → `POLICY_LOOSENING` |
| ordered enums | `existingTestModification` (deny > flag), `resolutionApproval` (human > adjudicator), approval (human > auto) | stricter | schema forbids the looser value (`const`) |
| definitions | `testPaths`, `fixturePaths`, `testInfraPaths`, `stubPaths` | repository-only | catch-all globs rejected by schema; glob matching a protected path → `POLICY_LOOSENING` |

A file that fails schema or merge is invalid as a whole; task creation fails `VALIDATION_FAILED` with `details.allowed = ["POLICY_LOOSENING:<pointer>"]` (or `REPO_POLICY_INVALID:<pointer>`). The previous base revision's policy is never used as fallback.

### 9.5 Loader invariants (F04/F06)

Beyond the schema: unique class names; `approval: auto` only with a deterministic judge (`required-facts`, `validation-scenario`, `invariants`, `benchmark`, `schema-check`) or `producesMergeableChange: false`; a catch-all class (allowedPaths contains `**`) is always `human` (schema); the effective paths of an `auto` class (allowedPaths minus deniedPaths) do not intersect the allowedPaths of any non-catch-all `human` class; no allowedPaths glob lies **entirely** inside the protected set (dead entry) — intersection with protected paths is allowed because ingestion subtracts them (this is the single wording used by schema and plan); every RE2 pattern compiles; `merge.requiredStatusContexts` ⊇ the two Runner contexts; `retry.policy.maxViolations == 0`; adapters `assertionTypes` non-empty. Repository side: definition globs do not intersect the protected set; at least one suite with phases `red` and `green` for test-first tasks.

### 9.6 Merge approvals and single-operator mode (CD-38, CD-39; text shared with plan 55 §7.2)

- Merge and approval are human-only: on every protected branch the merge whitelist and the approvals whitelist are team `sc-mergers`, admin override is blocked, and no `sc-*` machine account may be a member (plan 16 `forge.yaml`, CD-38).
- `N` = number of members of `sc-mergers`, recorded in installation config. Agent-authored (task) PRs need `agentPullRequestApprovals` = 1 human approval; operator-authored PRs need `operatorPullRequestApprovals` = 0 approvals but can be merged only by `sc-mergers` (human authorship is the review).
- PRs touching `merge.elevatedPaths` need `min(elevatedApprovals, N)` approvals from `sc-mergers` on the head SHA. When `N = 1` (single-operator mode), `strange-company/review` additionally stays `pending` until `singleOperatorCoolingOff` (24 h) after the last push touching an elevated path, and the start of the window is announced to the operator's notification channel (plan 55 §7.2). Emergency merge (plan 55 §14.8a) is the only bypass. Recorded as SI-59.

## 10. Secrets

N/A — no new secret class. Verification pipelines receive **no secrets** (ADR-0008, ADR-0301). `sc-verify` scrubs test output tails and messages with generic patterns (PEM blocks, `sk-…`/`ghp_…`-style tokens, JWTs, high-entropy `KEY=value`) before emitting, and the Runner re-scrubs on ingestion with the known-secret set (plan 26 step 7). The evidence nonce is not a secret class (single-use, per intent; plan 20).

## 11. Storage

S3 `evidence` (§6): a classified document ≤ 512 KiB (observed ≤ 256 KiB + Runner additions), a manifest ≤ 256 KiB. At 20 tasks/day × ~6 documents ≈ 60 MiB/day upper bound; typical ≪ 10 MiB/day. No PVC.

## 12. Resource requirements

- `sc-verify` runs inside the CI step's resource envelope (plan 20 lane defaults; plan 51). Its own overhead ≤ 50 MiB RSS and ≤ 5% CPU besides the test processes.
- RED runs suites twice (base and head) unless the baseline cache applies (§14.1 step 3), so RED wall time ≈ 2 × suite time; `redScope` (e.g. Go packages of changed test files) bounds it. Target: strange-company RED ≤ 10 min p95 (plan 20 20-AC-34).
- F06 classification in the Runner: ≤ 200 ms CPU per document.

## 13. Security boundary

- **Trust levels.** `sc-verify run` executes next to untrusted code (tests, implementation) with the same uid; its observations are produced **inside the code under test's sandbox** and could be influenced by it. The run/emit split (§8) keeps the nonce out of that sandbox, the subreaper and sealed report copies stop post-suite rewrites, and `emit` frames only the sealed observation. The Runner's classification uses Git facts it computes itself (§14.11: protected-path diffs, manifest blob SHAs, discovery files, stub shapes, ancestry), which code under test cannot influence. What code under test **can still** forge: per-test pass/fail lines it prints itself into a report format that trusts stdout (SI-07; reduced for Go, see §14.1). The reviewer packet and the PR body state: "GREEN observations are produced inside the code under test." Mitigations: manifest immutability and blob checks, static diff checks, review with diff + evidence (SPEC §55), human merge.
- **Base-revision configuration.** Commands come from `validation.yaml` at the base commit; protected paths make head == base for that file; `sc-verify` reports the blob it read and the Runner rejects a mismatch (`validation_config_mismatch`).
- **No secrets in CI**, NetworkPolicy of `ci-jobs` (plan 36): egress only to package registries allowed by plan 20/36.
- Evidence ingestion is in the Runner (trusted). `sc-verify` has no credential and no write path other than its log.

## 14. Happy path

### 14.1 `sc-verify run` (inside the `verify` workflow)

1. Read `SC_*` (§5); read the nonce and drop it from the environment; verify `git rev-parse HEAD == SC_EXPECTED_SHA` (else emit nothing, exit 2 → `EVIDENCE_MISSING`, infrastructure).
2. Load `.strange-company/validation.yaml` (schema-validate; record its blob id).
3. **Base run** (RED with `SC_BASELINE=run`, subject `tests`): `git worktree add .sc/base $SC_BASE_SHA`; run **every suite in full** (never `redScope`-limited, so the full baseline also serves GREEN rule G5b and can be cached by the Runner per (base SHA, validation blob, sc-verify version)); parse the reports, compute the base `test_id_sets` (passing, skipped), then **seal** the base results in memory and delete `.sc/base` before the head run starts, so code under test cannot rewrite base reports (TDD-23).
4. **Head run**: RED: suites with `redScope` appended and `{protected_test_packages}` = Go package patterns `./<dir>/...` of test files changed in `base..HEAD` — **unscoped** when the red range touches fixturePaths or testInfraPaths (TDD-23); the new and declared tests are then re-run `red.repeatNewTests` times (default 3) through `selectArgv` (full suite if absent) and any status change is recorded in `repeats.inconsistent`. GREEN: full suites, `repeatProtectedTests` (3) runs of the manifest tests, then the **isolation run** (G4b: a copy of the workspace with every test-, fixture- or test-infra-classified file added after `SC_RED_SHA` removed — classification from base-revision `policy.yaml` globs and `git diff --diff-filter=A $SC_RED_SHA..HEAD` — and discovery whole-files restored to their red-commit content; the manifest tests are run there), then the **RED replay** (G9: `git archive $SC_RED_SHA` exported without `.git` into a fresh directory, the change-criterion manifest tests run there in the same environment), then every `checks` entry (`toolingFrom: base` checks run from a base worktree with `SC_TARGET_TREE` = the head workspace, TDD-15). Every run uses the from-scratch child environment of §8.
5. **Normalize** each test to `TestId = <suite>::<id>` where `<id>` follows the adapter `idTemplate` (Go: `{package}.{test}` including subtests as `{test}/{subtest}`; pytest: `{file}::{name}` with parametrization ids; jest/vitest: `{file}::{describe path} {name}`; cargo-nextest: `{binary}::{name}`), and its status via the adapter rules: a failure whose type or first message line matches `assertionTypes` → `failed_assertion`; matches `errorTypes`, a timeout pattern, or matches neither → `failed_error` (fail closed); message containing `SC-STUB-NOT-IMPLEMENTED` → `stub_unimplemented`; JUnit `<skipped>`/go `skip` → `skipped`; not in the report → `absent`. A suite whose report is missing, or whose build/collection failed (`buildFailurePatterns`, go `[build failed]`), sets `build_failed`/`report_found=false`.
6. **Select tests to report** (size bound): every test absent at base, every test whose status differs between trees, every non-passing test, and (GREEN) every ID in `SC_PROTECTED_TESTS`. Over 2000 → `tests_truncated: true`. In addition the complete ID sets are emitted compressed (`test_id_sets`: RED base passing/skipped and head present; GREEN head present/skipped) for the full-suite comparisons R8/G5b.
7. Judge subject: run the judge artifact's checks (`required-facts` evaluated by sc-verify itself with RE2 over the matched files; `command` checks executed like suites; `threshold` checks parse `METRIC <name> <value>` lines from the command's stdout).
8. Emit the `observed` document as the single framed block (plan 20 §8.5); exit 0.

**Version-sensitive facts** (CD-29 baseline: Go go1.27.x for `go test -json`/test2json, `plans/compatibility-baseline.yaml`; pytest, jest, vitest and cargo-nextest are pinned by each repository's toolchain image digest in `validation.yaml`, and the fixture repositories pin the versions the adapters were written against — the lead is asked to add them as baseline entries). Re-verified by 22-AC-12 with recorded fixture repositories per adapter: `go test -json` action names and build-failure output, and that a test binary printing `--- PASS: X` lines does not change the reported status of `X` under test2json framing at go1.27 (verify); pytest JUnit `xunit2` failure `message`/`type` attributes; jest-junit `addFileAttribute` and suite-failure reporting; vitest JUnit reporter attributes; cargo-nextest JUnit output path and `classname`.

### 14.2 Red scope and stubs (ADR-0352)

Every path in `generation_start..red_commit` is classified (Runner, from Forgejo): `test` (testPaths), `fixture`, `test_infra`, `stub` (in stubPaths and passes the stub check), `judge`, else `other`. Any `other`, any deletion of a non-test file, or any protected path → RED `red_invalid` with `scope_violation` (plan 26 ingestion already rejects most as `ROLE_DIFF_VIOLATION`; this is defense in depth).

**Stub check** (statically typed languages need a symbol to compile against; dynamic languages need it to avoid `NameError`/`AttributeError`, which are error-class):
- The hunk is **additions only** in an existing or new file under `stubPaths`.
- Each added declaration carries the marker comment `SC-STUB(<task-id> AC-n[ AC-m…])` on the line immediately before it.
- Its body has ≤ `maxAddedLinesPerStub` lines and is either **zero-value** (returns zero values / `None` / `null` / `Default::default()`; Go checked by AST, others by a per-language line regex) or **panic** (`panic("SC-STUB-NOT-IMPLEMENTED")`, `raise NotImplementedError("SC-STUB-NOT-IMPLEMENTED")`, `todo!("SC-STUB-NOT-IMPLEMENTED")`); panic stubs count as RED evidence only for languages in `allowPanicStubsFor` (default Rust, where many types lack a cheap zero value).
- ≤ `maxStubsPerTask` stubs. A malformed stub → `stub_malformed` (`red_invalid`).
- Tests that only fail to compile or import (no stub) → `build_failed`/`error_class_failure` → `TEST_NEEDS_WORK` with evidence telling the tester to add declared stubs [SPEC §50 "a test that merely fails to compile is not automatically useful RED evidence"].
- GREEN requires every manifest stub's marker to be gone (`stub_not_replaced`, merit).

### 14.3 Valid RED (exact rules, `ClassifyRed`)

Inputs: observed doc, scope, declared mapping (tester `ac_mapping`, normalized to TestIds; a declared name that matches no reported ID by exact or `::`-suffix match is "not discovered"), frozen criteria, effective policy. Rules are evaluated in order; the first failing **blocking** rule decides; all failing rules are listed in `reasons`.

| # | Rule | Failing outcome (reason) |
|---|---|---|
| R0 | Pipeline identity checks passed (plan 20 I1–I7) and `validation_blob` equals the base revision's blob | not classified: `verification_infra_failed` (`EVIDENCE_*`) / `red_invalid` (`validation_config_mismatch`) |
| R1 | Scope valid (§14.2); a tester may **not delete or rename any file** (consistent with plan 25 tester `diffRules`), so pre-existing tests can only be changed, visibly to R8 (TDD-24) | `red_invalid` (`scope_violation`, `tester_deletion`, `stub_malformed`, `panic_stub_not_allowed`) |
| R1b | Existing tests not weakened (blind TDD-B2): a tester change to a testPaths/fixturePaths/testInfraPaths file that existed at base is **additions only outside the bodies of existing test functions** (new functions or new files), determined from the Go AST or the adapter's line ranges; a removed or changed line is allowed only inside a function mapped to a `change` criterion of this task. Every existing test file touched is listed in the reviewer packet | `red_invalid` (`existing_test_weakened`) |
| R2 | Base run: every suite built, reported, not timed out, and no test `failed_assertion`/`failed_error` at base | `baseline_failing` (`baseline_failing`) → NEEDS_INVESTIGATION. A test failing at base and passing at head with unchanged file → `flaky_baseline` → `inconclusive` |
| R3 | Head run: every suite built and reported, none timed out | `red_invalid` (`build_failed`, `report_missing`, `suite_timed_out`) |
| R4 | New tests exist: set `N` = TestIds `absent` at base and present at head; `N ∪ M ≠ ∅` where `M` = declared tests that existed at base, passed at base and whose file changed in the red range | `red_invalid` (`no_new_tests`) |
| R5 | Every declared test is discovered at head, is in `N ∪ M`, and is not `skipped` | `red_invalid` (`declared_test_not_discovered`, `declared_test_not_new`, `new_test_skipped`) |
| R6 | Coverage: every criterion of the frozen version has ≥ 1 declared test | `red_invalid` (`ac_not_covered`) |
| R7 | No test in `N ∪ M` ends `failed_error` (and `stub_unimplemented` only where panic stubs are allowed) | `red_invalid` (`error_class_failure`, `panic_stub_not_allowed`) |
| R8 | Pre-existing tests **within the RED head scope**: every test in the base `passing` set that belongs to the scope the head run executed (all tests when the head run is unscoped; with `redScope`, only base IDs of the scoped packages — redScope is defined only for Go suites, where a test ID's package prefix decides membership; every other suite runs unscoped at RED) and is not in `M` passes at head; `absent` or `skipped` counts as not passing. Tests outside the RED scope are compared by GREEN rule G5b against the same full base set (TDD-4) | `red_invalid` (`preexisting_test_broken`, `preexisting_test_removed`) |
| R9 | `preservation` criteria: each has ≥ 1 mapped test `passed` at head | `red_invalid` (`preservation_ac_not_passing`) |
| R10 | `change` criteria: each has ≥ 1 mapped test `failed_assertion` (or allowed `stub_unimplemented`) at head | none failing → all change criteria satisfied → `already_satisfied`; some satisfied → `partially_satisfied` (NEEDS_INVESTIGATION) |
| R11 | `tests_truncated` is false or every test in `N ∪ M` and every declared test is in the reported list | `inconclusive` (`tests_truncated`) |
| R11b | Determinism: across `repeatNewTests` runs every test in `N ∪ M` has the same status (TDD-3); one of the repeats runs on a copy of the head tree with every `SC-STUB(...)` marker comment line removed and nothing else changed — a status change there means the test keys on the marker (blind TDD-B6) | `inconclusive` (`nondeterministic_test`) / `red_invalid` (`marker_conditional`) |
| R12 | Superset (generations opened by `strengthened`, which require a human `/sc strengthen-tests`, and tester attempts after `ALREADY_SATISFIED`/`NEEDS_INVESTIGATION`): every manifest test of the previous generation (or every previously declared test of the episode) is still declared and present; every previous manifest file is unchanged except for **whole new functions or files** — no line is inserted into, or removed from, the body of a function that already exists in the manifest (no early `return`, `defer recover()`, `t.Skip`, `t.Cleanup` or helper call added; TDD-B3). The PR body shows the strengthening diff `red(g)..red(g+1)` | `red_invalid` (`manifest_not_superset`) |
| R13 | Approved scope (generations opened by `test_change_approved`): every manifest test and file of red(g) **not** mapped to the conflict's approved `ac_ids` is present and blob-identical at red(g+1) (TDD-8) | `red_invalid` (`unapproved_test_change`) |
| R14 | Size (TDD-B8): the serialized protected-test list (`<kind>\t<id>` lines) is ≤ 60 KiB, below the 64 KiB intent limit used at GREEN, and the manifest stays within its schema limits | `red_invalid` (`manifest_too_large`; tester retry with guidance to consolidate tests) |

Warnings (never verdicts, shown to the reviewer and in the PR body, TDD-21): `shared_test_mapping` (a test mapped to > 1 change criterion, or fewer distinct change tests than change criteria), `preservation_test_new_file_only` (a preservation criterion whose only tests are new), `environment_dependence` (a manifest test reads `SC_*`, `/proc/`, invokes `git`, or uses clock/random values in assertions).

All pass → **`red_valid`**. This realizes SPEC §50: (1) "the relevant test runs" = R3, R5; (2) "≥ 1 new requirement-specific assertion fails" = R10 with R4/R6 (stricter: every change criterion); (3) "failure corresponds to absent behavior" = R2 (base green), R7 (assertion-class only), R8 (no collateral breakage), the stub policy (failure reaches the assertion through a declared stand-in for the absent behavior); (4) "not broken infrastructure, syntax error, missing dependency" = R3, R7; "deliberately impossible test" = §14.4.

### 14.4 Heuristics and honest limits

| Concern | Mechanism | Strength |
|---|---|---|
| Deliberately impossible, phase-, environment- or marker-conditional test | `unconditionalFailure` patterns and `environment_dependence` warnings; `source_read_in_test` (a manifest test reading a non-test, non-fixture repository file: warning for the reviewer, **violation** when the file is under stubPaths); R11b determinism (×3, one repeat with `SC-STUB` markers stripped); **G9 RED replay** against the red-commit export **with the markers stripped** (TDD-B6); from-scratch child environment (§8); reviewer check `red_failed_for_right_reason` | Literal impossibility is caught only by patterns, but such a test can never become GREEN without a human-approved conflict. Tests conditional on the phase, the environment, the commit subject or the stub marker are caught by R11b/G9. A test conditional on other head-only content that differs from the red commit is the behavior change G9 tolerates; reviewer and human merge remain the control (SI-07 extension). |
| Test unrelated to the criterion | AC mapping (R6) + `ac_tag_missing` warning when the test file lacks the `AC-n` token | The mapping is a claim. Semantic fit is judged by the reviewer. |
| Test depends on network/clock | `network_or_clock_dependence` warning (patterns: `time.Now()` in assertions, `http.Get("http`) | Heuristic. CI egress restrictions (plan 36) make network tests fail at base (R2). |
| Weak test (passes for wrong implementations) | none deterministic in MVP; mutation testing is post-MVP | Reviewer and human merge only. |
| Mocks that eliminate behavior | implementer may not modify any test-classified file that existed at the red commit (`existing_test_modified`, TDD-1); **G4b isolation run**: manifest tests must pass without any test-classified file added after the red commit, so a new `zz_test.go` `init`, `TestMain`, module-level monkeypatch, `conftest.py` or `__mocks__` cannot be what makes them pass (TDD-2); `test_env_detection` in non-test code is a violation; `new_test_file_init_hook` warnings for `func init(`, `TestMain`, module-level assignments to imported names and new `__mocks__/` in implementer-added test files; test-dependency paths and semantic keys (`go.mod replace`, `Cargo.toml [patch]`/`dev-dependencies`, `package.json devDependencies`, `vendor/**`) are protected | Production-code special-casing of test inputs is not detected deterministically; `test_literal_leakage` warns. |
| Forged observations by code under test | ADR-0302 nonce + single-block rule; manifest blob checks; review | Residual risk SI-07. |

### 14.5 Manifest creation (`BuildManifest`, on `red_valid`)

`files` = every path in `base..red_commit` classified `test`, `fixture`, `test_infra` with blob SHAs from Forgejo at `red_commit` (cumulative over generations: generation n's manifest includes generation n−1's files, updated blobs); `tests` = `N ∪ M` plus earlier generations' tests still present, each with `ac_ids` (declared mapping), `kind`, `red_status`; `ac_mapping`; `stubs` from scope; `discovery_protection` = blobs (or absence) at `red_commit` of every platform+repository `wholeFiles` glob match **plus** every file referenced by a suite/check argv (`argv[0]` if a repo path; `Makefile`/`GNUmakefile`/`makefile` when `argv[0] == make`, resolved **transitively** through `include`, `-include`, `-f` and literal `$(wildcard …)` patterns, and every script a recipe invokes by repository path) and every file in a check's `configPaths` (`.golangci.yml`, `ruff.toml`, `.eslintrc*`, …; blind TDD-B5); `make policy-check` warns when a check's tool configuration is not covered. Checks default to `toolingFrom: base`, and canonical-JSON sha256 of each configured semantic key; `test_paths` snapshot; `supersedes`. Digest = sha256 of canonical JSON. Stored before the `RED_VERIFIED` transition commits (effect order within T020).

### 14.6 Test protection (implementer and producer diffs)

`CheckIngestion` runs at plan 26 step 6 on `start..commit` of every implementer/producer result, and again at GREEN on `red..head` (`integrity`). Violations (`TEST_TAMPER` with sub-code, class policy → `NEEDS_HUMAN`, branch quarantined):

| Sub-code | Rule |
|---|---|
| `manifest_file_changed` | a manifest file's blob differs, or it is deleted or renamed |
| `test_file_deleted` | any deletion or rename of a file matching testPaths, fixturePaths or testInfraPaths |
| `existing_test_modified` | modification of any file that existed at the red commit and matches testPaths, fixturePaths or testInfraPaths but is not in the manifest — tests, golden files, fixtures, test helpers (policy `deny`, TDD-1) |
| `test_dependency_changed` | any change under `discoveryConfig.testDependencyPaths` (`vendor/**`, …) or of a test-dependency semantic key (`go.mod replace`, `Cargo.toml patch`/`dev-dependencies`, `package.json devDependencies`) |
| `skip_marker_added` | an added line in a testPaths file matches `skipMarkers` for its language |
| `discovery_config_changed` | a `discovery_protection` whole file changed/appeared, a semantic key value changed (incl. `tsconfig*.json` `compilerOptions.paths`), or a file matching `forbidNewFiles` was added (nested `go.mod`, `pytest.ini`, babel config, …; TDD-4) |
| `test_env_detection` | an added line in a non-test file matches `testEnvDetection` |
| `protected_path_changed` | (reported as `PROTECTED_PATH_VIOLATION`) |

Allowed for implementers: **new** test files under testPaths (their own tests; not protected, may not be skipped later; they cannot influence protected tests because of the G4b isolation run), production code, stub replacement. The only path to change a protected test, golden, fixture or helper is the TEST_SPEC_CONFLICT flow (plan 21 §14.6): a new red commit by a tester and a new RED verification (SPEC §53 "distinct reviewable action"). Limits: production-side special-casing of test inputs and test-environment detection outside the listed patterns are not detected deterministically (§14.4).

### 14.7 Valid GREEN (`ClassifyGreen`)

| # | Rule | Failing outcome (reason) |
|---|---|---|
| G0 | Identity (plan 20) + `validation_blob` | infra / `validation_config_mismatch` (green_fail) |
| G1 | `head_descends_from_red` (Forgejo compare: `red_commit` reachable from head) | `tamper_detected` (`not_descendant_of_red`) |
| G2 | Integrity: no manifest/discovery/static violation (§14.6), no protected path, no `foreign_commits` — every commit SHA in `red..head` is a **recorded accepted result commit** of this task or a **recorded `human_commit`** (`/sc accept-external-commit`); trailers are never used (TDD-9) | `tamper_detected` |
| G3 | Every suite built, reported, not timed out | `green_fail` (`build_failed`, `report_missing`, `suite_timed_out`) |
| G4 | Every manifest test discovered (`protected_tests_missing` empty), not skipped, `passed` | missing/skipped → `tamper_detected` (`protected_test_missing`, `protected_test_skipped`); failing → `green_fail` (`protected_test_failing`) |
| G4b | Isolation run: every manifest test passes in head minus test-classified files added after the red commit (TDD-2) | `tamper_detected` (`protected_pass_depends_on_new_test_files`) |
| G4c | Every manifest test passes in all `repeatProtectedTests` runs | `green_fail` (`nondeterministic_test`) |
| G5 | Every other test passes or is skipped; `absent` counts as not passing; no test that passed at base (red baseline) is skipped now; every `protectedTestIds` passes | `green_fail` (`existing_test_failing`); new skip → `tamper_detected` (`new_skip_vs_base`) |
| G5b | Full-suite comparison: every test in the RED full-baseline `passing` set is present and not skipped in the head `test_id_sets`, unless its file was deleted through an approved generation (TDD-4) | `tamper_detected` (`preexisting_test_removed`) |
| G6 | Every `checks` entry exit 0, not timed out (`toolingFrom: base` checks use base-built tooling, TDD-15) | `green_fail` (`required_check_failed`) |
| G7 | No manifest stub marker remains | `green_fail` (`stub_not_replaced`) |
| G8 | `tests_truncated` false or all protected tests reported | `green_fail` (`tests_truncated`) |
| G9 | RED replay: every change-criterion manifest test still ends `failed_assertion` (or allowed `stub_unimplemented`) against the red-commit export **with every `SC-STUB(...)` marker line removed**, in the GREEN environment (TDD-3, TDD-B6) | `tamper_detected` (`red_not_reproducible`, `marker_conditional`) |

All pass → **`green_pass`** [SPEC §54]. GREEN evidence binds to the exact head SHA; any head change invalidates it (plan 55 §14.5).

### 14.8 Verification-first judges

- **Judge validation** (`SC_PHASE=red`, `SC_SUBJECT=judge`, at the judge commit): the judge file validates against `verification.schema.yaml#/$defs/JudgeArtifact`, its `exemptionClass` equals the task's, every frozen criterion has ≥ 1 check, every check runs without `error`, and **at least one check fails** at the judge commit (the answer does not exist yet). Outcomes: `judge_valid`; `judge_invalid` (`judge_check_error`, `ac_not_covered`, schema); `already_satisfied` (`judge_passes_on_base`: every check passes); `partially_satisfied` (some ACs' checks all pass).
- **Judging** (`SC_PHASE=green`, `SC_SUBJECT=judge`): judge artifact files byte-identical to the judge manifest (`judge_artifact_changed` → `tamper_detected`), producer diff within class paths, all checks `pass`, required `checks` of validation.yaml pass → `judging_pass`, else `judging_fail`.
- **Characterization judges** (`refactor` class, TDD-B9): judge validation requires every check (characterization test) to **pass** at the judge commit and at base — there is no "must fail" rule and `judge_passes_on_base` is not an outcome; judging requires them to pass at the produced head with the judge manifest intact, and the producer diff must not touch any test-classified path.
- **Rubric judges** (`kind: rubric`): no CI evaluation; approval human (or ACs-as-judge with `runner-local` executor for `research`, where JUDGING checks only that the research report validates, is non-empty and addresses every AC id by heading — deterministic, Runner-local); the reviewer applies the rubric at REVIEW.

### 14.9 CI enforcement of RED/GREEN provenance: merge-time facts (SPEC §102)

`MergeFacts(task, head)` — computed by the Runner from **its own database, S3 evidence and Forgejo**, never from statuses or labels; used for `strange-company/tdd-provenance` (plan 55) and re-run at merge:

| # | SPEC §102 fact | Computation | Source |
|---|---|---|---|
| F1 | acceptance criteria exist | current AC version `frozen` with ≥ 1 criterion | `sc-pg` |
| F2 | test execution exists | current generation has a tester execution `SUCCEEDED`, result `accepted` | `sc-pg` |
| F3 | test commit exists | generation `test_commit` set and `GET /repos/{o}/{r}/git/commits/{sha}` = 200 | `sc-pg` + Forgejo |
| F4 | red verification exists | a `verifications` row phase `red` for `test_commit`, pipeline created by `sc-runner`, evidence object present with sha256 equal to the recorded digest (re-hashed) | `sc-pg` + S3 |
| F5 | red verification valid | that document's Runner classification is `red_valid` | S3 |
| F6 | implementation descends from red | `git merge-base --is-ancestor <test_commit> <head>` in the Runner's mirror (§14.11; TDD-B7) | Runner Git mirror |
| F7 | protected acceptance tests intact | for every manifest file, `git ls-tree <head> -- <path>` (Runner mirror, §14.11) gives the manifest blob and mode; no file that existed at the red commit and matches the test-classified globs changed in `red..head`; discovery protection unchanged; no protected path changed in `base..head`, evaluated against the **union** of the base-revision and target-branch-head protected sets and test globs (TDD-1, TDD-10) | Forgejo + manifest |
| F8 | green verification succeeds | `green_pass` evidence whose `head_sha` equals the PR head | `sc-pg` + S3 |
| F9 | required review succeeds | latest `final_review` verdict `accept` with all five checks `pass` and `reviewed_commit` == PR head | `sc-pg` |
| F10 | (derived) no foreign commits | every commit SHA in `base..head` is a recorded accepted result commit of this task or a recorded `human_commit` (never trailer-based, TDD-9) | Forgejo + `sc-pg` |
| F11 | (derived) the merge is the task's merge | the PR is `task.pull_request.number` and its base branch is `task.target_branch`; any other PR whose head maps to the task gets `failure` (TDD-11) | Forgejo + `sc-pg` |
| F12 | (derived) policy unchanged since base | `.strange-company/{policy,validation,repo}.yaml` and the `policy/**` revision at the target-branch head equal those at `base_commit`; otherwise `failure: policy_changed_since_base` and the task must be re-verified on a fresh base (TDD-10) | Forgejo + `sc-pg` |

Verification-first equivalents: F1; F2–F5 → judge commit exists, judge validation `judge_valid` (or rubric approval) **and** judge approval recorded (auto or human); F6 → head descends from the judge commit; F7 → judge manifest intact and producer diff within class paths; F8 → `judging_pass` at head; F9 as above. "Authorized exemption" (SPEC §102) = the verification-first fact set with a recorded exemption decision under the policy revision in force.

### 14.11 Diff derivation (blind TDD-B7, SEC-B3; plan 26 implements)

Every diff-based control — protected paths, role allow/deny, `TEST_TAMPER` sub-codes, deletions, red scope, stub checks, R1b/R12/R13, G2, F6, F7, F10 — uses diffs the **Runner computes from Git itself**, never the Forgejo compare API:

1. The Runner keeps a bare mirror per managed repository (emptyDir/scratch, plan 26) and fetches the exact SHAs with its read-only credential (`git fetch --no-tags origin <sha>…`; refs are never trusted, only object ids).
2. Changed paths: `git diff --raw -z --no-renames --no-ext-diff --no-textconv <a> <b>`. A rename is a delete plus an add, so both old and new paths are checked. Mode changes (`100644`↔`100755`), type changes (file↔symlink `120000`) and gitlinks (`160000`, denied unless policy allows) count as modifications. Binary files are compared by blob id. There is no truncation: the full list is read; a range whose file count exceeds the role's `maxFilesChanged` is rejected (`DIFF_TOO_LARGE`) **before** any other rule is evaluated, so no check ever sees a partial list.
3. Commits: `git rev-list --reverse <a>..<b>`; ancestry: `git merge-base --is-ancestor`; trees and blobs: `git ls-tree -r -z`, `git cat-file`.
4. Diff text for static checks (skip markers, env detection, stubs, R1b/R12 function bodies): `git diff -U0 --no-color --no-ext-diff --no-textconv <a> <b>` per path.
5. A Forgejo compare response may be used only as a cross-check; any disagreement with the Runner's own list is an infrastructure failure (`DIFF_UNVERIFIABLE`), never a success.

### 14.10 Pipeline timeouts and retriggers

The Runner cancels a verification pipeline not terminal by `pipelineTimeout` (RED 30 m, GREEN 60 m, judge 30 m) → `verification_infra_failed`. Infrastructure outcomes (pipeline error, `EVIDENCE_MISSING/TRUNCATED/CORRUPT/SCHEMA_INVALID/LOG_TOO_LARGE/PIPELINE_LOST`, `EVIDENCE_COMMIT_MISMATCH`, timeout) re-trigger the same phase at the same SHA up to `maxInfraRetriggers` (2), then `NEEDS_HUMAN` (plan 29 T025). `EVIDENCE_AMBIGUOUS` → `verification_evidence_forged` (policy, no retry, T026). Merit outcomes are never re-triggered.

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Platform policy invalid | F04 loader | dispatch disabled | LKG kept, `SCRunnerPolicyInvalid` (plan 26/42) | fix by PR |
| Repository policy loosens platform | `MergePolicy` | task creation refused | `VALIDATION_FAILED` with pointer | fix repository policy by human PR |
| `validation.yaml` missing/invalid | creation-time check | test-first refused | error to Hermes | add file by human PR |
| Adapter cannot parse a report | `report_found=false` | RED `red_invalid` / GREEN `green_fail` | tester/implementer evidence | `tdd-task-recovery.md` |
| Tool output format changed after upgrade | 22-AC-12 fixture tests fail in CI | upgrade blocked | none | update adapter rules by PR |
| Baseline broken on main | R2 | tasks to NEEDS_INVESTIGATION | notification | `tdd-task-recovery.md` §baseline |
| Flaky test | R2 `flaky_baseline`, or GREEN fails intermittently on non-manifest tests | inconclusive / green_fail | investigation; quarantine via plan 52 `quarantine-check` | `tdd-task-recovery.md` |
| Evidence forged (ambiguous block) | plan 20 §8.5 | NEEDS_HUMAN | `SCTddProvenanceBypass` | `tdd-task-recovery.md` §forgery |
| Evidence missing/corrupt | plan 20 §8.5 | retrigger ≤ 2 | then NEEDS_HUMAN | `tdd-task-recovery.md` |
| Classification bug (wrong verdict) | reviewer finding, human | wrong state | none | `tdd-task-recovery.md` §reclassify (offline `sc-verify classify`, then `/sc reverify`) |
| Manifest object lost (S3) | F4/F7 re-hash failure | provenance fails at merge | status `failure` | `tdd-task-recovery.md` §evidence-loss |
| Evidence store unavailable | upload error | transition not applied (manifest must be stored first) | retried via outbox | `object-storage-outage.md` |

## 16. Retry behavior

Verification retries are infrastructure-only (§14.10). Merit verdicts are final for that commit; the next attempt is a new execution (plan 29). Classification itself is deterministic: re-classifying the same observed document under the same policy revision yields byte-identical classification (22-AC-3), so the Runner may re-run it safely after a crash.

## 17. Idempotency behavior

- Evidence keys include the pipeline number; storing twice is a no-op (same bytes, same digest).
- Manifest per (task, generation) is written once; a second `BuildManifest` for the same generation must produce the same digest or the Runner refuses (`SCTaskInvariantViolation`).
- The baseline cache key `(repo, base_sha, validation_blob, sc_verify_version)` makes reuse deterministic; a cached baseline is used only if its document digest is recorded in `sc-pg`.

## 18. Bootstrap behavior

- F04/F06 ship the platform `policy/verification.yaml` and the schemas; `make policy-check` validates.
- Until P08 (Woodpecker), RED/GREEN for Phase-1 work is the human PR discipline of plan 52 (`tdd-discipline-check`, ADR-0018), which re-runs the RED commit in CI for human PRs; it is not task evidence.
- The `sc-verify` image is built by trusted CI (plan 23) and its digest goes into `policy/ci.yaml` `spec.verify.installerImage` (plan 20).
- `strange-company`'s own `.strange-company/policy.yaml` and `validation.yaml` are created by plan 01 (F01) and must validate against these schemas (01-AC-11).

## 19. Upgrade behavior

- Policy changes are PRs; evidence records the policy revision it was classified under; a task classified under revision A is not re-classified when B arrives (no retroactive verdict change). Merge facts F5/F8 use the stored classification.
- `sc-verify` upgrades: new digest via plan 23 promotion; observed documents carry the version; F06 must accept observed documents from the previous minor version (one release overlap).
- Adapter rule changes (new error types) are policy changes and are re-verified by 22-AC-12.

## 20. Recovery behavior

- Evidence and manifests are in S3 with versioning (plan 14). After `sc-pg` PITR, the Runner re-links by digest; missing pipelines are re-triggered (plan 29 §20).
- If a manifest object is unrecoverable, provenance for that task cannot be proven: F7 fails, the PR cannot get `tdd-provenance` success, and the procedure re-runs RED at the same red commit (`/sc reverify red`) to rebuild an equivalent manifest (same commit → same blobs).

## 21. Observability

**Metrics** (plan 38 catalogue names): `sc_verification_runs_total{verification_phase, outcome}` with outcomes `red_valid`, `red_invalid`, `already_satisfied`, `green_pass`, `green_fail`, `infra_error`, `timeout`, `evidence_rejected` — this plan adds `partially_satisfied`, `baseline_failing`, `inconclusive`, `tamper_detected`, `judge_valid`, `judge_invalid`, `judging_pass`, `judging_fail` (plan 38/42 to extend the enumeration; `verification_phase` gains `judge_validate`, `judge`); `sc_verification_duration_seconds{verification_phase}`; `sc_policy_violations_total{kind}` with kinds `protected_path`, `foreign_ref`, `test_tamper`, `admission_denied`, `malformed_result` (+ new `evidence_forged`). New: `sc_verification_reasons_total{phase, reason}` (bounded by the schema enums), `sc_tdd_compliance_ratio` input from F1–F10.

**Alerts:** `SCTddPipelineFailure`, `SCTddVerificationStalled`, `SCTddProvenanceBypass`, `SCWorkerPolicyViolation` (existing catalogue, signal owner 22/55). New: `SCRepoPolicyRejected` (info, runner-post: a task was refused because repository policy loosened platform policy; the human must fix the repository).

**Logs:** classification decisions with task, phase, pipeline, outcome and reasons (no test output).

## 22. Testing strategy

- **Unit (F06):** one fixture set per rule R0–R11, G0–G8, each judge rule and each `TEST_TAMPER` sub-code: synthetic observed documents + Git facts → expected outcome and reason list (table-driven). Merge function: one fixture per row of §9.4 (loosening rejected, tightening merged).
- **Adapter fixtures (F06):** recorded real outputs of each tool (`go test -json`, pytest xunit2, jest-junit, vitest junit, cargo-nextest junit) for: assertion failure, panic/exception, compile/collection error, timeout, skip, subtests/parametrized, missing symbol, stub panic. Expected normalized statuses checked in.
- **Determinism:** classify the same input 100× → identical bytes.
- **sc-verify integration (C04):** fixture repositories under `testdata/repos/{go,python,ts,rust}` with scripted commits (base, red-valid, red-compile-error, red-with-stub, red-broken-baseline, already-satisfied, green, green-with-skip, green-tamper) run in the CI image; expected observed documents (modulo durations).
- **Contract (F02):** all six schemas; F06 outputs validate against them.
- **End to end (C04/C10):** plan 21 §22 suite on `sc-e2e/fake-target`.

## 23. Acceptance criteria

### GATE-F04 (with plan 26)

- [ ] **22-AC-1** `make policy-check` validates `policy/verification.yaml` (equal to `verification.schema.yaml` `examples[0]` except documented local overrides) and exits non-zero for each fixture in `src/policy/testdata/invalid/verification/` (one per §9.5 invariant) with the fixture's error code.
- [ ] **22-AC-2** `MergePolicy` table test: each §9.4 row has a tightening fixture (merged value asserted) and a loosening fixture (`POLICY_LOOSENING` with the expected JSON pointer); 26-AC-4 shares the `repo-policy-loosen.yaml` fixture.

### GATE-F06

- [ ] **22-AC-3** `go test ./src/verify/...` passes with ≥ 1 fixture per R0–R11, G0–G8, judge rule and tamper sub-code; classifying each fixture 100 times yields identical output bytes.
- [ ] **22-AC-4** A RED fixture where the new Go test fails only to compile (no stub) classifies `red_invalid` with `build_failed`; the same test with a zero-value `SC-STUB` declaration classifies `red_valid`; with a panic stub in Go → `red_invalid` (`panic_stub_not_allowed`); in Rust → `red_valid` [SPEC §50].
- [ ] **22-AC-5** A fixture whose new test raises `ImportError` (pytest) or `ReferenceError` (jest) classifies `red_invalid` (`error_class_failure`); a failure type matching neither adapter list classifies `failed_error` (fail closed).
- [ ] **22-AC-6** Already satisfied: all change-criterion tests pass at head → `already_satisfied`; one of two passes → `partially_satisfied`; a preservation criterion whose test fails at head → `red_invalid` (`preservation_ac_not_passing`).
- [ ] **22-AC-7** Baseline: a fixture with a failing pre-existing test at base → `baseline_failing`.
- [ ] **22-AC-8** `CheckIngestion` flags each tamper sub-code on its fixture (`manifest_file_changed`, `test_file_deleted`, `existing_test_modified`, `skip_marker_added` for Go/Python/JS/Rust, `discovery_config_changed` for `jest.config.js` and `package.json#scripts.test`, `test_env_detection` for `PYTEST_CURRENT_TEST` and `JEST_WORKER_ID`) and flags nothing on a clean implementation fixture.
- [ ] **22-AC-9** `BuildManifest` on the sc-184 fixture produces the document in `test-manifest.schema.yaml` `examples[0]` (digest equal), and the manifest validates.
- [ ] **22-AC-10** Exemption validation: `documentation-only` on a repository allowing it → `granted/auto`; same with `requireHumanApproval` → `pending_approval/human`; `infrastructure-config` on a repository whose `allowedExemptionClasses` omits it → `EXEMPTION_NOT_ALLOWED`; an unknown class → `EXEMPTION_NOT_ALLOWED` [SPEC §106 "exemption requires approved policy"].
- [ ] **22-AC-11** Producer scope: a `documentation-only` producer diff touching `src/main.go` → `ROLE_DIFF_VIOLATION`; a `metadata` task whose diff touches `charts/x/values.yaml` → `ROLE_DIFF_VIOLATION` (`exemption_escalation_required`); a repository policy without `allowedExemptionClasses` refuses every class; a policy with an `auto` class whose effective paths intersect a non-catch-all `human` class fails `make policy-check` (TDD-12).
- [ ] **22-AC-22** Test-protection gaps closed (TDD-1/TDD-4): `CheckIngestion` flags `existing_test_modified` for fixtures `golden_modified` (pre-existing `testdata/*.golden`), `test_helper_modified` (pre-existing `internal/testutil/eq.go` made a no-op); `test_dependency_changed` for `assertion_lib_replaced` (`go.mod replace github.com/stretchr/testify => ./x`) and a `vendor/` edit; `discovery_config_changed` for `nested_go_mod` (new `pkg/x/go.mod`) and a `tsconfig.json` `paths` change.
- [ ] **22-AC-23** Isolation run (TDD-2): GREEN fixtures where the implementer adds (a) `zz_test.go` with `func init(){ pkg.now = fixed }`, (b) a pytest module assigning `mymod.f = lambda *a: 42`, (c) a jest `__mocks__/http.js` — and the production code is unchanged — classify `tamper_detected` with `protected_pass_depends_on_new_test_files`; the same implementer test files alongside a real implementation classify `green_pass`.
- [ ] **22-AC-24** RED replay and determinism (TDD-3, TDD-B6): a manifest test that fails only when the `test(...)` commit subject (via `git log`), an `SC_PHASE` variable **or an `SC-STUB` marker** is present classifies `tamper_detected` (`red_not_reproducible` / `marker_conditional`) at GREEN when the implementation is absent (the replay export has no `.git`, no `SC_*` and no stub markers), and the marker case already fails R11b at RED with `marker_conditional`; a 50 %-random test fixture yields `inconclusive` (`nondeterministic_test`) at RED with `repeatNewTests: 3` (seeded fixture); the child environment of a suite run contains no `SC_` variable (fixture test prints its environment).
- [ ] **22-AC-25** Full-suite comparison (TDD-4): a GREEN fixture in which a pre-existing passing test outside `redScope` disappears (file renamed out of discovery) classifies `tamper_detected` (`preexisting_test_removed`); `absent` at head fails R8 at RED.
- [ ] **22-AC-26** Generations (TDD-5/TDD-8): a strengthened generation that drops a previous manifest test → `red_invalid` (`manifest_not_superset`); a `test_change_approved` generation for `AC-2` that also edits AC-1's test → `red_invalid` (`unapproved_test_change`); a tester diff deleting any file → `red_invalid` (`tester_deletion`).
- [ ] **22-AC-27** Foreign commits are SHA-based (TDD-9): a commit carrying a copied `SC-Execution: sc-1-x3` trailer but not recorded as an accepted result is listed in `integrity.foreign_commits` and yields `tamper_detected`; after `/sc accept-external-commit <sha>` the same commit is not foreign.
- [ ] **22-AC-29** Observer hardening (blind TDD-B1): fixtures (a) whose production `init()` spawns a detached (`setsid`) process that rewrites the JUnit report after the suite exits — the observation reflects the sealed original and the rewrite never reaches `emit`; (b) that reads `/proc/<ppid>/environ` in `sc-verify run` — no `SC_EVIDENCE_NONCE`/`SC_INTENT_ID` is present; (c) a Go test binary printing fake `--- PASS: TestProtected` lines — the go1.27 test2json-based status of `TestProtected` is unchanged (verify on the baseline Go line; if it changes, the case is recorded as SI-07 residual and the AC asserts the reviewer-packet statement instead).
- [ ] **22-AC-30** Tester weakening and strengthening (blind TDD-B2/B3): a red commit that removes an assertion inside a pre-existing passing test function classifies `red_invalid` (`existing_test_weakened`); a strengthened generation that inserts `return` at the top of an existing manifest test function classifies `red_invalid` (`manifest_not_superset`); a dispatch of a strengthening tester without `/sc strengthen-tests` is rejected by the state machine (`G_STRENGTHEN_AUTHORIZED`).
- [ ] **22-AC-31** Diff completeness (blind TDD-B7, SEC-B3): a fixture commit changing more files than one Forgejo API page, a rename out of `.woodpecker/`, a `chmod +x` of a protected script, a file→symlink type change and a gitlink each yield the expected violation from the Runner's own `git diff --raw` derivation; a fixture compare response with a shortened file list yields `DIFF_UNVERIFIABLE`.
- [ ] **22-AC-32** Checks and size (blind TDD-B5/B8): a `toolingFrom` omitted in `validation.yaml` runs from base; an implementer edit of an `include`d `mk/lint.mk` or of `.golangci.yml` listed in `configPaths` yields `discovery_config_changed`; a red commit with a serialized protected-test list of 61 KiB yields `red_invalid` (`manifest_too_large`).
- [ ] **22-AC-33** Enforcement plane (blind TDD-B4): a `strange-company` repository policy whose `elevatedPaths` omits `src/internal/runner/**` fails `make policy-check` with `ENFORCEMENT_PLANE_NOT_ELEVATED`; a tester diff touching `test/e2e/tdd/x.yaml` yields `PROTECTED_PATH_VIOLATION`.

### GATE-C04 (with plans 20 and 21)

- [ ] **22-AC-12** Version re-verification: `make test-sc-verify-adapters` runs each fixture repository with the tool versions pinned in the `ci` image and compares the normalized observations with the committed expectations; any difference fails.
- [ ] **22-AC-13** On `sc-e2e/fake-target`, `fake-red-valid` produces a classified red-result with `outcome: red_valid`, stored at `evidence/<task>/red/<pipeline>.json` whose sha256 equals the digest in `sc-pg`, and the manifest object exists with the digest recorded on generation 1.
- [ ] **22-AC-14** `fake-red-broken-test` (compile error) yields `red_invalid` and state `TEST_NEEDS_WORK`; `fake-already-satisfied` yields `ALREADY_SATISFIED` [SPEC §106].
- [ ] **22-AC-15** A verification whose `validation.yaml` blob in the observation differs from the base revision's (fault hook) yields `red_invalid` with `validation_config_mismatch`.
- [ ] **22-AC-16** Observed documents carry no `classification`/`scope`/`integrity` (schema-enforced): a doctored `sc-verify` fixture emitting `classification` is rejected as `EVIDENCE_SCHEMA_INVALID`.

### GATE-C09 (with plan 55)

- [ ] **22-AC-28** `MergeFacts` unit fixtures: for each of F1–F12 a fixture task with exactly that fact false returns it false and the others true (12 fixtures), including F11 (second PR from `agent/<task>` into `release/x`) and F12 (`.strange-company/policy.yaml` changed on `main` after the base commit).

### GATE-C10 (contributions; needs the C10 happy-path task)

- [ ] **22-AC-20** `MergeFacts` on the C10 happy-path task returns F1–F12 true, and for a live variant with one fact falsified (F7: a manifest blob changed by a test human push) plan 55's status for that head is `failure` naming the fact.
- [ ] **22-AC-21** Deleting the manifest object from a test bucket (versioned delete marker) makes F4/F7 fail with `evidence_missing`, and the status becomes `failure`.

## 24. Explicit non-goals

- Mutation testing, coverage gates and semantic test-quality scoring (post-MVP; reviewer covers semantics).
- Running tests outside trusted CI as evidence; worker-local validation is advisory (SPEC §36).
- Proving the absence of forged observations (SI-07 residual risk is documented, not eliminated).
- Language-specific stub generation tools (testers write stubs; the Runner only checks their shape).
- Policy editing UIs; changes are PRs.

## 25. Open questions

1. **New intent variables** (`SC_SUBJECT`, `SC_RED_SHA`, `SC_PROTECTED_TESTS`, `SC_BASELINE`) for plan 20's intent and `sc-ci-config` injection. Blocking: no (until C04). Proposed: plan 20 adds them to §8.4 and the intent table. Owner: 20.
2. **Stub tokens in role diff rules** (`@stubPaths`, fixture/infra paths) in `role.schema.yaml`. Blocking: no. Owner: 25.
3. **Forgejo compare `behind_by` field** — superseded by §14.11 (blind TDD-B7): F6 uses `git merge-base --is-ancestor` in the Runner's mirror; the compare API is at most a cross-check.
4. **Mixed-content discovery keys** for languages not listed (Java `pom.xml` surefire config, Gradle). Blocking: no. Resolution: repositories add whole-file protection for build files until semantic adapters exist. Owner: 22.
5. **Research report AC addressing** relies on headings `## AC-n` in the report (plan 27 `ResearchReport.report_markdown`). Blocking: no. Owner: 27/21.
6. **Cost of the added runs (TDD-2/3/4).** RED runs the full base suite (cached per base SHA) and ×3 repeats of new tests; GREEN adds ×3 manifest repeats, the isolation run and the RED replay (both scoped by `selectArgv`). For `strange-company` the estimate is +40 % GREEN wall time; plan 20's latency targets (RED ≤ 10 min, GREEN ≤ 20 min p95) are re-measured at C04 (20-AC-34). Blocking: no. Owner: 22/20.
7. **Elevated control for enforcement code (TDD-15).** Resolved by `merge.elevatedPaths` (repository policy) + `spec.merge.elevatedApprovals: 2` (plan 55 review status) and `toolingFrom: base` for GREEN checks. `strange-company`'s `policy.yaml` must list the enforcement paths and its `validation.yaml` must mark checker targets `toolingFrom: base` and honor `SC_TARGET_TREE` (plan 01). Blocking: no. Owner: 01.
