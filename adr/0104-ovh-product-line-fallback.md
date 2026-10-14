# ADR-0104: OVH product line — VPS via `ovh_vps` by default, Public Cloud fallback, manual order + `existing` as last resort; decided by a recorded spike

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 04

## Context
SPEC §13 names `ovh` as an initial provider and SPEC §11 targets an inexpensive VPS. ADR-0020 makes the SPEC "preferred" memory (12 GiB) the default; SPEC v1.2 sets the disk minimum to 100 GiB and the preferred disk to 200 GiB NVMe (CD-25). OVHcloud's 2025 VPS range has a class matching that shape (VPS-2, 6 vCore / 12 GB / 100 GB NVMe, verify) at a monthly price far below comparable Public Cloud instances. The OVH OpenTofu provider documents an `ovh_vps` resource that orders through OVH's order workflow and runs the termination workflow on destroy, but its behavior (key injection without a mailed password, orphan detection, termination tokens, plan JSON exposing the plan code) is unverified in practice. VPS orders commit a month of spend.

## Decision
1. Default product line **`vps`**, root module `infra/tofu/compute/ovh-vps`, resource `ovh_vps`, machine class `ovh-vps-6c-12g-100nvme`, `do_not_send_password = true`, SSH key injected at order time, display name `sc-<cluster>-node-NN`.
2. A mandatory **spike** (plan 04 §14.1, S-1..S-7) with recorded evidence decides whether (1) is usable at the pinned provider version.
3. If ordering, key injection or orphan detection fails, fall back to **`public-cloud`** (`ovh_cloud_project_instance`, hourly billing, flavor ≥ preferred shape).
4. If that also fails, the `ovh` provider is disabled for installs (`providers.ovh.enabledProductLines: []`) and the supported OVH path is a **manual order in the OVH control panel followed by `PROVIDER=existing`** (plan 05).
5. A termination that needs an e-mailed token does not disqualify (1); destroy goes `blocked` and the procedure completes it.
6. DNS is never at OVH (Cloudflare, plan 09); no vRack in MVP.

## Consequences
- P02 with `PROVIDER=ovh` is blocked until the spike passes; P02 can pass meanwhile with `existing`.
- Monthly commitment is shown as `committed_minimum` and `reversible_within_hours: false`.
- Public Cloud fallback roughly multiplies monthly compute cost (verify); the footprint shows it.

## Alternatives rejected
- Public Cloud as default: hourly flexibility is not worth the price multiple for a long-lived node.
- Calling the OVH order API directly from Go: bypasses the plan-JSON guard (ADR-0102) and OpenTofu state.
- OVH Dedicated/Kimsufi: larger commitments, slower delivery, no declarative ordering.

## Verification
P02 gate (plan 04): 04-AC-20 spike evidence present and consistent with the default product line; 04-AC-21..25 on a real install and destroy.
