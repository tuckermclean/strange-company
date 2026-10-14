# Procedure: Hermes failure

**Owning plan:** plans/24-hermes.md (related: 13, 15, 26, 33, 53, ADR-0003, ADR-0401)
**SPEC references:** §4.6, §43, §96, §113 ("Hermes failure"), lessons L-1, L-5
**Severity / urgency:** degraded — human orchestration is unavailable; the Runner, running workers, CI and Flux continue. Act within the working day; within 1 h if Hermes misbehaves (§H).
**Expected duration:** 10–45 min; volume restore up to 2 h
**Requires break-glass:** cluster kubeconfig; `SC-RUNNER-ADMIN` for §B/§H rotation calls

## When to use

- Alerts: `SCHermesDown` (blackbox `hermes-gateway`), `SCHermesRestartLoop`, `SCHermesMcpSilent`, `SCHermesCredentialRotationFailed`, `probe_success{probe="public-hermes"} == 0`.
- Symptoms: dashboard login loops or errors; Hermes answers with errors (Hermes API returns HTTP 200 with `finish_reason: "error"`, L-1); Hermes does not progress tasks; unexpected dispatch storms or orchestrator spend.
- Before upgrading Hermes (§I).

## Preconditions and required material

- `./bootstrap shell`, `sc-env`. Runner admin helpers as in `agent-runner-failure.md` (`RUNNER`, `AUTH`).
- Hermes API access (machine path, never the dashboard):

  ```sh
  kubectl -n hermes port-forward svc/hermes 18642:8642 >/tmp/pf-hermes.log 2>&1 &
  HKEY=$(kubectl -n hermes get secret hermes-managed-env -o jsonpath='{.data.\.env}' | base64 -d | sed -n 's/^API_SERVER_KEY=//p')
  hermes_api() { curl -fsS -H "Authorization: Bearer $HKEY" "http://127.0.0.1:18642$1" "${@:2}"; }
  ```

  Keep `HKEY` in the shell only.

## Safety checks before acting

1. Hermes is non-authoritative: tasks, evidence and costs live in `sc-pg`, S3 and Forgejo. Wiping `/opt/data` loses conversations and learned memory, not work. Prefer restore (§F) over wipe, but a wipe is safe.
2. **Do not** enter provider keys in the dashboard or put them in `/opt/data` (managed scope wins anyway; the preflight deletes them).
3. **Do not** give Hermes Kubernetes, Forgejo or provider credentials to "fix" it.
4. If Hermes is suspected compromised or is spending unexpectedly, contain first (§H) before diagnosing.

## Steps

### 1. Triage

```sh
kubectl -n hermes get deploy,pods,pvc
kubectl -n hermes describe pod -l app.kubernetes.io/name=hermes | sed -n '/Events/,$p' | tail -20
kubectl -n hermes logs deploy/hermes -c managed-scope-preflight --tail=20
kubectl -n hermes logs deploy/hermes -c hermes --tail=100
hermes_api /v1/models | jq .
```

Healthy: pod 2/2 init done, 1/1 Ready; preflight log `ok removed_keys_count=<n>`; `/v1/models` lists `hermes-agent`.

| Observation | Section |
|---|---|
| `CrashLoopBackOff`, log mentions TTY/terminal or interactive mode | §A |
| preflight exit 1, or log `API_SERVER_KEY` missing | §B |
| preflight `removed_keys_count > 0` repeatedly, or answers come from an unexpected model | §C |
| Turns return `finish_reason: "error"` | §D |
| `ContainerCreating` > 5 min with `Multi-Attach error` | §E |
| Hermes errors reading `state.db` / volume faulted | §F |
| Hermes up but tasks do not progress / MCP errors | §G |
| Dispatch storm, unexpected orchestrator spend, suspected injection | §H |

### A. Wrong run mode (L-1: bare `hermes` is a TUI)

1. `kubectl -n hermes get deploy hermes -o jsonpath='{.spec.template.spec.containers[0].args}'` must be `["gateway"]` (plan 24 §9.1).
2. If different, the chart or values were changed: revert by PR. If correct and still crashing after an image upgrade, the upstream entrypoint changed: roll back the image (§I step 4) and record the finding for 24-R-5.

### B. Managed scope missing, incomplete or rotation failed

1. `kubectl -n hermes get secret hermes-managed-env -o json | jq '.data | keys'` must list `.env` and `config.yaml`.
2. Missing or incomplete: the Runner re-renders it within 60 s if healthy. Check `kubectl -n strange-company logs deploy/agent-runner | grep -i hermes-managed`. If the Runner is down → `agent-runner-failure.md`.
3. Force re-rendering/rotation:

   ```sh
   curl -fsS -X POST "$RUNNER/v1/admin/hermes-credentials:rotate" "${AUTH[@]}" -H 'Content-Type: application/json' \
     -H "Idempotency-Key: hermes-rotate-$(date -u +%Y%m%dT%H%M%SZ)" \
     -d '{"credentials":["gateway_token","mcp_token","api_server_key"],"revoke_previous":"after_overlap","reason":"hermes-failure procedure §B"}' | jq .
   ```

   Expected: 200 with new ids and `managed_revision`; Hermes restarts (`kubectl -n hermes rollout status deploy/hermes --timeout=5m`).
4. `SCHermesCredentialRotationFailed`: the Runner log names the failing step (RBAC on `hermes-managed-env`, Deployment patch, ledger insert). RBAC drift → compare with plan 26 §13 (chart reconcile fixes it).

### C. Credential precedence hazard (L-1)

1. `kubectl -n hermes exec deploy/hermes -c hermes -- cat /opt/data/.sc-preflight.json` (shows `removed_keys_count`, never values).
2. Repeated non-zero counts mean someone keeps entering keys in the dashboard. Remind users; managed scope already overrides them, and the NetworkPolicy blocks other providers.
3. Confirm Hermes uses the Gateway token: the latest orchestrator ledger row's jti equals the current one: `psql_ro "SELECT token_jti, started_at FROM ledger.cost_records WHERE token_kind='orchestrator' ORDER BY started_at DESC LIMIT 1"` (helper from `agent-runner-failure.md`).

### D. Turns fail (`finish_reason: "error"`)

1. Reproduce through the API:

   ```sh
   SID=$(hermes_api /api/sessions -X POST -H 'Content-Type: application/json' -d '{"title":"sc-diagnostic"}' | jq -r .id)
   hermes_api "/api/sessions/$SID/chat" -X POST -H 'Content-Type: application/json' -d '{"message":"Reply with the word ok."}' | jq '{finish: .choices[0].finish_reason, content: .choices[0].message.content}'
   hermes_api "/api/sessions/$SID" -X DELETE
   ```

   Always check `finish_reason`; HTTP 200 does not mean success (L-1).
2. Content contains `SC_EMERGENCY_STOP` → an e-stop at level `all` is active (`budget-emergency-stop.md`). `SC_BUDGET_EXHAUSTED` on `orchestration-*` → orchestration budget exhausted (plan 33). `SC_TOKEN_REVOKED`/`SC_TOKEN_EXPIRED` → §B. `SC_MODEL_NOT_ALLOWED` → the managed model pin disagrees with `ModelRouting.orchestrator`: fix `hermes.orchestrator.*` values by PR (CI `make check-chart-policy-sync`). Connection errors → `gateway-failure.md`.

### E. Multi-Attach after rollout (L-5)

1. `kubectl -n hermes describe pod -l app.kubernetes.io/name=hermes | grep -i multi-attach` and `kubectl get volumeattachments | grep $(kubectl -n hermes get pvc hermes-data -o jsonpath='{.spec.volumeName}')`.
2. If the old pod still exists (Terminating), wait up to 5 min for Longhorn to detach. If the old node is `NotReady` (lost), force-delete the old pod: `kubectl -n hermes delete pod <old> --force --grace-period=0` — only after confirming the node is down (`kubectl get node <n>`), otherwise two writers could attach.
3. If the VolumeAttachment for a dead node persists > 10 min after the pod is gone, delete it: `kubectl delete volumeattachment <name>`; Longhorn then attaches to the new node (verify in `restore-longhorn.md` §attachment).
4. Expected: new pod Running within 5 min of the detach.

### F. Volume degraded, faulted or corrupted

1. Longhorn volume state: `kubectl -n longhorn-system get volumes.longhorn.io <pv-name> -o jsonpath='{.status.robustness}'`. `faulted` → `restore-longhorn.md`.
2. Corrupted `state.db` (Hermes log errors at start) — choose:
   - **Restore** (default): merge a PR setting chart value `hermes.replicas: 0` (keeps the rest of the HelmRelease reconciling; review RA-m10) and wait until the pod is gone; restore the latest Longhorn backup into the stable volume behind `hermes-data` per `restore-longhorn.md` (the PV/PVC/Volume triplet is plan 13's, not the chart's); then a PR back to `hermes.replicas: 1`.
   - **Reset** (if no usable backup): with `hermes.replicas: 0` as above, recreate the stable-named `hermes-data` volume empty following `restore-longhorn.md` §"recreate empty stable volume" (plan 13 owns the triplet; never delete the PVC and let something else recreate it under a new name), then `hermes.replicas: 1`. Hermes resynchronizes task knowledge through `agent_list`.
   - If a PR cannot be merged quickly, `flux suspend helmrelease -n strange-company strange-company` is the fallback, for at most 60 minutes, because it also freezes Runner, Gateway and egress-proxy drift correction.
3. Expected: Hermes Ready; §D diagnostic turn succeeds.

### G. Hermes up, but no progress

1. Runner side: `kubectl -n strange-company exec deploy/agent-runner -- wget -qO- http://127.0.0.1:8090/metrics | grep sc_runner_mcp_requests_total` — counters must increase during Hermes activity.
2. 401s at the Runner (`code="UNAUTHENTICATED"`): MCP bearer mismatch → §B (rotate `mcp_token`).
3. No requests at all while non-terminal tasks exist (`SCHermesMcpSilent`): the watcher job is not running. Inspect Hermes' scheduler (`hermes cron list` inside the pod, verify command) and the skill files under `/opt/data/skills/strange-company/` (read-only mount from Git). A broken skill or interval is fixed by PR to `charts/strange-company/files/hermes/`.
4. Meanwhile, humans can progress tasks directly with the Runner admin API (`POST /v1/tasks/{id}/executions`), exactly as Hermes would.

### H. Misbehavior or suspected compromise

1. Contain: revoke and disable Hermes in one step (review RA-10). Unlike a rotation, this does not re-arm Hermes with new credentials:

   ```sh
   curl -fsS -X POST "$RUNNER/v1/admin/hermes-credentials:revoke" "${AUTH[@]}" -H 'Content-Type: application/json' \
     -H "Idempotency-Key: hermes-revoke-$(date -u +%Y%m%dT%H%M%SZ)" \
     -d '{"reason":"suspected compromise: <what was observed>"}' | jq .
   ```

   Expected: `hermes_disabled: true`. From now on the Runner's MCP listener answers 401 to every bearer, the Hermes gateway token is revoked (its model calls fail `SC_TOKEN_REVOKED`), and the Runner does not re-render `hermes-managed-env`.
2. If Hermes may also be spending through an unexpected path, or the Runner is unavailable, engage e-stop `all` as well (`budget-emergency-stop.md` A1, or A3 without the Runner). Then stop the pod by PR `hermes.replicas: 0` and keep `/opt/data` untouched as evidence.
3. Review what it did: `curl -fsS "$RUNNER/v1/tasks?limit=100" "${AUTH[@]}" | jq` and the transitions with actor `hermes` since the suspected start; cancel unwanted tasks with `POST /v1/tasks/{id}/cancel` (`disposition: needs_human`).
4. Preserve evidence: on-demand Longhorn snapshot of `hermes-data` (Longhorn UI via port-forward or `kubectl -n longhorn-system create -f` a `Snapshot` object for the volume; verify CRD at implementation).
5. Release the e-stop only after the cause is understood (`budget-emergency-stop.md` §release).
6. Re-enable is a separate human decision after cleanup (inspect and, if needed, reset `/opt/data` per §F; review skills and dashboard-added content): `POST $RUNNER/v1/admin/hermes-credentials:enable` with a reason, then `hermes.replicas: 1`.

### I. Before an upgrade / rolling back an upgrade

1. Take an on-demand Longhorn backup of `hermes-data` and wait for completion (plan 13).
2. Confirm the Renovate PR passed `make hermes-contract-test`.
3. After merge and rollout, run §D's diagnostic turn and one MCP read (`agent_list`) from the dashboard.
4. Rollback: revert the image PR; if Hermes cannot read its data after the downgrade, restore the pre-upgrade backup (§F restore).

## Verification

- `kubectl -n hermes get deploy hermes` 1/1; `probe_success{probe="hermes-gateway"} == 1` and `probe_success{probe="public-hermes"} == 1` in Prometheus.
- §D diagnostic turn returns `finish_reason` ≠ `error` and a ledger row with `token_kind='orchestrator'` appears.
- `sc_runner_mcp_requests_total` increases after asking Hermes for task status in the dashboard.
- Triggering alerts resolved.

## Rollback / abort

- Rotations are forward-only; a rotated credential cannot be restored (none needs to be).
- A reset PVC cannot be undone; prefer restore when unsure.
- If Hermes cannot be recovered, operate via the Runner admin API until it is (all task operations are available there).

## Evidence to record

Incident issue in `strange-company` (label `incident`): timeline, section followed, rotation responses (ids only), restore/reset decision, Longhorn backup/snapshot names, tasks cancelled.

## Follow-ups

- Any newly observed upstream behavior becomes a test in `make hermes-contract-test` (24-R-5) and, if relevant, a note in `plans/reference/prior-implementation-lessons.md` via PR.
- Repeated dashboard credential entries: user guidance in the Hermes skills/SOUL.
