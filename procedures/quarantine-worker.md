# Procedure: Quarantine a worker execution

**Owning plan:** plans/26-agent-runner.md (related: 25, 27, 28, 34, 35, 55; ADR-0007, ADR-0008)
**SPEC references:** §84–§88, §94, §105, §113 (added procedure)
**Severity / urgency:** degraded/security — act within 30 min for secret exposure or unexplained ref changes; routine review for ordinary policy violations already auto-quarantined
**Expected duration:** 15–45 min
**Requires break-glass:** cluster kubeconfig; `SC-RUNNER-ADMIN`; for the manual path, a Forgejo admin or `sc-runner` token (`SC-FORGEJO-BOT-TOKENS`)

## When to use

Note: since the two-container worker pod (plan 26 §13), the harness holds no Git or Runner credential; a ref change attributed to an execution therefore points at an `sc-worker` defect, a compromised image, or a misattribution, and the image digest of the execution must be reviewed (step 4).


- Alerts: `SCWorkerPolicyViolation`, `SCWorkerQuarantined` (review), `SCRunnerUnexplainedRefChange`, `SCRunnerOrphanDeployKeys` (key of a live execution), `SCContextSecretDetected` (§S), heartbeat lost on a suspicious execution.
- A human observes a worker doing something it must not (pushing other refs, touching protected paths, leaking data into output, excessive egress attempts in the egress-proxy log).

## Preconditions and required material

- `./bootstrap shell`, `sc-env`, Runner admin helpers (`RUNNER`, `AUTH`, `psql_ro`) as in `agent-runner-failure.md`.
- The execution id `sc-<n>-x<m>` (from the alert annotation `execution_id`, or `agent_status`).

## Safety checks before acting

1. Quarantine is safe to repeat (idempotent steps). It stops the worker, revokes its task token, gateway token and deploy key, preserves logs, pod spec and ref snapshot, and quarantines refs.
2. **Do not** delete the worker Job or Secret by hand before evidence is captured; **do not** force-push or delete refs manually while the Runner is healthy (the Runner records what it does).
3. **Do not** paste secret values into issues or chat. Refer to them by class and fingerprint.
4. If the violation involves a leaked provider key or the Hermes token, also follow `rotate-model-credentials.md` §C.

## Steps

### 1. Identify

```sh
EXE=sc-<n>-x<m>; TASK=${EXE%-x*}
curl -fsS "$RUNNER/v1/tasks/$TASK" "${AUTH[@]}" | jq '{state, current_execution, last_transition}'
curl -fsS "$RUNNER/v1/tasks/$TASK/result?execution_id=$EXE" "${AUTH[@]}" | jq '{execution_state, outcome, failure_class, failure_code, violations}'
```

If `outcome` is `rejected` with violations and `execution_state` is terminal, the Runner already quarantined automatically: go to step 3 (review). Otherwise continue with step 2.

### 2. Quarantine (Runner path)

```sh
curl -fsS -X POST "$RUNNER/v1/executions/$EXE/quarantine" "${AUTH[@]}" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: quarantine-$EXE" \
  -d '{"reason":"<what was observed, ≥ 10 chars>","quarantine_branch":true}' | jq .
```

Expected: 200 `QuarantineReport` with each step `done` or `already_done`: `revoke_gateway_token`, `revoke_task_token`, `delete_deploy_key`, `capture_logs`, `capture_pod_spec`, `capture_ref_snapshot`, `quarantine_refs`, `delete_job`, `delete_secret`, `raise_alert`, and `evidence_prefix` `s3://<prefix>-evidence/quarantine/<execution-id>/`. Any `failed` step: re-run the same call (idempotent); if it keeps failing, use §M for that step. If the Runner is unreachable, use §M.

### 3. Review evidence

```sh
curl -fsS "$RUNNER/v1/tasks/$TASK/logs?execution_id=$EXE&tail_lines=500" "${AUTH[@]}" | jq -r '.lines[]' | less
curl -fsS "$RUNNER/v1/tasks/$TASK/transitions" "${AUTH[@]}" | jq -r '.transitions[] | "\(.at) \(.actor) \(.from)->\(.to) \(.reason)"'
```

- Ref changes: compare the captured ref snapshot (`quarantine/<execution-id>/refs-before.json` vs `refs-after.json` in the evidence bucket) — the task branch head was tagged `sc-quarantine/<execution-id>` and the branch reset to the last accepted commit (plan 21 §14.9; no tag for `SECRET_DETECTED`); other changed branches were tagged and deleted; **foreign tags** created during the window were recorded in the evidence prefix (name, target SHA) and deleted, so the GitHub mirror prunes them (plan 17). The Runner reports these as QuarantineReport steps `quarantine_refs` and `delete_foreign_tags`. `main` and other protected branches cannot have changed (branch protection); if they did, this is a Forgejo security incident: escalate immediately and treat the deploy-key configuration as compromised.
- Diff: `git fetch origin refs/tags/sc-quarantine/<execution-id>` in a scratch clone in the toolbox and inspect.
- Egress: `kubectl -n strange-company logs deploy/egress-proxy --since=2h | jq 'select(.execution_id=="'$EXE'")'` for denied hosts.

### 4. Decide the task outcome

- Accidental violation by a model (typical): leave the task in `NEEDS_HUMAN`; a human decides to cancel (`POST /v1/tasks/$TASK/cancel`), respecify (new task), or retry after adjusting context/instructions through a reviewed PR. Policy-class violations have 0 automatic retries.
- Deliberate-looking escape attempt (e.g. pushing other branches, harvesting credentials, repeated forbidden egress): keep the task `NEEDS_HUMAN` or move it to `BLOCKED` via a human transition with the reason; open a security issue; review the repository content that may have injected instructions (protected paths, nested instruction files, plan 27 §25 Q2).

### S. Secret found in a source or output (`SCContextSecretDetected`, `SECRET_DETECTED`)

1. Identify the class of the secret from the redaction kind (`[REDACTED:<kind>]`) and the source path (alert annotation): a Git file at a base commit, a handoff, an issue body, or a worker output.
2. If an issued execution credential leaked (gateway/task token, deploy key): the Runner has already revoked it (verify `runner.tokens.revoked_at` via `psql_ro "SELECT jti, kind, revoked_at FROM runner.tokens WHERE execution_id='$EXE'"`). No further action except §3.
3. If a long-lived credential leaked (provider key, Forgejo token, any `SC-*` class from `contracts/secret-classes.yaml`): rotate it now with the class's procedure (`rotate-secrets.md` index).
4. If the secret is in Git history (a pushed agent commit): the agent branch was deleted without a quarantine tag (plan 26/55). Ask a Forgejo admin to run repository garbage collection after rotation (`gitea admin repo-sync`-style GC; verify the Forgejo admin command), and confirm the GitHub mirror did not receive it (agent branches are not mirrored if plan 17's filter excludes `agent/*`; verify).
5. If the secret is in a base-revision file of a managed repository: rotate it, then remove it from the repository through a human PR.

### M. Manual path (Runner unavailable)

1. Stop spend: e-stop `workers` via ConfigMap (`budget-emergency-stop.md` A3).
2. Capture: `kubectl -n agent-jobs logs job/$EXE > /tmp/$EXE.log; kubectl -n agent-jobs get job $EXE -o yaml > /tmp/$EXE-job.yaml`; upload both to `s3://<prefix>-evidence/quarantine/$EXE/manual/` with the toolbox S3 client (evidence credential, plan 14).
3. Kill: `kubectl -n agent-jobs delete job $EXE --cascade=foreground` (the per-execution Secret is garbage-collected via its ownerReference).
4. Deploy key: list and delete the key titled `sc-$EXE` on the task repository through the Forgejo API (see `agent-runner-failure.md` §E step 2).
5. Refs: with a Forgejo admin token, tag the task branch head `sc-quarantine/$EXE` (skip for secret leaks) and reset/delete per plan 21; delete other unexplained branches and any tag created during the window after recording name and target SHA in the evidence prefix (`POST /repos/{o}/{r}/tags`, `DELETE /repos/{o}/{r}/branches/{name}`, `DELETE /repos/{o}/{r}/tags/{tag}`).
6. When the Runner returns, call step 2 anyway: it records the quarantine, revokes tokens in the DB (gateway token revocation is effective only once recorded) and reports remaining steps as `already_done`.

## Verification

- `kubectl -n agent-jobs get job,secret -l strange.company/execution-id=$EXE` returns nothing.
- `GET /repos/<o>/<r>/keys` has no `sc-$EXE` key.
- `psql_ro "SELECT revoked_at IS NOT NULL FROM ledger.gateway_tokens WHERE execution_id='$EXE'"` returns `t`; a Gateway request with that token would fail `SC_TOKEN_REVOKED`.
- The evidence prefix contains logs, pod spec and ref snapshots.
- The task is in `NEEDS_HUMAN`, `BLOCKED` or `CANCELLED` per the decision.

## Rollback / abort

Quarantine is not reversed. If it was a false positive, the task continues through a human transition (`POST /v1/tasks/$TASK/transitions` from `NEEDS_HUMAN` per plan 29) and a new execution; quarantined refs stay as evidence tags until retention (plan 43).

## Evidence to record

Security or incident issue (label `quarantine`): execution id, violation codes, evidence prefix, ref changes, egress denials, credentials rotated (class and new fingerprint only), decision and rationale.

## Follow-ups

- If repository content injected the behavior: PR to fix the content and, where applicable, extend protected paths (plan 22).
- If a violation pattern recurs for a harness version: consider disabling that version (`policy/harnesses.yaml` PR) and add a fake scenario reproducing it (plan 28).
- If `sc-worker` pre-checks failed to catch an accidental violation: add a regression fixture (plan 27 §22).
