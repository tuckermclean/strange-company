# 05 — Existing-nodes provider and the lab substrate

**Status:** Stage-0 plan (draft)
**DAG nodes:** F09 (co-owner with 03, 04), P02 (co-owner with 03, 04); lab-based contributions to P03 and H01 (§23, GC-01). Supplies the lab substrate that F10 (plans 07, 08), F08 (plan 50 `e2e-lab`) and the engine matrix of plan 02 use.
**Related plans:** [02](02-bootstrap-toolbox.md), [03](03-provider-interface.md), [04](04-ovh-provider.md), [07](07-k3s.md), [08](08-node-security.md), [14](14-object-storage.md), [47](47-ha-expansion.md), [49](49-destroy.md), [50](50-bootstrap-orchestrator.md), [52](52-ci-strategy.md)
**Contracts:** conforms to `contracts/provider.schema.yaml`; reads `bootstrap-config.schema.yaml` `spec.provider.existing`; uses the lab substrates in `contracts/substrate.schema.yaml` examples (`lab-small`)
**ADRs:** relies on ADR-0011, ADR-0020, ADR-0102; introduces ADR-0105 (lab substrate: unprivileged QEMU/KVM VMs presented to `existing`)

## 1. Purpose

`existing` accepts machines that already exist and are reachable over SSH (SPEC §13): bare metal, any VPS ordered by hand (including OVH fallback (c), plan 04), Proxmox/VMware guests, or CI lab VMs. It creates nothing and spends nothing. Its job is to **discover** each machine's facts, **validate** them against the substrate before anything is installed, and emit the same normalized `ProviderOutput` as a paid provider, so configure and every later phase cannot tell the difference.

The second half of this plan defines the **lab substrate**: local QEMU/KVM virtual machines built from pinned cloud images and presented to `existing`, which lets CI run the real configure path (Ansible, k3s, firewall) without any cloud account.

## 2. Requirements derived from SPEC.md

1. `existing` accepts already-provisioned SSH-accessible nodes [SPEC §13].
2. All providers expose normalized node information; higher layers do not depend on provider concepts [SPEC §13].
3. Nodes are validated against the substrate minimum; a smaller machine is never silently accepted [SPEC §11].
4. Validation before configuration covers configuration, SSH material, node count and (zero) cost [SPEC §14].
5. Expansion from one to three nodes uses the same path [SPEC §12, §16].
6. Destroying compute never touches recovery data; for `existing`, "destroy" removes Strange Company from the machines and leaves the machines [SPEC §114].
7. Core infrastructure behavior must be testable deterministically in CI without paid services [SPEC §104 spirit, ADR-0018].

Derived requirements:
- **05-R-1** Discovery is read-only on the node: it never installs packages, writes files or changes configuration. It runs one fixed script over SSH stdin and requires no agent on the node.
- **05-R-2** Every fact used for validation states its source (`discovered`, `operator-attested`) and attested values are reported as warnings, never silently trusted.
- **05-R-3** A machine already running a foreign Kubernetes or container platform is refused unless it carries this installation's marker (resume) — Strange Company never "takes over" an unknown cluster.
- **05-R-4** The operator must confirm the exact node list and host keys before configure touches any host (`existing-nodes` confirmation, plan 50 §14.3 step 8).
- **05-R-5** The lab never needs root on the CI host: VMs run as an unprivileged user with `/dev/kvm` access and user-mode networking.

## 3. Inputs

| Input | Source |
|---|---|
| Node list | `EXISTING_NODES` → `spec.provider.existing.nodes[]` — format `node-01=user@host[:port][/internal_ip],node-02=…` (plan 50 §9.2) |
| Overrides | `EXISTING_NODE_OVERRIDES` — `node-01:disk_class=ssd;node-01:host_key=SHA256:…` → `diskClassOverride`, `hostKeyFingerprint` |
| Node count | `EXPECTED_NODE_COUNT` (must equal the number of entries; PF-09) |
| Cost limit | `MAX_MONTHLY_COST=0` is valid (plan 03 §14.6) |
| SSH key | `SC-SSH-OPERATOR` (or `SC-LAB-SSH` in the lab) |
| Substrate | `infra/profiles/<profile>.yaml` |
| Supported OS list | plan 08 08-R-1: Debian 13, Ubuntu 24.04 LTS, **amd64 only**, kernel ≥ 6.1, cgroup v2, AppArmor enabled |

## 4. Outputs

- `ProviderOutput` with `provider.name: existing`, `product_line: none`, `capabilities` (§7.1), `cost_estimate` all `0` and `complete: true`, `tofu: null`.
- `discovery.json` per node in `.bootstrap/runs/<run-id>/discovery/<node>.json` (raw facts, for diagnosis).
- No OpenTofu state, no remote resources.

## 5. Dependencies

- **Hard:** plan 03 interface (F09), plan 50 engine (F08), plan 02 toolbox (OpenSSH client).
- **Consumers:** plans 07/08 (inventory), 47 (expand), 49 (decommission), 45 (recover onto existing hardware), 04 (reuses the discovery library, §14.2).
- **Lab consumers:** plan 07 (F10 lab suite), plan 08 (baseline tests, port scan), plan 50 (`e2e-lab`), plan 02 (SELinux engine matrix uses a Fedora lab VM).

## 6. Owned state

None durable. The node list is configuration (`bootstrap.env` → committed `bootstrap-config.yaml`), and the normalized result is committed as `inventory.yaml` (plan 03 §4). Lab VMs and their disks are ephemeral (`/tmp` of the CI container), destroyed at the end of every run.

## 7. External interfaces

### 7.1 Capabilities

`provisions_compute: false`, `paid: false`, `reversible_within_hours: true`, `supports_expand: true`, `supports_destroy_compute: false` (destroy = decommission, plan 49 §14.4), `host_key_source: operator-supplied` when every node has `hostKeyFingerprint`, otherwise `tofu` (trust on first use, confirmed by the operator in the `existing-nodes` confirmation). The lab reports `cloud-init` (the harness generates host keys and injects them; §14.6).

### 7.2 Normalization rules

| ProviderOutput field | Rule |
|---|---|
| `name` | the key in `EXISTING_NODES` (`node-NN`); names are never reused (plan 03 §8) |
| `provider_id` | `existing:<name>` |
| `machine_class` | `existing` (`lab` for lab-labeled substrates) |
| `roles` | `[server]` for the first `kubernetes.servers` nodes in name order, otherwise `[agent]` (MVP profiles have servers = count) |
| `addresses.internal_ip` | `/internal_ip` from `EXISTING_NODES` if given; else the source address of `ip -j route get 1.1.1.1` on the node (verify that the address is configured on an interface) |
| `addresses.public_ipv4` / `public_ipv6` | the node's global-scope addresses that are not RFC 1918, RFC 6598 (CGNAT), loopback, link-local or ULA; `null` if none |
| `addresses.external_ip` | the SSH host if it is a public IP literal different from `internal_ip` (NAT, port-forwarded servers); otherwise `null` |
| `ssh` | from `EXISTING_NODES` (`user` = the initial user); `admin_user` = `null` until configure's admin transition sets `sc-admin` (BOOT-8); `host_key_fingerprints` = keys captured and pinned (§14.4) |
| `hardware.*` | discovery (§14.2, §14.3) |
| `os` | `/etc/os-release` `ID`, `VERSION_ID`, `PRETTY_NAME`; `uname -r` |
| `location` | `region: existing` (`lab` in the lab), `failure_domain` = same; operators may not set zones in MVP (single failure domain; documented limitation for small-ha on one hypervisor) |
| `state` | `running` if SSH works, else the phase fails (no partial output) |

## 8. Internal interfaces

- Go package `src/internal/provider/existing` (plan 03 operations) and the shared **discovery library** `src/internal/provider/discovery`, also used by plan 04 after delivery.
- `Plan()` = discovery + validation + host-key capture; it produces a no-op plan whose digest covers the node list, host-key fingerprints and the discovered facts used for validation. `Apply()` is a no-op. `Inventory()` = the plan's normalized output. `OrphanScan()` returns `[]`. `Expand()` = `Plan()` with the new entries. `DestroyCompute()` returns `ErrNotSupported`; plan 49 calls `Decommission(nodes)` (`infra/ansible/decommission.yml`, plans 07/08) instead.
- Lab: `sc-lab` (Go, `src/cmd/sc-lab`, ci image only) with subcommands `up`, `status`, `down`, `env` (§14.6).

## 9. Configuration

| Knob | Default | Source | Notes |
|---|---|---|---|
| SSH connect timeout | 10 s | constant | `ConnectTimeout=10`, `BatchMode=yes` |
| Discovery timeout per node | 60 s | `infra/bootstrap/settings.yaml` `existing.discoveryTimeout` | |
| Parallelism | all nodes concurrently (≤ 9) | constant | |
| Required free space on the rancher filesystem | 60 GiB (self-hosting-minimum), 15 GiB (lab) | substrate-derived: `0.8 × minimumPerNode.storage.size` | warns below `storage.size` |
| Lab VM shapes, images, network | `infra/lab/lab.yaml` | §14.6 | PR |
| Lab image pins | `versions/lab-images.lock.yaml` (plan 01 §6.3) | Renovate | |

## 10. Secrets

- `SC-SSH-OPERATOR` — consumed (public key must already be in the node user's `authorized_keys`; `existing` never injects keys).
- `SC-LAB-SSH` — registered by plan 35 (ephemeral, TTL 6 h): ed25519 key generated by `sc-lab up` per run into `/run/sc/lab/<run-id>/id_ed25519` (tmpfs), injected via cloud-init, destroyed with the container. Never written to the repository or uploaded.
- Lab host keys are generated per run the same way (not a secret class: they authenticate disposable VMs; stored only in the run tmpfs).
- No provider credential exists for `existing`.

## 11. Storage

None on the operator side beyond run artifacts. Lab: per VM a qcow2 overlay (≤ 20 GiB sparse, typically 3–6 GiB used) on `/tmp/sc-lab/<run-id>/` inside the ci container, plus the cached base images in `$SC_REPO_ROOT/.cache/lab-images/` (verified against the lock on every use).

## 12. Resource requirements

- Discovery: < 30 s per node, negligible load.
- Lab-small: 1 VM, 2 vCPU / 4 GiB / 20 GiB. Runs on a standard 4-vCPU / 16 GiB GitHub-hosted Linux runner with KVM.
- Lab-ha: 3 VMs, 2 vCPU / 4 GiB / 20 GiB each; requires a runner with ≥ 8 vCPU / 24 GiB (larger hosted runner or a self-hosted lab host; plan 52 schedules it nightly).

## 13. Security boundary

- `existing` holds no spending authority. Its risk is **configuring the wrong machine**: mitigated by the typed `existing-nodes` confirmation showing every host, its discovered OS/CPU/memory and host-key fingerprint (05-R-4), and by host-key pinning (a changed key later is fatal, `HOST_KEY_MISMATCH`).
- The node user must have passwordless `sudo` (configure needs root); discovery verifies it (`sudo -n true`) but runs nothing as root.
- Lab VMs run in the ci container as the container user with `/dev/kvm` only (`./bootstrap exec --image ci --kvm --network host`); no `CAP_NET_ADMIN`, no bridges or tap devices, no privileged container (05-R-5). The lab's port forwards bind to `127.0.0.1` only.

## 14. Happy path

### 14.1 Sequence (plan 50 phases)

1. `preflight` PF-07 (`existing`): every node reachable over SSH with the configured key within 10 s; `sudo -n true` succeeds.
2. `plan.compute-plan`: discovery (§14.2) → normalization (§7.2) → validation (§14.5) → host-key capture (§14.4).
3. `plan.confirm`: the footprint (plan 03 §14.7) shows `0 paid resources`, the node table and the warning "configure will take over these hosts (k3s, firewall, sysctls)"; the operator types `<cluster>`; the confirmation `existing-nodes` records the plan digest.
4. `provision`: re-runs discovery, recomputes the digest and compares it with the confirmation (`PLAN_DIGEST_MISMATCH` if anything changed, e.g. a different machine answers on that address), writes `inventory.yaml`.

### 14.2 Discovery (shared library)

One SSH session per node, as `admin_user` if recorded in `inventory.yaml`, else trying `sc-admin` first and then the `EXISTING_NODES` user (plan 08's `AllowGroups sc-admin` locks the initial user out after configure; BOOT-8): `ssh -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=<yes|accept-new during capture> <user>@<host> -p <port> 'sh -s'` with a fixed POSIX script on stdin (embedded in the binary, versioned). The script prints one JSON document assembled from read-only commands:

| Fact | Command (non-root) |
|---|---|
| CPU count | `nproc --all`; model from `/proc/cpuinfo` |
| Memory | `MemTotal` from `/proc/meminfo` (bytes) |
| Block devices | `lsblk -J -b -o NAME,KNAME,PKNAME,SIZE,ROTA,TYPE,TRAN,MODEL,MOUNTPOINTS` |
| Rancher filesystem | `findmnt -J -T /var/lib/rancher` (falls back to `/var/lib`, `/`) → source device, fstype, size, available |
| OS / kernel / arch | `/etc/os-release`, `uname -r`, `uname -m` |
| Virtualization | `systemd-detect-virt` (`none` = bare metal) |
| cgroup | `stat -fc %T /sys/fs/cgroup` (`cgroup2fs` required) |
| AppArmor | `cat /sys/module/apparmor/parameters/enabled` |
| WireGuard availability | `/sys/module/wireguard` exists, or `modinfo wireguard` succeeds |
| Addresses / routes | `ip -j addr`, `ip -j route get 1.1.1.1` |
| Time sync | `timedatectl show -p NTPSynchronized --value` |
| Swap | `swapon --show=SIZE --bytes --noheadings` |
| Listening ports | `ss -Hltnu` → conflicts on 22 (expected), 6443, 2379, 2380, 2381, 10250, 51820/udp, 51821/udp, 80, 443 |
| Existing platforms | presence of `/usr/local/bin/k3s`, `/etc/rancher`, `kubelet`, `dockerd`, `containerd` processes or units; `/etc/strange-company/install-uid` content |
| sudo | `sudo -n true` exit code (no command is run as root) |

Required commands on the node: `sh`, `nproc`, `lsblk`, `findmnt`, `ip`, `ss`, `uname`, `stat`, `timedatectl` (all present on Debian 13 and Ubuntu 24.04 cloud and server images). A missing command yields `DISCOVERY_TOOL_MISSING:<cmd>` (fail) rather than a guessed value.

### 14.3 Disk classification

For the device backing the rancher filesystem (walk `PKNAME` to the whole disk):
1. `TRAN=nvme` or `KNAME` starting with `nvme` → `nvme`, `class_source: discovered`.
2. Else, bare metal (`virtualization: none`) and `ROTA=0` → `ssd` discovered; `ROTA=1` → `hdd` discovered.
3. Else (virtualized): `ROTA=0` → `ssd` discovered (the hypervisor declared it non-rotational, e.g. `scsi-hd rotation_rate=1`); `ROTA=1` → `unknown` (virtio disks report `ROTA=1` regardless of the backing medium).
4. If `diskClassOverride` is set: class = override, `class_source: operator-attested`, finding `DISK_CLASS_ATTESTED` (warn). An override is honored only when the discovered class is `unknown`; overriding a discovered `hdd` fails with `DISK_CLASS_OVERRIDE_CONFLICT`.
5. `unknown` without override fails validation with `DISK_CLASS_UNVERIFIED` and the remediation "confirm the medium with your provider and set `EXISTING_NODE_OVERRIDES=<node>:disk_class=ssd|nvme`".

The class is a declaration; plan 08's configure-time `fio` fdatasync check (p99 ≤ 10 ms, fail > 25 ms with `NODE_DISK_TOO_SLOW`; plan 07 storage section, plan 08 §14.2) measures the real medium and is the backstop for a wrong attestation.

### 14.4 Host keys

- With `hostKeyFingerprint` set: `StrictHostKeyChecking=yes` against a temporary known-hosts file built from `ssh-keyscan` output **only if** one scanned key's SHA256 fingerprint equals the attested value; otherwise fail `HOST_KEY_MISMATCH` before any login.
- Without it: `ssh-keyscan -t ed25519,ecdsa,rsa` once, fingerprints shown in the confirmation footprint (trust on first use, recorded in the report).
- Pinned keys are written to `.bootstrap/known_hosts` and `inventory.yaml` `ssh.host_key_fingerprints`. All later SSH uses `StrictHostKeyChecking=yes`.

### 14.5 Validation (beyond plan 03 §14.3 capacity checks)

| Check | Rule | Code (severity) |
|---|---|---|
| Capacity | plan 03 §14.3 (0.92 tolerance; CPU, memory, disk size, disk class rank) against SPEC v1.2 (minimum 100 GiB disk, preferred 200 GiB NVMe, CD-25); 8 GiB memory passes as `constrained` (CD-23, warn) | `NODE_BELOW_MINIMUM` (fail), below preferred (warn) |
| OS | `ID`/`VERSION_ID` ∈ plan 08 supported list | `NODE_OS_UNSUPPORTED` (fail) |
| Architecture | `amd64` (plan 08 08-R-1) | `NODE_ARCH_UNSUPPORTED` (fail) |
| Kernel | ≥ 6.1 (plan 08 08-R-1) | `NODE_KERNEL_TOO_OLD` (fail) |
| AppArmor | enabled (`/sys/module/apparmor/parameters/enabled` = `Y`; plan 08 08-R-1) | `NODE_APPARMOR_DISABLED` (fail) |
| Size class | all nodes in the same capacity class (plan 07 Q-07-04, plan 51) | `EXISTING_MIXED_SIZE_CLASS` (fail) |
| cgroup v2 | required by k3s on these distributions | `NODE_CGROUP_V1` (fail) |
| WireGuard | module available (ADR-0011) | `NODE_NO_WIREGUARD` (fail) |
| sudo | passwordless | `NODE_SUDO_REQUIRED` (fail) |
| Foreign platform | k3s/kubelet/dockerd present and no matching `/etc/strange-company/install-uid` | `NODE_HAS_FOREIGN_PLATFORM` (fail) (05-R-3) |
| Marker of another install | `/etc/strange-company/install-uid` present and ≠ this `install_uid` | `NODE_BELONGS_TO_OTHER_INSTALL` (fail) |
| Port conflicts | any listener on the k3s/etcd/WireGuard/ingress ports not owned by a matching install | `NODE_PORT_CONFLICT:<port>` (fail) |
| Free space | rancher filesystem available ≥ 0.8 × `storage.size` | `NODE_DISK_FREE_LOW` (fail); between 0.8× and 1.0× (warn) |
| Filesystem type | `ext4` or `xfs` (plan 08) | `NODE_FS_UNSUPPORTED` (fail) |
| Time sync | `NTPSynchronized=yes` | `NODE_TIME_UNSYNCED` (warn; plan 08 configures chrony/timesyncd) |
| Swap | reported only (plan 08 disables it) | `NODE_SWAP_ENABLED` (info) |
| Duplicate machine | two entries resolve to the same `/etc/machine-id` | `EXISTING_DUPLICATE_NODE` (fail) |
| Address sanity | `internal_ip` configured on the node; internal IPs unique across nodes | `NODE_INTERNAL_IP_INVALID` (fail) |

The node marker `/etc/strange-company/install-uid` is written by plan 08's baseline role at configure (interface assumption: plan 08) and removed by decommission. It lets a resumed run accept nodes it already configured.

### 14.6 Lab substrate (ADR-0105)

**Purpose:** run the real `existing` → configure path (and, once available, seed) in CI and on developer machines with no cloud account.

**Host requirements:** Linux with `/dev/kvm` accessible to the invoking user. GitHub-hosted Linux runners expose KVM on standard runners (verify at implementation; the workflow adds the udev rule `KERNEL=="kvm", GROUP="kvm", MODE="0666"` and reloads udev, as GitHub documents for Android emulators (verify)). Without KVM, `sc-lab up` fails with `LAB_NO_KVM` (TCG emulation is not supported: too slow for etcd timeouts).

**Execution:** everything runs inside one ci container started with `./bootstrap exec --image ci --kvm --network host -- make e2e-lab …` (plan 02 §7.1). The harness (`test/e2e/lab/run`) calls `sc-lab up`, then calls the in-image `sc-bootstrap` directly (the wrapper is tested separately by plan 02), then `sc-lab down` in a trap. QEMU processes live and die with the container.

**`infra/lab/lab.yaml`** (`kind: LabSpec`; its schema is the `LabSpec` `$defs` entry of `contracts/version-locks.schema.yaml`, next to `LabImageLock`):

| Field | lab-small | lab-ha |
|---|---|---|
| VMs | `node-01` | `node-01..03` |
| vCPU / memory / disk | 2 / 4 GiB / 20 GiB | 2 / 4 GiB / 20 GiB each |
| Disk device | `virtio-blk` (exercises the attested path: `diskClassOverride: ssd`) | `virtio-scsi` + `scsi-hd,rotation_rate=1` (exercises the discovered-`ssd` path) |
| Images (matrix) | Debian 13 genericcloud, Ubuntu 24.04 cloud image | same |

**Networking (unprivileged):**
- NIC0: QEMU user-mode (`-netdev user,id=n0,hostfwd=tcp:127.0.0.1:220N-:22`) for SSH from the toolbox and NAT egress to the internet (package mirrors, k3s binary download).
- NIC1: a private L2 segment shared by all lab VMs without privileges: `-netdev socket,id=n1,mcast=230.0.0.1:<port>,localaddr=127.0.0.1` (verify on the pinned QEMU; alternative `-netdev dgram` with a multicast remote), static addresses `10.77.0.1N/24` set by cloud-init. This is the `internal_ip` k3s and flannel WireGuard use, so multi-node lab clusters exercise real inter-node WireGuard (ADR-0011).
- `<port>` is derived from the run id, so concurrent lab runs on one host do not share a segment.

**Cloud-init (NoCloud seed ISO built with `cloud-localds`):** user `debian`/`ubuntu` with the run's `SC-LAB-SSH` public key; pre-generated ed25519 host key (`ssh_keys`), so `host_key_source: cloud-init` and fingerprints are known before boot; NIC1 static IP; hostname `lab-<run-id>-node-0N`; `/etc/hosts` entry for the lab S3 host (below); no password, `ssh_pwauth: false`.

**Lab S3:** the SeaweedFS `s3-emulator` (`weed server -s3`, ADR-0205/CD-12) inside the ci container (`127.0.0.1:9000`, TLS with a run-local CA; certificate SANs `IP:127.0.0.1`, `IP:10.0.2.2`, `DNS:s3.lab.internal`). The toolbox side uses `S3_ENDPOINT=https://127.0.0.1:9000`; VMs reach the same server through QEMU's host alias `10.0.2.2`. In-cluster consumers therefore need a different endpoint (`https://10.0.2.2:9000`) and the lab CA: `sc-lab env` sets `SC_LAB_S3_INCLUSTER_ENDPOINT` and `SC_LAB_S3_CA_FILE` (test-mode-only runtime variables; interface assumption on plans 14/50, §25 OQ-05-3).

**`sc-lab` commands:**
- `sc-lab up --substrate lab-small|lab-ha --os debian-13|ubuntu-24.04 --run-id <id>`: verify `/dev/kvm`; fetch (or reuse from `.cache/lab-images/`) the pinned image and verify its checksum from `versions/lab-images.lock.yaml`; create qcow2 overlays; generate `SC-LAB-SSH`, host keys and seed ISOs; start QEMU (`-enable-kvm -cpu host -machine q35 -nographic -serial file:/tmp/sc-lab/<id>/node-0N.console`); wait until SSH answers (≤ 180 s per VM); start the `s3-emulator`; write `/run/sc/lab/<id>/bootstrap.env` and print its path.
- `sc-lab env --run-id <id>`: prints the generated `bootstrap.env` (non-secret values plus file paths).
- `sc-lab status`: VM pids, SSH reachability, console tail.
- `sc-lab down --run-id <id>`: kill QEMU and the `s3-emulator`, delete overlays and tmpfs material. Idempotent.

**Generated lab `bootstrap.env` (illustrative):**

```sh
CLUSTER_NAME=lab-ci-4711
BASE_DOMAIN=lab.example.net
ADMIN_EMAIL=ci@example.net
PROFILE=lab-small
PROVIDER=existing
EXPECTED_NODE_COUNT=1
MAX_MONTHLY_COST=0
EXISTING_NODES=node-01=debian@127.0.0.1:2201/10.77.0.11
EXISTING_NODE_OVERRIDES=node-01:disk_class=ssd;node-01:host_key=SHA256:<from the generated key>
SSH_PRIVATE_KEY_PATH=/run/sc/lab/4711/id_ed25519
INGRESS_MODE=direct
S3_ENDPOINT=https://127.0.0.1:9000
S3_REGION=us-east-1
S3_FORCE_PATH_STYLE=true
# Cloudflare, GitHub, notification and model values are fixed fakes; see §14.7
```

### 14.7 Test-mode behavior required from plan 50 (interface assumption)

Lab runs set `SC_TEST_MODE=1`, which plan 50 refuses for paid providers (50-AC-7). In test mode with a lab-labeled substrate, sc-bootstrap must: (1) report PF-15, PF-16, PF-17, PF-18, PF-19, PF-20 as `skip` with reason `lab` instead of calling Cloudflare, GitHub, the notification transport or model APIs; (2) honor `SC_TEST_STOP_AFTER=<phase>` (e.g. `configure`, later `seed`) by ending the pipeline successfully after that phase with installation state unchanged beyond it; (3) accept the passphrase from `SC_RECOVERY_PASSPHRASE_FILE`. These are listed for plan 50 in §25.

### 14.8 Expand and decommission

- **Expand:** add `node-02`, `node-03` to `EXISTING_NODES`, set `EXPECTED_NODE_COUNT=3`, run `./bootstrap expand --profile small-ha`. Discovery runs on all nodes (existing nodes must still match their pinned host keys and install marker); the confirmation token is `expand <cluster> <D8>` (plan 03 §14.7).
- **Removing a node from the list** is never done by editing `EXISTING_NODES` alone: plan 47's `remove-node` procedure decommissions it first; a node that disappears from the list while present in `inventory.yaml` fails PF-09 with `EXISTING_NODE_REMOVED_WITHOUT_PROCEDURE`.
- **Decommission (plan 49):** Ansible `infra/ansible/decommission.yml` (k3s uninstall, firewall back to SSH-only, marker removed). The machines stay.

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Node unreachable / auth refused | PF-07 | nothing touched | exit 3 | bootstrap-preflight-failure.md#credentials |
| Host key differs from attestation or pin | §14.4 | nothing touched | `HOST_KEY_MISMATCH` exit 3 | bootstrap-preflight-failure.md#ssh |
| Node below minimum / unsupported OS / no WireGuard / cgroup v1 | §14.5 | nothing touched | exit 3 with codes | bootstrap-preflight-failure.md#node-size |
| Disk class unverifiable | §14.3 | nothing touched | `DISK_CLASS_UNVERIFIED` exit 3 | bootstrap-preflight-failure.md#node-size |
| Foreign platform / other install's marker | §14.5 | nothing touched | exit 3 | bootstrap-preflight-failure.md#node-size |
| Different machine answers at confirm vs provision | digest recompute | nothing touched | `PLAN_DIGEST_MISMATCH` exit 4 | rerun and re-confirm |
| Discovery script command missing | §14.2 | nothing touched | `DISCOVERY_TOOL_MISSING:<cmd>` | bootstrap-preflight-failure.md#node-size |
| Node lost after install | `SCNodeDown` (plan 42) | cluster degraded/down | none | lost-node.md (plan 47), provider-outage.md |
| Lab: no KVM / image checksum mismatch / VM boot timeout | `sc-lab up` | lab run fails | `LAB_NO_KVM`, `LAB_IMAGE_CHECKSUM_MISMATCH`, `LAB_BOOT_TIMEOUT` (console log attached) | — |

## 16. Retry behavior

SSH connection failures during discovery are transient (3 attempts, 5 s/15 s/30 s) — useful right after a reboot. Validation failures are permanent. Lab boot waits poll every 5 s up to 180 s; image downloads retry 3 times.

## 17. Idempotency behavior

Discovery is read-only (05-R-1); repeated plans over unchanged nodes give the same digest (facts that legitimately change between runs — free space, swap, time-sync status — are excluded from the digest and re-checked at provision). `Apply` is a no-op. Lab `up` with an existing run id refuses (`LAB_RUN_EXISTS`); `down` is idempotent.

## 18. Bootstrap behavior

Used by `./bootstrap` when `PROVIDER=existing`, including OVH fallback (c). No S3 compute state exists for `existing` (the storage module still runs; plan 06).

## 19. Upgrade behavior

- The discovery script is versioned with sc-bootstrap; new checks start as `warn` for one release (plan 50 §19 rule for verify checks applies by analogy) unless they protect against data loss.
- Lab image pins move by Renovate PRs; the lab suite re-runs for both OS images before merge (plan 52 schedules).
- OS support changes are plan 08 decisions; this plan's `NODE_OS_UNSUPPORTED` list follows them.

## 20. Recovery behavior

- **Total cluster loss on existing hardware:** `./bootstrap recover` with the same or replacement machines (plan 45). Reinstalled machines have new host keys: the operator updates `EXISTING_NODE_OVERRIDES` host keys or accepts TOFU again; a stale marker on a surviving machine is accepted only if it equals this `install_uid`.
- **Replacing a dead machine:** a new name (`node-04`), never reuse (plan 03 §8).

## 21. Observability

Run artifacts (`discovery/<node>.json`, footprint, provider output). The report lists each node's attested fields. In-cluster observability is plans 38/42. Lab: QEMU console logs and `s3-emulator` logs are attached to the CI job artifacts on failure (`out/e2e-lab/<run-id>/`).

## 22. Testing strategy

- **Unit:** the discovery parser against recorded JSON from Debian 13, Ubuntu 24.04 (cloud and bare-metal samples), a VM with virtio and with `scsi-hd rotation_rate=1`, an NVMe host, a host with k3s already present, cgroup v1; normalization (§7.2) for public, private, CGNAT and NAT-forwarded addresses; every §14.5 code; `EXISTING_NODES`/`EXISTING_NODE_OVERRIDES` parser (golden cases including malformed entries, duplicate names, port defaults).
- **Script safety test:** the discovery script runs in a container with a read-only root filesystem and `strace -f -e trace=openat,unlink,rename,write` (ci image), asserting no write outside `/dev/null`/stdout (05-R-1).
- **Lab (`make e2e-lab`):** lab-small × {Debian 13, Ubuntu 24.04}: `./bootstrap` through `configure` (`SC_TEST_STOP_AFTER=configure`), then plan 07/08 assertions; lab-ha nightly: install 1 node then expand to 3 (H01 rehearsal, plan 07).
- **Fedora SELinux lab VM** (plan 02 engine matrix): a lab VM with Fedora runs rootless Podman and the toolbox wrapper tests; it is a test target, not a supported cluster OS.

## 23. Acceptance criteria

### GATE-F09 (co-owned with plans 03, 04)

- **05-AC-1** Parser golden tests: every case in `test/fixtures/existing/env/*.env` produces the expected `spec.provider.existing` JSON, and malformed inputs (`node-1=…`, duplicate `node-01`, `port=0`, `host_key=MD5:…`) fail with field-level errors (exit 2 at preflight).
- **05-AC-2** Discovery fixtures: each recorded node JSON yields the expected `ProviderOutput.nodes[0]` (validated against `provider.schema.yaml`) and the expected finding codes; the virtio fixture without override yields `DISK_CLASS_UNVERIFIED`, with override `ssd` yields `class_source: operator-attested` and `DISK_CLASS_ATTESTED` (warn).
- **05-AC-3** Discovery is read-only: the script-safety test reports zero writes (05-R-1).
- **05-AC-4** With `PROVIDER=existing`, the `plan` footprint shows `0 paid resources` and each node's host-key fingerprint; `--non-interactive` without confirmation exits 5; the journal records an `existing-nodes` confirmation whose `subject_digest` equals the plan digest after a correct `<cluster>` token.
- **05-AC-5** `MAX_MONTHLY_COST=0` passes the cost check and `provider-output.json` has `cost_estimate.monthly_total == "0"` and `complete: true`.
- **05-AC-6** Lab smoke: `./bootstrap exec --image ci --kvm --network host -- make e2e-lab LAB_SUBSTRATE=lab-small LAB_OS=debian-13 LAB_STOP_AFTER=provision` on a GitHub-hosted Linux runner exits 0; `provider-output.json` shows `host_key_source: cloud-init`, `internal_ip: 10.77.0.11`, `ssh.port: 2201`; `sc-lab down` leaves no `qemu-system` process (`pgrep -c qemu-system` = 0).
- **05-AC-7** Lab image integrity: a fixture lock entry with a wrong checksum makes `sc-lab up` fail with `LAB_IMAGE_CHECKSUM_MISMATCH` and no VM starts.
- **05-AC-8** KVM availability re-verified: the CI job log of 05-AC-6 contains `sc-lab: kvm ok` (the `/dev/kvm` open succeeded) on the runner type plan 52 configures; a runner without `/dev/kvm` yields `LAB_NO_KVM` (exit non-zero), never a silent TCG fallback.

### GATE-P02 (co-owned with plans 03, 04; applies when the reference install uses `PROVIDER=existing`)

- **05-AC-20** A real install on operator-supplied machines (or a manually ordered OVH VPS, fallback (c)): `provider-output.json` validates; every node `meets_minimum: true`; `inventory.yaml` committed; no OpenTofu compute state object exists (`aws --profile sc-admin s3api head-object --key <cluster>/compute/terraform.tfstate` → 404).
- **05-AC-21** Safety refusals on a lab VM: (a) with k3s pre-installed by hand → `NODE_HAS_FOREIGN_PLATFORM`; (b) with a different host key than attested → `HOST_KEY_MISMATCH`; (c) a cgroup-v1 kernel fixture node → `NODE_CGROUP_V1`. Each run exits 3 and the VM's `/etc` checksum (`find /etc -type f -exec sha256sum {} +`) is unchanged.

### GATE-P03 (contribution; needs configure, plans 07/08 — GC-01)

- **05-AC-22** Lab end-to-end through configure for both OS images: the run exits 0 with `SC_TEST_STOP_AFTER=configure`, `kubectl get nodes` (through `./bootstrap exec`-equivalent tunnel) shows `node-01 Ready`, and a second run reports provision and configure `skipped (already done)` (plan 07 07-AC-2 asserts `changed=0`).

### GATE-H01 (contribution; expansion rehearsal — GC-01)

- **05-AC-23** Lab expand (nightly, lab-ha): 1 → 3 nodes via `./bootstrap expand --profile lab-ha`, all three Ready, and node-to-node traffic on NIC1 is WireGuard (plan 07/ADR-0011 check: `tcpdump`-free assertion via `wg show flannel-wg` peers = 2 on each node).

## 24. Explicit non-goals

- Installing an OS, partitioning disks, or injecting SSH keys on existing machines (the operator prepares them).
- Agent-only nodes, Windows, non-systemd distributions.
- Zones/failure-domain modeling for existing machines in MVP.
- A lab that runs without KVM, on macOS hosts, or with bridged/tap networking.
- Using the lab for performance or memory-budget gates (V-30 runs only on real nodes).

## 25. Open questions

| # | Question | Blocking | Proposed resolution | Owner |
|---|---|---|---|---|
| OQ-05-1 | Does the pinned QEMU support `-netdev socket,mcast=…,localaddr=127.0.0.1` inside a container on GitHub runners (multicast on loopback)? | no | Verify at F09; fallback `-netdev dgram` (QEMU ≥ 7.2) with a multicast remote, or a single lab VM per run plus a 3-VM lab only on the self-hosted lab host. | 05 |
| OQ-05-2 | GitHub-hosted runner KVM availability and the udev step. | no | 05-AC-8 re-verifies on every run; plan 52 pins the runner label. | 05 / 52 |
| OQ-05-3 | In-cluster consumers in the lab need a different S3 endpoint (`https://10.0.2.2:9000`) and a custom CA. | no (F09/P02 stop at configure) | Plan 14 adds optional `S3_CA_BUNDLE` support and plan 50 a test-mode `SC_LAB_S3_INCLUSTER_ENDPOINT` override written into `cluster-vars`; required before `e2e-lab` extends to `seed`. | 14 / 50 |
| OQ-05-4 | Plan 50 must implement the test-mode behaviors in §14.7 (`skip(lab)` for external-service PF checks, `SC_TEST_STOP_AFTER`). | no | Add to plan 50 at consolidation (runtime-only variables, refused with paid providers). | 50 |
| OQ-05-5 | Plan 08 must write and remove the node marker `/etc/strange-company/install-uid` (§14.5). | no | Assumed; plan 08 confirms. | 08 |
