# 55 — Forge Workflow (branches, issues, PRs, statuses, labels, merge policy)

**Status:** Stage-0 plan (draft)
**DAG nodes:** C03 forgejo-adapter (co-owner with 26: this plan owns semantics, 26 owns adapter mechanics), C09 merge-policy-enforcement (primary; co-owner 22 for merge facts)
**Related plans:** [16 Forgejo](16-forgejo.md), [20 Woodpecker](20-woodpecker.md), [21 TDD workflow](21-tdd-workflow.md), [22 verification policy](22-verification-policy.md), [25 context](25-context.md), [26 Agent Runner](26-agent-runner.md), [27 worker contract](27-worker-contract.md), [29 state machine](29-task-state-machine.md), [30 handoffs](30-handoffs.md), [32 cost accounting](32-cost-accounting.md), [42 alerting](42-alerting-notifications.md), [52 CI strategy](52-ci-strategy.md)
**Contracts:** consumes `task.schema.yaml`, `state-machine.yaml` (29: `humanProjection`), `verification.schema.yaml` (`spec.merge`), `test-manifest`, `red-result`, `green-result` (22), `handoff` (30), `runner-api.openapi.yaml` (26)
**ADRs:** relies on 0003, 0007, 0008, 0017, 0019, 0301, 0302, 0350; introduces [ADR-0354](../adr/0354-pr-lifecycle-and-archive-tags.md) (PR lifecycle: draft at RED, ready at GREEN; Runner-managed body; archive tags)

> Every section below is mandatory (SPEC §121).

## 1. Purpose

Forgejo is the engineering ledger and the human surface [SPEC §30, §101]; the Strange Company database is the authority for machine state (ADR-0003, SPEC §101). This plan defines how the Runner **projects** tasks into Forgejo and how Forgejo's merge gate **enforces** TDD provenance [SPEC §102]: the branch convention, tracking issues and labels, when and how PRs are created and what their body contains [SPEC §100], the two Runner-owned commit statuses and their exact state rules, the human merge and the DONE definition, branch and tag cleanup, and the detection of any merge that bypassed provenance.

## 2. Requirements derived from SPEC.md

1. Agent branches are `agent/<task-id>`; workers do not push protected branches [SPEC §100].
2. PRs contain task ID, acceptance criteria, test commit, RED evidence, implementation summary, GREEN evidence, known limitations and handoff [SPEC §100].
3. The Runner creates PRs after validating worker output and holds PR/label/status authority; workers do not [SPEC §67, §87].
4. Forgejo issues provide the human backlog with states Backlog, Ready, In Progress, Review, Done, Needs Human, Blocked; the database is authoritative [SPEC §71, §101].
5. For test-first tasks merge policy verifies the nine provenance facts; missing provenance blocks merge unless an authorized exemption applies [SPEC §102].
6. MVP merges are human-performed (ADR-0019; SPEC §127 "automatic merge everywhere" is a non-goal); "Human or policy merges" [SPEC §129].
7. TDD provenance survives squash [SPEC §56].
8. Every merged artifact traces back to PR, task and acceptance criteria [SPEC §37].

Derived requirements:

- **55-R-1** Statuses `strange-company/tdd-provenance` and `strange-company/review` are posted only by `sc-runner`, only for a specific SHA, and only after re-deriving every fact from the Runner's own records, S3 evidence and Forgejo Git data — never from labels, other statuses or PR text.
- **55-R-2** Any head change invalidates both statuses (new SHA ⇒ re-evaluation).
- **55-R-3** Every merge into a protected branch is re-verified after the fact; a merge whose provenance fails raises a critical alert and moves the task to NEEDS_HUMAN.
- **55-R-4** Forgejo projections are idempotent and self-healing (5-minute resync); manual label edits on managed issues are reverted.

## 3. Inputs

| Input | Source |
|---|---|
| Task state transitions and effects (`E_ENSURE_PR`, `E_POST_STATUS`, `E_CLOSE_TRACKING`, `E_ARCHIVE_BRANCH`, `E_QUARANTINE_HEAD`, `E_CLOSE_PR`, `E_ENSURE_TRACKING_ISSUE`) | plan 29 via the Runner outbox |
| Merge facts | plan 22 `MergeFacts` (F1–F12) |
| Forgejo webhooks: `push`, `pull_request` (opened/synchronized/closed/edited/reopened), `pull_request_review`, `issues`, `issue_comment`, `status`, `create`, `delete` | plan 16 `forge.yaml` webhook (events list; plan 26 receiver) |
| Branch protection desired state | `forge/forge.yaml` (plan 16), required contexts from `policy/verification.yaml` `spec.merge` |
| Researcher `context_proposals` | worker-result (plan 25 §14.6) |

## 4. Outputs

- Forgejo objects: tracking issues, labels, PRs (draft/ready), PR body, PR comments, commit statuses, tags `sc-archive/*` and `sc-quarantine/*`, branch deletions, context-proposal issues.
- Events to plan 29: `pr_merged`, `pr_closed_by_human`, `head_changed_externally`, human commands (comments, via plan 29 §7.3).
- Metrics and alerts (§21).

## 5. Dependencies

**Hard (DAG):** C03 ← C01, P05 (Forgejo with `sc-runner`, plan 16). C09 ← C03, C04.

**Interfaces assumed:**
- Plan 16: org labels (`status/*`, exclusive scope), teams `Owners`/`engineers`/`viewers`/`sc-automation`, `sc-runner` token scopes `write:repository,write:issue,read:organization,read:user`, webhook events incl. `issue_comment` and `status`, branch protection `steady` stage. **Change request:** at C09 a PR to `forge/forge.yaml` sets `steady.statusChecks` = `policy/verification.yaml` `spec.merge.requiredStatusContexts` (`ci/woodpecker/pr/ci`, `strange-company/tdd-provenance`, `strange-company/review`) and adds tag protection for `sc-archive/*` and `sc-quarantine/*` (whitelist `sc-runner`) and labels of §9.2.
- Plan 26 §7.3 Forgejo adapter calls, plus: `POST /repos/{o}/{r}/issues`, `PATCH /repos/{o}/{r}/issues/{n}`, `POST /repos/{o}/{r}/issues/{n}/comments`, `GET /repos/{o}/{r}/commits/{sha}/statuses` (creator check), `GET /repos/{o}/{r}/pulls/{n}/commits`, `POST /repos/{o}/{r}/labels`, `POST /repos/{o}/{r}/tag_protections` (verify), `DELETE /repos/{o}/{r}/tags/{tag}` (never used for archive tags).
- Plan 20: the Woodpecker PR context name `ci/woodpecker/pr/ci`.
- Plan 16 (requested, GC-02/TDD-16/SEC-2): the `bootstrap` protection stage whitelists the Forgejo break-glass identity `sc-breakglass` for emergency merges; branch protection for `renovate/*` with push whitelist `sc-renovate`; evaluate a two-rule protection (`agent/**` writable by deploy keys, `**` closed to deploy keys) so worker keys cannot push other unprotected branches.
- Plan 52: `tdd-discipline-check` runs inside the PR `ci` workflow for human PRs (re-runs the RED commit); not applicable to `agent/*`.

## 6. Owned state

Forgejo objects listed in §4 (projection; the database is authoritative). The Runner records Forgejo identifiers in the task record (`tracking_issue`, `pull_request`, `statuses`, plan 29) and in `runner.forge_projection` (DDL plan 26): `task_id`, `object` (`issue`,`pr`,`status:<context>`,`labels`), `forgejo_id`, `last_rendered_digest`, `last_synced_at`. The table lets the resync skip unchanged renders and detect drift. Bypass detections are stored in `runner.merge_audits` (`id`, `repository`, `pr_number` nullable, `merged_sha`, `target_branch`, `task_id` nullable, `facts jsonb`, `outcome` (`verified`,`bypass`,`human_non_task`,`direct_push`,`status_forged`,`emergency_merge`), `detected_at`, `status_creator` nullable, `status_context` nullable) — append-only, retained indefinitely.

## 7. External interfaces

### 7.1 Branch and tag conventions

| Ref | Owner | Rules |
|---|---|---|
| `agent/<task-id>` | workers push (deploy key), Runner resets/deletes | one per task; only commits with the task's `SC-Task` trailer; never protected |
| `main`, `release/*`, repository `protectedBranches` | humans via PR | branch protection: no direct push (steady), deploy keys cannot push, required contexts, ≥ 1 approval, stale approvals dismissed, `apply_to_admins` (verify) |
| `sc-archive/<task-id>/<g<n>|x<n>|merged|cancelled|declared-done>` | Runner only (tag protection) | keep provenance commits reachable after branch deletion and squash (SPEC §56) |
| `sc-quarantine/<execution-id>` | Runner only | evidence of a policy violation; never for `SECRET_DETECTED` |
| any other ref created by a worker key | — | `REF_VIOLATION` (ADR-0007), deleted by the Runner |

### 7.2 Commit statuses (posted by `sc-runner` only)

Context `strange-company/tdd-provenance`:

| Situation (evaluated for the PR head SHA) | State | Description (≤ 140 chars) |
|---|---|---|
| test-first task, RED verified, no GREEN at this SHA yet | `pending` | `RED <pipeline> valid; awaiting GREEN at <sha7>` |
| GREEN pass at this SHA, review not yet accepted | `pending` | `RED/GREEN ok at <sha7>; review pending` |
| F1–F8 and F10–F12 true at this SHA (plan 22 §14.9; F9 is the review context) | `success` | `RED <p> valid, GREEN <p> pass, manifest intact (<n> files)` |
| any of F1–F8/F10–F12 definitively false (tamper, foreign commit, not descendant, protected path, evidence missing) | `failure` | `provenance failed: <fact id> <short reason>` |
| task NEEDS_HUMAN, BLOCKED or CANCELLED | `failure` (CANCELLED/NEEDS_HUMAN with policy reason) or `pending` (other NEEDS_HUMAN/BLOCKED) | `task <state>: <reason>` |
| `agent/*` PR whose branch maps to no task, or to a terminal task | `failure` | `agent branch not bound to an active task` |
| a PR whose head maps to an active task but which is **not** the task PR, or whose base is not `task.target_branch` (fact F11, TDD-11) | `failure` | `not the task PR (task PR #<n> into <target>)` |
| `renovate/*` PR not authored by `sc-renovate`, or a bot-authored PR containing a commit not authored by that bot (TDD-16, SEC-2) | `failure` | `bot branch with foreign author or commits` |
| non-task PR (human or `sc-renovate` branch) without agent-authored commits | `success` (policy `nonTaskPullRequests: success-human-authored`) | `human-authored PR: no task; tdd-discipline-check and human review apply` |
| non-task PR containing any commit with an `SC-Execution` trailer or `@agents.strange-company.invalid` author | `failure` | `agent commits outside agent/<task> branch` |
| human-authored non-task PR containing a commit **pushed by a worker deploy key** (ref audit / push-webhook attribution, plan 26) or an agent-marked commit that a human has not adopted (SEC-2) | `failure` until a member of `spec.humanAuthority.forgejoTeams` comments `/sc accept-external-commit <sha>…` on the PR; the Runner records the adoption in `runner.adopted_commits (repository, sha, pr_number, adopted_by, adopted_at)` and re-evaluates | `unadopted foreign commit <sha7>` |
| verification-first task | same rows with judge facts (plan 22 §14.9) | `judge <p> valid (approved <auto|human:x>), judging <p> pass` |

Context `strange-company/review`:

| Situation | State |
|---|---|
| no accepted final review for this SHA | `pending` (`review pending`) |
| latest final review verdict `accept` with all five checks pass and `reviewed_commit == SHA` (F9), and — when the PR diff touches the repository's `merge.elevatedPaths` — at least `spec.merge.elevatedApprovals` (2) approvals on the head SHA by human accounts (`GET /repos/{o}/{r}/pulls/{n}/reviews`, state `APPROVED`, not machine accounts; TDD-15) | `success` (`review <execution> accepted`; `… + 2 human approvals (elevated paths)`) |
| latest final review `changes_requested` or `reject_bypass` at this SHA | `failure` |
| non-task PR | `success` (`non-task PR: human approval required by branch protection`) |

Context `strange-company/release-provenance` (SEC-11, plan 23 §14.5; non-task merge path):

| Situation | State |
|---|---|
| PR whose head branch is not `promote/*` | `success` (`not a promotion PR`) |
| `promote/*` PR: every promoted digest's provenance `invocationId` names an existing Woodpecker pipeline (Runner Woodpecker token) with event `tag`, run on the registered build agent's ID, for a tag that exists, is protected, was pushed by an identity listed in the shared allow-list `security/release-pushers.yaml` (plan 23, read at the PR's base revision) and points to a commit on `main` | `success` (`release provenance verified (<n> digests)`) |
| `promote/*` PR where any check fails or cannot be evaluated | `failure` (`release provenance: <check> failed`) / `pending` while Woodpecker or Forgejo is unreachable |

The context is posted by `sc-runner` only and is covered by the status-forgery watcher (§14.10) like the other `strange-company/*` contexts.

`target_url` = the tracking issue URL. Statuses are re-evaluated on: every task transition with `E_POST_STATUS`; every `pull_request` `synchronized`/`opened`/`reopened` and `push` to the head branch (new SHA → both contexts `pending` immediately, then recomputed); every 5-minute resync; and every `status` webhook for a `strange-company/*` context whose creator is not `sc-runner` (forgery → §14.10 watcher).

### 7.3 Labels

| Label | Scope | Set by | Meaning |
|---|---|---|---|
| `status/backlog`, `status/ready` | issues without a task | humans | human backlog (Backlog / Ready) |
| `status/in-progress`, `status/review`, `status/done`, `status/needs-human`, `status/blocked`, `status/ready` | task issues and PRs | Runner only (projection, plan 29 `humanProjection`) | exclusive scope `status/` |
| `task/sc-<n>` | issue + PR | Runner | links Forgejo objects to the task (created on demand) |
| `mode/test-first`, `mode/verification-first` | issue + PR | Runner | SPEC §46 machine-visible mode |
| `exemption/<class>` | issue + PR | Runner | granted exemption class |
| `resolution/merged`, `resolution/cancelled`, `resolution/declared-done`, `resolution/completed` | closed issues | Runner | how a task ended |
| `sc/managed` | issue + PR | Runner | body and labels are Runner-owned |
| `sc/context-proposal` | proposal issues | Runner | researcher context proposals (plan 25) |
| `sc/provenance-bypass` | PR + issue | Runner | merged without provenance (until resolved) |

Org-level labels are created by `forge apply` (plan 16); per-task `task/sc-<n>` labels are created by the Runner in the repository.

### 7.4 PR body template (`<!-- sc:managed-body v1 -->`)

```markdown
<!-- sc:managed-body v1 task=sc-184 -->
## [sc-184] Preserve method on HTTP 307
Tracking issue: #184 · Mode: test-first · State: GREEN_VERIFIED (Review) · Generation 1

### Acceptance criteria (version 1, frozen 2026-10-13T12:00:01Z)
| ID | Kind | Criterion | Tests (manifest) | RED | GREEN |
|---|---|---|---|---|---|
| AC-1 | change | HTTP 307 preserves the original method. | `TestRedirect307PreservesMethod` | failed_assertion | passed |

### Test commit and RED evidence
Test commit `89abcde` (tester sc-184-x1) · Woodpecker pipeline #412 (`verify`, config from base `0123456`) · outcome **red_valid** · evidence `sha256:abab…` · manifest `sha256:efef…` (2 files, 3 tests, 1 stub)

### Implementation
Implementer sc-184-x3 (attempt 2 of 5, tier standard) · commits `red..fedcba9` · <summary from implementer result, rendered as code block>

### GREEN evidence
Pipeline #431 at `fedcba9` · **green_pass** · suites unit (215 tests, 0 failed) · checks lint ✔ contracts ✔ · manifest intact · warnings: none

### Known limitations
<implementer known_risks (severity) + integrity warnings + ac-lint warnings, rendered as code>

### Handoff
<latest implementer handoff and tester handoff summary, rendered per plan 30 §13>

### Provenance
| Gen | Execution | Role | Outcome | Commit | Evidence |
|---|---|---|---|---|---|
| 1 | sc-184-x1 | tester | accepted | 89abcde | red #412 red_valid |
| 1 | sc-184-x2 | implementer | green_fail | 1111111 | green #420 |
| 1 | sc-184-x3 | implementer | accepted | fedcba9 | green #431 green_pass |
Conflicts: none · Cost so far: $0.84 (complete)

### Human actions
Merge when both `strange-company/*` checks are green. Commands (comment on this PR): `/sc status`, `/sc block`, `/sc cancel`, `/sc escalate` (plan 29 §7.3).
```

Rules: body ≤ 60 KiB (sections truncated with `… [truncated; see task record]`); all model-authored text in fenced code blocks with a fence longer than any backtick run inside; `@` neutralized; no images. The Runner overwrites manual edits of the managed body on the next render (comment instead). Title: `[sc-<n>] <task title>`; draft PRs carry the WIP prefix (`WIP: `, Forgejo default `WORK_IN_PROGRESS_PREFIXES`; verify at C03).

## 8. Internal interfaces

Runner components (plan 26 §8): outbox worker executes effects in order per task (`dedup_key = <task>:<seq>:<effect>`); Forgejo sync renders bodies/labels/statuses and processes webhooks; merge auditor (§14.8) runs on merge/push webhooks and on the 5-minute resync; status-forgery watcher (§14.10) runs on `status` webhooks and on the resync; `MergeFacts` from plan 22.

## 9. Configuration

### 9.1 Policy (`policy/verification.yaml` `spec.merge`; repository may tighten)

`requiredStatusContexts: [ci/woodpecker/pr/ci, strange-company/tdd-provenance, strange-company/review, strange-company/release-provenance]`, `reviewRequired: true`, `humanApprovalsRequired: 1`, `nonTaskPullRequests: success-human-authored`. Repositories add contexts (`additionalStatusContexts`) and raise approvals.

### 9.2 Runner config (chart values, plan 54)

| Key | Default |
|---|---|
| `forge.prBodyMaxBytes` | 61440 |
| `forge.resyncIntervalSeconds` | 300 |
| `forge.wipPrefix` | `"WIP: "` |
| `forge.trackingIssueRepo` | the task's repository |
| `forge.directPushAlert` | `true` |

### 9.3 Forgejo (plan 16 `forge/forge.yaml`)

Branch protection `steady` stage per §5; tag protection for `sc-archive/*`, `sc-quarantine/*`; merge styles allowed: merge, squash, rebase (provenance survives all three via archive tags and recorded SHAs); `block_on_outdated_branch: false` (MVP; §25 Q3).

## 10. Secrets

No new class. Uses `SC-FORGEJO-BOT-TOKENS` (`sc-runner` API token) and `SC-RUNNER-WEBHOOK` (webhook HMAC; CD-08 name). Workers hold only deploy keys (ADR-0007) and cannot post statuses, open PRs, label or comment.

## 11. Storage

Forgejo DB/PVC (plan 16) holds PRs, issues, statuses, labels and tags; the Runner holds `runner.forge_projection` and `runner.merge_audits` (< 50 MiB/year).

## 12. Resource requirements

N/A — runs inside the Runner. Forgejo API load: per task ≈ 60 calls over its life plus 1 resync pass per 5 min over open tasks (≤ 2 calls per open task).

## 13. Security boundary

- **Who can merge:** humans with write permission on the protected branch, subject to branch protection. `sc-runner` holds repo-admin (deploy-key management, R-16-1) and therefore could merge or edit protection; its code never calls merge, review or protection endpoints (unit test asserting the adapter's call allowlist, 55-AC-15), and `SCForgeBranchProtectionDrift` (plan 16) detects protection edits.
- **Status forgery:** any account with write access can post a commit status with any context (Forgejo does not bind contexts to users; verify). Mitigation: the status-forgery watcher (§14.11, CD-07) reposts the Runner's state and opens a merge audit, and the merge auditor re-verifies every merge (DONE condition 2 checks the status creator). Residual risk: a human with write access who forges a status and merges inside the detection window (≤ 30 s webhook path) is detected after the merge, not prevented.
- **Admin override:** with `apply_to_admins` (verify) admins are bound by protection; a site admin can still change protection — detected by `SCForgeBranchProtectionDrift` and by the merge auditor.
- **Rendering safety:** model text is inert (plan 30 §13).
- **Human commands** via comments are authorized by plan 29 §7.3 (ADR-0350) and accepted only from human accounts. Hermes holds **no** Forgejo account (CD-15); machine accounts (`sc-*`) are always refused.

## 14. Happy path

### 14.1 Intake (MVP; database authoritative)

1. **Hermes-first:** a human describes work to Hermes; Hermes calls `agent_start` with an embedded task (`source.kind: human`). In the creation transaction the Runner enqueues `E_ENSURE_TRACKING_ISSUE`: create issue `[sc-<n>] <title>` in the task repository with body (objective, criteria table, mode, source) and labels `sc/managed`, `task/sc-<n>`, `mode/*`, `exemption/*`, `status/<projection>`.
2. **Issue-first:** a human files an issue (optionally labeled `status/backlog` or `status/ready`) and asks Hermes to work on it. Hermes calls `agent_start` with `source: {kind: forgejo_issue, ref: "<org>/<repo>#<n>"}`. The Runner verifies the issue exists and is open and not already linked to a non-terminal task (else `VALIDATION_FAILED`), then links it: adds the managed labels, replaces `status/*`, and posts a comment `Linked to task sc-<n>` with the criteria. The issue body stays human-owned; the Runner appends nothing to it.
3. The Runner never starts work because of an issue or label alone (no autonomous intake in MVP). `status/backlog`/`status/ready` on unlinked issues are human-managed.
4. **Context proposals** (plan 25 §14.6): for each accepted researcher `context_proposals[]` item the Runner creates an issue in the target repository labeled `sc/context-proposal`, `task/sc-<n>`, with the rationale and proposed content in code blocks; dedup by sha256 of (repository, path, proposed_content) against open proposal issues (`sc_context_proposals_total{outcome}`).

### 14.2 Branch

Workers create and push `agent/<task-id>` per plan 21 §14.9 topology. The Runner snapshots refs before each execution and audits after it (ADR-0007).

### 14.3 PR lifecycle (ADR-0354)

| Task event | PR action |
|---|---|
| T020 RED_VERIFIED (test-first) / T116, T121 JUDGE_APPROVED (verification-first, mergeable) | create PR `agent/<task>` → `target_branch`, title `WIP: [sc-<n>] <title>`, managed body, labels; if a PR already exists for the head (re-open after reset) reuse it |
| any transition with `E_ENSURE_PR`/`E_POST_STATUS` | re-render body and statuses |
| T033 / T127 GREEN_VERIFIED | remove the WIP prefix (ready) |
| T041 AWAITING_MERGE | statuses success; comment "Review accepted (sc-<n>-x<k>); ready for human merge" with the reviewer verdict and checks |
| CHANGES_REQUESTED | review status failure; PR comment with the findings (code-rendered) |
| merge | §14.7 |
| CANCELLED / declared done | close the PR with a comment (`E_CLOSE_PR`) |

PRs are opened at RED rather than at GREEN so humans see the failing tests and the frozen criteria while implementation runs; required statuses keep the draft unmergeable.

### 14.4 Non-task PRs

Human feature branches and Renovate PRs are not tasks. The Runner evaluates them on `pull_request` events: commits with agent markers → `tdd-provenance = failure`; a `renovate/*` branch not authored by `sc-renovate`, or a bot-authored PR with any commit not authored by that bot (a worker deploy key can push to unprotected branches, SEC-2) → `failure`; otherwise `success` with the human-authored description, and `review = success` unless the PR touches `merge.elevatedPaths` (then 2 human approvals are required first). Branch protection still requires `ci/woodpecker/pr/ci` (which runs `tdd-discipline-check` for human PRs, plan 52) and ≥ 1 human approval. A PR whose head is `agent/*` but not bound to an active task → `failure` on both contexts; a second PR from a task head (other number or other base branch) → `failure` (F11).

### 14.5 Re-verification on head change

On a new head SHA for a task PR: post `pending` for both contexts on the new SHA; if the change came from an accepted execution the normal flow continues (GREEN re-runs); otherwise plan 29 `head_changed_externally` (T055) moves the task to NEEDS_HUMAN. Statuses on the old SHA are left as they were (Forgejo evaluates protection against the current head only).

### 14.6 Human merge

A human with write access merges in the Forgejo UI (merge, squash or rebase) once `ci/woodpecker/pr/ci`, `strange-company/tdd-provenance`, `strange-company/review` are success and the approval count is met. The Runner never merges in MVP (ADR-0019).

### 14.7 DONE definition

A test-first (or mergeable verification-first) task is **DONE** when, and only when:
1. Forgejo reports **the task PR** (`task.pull_request.number`) merged into `task.target_branch` (webhook or resync) (F11), and
2. the merged PR head SHA equals the SHA for which `sc-runner` posted `success` on both Runner contexts (checked with `GET /repos/{o}/{r}/commits/{sha}/statuses`, creator `sc-runner`), and
3. `MergeFacts` re-evaluated at that SHA returns F1–F12 true (plan 22 §14.9; F11 = the task PR into `target_branch`, F12 = policy unchanged since base), and the protected-path check holds for every commit in the merged range.

Then T050 fires; the Runner records merge commit, merger and facts (`runner.merge_audits` outcome `verified`), tags `sc-archive/<task>/merged` at the PR head, deletes `agent/<task>`, closes the tracking issue with `resolution/merged`, and posts a final PR comment with the provenance summary. Non-mergeable verification-first tasks are DONE at accepted review (T042). Human `done` (T071) closes without merge (`resolution/declared-done`).

### 14.8 Bypass detection (`MergeWithoutProvenance` → alert `SCTddProvenanceBypass`)

The merge auditor evaluates every update of a protected branch:

| Trigger | Check | Outcome |
|---|---|---|
| `pull_request` closed+merged for a task PR | DONE conditions 2–3 | pass → `verified` (T050); fail → `bypass`: T051/T052/T053, label `sc/provenance-bypass`, `sc_tdd_merges_without_provenance_total++`, `SCTddProvenanceBypass` (critical, page, notify) |
| merge of any PR whose head maps to a task but which is not the task PR, or into a branch ≠ `target_branch` (TDD-11); or merge of the task PR after the task is terminal (TDD-22) | always | `bypass` + alert, independent of whether a task transition applies |
| emergency merge (`procedures/emergency-merge.md`): direct push by the break-glass identity during an announced emergency (commit trailer `SC-Emergency-Merge: <incident-id>`) | agent markers; recorded incident | `emergency_merge` + `SCBreakGlassUsed{target="forgejo"}`; the range is re-audited when the Runner returns |
| merged non-task PR | merged commits contain no agent markers and no unadopted worker-pushed commit | pass → `human_non_task`; fail → `bypass` + alert |
| `push` to a protected branch without a corresponding merged PR within 60 s | any new commit with agent markers → `bypass`; otherwise `direct_push` + `SCProtectedBranchDirectPush` (warning; expected only in the bootstrap protection stage) |
| 5-minute resync | protected branch tips compared with the last audited tip; unaudited ranges audited as above | catches missed webhooks |

"Agent markers" = trailer `SC-Execution:`, trailer `SC-Task:`, or author email `*@agents.strange-company.invalid`. The procedure is `procedures/merge-without-provenance.md` (revert-or-ratify decision by a human).

### 14.8a Emergency merge (GC-02, TDD-17)

From C09, every merge into a protected branch needs Runner-posted statuses and `ci/woodpecker/pr/ci`. When the Runner, Woodpecker, `sc-ci-config`, the egress-proxy or the heavy-slot advertiser is broken, the PR that would repair it cannot merge. The **only** sanctioned path is `procedures/emergency-merge.md` (owned by this plan): a human reviews the fix as a normal PR; the operator applies the `bootstrap` protection stage (`sc-bootstrap forge apply --stage bootstrap`, push whitelist = break-glass identity), fast-forward-pushes exactly the reviewed commit with trailer `SC-Emergency-Merge: <incident-id>`, and re-applies `--stage steady` immediately. `SCBreakGlassUsed{target="forgejo"}` (always notify) and `SCForgeBranchProtectionDrift` fire and are expected. When the Runner is back, the merge auditor audits the unaudited range of the protected branch, records `emergency_merge` for commits carrying the trailer (and `bypass` for any other unreviewed commit in the range), and posts a PR comment. Every other procedure that needs a merge while a merge-gate component is down references this procedure.

### 14.9 Quarantine and cleanup

- **Policy violation (except secrets):** tag `sc-quarantine/<execution-id>` at the offending head, reset `agent/<task>` to the last accepted commit (delete + recreate branch at that SHA), record evidence (plan 26).
- **`SECRET_DETECTED`:** delete `agent/<task>` (and any ref the execution pushed) **without** a tag, so the commit becomes unreachable; recreate the branch at the last accepted commit if the task continues; the Forgejo `git gc` schedule (plan 16) prunes it. The secret is rotated by credential revocation (plan 26).
- **Cancelled / declared done:** tag `sc-archive/<task>/cancelled|declared-done`, delete the branch, close PR and issue.
- **Generation reset:** plan 21 §14.9.
- Archive tags are never deleted automatically (provenance; plan 43 records them as retained Git refs).

### 14.10 Status-forgery watcher (CD-07; the only forgery detector)

Runs in the Runner leader; plan 34's `SCStatusForgeryDetected` is withdrawn in favour of this watcher and its single alert `SCProvenanceStatusForged`.

1. **Inputs.** Every Forgejo `status` webhook delivery for a managed repository (plan 16 webhook events include `status`), plus the 5-minute resync, which lists `GET /repos/{o}/{r}/commits/{sha}/statuses` for the head SHA of every open PR and the tip of every protected branch.
2. **Rule.** A status is forged when its context matches `strange-company/*` and its `creator.login` is not `sc-runner` — whoever the creator is: a human, `sc-renovate`, `sc-release`, `sc-woodpecker`, `sc-backup` or any other token with `write:repository`. Creator identity is taken from the API response (re-fetched by the Runner; the webhook payload alone is not trusted).
3. **Response,** in order, within one outbox sequence: (a) increment `sc_forge_status_forged_total{context}`; (b) repost the Runner's computed state for that context and SHA (§7.2 tables), so the latest status for the context is again `sc-runner`'s; (c) insert `runner.merge_audits` (outcome `status_forged`, `status_creator`, `status_context`) and, if the SHA has already been merged into a protected branch, run the §14.8 bypass check for that merge; (d) post `SCProvenanceStatusForged` (critical, page) with the repository, SHA, context and creator as annotations; (e) comment on the PR: "A `strange-company/*` status was posted by `<creator>` and has been replaced."
4. **Latency.** Webhook path: forged status → repost ≤ 30 s (p95; webhook delivery + outbox), alert active ≤ 60 s. Without webhooks (lost delivery): ≤ 5 min 30 s via resync. Merges inside that window are caught by the merge auditor.
5. A forgery never changes task state by itself; the merge audit decides whether a bypass happened.

### 14.11 Label and issue projection

On every transition the Runner computes `Project()` (plan 29) and applies `status/<label>` (exclusive), plus resolution labels at terminal states, and closes/reopens the issue (closed for DONE/CANCELLED). On resync, a manual `status/*` change on a managed issue is reverted and a comment explains: "Status labels are managed by Strange Company; use `/sc` commands."

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Forgejo API down | adapter circuit breaker (plan 26) | projections lag; merges impossible anyway | outbox retries; resync catches up | `restore-forgejo.md` |
| Webhook lost | resync every 5 min | ≤ 5 min delay in status/merge detection | resync | — |
| Status forged by a non-Runner account (human or any bot token) | status-forgery watcher (§14.10): webhook ≤ 30 s, resync ≤ 5.5 min | merge gate may look green | repost Runner state, merge audit `status_forged`, `SCProvenanceStatusForged` (critical) | `merge-without-provenance.md` §forged |
| Merge without provenance | merge auditor | unverified code on protected branch | NEEDS_HUMAN, label, `SCTddProvenanceBypass` | `merge-without-provenance.md` |
| Direct push to protected branch | merge auditor | — | `SCProtectedBranchDirectPush` / bypass | `merge-without-provenance.md` §direct-push |
| Branch protection weakened | `SCForgeBranchProtectionDrift` (plan 16) | gate weaker | none | plan 16 procedure |
| PR body render fails (size, API error) | outbox error | stale body | retry; truncated render | — |
| Tracking issue deleted by a human | resync 404 | no projection | re-create issue with note | — |
| Human edits managed labels/body | resync digest mismatch | drift | revert + comment | — |
| Archive tag push rejected (tag protection misconfigured) | outbox error | provenance commit may become unreachable after branch deletion | branch deletion is skipped until the tag exists (ordering) | `tdd-task-recovery.md` §archive |

## 16. Retry behavior

All Forgejo writes are outbox side effects with exponential backoff (1 s × 2ⁿ, cap 5 min; plan 26). Ordering per task is preserved (`seq`); `E_ARCHIVE_BRANCH` deletes the branch only after the tag creation succeeded. Webhook processing is idempotent; failures fall back to resync.

## 17. Idempotency behavior

- Issue: one per task (`runner.forge_projection`); create only if absent (search by `task/sc-<n>` label as fallback).
- PR: one per `head = agent/<task>`; `GET /repos/{o}/{r}/pulls?state=all&head=…` before create.
- Status: idempotent by (SHA, context) — reposting the same state is harmless.
- Labels: set semantics. Tags: create if absent; existing tag at a different SHA → `SCTaskInvariantViolation` (never moved).
- Merge audit: unique (repository, merged_sha).

## 18. Bootstrap behavior

- P05/P12 (plan 16): org labels and `bootstrap`→`steady` branch protection without Runner contexts.
- C03: webhooks activated (`active: true`), Runner projections live on `sc-e2e/fake-target` and `strange-company`.
- C09: `forge.yaml` PR adds the Runner contexts (including `strange-company/release-provenance`, required before the first promotion PR merges, plan 23 Q9) to `steady.statusChecks` and tag protection; from then on no PR merges into `main` without `tdd-provenance` and `review`. Human PRs created before C09 are unaffected until their next push.

## 19. Upgrade behavior

- PR body template version marker `v1`; a template change re-renders open PRs on the next resync (bounded to 20 PRs per pass).
- Status context names are a contract with branch protection; renaming requires a coordinated `forge.yaml` change in the same release (both contexts posted during a transition window).
- Forgejo upgrades re-verify API behaviors listed in §5 and §7 (55-AC-1).

## 20. Recovery behavior

- After `forgejo-pg` restore: PR/issue/status objects may be older than the Runner's records; the resync re-renders projections and re-posts statuses for open task PRs; the merge auditor re-audits protected-branch tips against `runner.merge_audits` (unaudited ranges audited).
- After `sc-pg` restore: projection rows may be older than Forgejo; the Runner trusts Git for refs and its restored task state for statuses; merges after the restore point are discovered by resync (T050/T051).
- Archive tags restore with the repository (plan 44 bundles include tags).

## 21. Observability

**Metrics:** `sc_tdd_merges_without_provenance_total` (existing catalogue), `sc_merge_audits_total{outcome}` (`verified`, `bypass`, `human_non_task`, `direct_push`, `status_forged`), `sc_forge_status_posts_total{context,state}`, `sc_forge_status_forged_total{context}`, `sc_forge_projection_drift_total{object}`, `sc_forge_resync_duration_seconds`, `sc_forge_projection_last_resync_timestamp_seconds`, `sc_context_proposals_total{outcome}` (plan 25).

**Alerts** (catalogue names, CD-01): `SCTddProvenanceBypass` (critical; runbook `procedures/merge-without-provenance.md`); `SCProvenanceStatusForged` (critical, page; `sum(increase(sc_forge_status_forged_total[15m])) > 0`; runbook `procedures/merge-without-provenance.md` §C) — the single forgery alert per CD-07, replacing plan 34's withdrawn `SCStatusForgeryDetected`; `SCProtectedBranchDirectPush` (warning; `sum(increase(sc_merge_audits_total{outcome="direct_push"}[1h])) > 0`); `SCForgeProjectionStale` (warning; `time() - max(sc_forge_projection_last_resync_timestamp_seconds) > 1800`).

## 22. Testing strategy

- **Unit:** status-state function over a table of (task state, facts, PR kind) → (state, description); body renderer golden files (incl. injection strings, truncation); merge-auditor decision table; adapter call allowlist (no merge/review/protection endpoints).
- **Integration (C03, Forgejo test container at the pinned version):** issue linking, PR create/draft/ready, labels exclusive scope, status post and `GET …/statuses` creator field, tag protection rejects a non-whitelisted tag push, webhook events received for each action.
- **C09 (live):** branch protection blocks merge without Runner contexts (UI/API as a non-admin test user), force-push resets statuses to pending, admin-override bypass detected.
- **C10:** plan 21 suite (happy path merge → DONE, squash survival, merge-without-provenance).

## 23. Acceptance criteria

### GATE-C03 (semantics; mechanics in plan 26)

- [ ] **55-AC-1** Re-verification of Forgejo behavior at the pinned version: (a) a PR titled `WIP: …` cannot be merged and is shown as work in progress; (b) `GET /repos/{o}/{r}/commits/{sha}/statuses` returns the `creator.login` of each status; (c) `POST /repos/{o}/{r}/tag_protections` with name pattern `sc-archive/*` and whitelist `sc-runner` makes a tag push with a test deploy key to `sc-archive/x` fail; (d) `POST /repos/{o}/{r}/branches` with `old_ref_name: <sha>` creates a branch at that SHA. Each sub-check fails loudly if the API differs.
- [ ] **55-AC-2** Hermes-first task creation on `sc-e2e/fake-target` creates exactly one issue titled `[sc-<n>] …` with labels `sc/managed`, `task/sc-<n>`, `mode/test-first`, `status/in-progress`; repeating `agent_start` with the same idempotency key creates no second issue.
- [ ] **55-AC-3** Issue-first: `agent_start` with `source.ref = sc-e2e/fake-target#<n>` links that issue (labels added, comment posted); a second task referencing the same open issue is rejected with `VALIDATION_FAILED`.
- [ ] **55-AC-5** Manually setting `status/done` on a managed issue is reverted within 6 minutes and a comment containing `/sc` guidance is posted.

### GATE-C09

- [ ] **55-AC-10** After the C09 `forge.yaml` change, `GET /repos/{o}/{r}/branch_protections/main` lists `status_check_contexts` ⊇ `ci/woodpecker/pr/ci`, `strange-company/tdd-provenance`, `strange-company/review`, and `forge verify` exits 0.
- [ ] **55-AC-11** A PR whose head lacks RED evidence (`agent/sc-999` with no task) has `tdd-provenance = failure` and cannot be merged by a non-admin test user through the API (`POST /repos/{o}/{r}/pulls/{n}/merge` → 405/409; verify code) [ADR-0019].
- [ ] **55-AC-15** Forged status (CD-07, blocking for C09): on a task PR with a pending head, (a) a test human and (b) the `sc-renovate` token each post `strange-company/tdd-provenance = success` and `strange-company/review = success`. For each post: within 60 s the latest status per context on that SHA has `creator.login = sc-runner` and the Runner's computed state (`pending`), `runner.merge_audits` has a row with outcome `status_forged` and the correct `status_creator`, and the Runner metric `sc_forge_status_forged_total` increased (the alert itself is asserted at C11, 55-AC-30; SEC-22). (c) With the Runner's webhook delivery disabled (hook `active: false` in the test repository), the same forgery is reposted within 6 minutes by the resync.
- [ ] **55-AC-19** Static test: the Runner Forgejo adapter has no code path calling `/merge`, `/reviews` or `/branch_protections`.
- [ ] **55-AC-20** Emergency merge drill (GC-02): on a fixture repository with steady protection, the Runner scaled to 0 and a reviewed revert PR open, `procedures/emergency-merge.md` lands the revert on `main` within 30 minutes; protection is back at `steady` (`forge verify` exits 0); after the Runner is scaled back, `runner.merge_audits` has an `emergency_merge` row for the commit within 6 minutes.
- [ ] **55-AC-21** Bot and second-PR rules: a `renovate/x` PR opened by a test human gets `tdd-provenance = failure`; a commit pushed by a test deploy key onto a `sc-renovate` PR branch turns its status to `failure`; a second PR from `agent/<task>` into `release/x` gets `failure` `not the task PR` (TDD-11, TDD-16, SEC-2).
- [ ] **55-AC-22** Elevated paths: on a fixture repository with `merge.elevatedPaths: [enforce/**]`, a PR touching `enforce/x.go` with an accepted review and one human approval has `review = pending`; after a second human approval on the head it is `success` (TDD-15).
- [ ] **55-AC-24** Release provenance (SEC-11): a non-promotion PR gets `strange-company/release-provenance = success`; the plan 23 forged-provenance fixture (validly signed attestation whose `invocationId` names a non-existent pipeline) on a `promote/*` PR gets `failure` and the PR cannot be merged by a non-admin test user (shared with 23-AC-21).
- [ ] **55-AC-16** Non-task PRs: a human branch PR without agent markers gets both contexts `success` (human-authored); a human branch containing a commit with an `SC-Execution` trailer gets `tdd-provenance = failure`.

### GATE-C10 (contributions; need full fake task flows)

- [ ] **55-AC-4** After `fake-red-valid` reaches RED_VERIFIED, exactly one PR exists with head `agent/<task>`, title prefix `WIP: `, body containing `<!-- sc:managed-body v1 task=<task> -->`, the criteria table and the RED pipeline number; `strange-company/tdd-provenance` on the head is `pending`. (moved from C03: needs C02/C04, GC-01)
- [ ] **55-AC-12** Happy path: after review acceptance both Runner contexts are `success` on the head; a test human merges with **squash**; within 6 minutes the task is DONE, `runner.merge_audits` has outcome `verified`, tag `sc-archive/<task>/merged` points at the PR head, branch `agent/<task>` is gone, issue closed with `resolution/merged`.
- [ ] **55-AC-13** A force-push (test human) to the PR head after success makes both contexts `pending` on the new SHA within 60 s and the task NEEDS_HUMAN with reason `external_push`.
- [ ] **55-AC-14** Bypass: on a test repository with protection temporarily removed (fault-injection fixture), merging a task PR in GREEN_VERIFIED produces T052 (NEEDS_HUMAN, `merged_without_provenance`), label `sc/provenance-bypass`, `sc_tdd_merges_without_provenance_total` +1 and an active `SCTddProvenanceBypass` alert within 5 minutes.
- [ ] **55-AC-17** `SECRET_DETECTED` (`fake-secret-in-handoff` variant that also commits the token): no `sc-quarantine/*` tag exists for that execution and `GET /repos/{o}/{r}/git/commits/<offending sha>` returns 404 after the Forgejo gc run in the test (or the commit is unreachable from every ref: `GET …/branches` and `…/tags` contain no ref pointing at it).
- [ ] **55-AC-18** Forgery during merge: a test human forges `success` on both contexts and merges within 10 s (protection otherwise intact on a fixture repository that requires only those contexts). Within 6 minutes the merge audit records `bypass` (DONE condition 2 fails: creator not `sc-runner`), the task is `NEEDS_HUMAN` with `merged_without_provenance`, and both `SCProvenanceStatusForged` and `SCTddProvenanceBypass` are active.
- [ ] **55-AC-23** Terminal-task merge (TDD-22): a task PR reopened and merged (admin override on a fixture repository) after the task is CANCELLED produces a `bypass` merge-audit row and `sc_tdd_merges_without_provenance_total` +1.

### GATE-C11 (contributions; alert rules loaded)

- [ ] **55-AC-30** Repeating 55-AC-15(a) on the live cluster makes `SCProvenanceStatusForged` active in Alertmanager within 2 minutes, and 55-AC-14 makes `SCTddProvenanceBypass` active.

## 24. Explicit non-goals

- Autonomous merge (ADR-0019) and merge queues.
- Forgejo Projects boards as an automated projection (SI-10; humans may use them manually).
- Reading task intent from issue bodies without Hermes (no autonomous intake).
- Cross-repository PRs or forks.

## 25. Open questions

1. **Forgejo draft semantics** (WIP prefix vs a native draft flag in newer Forgejo). Blocking: no. Resolution: WIP prefix; 55-AC-1(a) re-verifies; switch to native draft if available and equivalent. Owner: 55.
2. **Status-context binding to `sc-runner`.** Forgejo does not restrict which user posts a context (assumed). Blocking: no. Resolution: detect and repost + merge audit (§13). A Forgejo feature to bind required checks to an app/user would remove the residual risk. Owner: 55/34.
3. **Outdated-branch merges.** GREEN runs at the PR head, not at the merge result; a semantic conflict with newer `main` is caught only by the post-merge `push/ci` pipeline. Blocking: no. Proposed: keep `block_on_outdated_branch: false` for MVP (rebasing would break red-commit ancestry); post-MVP the Runner merges `target_branch` into `agent/<task>` and re-runs GREEN. Owner: 55/22.
4. **Webhook secret naming** — resolved by CD-08: `SC-RUNNER-WEBHOOK`.
5. **Alert runbook for `SCTddProvenanceBypass`** should point to `procedures/merge-without-provenance.md` (catalogue currently says `credential-compromise.md`). Blocking: no. Owner: 42.
6. **Archive tags on the GitHub mirror** — accepted per CD-17 (private mirror; secret-bearing commits are never tagged).
7. **Required checks of plan 48 (`strange-company/c10-regression`, `upgrade-freeze`)** are not in this plan's required-context list (GC-07). Until the lead decides, plan 55 posts neither and branch protection does not require them. Owner: lead/48/52.
