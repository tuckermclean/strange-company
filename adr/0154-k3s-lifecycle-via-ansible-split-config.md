# ADR-0154: k3s is installed, configured and upgraded by Ansible with a split cluster/node configuration; no system-upgrade-controller

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plans:** 07, 48

## Context
SPEC §16 requires critical k3s server configuration to stay consistent across control-plane nodes. The usual install path pipes `get.k3s.io` into a shell, which is not checksum-pinned. Rancher's system-upgrade-controller upgrades k3s from inside the cluster through privileged Plans, which puts node-root authority into a GitOps-reconciled workload and couples upgrades to cluster health at exactly the moment the cluster is changing.

## Decision
- The Ansible role `k3s` installs the binary from `versions/k3s.lock.yaml` with sha256 verification and its own systemd unit. Nothing is piped from curl to a shell.
- Configuration is split:
  - `/etc/rancher/k3s/config.yaml`, the **cluster file**, holds every cluster-wide key and is byte-identical on every server (rendered once from cluster variables; values that could differ, such as the ephemeral reserve, use the minimum across servers);
  - `/etc/rancher/k3s/config.yaml.d/10-node.yaml`, the **node file**, holds only node identity (`node-name`, `node-ip`, `node-external-ip`, labels) and `cluster-init` or `server:`.
- Drift is detected by the textfile metric `sc_k3s_cluster_config_info{sha256}` (alert `SCK3sServerConfigDrift`) and by `./bootstrap verify`.
- Upgrades run from the toolbox (`./bootstrap upgrade k3s --to <v>`): one minor at a time, server by server, pre-upgrade etcd snapshot, health gates between servers (plan 48). system-upgrade-controller is not installed.

## Consequences
- Node-level software stays outside GitOps; it is changed by a merged PR and then an operator-run Ansible step. This is the accepted exception to "routine management through Git": the Git change is still the authority, and `--check` reports drift.
- Upgrades need the operator's machine and SSH, but never depend on the cluster being healthy enough to run a controller.

## Alternatives rejected
- system-upgrade-controller: privileged in-cluster node mutation; harder to stop safely mid-way.
- One monolithic config file per node: identical keys would be duplicated by hand and drift silently.
- `install.sh` with `INSTALL_K3S_VERSION`: no checksum pin, network fetch of a script at install time.

## Verification
F10/P03: 07-AC-1 to 07-AC-6 (identical renders, idempotency, merge of the drop-in, rolling restarts, checksum refusal). F10 (contributed by plan 48): 48-AC-1 (lab upgrade, one server at a time, minor-skip refusal).
