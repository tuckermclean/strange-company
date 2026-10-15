# Procedure: Infrastructure provider outage

**Owning plan:** plans/03-provider-interface.md (provider specifics: 04 OVH, 05 existing; rebuild elsewhere: 45; node replacement: 47)
**SPEC references:** §13, §14, §108, §111, §113 ("provider outage")
**Severity / urgency:** degraded (provider API only) → outage (nodes unreachable) → disaster (region lost)
**Expected duration:** minutes (API outage, wait) to ≤ 4 h (rebuild in another region from backups, SPEC §108 RTO for databases)
**Requires break-glass:** `SC-BREAK-GLASS-INFRA` (provider console account) for console/rescue access; `SC-BREAK-GLASS-CLUSTER` if the API is reachable only through a non-standard path

## When to use
- sc-bootstrap fails with provider API errors (HTTP 5xx, timeouts, `OVH_*` transient errors) during `plan`, `provision`, `expand`, `destroy` or `recover`.
- Alerts `SCNodeDown`, `SCEtcdNoLeader`, the external heartbeat stops (total outage, ADR-0009), or the OVH status page reports an incident in your region.
- A delivered or existing node is unusable (`SSH_KEY_NOT_INSTALLED`, reinstalled or resized outside Strange Company) — [unusable node](#unusable-node).
- The post-apply audit found resources nobody planned — [unexpected resources](#unexpected-resources).

## Preconditions and required material
- Repository clone, `bootstrap.env`, passphrase, bundle (all off-cluster).
- Provider console credentials (`SC-BREAK-GLASS-INFRA`), provider status page.
- For rebuild elsewhere: the material listed in `total-cluster-loss.md` (plan 45).

## Safety checks before acting
1. Distinguish **API outage** (cluster healthy, only provisioning blocked) from **node outage** (cluster degraded or down). Check `./bootstrap exec -- kubectl get nodes` and the external heartbeat before anything else.
2. Never order replacement compute while the provider API is flapping: orders may be accepted but reported as failed (duplicates cost money). Wait until `OrphanScan` (part of every `./bootstrap plan`) works again.
3. Never run `./bootstrap destroy` during a provider incident to "clean up": termination calls may also fail half-way and the cluster may come back.
4. Rebuilding in another region is a **disaster-recovery decision** with data-loss implications (backups' RPO); it requires fencing the old installation (ADR-0554) if it may come back.

## Steps

1. **Classify.**
   - `./bootstrap exec -- kubectl get nodes` succeeds and all nodes `Ready` → **API-only outage**: go to step 2.
   - Some nodes `NotReady` / unreachable (small-ha) → **partial node outage**: go to step 3.
   - All nodes unreachable / heartbeat silent → **full outage**: go to step 4.

2. **API-only outage.** Nothing is broken in the running platform. Postpone `plan`/`provision`/`expand`/`destroy`. If a run failed mid-way, it is safe: rerun after the provider recovers (`bootstrap-resume.md`; apply is never retried blindly, orphan scan protects against duplicates). Expected: provider status page shows resolution; `./bootstrap plan` exits 0 with an empty orphan scan. Done.

3. **Partial node outage (small-ha).** The cluster keeps running on quorum (2 of 3). Follow `lost-node.md` (plan 47) to wait or replace. Do not remove the etcd member of a node that may return within the provider's announced recovery time. Replacement nodes always get a new name (`node-04`, plan 03 §8).

4. **Full outage — decide: wait or rebuild elsewhere.**
   1. Check the provider status page for the region and an ETA. Check in the provider console (`SC-BREAK-GLASS-INFRA`) whether the VPS shows `running`, `stopped`, or `rescue`; try the KVM/serial console.
   2. If the ETA is ≤ 2 h, or the provider confirms disks are intact: **wait**. When nodes return, run `./bootstrap verify` (`bootstrap-verify.md`). Expected: all checks pass after reconciliation; k3s and CNPG recover from local disks.
   3. If the region is lost or the ETA exceeds your tolerance (unreachable old nodes are fenced out of managed state with the typed `fence-nodes <cluster> <count>` during `recover`; plan 03 §20.1) (SPEC §108: platform databases RTO ≤ 4 h): **rebuild elsewhere** with `total-cluster-loss.md` (plan 45): `./bootstrap recover` with a different `PROVIDER_REGION` (allowed; region is not an immutable field) or `PROVIDER=existing` on other hardware. Recovery restores from object storage and GitHub; data written after the last backup/WAL archive is lost (the destroy-style data-loss window is printed by `recover`).
   4. When the old region returns after a rebuild, the old VPSes still bill and may run stale services. Do **not** let them start workloads: in the provider console stop them, then remove them with the old installation's state as described in `total-cluster-loss.md` (fenced installation cleanup). The new installation's `OrphanScan` will flag them only if names collide (they will not — recovery uses new node numbers).

<a id="unusable-node"></a>
### Unusable node (delivered without key, reinstalled, resized outside Strange Company)
1. Symptoms: `SSH_KEY_NOT_INSTALLED` at provision, `HOST_KEY_MISMATCH` later, or a plan showing replace of the node.
2. If the node was **just delivered** and SSH with the key fails: in the provider console, reinstall the OS (Debian 13) with your SSH public key (OVH: VPS → Reinstall, select key), wait, then rerun `./bootstrap` (provision re-captures the host key only if the node has no pinned key yet; if it was pinned, remove that node's entry from `.bootstrap/known_hosts` **only after** confirming in the console that you reinstalled it).
3. If a node of a **running** cluster was reinstalled or resized outside Strange Company, treat it as lost: `lost-node.md` (plan 47) replaces it with a new node; then destroy the altered machine through the normal remove-node path.
4. Never change `MACHINE_CLASS` to make the plan match a console resize; the guard forbids replace by design.

<a id="unexpected-resources"></a>
### Unexpected resources after apply (`POST_APPLY_AUDIT_FAILED`)
1. Read `plan-summary.json` (`audit.unexpected[]`): type, address/provider id, created time.
2. Check in the provider console whether the resource is billable and whether it was created by this run (creation time within the run window; name `sc-<cluster>-…`).
3. If it is a duplicate node of this installation: import it (`./bootstrap state import --module compute`) so state is truthful, then remove the surplus node with `remove-node.md` (plan 47), which uses the guarded destroy path. If it is not Strange Company's, leave it and report a bug: the guard and audit worked, but provider code created something outside the plan.
4. Record the resource and cost impact in the evidence.

## Verification
- API outage: `./bootstrap plan` exits 0, orphan scan empty, `plan-summary.json` 0 changes.
- Node outage: `./bootstrap verify` exits 0; `kubectl get nodes` all `Ready`; etcd member count equals `kubernetes.servers`.
- Rebuild elsewhere: `total-cluster-loss.md` verification (`CONTROL TRANSFER COMPLETE (RECOVERY)` report) and the old region's machines stopped or terminated.

## Rollback / abort
Waiting is always reversible. A started rebuild in another region can be abandoned before its `seed` phase with `./bootstrap destroy` on the new installation (its buckets generation is retained); after seeding, finishing the rebuild is safer than abandoning it.

## Evidence to record
Incident timeline (detection, provider ETA, decision, recovery), provider incident reference, whether data loss occurred (last backup/WAL timestamps versus outage start), any unexpected resources and their cost.

## Follow-ups
- After a rebuild elsewhere: update `bootstrap.env` (`PROVIDER_REGION`), confirm the old region's resources are terminated, and run a full `./bootstrap verify` and backup restore tests (plan 44).
- Review whether small-ha (three nodes) would have avoided the outage (`expand-to-small-ha.md`, plan 47); note that three VPSes in one region share the region failure domain.
