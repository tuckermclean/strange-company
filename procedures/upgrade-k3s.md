# Procedure: Upgrade k3s

**Owning plan:** plans/48-upgrade.md (mechanics: plans/07-k3s.md §19, ADR-0154)
**SPEC references:** §16, §37, §113
**Severity / urgency:** routine (patch), planned (minor)
**Expected duration:** single node 15–30 min; small-ha 45–90 min
**Requires break-glass:** no

## When to use
- A merged PR changed `versions/k3s.lock.yaml` (Renovate patch PR, or a minor PR scheduled by the operator).
- `SCK3sVersionDrift` / `SCK3sUpgradeIncomplete`: a previous upgrade stopped half-way → §Resume.
- `SCK3sServerConfigDrift`: cluster files differ between servers → §Drift.

## Preconditions and required material
- The upgrade PR is merged on `main` and your checkout is at that commit (`git pull`). For a minor upgrade the PR also bumped the toolbox kubectl (plan 01 skew rule): `./bootstrap version` must show the new kubectl minor.
- Recovery passphrase.
- A maintenance window (minor upgrades pause the Runner).

## Safety checks before acting
1. Never skip a minor (the run refuses with `K3S_SKIP_MINOR_REFUSED`).
2. No expansion, node operation or other upgrade is in progress (`./bootstrap status`); no critical alert firing.
3. Backups fresh: CNPG base backups < 26 h, Longhorn backups < 26 h, etcd S3 snapshot < 13 h (the run checks).
4. Read the k3s release notes for the target version (linked in the PR) for removed APIs or flags; `make gitops-validate` already checked rendered manifests against the new API version (plan 18).
5. Blast radius: each server's k3s restarts once (single node: API down 1–3 min, workloads keep running; small-ha: drained one at a time).

## Steps
1. `./bootstrap upgrade k3s --to <version>`. Expected: preflight passes and prints the server order (node-01 last at small-ha is **not** required; the order is node-01, node-02, node-03).
2. The run takes `pre-k3s-upgrade` etcd snapshot and confirms the S3 copy (≤ 120 s). Failure → fix object storage (`object-storage-outage.md`) and rerun.
3. Minor upgrades: the run pauses Runner dispatch and waits for running workers to finish (cancel them if needed).
4. Per server (automatic, one at a time):
   1. small-ha: `kubectl cordon` + `kubectl drain --ignore-daemonsets --delete-emptydir-data --timeout=15m`. A PDB blocking the drain stops the run; resolve the unhealthy workload and rerun (resumes at this server).
   2. Install the new binary (sha256-verified), re-render the CoreDNS manifest if the bundled CoreDNS version changed, restart k3s.
   3. Wait ≤ 300 s for the node Ready with the new `kubeletVersion`; `sc-etcdctl endpoint health --cluster` all healthy; `/readyz` ok.
   4. Uncordon; soak 5 min watching for new critical alerts.
   Expected after each server: the journal step `upgrade-k3s.server.<node>` succeeded.
5. Post-checks (automatic): all nodes on the new version; `flux get all -A` Ready; `./bootstrap verify --only V-10,V-12,V-13,V-22`; dispatch resumed.

### §Resume
Rerun `./bootstrap upgrade k3s --to <version>`; servers already at the target (binary sha256 and `kubeletVersion` match) are skipped.

### §Drift
1. `./bootstrap exec -- ssh <node> 'sudo sha256sum /etc/rancher/k3s/config.yaml'` on every server; identify the odd one.
2. `./bootstrap configure --tags k3s` re-renders and rolls servers one at a time (plan 07 §14.2). Do not edit the file by hand.

### §Rollback
- **Patch upgrade misbehaves after completion:** revert the PR, merge, then run this procedure with `--to <previous patch>` (patch downgrades within a minor are accepted by the run with `--allow-patch-downgrade`).
- **Minor upgrade, a server fails to come back:** stop. small-ha: the other two servers hold quorum; inspect logs, fix forward if possible; otherwise remove the failed server (`remove-node.md`, it is not healthy so use `lost-etcd-member.md` §B) and re-add it later at the new version. Single node: if the API is broken, `restore-etcd.md` §A using the `pre-k3s-upgrade` snapshot **and the previous k3s binary** (reinstall it with `./bootstrap upgrade k3s --to <previous> --force-binary-only`, then reset). A minor downgrade with existing data is never attempted.

## Verification
- `./bootstrap exec -- kubectl get nodes -o wide` → every node `VERSION` = target.
- `./bootstrap exec -- kubectl version -o json | jq -r .serverVersion.gitVersion` = target.
- `SCK3sVersionDrift`, `SCK3sUpgradeIncomplete` not firing; `./bootstrap verify` passes.
- Lab-only re-verifications for minors (plan 48 §14.1) recorded in the PR.

## Rollback / abort
See §Rollback. Aborting between servers (Ctrl-C) is safe: the journal records which servers are done; the cluster tolerates one minor of skew between servers temporarily (Kubernetes version-skew policy), but finish within 24 h.

## Evidence to record
Run report (`./bootstrap report --json`), snapshot name, per-server timings, any PDB blocks. Attach to the upgrade PR as a comment.

## Follow-ups
- Re-run `./bootstrap verify --only V-30` within a week (kubelet changes can shift memory).
- Record new third-party facts (e.g. `k3s.etcdLearnerJoin`) in `versions/compat.yaml` if the lab checks changed.
