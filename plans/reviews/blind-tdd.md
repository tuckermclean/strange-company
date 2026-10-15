# Blind review: TDD enforcement

**Reviewer mode:** blind, independent. Only `plans/reviews/consolidation-decisions.md` was read from `plans/reviews/`.
**Area:** TDD enforcement (RED before GREEN, test protection, exemptions, evidence integrity, merge gate).

## 1. Scope read

- SPEC §4.4, §44–§58, §100–§102, §103–§106.
- Plans 21, 22, 55 (read in full). Plan 20 §8.4–8.7 and §13.5. Plan 26 §7.3 and §14.4–14.6. Plan 29 §7.3 and the state-machine contract (T001–T016 region). Plan 25 role matrix. Plan 27 isolation sections. Plan 28 registry and fake restriction. Plan 52 §8.3. Plan 01 elevated paths. Plan 46 (references only).
- Contracts: `state-machine.yaml`, `verification.schema.yaml` (platform-default example, exemption classes), `repo-policy.schema.yaml`, `repo-validation.schema.yaml`, `role.schema.yaml`, `harness.schema.yaml`.
- ADRs 0008 (via 0302 and 0351 references), 0019, 0301, 0302, 0350, 0351, 0355.
- Procedures `emergency-merge.md`; `merge-without-provenance.md` only through plan 55 references.

## 2. Verdict

**PASS WITH FIXES.** There are no blockers. The ordering chain is genuinely strong. It uses four independent ancestry checks, the Runner computes the verdicts, the base-revision policy is frozen, and human merge is gated by per-SHA statuses. The weaknesses are in the places where untrusted code shares a trust domain with the observer, and in a few paths that change or weaken a test without a human on record. Several of those paths are not covered by SPEC §53 as written.

## 3. Findings (ordered by severity)

| ID | Sev | Location | Summary |
|---|---|---|---|
| TDD-B1 | major | plan 20 §8.4–8.5, ADR-0302, plan 22 §8, §14.1, §13 | Code under test shares uid, PID namespace, environment and report files with `sc-verify`. Evidence can be forged or replaced by means the plan does not cover. |
| TDD-B2 | major | plan 22 R1/R8, §14.6 | A tester can weaken pre-existing tests in the red commit. Only implementers are covered by `existing_test_modified`. |
| TDD-B3 | major | contract T012, plan 22 R12, plan 21 §14.9 | Hermes can open a "strengthened" generation alone. The "only adds lines" rule does not prevent neutralising existing assertions. |
| TDD-B4 | major | plan 01 §13.2, `repo-policy` example, plan 22 §9.3 | `merge.elevatedPaths` omits most of the enforcement code. `testPaths: test/**` also puts the C10 self-test trajectories in tester reach. |
| TDD-B5 | major | `repo-validation.schema.yaml` `toolingFrom` default `head`, plan 22 §14.1/G6/§14.5 | Required GREEN checks can be weakened by the work they judge. Discovery protection covers only the top-level Makefile and `argv[0]`. |
| TDD-B6 | major | plan 22 §14.4, G7/G9 | A tester-written, marker-conditional test passes G9 and goes green once `SC-STUB` is removed. G7 forces that removal. |
| TDD-B7 | major | plan 26 §7.3 (compare row), plan 22 F6/F7/F10 | Every diff-based control relies on an unverified Forgejo `compare` response (file list, completeness, rename and mode data). |
| TDD-B8 | minor | plan 20 §7 / plan 22 R11 | The GREEN intent allows 64 KiB of protected test IDs. RED permits roughly 2000 tests. The GREEN trigger fails with `INTENT_TOO_LARGE` after a valid RED. |
| TDD-B9 | minor | plan 22 §9.2, plan 21 §9.3 | No mode exists for behavior-preserving work (refactor). This pushes Hermes to invent `change` criteria. |
| TDD-B10 | minor | plan 55 §14.8a vs §14.8 | The emergency-merge audit outcome is keyed on a self-asserted trailer. |
| TDD-B11 | minor | plan 26 §14.4 step 5 | Ingestion has no explicit rule for no-commit results (`blocked`, `failed`, `test_spec_conflict`, `already_satisfied`) when the branch head moved. |
| TDD-B12 | minor | plan 52 §8.3 T0/T2/T8 | Bootstrap-phase discipline for human PRs has a self-granted exemption, a warning-only post-RED edit, and an `agent/*` exclusion with no Runner behind it. |
| TDD-B13 | minor | plan 29 §7.3, ADR-0355 | `/sc accept-test-change` approves a state, not a specific conflict or proposed change. |
| TDD-B14 | minor | plan 25 / plan 21 §14.2 | Nothing screens Hermes-authored task text or `orchestrator_notes` for a proposed implementation (SPEC §48). |

### TDD-B1 (major): the observer can be fooled from inside the sandbox

**Problem.** `sc-verify` runs the suites as child processes with the same uid, in the same container, and in the same PID namespace. It reads the junit and go-json reports from workspace files that those children can write. The plan names two residuals (R-20-2 and SI-07) and 20-AC-32 tests exactly one forgery shape. Several others are not mentioned.

1. **Nonce theft.** Unsetting `SC_EVIDENCE_NONCE` in-process leaves `/proc/<ppid>/environ` readable. It contains the intent id and the nonce. Production code under test therefore learns both.
2. **Report rewrite.** A `setsid` descendant of the test process outlives the suite and rewrites the junit or log file in the milliseconds before `sc-verify` parses it. Nothing in plan 22 §14.1 or §8 says `sc-verify` waits for or kills all descendants. A search for subreaper, process group, or descendant handling found nothing. The step still exits 0 and emits exactly one genuine block, so I1–I7 and the ambiguity rule all pass.
3. **Output forgery.** Test-binary stdout is parsed by `go test -json` or `test2json`. A test binary can print `--- PASS: <ProtectedTest>` lines itself.

**Scenario.** A prompt-injected implementer adds a `go:build`-neutral production file whose `init()` spawns a detached process that waits for `out/r.xml`, then rewrites every manifest test as passed. G4, G4b and G5 all consume that file. G9 (RED replay) still holds, because the replay tree has no implementation. All other rules are satisfied. The only remaining controls are the LLM reviewer and the one human approver, who sees `strange-company/tdd-provenance = success`.

**Fix.**
- Plan 20 §8.4 and plan 22 §14.1: change the verify workflow to three steps. The first installs (as now). The second, `sc-verify-run`, runs the suites and receives **no** `SC_EVIDENCE_NONCE`, `SC_INTENT_ID` or other secrets. The third, `sc-verify-emit` (a separate container, so a separate PID namespace and `/proc`), receives the nonce, parses the reports and emits the block. This is the same split plan 26 §13 already uses for worker pods.
- In `sc-verify-run`, set `PR_SET_CHILD_SUBREAPER`, run each suite in its own process group, and after each suite SIGKILL and reap every descendant before any report is read.
- Copy the report files out of the workspace into a directory that is read-only to later suites before parsing, and sha256 the copy.
- Add ACs: (a) a fixture whose `init()` rewrites the report after the suite exits; (b) a fixture that reads `/proc/<ppid>/environ`; (c) a fixture that prints fake `--- PASS` lines. Each must end in `tamper_detected`, or in `EVIDENCE_AMBIGUOUS`, or be shown to be impossible.
- Plan 22 §13 and plan 55 §14.4 must state plainly that per-test pass observations remain forgeable by in-test code (SI-07). The reviewer packet and PR body should say "GREEN observations are produced inside the code under test".
- Do not rely on SI-07 alone as mitigation. It is accepted only for the shapes that cannot be closed (printed fake lines).

### TDD-B2 (major): tester can weaken existing tests

**Problem.** R1 forbids a tester from deleting or renaming files, and says "pre-existing tests can only be changed, visibly to R8". R8 only requires pre-existing tests to still pass or be in `M`. §14.6 `existing_test_modified`, `skip_marker_added` and friends apply to implementer and producer diffs only. Removing or neutralising assertions inside an existing passing test therefore passes R1–R13.

**Scenario.** The tester edits `foo_test.go` so that `TestFooKeepsOldBehaviour` no longer asserts the old behavior. The test still passes at base and at head. The file blob then enters the manifest. From that point it is protected and the weakened form is canonical. The implementer regresses the old behavior freely. SPEC §53 ("changing the acceptance test then requires a distinct reviewable action") applies to the judge of the task. Here the weakened test belongs to an unrelated but real criterion.

**Fix (plan 22 §14.2/R1).** Add a rule R1b. A tester diff to a testPaths, fixturePaths or testInfraPaths file that existed at base must be additions only outside the bodies of existing test functions. Concretely: new test functions, or new files. Any removed or changed line is a `red_invalid` with `existing_test_weakened`, unless the file is in `M` and the removed or changed lines fall inside a function mapped to a change criterion. Use the Go AST or each adapter's line-range data. The reviewer packet should list every existing test file touched.

### TDD-B3 (major): unilateral strengthening can weaken

**Problem.** T012 (`RED_VERIFIED → TEST_AUTHORING`, actors `[hermes, human]`) opens a generation of kind `strengthened` with no human decision. R12 only requires previously declared tests to be present and old blobs to be changed only by added lines. An added line can neutralise a test: an early `return`, `defer recover()`, a stub assertion helper that does nothing, or a `t.Cleanup` that swallows the failure. The test still counts as passed and is not skipped, so R5 does not fire. R10 only needs one failing test per change criterion. This is a path to a weakened judge that never involves a human. ADR-0355 deliberately requires a human on that path.

**Fix.**
- Plan 22 R12: define "adds only" at function-body granularity per language (no lines inserted into the body of a function that already exists in the manifest), the same as TDD-B2.
- Optionally require `/sc strengthen-tests` (T016 style) for any strengthened generation whose red range touches an existing manifest file.
- `contracts/state-machine.yaml` T012: add a guard or evidence field `E_RECORD_STRENGTHEN_SCOPE` so the diff `red(g)..red(g+1)` is shown in the PR body, as plan 21 already does for conflicts.

### TDD-B4 (major): the code that decides TDD is only partly elevated

**Problem.** Plan 01 §13.2 and the `repo-policy` example list `src/internal/verify/**`, `src/internal/statemachine/**`, `src/internal/policy/**`, `src/cmd/sc-verify/**`, `src/cmd/sc-ci-config/**`. Autonomous tasks may edit, with one ordinary approval, all of the following:
- `src/internal/runner/**` (ingestion steps 5–8, evidence poller identity checks I1–I7, `/sc` authorization in plan 29 §7.3, status posting, merge auditor, dispatch gate);
- `src/cmd/sc-worker/**` (Git operations, trailer production);
- the Forgejo adapter;
- the Gateway and ledger (budget enforcement);
- `src/cmd/sc-e2e/**` and `test/e2e/tdd/**` (the §106 trajectories);
- `src/internal/lint/**`.

The example also gives `testPaths: ["**/*_test.go", "test/**", ...]`, so a tester can edit `test/e2e/tdd/*.yaml` in a red commit.

**Scenario.** A task "tidy webhook handling" gets an implementer diff that adds `|| strings.HasPrefix(login, "sc-ci")` to the `/sc` authorization or removes a guard in the status poster. Its tests were written by a tester that also trimmed the `tdd-human-command-authz` trajectory. The task is reviewed by an LLM and approved by one human reading a large diff. No part of the process marks it as touching the enforcement plane.

**Fix.**
- Plan 01 §13.2 and `contracts/repo-policy.schema.yaml` example: extend `elevatedPaths` to `src/internal/runner/**`, `src/internal/forge/**`, `src/cmd/agent-runner/**`, `src/cmd/sc-worker/**`, `src/internal/gateway/**`, `src/internal/ledger/**`, `src/internal/lint/**`, `src/cmd/sc-e2e/**`, `test/e2e/**`.
- Add a loader invariant in F04: every path named in plan 22's protected-trust list must match some `elevatedPaths` glob, or the repository policy is rejected. Alternatively, move `test/e2e/**` into `protectedPaths` for `strange-company`, so only humans change the self-tests.

### TDD-B5 (major): required checks judged by their own work

**Problem.** `toolingFrom` defaults to `head`. Only `strange-company` opts out, for four targets. Discovery protection is `argv[0]` plus `Makefile`/`GNUmakefile`/`makefile` when `argv[0] == make` (plan 22 §14.5). Not protected: `include`d `.mk` files, scripts the Makefile calls, `.golangci.yml`, `ruff.toml`, `.eslintrc*`, `package.json` scripts other than `scripts.test`, and the like. G6 only checks exit 0. SPEC §54 requires "all repository-required validation".

**Scenario.** An implementer in a project repository edits `scripts/lint.sh` to `exit 0`, or sets `issues: exclude-use-default` and broad ignores in `.golangci.yml`. G6 passes. Static checks do not trigger either: the diff touches no test path, no skip marker, and no listed discovery file.

**Fix.**
- `contracts/repo-validation.schema.yaml`: default `toolingFrom: base` for `checks`; `head` must be justified explicitly and listed in `policy.yaml` `merge.elevatedPaths`-style human review.
- Plan 22 §14.5: resolve `make` includes and `-f` targets transitively (`include`, `-include`, `$(wildcard ...)`), and treat every file named in a check or suite `argv` and every file a `checks[].configPaths` entry names as a discovery whole-file. `make policy-check` should warn when a check's tool config is not covered.

### TDD-B6 (major): marker-conditional test defeats G9

**Problem.** Plan 22 §14.4 admits that a test conditional on content that exists only in the head tree (for example the stub marker removed by the implementer) "is exactly the behavior change G9 tolerates". G7 *requires* the implementer to remove every `SC-STUB` marker. A malicious or compromised tester can therefore write a test that reads its own production source (via `os.ReadFile`, `runtime.Caller` plus file read, `inspect.getsource`, etc.) and fails while `SC-STUB(...)` is present. The test is red for a reason unrelated to behavior and becomes green when any implementation replaces the stub, correct or not. `environment_dependence` warns about `SC_*`, `/proc`, `git`, clock and randomness, but not about reading production source.

**Fix.**
- Plan 22 G9 and R11b: run the RED replay (and one RED head repeat) on a tree in which every `SC-STUB(...)` marker comment line is removed and nothing else changes. If the change tests' statuses differ, classify `red_not_reproducible` or `red_invalid` with `marker_conditional`.
- Promote `source_read_in_test` (a manifest test file reading a non-test, non-fixture file under the repository tree) from a nonexistent check to a warning that the reviewer must acknowledge, and a violation for stub paths.

### TDD-B7 (major): diff controls depend on an unverified API response

**Problem.** Plan 26 §7.3 lists `GET /repos/{o}/{r}/compare/{base}...{head}` with "(files, stats)". Everything below depends on it: protected-path checks, role allow/deny, `TEST_TAMPER` sub-codes, deletion and rename detection, F6 (`behind_by`), F7, F10 and commit counting for "exactly one new commit". To my knowledge the Gitea/Forgejo compare endpoint returns a commit list and a total count, not a changed-file list, and large responses are paginated or capped. The compatibility baseline lists only "compare `behind_by`" for verification. There is no plan for pagination, file-list truncation, rename detection, binary and mode changes, or symlink and submodule entries.

**Scenario.** If the response silently omits files past a cap, or does not return file modes, a tamper beyond that cap is invisible, and every check that says "no file matching X changed" passes.

**Fix.**
- Plan 26 §7.3 and `plans/compatibility-baseline.yaml`: add a C03 AC that proves, against the pinned Forgejo, a complete file list for ranges above the largest permitted diff, with rename and mode data. If it does not, the Runner must compute diffs itself from a bare repository fetched with a read-only credential (`git diff-tree -r -z --raw --no-renames` plus `git ls-tree` for modes), with a hard rejection of any range larger than what it verified.
- Treat a compare response whose file count differs from the Runner's own `diff-tree` count as `ingestion_infra_failure`, never success.

### TDD-B8 (minor): GREEN intent too small for permitted RED

R11 accepts any red with `N ∪ M` up to roughly 2000 reported tests. The GREEN intent carries each as `<kind>\t<id>` in at most 64 KiB (plan 20 §7, 422 `INTENT_TOO_LARGE`). About 1100 table-driven subtests are enough to exceed it. The failure occurs only at GREEN trigger, after an implementation has been paid for, and ends as an infrastructure failure and `NEEDS_HUMAN`. A hostile tester can use it as a cheap denial of service. **Fix:** add a RED rule R14 that rejects a manifest whose serialized protected-test list exceeds a bound below the intent limit (`manifest_too_large`, tester retry with guidance), or pass the list by manifest digest and let `sc-verify` fetch it.

### TDD-B9 (minor): no home for behavior-preserving work

An AC set must contain at least one `change` criterion (AC-L6) and the six exemption classes do not cover refactors. Hermes must therefore either invent a behavior change, or use `metadata`/`documentation-only`, or leave the task to humans. **Fix (plan 22 §9.2):** add a human-approved `refactor` / characterization class (judge: preservation tests that must pass at base *and* head, diff limited to non-test paths), so honest labelling is possible.

### TDD-B10 (minor): emergency-merge outcome keyed on a trailer

Plan 55 §14.8a says the auditor records `emergency_merge` "for commits carrying the trailer"; the §14.8 table is tighter ("direct push by the break-glass identity"). A human-authored commit with the trailer merged by any bypass would be filed as `emergency_merge` (an always-notify audit event) rather than `bypass` (critical page). **Fix:** one rule in §14.8 and §14.8a: `emergency_merge` requires pusher = `sc-breakglass`, an open incident id recorded in advance, and the push falling inside an observed `bootstrap`-stage protection window (the `SCForgeBranchProtectionDrift` interval). Anything else is `bypass`.

### TDD-B11 (minor): no-commit results from pushing roles

Plan 26 step 5 verifies `head == commit` for pushing roles and "for `completed` ... a commit exists". It does not say what is required when an implementer returns `blocked`, `failed` or `test_spec_conflict` with no commit but the branch head has moved. Plan 21 §14.6 says "a pushed commit is archived and dropped" in prose only. The G2/F10 foreign-commit check stops such a commit from merging, so this is a liveness and clarity gap, not an ordering bypass. **Fix:** step 5 gets an explicit rule: when no commit is claimed, the head must equal `start_commit`; otherwise `UNREPORTED_PUSH` is recorded, the extra commits archived to `sc-archive/<task>/x<n>`, the branch reset (the same path as `RESULT_MISSING`), and the result judged normally.

### TDD-B12 (minor): bootstrap discipline gaps

Plan 52 §8.3: T2 accepts a commit trailer `TDD-Exempt: <class>: <reason>` written by the PR author; T8 makes post-RED edits to red tests a warning only; T0 excludes `agent/*` "covered by the Runner's provenance", which is false while GitHub is primary and no Runner exists. During bootstrap the implementer is very likely a model session. **Fix:** T0 exclusion for `agent/*` only when `primary == forgejo`; T8 an error unless a second reviewer label `tdd-test-change-approved` is present; T2 for classes other than documentation requires that label as well.

### TDD-B13 (minor): approvals are not bound to the conflict

`/sc accept-test-change` carries optional `guidance`; it names no conflict id and no digest of `proposed_test_change`. The approval is valid for the current `TEST_SPEC_CONFLICT`, and R13 scopes the new red to the conflict's `ac_ids`, so exposure is small. A human approving a four-page conflict report cannot later show what exactly they approved. **Fix (plan 29 §7.3):** require `conflict=<id>` and `digest=<first 12 hex of sha256(proposed_test_change)>` in the command; reject mismatches with `PAYLOAD_INVALID`; record both on the transition.

### TDD-B14 (minor): proposed implementation reaching the tester

Plan 25's tester forbid-list covers implementation diffs, implementer handoffs and green evidence, but `task_spec`, `context_files` and `orchestrator_notes` (4 KiB) are Hermes-authored and unscreened. A human intent such as "implement it like this: ```...```" flows into the tester, which then writes tests shaped to that solution. **Fix:** `ac-lint`-style check at `agent_start` for a tester dispatch: fenced code blocks of more than N lines, or function bodies, in `objective`, `constraints` and notes produce a warning on the manifest and a reviewer check item. State in plan 25 that this is advisory.

## 4. Attacks and failures the design withstood

- **Implementer before RED / out-of-order history.** Four independent ancestry checks (dispatch gate, ingestion `NOT_DESCENDANT_OF_START`, G1, F6). Single-commit-with-parent-equals-start rule defeats history rewrite and force-push; red commit content is content-addressed.
- **Worker pushing after its result.** Deploy key is deleted before judging; token narrowed to result replay only; head must equal the claimed commit. Orphan commits are covered by G2/F10 (`recorded accepted commits` only, never trailers).
- **Replay or substitution of evidence.** Identity checks I1–I7, intent keyed by `(repo, branch, commit)`, nonce, 15-minute expiry, pipeline number recorded by the Runner, evidence stored immutably with digest re-hashed at merge (F4). Another manual pipeline on the same commit or a human restart is ignored.
- **Self-exemption.** Mode fixed at creation by Hermes or a human, validated against base-revision repo policy; absent or empty `allowedExemptionClasses` means none; per-path most-restrictive approval at every producer ingestion; workers have no field to change it (extra fields rejected). Fake harnesses are restricted to `sc-e2e/fake-target`.
- **Hermes choosing an older, looser policy base.** `BASE_REF_NOT_ALLOWED` for tags, agent and unprotected refs; policy blobs must equal the tip's; F12 rechecks at merge.
- **Status forgery and head races.** Statuses are per SHA and reposted `pending` on any push; the forgery watcher is detective only, but it is the correct residual for human-with-write forgery.
- **Test deletion, skip, golden and helper edits, discovery config, nested modules, monkeypatch via new test files, test-environment detection.** Covered by manifest blobs, `existing_test_modified`, `skip_marker_added`, `discovery_config_changed`, `forbidNewFiles`, G4b isolation run, `test_env_detection`, with named C10 trajectories.
- **Phase-conditional test (environment, time, commit subject).** G9 and the from-scratch child environment. The marker-conditional variant is TDD-B6.
- **Broken or syntax-error tests as RED.** Fail-closed classification: unknown failures are errors, not assertions (R3, R7).
- **TEST_SPEC_CONFLICT used to launder a weaker test.** Adjudication plus a human `/sc accept-test-change` by default (ADR-0355), scoped red range (R13), caps on conflicts and generations.
- **`/sc` commands from Hermes, workers, edited comments, machine accounts, non-members.** Rejected by ADR-0350 rules; team membership is checked live.
- **Runner crash mid-ingestion or mid-dispatch.** Outbox, one-transaction judge, credential-unit checkpoints; framed result recovery from the archived log; no dispatch reuse of a deploy key.
- **Merge without provenance.** Branch protection with per-SHA required contexts, plus post-hoc merge audit for admin or break-glass bypass. The detective-only nature for admins is acknowledged in plan 55.
- **Outdated-branch merges.** Accepted and recorded (plan 55 Q3); policy and test globs are checked against the union of base and target-head sets (F7, F12).
