# Procedure: Total cluster loss (black start from GitHub and object storage)

**Owning plan:** plans/45-recovery.md (§14)
**SPEC references:** §108, §110, §111, §112, §113 ("total cluster loss"), §22, §114
**Severity / urgency:** disaster
**Expected duration (two RTOs, CD-32; clock from the decision to rebuild, plan 45 §14.3):** Forgejo serving `main` with Flux reconciling (SR-1) after 2 h 35 – 3 h 05 min (target ≤ 4 h); all data available (end of step 9) after 3 h 05 – 3 h 35 min (SPEC §108 ≤ 4 h); `RECOVERY VERIFIED — MIRROR PENDING` after 3 h 30 – 4 h; Runner dispatching again (SR-2) after 4 h 10 – 5 h 10 min (target ≤ 6 h)
**Requires break-glass:** yes — `SC-BREAK-GLASS-INFRA` (provider console) if the provider account itself is involved; the recovery bundle (`SC-RECOVERY-PASSPHRASE`) always; the offline recovery age identity (`SC-AGE-RECOVERY`) only if the bundle is unusable

## When to use

- All nodes are gone or unrecoverable (self-hosting-minimum: the single node's disk is lost).
- etcd is lost and plan 07's decision tree (`restore-etcd.md` §Decision) says **rebuild** (no intact member, and no usable disk + snapshot + token combination).
- Provider region lost and you choose to rebuild elsewhere (`provider-outage.md` points here).
- Triggering signals: external heartbeat alarm with the API unreachable; `SCNodeDown` on every node; provider console shows instances deleted.

Do **not** use this procedure if any etcd member is intact (`restore-etcd.md` §B), or for a single failed component (`restore-forgejo.md`, `restore-cnpg.md`, `restore-longhorn.md`, `restore-authentik.md`).

## Preconditions and required material

| Material | Where | If missing |
|---|---|---|
| A machine with Git and Docker or Podman | any | — |
| GitHub `strange-company` repository (`main`) | `https://github.com/<owner>/strange-company` | `github-black-start.md` §Alternative seed |
| `bootstrap.env` with provider, Cloudflare, S3 (admin), GitHub mirror, notification and model credentials | operator records / provider consoles | re-create from provider consoles; immutable fields must equal `gitops/clusters/<cluster>/bootstrap-config.yaml` |
| Recovery bundle | `${SC_BLACKSTART_DIR:-$HOME/.strange-company}/<cluster>/bundles/` or `s3://<prefix>-recovery/<cluster>/bundles/LATEST.json` | plan 35 §14.3 lost-bundle path with the offline identity (`rotate-age-key.md` §lost-bundle) |
| Recovery passphrase | operator memory / vault | offline identity path as above |
| Offline recovery age identity | operator offline storage | needed only if the bundle is unusable; if both are lost → §Both lost |
| Object storage reachable with backups | provider | if lost too → §No backups |

All tools come from the toolbox (`./bootstrap shell` / `./bootstrap exec -- <cmd>`). `./bootstrap recover` drives every step; the numbered steps below say what it does, what to expect, and what to decide.

## Safety checks before acting

1. **Is the old installation really gone?** If any old node might still be running (unreachable is not gone), do **not** attest it destroyed: keep `--old-installation unknown` so recovery fences it (ADR-0554). Two live installations would interleave backups, fight over the tunnel and could push stale refs to GitHub. `recover` also probes liveness before fencing and refuses with `FENCE_REFUSED_LIVE` if the old installation answers.
2. **Pause the external heartbeat check** only if you are certain of the outage; otherwise leave it alarming.
3. **Never** run `./bootstrap destroy`, delete buckets, delete object versions, or force-push to GitHub during recovery.
4. **Do not reuse old node disks** unless plan 07's snapshot-restore conditions hold (then this is the wrong procedure).
5. Record the **loss time** (last known healthy timestamp) for the RPO evidence.
6. Know the trust anchor: `recover` compares GitHub `main` with `refs/heads/main` of the newest `repo-backups` manifest (written ≤ 30 min before the loss). Note any other SHA you trust (your own clone, the last control-transfer report in `s3://<prefix>-tofu-state/<cluster>/reports/`) to cross-check in step 2.

## Steps

1. **Clone and prepare** (≈ 10 min).
   ```sh
   git clone https://github.com/<owner>/strange-company.git && cd strange-company
   cp /secure/location/bootstrap.env .        # or recreate it (plan 45 S20)
   ./bootstrap version                        # pulls the toolbox by digest, or builds it (ADR-0013)
   git log -1 --format='%H %cI'               # G
   ```
   **Expected:** the toolbox runs. During a GitHub platform outage the clone or the toolbox pull fails → `github-black-start.md` §D (plan 45 S19b).
2. **Start recovery: preflight, unlock, state, trust check** (≈ 25 min).
   ```sh
   ./bootstrap recover --old-installation unknown
   ```
   Enter the decision time (`decidedAt`, the RTO clock start) and the passphrase when prompted. **Expected:** preflight passes (exit 3 → `bootstrap-preflight-failure.md`); `unlock: bundle v<N> install_uid=<uuid> OK`; `state-recover: compute serial <s> lineage <l> verified; provider inventory: <list>`; `github trust: G == anchor` (or ancestor).
   - Exit 8 (bundle/passphrase) → §Bundle.
   - Undecryptable state → `recover-opentofu-state.md` §lost-passphrase (import rebuild), then rerun.
   - `GITHUB_COMMITS_UNTRUSTED` → recover lists the commits on GitHub beyond the newest `repo-backups` manifest ref. Inspect each (author, merge-PR trailer). If every commit is legitimate, type `accept-github-commits <n>`; otherwise stop and go to `github-black-start.md` §B (recover can seed at the anchor instead).
3. **Liveness probe and fencing** (≈ 15 min, part of the `plan` phase).
   **Expected:** `liveness: api=unreachable healthz=unreachable newest-wal-age=<h> → not alive`, then `fence: object-storage rotated, github-mirror rotated, tunnel rotated, dr-tests-results rotated, telemetry-debug rotated; SOPS outputs written`. For the GitHub mirror, recover prompts you to create a new fine-grained token and to revoke the old one in GitHub; it verifies the old token returns 401. It asks whether to rotate model API keys if the probe was inconclusive (`rotate-model-credentials.md`).
   - `FENCE_REFUSED_LIVE` → the old installation answers. **Stop**: this is probably not a total loss (`lost-node.md`, `restore-etcd.md`). Type `fence-live <cluster>` only if you are certain you want to replace a running installation.
   - `FENCE_UNVERIFIED` → revoke the named credential in the provider console manually, then rerun the same command (idempotent: no second fencing).
   - Only if you are **certain** the old installation is destroyed (e.g. the provider shows the instances deleted): rerun with `--old-installation destroyed` and type `destroyed <cluster>`.
4. **Old nodes and provision** (≈ 30–60 min). Recover lists compute resources of the lost installation still in state; with `unknown`, confirm moving them out of the managed set (they are recorded as `fencedResources` and destroyed later by §Fenced installation cleanup). Review the printed footprint and plan, then type `create <cluster> <D8>` (paid) or confirm the `existing` hosts. **Expected:** new nodes `node-NN` with unused numbers; `inventory.yaml` updated. Provider capacity errors → `provider-outage.md` (another region is allowed in `recover`).
5. **Configure k3s** (≈ 20 min, automatic). **Expected:** `configure: N/N nodes Ready, k3s <version>`.
6. **Recovery overlay and seed** (≈ 40 min, automatic). Watch for:
   - `backup discovery: forgejo-pg g<N> (base <ts>, WAL to <ts>) …` for all four clusters and `forgejo-data backup <name> (<ts>)`, `hermes-data …`.
     - `NO_CNPG_BACKUP <cluster>` → decide: wait for object storage, or type `accept-empty <cluster>` (that database starts empty; record it). For `authentik-pg`, recover then keeps the Authentik layer suspended until `sso-init --from A0` has run (step 9).
     - `GENERATION_MISMATCH <cluster>` (S3's highest generation differs from the generation committed in Git) → inspect `aws s3 ls s3://<prefix>-pg-backups/<cluster>/`; confirm with `accept-generation <cluster> <N>` only for the generation that holds the newest WAL.
     - Newest WAL older than 15 min before the loss → recover prints the expected data loss; confirm or investigate.
     - A volume without a backup → printed; no restore component for it (Forgejo repositories then come from bundles/GitHub in step 7).
   - `recover.commit: G' = <sha> (recover(<cluster>): recovery overlay run <id>)` — it includes the fencing SOPS files and `policy/budgets.yaml emergencyStop=workers`.
   - `create window: flux-system/sc-cnpg-create-window runId=<id>` (plan 50; Clusters carry `strange.company/recovery-run=<id>`, plan 12). A `Cluster` denied by `sc-cnpg-cluster-create-guard` means the window or annotation is missing → rerun `./bootstrap recover`.
   - Seed progress per layer; `forgejo-pg Ready (timeline <t>)`, `forgejo-data restored`, `Forgejo healthz pass`.
   Failure → `seed-flux-handoff.md` (seed issues) or plan 45 §15; the run is resumable by rerunning `./bootstrap recover`.
7. **Forgejo recovery and ref reconciliation** (≈ 15 min). **Expected:** forge-init steps `verified`; a ref report:
   ```text
   strange-company main: forgejo=<F> github=<G> → fast-forward to G' (or: rebuilt recovery commit on F, re-seeded)
   release/*: …   tags fetched from GitHub: <n>
   projects/*: <k> repositories fast-forwarded from bundles, <d> divergent refs saved under refs/recovered/<run-id>/
   ```
   `REFS_DIVERGED strange-company main` → stop; decide with `github-mirror-failure.md` §C (never force); rerun recover afterwards. With `--pitr-target T`, confirm the proposed heads (all ≤ T).
8. **Source switch** (≈ 10 min, automatic; its end is milestone **SR-1**, logged as `rto: sr1 <seconds>`). **Expected:** `transfer: root sourced from GitRepository/flux-system @ <sha>; seeded UIDs unchanged; Bucket deleted`.
9. **Restore remaining state** (≈ 30 min, automatic; its end is the **data-availability** milestone, logged as `rto: data-availability <seconds>`). **Expected:** `authentik-pg`, `woodpecker-pg`, `sc-pg` Ready from `g<N>` archiving to `g<N+1>`; first base backups completed; `hermes-data` restored (or created empty); observed RPO per database printed from the heartbeat markers (`observed RPO: forgejo-pg 412 s …`); `create window closed` (`kubectl -n flux-system get cm sc-cnpg-create-window` → NotFound). If `authentik-pg` was accepted empty: run `./bootstrap exec -- sc-bootstrap sso-init --from A0` when prompted (plan 15 §18); recover then resumes the Authentik layer.
10. **Recovery verify** (≈ 25 min, automatic). **Expected:** the report starts with
    ```text
    RECOVERY VERIFIED — MIRROR PENDING
    ```
    and lists `mirror compare-refs: GitHub refs are ancestors of Forgejo refs; nothing pushed`, `g<N+1> base backups: 4/4`, observed RPO per database. It does **not** say `CONTROL TRANSFER COMPLETE` yet (the mirror is still disabled). Any required check failing → `bootstrap-verify.md`; rerun `./bootstrap recover` after fixing (it resumes at verify).
11. **Reconnect the GitHub mirror.**
    ```sh
    ./bootstrap recover --step forge-reconnect
    ```
    **Expected:** refs compared again (fast-forward only), mirror configuration recreated with the new token, `mirror: still disabled until finalize`.
12. **Finalize.**
    ```sh
    ./bootstrap recover --step finalize
    ```
    It opens the Forgejo PR `bootstrap/recovery-finalize-<run-id>` authored by `sc-admin`. Review the diff (only `gitops/clusters/<cluster>/kustomization.yaml`, `seed/kustomization.yaml`, `vars/cluster-vars.yaml` (Longhorn keys removed), `config/github-mirror.yaml`, and `policy/budgets.yaml` `spec.emergencyStop` → `none`), wait for Woodpecker CI, approve and merge with your own account. Rerun the same command after the merge. **Expected:** `mirror verify: GitHub main == Forgejo main`; `restore tests: <n>/<n> forced runs passed (new generations)`; `forge finalize: protection stage steady`; `bundle v<N+1> (black-start) sealed; attestation written` (it asks for your offline recovery identity); and the final report:
    ```text
    CONTROL TRANSFER COMPLETE
    CONTROL TRANSFER COMPLETE (RECOVERY)
    ```
    A failing forced restore test → `restore-test-failure.md`; rerun finalize after fixing.
13. **Resume workers.**
    The finalize PR reset `policy/budgets.yaml` `spec.emergencyStop.level` to `none`. Confirm no other source holds a stop: `./bootstrap exec -- kubectl -n strange-company get configmap sc-emergency-stop` → NotFound. Confirm the effective level is `none` (Gateway `/sc/v1/status` via port-forward, or `PQ 'max(sc_emergency_stop_level{source="effective"})'` = 0). List blocked tasks (`GET /v1/tasks?state=BLOCKED&blocked_reason=emergency_stop`) and resume each one deliberately with the human transition API (plan 33 §14.5); cancel tasks whose work is obsolete. The first dispatch is milestone **SR-2**; `recover` records it in the report as `rto_sr2_seconds` (target ≤ 6 h from the decision).
14. **Re-enable outage detection:** resume the external heartbeat check if it was paused; confirm a ping arrives.

### §Bundle (exit 8)
- Wrong passphrase three times: stop and recover the passphrase from your vault. Do not guess repeatedly.
- Bundle unreadable or lost but the offline identity is available: `rotate-age-key.md` §lost-bundle-black-start (plan 35; it works without a cluster: new cluster key generated offline, tier-1 re-encrypted, files carried by the recovery commit, new bundle sealed before seed); then rerun step 2.
- Both lost: §Both lost.

### §No backups (object storage also lost)
Plan 45 S16. If the provider or account is gone, first run `replace-object-storage.md` (plan 14/50) to establish a new endpoint and buckets. Then recover with `accept-empty` for every database; repositories other than `strange-company` are lost unless an operator clone exists. An empty `authentik-pg` needs `sso-init --from A0` before Authentik serves (step 9). Record every loss in the evidence.

### §Both lost (bundle/passphrase and offline identity)
Plan 45 S24: a black start from Git secrets is impossible. Perform a fresh installation (`fresh-install.md`) with new credentials; restore databases from S3 only if their backup credentials can be re-issued by the provider (CNPG backups are not client-side encrypted); every SOPS-encrypted value is lost.

### §Split brain (the old installation turns out to be alive)
Immediately: revoke the old installation's object-storage keys, GitHub token and tunnel credential at the providers (fencing), power off its nodes from the provider console (`SC-BREAK-GLASS-INFRA`), and inspect `pg-backups` for writes into `g<N>` after the recovery started (they are harmless to `g<N+1>` but must be recorded).

### §Fenced installation cleanup
When the provider API for the lost installation's region is reachable again: for each ID in `config/recovery.yaml` `fencedResources`, confirm in the provider console that it is not part of the new installation, then delete it there (or with `./bootstrap exec -- sc-bootstrap provider delete --id <id> --confirm-fenced <cluster>`, plan 03/50). Its etcd snapshots in `etcd-snapshots/<install-id>/etcd-snapshot-<old node>-*` are left to the lifecycle rule. Record each deletion in the incident.

## Verification

- `./bootstrap recover --step finalize` printed `CONTROL TRANSFER COMPLETE` and `CONTROL TRANSFER COMPLETE (RECOVERY)`; `verify.json` contains no required failures, and V-08 and V-37 are `pass`.
- `./bootstrap exec -- kubectl get cluster -A -o jsonpath='{range .items[*]}{.metadata.name}={.spec.plugins[0].parameters.serverName}{"\n"}{end}'` → every value ends with `-g<N+1>`.
- `aws s3 ls s3://<prefix>-pg-backups/<cluster>/<cluster>-g<N+1>/base/` lists a completed base backup per cluster.
- GitHub `main` == Forgejo `main`; `SCGitHubMirror*` alerts not firing.
- `kubectl -n flux-system get cm sc-recovery-mode` → NotFound.
- Observed RPO per database (heartbeat markers) ≤ 900 s, or the deviation explained in the evidence.
- Report `rto_data_availability_seconds` ≤ 14400, `rto_sr1_seconds` ≤ 14400 and `rto_sr2_seconds` ≤ 21600; the finalize PR's `config/recovery.yaml` contains `spec.rto` with the first two.

## Rollback / abort

Recovery never deletes recovery data, so aborting is always safe: stop the run (Ctrl-C; the journal stays consistent) and rerun later. To abandon a partially recovered cluster entirely: `./bootstrap destroy` on the **new** installation (plan 49) — it never touches buckets, GitHub or the bundle — then start again at step 2. Never roll back by deleting `g<N+1>` objects.

## Evidence to record

- The recovery report `s3://<prefix>-tofu-state/<cluster>/reports/recovery-<run-id>.json` (step durations, observed RPO, ref report, fencing outcome, generations) — produced automatically.
- An incident issue in Forgejo (`incident` label) with: loss time, cause, decisions taken (`accept-empty`, divergences, attestation), data lost per plan 45 §7.2 S11 column, the three RTO measurements (data availability, SR-1, SR-2) and follow-up actions.
- If any SPEC §108 target was missed, a note in the issue and a plan 45 open-question update.

## Follow-ups

- Schedule an RD2 drill within 30 days (`recovery-drill.md`) to re-attest the new bundle version.
- Review alerts that preceded the loss (black-start threats).
- Re-verify the plan 42 notification path (`notification-failure.md` §G).
