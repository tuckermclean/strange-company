# Existing-nodes discovery fixtures (05-AC-2, plan 05 section 14.2)

source: derived, not recorded. No machine of these kinds was available (CD-71 applies the same rule to the OVH
fixtures). `facts.json` is the output format of the discovery script (`discovery.Script()`); the values under
`lsblk`, `findmnt`, `ip_addr` and `ip_route_get` follow the documented JSON output of `lsblk -J -b`,
`findmnt -J`, `ip -j addr` and `ip -j route get` (util-linux 2.39 to 2.41, iproute2 6.x); `meminfo`, `os_release`
and `ss` are the verbatim text of `/proc/meminfo`, `/etc/os-release` and `ss -Hltnu`. Every value is invented
(documentation addresses 203.0.113.0/24, 198.51.100.0/24, 2001:db8::/32). `hostkeys.txt` is `ssh-keyscan -t
ed25519,ecdsa,rsa` output; the keys are real, freshly generated public keys whose private halves were discarded,
and the SHA256 fingerprints in `expected.json` were computed from them with `ssh-keygen -lf`.

## facts.json (script version 1)

| Key | Content |
|---|---|
| `script_version` | integer, 1 |
| `nproc`, `cpu_model` | `nproc --all`; model name from `/proc/cpuinfo` |
| `meminfo` | text of `/proc/meminfo` (MemTotal in kB) |
| `lsblk` | `lsblk -J -b -o NAME,KNAME,PKNAME,SIZE,ROTA,TYPE,TRAN,MODEL,MOUNTPOINTS`. `size` and `rota` are numbers and booleans (util-linux 2.41) or strings (`"480103981056"`, `"0"`; older); the parser accepts both |
| `findmnt` | `findmnt -J -b -T /var/lib/rancher` (falling back to `/var/lib`, `/`), `size` and `avail` as strings |
| `os_release` | text of `/etc/os-release` |
| `kernel`, `arch` | `uname -r`, `uname -m` (`x86_64`, `aarch64`) |
| `virt` | `systemd-detect-virt` (`none` = bare metal) |
| `cgroup_fs` | `stat -fc %T /sys/fs/cgroup` |
| `apparmor` | `/sys/module/apparmor/parameters/enabled` (`Y`, `N` or empty) |
| `wireguard` | boolean: `/sys/module/wireguard` exists or `modinfo wireguard` succeeds |
| `ntp_synchronized` | `timedatectl show -p NTPSynchronized --value` (`yes`/`no`) |
| `swap` | `swapon --show=SIZE --bytes --noheadings` (empty = no swap) |
| `ip_addr`, `ip_route_get` | `ip -j addr`, `ip -j route get 1.1.1.1` |
| `ss` | `ss -Hltnu` |
| `platforms` | names found of `k3s`, `kubelet`, `dockerd`, `containerd` (binary, unit or process) |
| `install_uid` | content of `/etc/strange-company/install-uid` (empty if absent) |
| `sudo_ok` | boolean: `sudo -n true` succeeded |
| `machine_id` | `/etc/machine-id` |
| `missing_tools` | commands from the required list that the node lacks; their facts are absent |

## Cases

Each directory has `facts.json`, `hostkeys.txt`, `input.json` (the `EXISTING_NODES` entry with its overrides),
and `expected.json`. `expected.json` holds `node` (a partial `ProviderOutput.nodes[0]`: every key listed must
match, keys not listed are not checked; the order of `host_key_fingerprints` does not matter), `findings` (the
code and severity pairs that must be present), `exact` (when true, no other finding with a code of the
plan 05 section 14.5 list or `DISK_CLASS_*` may be present; findings with other codes, such as capacity warnings,
are not checked) and `absent` (codes that must not occur).

| Directory | Models |
|---|---|
| `debian13-baremetal-nvme` | Debian 13 bare metal, NVMe, public IPv4 and IPv6 on the interface, no findings |
| `ubuntu2404-baremetal-ssd` | Ubuntu 24.04 bare metal, SATA SSD (string-typed lsblk), private address behind a port-forwarded public SSH endpoint, swap on |
| `debian13-virtio-unverified` | KVM guest with a virtio-blk disk (`ROTA=1`, no `TRAN`): class `unknown`, `DISK_CLASS_UNVERIFIED` |
| `debian13-virtio-attested` | the same machine with `disk_class=ssd` attested: `operator-attested`, `DISK_CLASS_ATTESTED` (warn) |
| `ubuntu2404-cloud-scsi` | lab-style guest: `scsi-hd,rotation_rate=1` (`ROTA=0`, class `ssd` discovered), a user-mode NIC and a private NIC, `internal_ip` given |
| `debian13-k3s-present` | a host that already runs k3s: `NODE_HAS_FOREIGN_PLATFORM`, `NODE_PORT_CONFLICT` |
| `ubuntu2404-cgroup-v1` | `/sys/fs/cgroup` is tmpfs: `NODE_CGROUP_V1` |
