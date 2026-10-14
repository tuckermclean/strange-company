# Procedure: Partial bootstrap resume

**Owning plan:** plans/50-bootstrap-orchestrator.md (with 03, 04, 05, 06)
**SPEC references:** §8 (idempotent, safely resumable), §15, §113 ("partial bootstrap resume")
**Severity / urgency:** degraded (an install, expand, recover or destroy pipeline stopped part-way; paid resources may be running)
**Expected duration:** 10 min to diagnose + the remaining pipeline time
**Requires break-glass:** no (yes only if nodes are unreachable over SSH: `SC-BREAK-GLASS-INFRA`, provider console)

## When to use
- `./bootstrap` (or `expand`/`recover`/`destroy`) exited 1, 7, 8, 75 or 130, or the machine/terminal died during a run.
- `./bootstrap status` shows a phase `failed`, `running` (stale) or `interrupted`.
- A run exits 6 (`run lock held`) and you believe no other run is active.
- You must continue an installation from a different machine (the original is lost).

## Preconditions and required material
- A clone of the repository at the **same commit** the installation used (`git log --grep 'SC-Install-Uid'` shows the install commit; on a new machine it may not exist yet — see §new-machine).
- The same `bootstrap.env` (credentials) and the recovery passphrase.
- The recovery bundle: local `~/.strange-company/<cluster>/bundles/…` or its S3 copy (fetched automatically).

## Safety checks before acting
1. Make sure no other run is active on any machine: `./bootstrap status` shows the lock holder (`locks/bootstrap.lock`: run id, host, user, start time, lease expiry). If a holder is alive and within its lease, **wait**; never break a live lock.
2. Do not delete the journal (`.bootstrap/journal.json` or its S3 copy) to "start fresh" — phases re-probe reality, but deleting state can orphan paid resources' bookkeeping.
3. Never re-order compute by hand while an order may still be pending at the provider (duplicates cost money).
4. Do not use `--retry-phase` until you understand why the phase failed five times.

## Steps

1. **Read the status.**
   ```sh
   ./bootstrap status
   ```
   Expected: a table of pipelines/phases with status, attempt, last error code and a `next_action` line naming a section of this or another procedure. Note the failed phase and its error code. If `status` itself exits 7, go to [journal conflict](#journal-conflict).

2. **Fix the cause** named by the error code: preflight-type codes → `bootstrap-preflight-failure.md`; S3 errors (`JOURNAL_REMOTE_WRITE_FAILED`) → `object-storage-outage.md` (plan 14); provider API errors → `provider-outage.md`; seed/transfer codes (`SEED_*`, `TRANSFER_*`) → `seed-flux-handoff.md` / `failed-flux-handoff.md` (plan 19); verify failures → `bootstrap-verify.md`; passphrase errors (exit 8) → re-enter carefully (3 attempts), or `recover-opentofu-state.md#lost-passphrase` if lost. Transient network errors need no fix.

3. **Re-run the same command.**
   ```sh
   ./bootstrap            # or: ./bootstrap expand --profile small-ha / recover / destroy
   ```
   Expected: completed phases are reported `skipped (already done)` after their real-world probe; the failed phase restarts; the pipeline continues. If a paid plan changed since your last confirmation (e.g. part of an apply succeeded), you are shown a **new footprint** and must confirm again — read it: it should only contain the remainder.

4. **If the same phase fails again** with the same code and input digest 5 times, the run stops with `RETRY_CEILING`. After fixing the cause, run `./bootstrap --retry-phase <phase>` once. This resets the counter; it never marks anything done.

5. **When the pipeline reaches its end**, continue with the normal next procedure (`fresh-install.md` step 11 for installs).

<a id="lock"></a>
### Run lock held (exit 6) or stale
0. A lock left by a crashed run **on this same machine** is taken over automatically (the local lock file is free, so the holder is provably dead; the journal records `stale-lock-takeover`; plan 50 §17.4). You only get exit 6 when the holder is another host or a live process.
1. `./bootstrap status` prints the holder. Contact the person/host named there, or check that host for a running `sc-toolbox-*` container (`docker ps --filter name=sc-toolbox` / `podman ps`).
2. Only if you are certain the holder is dead (host off, container gone, or lease expired):
   ```sh
   ./bootstrap unlock
   ```
   Type `unlock <cluster> <run_id>` exactly when asked. Expected: `lock released`; the journal records an `unlock-run-lock` confirmation. Then go to step 3 above.
3. OpenTofu's own lock (`TOFU_STATE_LOCKED`) is separate: follow `recover-opentofu-state.md#stale-lock`.

<a id="journal-conflict"></a>
### Journal conflict (exit 7, `JOURNAL_CONFLICT`)
Two journal copies have the same sequence number but different content (e.g. two machines wrote without a working lock).
1. Both copies are saved in `.bootstrap/runs/<run-id>/journal-conflict/{local,remote}.json`. Compare: `./bootstrap exec -- sh -c 'diff <(jq -S . local.json) <(jq -S . remote.json)'` (run in that directory).
2. Decide which reflects reality: the copy whose `runs[-1]` is the run that actually progressed further (check provider console, S3 objects, `git log` install commits).
3. Make the chosen copy authoritative by **removing the other copy only**: if remote is right, delete `.bootstrap/journal.json` locally (the remote is re-downloaded); if local is right, move the remote aside with `./bootstrap exec -- aws --profile sc-admin s3 cp s3://<prefix>-tofu-state/<cluster>/journal/journal.json s3://<prefix>-tofu-state/<cluster>/journal/conflict-<date>.json` and then upload the local one the next run writes (the next run increments `journal_seq` beyond both). Object versioning keeps every version.
4. Re-run step 3 of the main steps. Phases re-probe reality, so a wrong choice is corrected by `Check()` probes rather than trusted blindly.

<a id="new-machine"></a>
### Continue from a different machine (original lost)
1. On the new machine: install Git and Docker/Podman; `git clone` the repository from GitHub (or your fork) and `git checkout -B main <same release tag>` (PF-04 requires a branch named `main`).
2. Restore `bootstrap.env` from your password manager (or rebuild it from the bundle, [lost operator machine](#lost-operator-machine)); restore the bundle to `~/.strange-company/<cluster>/bundles/` (or let sc-bootstrap fetch it from `s3://<prefix>-recovery/<cluster>/bundles/`).
3. Run `./bootstrap`. Expected: it loads the S3 journal (higher sequence), asks for the passphrase, and:
   - if forge-init had already pushed (journal `facts.forge_init.pushed_sha`), first fetches Forgejo `main` through a run-scoped port-forward and fast-forwards your local `main` (no re-render, no new tokens; plan 50 §20);
   - otherwise, if the install commit does not exist in this clone, re-renders `gitops/clusters/<cluster>/` **from the bundle** and recommits;
   - takes host-key pins from S3 `provider/output.json` and never re-captures keys for recorded nodes (a missing pin is `HOST_KEY_PIN_MISSING`, not trust-on-first-use);
   - issues a fresh operator kubeconfig over SSH if configure had succeeded;
   - continues at the first phase whose probe says not done (seed, forge-init and flux-bootstrap are never re-run after the source switch).
4. If the run reports `LOCAL_DIVERGED` (your local `main` has commits Forgejo does not have), do not force anything: inspect `git log forgejo/main..main` after `./bootstrap sync-local` fails, move local-only commits to a branch, reset local `main` to Forgejo's with `./bootstrap sync-local`, and re-run.

<a id="lost-operator-machine"></a>
### Lost operator machine after transfer (no `bootstrap.env`, no local pins)
1. On a new machine, clone and check out as in [new machine](#new-machine) step 1. Fetch the bundle from S3 (you need the S3 credentials and the passphrase): place the S3 keys in a minimal `bootstrap.env` with `CLUSTER_NAME`, `S3_*`.
2. ```sh
   ./bootstrap exec --unlock -- sc-bootstrap bundle export-env
   ```
   Expected: `/blackstart/<cluster>/bootstrap.env.recovered` (0600). Review it, then `mv ~/.strange-company/<cluster>/bootstrap.env.recovered ./bootstrap.env`. PF-21 (credential fingerprints) now matches by construction.
3. `./bootstrap status` and `./bootstrap verify`. Host-key pins come from S3; a new operator kubeconfig is issued over SSH with `SC-SSH-OPERATOR`. If the operator SSH key itself was on the lost machine, use the break-glass node access (`break-glass-access.md`) to install a new key, then rotate per `rotate-secrets.md#sc-ssh-operator`.
4. Treat the lost machine as possibly compromised: `credential-compromise.md` §operator-laptop (plan 35).

<a id="provision"></a>
### Provision interrupted, orphan resources, pending orders
1. If the rerun stops with `ORPHAN_PENDING_ORDER` (OVH): an order was placed but not delivered. **Wait** for delivery (OVH console → Orders); do not order again. Rerun after delivery.
2. If it stops with `ORPHAN_PAID_RESOURCE`: a machine named `sc-<cluster>-node-NN` exists at the provider but not in state. Import it:
   ```sh
   ./bootstrap state import --module compute
   ```
   Expected: an import-only plan listing exactly the orphan(s); type the confirmation; then rerun `./bootstrap`. If the orphan is **not** yours (another install with the same name — should be impossible with unique cluster names), stop and investigate in the provider console.
3. If `POST_APPLY_AUDIT_FAILED` reports unexpected resources, follow `provider-outage.md#unexpected-resources`.
4. If a delivered node fails SSH (`SSH_KEY_NOT_INSTALLED`) or substrate validation, see `provider-outage.md#unusable-node` / `bootstrap-preflight-failure.md#node-size`.
5. `PLAN_STALE`: rerun; a fresh plan is made and must be confirmed.

## Verification
- `./bootstrap status --json | ./bootstrap exec -- jq -r '[.pipelines[-1].phases[] | select(.status!="succeeded" and .status!="skipped")] | length'` prints `0` after the pipeline completes, and `.installation.state` is the expected end state (`transferred`, or `compute-destroyed` for destroy).
- For provision resumes: `plan-summary.json` of the last run shows `.changes | length == 0` and an empty orphan scan; the provider console shows exactly `EXPECTED_NODE_COUNT` machines named `sc-<cluster>-node-*`.

## Rollback / abort
Stopping is always safe (Ctrl-C → exit 130, state consistent). To abandon a partial install, run `./bootstrap destroy` (`safe-destruction.md`), which works from any partial state at or after provision.

## Evidence to record
The run ids of the failed and the successful run, the error code and the fix applied; for lock breaks and journal conflicts, which copy/holder was chosen and why (journal confirmation entries record the digests).

## Follow-ups
- Repeated `JOURNAL_CONFLICT` or stale locks: check that the S3 provider still passes conformance (`./bootstrap state verify`, plan 06).
- A crash point that did not resume cleanly is a bug against plan 50's crash-injection suite (50-AC-4): file it with the journal and run logs.
