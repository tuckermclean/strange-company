# Procedure: Merge without TDD provenance (bypass detected)

**Owning plan:** plans/55-forge-workflow.md (merge facts: plans/22-verification-policy.md §14.9)
**SPEC references:** §56, §58, §100, §102, §106
**Severity / urgency:** outage-class for the engineering process (unverified code on a protected branch); act within 4 hours, before the next release tag
**Expected duration:** 30–90 min
**Requires break-glass:** cluster kubeconfig and `SC-RUNNER-ADMIN` (recovery bundle) for inspection; Forgejo repository-admin rights for protection checks; never the Forgejo site-admin break-glass unless protection itself was tampered with (§D)

## When to use

- `SCTddProvenanceBypass` (critical): `sc_tdd_merges_without_provenance_total` increased — the merge auditor (plan 55 §14.8) found a merge or push into a protected branch whose provenance facts failed, or agent-authored commits outside an agent branch.
- `SCProvenanceStatusForged` (critical): a `strange-company/*` commit status was posted by an account other than `sc-runner`.
- `SCProtectedBranchDirectPush` (warning): a direct push to a protected branch in the steady protection stage.
- A task in `NEEDS_HUMAN` with reason `merged_without_provenance`, or a PR labeled `sc/provenance-bypass`.

## Preconditions and required material

- Toolbox shell: `./bootstrap shell`, then `sc-env`.
  ```sh
  read -rs SC_RUNNER_ADMIN; export SC_RUNNER_ADMIN; export SC_ACTOR="human:<you>"
  kubectl -n strange-company port-forward deploy/agent-runner 18080:8080 >/tmp/pf-runner.log 2>&1 &
  kubectl -n forgejo port-forward svc/forgejo-http 13000:3000 >/tmp/pf-fj.log 2>&1 &
  RUNNER=http://127.0.0.1:18080; FJ=http://127.0.0.1:13000/api/v1
  AUTH=(-H "Authorization: Bearer ${SC_RUNNER_ADMIN}" -H "X-SC-Actor: ${SC_ACTOR}")
  read -rs FJ_TOKEN; export FJ_TOKEN   # your own Forgejo token (read:repository) — not a bot token
  FJAUTH=(-H "Authorization: token ${FJ_TOKEN}")
  psql_ro() { kubectl -n strange-company exec -i "$(kubectl -n strange-company get pod -l cnpg.io/cluster=sc-pg,role=primary -o name)" -c postgres -- psql -d strange_company -At -c "$1"; }
  ```
- Membership in team `Owners` of the affected organization (for `/sc` commands and to open a revert PR).

## Safety checks before acting

1. Do **not** force-push or rewrite the protected branch. Corrections go through a normal revert PR so history stays explainable (SPEC §4.2).
2. Do not delete tags `sc-archive/*` or `sc-quarantine/*`; they are evidence.
3. If a release tag was already created on top of the bypassed merge, stop and treat the release as untrusted: follow plan 23's release-revocation steps before continuing.
4. Blast radius: the protected branch of one repository; for `strange-company` itself, Flux may already reconcile the merged change (check §B step 4).

## Steps

### A. Identify the event

1. `psql_ro "select id, repository, pr_number, merged_sha, target_branch, task_id, outcome, detected_at, facts from runner.merge_audits where outcome in ('bypass','direct_push') order by detected_at desc limit 5"`.
   Expected: the row matching the alert, with `facts` listing which of F1–F10 failed. No row → the alert may come from a forged status (§C).
2. If `task_id` is set: `curl -fsS "${AUTH[@]}" $RUNNER/v1/tasks/<task_id> | jq '{state, needs_human, statuses, pull_request}'`. Expected: `NEEDS_HUMAN`, reason `merged_without_provenance`.
3. Who merged: `curl -fsS "${FJAUTH[@]}" $FJ/repos/<owner>/<repo>/pulls/<pr_number> | jq '{merged_by: .merged_by.login, merged_at, head: .head.sha, merge_commit_sha}'`. For a direct push, the Forgejo push activity of the branch (`$FJ/repos/<owner>/<repo>/activities/feeds?...` or the UI) names the pusher.

### B. Assess the merged change

1. Read the failing facts and the diff: Forgejo UI compare `<previous tip>...<merged sha>`.
2. Classify:
   - **Timing/visibility gap** (e.g. F8 failed only because GREEN evidence was recorded for a different SHA after a harmless rebase merge, or Forgejo returned an error during the check): re-run the facts — `sc-verify` offline is not needed; ask the Runner to re-audit by commenting `/sc status` on the PR and checking `facts` again after 5 minutes (resync). If all facts now hold, the auditor records a second row `verified`.
   - **Real bypass** (merged before review/GREEN, protected test changed, foreign commits, agent commits on a human branch): continue with step 3.
3. Check protection: `curl -fsS "${FJAUTH[@]}" $FJ/repos/<owner>/<repo>/branch_protections | jq '.[] | {branch_name, enable_status_check, status_check_contexts, apply_to_admins, push_whitelist_usernames}'`. Expected: contexts include `ci/woodpecker/pr/ci`, `strange-company/tdd-provenance`, `strange-company/review`, and `SCForgeBranchProtectionDrift` is not firing. If protection is weaker than `forge/forge.yaml`, go to §D after finishing §B.
4. For `strange-company`: check whether Flux already applied the change: `flux get kustomizations -A | head` and compare the applied revision with the merged SHA. If applied and the change is unsafe, revert first (step 5) and let Flux reconcile the revert.
5. **Revert** (default for a real bypass). If the revert cannot pass the gate because a merge-gate component is broken, land it with `procedures/emergency-merge.md`: open a PR `revert: <title> (bypass of sc-<n>)` created by a human with `git revert -m 1 <merge commit>` (or the squash commit). It follows normal protection; human PRs get the human-authored provenance status and `tdd-discipline-check`. Merge it after CI.
6. **Ratify** (only when a second human reviewer agrees the change is correct and adequately tested): record the justification in the task issue, and have the work re-verified: comment `/sc reverify green <justification>` is not possible after merge; instead create a follow-up test-first task (Hermes) that adds the missing verification against `main`.

### C. Forged status (`SCProvenanceStatusForged`)

1. Find the forgery record: `psql_ro "select detected_at, repository, merged_sha, status_context, status_creator from runner.merge_audits where outcome='status_forged' order by detected_at desc limit 5"`. The watcher (plan 55 §14.10) covers every `strange-company/*` context and every creator other than `sc-runner`, including bot tokens such as `sc-renovate` or `sc-release`. Then confirm the status author: `curl -fsS "${FJAUTH[@]}" $FJ/repos/<owner>/<repo>/commits/<sha>/statuses | jq '.[] | select(.context|startswith("strange-company/")) | {context, state, creator: .creator.login, created_at}'`.
   Expected: a non-`sc-runner` creator; the Runner has already reposted its own state (latest status by `sc-runner`).
2. If the creator is a person: ask them; if unexplained or a bot/token (a bot never has a reason to post these contexts): follow `credential-compromise.md` for that account's tokens (revoke, rotate).
3. If the PR was merged in the window, continue with §A/§B.

### D. Protection was weakened

If `runner.merge_audits` shows `emergency_merge` for the commits in question, protection was weakened on purpose through `procedures/emergency-merge.md`: verify the incident record and that `--stage steady` was re-applied, then stop here.

1. Restore protection from Git: `./bootstrap exec -- sc-bootstrap forge apply --only branch-protection` (plan 16; idempotent). Expected: `forge verify` exits 0 and `SCForgeBranchProtectionDrift` resolves.
2. Identify who changed it (Forgejo admin audit/logs). If unexplained, `credential-compromise.md` (the `sc-runner` bot holds repo-admin, R-16-1).

### E. Close the task

1. After a revert (§B.5): comment on the task PR or issue `/sc cancel bypass reverted in <revert PR link>`. Expected: `Accepted: cancel → T061`, task `CANCELLED`; the work can be restarted by Hermes as a new task.
2. After ratification (§B.6): `/sc done ratified by <you> and <second reviewer>; follow-up task sc-<m>`. Expected: `T071`, `resolution/declared-done`.
3. Remove the label `sc/provenance-bypass` only after E.1/E.2 (the Runner removes it on the terminal transition; verify).

## Verification

- `runner.merge_audits` has no `bypass` row without a linked resolution: `psql_ro "select count(*) from runner.merge_audits m join runner.tasks t on t.id=m.task_id where m.outcome='bypass' and t.state='NEEDS_HUMAN'"` = 0.
- Branch protection matches `forge/forge.yaml` (`forge verify` exit 0).
- For a revert: the protected branch tip contains the revert commit and the post-merge `push/ci` pipeline passed.
- `SCTddProvenanceBypass` resolved (it is an `increase(...[1h])` rule: resolves one hour after the last increment).

## Rollback / abort

A revert PR can itself be reverted by a normal PR if the assessment was wrong. Do not delete merge-audit rows or evidence.

## Evidence to record

In the task's tracking issue (or a new incident issue for non-task merges): the merge-audit row, failed facts, merger identity, protection state, decision (revert/ratify) with names of both reviewers for ratification, links to the revert PR or follow-up task. Recorded for the SPEC §106 compliance trend (`sc_tdd_compliance_ratio`).

## Follow-ups

- Every real bypass → review of branch-protection settings and admin membership (plan 16), and of the residual risks in plan 55 §13.
- Repeated forged statuses → restrict write access on protected repositories to fewer humans.
