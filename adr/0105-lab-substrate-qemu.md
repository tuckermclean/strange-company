# ADR-0105: Lab substrate — unprivileged QEMU/KVM VMs from pinned cloud images, presented to the `existing` provider

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 05

## Context
Node baseline, k3s and the configure/seed path (F10, F08, P03 rehearsal) need real Linux machines with systemd, kernels, firewalls and WireGuard. Paid VPSes for every CI run cost money and need credentials in CI; containers (k3d, kind) do not exercise the host-level Ansible roles. SPEC §104 requires deterministic CI without paid model APIs, and ADR-0018 runs Phase-1 gates on GitHub Actions.

## Decision
- CI and developers create **local QEMU/KVM VMs** from pinned Debian 13 and Ubuntu 24.04 cloud images (`versions/lab-images.lock.yaml`), configured by a NoCloud seed (SSH key `SC-LAB-SSH`, pre-generated host keys, static internal address).
- The VMs are presented to the **`existing` provider** exactly like operator machines (`EXISTING_NODES=node-01=debian@127.0.0.1:2201/10.77.0.11`), so the code under test is the production code path.
- Everything runs **unprivileged** inside the ci image: `/dev/kvm` only, QEMU user-mode networking for SSH forwards and egress, and an unprivileged multicast-socket L2 segment between VMs for the internal network (WireGuard between lab nodes is real). No bridges, taps, `CAP_NET_ADMIN` or privileged containers.
- The lab S3 is the SeaweedFS `s3-emulator` (ADR-0205) in the same container (TLS with a run-local CA).
- Lab substrates carry `strange.company/lab: "true"` and are refused with paid providers; test-mode-only behaviors (skipping external-service preflight checks, stopping after a phase) require `SC_TEST_MODE=1`.

## Consequences
- GitHub-hosted runners with KVM can run lab-small; lab-ha (three VMs) needs a larger or self-hosted runner.
- The lab cannot validate memory budgets or public edge behavior; those remain P12 gates on real nodes.
- Multicast-on-loopback support in QEMU must be verified; a `dgram` netdev is the fallback.

## Alternatives rejected
- Vagrant/libvirt: requires a privileged daemon and bridges.
- Firecracker: different kernel/boot model than cloud images.
- Testing Ansible roles only in containers (molecule-docker): no real systemd/kernel/firewall behavior.
- Paid VPS per CI run: cost and credentials in CI.

## Verification
F09 gate (plan 05): 05-AC-6 (lab-small provider output on a hosted runner), 05-AC-7 (image checksum enforcement), 05-AC-8 (KVM re-verified, no TCG fallback); P02/F10 rehearsals 05-AC-22, 05-AC-23.
