# Procedure: Bootstrap preflight failure

**Owning plan:** plans/50-bootstrap-orchestrator.md (checks contributed by 02, 03, 04, 05, 06, 09, 14, 17, 42, 53)
**SPEC references:** §8, §14, §15, §113 ("bootstrap preflight failure")
**Severity / urgency:** routine (nothing has been created or changed when preflight fails)
**Expected duration:** 5–60 min depending on the cause
**Requires break-glass:** no

## When to use
`./bootstrap`, `./bootstrap preflight`, `plan`, `expand`, `recover` or `destroy` exit with:
- **2** (usage / invalid configuration), **3** (preflight failed), **4** (guardrail refused) or **5** (confirmation missing or mismatched) before any provisioning step;
- or a wrapper error before the container starts (`bootstrap: WRAPPER_…`).

The failing check IDs (`PF-xx`) and codes are printed in the table and stored in `.bootstrap/runs/<run-id>/preflight.json`; each row names an anchor in this document.

## Preconditions and required material
- The same machine, clone and `bootstrap.env` that failed.
- Access to the relevant provider consoles (OVH, S3 provider, Cloudflare, GitHub, notification service).

## Safety checks before acting
1. Confirm nothing was created: `./bootstrap status` shows phase `provision` as `pending` (never `running`/`succeeded`). If provision has started, this is not a preflight failure — use `bootstrap-resume.md`.
2. Never "work around" a failing check by editing generated files, the journal or the guard/catalogue files. Guard and catalogue changes are reviewed PRs (protected paths).
3. Re-running preflight is always safe (read-only, except one test notification per configuration digest).

## Steps

1. **Read the failing rows.**
   ```sh
   ./bootstrap preflight --profile <profile>
   ./bootstrap exec -- jq '.checks[] | select(.status=="fail")' .bootstrap/runs/$(ls -t .bootstrap/runs | head -1)/preflight.json
   ```
   Expected: one object per failing check with `id`, `code`, `message`, `remediation`. Go to the section below matching its anchor. Fix all failing checks, then go to step 2.

2. **Re-run preflight** with the same command. Expected: exit 0. If a different check now fails, repeat step 1. If the same check still fails after the documented fix, record the full row in an issue and stop.

<a id="toolbox"></a>
### A. Toolbox and container engine (PF-01, PF-25, wrapper errors)
| Symptom | Fix |
|---|---|
| `WRAPPER_ENGINE_MISSING` | Install/start Docker or Podman; `docker info` or `podman info` must succeed as your user. Force one with `--engine podman`. |
| Rootless Podman: `cannot find UID/GID for user` / `newuidmap` errors | Add subordinate ids: `/etc/subuid` and `/etc/subgid` need a line `<user>:100000:65536` (administrator), then `podman system migrate`. |
| `WRAPPER_TOOLBOX_PULL_FAILED` | GHCR unreachable. Retry later, or unset `SC_TOOLBOX_REQUIRE_PULL` to allow a local build. |
| `WRAPPER_TOOLBOX_BUILD_FAILED` / `TOOLBOX_CHECKSUM_MISMATCH:<tool>` | Read `.bootstrap/toolbox/build-*.log`. A checksum mismatch means the upstream file changed: do **not** edit the lock locally; report it (a release PR re-pins). Use a release tag whose image is published (pull path). |
| `PF-01 TOOLBOX_STALE` | The running image was built from another lock. Run `./bootstrap --toolbox-build version` to rebuild. |
| Permission denied on the repository inside the container (SELinux host) | Confirm `getenforce`; the wrapper must have used `label=disable` (see `bootstrap: engine … selinux=…` line). Do not relabel the repo with `chcon`; report the bug with the wrapper's stderr. |
| PF-25 warn (built although a published digest exists) | Acceptable; the report records it. To use the published image, check out the release tag exactly. |

<a id="config"></a>
### B. Configuration (PF-02, PF-03, PF-09, exit 2)
- `PF-02` unknown key: fix the spelling (the message suggests the nearest key). Empty values count as unset.
- `PF-02` schema errors: each error names the field (`/spec/provider/maxMonthlyCost/amount`) and the variable; correct the value format (e.g. `MAX_MONTHLY_COST=25.00`, not `25€`).
- `CONFIG_IMMUTABLE_CHANGED`: you changed `CLUSTER_NAME`, `BASE_DOMAIN`, `S3_ENDPOINT`, `S3_BUCKET_PREFIX` or `PROVIDER` on an existing installation. Restore the original value (see `gitops/clusters/<cluster>/bootstrap-config.yaml`). Changing them means a new installation.
- `PF-03 PROFILE_KIND_MISMATCH`: fresh installs use `--profile self-hosting-minimum` (or a lab profile); `small-ha` is reached with `./bootstrap expand`.
- `PF-03 LAB_SUBSTRATE_PAID_REFUSED`: lab profiles cannot be used with a paid provider.
- `PF-09`: `EXPECTED_NODE_COUNT` must equal the profile's node count (1 for install, 3 for small-ha expand) and, for `existing`, the number of `EXISTING_NODES` entries.

<a id="repo"></a>
### C. Repository state (PF-04, PF-05)
- Not on `main` / tracked modifications: `git status`; commit or stash your changes, `git checkout main` (or the release tag's commit on a local branch named `main`: `git checkout -B main v<X.Y.Z>`).
- `gitops/clusters/<cluster>/` from a different `install_uid`: you are reusing a cluster name of another installation. Choose a new `CLUSTER_NAME`, or, if that installation is yours and gone, follow `total-cluster-loss.md` (plan 45) instead.
- PF-05 warn (untagged HEAD): acceptable for development; production installs should use a tag.
- PF-16 `LOCAL_BEHIND` (after forge-init): your clone is older than Forgejo `main`. Run `./bootstrap sync-local`, then rerun.

<a id="ssh"></a>
### D. SSH material and host keys (PF-06, `HOST_KEY_MISMATCH`)
- Key unreadable / wrong type: use ed25519 or RSA ≥ 3072; `chmod 600 <key>`; `SSH_PUBLIC_KEY_PATH` must match (`ssh-keygen -y -f <key>` equals the `.pub` content).
- `SSH_USE_AGENT=true` but no agent: `ssh-add -l` must list the key; on macOS Docker Desktop the agent is forwarded via `/run/host-services/ssh-auth.sock`.
- `HOST_KEY_PIN_MISSING`: a recorded node has no pinned key in S3 `provider/output.json` or `.bootstrap/known_hosts`. Verify the key out of band (provider console) and add it with `EXISTING_NODE_OVERRIDES=<node>:host_key=SHA256:…`; sc-bootstrap never re-trusts a recorded node silently.
- `HOST_KEY_MISMATCH` (existing nodes): the machine answering is not the one you attested. Verify out of band (provider console) with `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` on the node; correct `EXISTING_NODE_OVERRIDES` only if the console confirms the new key; otherwise treat as a possible interception and stop.

<a id="credentials"></a>
### E. Credentials and accounts (PF-07, PF-12, PF-15, PF-16, PF-17, PF-18, PF-19)
| Check | Typical fix |
|---|---|
| PF-07 OVH: `OVH_INVALID_CREDENTIAL`, clock skew, `OVH_CREDENTIAL_TOO_BROAD`, `OVH_NO_PAYMENT_METHOD`, `OVH_PRODUCT_LINE_DISABLED` | Recreate the consumer key with plan 04 §10 rules (`fresh-install.md#ovh-credential`); sync the host clock; add a default payment method (not `BANK_ACCOUNT`) in the OVH console; if the line is disabled by the spike, use `fresh-install.md#manual-ovh-order`. |
| PF-07 existing: SSH unreachable, `NODE_SUDO_REQUIRED` | Check firewall/security groups for port 22 from your IP; add `<user> ALL=(ALL) NOPASSWD:ALL` via `/etc/sudoers.d/` on the node. |
| PF-07 order rejected later (payment refused) | Resolve with OVH billing; rerun. |
| PF-12 S3 | Endpoint must be `https://`; check region; key must list/create buckets (`create` mode) or the buckets must exist with versioning `Enabled` (`existing` mode). |
| PF-15 / PF-20 Cloudflare | Token must be active and scoped to the account and zone (plan 09 permission set); the zone must be `active`. |
| PF-16 GitHub | Repository must exist and be private (or `GITHUB_MIRROR_ALLOW_PUBLIC=true`); token must push; its `main` must be empty or an ancestor of your HEAD. |
| PF-17 / PF-18 notifications | Fix URL/token/SMTP settings; confirm you received the test message. A heartbeat URL is optional (warn). |
| PF-19 model keys | The non-billable test call failed: the key is wrong, revoked, or lacks API access. |
| PF-19 `MODEL_KEY_FORBIDDEN_PREFIX` | The value is a subscription/OAuth token, not an API key (SPEC §77). Create a provider API key and use it instead. |
| PF-26 model budget not confirmed (exit 5) | Review `policy/budgets.yaml` limits shown next to the footprint and type `budget <cluster> <monthlyUsd>`; changing the limits is a PR to `policy/budgets.yaml`. |
| PF-27 weak object-storage protection | The provider cannot scope credentials or lock objects (plan 14). Prefer a provider that can; otherwise set `S3_ALLOW_WEAK_PROTECTION=true` knowingly (recorded and permanently alerted). |
| PF-21 credential fingerprint mismatch | A credential in `bootstrap.env` changed since the bundle was sealed: run `./bootstrap rotate refresh-bundle`, then rerun. |

<a id="s3-locking"></a><a id="object-storage"></a>
### F. S3 locking, versioning and clock (PF-13, PF-14, `PF_S3_LOCK_UNSUPPORTED`, `S3_VERSIONING_UNSUPPORTED`, `TOFU_LOCKFILE_NOT_HONORED`)
1. Read `.bootstrap/runs/<run-id>/s3-conformance.json`: each test C1–C8 shows status and HTTP codes (plan 06 §14.3).
2. `PF-13`/`S3_CLOCK_SKEW`: fix the host clock (`timedatectl`), or the provider is skewed (report to provider).
3. C2/C3/C7 failures mean the provider does **not** reject a second conditional create. OpenTofu state on it would be unprotected. There is no override. Choose another S3 provider (at minimum for the `tofu-state` bucket if plan 14 supports per-purpose endpoints), update `S3_*`, and restart from `fresh-install.md` step 6 (immutable `S3_ENDPOINT` means this must happen before `plan` succeeds).
4. C4 failure: enable versioning on the buckets (provider console) or use a provider that supports it.
5. C6 `S3_CHECKSUM_INCOMPATIBLE`: report the provider and toolbox versions; both checksum modes failed.
6. `OBJSTORE_STATE_UNPROTECTED` (exit 3, `plan.storage-credentials`): in `shared` credential mode the OpenTofu state and recovery bundle would be reachable with a key the cluster holds (SPEC §15). Create a **second object-storage account** (same or another provider) for the OpenTofu state and the recovery bundle, and set `S3_STATE_ACCESS_KEY_ID`, `S3_STATE_SECRET_ACCESS_KEY` (and `S3_STATE_ENDPOINT` if it differs) (plan 14 14-R-2a). Only if that is impossible, set `S3_ALLOW_UNPROTECTED_STATE=true`; it is recorded, flagged in every report and `verify`, and permanently alerted.

<a id="product"></a>
### G. Product, region, quota (PF-08, PF-10)
- `OVH_PRODUCT_UNAVAILABLE`: the class is sold out or not offered in the region; the message lists regions offering it. Change `PROVIDER_REGION` (allowed before provision) or `MACHINE_CLASS` to another catalogue entry ≥ the substrate minimum.
- Region not in the allowlist: use a catalogue region.
- Public cloud quota: request a quota increase in the OVH console.
- `PF-10 S3_SAME_REGION_AS_COMPUTE` (warn): consider moving object storage to another region/provider (SPEC §29).

<a id="guardrail"></a>
### H. Guardrails (exit 4)
| Code | Meaning | Action |
|---|---|---|
| `COST_OVER_LIMIT` | estimate > `MAX_MONTHLY_COST` | Raise the limit only if you accept the price shown, or choose a cheaper class. |
| `COST_INCOMPLETE` / `PRICE_CATALOG_STALE` | missing or > 180-day-old price | Use a newer release (catalogue updated), or after checking the price on the provider site run once with `SC_ACCEPT_STALE_PRICES=1` (recorded). |
| `PRICE_CURRENCY_MISMATCH` | account/catalogue currency ≠ `COST_CURRENCY` | Set `COST_CURRENCY` to the catalogue currency; there is no conversion. |
| `GUARD_UNEXPECTED_RESOURCE`, `GUARD_ACTION_FORBIDDEN`, `GUARD_COUNT_EXCEEDED`, `GUARD_PRODUCT_MISMATCH`, `GUARD_REGION_MISMATCH`, `GUARD_SSH_KEY_MISMATCH`, `GUARD_REPLACE_FORBIDDEN`, `GUARD_PLAN_FORMAT_UNSUPPORTED` | the OpenTofu plan contains something not allowed | **Do not edit guard files.** Check that the checkout is a release tag and unmodified (`git status`). A replace usually means `MACHINE_CLASS`/region/key changed on an existing node: revert the change; resizing is a node replacement (plan 47). Otherwise report a bug with `plan-summary.json`. |
| `ORPHAN_PAID_RESOURCE` / `ORPHAN_PENDING_ORDER` | provider resources with this installation's name exist outside state | Not a preflight issue: follow `bootstrap-resume.md#provision`. |
| `PLAN_DIGEST_MISMATCH` | the plan changed after you confirmed | Rerun; review and confirm the new footprint. |
| exit 5 at `plan.storage-confirm` | storage footprint not confirmed (`--non-interactive` without `--confirm-storage`) | Review the bucket list and Object Lock periods, then type `storage <cluster>` or pass `--confirm-storage <cluster>`. |

<a id="node-size"></a>
### I. Node validation (existing provider, or delivered OVH node: `NODE_BELOW_MINIMUM`, `DISK_CLASS_UNVERIFIED`, `NODE_OS_UNSUPPORTED`, `NODE_CGROUP_V1`, `NODE_NO_WIREGUARD`, `NODE_HAS_FOREIGN_PLATFORM`, `NODE_PORT_CONFLICT:<p>`, `EXISTING_MIXED_SIZE_CLASS`, `DISCOVERY_TOOL_MISSING:<cmd>`)
- Below minimum: use a larger machine. There is no waiver. For a delivered paid node this happens after spend; decide whether to destroy it (`safe-destruction.md`) — the report shows the committed amount.
- `DISK_CLASS_UNVERIFIED`: confirm with your provider that the disk is SSD/NVMe, then set `EXISTING_NODE_OVERRIDES=node-01:disk_class=ssd`. Configure will still measure fsync latency (plan 07/08) and fail on slow disks.
- Unsupported OS / cgroup v1 / no WireGuard: reinstall the machine with Debian 13 or Ubuntu 24.04.
- Foreign platform / port conflict: the machine already runs Kubernetes or a service on k3s/etcd/ingress ports. Use a clean machine. Do not uninstall other software from production machines just to pass this check without understanding its owner.
- Mixed size class: all nodes must be the same class.

## Verification
`./bootstrap preflight --profile <profile>` exits 0 and `preflight.json` has no `fail` rows. For exit 4 causes, `./bootstrap plan` exits 0 and prints the expected footprint.

## Rollback / abort
Nothing to roll back: preflight and plan create nothing (except buckets after `plan.storage-apply` during a real `./bootstrap` run, which are retained by design and reused by the next run).

## Evidence to record
The failing and the passing `preflight.json` (run directories are kept for the last 20 runs) and, for guardrail overrides (`SC_ACCEPT_STALE_PRICES`), the journal confirmation entry.

## Follow-ups
- A check that failed because of a product/plugin change (catalogue, guard fixture, OVH API) gets an issue so the next release updates the catalogue or fixtures.
- A failure not covered here gets a new section in this procedure (SPEC §113: add missing procedures).
