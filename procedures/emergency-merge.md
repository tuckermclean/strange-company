# Procedure: Emergency merge (a merge-gate component is broken)

**Owning plan:** plans/55-forge-workflow.md §14.8a (GC-02, TDD-17)
**SPEC references:** §4.2, §22, §58, §102, §113
**Severity / urgency:** outage (the repair of a merge-gate component cannot merge through the normal gate)
**Expected duration:** 20–30 min (target: revert landed within 30 min, 55-AC-20)
**Requires break-glass:** yes — the Forgejo break-glass identity `sc-breakglass` (`SC-BREAK-GLASS-FORGEJO`, recovery bundle) and the operator kubeconfig for `sc-bootstrap forge apply`

## When to use

Only when **all** of the following hold:

1. A fix (normally a revert or a promotion rollback) must land on a protected branch of `strange-company` (or a managed project) to restore one of the components the merge gate depends on:
   - the Agent Runner (it posts `strange-company/tdd-provenance` and `strange-company/review`; alerts `SCRunnerDown`, `SCRunnerPolicyInvalid`, `SCRunnerNotReady`);
   - Woodpecker or its agents (`ci/woodpecker/pr/ci`; `SCWoodpeckerDown`, `SCWoodpeckerNoAgents`);
   - `sc-ci-config` (fails closed, so no pipeline runs);
   - the egress-proxy (CI and worker egress);
   - the heavy-slot advertiser (CI step pods cannot schedule without `strange.company/heavy-slot` units).
2. The fix is reviewed as a normal PR by a human other than the operator, or by the operator plus a second human when a second human is available. Record who reviewed it.
3. The normal gate demonstrably cannot pass for that PR: a required context is missing or stuck `pending` for more than 15 min, because of the broken component.

Not for: TDD tasks (never merge agent work this way); routine merges; branch-protection drift caused by an attacker (`credential-compromise.md` first). Referenced by `agent-runner-failure.md`, `woodpecker-failure.md`, `flux-reconciliation-failure.md`, `restore-cnpg.md` (sc-pg / woodpecker-pg Replace), `restore-authentik.md` (identity outage: `sc-breakglass` is a local Forgejo account and works without Authentik), `stuck-task-recovery.md`, `tdd-task-recovery.md` and `merge-without-provenance.md`.

## Preconditions and required material

- Toolbox shell: `./bootstrap shell`, then `sc-env`.
- `SC-BREAK-GLASS-FORGEJO` from the recovery bundle: `./bootstrap exec -- sc-bootstrap bundle open --show SC-BREAK-GLASS-FORGEJO` (the password of `sc-breakglass`).
- An incident ID (e.g. `inc-20261014-1`) recorded **before step 2** in an issue in `strange-company` labeled `sc/incident` whose title contains the ID. The merge auditor accepts the push as `emergency_merge` only if that issue predates the push, the pusher is `sc-breakglass`, and the push falls inside the observed bootstrap-stage window (plan 55 §14.8); otherwise it records `bypass`.
- The reviewed PR number `<n>` and its head commit `<sha>`. The commit must apply as a **fast-forward** onto the protected branch tip; if it does not, rebase the PR branch first and get the review re-confirmed.

## Safety checks before acting

1. Confirm the gate is really blocked by the broken component: `curl -fsS -H "Authorization: token <your PAT>" https://git.<domain>/api/v1/repos/strange-company/strange-company/commits/<sha>/status | jq '.statuses[] | {context, state, creator: .creator.login}'`. Expected: a required context absent or `pending`. If the contexts are `failure`, do **not** use this procedure: the gate is working and found a problem.
2. Diff the PR: only the files needed for the repair. No `policy/**` loosening, no `.woodpecker/**` change unrelated to the incident, no protected-test change.
3. Check that branch protection currently matches Git (`./bootstrap exec -- sc-bootstrap forge verify`). Expected: exit 0. If it already differs, stop and follow `merge-without-provenance.md` §D, because someone else changed it.
4. Blast radius: one protected branch, for at most a few minutes.

## Steps

1. **Commit trailer.** Make sure the commit to land carries the trailer `SC-Emergency-Merge: <incident-id>`. If it does not, amend the commit on the PR branch (`git commit --amend --trailer "SC-Emergency-Merge: <incident-id>"`), push the PR branch, and have the reviewer re-approve. Expected: `git log -1 --format=%B <sha>` shows the trailer.
2. **Open the gate (bootstrap stage).** `./bootstrap exec -- sc-bootstrap forge apply --stage bootstrap`. Expected: the protected branch's push whitelist contains the break-glass identity, as committed in `forge/forge.yaml`. Plan 16 whitelists `sc-breakglass` in the bootstrap stage; push only as `sc-breakglass` (a push by `sc-admin` is audited `bypass`). The alerts `SCForgeBranchProtectionDrift` and `SCBreakGlassUsed{target="forgejo"}` fire and are expected; annotate them with the incident ID.
3. **Fast-forward push** exactly the reviewed commit as the whitelisted identity:
   ```sh
   git fetch https://git.<domain>/strange-company/strange-company.git main pull/<n>/head
   git merge-base --is-ancestor FETCH_HEAD <sha> || echo "NOT A FAST-FORWARD - STOP"
   git push https://sc-breakglass@git.<domain>/strange-company/strange-company.git <sha>:refs/heads/main
   ```
   Expected: the push succeeds without `--force`. If it is rejected as non-fast-forward, do not force it: re-apply `--stage steady` (step 4), rebase, and start over.
4. **Close the gate immediately:** `./bootstrap exec -- sc-bootstrap forge apply --stage steady`, then `./bootstrap exec -- sc-bootstrap forge verify`. Expected: exit 0 and `SCForgeBranchProtectionDrift` resolves within 15 min. Do this even if step 3 failed.
5. **Close the PR** in Forgejo with the comment `Landed by emergency merge <incident-id> as <sha>.` (Forgejo may already mark it merged.)
6. **Let Flux apply** the fix (`flux get kustomizations -A`) and confirm the broken component recovers (Runner `1/1`, Woodpecker agents connected, …).
7. **Audit after recovery.** Once the Runner is healthy, within 6 minutes the merge auditor audits the protected-branch range: `psql_ro "select outcome, merged_sha, detected_at from runner.merge_audits where merged_sha='<sha>'"` (helper as in `stuck-task-recovery.md`). Expected: `emergency_merge`. Any other unreviewed commit in the range shows `bypass`; handle it with `merge-without-provenance.md`.

## Verification

- The protected branch tip is `<sha>` or a descendant of it, and `git log -1 --format=%B <sha>` contains `SC-Emergency-Merge: <incident-id>`.
- `sc-bootstrap forge verify` exits 0 (protection is back at `steady`).
- `runner.merge_audits` has an `emergency_merge` row for `<sha>`.
- The component that was broken is healthy, and the next normal PR merges through the normal gate.

## Rollback / abort

If the landed fix is wrong, revert it with another reviewed commit. Use this procedure again only if the gate is still broken; otherwise use a normal PR. Always finish with `--stage steady`.

## Evidence to record

In the incident issue: the reason the gate could not pass (status output), reviewer names, the commit SHA, timestamps of steps 2–4, the `forge verify` output, the merge-audit row, and the annotated alerts.

## Follow-ups

- A post-incident review within 5 working days: why the component broke, and why the normal rollback path was not enough.
- If the break-glass password was revealed on a shared screen, rotate it (`rotate-break-glass.md`).
- Normal re-verification of any TDD task whose PR was open during the incident: its statuses are re-posted by the Runner after recovery.
