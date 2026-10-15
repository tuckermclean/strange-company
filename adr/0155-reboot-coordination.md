# ADR-0155: Reboots happen only in a maintenance window — a local timer on one node, kured on small-ha

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 08

## Context
SPEC §17 requires an update and reboot policy. Security updates install automatically (unattended-upgrades) and some need a reboot. On one node a reboot is a full outage; on three nodes it must not take more than one node down or violate PodDisruptionBudgets. k3s leaf certificates renew only when k3s restarts (plan 07), so a node that never restarts eventually breaks TLS. `unattended-upgrades` can reboot only at a daily time, not on a given weekday, and knows nothing about Kubernetes.

## Decision
- `Unattended-Upgrade::Automatic-Reboot "false"` everywhere.
- **Single node:** `sc-maintenance.timer` (Sunday 04:00 UTC by default, `node.maintenanceWindow`) reboots only if `/var/run/reboot-required` exists, after a `pre-reboot` etcd snapshot. Otherwise it restarts `k3s.service` if it has run longer than 28 days (certificate renewal; pods keep running). An inhibit file `/run/sc/maintenance-inhibit` and an active bootstrap run both skip the window.
- **Small-ha:** kured (DaemonSet in `kube-system`, enabled by the small-ha profile) reboots one node at a time inside the same window, drains with a 15-minute timeout and respects PDBs. `sc-maintenance` then performs only the monthly k3s restart, staggered by node index so no two servers restart on the same day.
- `SCNodeRebootRequiredStale` fires when a required reboot has been pending for 7 days.

## Consequences
- Predictable outage windows on one node; no unplanned reboots.
- Kernel fixes can wait up to a week; urgent ones are applied by an operator-triggered window (`systemctl start sc-maintenance`) or plan 48's OS procedure.

## Alternatives rejected
- unattended-upgrades automatic reboot: daily time only, no snapshot, no drain.
- kured on a single node: drain has nowhere to go; it only adds a controller.
- Never rebooting: unpatched kernels and expiring k3s certificates.

## Verification
F10: 08-AC-10 (lab reboot drill, inhibit behavior). H01: 47-AC-8 (kured flags). H04: drill D1 in plan 47.
