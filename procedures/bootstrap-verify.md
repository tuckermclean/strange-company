# Procedure: Bootstrap verification

**Owning plan:** plans/50-bootstrap-orchestrator.md (checks owned by the plans listed per check in plan 50 §14.8)
**SPEC references:** §35, §113 ("bootstrap verification"), §117, §130; ADR-0020
**Severity / urgency:** routine when run on demand; degraded when the install's verify phase failed
**Expected duration:** 15–45 min per run (reconcile wait ≤ 60 min, settle 10 min, memory window 30 min on a fresh install); plus fixing time
**Requires break-glass:** no (the checks themselves exercise break-glass logins, V-20)

## When to use
- The install/expand/recover pipeline failed in `verify` (exit 1, no `CONTROL TRANSFER COMPLETE`).
- After any platform change or incident, to re-prove the installation (`./bootstrap verify` on a transferred install).
- Monthly as part of operator hygiene, and before `./bootstrap expand` or `destroy`.

## Preconditions and required material
- The repository clone, `bootstrap.env`, the passphrase (the kubeconfig is in the black-start directory; the passphrase is only needed for checks that read bundle-derived material).
- SSH access to the nodes (the toolbox tunnels the Kubernetes API over SSH).

## Safety checks before acting
1. `./bootstrap verify` performs only reads plus a temporary branch push/delete (`sc-verify/push-probe-<run-id>`) and a probe tag (`sc-mirror-probe/<run-id>`) and synthetic alerts to your notification transport. Tell other operators that test notifications will arrive.
2. Do not "fix" a failing check by suspending Flux objects, editing resources with `kubectl edit`, or deleting the check: after control transfer every fix goes through Git (PR) or the owning procedure. A manual change that makes a check pass is drift that Flux will revert.
3. V-30 (memory budget) is never waived.

## Steps

1. **Run verify.**
   ```sh
   ./bootstrap verify            # all checks
   ./bootstrap verify --only V-18,V-19   # only selected checks after a fix
   ```
   Expected: exit 0 and a summary `<pass>/<total> required checks passed`. On exit 1 continue with step 2.

2. **List failures.**
   ```sh
   ./bootstrap exec -- jq -r '.[] | select(.required and .status!="pass") | "\(.id) \(.owner_plan) \(.title)"' .bootstrap/runs/$(ls -t .bootstrap/runs | head -1)/verify.json
   ```
   Expected: one line per failing required check. Read its `evidence` field (`jq '.[] | select(.id=="V-18").evidence'`).

3. **If the reconcile wait itself timed out** (`VERIFY_RECONCILE_TIMEOUT`): find the non-ready Flux objects:
   ```sh
   ./bootstrap exec -- flux get all -A --status-selector ready=false
   ./bootstrap exec -- kubectl -n flux-system get kustomizations -o wide
   ```
   Fix per `failed-flux-handoff.md` / the component's procedure, then rerun step 1.

4. **Fix each failing check with its owning procedure** (table below), then rerun only those checks with `--only`. Expected: they pass. When all pass, rerun full `./bootstrap verify` (or `./bootstrap` to resume the install pipeline, which reaches `control-transfer`).

| Check | Owning procedure |
|---|---|
| V-01..V-08 (SPEC §35; aliases of plan 19 C1–C8, run with `./bootstrap exec -- sc-bootstrap verify control-transfer`) | V-01–V-03: `restore-forgejo.md` (plan 16); V-04–V-06: below; V-08: `github-mirror-failure.md` |
| V-04, V-05, V-06 Flux source, reconcile, seeded releases | `failed-flux-handoff.md`, `seed-flux-handoff.md` (plan 19) |
| V-08 GitHub mirror | `github-mirror-failure.md` (plan 17) |
| V-10 nodes, k3s, etcd | `lost-node.md` (plan 47), `lost-etcd-member.md` (plan 07) |
| V-11 node baseline / external port scan | plan 08 node-baseline procedure: fix by PR to `infra/ansible/**`, then re-apply as plan 08 describes (the install pipeline does not re-run `configure` after transfer, plan 50 §14.7) |
| V-12, V-13, V-14, V-15 network policy, admission, agent namespace, RBAC | plans 36/37/51/54 procedures (fix in Git) |
| V-16, V-17, V-35 certificates, edge | `certificate-failure.md` (plan 10) |
| V-18 CNPG | `cnpg-primary-loss.md`, `restore-cnpg.md` (plan 12) |
| V-19 Longhorn | `longhorn-replica-loss.md` (plan 13) |
| V-20 Authentik, SSO, break-glass | `restore-authentik.md`, `break-glass-access.md` (plan 15) |
| V-21 Woodpecker | `woodpecker-failure.md` (plan 20) |
| V-22, V-23 Prometheus/Grafana/OTel | `monitoring-failure.md` (plan 38) |
| V-24 notifications / heartbeat | `notification-failure.md` (plan 42). The gate is automatic delivery evidence; acknowledging the nonces (`./bootstrap verify --ack-notifications`) is optional |
| V-25, V-37 restore tests (primed by `verify.dr-prime` in the same run) | `restore-test-failure.md` (plan 44); re-prime with `./bootstrap verify --only V-25,V-37` |
| V-26 image pins and published release signatures | plan 23 supply-chain procedure. The installation's own signing chain is a post-transfer step, not a verify failure (CD-19) |
| V-30 memory budget | [below](#memory-budget) |
| V-31 SPEC §130 DoD | its sub-items point at the checks above; a failing (a) or (c) means a phase was completed manually or an unexpected prompt occurred — report a bug |
| V-32 recovery material | `rotate-secrets.md#recovery-bundle` (plan 35): re-upload with `./bootstrap rotate recovery-bundle` if the S3 copy is missing or its digest differs |
| V-33 OpenTofu state (only the modules that exist; `existing` + `existing` installs pass with no module) | `recover-opentofu-state.md`; `OBJSTORE_STATE_UNPROTECTED` warnings: `bootstrap-preflight-failure.md#s3-locking` |
| V-34 SOPS recipients | `rotate-age-key.md` (plan 35) |
| V-36 threshold component list | install the missing component via Git (its owning plan) |

<a id="memory-budget"></a>
### V-30 memory budget failed (ADR-0020)
1. Read the evidence: `jq '.[] | select(.id=="V-30").evidence'` shows per node `platform_ws`, `alloc`, `worker_max`, `ci_job`, `min_mem_available`, the PromQL used, and the node shape.
2. Identify the largest consumers:
   ```sh
   ./bootstrap exec -- kubectl top pods -A --sort-by=memory | head -20
   ```
3. Decide, **in Git**:
   - If a component exceeds its plan's stated requests/limits, fix its values (component plan) by PR.
   - If the node is an 8 GiB (`constrained`) node and the platform genuinely does not fit: 8 GiB stays an allowed configuration (CD-23), but this node fails V-30 (report line `this 8 GiB node does not pass the measured budget; use a larger node (CD-23)`). Move to a larger node (12 GiB or more; on OVH VPS-2, or VPS-3 as the upgrade path, plan 04 §9.2.1) through node replacement (plan 47 add-node/remove-node) or a fresh install. A 12 GiB node that fails after request calibration moves to the next class the same way (CD-24).
   - Do **not** lower `worker_max` or the CI job limit just to pass; resource profiles are policy changes reviewed under plan 51.
4. Re-run `./bootstrap verify --only V-30` after the change has reconciled (the 30 min window starts after settle).

## Verification
`./bootstrap verify` exits 0; `verify.json` satisfies `jq '[.[] | select(.required and .status != "pass")] | length' == 0`, and the report (`./bootstrap report`) shows the new verify summary. For an install, `./bootstrap status --json | jq -r .installation.state` = `transferred`.

## Rollback / abort
Verify changes nothing persistent except: the `sc-verify/push-probe-<run-id>` branch and the `sc-mirror-probe/<run-id>` tag (deleted in the same run; if the run is interrupted, delete it with `git push forgejo --delete sc-verify/push-probe-<run-id>` from `./bootstrap exec --` and on GitHub if mirrored) and synthetic alerts (resolve themselves).

## Evidence to record
`verify.json` and `report.json` of the passing run (S3 `…/tofu-state/<cluster>/runs/<run-id>/`), and for V-30 the evidence block (it is the SI-01 measurement record).

## Follow-ups
- Each failing check fixed by a code or config change gets a regression test in its owning plan's suite.
- New failure classes discovered here are added to this table.
