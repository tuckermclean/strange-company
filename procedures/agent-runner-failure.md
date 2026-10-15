# Procedure: Agent Runner failure

**Owning plan:** plans/26-agent-runner.md (related: 12, 25, 27, 33, 36, 53, 54)
**SPEC references:** §59, §96, §113 ("Agent Runner failure"), §1.3, §4.8
**Severity / urgency:** outage for orchestration (no dispatch, no ingestion, no RED/GREEN transitions); the platform, Forgejo, CI and Flux keep working. Act within 30 min for `SCRunnerDown`.
**Expected duration:** triage 10 min; typical fix 15–60 min; DB restore cases per `restore-cnpg.md`
**Requires break-glass:** cluster kubeconfig for every section; your per-operator `SC-RUNNER-ADMIN` credential (break-glass credential from the recovery bundle only if you have none) for admin-API steps

## When to use

- Alerts (catalog names, plan 42): `SCRunnerDown`, `SCAgentRunnerReconcileStalled`, `SCRunnerPolicyInvalid`, `SCExecutionInfraFailuresHigh`, `SCEgressProxyDown` (§H). Plan 26 alert IDs that route here: `SCRunnerNotReady`, `SCRunnerDatabaseDown`, `SCRunnerDependencyDown`, `SCRunnerOutboxStalled`, `SCRunnerLogArchiveFailing`, `SCRunnerOrphanJobs`, `SCRunnerPolicyCommitUnknown`, `SCRunnerQueueBacklog`, `SCWorkerPendingCapacity` (heavy-slot wait, plan 51).
- Symptoms: Hermes reports MCP errors or 401/503; `agent_start` returns `POLICY_UNAVAILABLE` or `DEPENDENCY_UNAVAILABLE`; tasks stay in `*_VERIFYING` or executions stay `STARTING`.
- After an `sc-pg` point-in-time restore (§I).

## Preconditions and required material

- Toolbox shell: `./bootstrap shell`, then `sc-env` (kubeconfig + tunnel, plan 50). Commands below run inside it.
- For admin-API steps: your **personal** admin credential (`SC-RUNNER-ADMIN`, issued by `sc-bootstrap access join`; CD-30) — or, only if you have none, the break-glass credential from the recovery bundle (its use pages everyone). `read -rs SC_RUNNER_ADMIN; export SC_RUNNER_ADMIN; export SC_REASON="<incident id: why>"`, then:

  ```sh
  kubectl -n strange-company port-forward deploy/agent-runner 18080:8080 >/tmp/pf-runner.log 2>&1 &
  RUNNER=http://127.0.0.1:18080
  AUTH=(-H "Authorization: Bearer ${SC_RUNNER_ADMIN}" -H "X-SC-Reason: ${SC_REASON}")   # never send X-SC-Actor: the actor is your credential (CD-30)
  psql_ro() { kubectl -n strange-company exec -i "$(kubectl -n strange-company get pod -l cnpg.io/cluster=sc-pg,role=primary -o name)" -c postgres -- psql -d strange_company -At -c "$1"; }
  ```

  `psql_ro` is for **read-only** queries (label names per CNPG, verify at implementation).

## Safety checks before acting

1. If model spend may be running uncontrolled while the Runner is down (workers cannot be cancelled), engage an emergency stop through path 3 (ConfigMap) of `budget-emergency-stop.md`. The Gateway enforces it without the Runner.
2. **Do not** write to `runner.*` or `ledger.*` tables with SQL, delete worker Jobs before their logs are archived, or delete `sc-*` deploy keys of executions that are still RUNNING (use §E/quarantine instead).
3. **Do not** downgrade the Runner image below the release that applied the newest migration unless that release's notes say N−1 is compatible (plan 26 §19: only the immediately previous release is guaranteed).
4. Blast radius: the Runner is the only writer of task state; restarting it is safe (in-flight Jobs continue; reconcile on start).

## Steps

### 1. Triage (always)

```sh
kubectl -n strange-company get deploy agent-runner -o wide
kubectl -n strange-company get pods -l app.kubernetes.io/name=agent-runner
kubectl -n strange-company logs deploy/agent-runner --tail=200 | jq -r 'select(.level!="debug") | "\(.ts) \(.level) \(.msg)"' | tail -50
kubectl -n strange-company exec deploy/agent-runner -- wget -qO- http://127.0.0.1:8090/readyz || true
flux get helmreleases -n strange-company
```

Expected when healthy: 1/1 Ready; `/readyz` → `{"ready":true,"checks":{"database":"ok","migrations":"ok","policy":"ok"}}`. Pick the section by the first failing item:

| Observation | Section |
|---|---|
| `CrashLoopBackOff`, log `migration <n> failed` | §A |
| `/readyz` `database: failing`, or log `sc-pg unavailable` | §B |
| `/readyz` `policy: degraded|unavailable`, `POLICY_UNAVAILABLE` | §C |
| Ready, but `sc_runner_dependency_up{dep}` = 0 | §D |
| `SCRunnerOutboxStalled`, `SCRunnerOrphanDeployKeys`, `SCRunnerOrphanJobs`, `SCRunnerLogArchiveFailing` | §E |
| `CreateContainerConfigError` (missing Secret) or `ImagePullBackOff` | §F |
| A bad release is deployed | §G |
| `SCEgressProxyDown` | §H |
| After an `sc-pg` PITR | §I |

### A. Startup / migration failure

1. `kubectl -n strange-company logs deploy/agent-runner --previous | grep -i migration`. Expected: the failing migration number and error.
2. Checksum mismatch of an applied migration (`checksum mismatch`) means a modified migration was released: this is a release defect. Go to §G (roll back) and open a bug; never edit `schema_migrations`.
3. Lock timeout (`advisory lock`): another Runner pod is migrating. `kubectl get pods` should show only one Runner pod (Recreate). If two exist, delete the older one; the survivor proceeds.
4. Any other SQL error: roll back (§G). The previous release runs against the partially migrated schema only if the failed migration's transaction rolled back (each migration is one transaction; verify `SELECT max(version) FROM runner.schema_migrations` equals the previous release's highest migration).

### B. Database unavailable

1. `kubectl -n strange-company get cluster sc-pg -o jsonpath='{.status.phase}'`. Expected `Cluster in healthy state`.
2. If not healthy, follow `cnpg-primary-loss.md` / `restore-cnpg.md` (plan 12). The Runner reconnects automatically (backoff) and becomes Ready without a restart.
3. If healthy but the Runner still fails: check the credential file: `kubectl -n strange-company get secret sc-pg-app -o jsonpath='{.data.uri}' | wc -c` > 0. An empty or missing value → plan 35 `rotate-secrets.md#sc-pg-app`.

### C. Policy invalid or commit unknown

1. `curl -fsS "$RUNNER/v1/policy/status" "${AUTH[@]}" | jq '{status, revision, commit, last_error, rejected_revision}'`.
2. `status: degraded`: the newest policy revision failed validation; `last_error` names the file and rule. Fix by a PR to `policy/**` (protected path, human review); CI's `make policy-check` must pass. After Flux reconciles, status returns to `ok` within 60 s. Do not edit the ConfigMap `sc-policy` by hand (Flux reverts it and it is not Git-explainable).
3. `commit: null` (`SCRunnerPolicyCommitUnknown`): the deployed policy digest matches none of the last 20 `policy/` commits on Forgejo. Check `flux get kustomizations strange-company` revision and Forgejo availability; if Forgejo is down go to §D.
4. `status: unavailable` at startup with an empty DB (fresh install) means no valid policy was ever loaded: fix the policy, the pod becomes Ready after the next mount update.

### D. Dependency down (Forgejo, Woodpecker, Gateway, object storage)

1. `kubectl -n strange-company exec deploy/agent-runner -- wget -qO- http://127.0.0.1:8090/metrics | grep '^sc_runner_dependency_up'`.
2. `dep="forgejo"` → `restore-forgejo.md` triage; `dep="woodpecker"` → `woodpecker-failure.md`; `dep="gateway"` → `gateway-failure.md`; `dep="s3"` → `object-storage-outage.md`.
3. Effects to expect while down: dispatch returns `DEPENDENCY_UNAVAILABLE` (Forgejo, Gateway); verification waits (Woodpecker) and later fails per plan 22 timeouts as infrastructure; log archiving retries (S3, Job deletion delayed up to `jobTTLSeconds`).
4. Nothing to undo on the Runner after recovery: the outbox and pollers resume. Verify §Verification.

### E. Outbox stalled, orphan deploy keys, orphan Jobs, log archive failing

1. Outbox: `psql_ro "SELECT kind, count(*), min(next_attempt_at), max(attempts), left(max(last_error),200) FROM runner.outbox WHERE state <> 'done' GROUP BY kind"`. The `last_error` points to the failing dependency → §D. When fixed, entries drain within 5 min (backoff cap).
2. Orphan deploy keys (`SCRunnerOrphanDeployKeys`): list keys titled `sc-*` on each managed repository:

   ```sh
   kubectl -n forgejo port-forward svc/forgejo-http 13000:3000 >/tmp/pf-fj.log 2>&1 &
   curl -fsS -H "Authorization: token <sc-runner or admin token>" http://127.0.0.1:13000/api/v1/repos/<owner>/<repo>/keys | jq -r '.[] | select(.title|startswith("sc-")) | "\(.id) \(.title) \(.read_only)"'
   ```

   For each title, check the execution: `psql_ro "SELECT id, state, finished_at FROM runner.executions WHERE id = '<execution-id>'"`. If the execution is terminal (or unknown) and the key still exists > 10 min after `finished_at + deployKeys.graceSeconds`, the sweeper is failing: read its error in the Runner log (`grep deploy-key-sweeper`). As a manual remedy, delete the key: `curl -fsS -X DELETE … /repos/<owner>/<repo>/keys/<id>` (idempotent; 404 means already gone). Never delete a key whose execution is RUNNING — quarantine it instead (`quarantine-worker.md`).
3. Orphan Jobs: `kubectl -n agent-jobs get jobs -l app.kubernetes.io/managed-by=agent-runner -o name`; compare with `psql_ro "SELECT id FROM runner.executions WHERE state IN ('STARTING','RUNNING')"`. The Runner deletes unmatched Jobs after logging their labels; if it does not, capture the log first: `kubectl -n agent-jobs logs job/<name> > /tmp/<name>.log`, upload it with the toolbox (`aws s3 cp` against the evidence bucket, credential per plan 14) under `logs/orphan/<name>.log`, then `kubectl -n agent-jobs delete job <name>`.
4. Log archive failing: see §D (S3). After recovery the archiver retries; logs of Jobs that hit `ttlSecondsAfterFinished` are recorded as `logs_status = lost` (expected, visible in `agent_result`).

### F. Missing Secret or image

1. `kubectl -n strange-company describe pod -l app.kubernetes.io/name=agent-runner | grep -A3 -E 'Warning|Error'`.
2. Missing Secret (`agent-runner-signing`, `agent-runner-forgejo`, `agent-runner-woodpecker`, `agent-runner-s3`, `agent-runner-admin`, `forgejo-webhook` (`SC-RUNNER-WEBHOOK`, CD-08), `agent-runner-ci-intents`, `sc-registry-pull`, `model-api-keys` (Gateway), `sc-pg-app`): restore it from Git (SOPS; Flux layer `strange-company`) — `flux reconcile kustomization strange-company --with-source`. If the SOPS file is missing in Git, follow `rotate-secrets.md` for that class (plan 35). There is no fallback value (L-4).
3. `ImagePullBackOff`: the digest in `images.runner` must exist in the Forgejo registry (`skopeo inspect docker://git.<d>/strange-company/agent-runner@sha256:…` in the toolbox). If missing, revert the promotion PR (§G).

### G. Rollback of a bad Runner release (§Rollback)

1. Identify the previous good digest: `git log -p -- gitops/platform/strange-company/base/values.yaml | grep -m2 'runner:'`.
2. Open a PR reverting the promotion commit (or the chart change). From C09 on, merges into `main` require the Runner-posted `strange-company/tdd-provenance` and `review` statuses, which a broken Runner cannot post (reviews GC-02, TDD-17). If the Runner is healthy enough to post them, merge normally (human). Otherwise land the reviewed revert with `procedures/emergency-merge.md` (plan 55: break-glass protection stage, fast-forward push of the reviewed commit, protection restored, merge audit and always-notify alert). Do not `kubectl set image` (Flux reverts it and it is not Git-explainable).
3. `flux reconcile kustomization strange-company --with-source`; the Runner restarts (`Recreate`). Expected: Ready within 3 min; `schema_migrations` unchanged (no down-migrations exist).
4. If the bad release applied a migration that the previous binary cannot run with (release notes say so), do not roll back: fix forward with a new release.

### H. Egress proxy down, `SCEgressProxyDown` (§Egress)

1. `kubectl -n strange-company get deploy egress-proxy; kubectl -n strange-company logs deploy/egress-proxy --tail=100`.
2. Effects: workers and (in proxy mode) CI cannot fetch packages; executions fail as infrastructure. Dispatch continues; consider `runner.dispatchPaused: true` by PR if failures pile up.
3. Common causes: missing `sc-runner-jwks` ConfigMap or `policy/egress.yaml` invalid (plan 36). Fix by PR; Flux reconciles. If CI cannot pass because it needs the broken proxy (review security §4 cycle 2), use the emergency-merge path of §G step 2.

### I. After an `sc-pg` point-in-time restore

1. Engage `runner.dispatchPaused: true` (PR) or e-stop `workers` before the Runner starts against the restored DB, if the restore was not part of black start (ADR-0554 sets it there).
2. Start the Runner. Before dispatch is re-enabled it completes the **id-floor scan** (plan 26 §20, review RA-6): `GET $RUNNER/readyz` shows dispatch refused with `ID_FLOOR_PENDING` until the scan of Forgejo refs/PRs and S3 evidence prefixes has set `task_seq` above every observed task number; `psql_ro "SELECT task_seq_floor, reason FROM runner.id_floor ORDER BY set_at DESC LIMIT 1"` shows the new floor. Then it reconciles (plan 26 §20): executions without Jobs → `JOB_LOST`; Jobs without rows → orphan deletion; unknown `sc-*` deploy keys → deleted by the sweeper; gateway tokens issued after the restore point are unusable (not in the ledger registry).
3. List tasks whose last evidence is newer than the restore point and re-poll: `curl -fsS "$RUNNER/v1/tasks?state=RED_VERIFYING&state=GREEN_VERIFYING" "${AUTH[@]}" | jq -r '.tasks[].task_id'` — the evidence poller re-checks pipelines referenced by these tasks automatically; verify each moves on within 15 min.
4. Record lost ledger spend as cost adjustments (`pricing-update.md` step 5).
5. Re-assert the emergency-stop state: e-stop events after the recovery point are lost, so follow `budget-emergency-stop.md` §"Re-assert after restore" and compare `GET $RUNNER/v1/admin/emergency-stop` (`effective_level`, `sources`) with the intended level.
6. Release the pause/e-stop; resume BLOCKED tasks per `budget-emergency-stop.md`.

## Verification

- `kubectl -n strange-company get deploy agent-runner` 1/1 Ready; `/readyz` all `ok`; `sc_runner_leader == 1`.
- `curl -fsS "$RUNNER/v1/policy/status" "${AUTH[@]}" | jq -r .status` = `ok`.
- `sum(sc_runner_dependency_up) == count(sc_runner_dependency_up)` in Prometheus; `sc_runner_outbox_oldest_seconds < 300`; `sc_runner_deploy_keys_orphaned == 0`.
- A fake execution succeeds: dispatch `fake-success` tester on `sc-e2e/fake-target` (`POST $RUNNER/v1/tasks` with a minimal task, harness `fake-success`) → execution `SUCCEEDED` within 10 min.
- The alert(s) that triggered the procedure have resolved in Alertmanager.

## Rollback / abort

- Every step is either read-only or an idempotent API/PR action. A PR can be reverted. Manual deploy-key or Job deletions are irreversible but only target orphaned objects.
- If the Runner cannot be recovered within the RTO (≤ 4 h for sc-pg-backed state), keep the e-stop/pause in place and escalate to `restore-cnpg.md` or `total-cluster-loss.md`.

## Evidence to record

- Timeline, triggering alerts, the section(s) followed, commands run (no secret values), PR links, `/v1/policy/status` output before and after, any manually deleted deploy keys/Jobs with their execution ids, and the `SCRunnerAdminAccess` alerts generated by admin calls. File it as a Forgejo issue labelled `incident` in `strange-company`.

## Follow-ups

- Root-cause issue for any release defect (migration, policy loader).
- If a sweeper or archiver failure caused orphans, add a regression test (plan 26 §22).
- Review whether tasks affected need `tdd-task-recovery.md` or `stuck-task-recovery.md`.
