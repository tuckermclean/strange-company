# 08 — Node baseline and security

**Status:** Stage-0 plan (draft)
**DAG nodes:** F10 (co-owned with 07), P03 (co-owned with 07, 09)
**Related plans:** [03](03-provider-interface.md) (normalized nodes), [05](05-existing-nodes.md) (lab VMs, `existing` preflight), [07](07-k3s.md) (k3s config, kubelet flags), [09](09-networking.md) (CNI, ingress modes), [11](11-storage.md) (disk budget), [13](13-longhorn.md) (Longhorn prerequisites), [35](35-secrets.md), [37](37-admission-policy.md), [38](38-prometheus.md) (node-exporter), [42](42-alerting-notifications.md), [47](47-ha-expansion.md), [48](48-upgrade.md) (OS upgrades), [50](50-bootstrap-orchestrator.md) (configure phase, inventory), [51](51-capacity-and-scheduling.md) (reservations, eviction)
**Contracts:** consumes `provider.schema.yaml` (node facts), `bootstrap-config.schema.yaml` (`ssh.allowedCidrs`, `ingress.mode`); produces no new contract
**ADRs:** relies on ADR-0010, ADR-0011, ADR-0020, ADR-0154; introduces ADR-0155 (reboot coordination) and ADR-0157 (supported OS and the `inet sc_host` nftables table)

## 1. Purpose

Define the host-level baseline every Strange Company node must satisfy before k3s starts and keep satisfying afterwards. That covers supported distributions, package update and reboot policy, time synchronization, SSH, the host firewall, swap, sysctls and kernel modules, filesystem layout, container-runtime assumptions, journald and log rotation, disk-pressure behavior, Longhorn host prerequisites and practical CIS hardening. It also defines the Ansible role structure that applies the baseline idempotently, and the node-local facts exporter (`sc-node-facts`) that makes the baseline observable. SPEC §17 lists these items; SPEC §18 requires that nodes expose only required services.

## 2. Requirements derived from SPEC.md

1. Stage 0 defines supported Linux distributions, update policy, reboot policy, time synchronization, SSH configuration, firewall policy, swap behavior, sysctls, filesystem requirements, container-runtime assumptions, log rotation and disk-pressure behavior. [SPEC §17]
2. Nodes expose only required services. [SPEC §17]
3. Infrastructure control interfaces are not exposed publicly without justification. [SPEC §18]
4. Network-policy enforcement and encrypted inter-node pod traffic must work on the baseline (kernel modules, sysctls). [SPEC §18; ADR-0011]
5. Infrastructure has scheduling priority over agents; a worker must not OOM-kill etcd. [SPEC §89]
6. Root disks must not fill because cleanup policy was omitted (journald, logs, images). [SPEC §107]
7. The operator does not install tools locally; everything runs from the toolbox. [SPEC §7, §9]
8. The one-node system is reduced redundancy, not a separate architecture: the same baseline applies to 1 and 3 nodes. [SPEC §12]
9. Disk-pressure and node-failure conditions are alertable. [SPEC §96]

Derived requirements:

- **08-R-1** Supported OS: Debian 13 (trixie) and Ubuntu 24.04 LTS, amd64 only, kernel ≥ 6.1, cgroup v2 unified hierarchy, AppArmor enabled. Anything else fails configure preflight with `NODE_OS_UNSUPPORTED` (ADR-0157).
- **08-R-2** The host firewall is one nftables table `inet sc_host` owned by Strange Company, loaded by its own unit `sc-firewall.service`. It never flushes the global ruleset, so k3s/kube-router chains are untouched. Input policy is drop. The forward hook is not filtered by this table.
- **08-R-3** SSH is key-only for the single admin user `sc-admin`. Root login and password authentication are disabled.
- **08-R-4** Swap is off and stays off across reboots.
- **08-R-5** Security updates install automatically. Reboots happen only inside the maintenance window: by `sc-maintenance.timer` on one node, by kured one node at a time on small-ha (ADR-0155).
- **08-R-6** journald and every file under `/var/log` have hard size caps.
- **08-R-7** Every baseline item has a deterministic check in `verify.yml` and a metric or alert when it can drift at runtime.
- **08-R-8** Re-running the baseline with unchanged inputs reports `changed=0`.

## 3. Inputs

| Input | Source | Used for |
|---|---|---|
| Node list, addresses (`internal_ip`, `external_ip`, `public_ipv4`, `public_ipv6`), hardware (vCPU, MemTotal, disk size/class) | plan 03 normalized output, rendered by plan 50 into inventory host vars `sc_node_name`, `sc_internal_ip`, `sc_external_ip` | firewall peer sets, preflight sizing |
| Initial SSH user and port | `bootstrap-config.yaml` `provider.existing.nodes[].ssh` or provider defaults (plan 04) | first connection only (§14.1) |
| Operator public key | `bootstrap-config.yaml` `ssh.publicKeyPath` (`SC-SSH-OPERATOR`) | `sc-admin` authorized_keys |
| SSH source CIDRs | `bootstrap-config.yaml` `ssh.allowedCidrs` (empty = any) | firewall set `sc_ssh_allowed_v4` |
| Ingress mode | `bootstrap-config.yaml` `ingress.mode` (`tunnel` / `direct`) | documentation of expected external scan; no host rule changes (hostPort DNAT, §9.4) |
| Maintenance window | `infra/ansible/group_vars/all/node.yaml` `node_maintenance_window` (default `Sun 04:00` UTC) | `sc-maintenance.timer`, kured window |
| Pod/service CIDRs | `bootstrap-config.yaml` `network.*` (plan 07 assumption; defaults `10.42.0.0/16` / `10.43.0.0/16`) | firewall pod-CIDR accept |
| Profile (node count) | `bootstrap-config.yaml` `cluster.profile` | reboot mechanism selection |

## 4. Outputs

- A node that passes `verify.yml` (§14.3) with these artifacts:
  - user `sc-admin` (uid 1500, group `sc-admin`, member of `sudo`), sudoers drop-in `/etc/sudoers.d/90-sc-admin` (`NOPASSWD:ALL`, validated with `visudo -cf`);
  - `/etc/ssh/sshd_config.d/10-sc.conf`;
  - `/etc/sc/firewall/sc-host.nft` and `sc-firewall.service`;
  - `/etc/chrony/conf.d/10-sc.conf` (Debian/Ubuntu `chrony` include dir; verify on both distros);
  - `/etc/sysctl.d/90-sc.conf`, `/etc/modules-load.d/sc.conf`, `/etc/modprobe.d/90-sc-blacklist.conf`;
  - `/etc/systemd/journald.conf.d/10-sc.conf`, `/etc/logrotate.d/sc-default-caps`;
  - `/etc/apt/apt.conf.d/52sc-unattended-upgrades`, `/etc/apt/apt.conf.d/20auto-upgrades`;
  - `/etc/multipath.conf` (Longhorn blacklist);
  - directories `/var/lib/rancher/k3s`, `/var/lib/longhorn`, `/var/lib/strange-company/local-path`, `/var/lib/node_exporter/textfile` (0755 root);
  - `sc-node-facts.service` + `.timer` (5 min) and `sc-maintenance.service` + `.timer`.
- Textfile metrics in `/var/lib/node_exporter/textfile/sc_node.prom` (§21), alongside plan 07's `sc_k3s.prom`.
- Cluster break-glass node account (SEC-9; plan 15 §14.4.1, ADR-0251): user `sc-breakglass` (uid 1501, group `sc-breakglass`), key-only, authorized key from `infra/ansible/files/breakglass.pub` (public half of `SC-BREAK-GLASS-CLUSTER`), sudoers drop-in `/etc/sudoers.d/91-sc-breakglass` (`NOPASSWD:ALL`, `visudo -cf` validated), a PAM hook `session optional pam_exec.so quiet /usr/local/libexec/sc/breakglass-login` in `/etc/pam.d/sshd` that acts only when `PAM_USER=sc-breakglass` and atomically writes `/var/lib/node_exporter/textfile/sc_breakglass.prom` (`sc_breakglass_ssh_last_login_timestamp_seconds{account="sc-breakglass"}`, `sc_breakglass_ssh_key_present{account="sc-breakglass"}`), timer `sc-breakglass-keycheck.timer` (15 min) refreshing the key-present line, and auditd watches `-w /home/sc-breakglass/.ssh -p wa -k sc-breakglass` plus `-a always,exit -F arch=b64 -S execve -F auid=1501 -k sc-breakglass-exec`.
- The managed `/etc/hosts` block (CD-21) mapping `git.<baseDomain>` to the pinned Traefik ClusterIP `10.43.0.20` (plan 09 §9.3), between `# BEGIN strange-company managed` / `# END strange-company managed` markers, so kubelet/containerd image pulls from the Forgejo registry never leave the cluster.
- The node ownership marker `/etc/strange-company/install-uid` (0644 root, one line: the installation's `install_uid` from the plan 50 journal). Plan 05's preflight reads it to accept nodes this installation already configured and to refuse nodes of another installation (`NODE_BELONGS_TO_OTHER_INSTALL`). The `baseline` role writes it first (before any other change) and only if it is absent or already equal; a different value fails the run with `NODE_BELONGS_TO_OTHER_INSTALL`. It is removed only by decommission: `playbooks/node-remove.yml` (plan 47, last step after the node has left the cluster) and plan 49's destroy of `existing` nodes.
- The Ansible facts file `/etc/sc/node-baseline.json` (`{"baseline_version": "<git tree hash of roles/baseline>", "applied_at": "<RFC3339>", "os": "<id>-<version>"}`), read by `sc-node-facts`.

## 5. Dependencies

**Hard (DAG):** F10 needs F03 (toolbox image with Ansible, `ansible-lint`). P03 needs P02 (provisioned nodes and normalized output) and F10.

**Soft / runtime:**
- Plan 50 owns the configure phase, the inventory and the CLI. **Assumed interfaces (plan 50):**
  - inventory host vars as listed in §3, plus `ansible_user`/`ansible_port`;
  - the admin-user transition is recorded **in Git**, not in the journal (BOOT-8): configure's transition commit sets the node's `ssh.admin_user: sc-admin` in `gitops/clusters/<c>/inventory.yaml` and the `ProviderOutput` node record (plans 03/05 add the nullable field; provision writes `null`). Every SSH client (Ansible inventory, `./bootstrap shell`/`exec` tunnel, PF-07, discovery, decommission) uses `admin_user ?? user`, and as a deterministic fallback tries `sc-admin` first, then the initial user (plans 02/05/50);
  - `bootstrap-config.yaml` gains `network.podCidr`, `network.serviceCidr` (plan 07 assumption) and `node.maintenanceWindow`, `node.sshIpv6` (this plan). Until plan 50 adds them, the defaults in §9 apply.
- Plan 07 runs after this plan in the same `site.yml` (tag order `baseline` → `firewall` → `k3s` → `network`) and relies on the sysctls required by `protect-kernel-defaults`.
- Plan 38 deploys node-exporter (namespace `monitoring-node`, ADR-0550) with the textfile collector reading `/var/lib/node_exporter/textfile`.
- Plan 37 must deny `Service` types `NodePort` and `LoadBalancer` and restrict `hostPort` (§13): DNAT'd traffic bypasses this plan's input chain.
- Plan 13 owns Longhorn configuration; this plan only provides host prerequisites (§9.7).

## 6. Owned state

| State | Location | System of record? | Writers |
|---|---|---|---|
| Baseline configuration files listed in §4 | node filesystem | No: Git (`infra/ansible/roles/{baseline,firewall}`) + inventory are | Ansible only |
| `/etc/sc/node-baseline.json` | node | No (derived) | Ansible |
| `/etc/strange-company/install-uid` | node | No (copy of the journal's `install_uid`, plan 50) | `baseline` role (write), `node-remove.yml` / plan 49 (remove) |
| Firewall runtime ruleset (`nft list table inet sc_host`) | kernel | No (derived from the rendered file) | `sc-firewall.service` |
| Textfile metrics `sc_node.prom` | node | No (derived) | `sc-node-facts` |

Hand edits to any of these are drift. `./bootstrap configure --check` (plan 50) reports them as `changed>0`; `SCHostFirewallNotLoaded` reports firewall drift at runtime.

## 7. External interfaces

| Interface | Consumer | Contract |
|---|---|---|
| SSH `sc-admin@<node>:22` (key-only) | plan 50 (Ansible, kubeconfig forward `127.0.0.1:16443 → <internal_ip>:6443`), operators via `./bootstrap exec -- ssh <node>` | §9.3 |
| Port exposure per §9.4 matrix | external scan in V-11 (plan 50), 07-AC-14, plan 09 direct mode | §9.4 |
| Textfile metrics (§21) | node-exporter → Prometheus (plan 38) → alerts (plan 42) | metric names in §21 |
| Ansible tags `baseline`, `firewall`, `os-upgrade` | plan 50 configure/expand, plan 48 | §8 |
| Reboot sentinel `/var/run/reboot-required` | kured (small-ha), `sc-maintenance` | Debian/Ubuntu convention (`update-notifier-common` on Ubuntu; `unattended-upgrades` writes it on Debian, verify on Debian 13) |
| Maintenance inhibit file `/run/sc/maintenance-inhibit` | operators, plan 48 procedures | while present, `sc-maintenance` skips reboots and restarts |

## 8. Internal interfaces

### 8.1 Ansible layout

```text
infra/ansible/
├── site.yml                 # plays in order: preflight → baseline → firewall → k3s (07) → network (09) → verify
├── playbooks/
│   ├── expand.yml           # plan 47: baseline+firewall on all, then k3s join
│   ├── node-remove.yml      # plan 47: drain/remove, then firewall peer update on survivors
│   ├── k3s-upgrade.yml      # plan 48 / 07
│   └── os-upgrade.yml       # plan 48: serial: 1, full dist-upgrade + reboot
├── decommission.yml         # plan 49: k3s uninstall (07) + node revert (this plan, §8.2)
├── group_vars/all/
│   ├── node.yaml            # this plan (§9.1)
│   ├── firewall.yaml        # this plan (§9.4)
│   ├── kubelet.yaml         # plan 51 numbers, plan 07 renders
│   ├── k3s.yaml             # plan 07
│   └── network.yaml         # plan 09
└── roles/
    ├── sc_preflight/        # os.yml, hardware.yml, disk.yml (fio), runtime.yml, network.yml
    ├── baseline/            # packages.yml, users.yml, ssh.yml, time.yml, swap.yml, sysctl.yml, modules.yml,
    │                        # journald.yml, logrotate.yml, updates.yml, storage_prereqs.yml, hardening.yml,
    │                        # dirs.yml, node_facts.yml, maintenance.yml, verify.yml
    ├── firewall/            # render.yml, load.yml, verify.yml
    ├── k3s/                 # plan 07
    └── network/             # plan 09
```

Role rules (enforced by `ansible-lint` profile `production` plus custom rules in `infra/ansible/.ansible-lint-rules/`):
- No `command`/`shell` task without `changed_when` and either `creates`, `removes` or an explicit read-only marker (`check_mode: false`, `changed_when: false`).
- Every templated file uses `validate:` where a validator exists (`sshd -t -f %s`, `visudo -cf %s`, `nft -c -f %s`, `chronyd -p -f %s`, `multipath -t` is not a validator, so the file is checked by `verify.yml`).
- Handlers restart services only when their own files changed. `sshd` reload uses `reload`, never `restart`, and runs after a syntax check.
- Every role supports `--check --diff`.
- Facts are gathered once per play (`gather_subset: [min, hardware, mounts]`).

### 8.2 Decommission (`infra/ansible/decommission.yml`, plans 49/47; BOOT-26)

Reverts what this plan installed, after plan 07's k3s uninstall, while keeping the machine reachable:
- **kept:** user `sc-admin` and its `authorized_keys` (so `ssh sc-admin@<host>` keeps working after destroy), chrony, journald and logrotate caps, sysctls, unattended-upgrades;
- **removed:** the `AllowGroups` line of `/etc/ssh/sshd_config.d/10-sc.conf` (the provider's initial user can log in again; the rest of the drop-in stays), user `sc-breakglass` with its sudoers drop-in and PAM hook, `sc-maintenance`, `sc-node-facts`, `sc-etcd-snapshot-prune` and `sc-breakglass-keycheck` units, the managed `/etc/hosts` block, the `ci-workspace` mount and image, textfile metrics, and `/etc/strange-company/install-uid` (last);
- **firewall:** `inet sc_host` is re-rendered as an SSH-only baseline (loopback, established, ICMP, `tcp dport 22` from `ssh.allowedCidrs`) and stays loaded, so the node is not left wide open;
- sshd is reloaded only after a fresh `sc-admin` login succeeded (same safety pattern as §14.1 step 4).

### 8.3 Node facts exporter

`sc-node-facts.service` (oneshot, `Nice=10`, `IOSchedulingClass=idle`) runs every 5 minutes via `sc-node-facts.timer` (`OnBootSec=2min`, `OnUnitActiveSec=5min`, `AccuracySec=30s`). It writes `sc_node.prom` atomically (temp file in the same directory + `rename`). Plan 07 adds its k3s collectors to the same unit as a second output file `sc_k3s.prom`. The exporter is a small script delivered by the `baseline` role (implementation detail, not specified here) that only reads local state; it never contacts the network.

## 9. Configuration

### 9.1 Knobs (`infra/ansible/group_vars/all/node.yaml` unless noted)

| Knob | Default | Who may change |
|---|---|---|
| `node_admin_user` | `sc-admin` | fixed (renaming is a migration) |
| `node_maintenance_window` | read from `cluster-vars.SC_MAINTENANCE_WINDOW` (default `Sun 04:00-06:00 UTC`); also rendered into the `strange-company` chart value `runner.maintenanceWindow` (GC-20) | PR (one value for nodes and Runner) |
| `node_k3s_restart_max_age_days` | `28` | PR |
| `node_ntp_servers` | `[]` (empty = distro pools: `2.debian.pool.ntp.org` / `ntp.ubuntu.com` + `pool.ntp.org`) | PR; providers MAY set their local NTP (plan 04) |
| `node_ssh_port` | `22` | install time only |
| `node_ssh_ipv6` | `false` | PR + configure |
| `node_journald_max_use` | `1G` | PR |
| `node_etcd_ondemand_retention_days` / `node_etcd_ondemand_max` | `30` / `10` | PR |
| `node_ci_workspace_size` | `12G` (self-hosting-minimum, both size classes) / `24G` (small-ha) | PR (grow only) |
| `node_fio_p99_warn_ms` / `node_fio_p99_fail_ms` | `10` / `25` | PR |
| `firewall_ssh_rate_per_minute` | `10` (`firewall.yaml`) | PR |
| `ssh.allowedCidrs` | `bootstrap-config.yaml`, empty = any | operator (`SSH_ALLOWED_CIDRS`) + configure |

### 9.2 Supported operating systems (ADR-0157)

| Distro | Versions | Kernel | Notes |
|---|---|---|---|
| Debian | 13 (trixie) | ≥ 6.12 (stock) | nftables default; `chrony` from main |
| Ubuntu | 24.04 LTS (noble) | ≥ 6.8 (GA or HWE) | `ufw` present but must be inactive (§14.1); snapd tolerated, not used |

Selection rule for additions: a new distro release (e.g. Ubuntu 26.04 LTS) is added only after (a) the pinned k3s and Longhorn versions list it as supported or tested, (b) `make e2e-lab` passes on it, and (c) a PR adds it to `sc_preflight/vars/supported.yaml`. Removal follows the distro's end of standard support minus 90 days, and the preflight starts warning (`NODE_OS_EOL_SOON`) 180 days before.

### 9.3 SSH (`/etc/ssh/sshd_config.d/10-sc.conf`)

```text
Port 22
PermitRootLogin no
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
AuthenticationMethods publickey
AllowGroups sc-admin sc-breakglass
MaxAuthTries 3
LoginGraceTime 30
MaxStartups 10:30:60
ClientAliveInterval 60
ClientAliveCountMax 4
AllowTcpForwarding local          # needed for the API forward (plan 50); remote forwarding denied
AllowAgentForwarding no
X11Forwarding no
PermitTunnel no
PermitUserEnvironment no
```

The drop-in relies on the stock `Include /etc/ssh/sshd_config.d/*.conf` at the top of `sshd_config`, so its values win (first match wins in sshd; verify on both distros, 08-AC-5). The provider's initial user (for example `debian` or `ubuntu`) keeps its account but is not in group `sc-admin`, so `AllowGroups` locks it out after the transition in §14.1. Host keys are left as provisioned so plan 03's recorded fingerprints stay valid.

### 9.4 Host firewall (ADR-0157)

**Unit.** `sc-firewall.service`: `Type=oneshot`, `RemainAfterExit=yes`, `DefaultDependencies=no`, `Before=network-pre.target k3s.service`, `Wants=network-pre.target`, `ExecStart=/usr/sbin/nft -f /etc/sc/firewall/sc-host.nft`, `ExecReload=` same, `ExecStop=/usr/sbin/nft delete table inet sc_host`. The distro `nftables.service` is disabled and masked: its default `/etc/nftables.conf` starts with `flush ruleset`, which would wipe kube-router and kube-proxy rules. `ufw` and `firewalld` must be absent or inactive (preflight `NODE_FOREIGN_FIREWALL` otherwise).

**Ruleset** (rendered from `roles/firewall/templates/sc-host.nft.j2`; the file starts with `table inet sc_host {}` + `delete table inet sc_host` so loading is atomic and idempotent):

```text
table inet sc_host {
  set sc_peers_v4        { type ipv4_addr; elements = { <every node internal_ip and external_ip> } }
  set sc_servers_v4      { type ipv4_addr; elements = { <every server internal_ip and external_ip> } }
  set sc_ssh_allowed_v4  { type ipv4_addr; flags interval; elements = { <ssh.allowedCidrs v4 or 0.0.0.0/0> } }
  set sc_ssh_allowed_v6  { type ipv6_addr; flags interval; elements = { <v6 CIDRs, or ::/0 when node_ssh_ipv6 and no list> } }

  chain input {
    type filter hook input priority filter + 10; policy drop;
    iif "lo" accept
    ct state established,related accept
    ct state invalid drop
    iifname { "cni0", "flannel-wg" } ip saddr 10.42.0.0/16 tcp dport { 6443, 10250, 9100, 2381 } accept
                                                            # pods → host: API (ClusterIP DNAT to the local node), kubelet,
                                                            # node-exporter, etcd metrics; nothing else (SEC-5, SEC-18)
    icmp type echo-request limit rate 10/second accept
    icmp type { destination-unreachable, time-exceeded, parameter-problem } accept
    icmpv6 type { nd-neighbor-solicit, nd-neighbor-advert, nd-router-advert, nd-router-solicit,
                  packet-too-big, time-exceeded, parameter-problem, destination-unreachable } accept
    icmpv6 type echo-request limit rate 10/second accept
    tcp dport 22 ip saddr @sc_ssh_allowed_v4 ct state new
        meter sc_ssh_v4 { ip saddr limit rate over 10/minute burst 5 packets } counter name sc_ssh_ratelimited drop
    tcp dport 22 ip saddr @sc_ssh_allowed_v4 accept
    tcp dport 22 ip6 saddr @sc_ssh_allowed_v6 accept          # set empty unless node_ssh_ipv6
    ip saddr @sc_peers_v4 tcp dport { 6443, 10250, 9100 } accept
    ip saddr @sc_servers_v4 tcp dport { 2379, 2380, 2381 } accept
    ip saddr @sc_peers_v4 udp dport 51820 accept
    counter name sc_input_dropped
  }
}
```

Notes:
- Priority `filter + 10` runs after the iptables-nft `filter` INPUT chain (priority 0). A packet must be accepted by every base chain on the hook, so this table only narrows exposure; it cannot open something kube-router drops.
- The `forward` hook is **not** filtered here. Pod traffic and hostPort DNAT traffic (direct mode) traverse forward and are governed by NetworkPolicy (plan 36) and admission (plan 37).
- Pod-originated traffic to the host (arriving on `cni0` or `flannel-wg` with a pod-CIDR source) is accepted **only** on 6443, 10250, 9100 and 2381 (SEC-5, GC-17). SSH 22 and etcd 2379/2380 are never reachable from pods, whatever NetworkPolicy says; this is the second independent control behind plan 36's NetworkPolicy. A spoofed pod-CIDR source on the uplink matches no rule (SEC-18). Pod traffic to *another* node's IP is masqueraded by flannel to the sending node's IP and is governed by the peer rules below.
- IPv6 inbound is dropped except essential ICMPv6 and, when `node_ssh_ipv6: true`, SSH. The cluster is IPv4 single-stack (plan 09).
- Output is not filtered on the host (apt, etcd-s3, image pulls). Pod egress is controlled by NetworkPolicy.

**Exposure matrix** (what an external scanner outside the peer set must observe; "pods" = reachable from the pod CIDR only):

| Port / proto | Service | 1 node, tunnel | 1 node, direct | 3 nodes, tunnel | 3 nodes, direct |
|---|---|---|---|---|---|
| 22/tcp | SSH (`ssh.allowedCidrs`, 10 new/min/src) | open | open | open (each node) | open (each node) |
| 80/tcp | HTTP via hostPort → Traefik 8000 | closed | open | closed | open (each node) |
| 443/tcp | HTTPS via hostPort → Traefik 8443 | closed | open | closed | open (each node) |
| 2222/tcp | Forgejo SSH via Traefik TCP entrypoint (plan 09, opt-in) | closed | open only if `forgejo.ssh.public` | closed | open only if opted in |
| 6443/tcp | Kubernetes API | pods†, lo | pods†, lo | peers, pods† | peers, pods† |
| 10250/tcp | kubelet | pods† | pods† | peers, pods† | peers, pods† |
| 2379–2380/tcp | etcd client/peer | lo (never pods) | lo (never pods) | peer servers | peer servers |
| 2381/tcp | etcd metrics (HTTP) | pods† | pods† | peer servers, pods† | peer servers, pods† |
| 9100/tcp | node-exporter (hostNetwork) | pods† | pods† | peers, pods† | peers, pods† |
| 51820/udp | flannel WireGuard | (self only) | (self only) | peers | peers |
| everything else | — | closed | closed | closed | closed |

"Closed" means the scanner gets no SYN-ACK (dropped), except where k3s/kube-router itself rejects first. † "pods" means: allowed by this table to any pod, and narrowed further by NetworkPolicy (plan 36 API-1, MO-1) to the pods that need it. SSH (22) is never reachable from pods.

### 9.5 Swap, sysctls and kernel modules

- **Swap:** `swapoff -a`; swap entries removed from `/etc/fstab`; `systemctl mask swap.target`; any `zram-*`/`dphys-swapfile` units masked. Verify: `swapon --show` empty, `/proc/meminfo` `SwapTotal: 0 kB`.
- **Modules** (`/etc/modules-load.d/sc.conf`, loaded immediately with `modprobe`): `overlay`, `br_netfilter`, `nf_conntrack`, `wireguard`, `iscsi_tcp`, `dm_crypt` (Longhorn encrypted volumes, optional). Blacklisted (`/etc/modprobe.d/90-sc-blacklist.conf`, `install <m> /bin/false`): `cramfs`, `freevxfs`, `hfs`, `hfsplus`, `jffs2`, `udf`, `dccp`, `sctp`, `rds`, `tipc`.
- **Sysctls** (`/etc/sysctl.d/90-sc.conf`):

| Key | Value | Why |
|---|---|---|
| `vm.panic_on_oom` | 0 | required by k3s `protect-kernel-defaults` |
| `vm.overcommit_memory` | 1 | required by `protect-kernel-defaults` |
| `kernel.panic` | 10 | required by `protect-kernel-defaults` |
| `kernel.panic_on_oops` | 1 | required by `protect-kernel-defaults` |
| `net.ipv4.ip_forward` | 1 | pod routing |
| `net.bridge.bridge-nf-call-iptables` / `-ip6tables` | 1 | kube-proxy and kube-router see bridged traffic |
| `net.ipv4.conf.all.rp_filter` / `.default.rp_filter` | 2 | loose mode; strict mode drops asymmetric flannel/WireGuard traffic (verify in lab, 08-AC-9) |
| `net.netfilter.nf_conntrack_max` | 262144 | headroom for pods + proxy tunnels |
| `fs.inotify.max_user_instances` | 8192 | kubelet, Longhorn, Flux, many watchers |
| `fs.inotify.max_user_watches` | 524288 | same |
| `fs.file-max` | 2097152 | k3s `LimitNOFILE` 1048576 |
| `vm.max_map_count` | 262144 | search-style workloads in `apps` |
| `kernel.pid_max` | 4194304 | kubelet pid accounting headroom |
| `net.core.somaxconn` | 4096 | Traefik, Forgejo |
| `kernel.dmesg_restrict` / `kernel.kptr_restrict` | 1 / 2 | CIS |
| `kernel.randomize_va_space` | 2 | CIS |
| `fs.suid_dumpable` | 0 | CIS |
| `fs.protected_hardlinks` / `fs.protected_symlinks` | 1 / 1 | CIS |
| `net.ipv4.conf.all.accept_redirects` / `.default.accept_redirects` | 0 | CIS |
| `net.ipv4.conf.all.send_redirects` / `.default.send_redirects` | 0 | CIS |
| `net.ipv4.conf.all.accept_source_route` | 0 | CIS |
| `net.ipv4.tcp_syncookies` | 1 | CIS |
| `net.ipv6.conf.all.accept_ra` | 1 on the provider uplink only if the provider uses SLAAC (plan 04 fact), else 0 | keep SSH-over-IPv6 working where enabled |

IPv6 itself stays enabled in the kernel (disabling it breaks some distro services); it is firewalled.

### 9.6 Filesystems and disks

- One root filesystem (ext4 or xfs) of at least **100 GiB** (preferred 200 GiB NVMe), SSD or NVMe, for both profiles (SPEC v1.2 §11/§12, CD-25). Plan 11 §12 is the authoritative disk budget.
- Separate **directories** (not partitions) for each data class, so a later dedicated disk can be mounted per path without reconfiguration:
  - `/var/lib/rancher/k3s` (k3s, etcd, containerd images, pod logs under `/var/log/pods` stay on `/var/log`);
  - `/var/lib/longhorn` (Longhorn replicas; must be ext4 or xfs);
  - `/var/lib/strange-company/local-path` (local-path PVs: CNPG PGDATA, Prometheus);
  - `/var/lib/node_exporter/textfile`.
- **CI workspace filesystem** (ADR-0305, owner plan 20; this plan creates it): a dedicated ext4 filesystem mounted at `/var/lib/sc/ci-workspace`, so a runaway CI build fills only that filesystem and never `/` or `/var/lib/rancher`.
  - If the node has an LVM volume group with enough free extents (some `existing` nodes), the role creates LV `sc-ci-workspace` in it. Otherwise (the VPS default) it creates the image file `/var/lib/sc/ci-workspace.img` with `fallocate -l <size>` (fully allocated, so the root-disk budget is honest), formats it `mkfs.ext4 -F -m 0 -L sc-ciws`, and mounts it through the systemd unit `var-lib-sc-ci\x2dworkspace.mount` (`What=/var/lib/sc/ci-workspace.img`, `Type=ext4`, `Options=loop,noatime,nodev,nosuid`, `WantedBy=local-fs.target`, ordered `Before=k3s.service`).
  - Size `node_ci_workspace_size`: 12 GiB on self-hosting-minimum and 24 GiB on small-ha, for any disk ≥ 100 GiB (plan 11 §12 budgets these lines; ADR-0305, CD-06). Workflow PVC request caps (CD-06): 8Gi in `ci-jobs` (default 6Gi) and 10Gi in `ci-build`; on one node the two lanes never run together (plan 51 §8.2), so 12 GiB holds the largest single workflow plus headroom. Growth only: a larger value runs `fallocate` + `resize2fs` online; a smaller value fails the run with `CI_WORKSPACE_SHRINK_REFUSED`.
  - The local-path-provisioner mapping of StorageClass `ci-workspace` to this path is plan 20's (with plan 07, since k3s manages `local-path-config`).
- Preflight (`sc_preflight/disk.yml`):
  - root fs type ∈ {ext4, xfs}, else `NODE_FS_UNSUPPORTED`;
  - size ≥ 100 GiB, else `NODE_DISK_TOO_SMALL` (SPEC v1.2; 75 GiB nodes are refused); 100–199 GiB passes with the warning `NODE_DISK_BELOW_PREFERRED`; inodes ≥ 4 M (ext4), else `NODE_INODES_TOO_FEW`;
  - etcd fsync test with `fio` (installed by `baseline/packages.yml`): `fio --name=sc-etcd-fsync --directory=/var/lib/rancher --rw=write --ioengine=sync --fdatasync=1 --size=22m --bs=2300 --output-format=json`. p99 `sync.lat_ns` ≤ 10 ms passes, 10–25 ms warns (`NODE_DISK_SLOW`), > 25 ms fails (`NODE_DISK_TOO_SLOW`). The JSON result is stored in the configure journal step evidence (plan 50).
- Mount options: `noatime` added to the root fs if absent (remount, persisted in fstab). `/tmp` is left as the distro provides (noexec on `/tmp` breaks apt hooks and some installers).

### 9.7 Container runtime assumptions and Longhorn prerequisites

- **Runtime:** k3s's bundled containerd only (plan 07, `prefer-bundled-bin: true`). No `docker`, `docker.io`, `containerd`, `containerd.io`, `podman` or `cri-o` packages or running units on nodes. Preflight fails with `NODE_FOREIGN_RUNTIME` if found (on `existing` nodes the operator removes them; plan 05).
- cgroup v2: `stat -fc %T /sys/fs/cgroup` = `cgroup2fs`, else `NODE_CGROUP_V1`. systemd cgroup driver (k3s auto-detects).
- AppArmor enabled (`aa-status --enabled` exit 0). SELinux is out of scope.
- **Longhorn V1 host prerequisites** (plan 13 consumes):
  - packages `open-iscsi` (service `iscsid` enabled and running), `nfs-common` (RWX and NFS backup targets, harmless otherwise), `cryptsetup`, `dmsetup`, `util-linux` ≥ distro version, `jq`;
  - `/etc/multipath.conf` with `blacklist { devnode "^sd[a-z0-9]+" }` and `defaults { user_friendly_names yes }`, `multipathd` restarted; if the `multipath-tools` package is absent it is not installed (Longhorn only needs the blacklist when multipathd exists);
  - `iscsi_tcp` module loaded;
  - verified with Longhorn's preflight (`longhornctl check preflight`, version-matched to the pinned Longhorn; verify the CLI name and subcommand at implementation, 08-AC-12).

### 9.8 Logs

- **journald** (`/etc/systemd/journald.conf.d/10-sc.conf`): `Storage=persistent`, `SystemMaxUse=1G`, `SystemKeepFree=5G`, `SystemMaxFileSize=64M`, `MaxRetentionSec=14day`, `RuntimeMaxUse=128M`, `RateLimitIntervalSec=30s`, `RateLimitBurst=10000`, `ForwardToSyslog=no`.
- **rsyslog** (Ubuntu ships it): left installed but receives nothing from journald (`ForwardToSyslog=no`).
- **logrotate** (`/etc/logrotate.d/sc-default-caps`): every `/var/log/*.log` and `/var/log/syslog` rotate daily, `rotate 7`, `compress`, `maxsize 100M`, `missingok`, `notifempty`.
- **Container logs:** kubelet `container-log-max-size=10Mi`, `container-log-max-files=3` (plan 07 cluster file).
- **k3s audit log:** plan 07 (≤ 300 MiB).
- **auditd** (CIS-lite): installed, `max_log_file = 50`, `num_logs = 4`, `max_log_file_action = ROTATE`, `space_left_action = SYSLOG`. Rules in `/etc/audit/rules.d/90-sc.rules`: watches (`-w ... -p wa -k sc-config`) on `/etc/rancher/k3s/`, `/etc/sudoers`, `/etc/sudoers.d/`, `/etc/ssh/sshd_config.d/`, `/etc/sc/`, `/etc/passwd`, `/etc/shadow`, `/etc/group`, plus `execve` of `/usr/local/bin/k3s` with `-k sc-k3s-exec`. Rules are immutable after load (`-e 2`) only on small-ha, so single-node operators can still adjust them without a reboot.

### 9.9 Update and reboot policy (ADR-0155)

- **Security updates:** `unattended-upgrades` with origins restricted to the security pocket: Debian `origin=Debian,codename=${distro_codename}-security,label=Debian-Security`; Ubuntu `${distro_id}:${distro_codename}-security` and `${distro_id}ESMApps:${distro_codename}-apps-security` / `${distro_id}ESM:${distro_codename}-infra-security` (only effective if the host is attached to Ubuntu Pro). `Unattended-Upgrade::Automatic-Reboot "false"` (reboots are handled below), `Remove-Unused-Kernel-Packages "true"`, `Remove-Unused-Dependencies "true"`, `MinimalSteps "true"`. Runs daily via the distro's `apt-daily-upgrade.timer`.
- **Non-security updates and OS release upgrades:** never automatic. Plan 48 owns them (`os-upgrade.yml`, serial 1).
- **Reboots, single node:** `sc-maintenance.timer` fires at the start of `node_maintenance_window` (`OnCalendar=Sun *-*-* 04:00:00 UTC`, `Persistent=false`). `sc-maintenance.service`:
  1. exits if `/run/sc/maintenance-inhibit` exists, or if the plan 50 run lock object reports an active run (read through a local marker `/run/sc/bootstrap-active`, written by Ansible plays for their duration);
  2. if `/var/run/reboot-required` exists: wait up to 45 min until no container of namespaces `agent-jobs`, `ci-jobs`, `ci-build` or `dr-tests` runs on the node (node-local check `k3s crictl ps -q --label io.kubernetes.pod.namespace=<ns>`, no API identity needed); if still busy, skip the reboot this window (`action="inhibited-busy"`, retried next window; `SCNodeRebootRequiredStale` covers repeated skips). Otherwise `k3s etcd-snapshot save --name pre-reboot`, wait ≤ 120 s for it, then `systemctl reboot` (GC-20). Dispatch is blacked out by the Runner itself (GC-20): the window has **one source**, `cluster-vars.SC_MAINTENANCE_WINDOW` (e.g. `Sun 04:00-06:00 UTC`, written by plan 50 from `node.maintenanceWindow`), which the `baseline` role reads for `sc-maintenance.timer` and which the `strange-company` chart receives as value `runner.maintenanceWindow` (Flux substitution, plan 54). The Runner dispatches no new worker from 45 min before the window start (the longest profile deadline) to the window end (plan 26 interface). The node-local busy check above is the final guard: no reboot while any worker or CI container runs. Alertmanager's mute time interval `sc-maintenance-window` for non-always-notify alerts (plan 42) is an assumed interface;
  3. otherwise, if `k3s.service` has been active longer than `node_k3s_restart_max_age_days` (28): `systemctl restart k3s` (pods keep running because of `KillMode=process`; this renews k3s leaf certificates, plan 07 §19.3);
  4. logs its decision to the journal with `SYSLOG_IDENTIFIER=sc-maintenance` and writes `sc_node_maintenance_last_run_timestamp_seconds` and `sc_node_maintenance_last_action{action}`.
- **Reboots, small-ha:** kured (plan 47 enables it via the small-ha overlay; §19 of this plan specifies values). `sc-maintenance` on small-ha performs **only** step 3, staggered by node index (node-01 Sun, node-02 Mon, node-03 Tue, same time) so no two servers restart k3s on the same day.
- Expected outage per reboot: single node ≈ 3–6 min of API and workload downtime (all pods restart). Recorded in the operator docs as accepted for self-hosting-minimum (SPEC §12: reduced redundancy).

### 9.9a On-demand etcd snapshot pruning (servers only)

k3s's `etcd-snapshot-retention: 28` prunes **scheduled** snapshots; the on-demand `pre-<op>-…` snapshots that plans 07, 47 and 48 take before risky operations are not reliably covered by it (retention is applied per name prefix; verify on the pinned k3s, 08-AC-21). Without a bound they would accumulate in `/var/lib/rancher/k3s/server/db/snapshots` (plan 07 budget 2 GiB).

- `sc-etcd-snapshot-prune.timer`: `OnCalendar=*-*-* 05:30:00 UTC`, `Persistent=true`, on `k3s_servers` only; delivered by the `baseline` role's `maintenance.yml`, populated with k3s paths by plan 07.
- `sc-etcd-snapshot-prune.service` (oneshot): lists local snapshots (`k3s etcd-snapshot ls --output json`, verify; fallback: the snapshot directory listing) whose name starts with `pre-`; keeps every one younger than `node_etcd_ondemand_retention_days` (30) and, among older ones, nothing; additionally keeps at most `node_etcd_ondemand_max` (10) newest regardless of age. Deletes the rest **locally only** (`k3s etcd-snapshot delete <name>` without S3 flags; verify that this leaves the S3 copy, 08-AC-21). S3 copies of `pre-*` snapshots expire by the `etcd-snapshots` bucket lifecycle (plans 14/43), so off-node history outlives the local prune.
- Never deletes scheduled (`etcd-snapshot-*`) snapshots and never runs while `/run/sc/bootstrap-active` exists.
- Writes `sc_etcd_snapshot_prune_last_run_timestamp_seconds`, `sc_etcd_snapshot_ondemand_local_count` and `sc_etcd_snapshot_dir_bytes` to `sc_node.prom`.

### 9.10 Time synchronization

`chrony` with `/etc/chrony/conf.d/10-sc.conf`: `pool <server> iburst maxsources 4` per configured or default pool, `makestep 1.0 3`, `rtcsync`, `leapsectz right/UTC`. `systemd-timesyncd` is disabled and masked. The node timezone is UTC (`timedatectl set-timezone UTC`). Verify: `chronyc tracking` shows `Leap status : Normal` and `System time` offset < 50 ms within 5 min of configure.

### 9.11 Practical CIS hardening (beyond items above)

Applied: separate admin user, key-only SSH, sudo logging to journald (`Defaults logfile` not used; journald captures `sudo`), file permissions (`/etc/ssh/sshd_config.d/*` 0600, `/etc/rancher` 0700), sysctls in §9.5, module blacklist, auditd-lite, core dumps disabled (`/etc/security/limits.d/90-sc.conf` `* hard core 0`; `systemd-coredump` `Storage=none`), unused services disabled if present (`avahi-daemon`, `cups`, `rpcbind` unless `nfs-common` needs it on Debian 13, verify), login banners (`/etc/issue.net` short notice).

Deliberately not applied (with reason): separate partitions for `/var`, `/var/log`, `/home` (VPS images have one disk; directories are separated instead); `/tmp` noexec (breaks apt); AIDE (memory and I/O cost on a small node; Git-managed config plus auditd watches cover the critical files); disabling IPv6 in the kernel (breaks distro services); password aging (no passwords).

## 10. Secrets

| Secret class | Role here | Notes |
|---|---|---|
| `SC-SSH-OPERATOR` (plan 50 calls it `SC-OPERATOR-SSH`; plan 35 register name wins) | Consumed: public key placed in `/home/sc-admin/.ssh/authorized_keys` (0600) | Rotation: `rotate-secrets.md` (re-run `./bootstrap configure --tags baseline` with the new key; the role removes keys not in the configured set from `sc-admin` only) |
| `SC-BREAK-GLASS-CLUSTER` (plan 15/35) | Consumed: public half only (`infra/ansible/files/breakglass.pub`) in `/home/sc-breakglass/.ssh/authorized_keys` | Rotation (`rotate-break-glass.md#cluster`): commit the new public key, run `./bootstrap configure --tags baseline`; the role removes any other key for that user |
| `SC-BREAK-GLASS-INFRA` (plan 15/35) | Interface | Provider console access is the break-glass path when SSH is broken (for example a firewall mistake). This plan guarantees the provider console login is not disabled: `sc-admin` has no password, so console break-glass uses the provider's rescue mode (plan 04/05) |

No new secret class is introduced by this plan.

## 11. Storage

This plan creates the directories in §9.6 and caps the log stores in §9.8. Budgets (plan 11 §12): OS + packages + journald (1 GiB) + auditd (200 MiB) + `/var/log` (≤ 1 GiB after caps) ≈ 7–8 GiB on the preferred node. The plan owns no PVC or object-storage path.

## 12. Resource requirements

Host services outside Kubernetes, covered by `system-reserved` (plan 51 §9): `sshd`, `chronyd`, `systemd-journald`, `iscsid`, `multipathd`, `auditd`, `unattended-upgrades` (bursty, up to ≈ 300 MiB during dpkg runs), `sc-node-facts` (seconds every 5 min). Measured steady RSS ≈ 150–250 MiB; the 512 MiB reservation (448 MiB constrained) leaves room for the unattended-upgrades burst. The P12 memory gate (plan 51 §14) verifies it.

## 13. Security boundary

- **Trust:** the node OS is the root of trust for everything above it. Access is `sc-admin` via SSH key, plus the provider console in rescue mode as break-glass.
- **Exposure:** exactly §9.4. Plan 50's V-11 runs an external scan (`nmap -Pn -sT -p 1-65535` and `-sU -p 51820,51821`) from the toolbox host against each public IP and compares the result with the matrix for the current mode and node count.
- **DNAT bypass:** traffic DNAT'd to pods (hostPort, NodePort, LoadBalancer via ServiceLB) never reaches the input chain. Required compensating controls:
  - ServiceLB is disabled (plan 07);
  - plan 37 VAP `sc-no-nodeport-lb` denies `Service.spec.type` ∈ {`NodePort`, `LoadBalancer`} in every namespace;
  - plan 37 VAP restricts `hostPort` to Traefik pods in `ingress` (direct mode, ADR-0151).
- **Host egress** is unrestricted. Residual risk: a compromised host process can reach the internet. Accepted: the host is fully trusted; pod egress is controlled.
- **etcd metrics on 2381** are plaintext HTTP between servers over the public network at small-ha. They contain no secrets. Accepted residual risk (plan 07 §13).
- **sudo** is passwordless for `sc-admin` because automation needs it and the account is key-only. Use is logged to journald and auditd.

## 14. Happy path

### 14.1 First configure (P03)

1. **Connect** as the initial user from `bootstrap-config.yaml` (key from `SC-SSH-OPERATOR`), with host-key checking against plan 03's recorded fingerprint (`StrictHostKeyChecking=yes`, known_hosts generated by plan 50).
2. **`sc_preflight`** (read-only, fails fast with the codes in §15): OS and kernel, cgroup v2, AppArmor, CPU/memory vs profile (`NODE_TOO_SMALL` if below SPEC §11 minimum; `NODE_CONSTRAINED` warning between 8 and 11 GiB MemTotal), disk (§9.6 incl. fio), foreign runtime, foreign firewall, pod/service CIDR overlap with any host route or address (`NODE_CIDR_OVERLAP`), DNS resolution of `deb.debian.org`/`archive.ubuntu.com`, outbound HTTPS to the S3 endpoint.
3. **`baseline`**: install marker `/etc/strange-company/install-uid` (§4; written before anything else so an interrupted run is still recognized as ours), the managed `/etc/hosts` block (CD-21), the `sc-breakglass` account and PAM hook (SEC-9), packages (`chrony`, `nftables`, `open-iscsi`, `nfs-common`, `cryptsetup`, `jq`, `fio`, `auditd`, `unattended-upgrades`, `apt-listchanges` removed, `curl`, `ca-certificates`), users (`sc-admin` + key + sudoers), SSH drop-in (validated, reloaded), time, swap, modules, sysctls (`sysctl --system`), journald, logrotate, updates, storage prerequisites, hardening, dirs, `sc-node-facts`, `sc-maintenance`.
4. **Admin transition:** Ansible opens a second connection as `sc-admin` (`ansible.builtin.ping` with `ansible_user=sc-admin`). Only after it succeeds does the play apply `AllowGroups sc-admin` and reload sshd. The run then records `ssh.admin_user: sc-admin` for the node in Git (see §5); subsequent runs use `sc-admin`. If the second connection fails, the drop-in is applied without `AllowGroups` and the run fails with `NODE_ADMIN_LOGIN_FAILED`, leaving the original user working.
5. **`firewall`**: render, `nft -c -f` check, enable `sc-firewall.service`, `systemctl reload-or-restart`, then from the toolbox re-test SSH within 30 s (Ansible `wait_for_connection` with a fresh connection). If SSH fails, a local `at`-style safety timer that was armed before the load (`systemd-run --on-active=120 nft delete table inet sc_host`) fires and removes the table; the run fails with `FIREWALL_LOCKOUT_ROLLBACK`. On success the safety timer is cancelled.
6. Plan 07's `k3s` role and plan 09's `network` role run.
7. **`verify.yml`** (§14.3).

### 14.2 Re-run

A converge with unchanged inputs reports `changed=0`. A changed firewall render reloads the table atomically (no window without rules), with the same safety timer.

### 14.3 Verification assertions (`baseline/verify.yml`, `firewall/verify.yml`)

Each is a command with an exact expected result; failures name the item:
- `cat /etc/strange-company/install-uid` equals the journal's `install_uid`;
- `getent hosts git.<baseDomain>` returns `10.43.0.20` (CD-21);
- `id -nG sc-breakglass` contains `sc-breakglass`; `sudo -l -U sc-breakglass` shows `NOPASSWD: ALL`; `grep -c pam_exec.so /etc/pam.d/sshd` ≥ 1;
- `id -nG sc-admin` contains `sudo` and `sc-admin`; `sshd -T | grep -E '^(permitrootlogin|passwordauthentication|kbdinteractiveauthentication) '` → `no` for all three;
- `swapon --show --noheadings | wc -l` = 0;
- `sysctl -n` of every §9.5 key equals the table value;
- `lsmod` contains every module in §9.5; `modprobe -n -v cramfs` prints `install /bin/false`;
- `systemctl is-active chrony iscsid sc-firewall auditd` all `active`; `systemctl is-enabled nftables` = `masked`; `systemctl is-active ufw` ≠ `active`;
- `nft list table inet sc_host` succeeds and its `sha256` (of `nft -s list table inet sc_host` output) equals the hash Ansible computed from the render, which is also written to `sc_host_firewall_ruleset_info{sha256}`;
- `journalctl --header | grep -c 'Max Use'` reflects the cap (or `systemd-analyze cat-config systemd/journald.conf` contains `SystemMaxUse=1G`);
- `chronyc tracking` offset < 50 ms;
- `stat -fc %T /sys/fs/cgroup` = `cgroup2fs`;
- `findmnt -n -o FSTYPE,SIZE /var/lib/sc/ci-workspace` shows `ext4` and the configured size (±2 %);
- `/var/lib/node_exporter/textfile/sc_node.prom` exists and is < 10 min old.

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Unsupported OS / kernel / cgroup v1 | preflight `NODE_OS_UNSUPPORTED`, `NODE_CGROUP_V1` | configure blocked | fail closed | `bootstrap-preflight-failure.md` (plan 50) |
| Disk too slow for etcd | preflight `NODE_DISK_TOO_SLOW` | configure blocked | fail closed | same; choose another machine class |
| Foreign container runtime or firewall | `NODE_FOREIGN_RUNTIME`, `NODE_FOREIGN_FIREWALL` | configure blocked | fail closed | same (plan 05 for existing nodes) |
| CIDR overlap with host networks | `NODE_CIDR_OVERLAP` | configure blocked (pod routing would break) | fail closed | choose other CIDRs at install (plan 07) |
| Firewall load locks out SSH | post-load reconnect fails | node unreachable for ≤ 120 s | safety timer deletes `sc_host`; run fails `FIREWALL_LOCKOUT_ROLLBACK` | fix render via PR; provider console if the timer also failed |
| Firewall table missing or drifted at runtime | `SCHostFirewallNotLoaded` | node more exposed than intended | none | re-run `./bootstrap configure --tags firewall` |
| Admin user transition fails | `NODE_ADMIN_LOGIN_FAILED` | original user still works | stop | `bootstrap-resume.md` (plan 50) |
| Clock drift / NTP unreachable | `SCNodeClockSkew` | TLS, etcd lease and token-expiry errors | chrony keeps retrying | §20.3 |
| Reboot required but not performed for 7 days | `SCNodeRebootRequiredStale` | unpatched running kernel/libc | none | §20.4 |
| Swap re-enabled (manual or package) | `sc_node_swap_bytes > 0` → `SCNodeSwapEnabled` | kubelet may refuse to start after restart | none | re-run configure `--tags baseline` |
| Root filesystem filling | `SCNodeRootDiskLow` / `SCNodeRootDiskCritical`, kubelet `DiskPressure` → `SCNodeDiskPressure` | image GC, then evictions | kubelet GC and eviction (§20.1) | `disk-pressure.md` |
| journald or `/var/log` growth | covered by caps; FS alerts if caps fail | — | caps | `disk-pressure.md` |
| unattended-upgrades breaks a package | dpkg error in journal; `sc_node_unattended_upgrades_last_status != 0` → `SCNodeUpdatesFailing` | security patches stop | none | plan 48 `upgrade-platform-component.md` §OS |
| Longhorn prerequisites missing (iscsid down) | `verify.yml`; Longhorn node not Ready (plan 13) | Longhorn volumes cannot attach on that node | systemd restarts iscsid | plan 13 `longhorn-replica-loss.md` |
| sc-node-facts stops | `SCNodeFactsStale` (plan 07) | node-level alerts go blind | none | `monitoring-failure.md` |

## 16. Retry behavior

- Package installs: Ansible `retries: 3`, `delay: 10`, `until: result is succeeded` (apt lock contention with `unattended-upgrades` is the common cause; the role first waits ≤ 300 s for `/var/lib/dpkg/lock-frontend` to be free).
- The fio test is run once; a `warn` result is not retried.
- Firewall reload is never retried automatically after a lockout rollback.
- `sc-maintenance` does not retry within a window; the next window retries.

## 17. Idempotency behavior

- Every file is a template or `copy` with content comparison; services restart or reload only via handlers on change.
- `sysctl --system` and `modprobe` run only when their files changed.
- The admin-user transition is guarded by the Git-recorded `ssh.admin_user` and by an idempotent check (`getent passwd sc-admin` + authorized_keys content); after journal loss the Git record still holds.
- The firewall load is atomic (`delete table` + define in one `nft -f` transaction). Loading the same file twice yields the same ruleset hash.
- `sc-maintenance` actions are naturally idempotent (no reboot without the sentinel; restart only after 28 days).

## 18. Bootstrap behavior

- **configure** (plan 50 §14.4): `site.yml` with tags `baseline,firewall,k3s,network` on `node-01`. The done check includes `changed=0` on an immediate `--check` re-run.
- **expand** (plan 47): `baseline,firewall` on the new nodes **and** `firewall` on existing nodes (peer sets grow) before any k3s join.
- **Black start** (plan 45): identical to a fresh configure on new or wiped nodes. No node state is restored; it is all derived from Git.

## 19. Upgrade behavior

- **Baseline changes** arrive as PRs to `infra/ansible/**`; the operator applies them with `./bootstrap configure --tags baseline,firewall` (plan 48 explains why node configuration is not Flux-managed: ADR-0154).
- **OS packages:** security updates automatically (§9.9); everything else through plan 48's `os-upgrade.yml` (`serial: 1`, drain, `apt-get dist-upgrade`, reboot, health gates).
- **OS major releases** (for example Debian 13 → 14): node replacement, not in-place upgrade, using `add-node.md` then `remove-node.md` at small-ha, or a rebuild at self-hosting-minimum (plan 48 §OS).
- **kured values** (small-ha), selection rule "latest kured chart release whose app version supports the pinned Kubernetes minor" (chart and image digest in `versions/platform.lock.yaml`; flag names verified at implementation and re-verified by plan 47 at GATE-H01):
  ```yaml
  configuration:
    rebootDays: [sun]
    startTime: "04:00"
    endTime: "06:00"
    timeZone: UTC
    period: 5m
    drainTimeout: 15m
    lockTtl: 30m
    concurrency: 1
    rebootSentinel: /var/run/reboot-required
    preRebootNodeLabels: ["strange.company/rebooting=true"]
    postRebootNodeLabels: ["strange.company/rebooting=false"]
  priorityClassName: platform-critical
  ```
  PDBs (plans 12, 13, 47, 51) bound what a drain may evict. A drain that cannot complete in 15 minutes aborts the reboot for that window (kured uncordons) and `SCNodeRebootRequiredStale` eventually fires.

## 20. Recovery behavior

### 20.1 Disk pressure (summary; full steps in `procedures/disk-pressure.md`)

Thresholds, in the order they bite (percentages of the root fs, 100–200 GiB; hard eviction 10 % per plan 07, STO-3):

| Free space | Mechanism | Effect |
|---|---|---|
| < 25 % | `SCNodeRootDiskLow` (plan 42 threshold change requested, §21) | operator notified |
| < 20 % (image fs > 80 % used; same fs as root) | kubelet image GC (high 80 / low 70) | unused images deleted until 70 % used |
| < 15 % for 2 min | kubelet soft eviction `nodefs.available<15%` | lowest-ranked pods evicted (agent and CI first, plan 51 §9.4) |
| < 15 % | `SCNodeRootDiskCritical` (requested threshold) | page |
| < 10 % | kubelet hard eviction | immediate eviction; `DiskPressure` → `SCNodeDiskPressure` |
| < 5 % inodes | hard eviction on `nodefs.inodesFree` | same |

### 20.2 Firewall lockout

If both the safety timer and SSH fail: provider console or rescue mode (plan 04/05, `SC-BREAK-GLASS-INFRA`), then `nft delete table inet sc_host`, then fix the render by PR and re-run configure.

### 20.3 Clock skew

Check `chronyc sources -v`. If the pool is unreachable (egress blocked by the provider), set `node_ntp_servers` to the provider's NTP (plan 04 documents OVH's) by PR and re-run `--tags baseline`. A skew over 1 s is stepped by `makestep` only in the first 3 updates; after that chrony slews, so for a large skew run `chronyc makestep` once.

### 20.4 Stale pending reboot

Single node: check `/run/sc/maintenance-inhibit` and the last `sc-maintenance` journal entry; reboot manually in a window (`./bootstrap exec -- ssh node-01 'sudo systemctl start sc-maintenance'`). small-ha: check kured logs for blocked drains (PDB) and fix the blocking workload.

## 21. Observability

**Textfile metrics** (`sc_node.prom`, written by `sc-node-facts`):

| Metric | Labels | Meaning |
|---|---|---|
| `sc_node_facts_last_run_timestamp_seconds` | — | exporter liveness (plan 07 alert `SCNodeFactsStale`) |
| `sc_node_baseline_info` | `baseline_version`, `os` | 1 |
| `sc_host_firewall_loaded` | — | 1 if `inet sc_host` exists |
| `sc_host_firewall_ruleset_info` | `sha256` | live ruleset hash |
| `sc_host_firewall_expected_info` | `sha256` | hash from the last Ansible render (`/etc/sc/firewall/sc-host.nft.sha256`) |
| `sc_host_firewall_dropped_packets_total` | `counter` (`sc_input_dropped`, `sc_ssh_ratelimited`) | from `nft -j list counters table inet sc_host` |
| `sc_node_reboot_required` | — | 1 if `/var/run/reboot-required` exists |
| `sc_node_reboot_required_since_timestamp_seconds` | — | mtime of the sentinel |
| `sc_node_swap_bytes` | — | `SwapTotal` |
| `sc_etcd_snapshot_prune_last_run_timestamp_seconds`, `sc_etcd_snapshot_ondemand_local_count`, `sc_etcd_snapshot_dir_bytes` | — | §9.9a (servers) |
| `sc_node_unattended_upgrades_last_status` | — | exit status of the last run (from journal) |
| `sc_node_maintenance_last_run_timestamp_seconds`, `sc_node_maintenance_last_action` | `action` ∈ {`none`,`reboot`,`k3s-restart`,`inhibited`} | §9.9 |

Clock metrics come from node-exporter's `timex` collector (`node_timex_offset_seconds`, `node_timex_sync_status`).

**Alerts introduced (plan 42 to register; SC prefix per the catalogue convention):**

| alertname | Expression sketch | for | Severity / class | Runbook |
|---|---|---|---|---|
| `SCNodeClockSkew` | `abs(node_timex_offset_seconds) > 0.05 or node_timex_sync_status == 0` | 15m | warning / notify | `lost-node.md` §Clock (this plan §20.3) |
| `SCNodeRebootRequiredStale` | `sc_node_reboot_required == 1 and (time() - sc_node_reboot_required_since_timestamp_seconds) > 7*86400` | 1h | warning / notify | §20.4 (`upgrade-platform-component.md` §OS) |
| `SCHostFirewallNotLoaded` | `sc_host_firewall_loaded == 0 or on(instance) (sc_host_firewall_ruleset_info != on(instance) group_left sc_host_firewall_expected_info)` (implemented as a sha256 label comparison in a recording rule) | 10m | critical / page | `add-node.md` §Firewall (re-run configure) |
| `SCNodeSwapEnabled` | `sc_node_swap_bytes > 0` | 15m | warning / notify | re-run configure |
| `SCEtcdSnapshotDirLarge` | `sc_etcd_snapshot_dir_bytes > 2 * 1024^3 or (time() - sc_etcd_snapshot_prune_last_run_timestamp_seconds) > 2*86400` | 1h | warning / notify | `disk-pressure.md` §Etcd snapshots |
| `SCNodeMaintenanceStale` | `time() - sc_node_maintenance_last_run_timestamp_seconds > 35*86400` (GC-21: the monthly k3s restart that renews certificates stopped) | 1h | warning / notify | `certificate-failure.md` §K3S |
| `SCNodeUpdatesFailing` | `sc_node_unattended_upgrades_last_status != 0` | 24h | warning / notify | `upgrade-platform-component.md` §OS |

**Requested changes to existing catalogue alerts** (plan 42): `SCNodeRootDiskLow` threshold `< 0.25` free (currently `< 0.15`) and `SCNodeRootDiskCritical` `< 0.15` free (currently `< 0.08`). With the current values, the critical alert fires only after kubelet hard eviction (`< 10 %`) has already started, which defeats its purpose. This matches plan 11 §12 ("warning at 75 % used, critical at 85 %").

## 22. Testing strategy

- **Static (F10, CI, no infrastructure):** `ansible-lint` (production profile + custom rules in §8.1); `ansible-playbook --syntax-check`; render tests that template the firewall file for the four matrix cases (1/3 nodes × tunnel/direct) and check them with `nft -c -f` inside a container with `CAP_NET_ADMIN` in a throwaway network namespace; golden-file comparison of rendered sshd, sysctl, journald and unattended-upgrades files.
- **Lab (F10, `make e2e-lab`, plan 05 QEMU VMs, Debian 13 and Ubuntu 24.04):** full `site.yml`; idempotency (`changed=0`); external port scan from the lab host for every matrix case; firewall lockout drill (deliberately render a ruleset without SSH, observe rollback within 120 s); reboot window drill with a faked sentinel and a faked clock (`systemd-run --on-calendar` override); fio warn/fail paths using a throttled virtual disk (`qemu` `iops` limit).
- **Live (P03):** gate items below. No paid model APIs are involved anywhere.

## 23. Acceptance criteria

### GATE-F10

- **08-AC-1** `make test-ansible-baseline` runs `ansible-lint` with zero violations and `--syntax-check` on `site.yml` and every playbook in `playbooks/`. Exit 0.
- **08-AC-2** `make test-firewall-render` renders `sc-host.nft` for {1, 3 nodes} × {tunnel, direct} and `nft -c -f` accepts all four. The 3-node render's `sc_peers_v4` contains exactly the six fixture addresses, and no render contains `flush ruleset`.
- **08-AC-3** Lab, Debian 13 and Ubuntu 24.04 separately: the pre-install `./bootstrap configure` (lab/`existing` mode, plan 50 BOOT-11) followed by an immediate second run reports `changed=0` for the `baseline` and `firewall` roles.
- **08-AC-4** Lab: an external scan from the lab host (`nmap -Pn -sT -p 1-65535 <vm-ip>`) of a one-node tunnel-mode VM lists only `22/tcp` open. With a second fixture VM outside the peer set, `nc -z -w 3 <vm-ip> 6443` fails.
- **08-AC-5** Lab: `ssh -o PreferredAuthentications=password sc-admin@<vm>` fails with `Permission denied (publickey)`, `ssh root@<vm>` fails, and `ssh <initial-user>@<vm>` fails after the transition, while `ssh sc-admin@<vm> true` succeeds. `sshd -T | grep -i ^allowtcpforwarding` = `allowtcpforwarding local`.
- **08-AC-6** Lab lockout drill: a fixture render without the SSH rule makes the run fail with `FIREWALL_LOCKOUT_ROLLBACK`, and `ssh sc-admin@<vm> true` succeeds again within 150 s of the load.
- **08-AC-7** Lab: `systemctl restart nftables` is impossible (`masked`), and after `systemctl restart sc-firewall` the iptables-nft chains from k3s are still present (`iptables-save | grep -c KUBE-` unchanged before and after).
- **08-AC-8** Lab: with a throttled disk, preflight fails with `NODE_DISK_TOO_SLOW`, and the journal step evidence contains the fio JSON p99 value.
- **08-AC-9** Lab, three VMs: with `rp_filter=2`, a pod on node-02 reaches a pod on node-03 (`curl` to a test HTTP server), and `tcpdump -ni <uplink> udp port 51820` shows the traffic while `tcpdump -ni <uplink> tcp port 80` between the node IPs shows none (re-verifies ADR-0011 encryption and the loose rp_filter choice).
- **08-AC-10** Lab reboot drill: with `/var/run/reboot-required` present and the timer forced (`systemctl start sc-maintenance`), the node reboots, `k3s etcd-snapshot ls` lists a `pre-reboot-*` snapshot taken before the reboot, and `sc_node_maintenance_last_action{action="reboot"}` is present after boot. With `/run/sc/maintenance-inhibit` present, no reboot happens and the action is `inhibited`.

### GATE-P03

- **08-AC-19** `./bootstrap exec -- ssh node-01 'cat /etc/strange-company/install-uid'` equals `./bootstrap status --json | jq -r .installation.install_uid`; in the lab, running configure against a VM whose marker holds a different uid fails with `NODE_BELONGS_TO_OTHER_INSTALL` and changes nothing (sha256 of `/etc/ssh/sshd_config.d/` unchanged).
- **08-AC-20** `./bootstrap exec -- ssh node-01 'findmnt -n -o SOURCE,FSTYPE,OPTIONS /var/lib/sc/ci-workspace'` shows a loop device (or the LV) with `ext4` and options containing `nodev,nosuid`; after `sudo systemctl restart var-lib-sc-ci\x2dworkspace.mount` the mount is present again; in the lab, filling it with `fallocate` to 100 % leaves `df /` usage unchanged within 100 MiB (co-verifies plan 20's 20-AC-10).
- **08-AC-21** Lab: create 15 snapshots `pre-test-<i>` (12 recent, 3 whose local files are back-dated with `touch -d '40 days ago'`); after `systemctl start sc-etcd-snapshot-prune`, local `pre-*` count = 10, no `etcd-snapshot-*` scheduled snapshot was removed, and the S3 bucket still lists all 15 `pre-test-*` objects (re-verifies local-only deletion and k3s retention behavior).
- **08-AC-22** Lab (SEC-5): from a pod on node-01 with **no** NetworkPolicy applied (k3s lab namespace without default-deny), `nc -z -w 3 <node-01 IP> 22` and `nc -z -w 3 <node-01 IP> 2379` fail, while `nc -z -w 3 <node-01 IP> 10250` succeeds. This proves the host firewall alone blocks SSH and etcd from pods.
- **08-AC-23** `./bootstrap exec -- ssh -i <bundle key> sc-breakglass@node-01 'sudo k3s kubectl get nodes'` lists node-01 Ready, and within 60 s `/var/lib/node_exporter/textfile/sc_breakglass.prom` holds a `sc_breakglass_ssh_last_login_timestamp_seconds` value within 60 s of the login (SEC-9; co-verifies 15-AC-19). Logging in as `sc-admin` does not change that value.
- **08-AC-24** `./bootstrap exec -- ssh node-01 'getent hosts git.<baseDomain>'` prints `10.43.0.20` (CD-21; the pull test with the tunnel down is 09-AC-17).
- **08-AC-11** `./bootstrap exec -- ssh node-01 'sudo nft list table inet sc_host'` exits 0 and `sc_host_firewall_ruleset_info` equals `sc_host_firewall_expected_info` (PromQL equality after P09; before P09 the textfile values are compared directly over SSH).
- **08-AC-12** `./bootstrap exec -- ssh node-01 'sudo longhornctl check preflight'` (or the pinned Longhorn version's equivalent) reports no errors, and `systemctl is-active iscsid` = `active` (re-verifies plan 13's host prerequisites).
- **08-AC-13** External scan of node-01's public IP from the toolbox host matches the §9.4 column for the configured mode: tunnel → only 22/tcp open; direct → 22, 80, 443 open (2222 only if opted in). UDP 51820 shows `open|filtered` only from a peer (not tested from outside at one node).
- **08-AC-14** `./bootstrap exec -- ssh node-01 'swapon --show --noheadings | wc -l; sysctl -n vm.overcommit_memory kernel.panic net.ipv4.ip_forward fs.inotify.max_user_watches'` prints `0`, `1`, `10`, `1`, `524288`.
- **08-AC-15** `./bootstrap exec -- ssh node-01 'chronyc tracking'` shows `Leap status : Normal` and |System time| < 0.05 s. `timedatectl show -p Timezone --value` = `UTC`.
- **08-AC-16** `systemd-analyze cat-config systemd/journald.conf | grep -E '^SystemMaxUse=1G$'` matches, and `journalctl --disk-usage` reports ≤ 1.0 G.
- **08-AC-17** `/var/lib/node_exporter/textfile/sc_node.prom` contains every metric family in §21 with a timestamp < 10 minutes old.
- **08-AC-18** `systemctl list-timers sc-maintenance.timer --no-legend` shows the next elapse on the configured weekday at 04:00 UTC, and `apt-config dump | grep 'Unattended-Upgrade::Automatic-Reboot '` shows `"false"`.

## 24. Explicit non-goals

- arm64 nodes, other distributions, SELinux, immutable OSes (Talos, Flatcar): not supported in MVP.
- Separate partitions or LVM layouts created by Strange Company (VPS images are used as provided).
- Host egress filtering.
- Full CIS benchmark conformance scoring, AIDE, and centralized syslog shipping (Loki covers pod logs and the k3s audit log at small-ha, plan 41).
- Automatic OS release upgrades.
- Managing provider-side firewalls (OVH network firewall); the host firewall is authoritative. Plan 04 MAY add a provider firewall later as defense in depth.

## 25. Open questions

- **Q-08-01** (blocking: no) Do Debian 13's `unattended-upgrades` and Ubuntu 24.04 both create `/var/run/reboot-required` after kernel updates without `update-notifier-common`? *Proposed:* install `needrestart` in list-only mode as a fallback and have `sc-node-facts` set `sc_node_reboot_required` when the running kernel differs from the newest installed one. Verified by 08-AC-10 on both distros. *Owner:* 08.
- **Q-08-02** (blocking: no) `rp_filter=2` versus `0` for flannel `wireguard-native`. *Proposed:* 2 (loose), re-verified by 08-AC-9; fall back to 0 if the lab shows drops. *Owner:* 08/09.
- **Q-08-03** (blocking: no) Should the API forward use a dedicated non-sudo SSH user with `PermitOpen 127.0.0.1:6443 <ips>:6443` instead of `sc-admin`? *Proposed:* post-MVP hardening; MVP uses `sc-admin`. *Owner:* 08/50.
- **Q-08-04** (blocking: no) Plan 11 §12 lists kubelet hard eviction at `nodefs.available<8%`, but plan 07 (binding brief) renders `<10%`. **Resolved:** 10 % everywhere (plans 07, 08, 11, 51 and the alert catalogue). *Owner:* 11.
- **Q-08-05** (blocking: no) The secret class for the operator SSH key is named `SC-OPERATOR-SSH` in plan 50 and `SC-SSH-OPERATOR` in `contracts/secret-classes.yaml`. *Proposed:* the register name wins; plan 50 renames. *Owner:* 35/50.
