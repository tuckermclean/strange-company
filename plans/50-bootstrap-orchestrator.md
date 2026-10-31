# 50 — Bootstrap orchestrator and `./bootstrap` CLI

**Status:** Stage-0 plan (draft)
**DAG nodes:** F08 (owner), P12 (co-owner with plan 19: orchestrator report, verify aggregation, SPEC §130 check, memory-budget measurement)
**Related plans:** [01](01-repository.md), [02](02-bootstrap-toolbox.md), [03](03-provider-interface.md), [04](04-ovh-provider.md), [05](05-existing-nodes.md), [06](06-opentofu-state.md), [07](07-k3s.md), [08](08-node-security.md), [09](09-networking.md), [14](14-object-storage.md), [16](16-forgejo.md), [17](17-github-mirror.md), [18](18-flux.md), [19](19-seed-handoff.md), [35](35-secrets.md), [42](42-alerting-notifications.md), [44](44-backup.md), [45](45-recovery.md), [47](47-ha-expansion.md), [49](49-destroy.md), [51](51-capacity-and-scheduling.md), [52](52-ci-strategy.md)
**Contracts:** owns `contracts/bootstrap-config.schema.yaml`, `contracts/bootstrap-journal.schema.yaml`; consumes `provider.schema.yaml`, `substrate.schema.yaml`, `cluster-profile.schema.yaml` (plan 03), `recovery-bundle.schema.yaml`, `secret-classes.yaml` (plan 35), `restore-test-result.schema.yaml` (plan 44)
**ADRs:** relies on ADR-0002, 0005, 0009, 0010, 0012, 0013, 0016, 0018, 0020; introduces ADR-0101 (operator-side secret handling)

## 1. Purpose

`sc-bootstrap` is the Go program (`src/cmd/sc-bootstrap`) that turns a fresh clone plus `bootstrap.env` into a running Strange Company and then hands routine management to Git (SPEC §7, §8, §35, §130). `./bootstrap` is the POSIX `sh` wrapper (ADR-0002) that resolves the toolbox image (plan 02) and runs `sc-bootstrap` inside it.

This plan owns:
- the CLI surface (every subcommand, flag, exit code);
- the phase pipelines (install, expand, recover, destroy): sequencing, preconditions, "already done" detection, resume, and timeouts;
- the phase journal and the run lock;
- normalizing `bootstrap.env` into `BootstrapConfig`;
- generating and committing installation config and secrets;
- when and how the recovery bundle is created, stored and confirmed;
- the `verify` aggregation, including the SPEC §130 Definition-of-Done check and the ADR-0020 memory budget;
- the exact `CONTROL TRANSFER COMPLETE` report.

Phase *internals* belong to other plans. `sc-bootstrap` calls them through the interfaces in §7–§8.

## 2. Requirements derived from SPEC.md

1. Target operator experience: clone, `cp bootstrap.env.example bootstrap.env`, edit, `./bootstrap --profile self-hosting-minimum` [SPEC §7, §130].
2. Local prerequisites are limited to Git, Docker or Podman, SSH key material and credentials. No host install of OpenTofu, Ansible, kubectl, Helm, Flux, SOPS, age, jq or yq [SPEC §7, §9].
3. Subcommands `./bootstrap`, `plan`, `status`, `verify`, `expand --profile small-ha`, `destroy`, `recover` [SPEC §8].
4. Pipeline order: preflight → plan → provision → configure → seed → forge-init → flux-bootstrap → verify → control-transfer [SPEC §8]. Between `flux-bootstrap` and `verify` the engine runs two sub-phases that SPEC §8 does not name but that need credentials created only after transfer: `sso-init` (plan 15) and `ci-init` (plan 20). They are part of SPEC's `verify` preparation, not new top-level stages.
5. Bootstrap is idempotent, safely resumable, observable, and conservative around destructive operations [SPEC §8].
6. Paid-resource validation, footprint printing and fail-closed behavior happen before creation [SPEC §14] (mechanics: plan 03).
7. OpenTofu state survives loss of the bootstrap machine [SPEC §15] (mechanics: plan 06).
8. Bootstrap is complete only when the eight SPEC §35 conditions hold. It then prints `CONTROL TRANSFER COMPLETE`, and routine management goes through Git afterwards [SPEC §35].
9. The seed phase and the seed→Flux transfer happen without duplicate-resource chaos [SPEC §34] (mechanics: plan 19, ADR-0005).
10. Black-start material lives outside the cluster [SPEC §15, §98, §111]. Recovery follows the SPEC §112 order [SPEC §112] (DR logic: plan 45).
11. Destroy is guarded and never touches recovery lifecycle resources [SPEC §114] (plan 49).
12. Bootstrap scratch state has a retention rule [SPEC §107].
13. No secret appears in plaintext in Git, logs or reports [SPEC §94, §98].

Derived requirements:
- **50-R-1** Every phase and step records its status in the journal *before and after* it acts. A crash at any instant leaves a journal from which a rerun converges to the same end state.
- **50-R-2** No phase is ever marked done by hand. There is no `--mark-done` or `--skip-phase` flag, so the SPEC §130 "without manually …" claim can be checked mechanically (V-31).
- **50-R-3** Paid infrastructure is never created unless the operator has confirmed the exact plan digest in this installation's journal (plan 03 §14).
- **50-R-4** The recovery bundle exists locally and in object storage, and has been confirmed by passphrase re-entry plus a decrypt round trip, *before* the first paid or node-mutating action (provision or configure).
- **50-R-5** After `control-transfer`, `sc-bootstrap` makes no further commits to `main` for that installation. Later configuration changes are opened as Forgejo PRs on `bootstrap/*` branches.
- **50-R-6** Every mutating command holds the run lock: a local lock plus an S3 conditional-write lock.

## 3. Inputs

| Input | Source | Notes |
|---|---|---|
| `bootstrap.env` | repo root (gitignored), or path in `--env-file` | KEY=VALUE, POSIX-shell-compatible subset (§9.1) |
| `--profile` | CLI; falls back to `PROFILE` in env | Fresh install accepts only `kind: BootstrapSubstrate` (plan 03 §9) |
| Profiles | `infra/profiles/*.yaml` | substrate / cluster-profile contracts |
| Price catalogue, plan guards | `infra/catalog/prices/*.yaml`, `infra/tofu/**/guard.yaml` | plan 03 |
| Toolbox tool versions | `toolbox/tools.lock.yaml` | plan 02 §9.4; every pin inside its `plans/compatibility-baseline.yaml` line (OpenTofu 1.13.x, ansible-core 2.21.x, Helm 4.3.x (Flux 2.9 embeds Helm v4), kubectl 1.36.x, SOPS 3.13.x, age 1.3.x, cosign 3.1.x, AWS CLI 2.38.x; CD-29) |
| Repository HEAD | local Git | the commit that is seeded and pushed (ADR-0005) |
| Recovery passphrase | interactive prompt, or `SC_RECOVERY_PASSPHRASE_FILE` (automation only) | never stored (ADR-0012, ADR-0101) |
| SSH private key | `SSH_PRIVATE_KEY_PATH` mounted read-only at `/run/sc/ssh/id_key`, or ssh-agent socket | plan 02 §14.4 |
| Existing journal | `.bootstrap/journal.json` and/or `s3://<prefix>-tofu-state/<cluster>/journal/journal.json` | §17.3 |
| Committed config (recover) | `gitops/clusters/<cluster>/bootstrap-config.yaml` | §14.9 |

## 4. Outputs

| Output | Location | Durable? |
|---|---|---|
| Normalized config | `gitops/clusters/<cluster>/bootstrap-config.yaml` (committed) | Git |
| Installation config and SOPS secrets | `gitops/clusters/<cluster>/**`, `.sops.yaml` (committed; plan 01 §6.5) | Git |
| Node inventory | `gitops/clusters/<cluster>/inventory.yaml` (committed after provision) | Git |
| Journal | `.bootstrap/journal.json` + `s3://<prefix>-tofu-state/<cluster>/journal/journal.json` | S3 (versioned) |
| Recovery bundle | `/blackstart/<cluster>/bundles/sc-recovery-<cluster>-v<N>.tar.age` + `s3://<prefix>-recovery/<cluster>/bundles/sc-recovery-<cluster>-v<N>.tar.age (pointer `bundles/LATEST.json`, plan 35)` | S3 (versioned) + operator |
| Recovery age identity | `/blackstart/<cluster>/recovery-identity.txt` (0600; operator moves it offline) | operator |
| Bootstrap kubeconfig | `/blackstart/<cluster>/kubeconfig` (0600; `SC-K3S-ADMIN-KUBECONFIG`) | operator machine |
| Run artifacts | `.bootstrap/runs/<run-id>/` (log.jsonl, preflight.json, plan-summary.json, provider-output.json, verify.json, report.json, report.txt, tools/*.log) | local; redacted copies in S3 `…/tofu-state/<cluster>/runs/<run-id>/` |
| Final report | stdout + `report.txt`/`report.json` + S3 `…/tofu-state/<cluster>/reports/control-transfer-<run-id>.json` | S3 |

`/blackstart` is the container mount of the host black-start directory `${SC_BLACKSTART_DIR:-$HOME/.strange-company}` (plan 02 §14.3). It lives outside the repository so that nothing in it can be committed by accident.

## 5. Dependencies

**Hard (DAG):** F08 depends on F02 (contract tooling validates the config and journal schemas) and F03 (toolbox image). P12 depends on P07–P11.

**Phase-internal owners** (called through §8):

| Phase | Owner of internals |
|---|---|
| preflight | 50 (framework), with checks contributed by 03/04/05 (provider), 06 (state), 14 (buckets), 09 (Cloudflare), 17 (GitHub), 42 (notification), 53 (model keys) |
| plan | 50 (identity, config generation, bundle), 06 (state init and migration), 14 (bucket set), 03 (guardrails), 04/05 (provider plan) |
| provision | 03/04/05 |
| seed-prep | 09 (tunnel, public hosts, DNS) |
| configure | 07 (k3s), 08 (baseline, firewall), 09 (node networking) |
| seed | 19 (ADR-0005) |
| forge-init | 16 (with 17 for mirror configuration) |
| flux-bootstrap | 19 (source switch), 18 (GitOps tree) |
| sso-init | 15 |
| ci-init | 20 |
| verify | 50 (aggregation), with checks contributed by every platform plan |
| control-transfer | 50 |
| expand internals | 47 |
| recover internals (restore) | 45 |
| destroy internals | 49 |

**Runtime:** Docker or Podman on the operator host; reachability of the provider API, S3 endpoint, Cloudflare API, GitHub API, notification endpoint, and model API list endpoints.

## 6. Owned state

| State | System of record | Writers |
|---|---|---|
| Bootstrap journal | `s3://<prefix>-tofu-state/<cluster>/journal/journal.json` (versioned); local copy `.bootstrap/journal.json` | `sc-bootstrap` only, while holding the run lock |
| Run lock | `s3://<prefix>-tofu-state/<cluster>/locks/bootstrap.lock` + `.bootstrap/run.lock` | `sc-bootstrap`; `./bootstrap unlock` |
| Normalized config | Git `gitops/clusters/<cluster>/bootstrap-config.yaml` | `sc-bootstrap` before transfer; human PR afterwards |
| Run logs and reports | `.bootstrap/runs/` (last 20 runs kept, §11) + S3 copies | `sc-bootstrap` |
| Install identity (`install_uid`) | journal `install_uid` + `bootstrap-config.yaml` `metadata.installUid` (BOOT-14) + bundle manifest | generated once |

Generated secrets are **not** owned here. Their system of record is the SOPS files in Git plus the recovery bundle (plan 35). OpenTofu state is owned by plan 06.

## 7. External interfaces

### 7.1 CLI surface

All commands run as `./bootstrap <command> [flags]`. Global flags: `--env-file PATH` (default `./bootstrap.env`), `--non-interactive` (never prompt; a missing required confirmation exits 5), `--log-format text|json` (stderr; default text on a TTY, json otherwise), `--engine docker|podman` (wrapper, plan 02), `--toolbox-build` (wrapper: force a local build), `-v` / `--verbose`. Pipeline-control flags for `run`/`expand`/`recover`: `--from-phase <phase>` (re-runs the named phase and every later one: their `Check()` probes run again and their status is reset to `pending`; it never marks anything done, 50-R-2; used by plan 19 procedures; refuses seed, forge-init and flux-bootstrap after the source switch with `PHASE_FROZEN`, §17.1 rule 5), `--retry-phase <phase>` (§16), `--resume-after-emergency` (only with `--from-phase flux-bootstrap` after an emergency seed, plan 19 / `failed-flux-handoff.md` F3).

**CD-14:** this plan is the CLI authority. §7.1 lists every `./bootstrap` command and §7.1.1 every `sc-bootstrap` subcommand used anywhere in `plans/` or `procedures/`. A plan or procedure that uses a form not listed here is wrong until this plan adds it.

| Command | Purpose | Mutating | Run lock | Unlocks bundle |
|---|---|---|---|---|
| `./bootstrap [run] [--profile P] [--confirm-plan D8]` | Run or resume the install pipeline. On a `transferred` install it runs preflight + verify only | yes | yes | yes |
| `./bootstrap preflight [--profile P]` | Preflight checks only | no (test notification only once per config digest) | no | no |
| `./bootstrap plan [--profile P]` | Read-only preview: preflight, plus a storage plan and compute plan against a throwaway local backend if remote state does not yet exist; prints footprint and cost. **Never creates buckets, uploads or commits.** | no | no | no |
| `./bootstrap status [--json]` | Journal summary: pipelines, phases, steps, last error and the procedure to follow | no | no | no |
| `./bootstrap verify [--json] [--only V-xx,...]` | Run the verify checklist (§14.8) against the live install | pushes and deletes the probe branch `sc-verify/push-probe-<run-id>` (V-03) and the probe tag `sc-mirror-probe/<run-id>` (V-08) | yes | yes (kubeconfig only if present) |
| `./bootstrap expand --profile small-ha [--confirm-plan D8]` | Expand pipeline (§14.10, plan 47) | yes | yes | yes |
| `./bootstrap destroy [--dry-run] [--confirm-plan D8]` | Destroy pipeline (plan 49) | yes | yes | yes |
| `./bootstrap recover [--scope full\|state-only] [--confirm-plan D8]` | Recover pipeline (§14.9, plan 45) | yes | yes | yes |
| `./bootstrap shell` | Interactive bash inside the toolbox (plan 02 §14.6) | — | no | on demand (`sc-env`) |
| `./bootstrap exec -- <cmd> [args]` | Run one command inside the toolbox with the shell environment | — | no | on demand |
| `./bootstrap build [--no-cache] [--platform PLATFORM] [-f CONTAINERFILE] --target toolbox\|ci` | Wrapper (plan 02 §7.1): build one image target locally (plan 02 §14.2 (a)) with the wrapper's engine selection, tag, inputs-id label and build log; `make toolbox` calls it for `toolbox` and then `ci` | local image store only | no | no |
| `./bootstrap capsule publish` | Upload the signed bootstrap capsule off-cluster from the release artifacts, for `shared` object-storage mode where the provider cannot mint the in-cluster `capsule-writer` a prefix-scoped put-only key in the separate state account (plan 14 Q-14-11; capsule layout plan 45 §9.4, CD-33) | yes (S3 `recovery` bucket, `capsule/` prefix) | no | yes (state-account credential) |
| `./bootstrap report [--run-id ID] [--json]` | Re-print the last (or given) final report | no | no | no |
| `./bootstrap rotate <target>` | Rotation entry points. Targets: `recovery-bundle` (new passphrase, generation+1), `tofu-passphrase` (plan 06 §20.3), `age-cluster`, `age-recovery`, `age-ci` (the CI-side age identity of `secrets/ci/<cluster>/`, plan 35 `rotate-age-key.md`), `secret <CLASS>` (below), `refresh-bundle` (re-seal after credential changes in `bootstrap.env`). Target flags: `tofu-passphrase [--reencrypt-history]` (re-uploads each retained noncurrent state version re-encrypted with the new passphrase so the previous one can leave the bundle, plan 06 §20.3 step 5), `age-cluster [--abort]` (before the bundle step only: removes the new key from `sops-age` and discards the rotation branch, plan 35 §14.3, `rotate-age-key.md` §Rollback / abort), `refresh-bundle [--from-version N]` (re-seals from the valid sealed version N plus the current `bootstrap.env` values when the latest version fails integrity, plan 35 §14.4, `rotate-secrets.md` §bundle-refresh) | yes | yes | yes |
| `./bootstrap state <status\|versions\|restore\|unlock\|verify\|import\|migrate-backend>` | OpenTofu state operations (plan 06 §7.2). `migrate-backend` copies the current version of each module key to the new `tofu-state` bucket after `storage apply --migrate`, re-verifies decryption and the address set, and records the old backend in the journal | `restore`/`unlock`/`import`/`verify` yes | yes (except `status`, `versions`) | yes |
| `./bootstrap unlock [--run-id ID]` | Break a stale bootstrap run lock after typed confirmation (§17.4) | yes | — | no |
| `./bootstrap version` | Prints sc-bootstrap version, toolbox digest and source (pulled/built), and the tool version matrix | no | no | no |
| `./bootstrap configure [--nodes n1,n2] [--tags baseline,firewall,k3s,network,os-upgrade] [--check] [-e <var>=<value>]` | Standalone `configure` pipeline (plans 07, 08, 09, 47, 48): runs `site.yml` for the selected nodes/tags. After transfer it runs `preflight → configure`. **Before install (lab / F10, BOOT-11):** on an installation in state `new`…`provisioned`, it runs `preflight → plan → provision → configure` and stops (equivalent to `./bootstrap` with `SC_TEST_STOP_AFTER=configure`). It is allowed only with `PROVIDER=existing`, where provision only discovers and pins the nodes; a paid provider exits 2 (`CONFIGURE_PREINSTALL_PAID_REFUSED`). This is the form plan 07 (07-AC-2) and plan 08 (08-AC-3) use on lab VMs. `--check` = Ansible check mode (reports drift, changes nothing). `-e` accepts only variables listed in plan 07 §9 as operator overrides (e.g. `k3s_force_cluster_init`, used by `restore-etcd.md`); others exit 2 | yes (unless `--check`) | yes | yes |
| `./bootstrap node add <node-NN> [--confirm-plan D8]` | Add one server node (plan 47 §14.4): preflight → plan → provision → peer-commit → configure → converge → verify; paid providers show a footprint | yes | yes | yes |
| `./bootstrap node remove <node-NN> [--allow-two-to-one] [--not-joined]` | Remove one node (plan 47 §14.5): drain → etcd-remove → peer-commit → decommission (`existing`) or destroy-compute of that node (paid); typed confirmation `remove <cluster> <node>` | yes | yes | yes |
| `./bootstrap upgrade k3s --to <version> [--allow-patch-downgrade] [--force-binary-only]` | k3s upgrade pipeline (plan 48 §14.1, `upgrade-k3s.md`): one server at a time with pre-snapshot and health gates; `<version>` must equal `versions/k3s.lock.yaml` at HEAD; confirmation `upgrade-k3s <cluster> <version>` | yes | yes | yes |
| `./bootstrap edge-bypass` | Human access while the public edge is down (plan 09, AUTH-11a): port-forward `svc/traefik` (`ingress`) to host `127.0.0.1:443` through the toolbox and print temporary `/etc/hosts` lines for every host in `config/public-hosts.yaml`; removes nothing on the host | no | no | no |
| `./bootstrap storage apply [--migrate]` | `--migrate` (with typed `migrate-object-storage <cluster>`) runs the object-storage replacement steps of `replace-object-storage.md` (plan 14; DR-13): `plan.storage-confirm`, bucket creation on the new endpoint, credential minting, SOPS files and the capability suite. Without it: re-run the storage root module and plan 14's credential/lifecycle reconciliation after a bucket-layout PR (plan 14 §19); same guard and `admin` credential as `plan.storage-apply`; never deletes | yes | yes | yes |
| `./bootstrap forge-init [--recover]` | Re-run the forge-init phase standalone (idempotent check-then-act, plan 16 §14); `--recover` = plan 16 restore mode | yes | yes | yes |
| `./bootstrap sso-init [--rotate-breakglass] [--check-bootstrap-secret] [--from <step>]` | Authentik initialization and break-glass account management (plan 15) | yes | yes | yes |
| `./bootstrap ci-init [--recover]` | Woodpecker initialization: OAuth app, bot logins, repository activation, intents (plan 20); `--recover` verifies a restored Woodpecker | yes | yes | yes |
| `./bootstrap handoff-rollback` | Return the root Kustomization to the seed `Bucket` source while it still exists (plan 19 §14.3); idempotent; confirmation `handoff-rollback` | yes | yes | no |
| `./bootstrap kubeconfig refresh --via-ssh [<user>@<host>] [--node <node>] [--identity <key-file>]` | Issue (or re-issue) the operator's **own** kubeconfig (AUTH-7, §10.1): over SSH to a server node, generate a key pair and a client certificate `CN=sc-operator:<operator-user>`, `O=sc:cluster-admins`, valid ≤ 30 days, signed on the node by the k3s client CA; store it at `/blackstart/<cluster>/kubeconfig` (`SC-K3S-ADMIN-KUBECONFIG`) with the server set to the SSH tunnel. `/etc/rancher/k3s/k3s.yaml` (`system:admin`, the break-glass identity) is **never** copied off the node. The former `kubeconfig import` form is withdrawn. The break-glass form names the SSH target and key explicitly (`--via-ssh sc-breakglass@<node-ip> --identity <key-file>`, with the key extracted from the bundle; plan 15 §14.4, `break-glass-access.md` §Cluster step 5) | local file only | no | no |
| `./bootstrap rotate secret <CLASS>[/<instance>] [--emergency] [--stage [add\|switch\|retire] \| --value-from-staging] [--new-public-key PATH] [--abort]` | Generic per-class rotation entry point of plan 35 (`rotate-secrets.md`): two-phase stage/commit for classes with overlap, `--emergency` for no-overlap replacement. `--stage` without a value is the stage step of that two-phase flow; `--stage add`, `switch` and `retire` (one PR each, ≥ 24 h apart) drive the three-PR JWKS overlap of `SC-RUNNER-SIGNING` (plan 35 §14.5.4, `rotate-secrets.md` §sc-runner-signing). `--abort` (before the old value is revoked) discards the staged candidate: it closes or reverts the PR (a revert PR if merged), restores node files and revokes the issuer-side new value (`rotate-secrets.md` and `rotate-storage-credentials.md` §Rollback / abort) | yes | yes | yes |
| `./bootstrap recovery-bundle verify --full` | Quarterly operator attestation (plan 35, CD-10): opens the bundle and decrypts every SOPS file with the bundle identity and with the offline recovery identity in a clean-room container; writes the attestation to S3 `evidence` | no | no | yes |
| `./bootstrap supply-chain init` | One-time trusted-CI signing setup (plan 23): generates `SC-RELEASE-SIGNING`, writes the public key and SOPS secrets, opens a PR. Runs **after** control transfer (CD-19; P11 is not part of the Bootstrap DoD) | yes | yes | yes |
| `./bootstrap abandon-identity` | Abandon an installation identity that never reached a confirmed compute plan (BOOT-6): typed `abandon <cluster> <uid8>`; archives the remote journal to `journal/abandoned-<uid>.json`, re-imports the existing buckets into fresh storage state (plan 06 `state import --module storage`) and lets `plan.install-identity` generate a new identity on the next run. Refused (`ABANDON_REFUSED_COMPUTE_CONFIRMED`) once any `paid-footprint`/`existing-nodes` confirmation exists | yes | yes | no (by design: the passphrase may be lost) |
| `./bootstrap sync-local` | Fetch Forgejo `main` through a run-scoped port-forward (credentials from the bundle) and fast-forward local `main`; refuses on divergence (`LOCAL_DIVERGED`) (BOOT-7) | local Git only | no | yes |

Toolbox access flags (wrapper, plan 02 §7.1): `./bootstrap shell [--image toolbox|ci] [--network bridge|host|none] [--unlock] [--api-node <node>] [--publish <port>]...` and `./bootstrap exec [--image …] [--network …] [--unlock] [--no-kube] [--kvm] [--api-node <node>] [--clean-room] -- <cmd>`. `--api-node` pins the Kubernetes API tunnel to one server (`lost-node.md`). `--publish <port>` publishes a container port on host `127.0.0.1:<port>` (e.g. a `kubectl port-forward` to the Longhorn UI, plan 13); repeatable. `--clean-room` (plan 35, `rotate-age-key.md`) starts the container with `--network none`, no repository write access (read-only mount), no black-start directory except the files named by `--with-file`, so decrypted material cannot leave it.

Non-interactive confirmations (BOOT-1, BOOT-12; each is the exact text the interactive prompt would require, and is recorded with `method: flag`): `--confirm-backstops <file>` / `SC_CONFIRM_BACKSTOPS` (PF-29 attestation file), `--confirm-plan D8` / `SC_CONFIRM_PLAN` (paid footprint or `existing` node list digest), `--confirm-storage <cluster>` / `SC_CONFIRM_STORAGE` (storage footprint), `--confirm-budget <monthlyUsd>` / `SC_CONFIRM_BUDGET` (PF-26), `--confirm-install <cluster>` (destroy). DR confirmations typed in `recover` only (plan 45 §7.3): `destroyed <cluster>`, `fence-live <cluster>` (fence an installation that still answers, plan 45 DR-2), `fence-nodes <cluster> <count>` (move unreachable compute out of managed state, plan 03 §20.1), `accept-empty <cluster-pg>` (a CNPG cluster without any backup), `accept-github-commits <n>` (trust `n` GitHub commits beyond the newest `repo-backups` manifest ref, DR-16), `accept-generation <cluster-pg> <N>` (accept a backup generation that differs from Git, plan 45 step 6.1), `defer-mirror <cluster>` (finalize while GitHub is unreachable: the finalize PR resets the emergency stop but keeps `expectMirror: false`; `SCGitHubMirrorRecoveryModeTooLong` keeps notifying; plan 45 §9.4), `seed-without-github <cluster>` (black start during a GitHub outage, DR-14), `migrate-provider <cluster>`, `migrate-object-storage <cluster>` (DR-13). Their journal kinds are in `bootstrap-journal.schema.yaml`.

Additional flags of existing commands: `verify --only V-xx,… | --check <section> | --profile recovery | --ack-notifications` (the last is the optional post-report V-24 acknowledgement, plan 42, kind `notification-nonce`; `--profile recovery` per §14.9); `kubeconfig refresh --via-ssh [<user>@<host>] [--node N] [--identity <key-file>]` (break-glass form, plan 15 §14.4); `dns plan --provider manual [--mode direct]` (records for operator-managed DNS, plans 09/10; `--mode direct` prints the direct-mode A records, every public host to every node IPv4, while the cluster still runs in tunnel mode, plan 10 §20.2, `edge-ingress-failure.md` §F) (sections: `platform`, `observability`, `dr`, `recovery`, `ci`; plan 38 uses `--check observability`); `destroy [--dry-run] [--skip-final-backup] [--confirm-install C] [--confirm-plan D8] [--accept-unmirrored-loss] [--non-interactive]` (plan 49); `recover` flags in §14.9.

#### 7.1.1 `sc-bootstrap` subcommands (run as `./bootstrap exec -- sc-bootstrap …`)

These are implemented in the same binary and reuse the orchestrator's clients and redactor. Commands marked M mutate; all mutating subcommands take the run lock. Semantics are owned by the listed plan.

| Group | Subcommands | Owner |
|---|---|---|
| toolbox | `toolbox fetch --lock F --arch A --dest D [--set S]`, `toolbox selfcheck [--json\|--embedded-only]`, `toolbox init`, `env <kube\|s3\|unlock\|tofu M>` (alias `sc-env`), `version` | 02 |
| phases (direct) | `run`, `preflight`, `plan`, `backup now --component C` (plan 44 §9.7), `configure --tags T` (M), `state …` (as `./bootstrap state`), `seed [--emergency-source --commit <sha>]` (M), `seed-gc` (M), `flux-bootstrap` (M), `forge-init` (M), `expand`, `recover`, `verify control-transfer` (plan 19 G1–G7) | 50 / 19 / 43 |
| pre-seed secrets | `pre-seed` (generation step of `plan.install-identity`, M), `seal` (bundle seal, M), `secrets generate\|status [--class C]\|compare\|record <CLASS> [--reason R]\|scan [--since <rev>]\|lint\|reencrypt [--all --with-recovery-identity]\|rotate <CLASS> [--keys K[,K…]] [--write-sops]` (M where writing; `record --reason R` appends a generation-ledger entry whose `reason` is one of the ledger's reasons, e.g. `schedule`, plan 35 §14.9, `signing-key-rotation.md`; `scan --since <rev>` scans only the commits since `<rev>`, plan 35 §7.1; `rotate --keys` rotates only the named keys of a multi-key class and keeps the others byte-identical, e.g. `SC-FORGEJO-APP-SECRETS` without its non-rotatable `SECRET_KEY`, plan 16 §10, `rotate-forge-credentials.md` §H), `secrets gen internal-ca [--rotate]` (M, plan 10) | 35 / 10 |
| provider | `provider delete --id <id> --confirm-fenced <cluster>` (M; deletes one **fenced** provider resource listed in `config/recovery.yaml` `fencedResources` / journal `facts.fenced_nodes`, refuses anything else; `total-cluster-loss.md` §Fenced installation cleanup) | 03 / 45 |
| bundle | `bundle open --show\|--extract [--to <dir>]` (`--extract <CLASS>[,<CLASS>…] --to <dir>` writes each value as a 0600 file under `<dir>` in the toolbox tmpfs; `break-glass-access.md` §Cluster, §Infrastructure, plan 15 §14.4), `bundle manifest`, `bundle export-env` (writes a `bootstrap.env` reconstructed from the bundle's credential fingerprints and values to `/blackstart/<cluster>/bootstrap.env.recovered`, 0600; DR-29), `bundle stage-rotation [--abort]` (M), `recovery-bundle status [--json]\|verify [--full]\|rebuild --from-recovery-identity [--black-start]` (M; `--black-start` rebuilds the bundle during a black start when the bundle is lost, `rotate-age-key.md#lost-bundle-black-start`, DR-B15) | 35 |
| age / k3s keys | `sops-age install [--exact]` (M), `sops-age fingerprint`, `sops-age rewrap-job --new-recipients K,R` (M; break-glass one-shot Job in `flux-system` that re-wraps every SOPS file to new recipients, `rotate-age-key.md`), `k3s secrets-encrypt-rotate` (M), `kubeconfig refresh [--via-ssh [<user>@<host>]] [--node N] [--identity <key-file>]` (M, local file; always signs on the node over SSH, AUTH-7; the explicit target and `--identity` are the break-glass form of §7.1), `etcd snapshot --name N` (M) | 35 / 07 |
| forge | `forge init [--only S] [--recover]` (M), `forge apply [--dry-run] [--only O] [--stage bootstrap\|steady] [--rotate <target> [--finalize]] [--rotate-mirror <repo>] [--github-import-token-file F]` (M; import and project mirrors per plan 16 §14.5, CD-43; `--stage bootstrap\|steady` applies that branch-protection stage of `forge/forge.yaml`, `bootstrap` whitelisting `sc-admin` and `sc-breakglass` for pushes, plan 16 §7.4 and §14.4, plan 55 §14.8a `emergency-merge.md`; `--rotate <target>` re-mints one credential without deleting the old one, `<target>` being a bot login, `webhook-secret`, `flux-deploy-key` or `woodpecker-oauth`, and `--rotate <target> --finalize` deletes the superseded tokens or deploy key once consumers use the new one, plan 16 §17, `rotate-forge-credentials.md` §B–§D), `forge verify`, `forge diff`, `forge finalize` (M), `forge api <method> <path>`, `forge key-hash --source S`, `forge hostkey stage` (M), `forge admin-password rotate --token-from-stdin` (M; reads a short-lived admin token from stdin, sets a new random `sc-admin` password and verifies it by API basic auth; the steady value lives in the bundle, plan 16 §13.3 AUTH-9, `rotate-forge-credentials.md` §A), `forge breakglass set-password\|verify [--from-staging]`, `forge clone-test --as <bot>`, `forge statuses --since T` | 16 / 55 |
| mirror | `mirror status [--json]\|sync\|verify [--run-id]\|check-github [--init-canary] [--repository <org/repo>]\|recreate [--token-from env\|bundle]\|freshness\|compare-refs [--tags]\|delete-github-tag <tag>\|gh-api <path>` (sync/recreate/delete/verify mutate) | 17 |
| identity | `access join <user> --email E --name N --role engineer\|viewer\|admin` (M; for operators, also issues a per-operator Runner admin credential, CD-30: generates `scadm_<kid>.<secret>` locally, shows it **once**, never stores the secret, and opens a PR adding only its hash entry `{kid, operator, hash}` to `secrets/strange-company/runner-admin-credentials.sops.yaml`), `access leave <user>` (M; calls `POST /v1/admin/credentials/{kid}:revoke` for each of the user's kids, then opens a PR deleting their entries), `access reset-mfa <user>` (M), `access revoke-sessions <user> [--all]` (M) (`manage-human-access.md`), `sso-init [...]` (M), `authentik key-hash --source S\|blueprints\|login-test`, `breakglass drill [--phase P07\|full] [--only S]` (M: evidence; `P07` is the first drill before monitoring exists, `full` the default from P09, plan 15 §14.4.5), `probes serve --config F --listen A` (in-cluster, plan 15 §21.2) | 15 |
| CI | `ci-init`, `ci init [--only S] [--rotate <agent>]` (M; `--only W07 --rotate <agent>` registers a new token for that one agent, writes its SOPS file by PR and deletes the old registration after reconnect, plan 20 §14.2 W07, `rotate-secrets.md` §sc-woodpecker-agent), `ci login <bot>`, `ci verify [--post-upgrade]`, `ci rotate <intents\|maint\|runner>` (M), `ci api <method> <path>`, `ci apply` (M), `ci smoke`, `ci-secrets apply [--delete [<secret>[,<secret>…]] \| --delete-all-release]` (M; plan 23: `--delete <secret>,…` removes the named Woodpecker secrets, e.g. the cosign key at emergency revocation, `signing-key-rotation.md` §emergency-revocation; `--delete-all-release` removes every release secret, `supply-chain-init.md` §Rollback / abort) | 20 |
| edge / DNS | `tunnel ensure\|rotate-token` (M), `gen public-hosts` (M: `config/public-hosts.yaml`), `dns plan [--provider manual] [--mode direct]\|diff\|apply [--exclude H]` (M; `--mode direct` as in §7.1 additional flags) | 09 |
| object storage / backup | `objstore render-lifecycle --bucket B`, `buckets inventory\|audit --since T`, `backup now --component C` (M), `backup status`, `backup open-bundle --manifest M` | 14 / 44 |
| recovery / drills | `drill <id>` and `drill all` (M), `drill record --type T [--outcome O] [--notes N] [--from-journal]` (M: evidence; writes the `restore-test-result` object of plan 45 §20.6: `--outcome` sets its `outcome`, `--notes` adds the operator's free-text note, `--from-journal` takes the outcome and measurements of an RD3 drill (step durations, observed RPO, RTOs from `decidedAt`) from the drill installation's journal, plan 45 §20.6–§20.7, `recovery-drill.md`) | 45 / 47 / 51 / 15 |
| runner / budget | `runner pause\|cancel --all\|dispatch pause\|dispatch resume` (M, Runner admin API through port-forward, authenticated with the operator's own `scadm_` credential — or the break-glass credential, actor `breakglass:runner-admin` — and sending `X-SC-Reason`; **never** `X-SC-Actor`, identity comes from the credential, CD-30), `estop set --level none\|workers\|all [--reason R]` (M; plan 33 levels, COST-17; `R` is sent as `X-SC-Reason` and the body `reason`, plan 33 §7.4), `ledger reconcile --provider P [--since T]` (`--since` limits the comparison of provider usage with the ledger to the window from `T`, `credential-compromise.md` §model-provider-keys, plan 32 §20) | 26 / 33 / 32 |
| observability | `notify test` (M: one notification), `notifications render [--cluster C]`, `telemetry-decide [--verify]` | 42 / 41 |
| supply chain | `supply-chain init [--abort]` (M; `--abort` before the PR merges deletes the bots' tokens and the staged key, `supply-chain-init.md` §Rollback / abort), `supply-chain rotate-signing-key --stage [add]` (M; the release key rotates in one PR that adds the new key `active` and retires the old one at merge, so `add` is its only stage and bare `--stage` means `--stage add`, `signing-key-rotation.md` §Routine rotation) | 23 |
| evidence | `evidence put <kind> <file> [--attach <file>]…` (M: S3 `evidence`; `--attach` uploads source excerpts with the record before they rotate away, AUTH-24, plan 15 §14.4, `break-glass-access.md` §After use), `evidence collect [--incident I] [--since T] [--resource <kind>/<name>]` (`--resource` limits the audit-log excerpt to one object, `sops-decryption-failure.md` §C) | 35 / 44 |
| checks (in-cluster) | `checks <id>` (entry point of the ADR-0553 check CronJobs in `dr-tests`, run from the toolbox image digest: exit 0 iff the invariant holds, evidence JSON to the Job log and S3 `evidence` `checks/<id>/`; ids and invariants in plan 35 §14.6) | 35 |
| nodes | `node exec <node> <command>` (allowlisted read-only diagnostics, e.g. `etcd-snapshot-list`) | 07 / 46 |

`--confirm-plan D8` takes the first 8 hex characters of the plan digest (plan 03 §14.7). With `--non-interactive`, a paid plan proceeds only if `D8` matches. `SC_CONFIRM_PLAN` is the environment equivalent.

### 7.2 Exit codes (stable; procedures branch on them)

| Code | Meaning |
|---|---|
| 0 | success (or already complete) |
| 1 | phase failed (journal error code says which; retryable flag set) |
| 2 | usage error or invalid configuration (schema) |
| 3 | preflight failed (≥1 check `fail`) |
| 4 | guardrail refused (plan 03): unexpected resource, cost over limit, incomplete price, stale catalogue |
| 5 | confirmation missing, declined or mismatched |
| 6 | run lock held by another run |
| 7 | journal conflict or remote journal unwritable |
| 8 | recovery bundle missing, unconfirmed or passphrase wrong |
| 9 | DR safety refusal: `FENCE_UNVERIFIED`, `FENCE_REFUSED_LIVE` (without `fence-live`), `NO_CNPG_BACKUP` (without `accept-empty`), `GENERATION_MISMATCH` (without `accept-generation`), `GITHUB_COMMITS_UNTRUSTED` (without `accept-github-commits`), `REFS_DIVERGED` (plan 45) |
| 75 | waiting on human (e.g. an expand PR awaits merge); rerun later |
| 130 | interrupted (SIGINT/SIGTERM); state consistent, resumable |

### 7.3 Final report (§14.11) and `status --json`

`status --json` prints the journal (contract `bootstrap-journal.schema.yaml`) plus a computed `next_action` string. `report --json` prints the report object described in §14.11.

## 8. Internal interfaces

### 8.1 Phase interface (Go, conceptual)

Each phase and step implements:

- `Inputs(ctx) → canonical JSON` (hashed into `input_digest`);
- `Check(ctx) → Done | NotDone | Drifted(reason)`: the idempotency probe against the *real world* (cloud API, S3, Git, Kubernetes), never just the journal;
- `Run(ctx) → outputs[]`;
- `Timeout() → duration`;
- `Retryable(err) → bool`.

The engine runs `Check` first. `Done` with a matching input digest is recorded as `succeeded` without calling `Run`. `Drifted` causes a re-run for idempotent phases. For `provision` it requires a new plan and a new confirmation.

### 8.2 Calls into other plans' internals

| Call | Owner | Contract |
|---|---|---|
| `provider.Validate/Preflight/Plan/Apply/Inventory/Expand/DestroyCompute` | 03 (+04/05) | `provider.schema.yaml`; plan 03 §8 |
| `state.InitLocal/ApplyStorage/Conformance/Migrate/InitRemote/Verify` | 06 | plan 06 §8 |
| `buckets.Inventory(prefix)` (names, purposes, versioning, lifecycle) | 14 | plan 14 |
| `ansible-playbook infra/ansible/site.yml -i .bootstrap/work/ansible/inventory.yaml -e @/run/sc/ansible/vars.json [--tags baseline,k3s,network]` | 07/08/09 | §14.4 |
| `seed.Run(kubeconfig, commit, snapshotCreds)` and `seed.Check` | 19 | ADR-0005 |
| `forgeinit.Run/Check` → outputs (Forgejo URL, repo, SSH host key fingerprint, deploy key id, mirror id, pushed SHA) | 16 (+17) | plan 16 |
| `transfer.Run/Check` (source switch) | 19 | ADR-0005 |
| `verify.Registry()` → check list with owner, requiredFrom, timeout | each plan | §14.8 |
| `bundle.Seal/Open(passphrase)` + manifest | 35 | `recovery-bundle.schema.yaml` |
| `secrets.Generate(classes)` → values and SOPS file layout | 35 | `secret-classes.yaml` |
| `restore.Run/Check` (recover only) | 45 | plan 45 |

## 9. Configuration

### 9.1 `bootstrap.env` format

UTF-8 text with `KEY=VALUE` lines. `#` starts a comment line. Values may be single- or double-quoted. There is **no** shell expansion except a leading `~/` in `*_PATH` variables, which expands to the host home and is resolved by the wrapper. Unknown keys fail preflight (`PF-02`) with a "did you mean" suggestion. An empty value counts as unset (lesson L-4: an empty string is worse than missing). `bootstrap.env.example` (plan 01) documents every key and is generated from the table below. A CI check (`make config-docs-check`) fails if the two drift apart.

### 9.2 Variables → `BootstrapConfig` (normative)

Pointers are relative to `spec`. "Secret" means the field is a `secretRef` that names the variable. The value is read at runtime and never written to the config.

| Variable | Req. | Default | Field | Secret class |
|---|---|---|---|---|
| `CLUSTER_NAME` | yes | — | `/cluster/name`, `metadata.name` | — |
| `BASE_DOMAIN` | yes | — | `/cluster/baseDomain` | — |
| `ADMIN_EMAIL` | yes | — | `/cluster/adminEmail` | — |
| `PROFILE` | no | `self-hosting-minimum` (overridden by `--profile`) | `/cluster/profile` | — |
| `PROVIDER` | yes | — | `/provider/name` (`ovh`, `existing`) | — |
| `PROVIDER_REGION` | ovh | — | `/provider/region` | — |
| `EXPECTED_NODE_COUNT` | yes | — | `/provider/expectedNodeCount` | — |
| `MACHINE_CLASS` | ovh | — | `/provider/machineClass` (catalogue productId) | — |
| `MAX_MONTHLY_COST` | yes | — | `/provider/maxMonthlyCost/amount` (`0` allowed for existing) | — |
| `COST_CURRENCY` | no | `EUR` | `/provider/maxMonthlyCost/currency` | — |
| `OVH_ENDPOINT` | ovh | `ovh-eu` | `/provider/ovh/endpoint` | — |
| `OVH_SUBSIDIARY` | no | from `/me` | `/provider/ovh/subsidiary` | — |
| `OVH_PRODUCT_LINE` | no | `vps` | `/provider/ovh/productLine` | — |
| `OVH_CLOUD_PROJECT_ID` | if public-cloud | — | `/provider/ovh/cloudProjectId` | — |
| `OVH_APPLICATION_KEY` / `OVH_APPLICATION_SECRET` / `OVH_CONSUMER_KEY` | ovh | — | `/provider/ovh/credentials/*` | SC-INFRA-PROVIDER |
| `EXISTING_NODES` | existing | — | `/provider/existing/nodes` — format `node-01=user@host[:port][/internal_ip],node-02=…` | — |
| `EXISTING_NODE_OVERRIDES` | no | — | per node `diskClassOverride`, `hostKeyFingerprint` — format `node-01:disk_class=ssd;node-01:host_key=SHA256:…` | — |
| `SSH_PRIVATE_KEY_PATH` | yes unless agent | `~/.ssh/id_ed25519` | `/ssh/privateKey` (`file: /run/sc/ssh/id_key`) | SC-SSH-OPERATOR |
| `SSH_PUBLIC_KEY_PATH` | no | `<private>.pub` | `/ssh/publicKeyPath` | — |
| `SSH_USE_AGENT` | no | `false` | `/ssh/useAgent` | — |
| `SSH_ALLOWED_CIDRS` | no | empty (= any; plan 08 decides enforcement) | `/ssh/allowedCidrs` | — |
| `INGRESS_MODE` | no | `tunnel` | `/ingress/mode` | — |
| `CLOUDFLARE_ACCOUNT_ID` | if tunnel mode or `DNS_PROVIDER=cloudflare` (BOOT-22) | — | `/ingress/cloudflare/accountId` | — |
| `CLOUDFLARE_ZONE_ID` | no | resolved from `BASE_DOMAIN` | `/ingress/cloudflare/zoneId` | — |
| `CLOUDFLARE_API_TOKEN` | if tunnel mode, `DNS_PROVIDER=cloudflare` or `ACME_SOLVER=dns01-cloudflare` | — | `/ingress/cloudflare/apiToken` | SC-DNS-API |
| `CLOUDFLARE_ACME_TOKEN` | if `ACME_SOLVER=dns01-cloudflare` | — | `/ingress/acme/apiToken` (zone-scoped DNS:Edit only, delegated `_acme-challenge` zone preferred; in-cluster cert-manager; AUTH-13) | SC-DNS-ACME |
| `DNS_PROVIDER` | no | `cloudflare` | `/ingress/dnsProvider` (`cloudflare`, `manual`; `manual` only in direct mode, plan 09) | — |
| `ACME_SOLVER` | no | `dns01-cloudflare` | `/ingress/acme/solver` (plan 10) | — |
| `POD_CIDR` / `SERVICE_CIDR` | no | `10.42.0.0/16` / `10.43.0.0/16` | `/network/podCidr`, `/network/serviceCidr` (plans 07, 09); rendered into `cluster-vars` as `SC_POD_CIDR` / `SC_SERVICE_CIDR` (plan 18 §9.2) and into the k3s config by plan 07 | — |
| `NODE_MAINTENANCE_WINDOW` | no | `Sun 04:00-06:00` (UTC) | `/node/maintenanceWindow` (plan 08); rendered into `cluster-vars` as `SC_MAINTENANCE_WINDOW` (the name plans 08, 18 and 42 read) and into Ansible `node_maintenance_window` | — |
| `NODE_SSH_IPV6` | no | `false` | `/node/sshIpv6` (plan 08) | — |
| `S3_ENDPOINT` | yes | — | `/objectStorage/endpoint` | — |
| `S3_REGION` | yes | — | `/objectStorage/region` | — |
| `S3_BUCKET_PREFIX` | no | `sc-<CLUSTER_NAME>` | `/objectStorage/bucketPrefix` | — |
| `S3_FORCE_PATH_STYLE` | no | `false` | `/objectStorage/forcePathStyle` | — |
| `S3_BUCKET_MODE` | no | `create` | `/objectStorage/bucketMode` | — |
| `S3_PROVIDER` | no | `generic` | `/objectStorage/provider` (`aws`, `ovh`, `b2`, `generic`; plan 14 §9.6) | — |
| `S3_CREDENTIAL_MODE` | no | `scoped` for `aws`/`ovh`, else `shared` | `/objectStorage/credentialMode` | — |
| `S3_OBJECT_LOCK` | no | `auto` | `/objectStorage/objectLock` | — |
| `S3_ALLOW_WEAK_PROTECTION` | no | `false` | `/objectStorage/allowWeakProtection` (recorded as confirmation `accept-weak-protection`) | — |
| `S3_STATE_ACCESS_KEY_ID` / `S3_STATE_SECRET_ACCESS_KEY` / `S3_STATE_ENDPOINT` | required in `shared` mode (plan 14 14-R-2a) | endpoint defaults to `S3_ENDPOINT` | `/objectStorage/stateAccount` | SC-OBJECT-STORAGE (state account; never in the cluster) |
| `S3_ALLOW_UNPROTECTED_STATE` | no | `false` | `/objectStorage/allowUnprotectedState` (recorded as `accept-state-unprotected`) | — |
| `S3_PRIVATE_CIDR` | if `S3_ENDPOINT` resolves to a private address or uses a port ≠ 443 | — | `/objectStorage/privateCidr`; rendered with the endpoint port into `cluster-vars` `SC_S3_CIDR`/`SC_S3_PORT` for plan 36's `s3-egress` component (SEED-3) | — |
| `S3_CRED_<INSTANCE>_ACCESS_KEY_ID` / `S3_CRED_<INSTANCE>_SECRET_ACCESS_KEY` | `existing` bucket mode with `scoped` credentials | — | `/objectStorage/instanceCredentials[]` (instance names of plan 14 §10, upper-case, `-`→`_`) | SC-OBJECT-STORAGE (that instance) |
| `S3_ACCESS_KEY_ID` / `S3_SECRET_ACCESS_KEY` | yes | — | `/objectStorage/credentials/*` | SC-OBJECT-STORAGE instance `admin` (plan 14 §10; the former name `bootstrap-admin` is dropped) |
| `GITHUB_MIRROR_REPO` | yes | — | `/githubMirror/repository` | — |
| `GITHUB_MIRROR_TOKEN` | yes | — | `/githubMirror/token` | SC-GITHUB-MIRROR |
| `GITHUB_MIRROR_ALLOW_PUBLIC` | no | `false` | `/githubMirror/allowPublic` | — |
| `GITHUB_PROBE_TOKEN` | yes unless the repository is public | — | `/githubMirror/probeToken` (written to SOPS `monitoring/platform-probes-github`, plan 17) | SC-GITHUB-PROBE |
| `GITHUB_MIRROR_READ_TOKEN` | yes unless the repository is public | — | `/githubMirror/readToken` (written to SOPS for the RT-6 black-start test in `plan.install-identity`, CD-19) | SC-GITHUB-MIRROR-READ |
| `GITHUB_MIRROR_PROTECTION_TIER` | no | `rulesets` | `/githubMirror/protectionTier` (`rulesets`, `unavailable-accepted`; plan 17 §9.2) | — |
| `NOTIFY_TRANSPORT` | yes | — | `/notifications/transport` | — |
| `NOTIFY_WEBHOOK_URL` | webhook-like | — | `/notifications/webhookUrl` | SC-NOTIFY-TRANSPORT |
| `NOTIFY_NTFY_TOKEN` | no | — | `/notifications/ntfyToken` | SC-NOTIFY-TRANSPORT |
| `NOTIFY_SMTP_HOST` / `_PORT` / `_FROM` / `_TO` (comma list) | email | port 587 | `/notifications/email/*` | — |
| `NOTIFY_SMTP_USERNAME` / `NOTIFY_SMTP_PASSWORD` | email | — | `/notifications/email/username`, `/password` | SC-NOTIFY-TRANSPORT |
| `HEARTBEAT_URL` | no (warn) | — | `/notifications/heartbeatUrl` | SC-NOTIFY-TRANSPORT |
| `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `DEEPSEEK_API_KEY`, `ZAI_API_KEY`, `GOOGLE_API_KEY` | ≥1 of these or of the next row | — | `/models/providers[]` (one entry per non-empty key) | SC-MODEL-API-KEYS |
| `OPENAI_COMPATIBLE_API_KEY` + `OPENAI_COMPATIBLE_BASE_URL` | optional | — | `/models/providers[]` `openai-compatible` | SC-MODEL-API-KEYS |

**Runtime-only variables (never in `BootstrapConfig`):** `SC_BLACKSTART_DIR` (wrapper), `SC_CONTAINER_ENGINE` (wrapper), `SC_TOOLBOX_BUILD` (wrapper), `SC_RECOVERY_PASSPHRASE_FILE`, `SC_CONFIRM_PLAN`, `SC_CONFIRM_STORAGE`, `SC_CONFIRM_BUDGET`, `SC_VERIFY_MEMORY_WINDOW` (default `30m`), `SC_VERIFY_SETTLE` (default `10m`), `SC_VERIFY_RECONCILE_TIMEOUT` (default `60m`), `SC_ACCEPT_STALE_PRICES` (plan 03 §14.5), `SC_TEST_MODE`, `SC_TEST_CRASH_AT`, `SC_TEST_STOP_AFTER=<phase>` (ends the pipeline successfully after that phase), `SC_LAB_S3_INCLUSTER_ENDPOINT` and `SC_LAB_S3_CA_FILE` (written into `cluster-vars` `S3_ENDPOINT` / `S3_CA_BUNDLE` instead of the toolbox-side values) (§22, plan 05 §14.7; all refused when the provider is paid). In test mode with a lab-labeled substrate, PF-15..PF-20 report `skip (lab)`.

**Immutable after `plan.install-identity`:** `CLUSTER_NAME`, `BASE_DOMAIN`, `S3_ENDPOINT`, `S3_BUCKET_PREFIX`, `S3_PROVIDER`, `PROVIDER`. Exception: `S3_ENDPOINT`, `S3_PROVIDER` and `S3_BUCKET_PREFIX` may change only through `migrate-object-storage <cluster>` (procedure `replace-object-storage.md`, plan 14; DR-13), which re-runs storage-apply, credential minting, a bundle re-seal, a storage and compute `state import` rebuild (plan 06 §20.4) and a CNPG generation bump. `PROVIDER` (and the provider-specific variables) may change **only in the `recover` pipeline**, with the typed confirmation `migrate-provider <cluster>` (journal kind `migrate-provider`; plan 45 S17, Q-45-2). Changing any immutable field otherwise is detected via `envDigest` and fails with `CONFIG_IMMUTABLE_CHANGED` (exit 2). Changing them is a new installation or a documented migration, not an edit. The following are mutable with effect: `EXPECTED_NODE_COUNT` (only for expand), `MACHINE_CLASS`, `MAX_MONTHLY_COST`, notification settings and credentials. Changing a credential value requires `./bootstrap rotate refresh-bundle` before the next mutating run. Preflight detects this by comparing the credential's fingerprint (sha256, stored in the bundle manifest) with the value in `bootstrap.env` (`PF-21`).

### 9.3 Orchestrator settings in Git

`infra/bootstrap/settings.yaml` (`kind: BootstrapSettings`; schema is inline in this plan and validated by `make contracts` as a `$defs` example in `bootstrap-config.schema.yaml` once the implementation adds it) holds phase timeouts, retry ceilings and verify windows (§16). Default timeouts: preflight 10 m, plan 30 m (excluding time spent waiting at the confirmation prompt), provision 60 m, configure 40 m, seed 45 m, forge-init 20 m, flux-bootstrap 30 m, verify 120 m, control-transfer 5 m. Changing it requires a PR (protected path, plan 01 §13.2).

## 10. Secrets

Consumed: `SC-INFRA-PROVIDER`, `SC-DNS-API` (toolbox only), `SC-DNS-ACME` (written to SOPS for cert-manager), `SC-GITHUB-MIRROR-READ`, `SC-OBJECT-STORAGE` (instance `admin`, plus operator-supplied instances), `SC-GITHUB-MIRROR`, `SC-GITHUB-PROBE`, `SC-NOTIFY-TRANSPORT`, `SC-MODEL-API-KEYS` (all from `bootstrap.env`), `SC-SSH-OPERATOR` (new, §10.1), `SC-RECOVERY-PASSPHRASE` (prompt).

`SC-DNS-TUNNEL` (the tunnel credential) is created by `sc-bootstrap tunnel ensure` (plan 09) with `SC-DNS-API` before the seed commit and SOPS-encrypted into the cluster directory. Generated once in `plan.install-identity`, all held in memory, sealed into the recovery bundle and/or SOPS-encrypted into Git as plan 35 assigns: `SC-AGE-CLUSTER`, `SC-AGE-RECOVERY` (private part written only to `/blackstart/<cluster>/recovery-identity.txt`), `SC-TOFU-ENCRYPTION`, `SC-K3S-TOKEN`, `SC-FORGEJO-ADMIN`, `SC-AUTHENTIK-BOOTSTRAP`, `SC-GRAFANA-ADMIN`, `SC-BREAK-GLASS-*`, `SC-PG-APP` seeds, and any other class plan 35 marks `creator: sc-bootstrap`.

**All bundle-resident material is generated before provision** (50-R-4). The Flux deploy key, the Forgejo SSH host key and the webhook HMAC are pre-seed material generated in `plan.install-identity` (plan 19 §8.2, plan 16 §4), not by later phases. The bundle is **re-sealed at most once during an install run**, in `plan.storage-credentials`, before its first confirmation (BOOT-19). Secrets that only exist after Forgejo or Woodpecker run (bot tokens, agent tokens) are SOPS-encrypted to both recipients and committed by forge-init and ci-init. The offline recovery identity can therefore always decrypt them.

### 10.1 Bootstrap-specific secret classes

`SC-SSH-OPERATOR` and `SC-K3S-ADMIN-KUBECONFIG` are registered in `contracts/secret-classes.yaml` (plan 35); the descriptions below state how this plan uses them, and plan 35's register must record the storage path `/blackstart/<cluster>/kubeconfig`. `SC-LAB-SSH` is also registered there (ephemeral).

- **`SC-SSH-OPERATOR`**: the operator's SSH key pair used by bootstrap for node access. Creator: operator. Consumer: sc-bootstrap / Ansible. Storage: operator machine, mounted read-only. The public key is injected into nodes. Rotation: `rotate-secrets.md` (re-run configure with the new key, then remove the old key from `authorized_keys`). Recovery: operator-held; the infrastructure break-glass path (plan 35) covers its loss.
- **`SC-K3S-ADMIN-KUBECONFIG`** (operator identity, AUTH-7): a kubeconfig at `/blackstart/<cluster>/kubeconfig` (0600) holding a **distinct** client certificate `CN=sc-operator:<operator-user>`, `O=sc:cluster-admins`, valid ≤ 30 days, signed on a server node by the k3s client CA over SSH (`sc-bootstrap kubeconfig refresh`; `openssl` against `/var/lib/rancher/k3s/server/tls/client-ca.{crt,key}` (verify paths)). `/etc/rancher/k3s/k3s.yaml` (`system:admin`) never leaves the node; it is the break-glass identity, so its audit events are distinguishable (plan 15 `SCK3sBreakGlassAuditSeen`). The group is bound to `cluster-admin` by ClusterRoleBinding `sc-cluster-admins`, which plan 07 ships as a k3s auto-deploy manifest. Server URL `https://127.0.0.1:<port>` through the SSH tunnel (§14.4). Refresh runs automatically when < 7 days remain. Recovery: re-issued over SSH from any server node.
- **`SC-LAB-SSH`**: an ephemeral SSH key generated per CI lab run (plan 05). Never stored beyond the run.

### 10.2 Handling rules (ADR-0101)

1. Generated secrets exist at rest on the operator machine **only** inside the passphrase-encrypted bundle and the SOPS files. The one exception is `recovery-identity.txt`, which ADR-0012 requires to be written once. The report and fresh-install procedure instruct the operator to move it offline and delete the local copy, and preflight warns while it remains (`PF-23`).
2. Every invocation that needs generated secrets prompts once for the recovery passphrase, opens the bundle in memory, and places derived material in the container tmpfs `/run/sc` (mode 0700): `SOPS_AGE_KEY` env for SOPS subprocesses, `TF_ENCRYPTION` env for OpenTofu (plan 06), and `/run/sc/ansible/vars.json` for Ansible. The tmpfs disappears with the container.
3. Every secret value is registered with the log redactor before first use. Every byte written to logs, tool logs, reports, the journal and S3 run copies passes through it. A redacted value appears as `«redacted:SC-CLASS»`.
4. `SC_RECOVERY_PASSPHRASE_FILE` is accepted for automation (CI lab, scheduled E2E). Preflight emits warning `PF-24` when it is used with a paid provider.

## 11. Storage

- `.bootstrap/` (gitignored): `journal.json`, `run.lock`, `known_hosts`, `runs/<run-id>/`, `work/<root-module>/` (OpenTofu working directories, plan 06), `work/ansible/`. **Retention:** the newest 20 run directories are kept. Older ones are deleted at the start of each run, but the run referenced by the latest successful `control-transfer` is never deleted [SPEC §107]. `work/` is recreated as needed.
- S3, under `<prefix>-tofu-state/<cluster>/`: `journal/journal.json`; `locks/bootstrap.lock`; `runs/<run-id>/log.jsonl.gz` and `preflight.json`, `verify.json`; `reports/`; `provider/output.json`. The lifecycle (noncurrent versions ≥ 90 days; `runs/` expire after 180 days) is applied by plan 14's bucket definition and owned by plan 43.
- `/blackstart/<cluster>/`: `bundles/sc-recovery-<cluster>-v<N>.tar.age` (+ local `bundles/LATEST.json`), `recovery-identity.txt` (until the operator removes it), `kubeconfig`, and a `README.txt` explaining each file and what to keep offline.

## 12. Resource requirements

Operator host: 2 CPU, 4 GiB free RAM, 5 GiB free disk (toolbox image about 1.2 GiB; tool logs and OpenTofu provider plugins under 500 MiB). Wall time on a preferred-shape node is a recorded measurement (target ≈ 150 min including ≤ 60 min provider delivery, warn above 180 min; BOOT-15), not a pass/fail criterion. Cluster-side: none (sc-bootstrap is off-cluster). The verify memory measurement runs Prometheus queries only.

## 13. Security boundary

- `sc-bootstrap` runs with the operator's full authority: provider, DNS, S3 admin, GitHub mirror token, and cluster-admin through `SC-K3S-ADMIN-KUBECONFIG`. It is trusted code built from the checked-out commit, inside a pinned toolbox.
- **Network exposure:** none. It makes outbound calls only. The Kubernetes API is reached through an SSH local forward (`127.0.0.1:16443 → <node internal_ip>:6443`), never through a public 6443 port (SPEC §18; plan 08 firewall). Forgejo is reached through `kubectl port-forward` until public ingress is verified.
- **Repo-content trust:** sc-bootstrap executes Ansible and OpenTofu from the checked-out tree. Preflight prints the HEAD SHA and warns if HEAD is not an annotated release tag `v*` (`PF-05`). Installing unreleased commits is allowed but recorded.
- Generated commits are scope-checked. A commit may only touch `gitops/clusters/<cluster>/**`, `secrets/offcluster/<cluster>/**`, `secrets/ci/<cluster>/**` (plans 17, 23, 35; BOOT-10d) (plan 01 §6.5) and `.sops.yaml`. **One exception, recovery commits only** (ADR-0554, plan 45 Q-45-5): the `recover.commit` step (whose regular scope already covers `.sops.yaml` and the fenced SOPS files under `gitops/clusters/<cluster>/secrets/**`, DR-4) may also change `policy/budgets.yaml`, and only its `spec.emergencyStop` field, only to the value `workers` (or `all` when the pipeline runs with `--drill`, DR-9); the check parses the file before and after and fails with `COMMIT_SCOPE_VIOLATION` if any other byte-level YAML node differs. The finalize PR (`recover --step finalize`) resets it to `none` through review. Any other path in the staged diff aborts with `COMMIT_SCOPE_VIOLATION`.

## 14. Happy path

### 14.1 Installation lifecycle states

`new → planned → provisioned → configured → seeded → forge-initialized → flux-sourced → verified → transferred` (`sso-init` and `ci-init` run in state `flux-sourced`). After that: `transferred → expanding → transferred`, `transferred → destroying → compute-destroyed`, `compute-destroyed → recovering → … → transferred`, and `* → recovering` for black start on a new machine. `installation.state` advances only when the corresponding phase succeeds.

### 14.2 preflight (always re-run; read-only)

Preflight runs at the start of every mutating command, **scoped by command** (SEEDFLUX-B17): `handoff-rollback`, `status`, `exec`, `shell`, `unlock`, `sync-local` and cluster-only repairs run only PF-01, PF-02 and PF-22; after `forge-initialized`, PF-16 downgrades GitHub *reachability* failures to `warn` (credential or ancestry failures still fail), so Forgejo-side operation never depends on GitHub (SPEC §32). A check with side effects runs once per `envDigest` and its result is cached in the journal. Each check has an ID, a status of `pass`, `warn` or `fail`, a message and a remediation that names a procedure section. Any `fail` exits 3. The output is written to `runs/<run-id>/preflight.json` and printed as a table.

| ID | Check | Owner |
|---|---|---|
| PF-01 | Toolbox self-check: every tool's `--version` matches `toolbox/tools.lock.yaml`; running image digest recorded | 02 |
| PF-02 | `bootstrap.env` parses; no unknown keys; normalized config validates against `bootstrap-config.schema.yaml` | 50 |
| PF-03 | Profile file exists, validates, and has the right kind for the command; substrate floor rules (plan 03 §9) | 03 |
| PF-04 | Repo: inside a Git work tree; branch `main`; no tracked modifications; no `gitops/clusters/<cluster>/` from a *different* `install_uid` | 50 |
| PF-05 | HEAD is a `v*` annotated tag (warn otherwise) | 50 |
| PF-06 | SSH key readable, type ed25519 or RSA ≥ 3072, public key derivable and equal to `SSH_PUBLIC_KEY_PATH`; agent socket usable if `SSH_USE_AGENT` | 50 |
| PF-07 | Provider credentials valid; account usable (plan 04: `/me`, payment method present; plan 05: SSH reachability of every node) | 04/05 |
| PF-08 | Product/machine class exists in the catalogue, is offered in the region, and meets the substrate minimum (SPEC v1.2: ≥ 100 GiB disk; 75 GiB-class products and nodes are rejected, CD-25); warns below preferred (12 GiB memory, 200 GiB NVMe); 8 GiB memory is allowed as `constrained` with a warning (CD-23) | 03/04 |
| PF-09 | Node count: `EXPECTED_NODE_COUNT` equals the profile `nodes.count` (install) or the target (expand) | 03 |
| PF-10 | Region is in the provider's allowlist | 03/04 |
| PF-11 | Cost: catalogue estimate ≤ `MAX_MONTHLY_COST`, catalogue fresh (plan 03 §14.5) | 03 |
| PF-12 | S3 endpoint reachable over TLS; credentials valid; `existing` mode: every bucket in plan 14's set exists with versioning `Enabled` | 06/14 |
| PF-13 | S3 server clock skew ≤ 60 s (`Date` header) | 06 |
| PF-14 | S3 conditional-write and lock conformance (plan 06 §14.3). In `create` mode before the bucket exists this is deferred to `plan.state-conformance` and reported `deferred` | 06 |
| PF-15 | Cloudflare token valid (`/user/tokens/verify` (verify)); account and zone accessible with plan 09's permission set | 09 |
| PF-16 | GitHub mirror repository exists, mirror/probe/read tokens valid (plan 17 §14.1.3), repository is private (or `GITHUB_MIRROR_ALLOW_PUBLIC=true`). Ancestry: before `forge-initialized`, GitHub `main` is an ancestor of local HEAD (or empty); afterwards local HEAD must be an ancestor of or equal to Forgejo `main` (else `LOCAL_BEHIND`, hint `./bootstrap sync-local`) (BOOT-21). In `recover` with `seed-without-github` the check is `skip` (DR-14) | 17 |
| PF-17 | Notification transport: test message accepted by the transport (SMTP 250 / HTTP 2xx), once per config digest; the operator's nonce acknowledgement (plan 42) is optional and recorded as `notification-nonce`, never required for the pipeline (BOOT-15) | 42 |
| PF-18 | Heartbeat URL reachable (HTTP 2xx on a ping); warn if unset (ADR-0009) | 42 |
| PF-19 | Model API keys: one non-billable authenticated call per provider (plan 53 defines the endpoint, e.g. list models); a key that is empty, or whose value matches a `forbiddenKeyPrefixes` entry of `policy/model-routing.yaml`/plan 53 (subscription or OAuth tokens, SPEC §77), fails with `MODEL_KEY_FORBIDDEN_PREFIX` | 53 |
| PF-20 | Base domain resolves at Cloudflare (zone active) | 09 |
| PF-21 | Credential fingerprints match the bundle manifest (only after the bundle exists) | 50 |
| PF-22 | Journal reachable and consistent (no `JOURNAL_CONFLICT`); no foreign run lock | 50 |
| PF-23 | `recovery-identity.txt` still present locally (warn) | 50 |
| PF-24 | Passphrase from file with a paid provider (warn) | 50 |
| PF-25 | Toolbox source `built` while `toolbox/image.lock` names a published digest (warn; recorded in report) | 02 |
| PF-26 | Model budgets: print `policy/budgets.yaml` `global.monthlyUsd`, `global.dailyUsd` and per-provider limits next to the infrastructure footprint, and require a typed confirmation `budget <cluster> <monthlyUsd>` or `--confirm-budget <monthlyUsd>` (journal kind `model-budget`, once per budget-file digest; plan 33 §14) | 33 |
| PF-28 | Egress placement (plan 36, SEED-3): an `S3_ENDPOINT` that resolves to a private address or uses a port ≠ 443 requires `S3_PRIVATE_CIDR` (else `S3_ENDPOINT_PLACEMENT_UNSUPPORTED`, exit 3); node resolvers found by discovery become `SC_DNS_UPSTREAM_1..3` (unused slots repeat slot 1); a resolver that no node can route to is `warn` | 36 |
| PF-29 | Provider spend backstops (BUD-B6, plan 33 §9.2a): for every enabled production model provider, a provider-side hard monthly limit or a prepaid balance with auto-reload off, ≤ 1.25 × that provider's `monthlyUsd`. Verified through the provider's admin API where one exists (verify per provider), otherwise a typed attestation `backstop <provider> <limit-usd>` (or `--confirm-backstops <file>` / `SC_CONFIRM_BACKSTOPS`), which also attests that **MFA is enabled on the provider console account**; the result is written to `gitops/clusters/<cluster>/config/provider-backstops.yaml` (journal kind `provider-backstop`). Missing → exit 5 | 33 |
| PF-30 | Service CIDR and pinned IPs (CD-37): the Traefik ClusterIP is derived as the **20th address** of `SC_SERVICE_CIDR` (`10.43.0.20` for the default `10.43.0.0/16`) and written to `cluster-vars` `SC_TRAEFIK_CLUSTER_IP` (plans 08, 09, 18). Fail with `SERVICE_CIDR_TOO_SMALL` if the CIDR holds fewer than 64 addresses, and with `SERVICE_CIDR_IP_COLLISION` if the derived IP equals the cluster DNS IP (the 10th address, k3s default) or any other pinned ClusterIP | 09 |
| PF-27 | Object-storage protection level: if plan 14's capability suite yields `weak`, fail unless `S3_ALLOW_WEAK_PROTECTION=true`, which is recorded as confirmation `accept-weak-protection` | 14 |

### 14.3 plan

Steps, in order, each journaled:

1. **`plan.install-identity`.** If the journal has no `install_uid`:
   - generate the UUID and every pre-generatable secret (§10) in memory;
   - prompt for the new recovery passphrase twice (≥ 20 characters, or ≥ 5 space-separated words; no leading or trailing whitespace; must not contain `CLUSTER_NAME`);
   - seal the bundle (plan 35 manifest; age scrypt via the Go library);
   - write `/blackstart/<cluster>/bundles/sc-recovery-<cluster>-v<N>.tar.age` atomically (tmp, fsync, rename) and `recovery-identity.txt`;
   - render `gitops/clusters/<cluster>/` from `gitops/clusters/_template/` (plans 18 and 19 own the template), with `bootstrap-config.yaml`, `cluster-vars.yaml` and SOPS files encrypted to both age recipients, and add a `.sops.yaml` creation rule for `gitops/clusters/<cluster>/.*\.sops\.yaml$`;
   - write the render-time egress variables into `cluster-vars`: `SC_S3_CIDR`/`SC_S3_PORT` (only when `S3_PRIVATE_CIDR` applies; plan 36 then includes the `s3-egress` component), `SC_DNS_UPSTREAM_1..3` (rewritten by provision from discovery; seeded with the toolbox resolver until then) `SC_POD_CIDR`/`SC_SERVICE_CIDR` and the derived `SC_TRAEFIK_CLUSTER_IP` (PF-30, CD-37);
   - render the CNPG creation window `gitops/clusters/<cluster>/vars/sc-cnpg-create-window.yaml` (ConfigMap `flux-system/sc-cnpg-create-window`, `mode: initial-install`; plan 37 VAP `sc-cnpg-cluster-create-guard`, ADM-63, DR-7), so the seed and post-transfer layers may create the production CNPG `Cluster`s;
   - run `sc-gitops gen cluster-config --cluster <cluster>` so `gitops/clusters/<cluster>/cluster-config/kustomization.yaml` lists every cluster-directory manifest (plan 18, SEED-1). **Every** phase that writes a Kubernetes manifest into the cluster directory (install-identity, storage-credentials, seed-prep, forge-init, ci-init, `recover.commit`/fence) regenerates it in the same commit and lists it among its outputs;
   - write the operator-supplied credentials that in-cluster components need as SOPS files in the cluster directory: `SC-DNS-ACME` (cert-manager), `SC-GITHUB-MIRROR-READ` (RT-6, CD-19 b), `SC-GITHUB-PROBE` (platform probes), notification transport secrets;
   - commit with author `sc-bootstrap <sc-bootstrap@<baseDomain>>`, subject `bootstrap(<cluster>): installation config`, trailers `SC-Install-Uid:` and `SC-Run-Id:`.

   *Done check:* the bundle file exists and opens with the passphrase; its digest matches the journal; HEAD contains a commit with trailer `SC-Install-Uid: <id>`; and the files and keys it owns (below) equal a fresh render, ignoring SOPS ciphertext but comparing decrypted content. *Resume:* if the bundle exists but no commit does, re-render **from the bundle contents** and commit. If the journal is missing but the bundle exists, the bundle is the source of truth.
   *Ownership (BOOT-9):* install-identity owns exactly the files and keys it renders (`bootstrap-config.yaml` minus `metadata.generatedAt`/`generatedBy`, `cluster-vars.yaml` minus the keys provision owns: `SC_NODE_IP_1..5`; the pre-seed SOPS files; `.sops.yaml` rules). Its done check compares only those, decrypting SOPS content. Once `installation.state ≥ planned`, it **never re-renders** unless the cluster directory is absent from HEAD (the §20 new-machine case).

The authoritative step table (BOOT-10). Plans 06 and 14 reference these step names; the credential column states which `SC-OBJECT-STORAGE` instance each step uses.

| # | Step | What | Credential | Done check |
|---|---|---|---|---|
| 2 | `plan.storage-confirm` | Print the storage footprint: bucket set, Object Lock periods per bucket, principals/keys that will be created, and the statement "usage-billed; cost bounded by retention; Object-Locked objects cannot be deleted for up to N days". Require typed `storage <cluster>` or `--confirm-storage <cluster>` (kind `storage-footprint`; BOOT-12, SPEC §14). In `existing` bucket mode only the verification list is printed and no confirmation is needed | — | confirmation recorded for the current storage-plan digest |
| 3 | `plan.storage-apply` | `create` mode: storage root module with an encrypted local state; guard (plan 06 §14.2); apply. `existing` mode: verify only. Then acquire the **remote run lock** | `admin` | every plan 14 bucket exists with versioning `Enabled` |
| 4 | `plan.bundle-upload` | Upload the bundle (generation N) and push the journal to S3 for the first time, **immediately**, so the early window holds only the local storage state (BOOT-6) | `admin` (the `recovery` instance does not exist yet) | object present, sha256 equal, `s3_version_id` recorded |
| 5 | `plan.storage-credentials` | plan 14 §14.2 step 4 (`EnsureCredentials`): mint one key per instance (`scoped`), take `S3_CRED_<INSTANCE>_*` (`existing`), or reuse `admin` (`shared`). In-cluster instances → SOPS in the cluster directory; operator-side instances (`tofu-state`, `recovery`, `seed-write`) → `secrets/offcluster/<cluster>/` and the bundle, which is **re-sealed once** (generation N+1) and re-uploaded with `recovery`. The remote run-lock holder switches to `tofu-state`. **State protection rule (BOOT-3, plan 14 14-R-2a):** in `shared` mode the buckets `tofu-state` and `recovery` live in a **second object-storage account** whose key the cluster never holds (`S3_STATE_ACCESS_KEY_ID`/`S3_STATE_SECRET_ACCESS_KEY`, optional `S3_STATE_ENDPOINT`); step 3 creates them with that key. Without it the step fails closed with `OBJSTORE_STATE_UNPROTECTED` (exit 3) unless `S3_ALLOW_UNPROTECTED_STATE=true`, recorded as `accept-state-unprotected` and reported and alerted permanently | `admin` | every instance authenticates (`HeadBucket`); SOPS files decrypt; bundle N+1 uploaded |
| 6 | `plan.objstore-capabilities` | plan 14 §14.3 suite; writes `objstore-capabilities.json`; fatal results exit 3; `weak` needs PF-27 | `admin` + minted instances | suite result recorded for the current credential set |
| 7 | `plan.state-conformance` | plan 06 §14.3 (C1–C8). Fails closed with `PF_S3_LOCK_UNSUPPORTED` / `S3_VERSIONING_UNSUPPORTED` | `tofu-state` (cleanup of the conformance prefix: `admin`) | result recorded |
| 8 | `plan.state-migrate` | plan 06 §14.4 | `tofu-state` | remote object decrypts; address set equal; local state deleted |
| 9 | `plan.bundle-confirm` | Clear the screen; show what to store offline; re-enter the passphrase; download the S3 copy (generation N+1) and open it in a fresh decoder. Three wrong attempts exit 8; the next run asks again; recovery from a lost passphrase at this point is `./bootstrap abandon-identity` (§15). No skip flag | `recovery` | `recovery_bundle.status = confirmed` |
| 10 | `plan.compute-plan` | plan 03/04/05; always recomputed until `provision` succeeds | `tofu-state` | — (never cached) |
| 11 | `plan.confirm` | Footprint (plan 03 §14.7); paid: `create <cluster> <D8>` / `--confirm-plan D8`; `existing`: `<cluster>` / `--confirm-plan D8` of the node-list digest; then PF-26's budget confirmation | — | confirmations recorded for the current digests |

`installation.state = planned`.

### 14.4 provision → configure

- **provision** (plan 03 §14.8; credential `tofu-state`): orphan scan, apply the saved plan whose digest was confirmed, post-apply audit, inventory (contract `provider.schema.yaml`), wait for SSH (timeout 15 min within the phase), and pin host keys into `.bootstrap/known_hosts` and `inventory.yaml`. Commit `gitops/clusters/<cluster>/inventory.yaml`, the node-shape fields of `config/cluster.yaml`, and `vars/cluster-vars.yaml` keys `SC_NODE_IP_1`..`SC_NODE_IP_5` (slot 1 = node-01's `internal_ip`; unused slots repeat slot 1, plans 36/47) with subject `bootstrap(<cluster>): node inventory`. *Done check:* a compute re-plan shows no changes, and the inventory node count equals expected.
- **seed-prep** (new phase after provision, BOOT-10b; plan 09 §14.1): `sc-bootstrap tunnel ensure` (tunnel mode), `gen public-hosts`, `dns apply` (needs node IPs in direct mode), commit `bootstrap(<cluster>): edge` (tunnel credential SOPS, `config/public-hosts.yaml`). *Done check:* the tunnel exists with the committed credential's id; every public host resolves as planned.
- **SSH user (BOOT-8):** every SSH client (Ansible inventory, discovery `Check()`, the shell/exec tunnel, decommission) uses the node's `ssh.admin_user` from `inventory.yaml` when set, else `ssh.user`; when the record is missing or stale it tries `sc-admin` first, then the initial user. Configure's admin-transition step (plan 08 §14.1 step 4) commits `inventory.yaml` with `admin_user: sc-admin` (`bootstrap(<cluster>): admin transition`); `provider/output.json` in S3 is updated the same way.
- **configure** (plans 07/08/09): render the Ansible inventory (group `k3s_servers`; host vars `sc_node_name`, `sc_internal_ip`, `sc_external_ip`, `ansible_host`, `ansible_port`, `ansible_user`) and `/run/sc/ansible/vars.json` (k3s token, versions from `versions/k3s.lock.yaml`, cluster settings). Run `site.yml`, then issue the operator kubeconfig (§10.1, AUTH-7: a distinct `sc-operator:<user>` client certificate signed on `node-01`; `k3s.yaml` itself is never copied off the node), with server `https://127.0.0.1:16443` with `tls-server-name: kubernetes`, and save it to `/blackstart/<cluster>/kubeconfig`. Every later step opens the SSH forward `ssh -N -L 127.0.0.1:16443:<internal_ip>:6443` with keepalives (`ServerAliveInterval=15`, `ServerAliveCountMax=4`) and re-establishes it on failure. *Done check:* `kubectl get nodes` shows the expected count Ready, the k3s version equals the lock, and an Ansible `--check` run reports `changed=0`. *Interface assumption (plan 07):* the k3s serving certificate includes the SANs `127.0.0.1` and `kubernetes`.

### 14.5 seed → forge-init → flux-bootstrap

These run the plan 19 / 16 / 19 internals with the operator kubeconfig. sc-bootstrap provides: the seed commit `S` (recorded as journal `facts.seed.commit`); the S3 seed credentials (plan 14: `seed-write` for the snapshot upload, `seed-read` for the Flux `Bucket` source, deleted at transfer); the cluster age key for `flux-system/sops-age`; and the forge-init inputs (Forgejo admin from the bundle, GitHub mirror token).

- **seed:** plan 19 §8.1–§8.4. The `GitRepository flux-system` and its deploy-key Secret are **not** created by the orchestrator: both are applied by the seed root from the Bucket snapshot (`flux-source-auth.sops.yaml`); exactly two Secrets are out-of-band (plan 19 §8.4) (SEED-8).
- **forge-init:** plan 16. It pushes `S` to Forgejo `main` and creates **exactly one** outputs commit (so `F != S`; SEEDFLUX-B19) `bootstrap(<cluster>): forge-init outputs` limited to plan 16 §4.4's output paths; the resulting head is `F` (journal `facts.forge_init.pushed_sha`). It does not commit anything else (no Flux `known_hosts` commit; the host key is pre-seeded, plan 19 §8.2). *Done check:* Forgejo `main` == `F` and descends from `S`.
- **flux-bootstrap:** plan 19 §8.5 T0–T8. Step `flux-bootstrap.sso-a0` runs plan 15's sso-init **A0** (bootstrap credential for Authentik's first start) **before** T2, so Authentik never starts without it (AUTH-3); the journal records `facts.sso_init.a0_done_at`, and A1+ run in the `sso-init` phase (§14.6). T2's switch is recorded as step `flux-bootstrap.switched` with `facts.transfer.switched_at`; from that moment §17.1 rule 5 freezes seed, forge-init and flux-bootstrap. The T1 pre-snapshot is uploaded by plan 19 to S3 and recorded as `facts.transfer.pre_snapshot` (SEED-7).

The local remote `forgejo` is added for the run only: **HTTP through the run-scoped loopback port-forward** (`http://127.0.0.1:<forwarded>/strange-company/strange-company.git` to `svc/forgejo-http`), authenticated as `sc-admin` with `GIT_ASKPASS` reading from `/run/sc/secrets/` (plan 16 is authoritative for the transport; the password never appears in a URL, argv or Git config; BOOT-24). The port-forward never leaves the operator's loopback.

### 14.6 sso-init, ci-init, verify and control-transfer

After `flux-bootstrap`, Flux reconciles the post-transfer layers (Authentik, Woodpecker, monitoring, …). The engine then runs:
1. **`sso-init`** (plan 15 §18, steps A1 onward; A0 ran inside flux-bootstrap): waits for layer `authentik` Ready, initializes Authentik (bootstrap admin lifecycle, OIDC apps, break-glass). It makes **no** Git commit (plan 15 records its outputs in the journal and in Authentik).
2. **`ci-init`** (plan 20 §14.2): waits for layer `woodpecker` Ready, creates Woodpecker users, tokens and repository activation, mints the **per-agent tokens** (W07) and commits them (SOPS) as `bootstrap(<cluster>): ci-init outputs`. Layer `woodpecker-agents` (plan 18 §8.3, 17a) can become Ready only after this commit is pushed to Forgejo `main` and reconciled; `ci-init`'s done check therefore includes `woodpecker-agents` Ready at the pushed SHA.
3. **`verify`** starts its reconcile wait (§14.8 step 1) only after `ci-init` succeeded, so the "all Kustomizations Ready" condition includes `woodpecker-agents`.
4. **`verify.dr-prime`** (CD-19 a; BOOT-1b): before the checks, create one Job from every required check CronJob (`kubectl -n dr-tests create job --from=cronjob/<name> <name>-prime-<run>`, and `forgejo/repo-bundle-backup`), wait for completion (timeout 45 min, in parallel subject to heavy-slot admission), and record the results. Plan 44's former manual first-run step is replaced by this.

**Ordering of the final steps (SEEDFLUX-B15):** after the `verify` checks other than C1–C8 pass: (1) `control-transfer.close-cnpg-window`; (2) wait for the GitHub mirror to catch up with the new Forgejo `main`; (3) `forge finalize` (protection `steady`, `sc-admin` push whitelist removed); (4) **then** C1–C8 (V-01..V-08) are evaluated once on this final state (C3's push probe runs under the post-finalize credential model) and the report is printed. Step (1) commits the removal of `vars/sc-cnpg-create-window.yaml` (`bootstrap(<cluster>): close CNPG create window`), waits for the ConfigMap to be pruned at the new Forgejo `main` SHA, and re-runs V-05 (done check: ConfigMap absent). CNPG `Cluster`s introduced later (e.g. `sc-pg` at C01) need a window opened by their own reviewed PR (plan 37). In recovery, `recover.commit` renders the window with `mode: recovery` and `runId: <run-id>` (plan 45) and the finalize PR removes it.

All these commits are pushed while branch protection is still in the `bootstrap` stage (`sc-admin` whitelisted, plan 16); `forge finalize` (just before `CONTROL TRANSFER COMPLETE`) moves it to `steady`, after which 50-R-5 applies. See §14.8 and §14.11. On success, `installation.state = transferred` and `transferred_at` is set.

### 14.7 The default run on an already transferred install

`./bootstrap` runs preflight + verify and prints the report again. It **never** re-runs seed, forge-init or flux-bootstrap (50-R-5). A drifted earlier phase (for example a node missing from the inventory) is reported with the procedure to follow, and is not auto-repaired.

### 14.8 verify checklist (aggregate)

Every check is registered by its owning plan with `id`, `title`, `owner_plan`, `section` (`platform`, `observability`, `dr`, `recovery`, `ci`), `required` (bool), `requiredFrom` (DAG node), `timeout` and `evidence`. Sequence:
1. Wait until every Flux Kustomization and HelmRelease is `Ready=True` at the Forgejo `main` SHA (timeout `SC_VERIFY_RECONCILE_TIMEOUT`, default 60 m).
1a. Run `verify.dr-prime` (§14.6 step 4).
2. Wait `SC_VERIFY_SETTLE` (10 m).
3. Run the checks (independent checks run in parallel; at most 4 concurrent).
4. Run the memory measurement over `SC_VERIFY_MEMORY_WINDOW` (30 m).

The result is written to `verify.json` as an array of `{id, title, owner_plan, required, status: pass|fail|warn|skip, evidence, started_at, duration_ms}`. Any required `fail` fails the phase.

| ID | Check (required at P12 unless marked) | SPEC | Owner |
|---|---|---|---|
| V-01..V-08 | **SPEC §35 conditions = plan 19 C1–C8, the only implementation** (SEED-10): `sc-bootstrap verify control-transfer` runs C1–C8 in this run and V-01..V-08 are aliases reporting the C-results. The rows below state what each alias covers | §35 | 19 |
| V-01 | Forgejo healthy: `GET /api/healthz` = 200 in-cluster **and** at `https://git.<domain>/api/healthz` | §35 | 16 |
| V-02 | Repo `strange-company/strange-company` exists, default branch `main`, and `main` descends from `F` (journal `facts.forge_init.pushed_sha`) | §35 | 16 |
| V-03 | Push succeeds: plan 19 C3's push probe, branch **`sc-verify/push-probe-<run-id>`** (the one name used everywhere, SEED-10), read back, deleted | §35 | 19/16 |
| V-04 | Flux sources Forgejo: `GitRepository/flux-system` URL host is the Forgejo SSH Service; no `Bucket` named `flux-system` and none labeled `strange.company/seed=true`; in capsule mode (CD-33) exactly `Bucket/capsule-charts` is permitted and listed in the report (SEEDFLUX-B9); deploy key `read_only: true` | §35, ADR-0016 | 19/18 |
| V-05 | Flux reconciles: all Kustomizations and HelmReleases Ready at the Forgejo SHA; no suspended object | §35 | 18 |
| V-06 | Seeded releases are in GitOps state: plan 19 C6 = G1–G7 (UIDs from the T1 pre-snapshot unchanged; no HelmRelease install action after the switch) | §34, §35 | 19 |
| V-07 | Platform verification: aggregate of V-10..V-26, V-30..V-36 | §35 | 50 |
| V-08 | GitHub mirror works: `sc-bootstrap mirror verify --run-id <run-id>` (plan 17 §14.3): GitHub `main` SHA = Forgejo `main` SHA within 10 min, and a probe **tag** `sc-mirror-probe/<run-id>` created on Forgejo appears on GitHub within 5 min and is deleted on both sides afterwards (a tag, not a branch, because the push mirror's branch filter would not carry an `sc-verify/*` branch) | §32, §35 | 17 |
| V-10 | Nodes Ready = expected; k3s version = lock; etcd members = `kubernetes.servers` | §16 | 07 |
| V-11 | Node baseline conformance (`--check` changed=0) + external TCP/UDP scan of each public IP matches plan 08's allowlist | §17, §18 | 08 |
| V-12 | NetworkPolicy enforced: default-deny probe pod cannot reach a denied target; an allowed target succeeds | §18 | 36 |
| V-13 | Admission: live subset of SPEC §105 escape attempts denied (privileged, hostPath, foreign SA in `agent-jobs`) | §85, §105 | 37 |
| V-14 | `agent-jobs` has ResourceQuota, LimitRange, PSA `restricted` and default-deny | §89 | 51/36 |
| V-15 | Runner RBAC objects exist as rendered from the chart (SA, Role, RoleBinding); `agent-worker` SA has zero rights (`kubectl auth can-i --list`) | §84, §130 | 54/26 |
| V-16 | cert-manager Certificates Ready; none expiring within 14 days | §23 | 10 |
| V-17 | Every public hostname answers through the configured edge with a valid certificate; non-public services are not reachable from outside | §18, §23 | 09/10 |
| V-18 | Four CNPG clusters healthy; ≥1 completed base backup each; WAL archiving `ContinuousArchiving=True` | §25, §26 | 12 |
| V-19 | Longhorn healthy; all volumes `healthy` at the profile's replica count; backup target reachable | §27 | 13 |
| V-20 | Authentik healthy; automated OIDC login to Forgejo and Grafana; break-glass login to Forgejo, Grafana and Authentik works | §21, §22 | 15 |
| V-21 | Woodpecker healthy; a `make verify` pipeline on `main` succeeded on Woodpecker (trusted CI cutover, ADR-0018) | §36 | 20/52 |
| V-22 | Prometheus targets up for nodes, k8s, etcd, CNPG, Longhorn, Forgejo, Authentik, Flux, Woodpecker; Grafana platform dashboards provisioned | §91, §92 | 38/39 |
| V-23 | OTel Collector Ready and accepting OTLP | §93 | 40 |
| V-24 | A synthetic alert per required category is **accepted by the transport** (Alertmanager `alertmanager_notifications_total{integration,receiver}` increments without a matching `…_failed_total`, plus the transport response recorded by the test hook; plan 42), each carrying a per-run nonce; `Watchdog` heartbeat received by `HEARTBEAT_URL` (warn if unset). The human nonce acknowledgement is optional and post-report (`./bootstrap verify --ack-notifications`, kind `notification-nonce`) (BOOT-15) | §96, §97 | 42 |
| V-25 | Plan 44's `bootstrap` restore-test subset passed at least once (primed by `verify.dr-prime` in this run, CD-19) | §109 | 44 |
| V-26 | Running internal images are digest-pinned and verify against the **published upstream release signatures** (public key under `security/cosign/`, digests from `versions/internal-images.lock.yaml`, images on GHCR; plan 23). Required at P12. The installation's **own** release chain (`supply-chain init`, trusted-keys PR, first own release and promotion) is post-transfer, gated at P11/C01, and is *not* part of the Bootstrap DoD (CD-19 c) | §37 | 23 |
| V-30 | **Memory budget** (ADR-0020, §14.8.1) | §11, §89 | 50/51 |
| V-31 | **SPEC §130 Definition of Done** (§14.8.2) | §130 | 50 |
| V-32 | Recovery material: bundle `confirmed`; S3 object present at the journal version id with matching sha256; recovery bucket versioning Enabled | §111, ADR-0012 | 50/35 |
| V-33 | OpenTofu state, **per module that exists** (BOOT-4; `storage` iff bucket mode `create`, `compute` iff the provider uses OpenTofu): remote, versioned, lock conformance passed, raw object is an encrypted envelope; `./bootstrap state verify` result ≤ 1 h old (a result with `modules: []` passes for `existing` + `existing`) | §15 | 06 |
| V-34 | SOPS: every `*.sops.yaml` under `gitops/clusters/<cluster>/` lists both age recipients and decrypts with the cluster key | §98, ADR-0012 | 35 |
| V-35 | Edge: cloudflared connections ≥ 1 healthy (tunnel mode), or 80/443 open and serving (direct mode) | §23 | 09 |
| V-37 | DR readiness (`--check dr`, plan 44 Q-44-5): `sc-restore-test last-results --json` shows a `passed` result younger than its `maxAge` for every `rt-*` test id required at P10 (`rt-cnpg-pitr-*`, `rt-repo-bundle`, `rt-longhorn-canary`, `rt-etcd-snapshot`, `rt-tofu-state`, `rt-github-mirror`) | §109 | 44 |
| V-38 | Seed snapshots retained per plan 43 R-31 (≥ 3 per cluster, current seed commit present) and `seed-gc` succeeded | §107 | 43 / 19 |
| V-36 | Self-Hosting Threshold platform list present (SPEC §117: k3s … OTel Collector). Hermes and Agent Runner checks become `required` once nodes C01/C08 ship (`requiredFrom`); until then they are `skip` | §117 | 50 |

#### 14.8.1 V-30 memory budget (ADR-0020 as refined by plan 51 §14.2 / ADR-0156)

Window `W = SC_VERIFY_MEMORY_WINDOW` (30 m) after `SC_VERIFY_SETTLE` (10 m), with the platform idle except for Prometheus scrapes and one synthetic `make verify` pipeline on Woodpecker. Per node `n` (the PromQL is plan 51 §14.2, verbatim):
- `platform_ws = max_over_time( sum(container_memory_working_set_bytes{container!="",container!="POD", namespace!~"agent-jobs|ci-jobs|ci-build|dr-tests|renovate", node="n"})[W:1m] )`
- `alloc = kube_node_status_allocatable{resource="memory", node="n"}`
- `worker_max` = largest `memoryLimit` in the active resource-profile set × (exclusive ? 1 : workers that can land on n)
- `ci_quota` = `ci-jobs` ResourceQuota `limits.memory`; `build_quota` = `ci-build` ResourceQuota `limits.memory`
- `heavy = exclusive ? max(worker_max, ci_quota, build_quota) : worker_max + ci_quota` (exclusive = the `strange.company/heavy-slot` mode of plan 51, CD-05)
- `requests_sum = sum(kube_pod_container_resource_requests{resource="memory", node="n", namespace!~"agent-jobs|ci-jobs|ci-build|dr-tests|renovate"})`

**Pass iff** (1) `platform_ws ≤ alloc − heavy − 0.10 × alloc`; (2) `min_over_time(node_memory_MemAvailable_bytes{node="n"}[W]) ≥ heavy`; (3) `requests_sum + heavy ≤ alloc`; (4) every platform container's p95 working set over `W` ≤ 1.10 × its memory request (the calibration table lists violations with the suggested request).

Evidence records every term in bytes, the node shape (provider output), size class, profile set, mode, window, PromQL text and the calibration table. On an 8 GiB node (allowed as `constrained`, CD-23) a failure produces `fail` plus the report line `this 8 GiB node does not pass the measured budget; use a larger node (CD-23)`; on 12 GiB plan 51 §14.4 defines the response (calibrate, then reduce profiles by PR, then the next machine class, plan 04 §9.2.1). The check is never auto-waived.

**Gateway checks** obtain a verify-only token from the Runner with `POST /v1/admin/gateway-tokens:verify` (plan 26; synthetic task `sc-0`, ≤ 10 min), authenticated with the operator's admin credential and `X-SC-Reason: bootstrap verify <run-id>` (no `X-SC-Actor`, CD-30). **Emergency stop tolerance (plan 33):** checks that call the Model Gateway (V-36 when Runner/Hermes are required) accept `SC_EMERGENCY_STOP` as the expected answer when an emergency stop is active (black-start recovery sets `workers`, ADR-0554); the report records the active level.

#### 14.8.2 V-31 SPEC §130 Definition of Done

Pass iff all of the following hold:
- (a) Every install-pipeline phase is `succeeded`, with `last_run_id` belonging to `sc-bootstrap` runs recorded in the journal. sc-bootstrap has no manual-completion path (50-R-2).
- (b) Each SPEC §130 item maps to a passing check:

| SPEC §130 item | Check |
|---|---|
| provisioning VPSes | provision succeeded + provider output valid |
| installing k3s | V-10 |
| installing bootstrap tooling | PF-01 (toolbox only) |
| configuring CNPG | V-18 |
| configuring Longhorn | V-19 |
| configuring Authentik | V-20 |
| creating Forgejo | V-01, V-02 |
| configuring Woodpecker | V-21 |
| wiring Flux | V-04, V-05 |
| configuring Prometheus/Grafana | V-22 |
| creating agent namespaces | V-14 |
| writing Runner RBAC | V-15 |
| implementing worker admission policy | V-13 |

- (c) The run's operator inputs were only `bootstrap.env`, the passphrase prompts and the typed confirmations (allowlist: `storage-footprint`, `paid-footprint` or `existing-nodes`, `model-budget`, `provider-backstop`, and optionally `accept-stale-prices`, `accept-weak-protection`, `accept-state-unprotected`, `notification-nonce`). Every other prompt type is forbidden in the install pipeline. Each allowlisted confirmation has a non-interactive flag form (§7.1), so the whole pipeline runs unattended (CD-19).

### 14.9 recover (black start; DR logic is plan 45)

Pipeline: `preflight → unlock → state-recover → plan → provision → configure → seed → forge-init → flux-bootstrap → restore → verify → control-transfer`, then two operator-gated phases `forge-reconnect` and `finalize` (run with `--step`). Plan 45 owns the step internals; this plan adds them to the engine:
- `recover.fence` (in `plan`, after `state-recover` and before any compute; full recovery only, never with `--scope state-only`; an installation that still answers requires typed `fence-live <cluster>`, 45-R-6): unless `--old-installation destroyed` (typed `destroyed <cluster>`, journal kind `old-installation-destroyed`), rotate the in-cluster write credentials, the GitHub mirror credential and the tunnel credential, and prove the old ones are rejected (plan 45 §14.2 step 2; `FENCE_UNVERIFIED`, exit 9). Fencing writes the rotated SOPS files **locally** into the cluster directory; they are part of the recovery commit (never a PR; DR-4). In `shared` credential mode fencing rotates `admin` and runs `refresh-bundle` (DR-30). In direct mode `dns apply` runs before seed (DR-30). Old compute addresses that cannot be reached are moved out of managed state per plan 03 §20.1 (`fence-nodes`, DR-12);
- `recover.commit` (before `seed`): create the local recovery commit (ADR-0554: recovery component, `config/recovery.yaml`, generation bump, `CNPG_<ID>_TARGET_TIME` for `--pitr-target`, and `policy/budgets.yaml` `spec.emergencyStop: workers` under the §13 exception);
- `recover.estop` (in `seed`): verify the Git emergency stop is in force before workloads start;
- `recover.reconcile-refs` (in `forge-init`, which runs with `--recover`): ref reconciliation against GitHub and bundles (fast-forward only; divergence halts);
- `restore` phase: `recover.restore` (plan 45 remaining-state restoration);
- `forge-reconnect` (`--step forge-reconnect`) and `finalize` (`--step finalize`: opens the finalize PR that removes the recovery component and resets `emergencyStop` to `none`, then `forge finalize` and a `black-start` bundle seal).

Flags: `recover [--scope full|state-only] [--bundle local|s3] [--old-installation destroyed|unknown] [--no-backups] [--from-capsule <s3-url|local dir>] [--pitr-target <RFC3339>] [--skip-longhorn-restore <vol,…>] [--drill --drill-source <cluster>] [--step forge-reconnect|finalize] [--confirm-plan D8]`. `--drill` (plan 45 §20.7 D3) uses `INGRESS_MODE=tunnel` with `BASE_DOMAIN=drill.<domain>` and a temporary drill tunnel (there is no special ingress mode), permits `emergencyStop: all` in the recovery commit (§13), requires `CLUSTER_NAME` ≠ `--drill-source`, and adds step `recover.drill-copy-objects`: after `plan.storage-apply` creates the drill's buckets, copy the source's `forgejo-objects` bucket (B7) **server-side** into `<prefix>-drill-forgejo-objects` with the `drill-reader` credential for reads (and the drill's `admin` for writes), verify object count and ETags, and record the copy in the journal; the source bucket is never written and builds a new installation identity that reads the source's backups.
- Inputs: a fresh clone from GitHub; `bootstrap.env` with credentials; the passphrase; the bundle from `/blackstart/<cluster>/` **or** `s3://<prefix>-recovery/<cluster>/bundles/sc-recovery-<cluster>-v<N>.tar.age (pointer `bundles/LATEST.json`, plan 35)`, chosen by `--bundle local|s3`, default local-then-s3.
- Config comes from the committed `gitops/clusters/<cluster>/bootstrap-config.yaml`. Credentials come from `bootstrap.env`. A mismatch in an immutable field fails with exit 2, except a `PROVIDER` change confirmed with `migrate-provider <cluster>` (§9.2).
- **unlock**: open the bundle, verify that the manifest's `install_uid` equals the committed `metadata.installUid`, and load the remote journal if one exists.
- **state-recover** (plan 06 §20): `tofu init` against the remote backend, decrypt, and compare state against the live provider inventory. `--scope state-only` stops here and reports.
- **plan** and **provision**: provider plan against the recovered state. Plan 45 decides whether surviving nodes are reused or replaced. Guardrails and confirmation apply unchanged.
- **`--from-capsule` (CD-33, plan 45 §9.4; implemented at H04):** black start with GitHub entirely unavailable. The operator downloads the capsule from `s3://<prefix>-recovery/<cluster>/capsule/<id>/` and clones its repository bundle; the wrapper loads the toolbox from the capsule's OCI archive after digest and signature checks (plan 02 §14.2 (d)). The pipeline then: verifies `capsule.json` with the trusted key from the unlocked bundle (`CAPSULE_SIGNATURE_INVALID`, exit 9); fast-forwards `main` from the newest `repo-backups` chain when newer (trust anchor, plan 45 §13); records `sourceKind: capsule` (journal `facts.recover.source_kind`, `config/recovery.yaml`); uses the capsule's provider mirror for OpenTofu (`TF_CLI_CONFIG_FILE` with a `filesystem_mirror`) and its collection tarballs for Ansible; copies the k3s air-gap bundle and every image archive to `/var/lib/rancher/k3s/agent/images/` on each node before k3s starts; uploads the chart archives into the seed Bucket and enables component `capsule-chart-sources` in the recovery commit (plan 18). PF-16 and GitHub-dependent steps are `skip` as with `seed-without-github`; finalize may use `defer-mirror`.
- **storage:** with `migrate-object-storage` (DR-13), when buckets are missing, and **always with `--drill`** (the drill has its own prefix), the recover pipeline runs `plan.storage-confirm` and `plan.storage-apply` before `state-recover`.
- **seed** uses plan 45's recovery components enabled by the recovery commit (ADR-0554; DR-22), with `seed-without-github` taking the seed source from the bundle's recorded commit when GitHub is down (DR-14). **forge-init** runs in plan 16's restore mode. **restore** runs plan 45's remaining-state restoration. **verify** uses the **recovery verify profile** (`--profile recovery`, DR-1): every P12 check except V-08 and V-37, which are replaced by `mirror compare-refs` (GitHub refs are ancestors of the Forgejo refs; nothing pushed), the first base backup under `g<N+1>` for each CNPG cluster, the observed RPO per database and healthy restored Longhorn volumes; its report is titled **`RECOVERY VERIFIED — MIRROR PENDING`**. Only `--step finalize`, after V-08 `mirror verify` and one forced run of every restore test against the new generations (V-37), prints the control-transfer report: first line exactly `CONTROL TRANSFER COMPLETE`, second line `CONTROL TRANSFER COMPLETE (RECOVERY)` (45-R-7). With `--no-backups` (black start without object storage, `replace-object-storage.md`), restore steps are skipped, CNPG bootstraps with `initdb`, and the typed `accept-empty <cluster>` is required.

### 14.10 expand (logic is plan 47)

Pipeline (plan 47 §14, adopted verbatim; Q-47-01): `preflight → plan → provision → peer-commit → configure → platform-commit → converge-platform → data-commit → converge-data → verify`. `EXPECTED_NODE_COUNT` must equal the target profile count (PF-09). Guardrails and confirmation work as for install, with token `expand <cluster> <D8>`. The Runner's dispatch is paused for the whole run (`sc-bootstrap runner dispatch pause`, resumed after verify; plan 26).
- **peer-commit:** PR `bootstrap/expand-peers-<run-id>` sets `SC_NODE_IP_2` / `SC_NODE_IP_3` in `cluster-vars` (slots 4–5 keep node-01's IP); exit 75 until merged and the `policies` layer is Ready at the merge SHA. Joins must not precede it (NetworkPolicy node IPs).
- **configure:** baseline, firewall, TLS-SAN roll, pre-join snapshot, joins of node-02 and node-03 (plan 47 step names `expand.configure.*`).
- **platform-commit:** PR `bootstrap/expand-platform-<run-id>` with `bootstrap-config.yaml` (`profile: small-ha`), the three profile lines (`sc-gitops set-profile`), and platform replica changes, with `CNPG_INSTANCES=1` and `LONGHORN_REPLICAS=1` still; exit 75 until merged; **converge-platform** waits for every Kustomization Ready at the merge SHA.
- **data-commit:** PR `bootstrap/expand-data-<run-id>` setting `CNPG_INSTANCES=3`, `LONGHORN_REPLICAS=2`; exit 75 until merged; **converge-data** waits for CNPG clusters healthy at 3 instances and Longhorn volumes healthy at 2 replicas.
- PRs are opened as the Forgejo admin; the next run resumes after each merge (50-R-5).
- **telemetry PR** (after `verify`, separate, GC-06): if plan 41's `telemetry-decide` is positive, PR `bootstrap/expand-telemetry-<run-id>` enables component `components/telemetry-h03` (Loki, Tempo, otel-agent). It is not part of the expand pipeline's success condition.

`node add` and `node remove` reuse these phases for one node (plan 47 §14.4–14.5); `upgrade k3s` runs pipeline `upgrade-k3s` = `preflight → upgrade (one server at a time) → verify` (plan 48 §14.1); `configure` runs `preflight → configure` with the selected nodes/tags.

### 14.11 control-transfer report (exact contents)

Preconditions: every required verify check passed, and C1–C8 (V-01..V-08) passed **in this run** (plan 19's single-run rule, SEED-10); other checks may come from a previous run with the same input digest within the last 60 minutes. `report.json` validates against `contracts/control-transfer-report.schema.yaml` (plan 19) extended by this plan's fields, and embeds plan 19's `final` report; there is one report (S3 `…/tofu-state/<cluster>/reports/`). The report goes to stdout and to `report.txt`, `report.json` and S3. The text form is:

```text
CONTROL TRANSFER COMPLETE
Installation: <cluster>  install_uid=<uuid>  run=<run-id>
Started/finished: <ts> → <ts> (<duration>); phases: preflight <d> | plan <d> | provision <d> | configure <d> | seed <d> | forge-init <d> | flux-bootstrap <d> | verify <d>
sc-bootstrap: <version> (commit <sha>); toolbox: <digest> (<pulled|built>)
Repository commit installed: <sha> (<tag or "untagged">)
Profile: <profile> (substrate <name>); provider: <name>/<product_line>; region: <region>
Nodes:
  node-01  <public_ipv4> <public_ipv6>  <machine_class>  <cpu> vCPU  <mem GiB> GiB  <disk GiB> GiB <class>  <os> <kernel>
Paid footprint: <n> × <product> = <monthly_total> <cur>/month (committed minimum <x>; catalogue <version>; limit <max>)
OpenTofu state: s3://<prefix>-tofu-state/<cluster>/{storage,compute}/terraform.tfstate  versioning=Enabled  lock=s3-conditional-write  encryption=aes_gcm/pbkdf2 (enforced)
Forgejo: https://git.<domain>/strange-company/strange-company  main=<sha>
GitHub mirror: https://github.com/<repo>  main=<sha>  lag=<seconds>s
Flux source: GitRepository flux-system → Forgejo @ <sha>; Kustomizations Ready <n>/<n>; HelmReleases Ready <m>/<m>
Ingress: <tunnel|direct>; public hostnames: <every host in plan 09's public-hosts list, e.g. auth.<d> git.<d> ci.<d> grafana.<d> hermes.<d> prometheus.<d> alertmanager.<d>>
Verify: <pass>/<total> required checks passed; warnings: <list of IDs>
Memory budget (node-01): platform_ws=<GiB> ≤ budget=<GiB> (alloc <GiB>, worker_max <GiB>, ci_job <GiB>, headroom 10%) — PASS
Notifications: <transport> test delivered <ts>; heartbeat: <configured, last ping ts | ABSENT — total-outage detection is not configured>
Recovery bundle: s3://<prefix>-recovery/<cluster>/bundles/sc-recovery-<cluster>-v<N>.tar.age (pointer `bundles/LATEST.json`, plan 35) (version <id>, sha256 <8>) + /blackstart/<cluster>/bundles/sc-recovery-<cluster>-v<N>.tar.age; confirmed <ts>
Black-start material you must hold OFFLINE: recovery passphrase; recovery age identity (<path> — move offline and delete local copy); bootstrap.env credentials
Break-glass: see procedures/break-glass-access.md (credentials inside the recovery bundle)
Routine management now happens through Git: https://git.<domain>/strange-company/strange-company
Next: log in at https://auth.<domain>; expand with ./bootstrap expand --profile small-ha
```

**`report.json` fields added by this plan** on top of plan 19's `control-transfer-report.schema.yaml` (G4b adds them to that contract): `mode` (`install`|`recovery`|`drill`), `stage` (`control-transfer-complete`|`recovery-verified-mirror-pending`), `installation` (`cluster`, `install_uid`, `run_id`), `timing` (`started_at`, `finished_at`, `wall_time_seconds`, `phases[]` of `{name, duration_seconds}`, `wall_time_warn` bool), `sc_bootstrap` (`version`, `commit`), `toolbox` (`image_ref`, `source`), `repository` (`commit`, `tag`), `profile` (`name`, `substrate`), `provider` (`name`, `product_line`, `region`), `nodes[]` (`name`, `public_ipv4`, `public_ipv6`, `machine_class`, `cpu_count`, `memory_bytes`, `disk_bytes`, `disk_class`, `os`, `kernel`, `meets_preferred`), `paid_footprint` (`monthly_total`, `currency`, `committed_minimum`, `catalog_version`, `max_monthly_cost`), `storage` (`buckets[]`, `credential_mode`, `protection_level`, `state_protected` bool), `opentofu_state` (`modules[]`, `versioning`, `lock`, `encryption`), `forgejo` (`url`, `main_sha`), `github_mirror` (`url`, `main_sha`, `lag_seconds`), `flux` (`source`, `sha`, `kustomizations_ready`, `helmreleases_ready`), `ingress` (`mode`, `public_hosts[]`), `verify` (`required_passed`, `required_total`, `warnings[]`), `memory_budget[]` (per node: `platform_ws`, `alloc`, `heavy`, `requests_sum`, `min_mem_available`, `result`), `notifications` (`transport`, `test_accepted_at`, `heartbeat`, `acknowledged` bool), `recovery_bundle` (`s3_uri`, `version_id`, `sha256_prefix`, `local_path`, `confirmed_at`), `confirmations[]` (`kind`, `method`), `model_budget` (`monthly_usd`, `daily_usd`).

The report contains no secret values. A unit test asserts that the report text passes the redactor unchanged and contains none of the generated secret values.

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Invalid or missing config | PF-02 | nothing done | exit 2, field-level errors | bootstrap-preflight-failure.md#config |
| Credential invalid or expired | PF-07/12/15/16/19 | nothing done | exit 3 | bootstrap-preflight-failure.md#credentials |
| S3 lacks conditional writes | PF-14 / `plan.state-conformance` | state unsafe | fail closed `PF_S3_LOCK_UNSUPPORTED`, exit 3 | bootstrap-preflight-failure.md#s3-locking |
| Guard refuses plan | plan 03 guard | nothing created | exit 4, offending resources printed | bootstrap-preflight-failure.md#guardrail |
| Operator declines confirmation | prompt | nothing created | exit 5; journal unchanged except the run | — |
| Bootstrap machine crashes mid-phase | next run: journal step `running` with stale `last_run_id` and no live lock holder | partial phase | `Check()` probe decides done or not done; re-run is idempotent | bootstrap-resume.md |
| Bootstrap machine lost entirely | — | local journal lost | remote journal and bundle on S3; new machine resumes from the S3 journal | bootstrap-resume.md#new-machine |
| Two runs concurrently | run lock | second run refused | exit 6 with the holder's details | bootstrap-resume.md#lock |
| Stale run lock (holder dead) | same host and `install_uid`, local OFD lock free → holder provably dead; otherwise lease expired or operator certain | blocked | same host: automatic takeover recorded as `stale-lock-takeover` (BOOT-5); foreign host: `./bootstrap unlock` with typed confirmation | bootstrap-resume.md#lock |
| Journal conflict (equal seq, different content) | load | ambiguous | exit 7, both copies written to `runs/<id>/journal-conflict/` | bootstrap-resume.md#journal-conflict |
| S3 write of journal fails after retries | write | durable record missing | phase fails `JOURNAL_REMOTE_WRITE_FAILED`, no further mutation | object-storage-outage.md (plan 14) |
| Passphrase forgotten before confirm | `plan.bundle-confirm` | bundle unusable | exit 8; nothing paid yet. `./bootstrap abandon-identity` archives the remote journal, re-imports the buckets into new storage state, and the next run generates a new identity (BOOT-6). Deleting local files alone does **not** work (PF-22 and the remote storage state would refuse) | fresh-install.md#passphrase |
| Passphrase forgotten after provision | unlock | cannot resume | offline recovery identity decrypts SOPS files; tofu passphrase is lost → state recovery by import (plan 06 §20.4) | recover-opentofu-state.md#lost-passphrase |
| Provider order delivered late | provision timeout | phase failed, order pending | rerun: orphan scan imports or waits; never re-orders (plan 04) | bootstrap-resume.md#provision |
| SSH tunnel to API drops | kubectl error | step error | re-establish tunnel, retry step (transient class) | — |
| Seed or transfer fails | plan 19 checks | platform partial | phase failed with plan 19 error code | seed-flux-handoff.md / failed-flux-handoff.md (plan 19) |
| Verify required check fails | verify | no transfer report | exit 1; per-check evidence + owning procedure | bootstrap-verify.md |
| Mirror lag | V-08 | not complete | retry window 10 min, then fail | github-mirror-failure.md (plan 17) |
| Memory budget fails | V-30 | not complete | fail; report ADR-0020 outcome | bootstrap-verify.md#memory-budget |
| Secret leaked to a log | redactor unit + E2E log scan (`make e2e-*` greps logs for generated values) | — | CI fails | credential-compromise (plan 35) |
| Repo dirty or wrong branch | PF-04 | nothing done | exit 3 | bootstrap-preflight-failure.md#repo |

## 16. Retry behavior

- **Error classes.** `transient` (network timeouts, connection refused while booting, HTTP 429/5xx, S3 503 SlowDown, OpenTofu state lock held < 60 s) and `permanent` (401/403, schema invalid, guard refusal, confirmation mismatch, conflicts). Each step's `Retryable(err)` decides.
- **In-run retries** apply to transient errors only: exponential backoff 5 s, 10 s, 20 s, 40 s, 60 s (cap), at most 5 attempts per step per run, always within the phase timeout.
- **Cross-run:** a failed phase is retried by rerunning `./bootstrap`. `attempt` increments. Once a phase reaches attempt ≥ 5 with the same error code and the same input digest, sc-bootstrap refuses further automatic attempts (`RETRY_CEILING`, exit 1) until the input digest changes or the operator passes `--retry-phase <name>`. That flag only resets the ceiling; it never marks anything done. This follows SPEC §4.8's spirit for a human-driven tool.
- **Paid creation is never retried blindly.** Provision re-runs always start with the orphan scan and a fresh plan (plan 03 §14.8).

## 17. Idempotency behavior

### 17.1 Rules

1. Every phase's `Check()` probes reality. The journal is a hint for ordering and reporting, never proof of completion.
2. Phase outputs are content-addressed where possible: commit trailers carry the install id, S3 objects are verified by sha256, and Kubernetes objects carry the `strange.company/install-id` label.
3. Re-running a succeeded phase with an unchanged input digest is a no-op recorded as `skipped (already done)`.
4. A changed input digest on a succeeded phase sets it to `pending` **only while `installation.state` has not advanced past that phase**. Once a later phase has consumed its output, `Check()=Done` wins over a digest change (BOOT-2): the drift is reported, not re-run. For `provision`, a re-run requires a new plan and confirmation.
5. **Freeze after the switch, keyed on reality (SEEDFLUX-B1).** Before T2's apply, plan 19's step T1b journals the intent `flux-bootstrap.switching` (with the `pre_snapshot` outputRef) and pushes the journal to S3. On any resume, `flux-bootstrap`'s `Check()` reads the **root Kustomization's actual `spec.sourceRef.kind`** first: `GitRepository` means switched, regardless of the journal, and T1 is never re-run (the pre-snapshot object is write-once, `If-None-Match: *`; plan 19). From the switch onward (the root is `GitRepository`, or the journal has `switching`/`switched`), seed, forge-init and flux-bootstrap are frozen: never re-run by the engine, including by `--from-phase`, which refuses them with `PHASE_FROZEN` (plan 19's `handoff-rollback` and the emergency procedures are the only paths back). This extends §14.7 from `transferred` to `flux-sourced`.
7. **Rollback residue first (SEEDFLUX-B2).** Before any phase runs, the engine checks for an interrupted rollback in reality: any Kustomization labeled `strange.company/suspended-by=handoff-rollback`, or a root sourced from the `Bucket` with `prune: false`. If found, it resumes plan 19's rollback at the first incomplete sub-step (`rollback.suspend` → `rollback.root` → `rollback.reconcile` → `rollback.verify`, each journaled) before anything else, and never proceeds to T6 while residue exists (plan 19 precondition `TRANSFER_PRECONDITION:rollback-residue`).
8. **Seed identity is the tree, not the commit (SEEDFLUX-B3).** The seed is identified by the snapshot's `gitops_tree_sha` and manifest (journal `facts.seed.gitops_tree_sha`), which is machine-independent. On a clone that lacks `facts.seed.commit` locally (new machine): (a) if the Forgejo repository `strange-company/strange-company` **exists**, run `sync-local` before forge-init or flux-bootstrap, whether or not `pushed_sha` was journaled; (b) otherwise, if HEAD's `gitops/` tree differs from the seeded tree (re-rendered SOPS ciphertext), re-seed automatically at the new HEAD before forge-init, journaling `seed.reseed` with reason `commit-lost`. Plan 19's T0(b) compares trees (`gitops/` at `F` = snapshot tree plus only forge-init output paths).
6. Post-transfer phases are never re-run (§14.7).

### 17.2 Input digests (what each phase hashes)

| Phase | Inputs hashed |
|---|---|
| preflight | normalized config (secrets excluded), profile file, tools lock |
| plan | config, profile, catalogue version, guard files, `infra/tofu/**` tree hash (`git rev-parse HEAD:infra/tofu`) |
| provision | the confirmed plan digest |
| seed-prep | inventory node IPs, ingress mode, public-host list source |
| configure | inventory, `HEAD:infra/ansible` tree hash, `versions/k3s.lock.yaml`, config subset (domain, ingress mode, SSH CIDRs) |
| seed | journal `facts.seed.commit` (recorded when the seed starts; not the moving HEAD) and its `gitops/` tree hash |
| forge-init | `facts.seed.commit`, Forgejo-related config |
| flux-bootstrap | journal `facts.forge_init.pushed_sha` (`F`) |
| sso-init / ci-init | Forgejo `main` SHA, plan 15/20 config subset |
| verify | Forgejo `main` SHA, check registry version |

### 17.3 Journal persistence

Writes follow this order: local atomic write (tmp, fsync, rename), then S3 PUT. When the conformance test recorded If-Match support, the S3 PUT is conditional on the previous ETag; otherwise it is protected by the run lock alone. `journal_seq` increments on every write. On load, the higher `journal_seq` wins. When S3 is higher, local is overwritten (another machine progressed). Equal seq with different content raises `JOURNAL_CONFLICT`.

### 17.4 Run lock

- **Local:** an OFD `fcntl` lock on `.bootstrap/run.lock`, held for the process lifetime.
- **Remote:** the object `locks/bootstrap.lock`, created with `PUT If-None-Match: *` and containing `{run_id, command, user, host, started_at, lease_expires_at}`. The lease is 12 h. When If-Match is supported, the lock is renewed every 10 minutes with `PUT If-Match: <etag>`.
- **Release:** delete on exit, conditional on the ETag where supported.
- **Contention:** a holder that is present and not expired causes exit 6, **except** a same-host stale holder: if the lock's `host` and `install_uid` equal this machine's and the local OFD lock on `.bootstrap/run.lock` is free, the holder is provably dead and the lock is taken over automatically (confirmation kind `stale-lock-takeover`, no prompt) (BOOT-5).
- **Breaking:** `./bootstrap unlock` shows the lock content and requires typing `unlock <cluster> <run_id>`. It deletes the lock and records the confirmation `unlock-run-lock`.
- **Ordering with OpenTofu:** OpenTofu's own lockfile (plan 06) is independent and additionally protects state. The order is always run lock first, then OpenTofu lock.

## 18. Bootstrap behavior

This plan *is* the bootstrap behavior. Before the remote bucket exists (first run up to `plan.storage-apply`), only the local journal and lock exist. Both become remote in the same run. The CI lab and fake runs (§22) exercise the identical code path.

## 19. Upgrade behavior

- sc-bootstrap is versioned with the repository. A newer sc-bootstrap must read every older `v1alpha1` journal. A journal written by a newer version causes an older binary to refuse with `JOURNAL_FROM_NEWER_VERSION`.
- After transfer, upgrading platform components happens through Git (plan 48), never through `./bootstrap`. `./bootstrap verify` from a newer checkout may add checks. New checks start as `required: false` for one release (plan 48 policy).
- Toolbox upgrades are lock-file PRs (plan 02 §19).

## 20. Recovery behavior

- **Bootstrap machine loss mid-install** (BOOT-7, BOOT-27): if the journal records `facts.forge_init.pushed_sha`, the engine first fetches Forgejo `main` (port-forward, `SC-FORGEJO-ADMIN` from the bundle) and fast-forwards local `main` (also `./bootstrap sync-local`); it re-renders install files from the bundle **only** if Forgejo lacks the install commit. Host-key pins come from S3 `provider/output.json` (versioned); keys are never re-captured for nodes already recorded there (`HOST_KEY_PIN_MISSING` instead of TOFU). Otherwise: clone on a new machine, provide `bootstrap.env` and `/blackstart/<cluster>/bundles/sc-recovery-<cluster>-v<N>.tar.age` (or let it fetch from S3), then `./bootstrap`. The remote journal resumes the pipeline (bootstrap-resume.md#new-machine). The committed config must be present, so the operator pushes nothing. Before forge-init, the install commits exist only on the lost machine. sc-bootstrap detects that `gitops/clusters/<cluster>/` is missing from HEAD while the remote journal shows `plan` succeeded, and re-renders the files from the bundle (`plan.install-identity` resume rule). Inventory is re-derived from provider inventory.
- **Operator machine lost after transfer** (DR-29): `sc-bootstrap bundle export-env` rebuilds `bootstrap.env` from the bundle; PF-21 then matches by construction; pins come from S3 as above; `bootstrap-resume.md#lost-operator-machine`.
- **Total loss after transfer:** `./bootstrap recover` (§14.9; plan 45).
- **Journal loss** (local and S3): phases re-probe reality with `Check()`, so a rerun reconstructs the journal. Unknown `install_uid` is recovered from the committed `metadata.installUid` or the bundle manifest.

## 21. Observability

- **Logs:** JSON lines in `runs/<run-id>/log.jsonl` with fields `ts, level, run_id, pipeline, phase, step, attempt, event, msg, duration_ms, error_code`. Tool subprocess output goes to `runs/<run-id>/tools/<phase>.<step>.<tool>.log`. Everything is redacted (§10.2).
- **Progress (stderr, TTY):** `[4/9 configure] ansible: TASK k3s : start server (node-01) … 03:12`, plus a heartbeat line every 30 s during long waits stating what is awaited and the remaining timeout.
- **Status:** `./bootstrap status` shows a table of pipeline, phase, status, attempt, last error and `next_action`.
- **Off-cluster to on-cluster bridge:** sc-bootstrap emits no Prometheus metrics. The verify result and the report are uploaded to S3, and plan 44's `dr-tests` job reads the last `state verify` result (plan 06 §21) for alerting.
- **Alerts:** none are raised by sc-bootstrap itself, which is an interactive tool. In-cluster alerts concerning bootstrap-owned off-cluster state are defined by plan 06 §21 (`SCTofuStateRestoreTestFailed`, `SCTofuStateLockStale`; catalogue names, CD-01).

## 22. Testing strategy

All tests are deterministic, need no paid model APIs, and run via Make targets (plan 01 §7.1):

- **Unit** (`make test`): `bootstrap.env` parser and normalizer (golden files: every row of §9.2 including error cases); phase engine with fake phases (done detection, digest change, retry ceiling, exit codes); journal merge rules (higher seq wins, conflict); run-lock semantics against an in-memory S3 fake with and without If-Match support; redactor (property test: no generated secret survives in any output stream); report renderer (golden file); passphrase policy.
- **Contract** (`make contracts`): the config and journal schemas, plus the normalizer output for golden env files validated against `bootstrap-config.schema.yaml`; journals produced by the engine validated against `bootstrap-journal.schema.yaml`.
- **Integration** (`make e2e-bootstrap-fake`): the full install pipeline with provider `fake` (plan 03 §22), the SeaweedFS `s3-emulator` as S3 (ADR-0205, CD-12; conditional writes asserted by plan 14 `make test-s3-emulator`), and stub implementations for configure/seed/forge-init/flux-bootstrap/verify that only record calls. Includes **crash injection**: `SC_TEST_MODE=1 SC_TEST_CRASH_AT=<step>:<before|after>` for every step boundary, followed by a rerun. It asserts the final journal equals a crash-free run's journal modulo timestamps and run IDs, with no duplicate commits, bucket creations or provider creates. A second scenario resumes on a "new machine" (fresh workspace, same S3).
- **Crash semantics (BOOT-5):** `SC_TEST_CRASH_AT=<step>:<before|after>` delivers **SIGKILL** to the engine process at that boundary (no deferred cleanup, lock not released, tmpfs gone). Crash injection also covers every step boundary of seed (U1–U6), forge-init (S00–S19), transfer (T0–T8, including `flux-bootstrap.sso-a0`), sso-init and ci-init, **plus** the point inside T2 between the root's SSA returning and the journal write (`during:T2`, SEEDFLUX-B1), each rollback sub-step (SEEDFLUX-B2), and two new-machine cases: "seed complete, forge-init not started" and "S08 pushed, S19 not recorded" (SEEDFLUX-B3) in `make e2e-handoff` (k3d) and `make e2e-lab`, owned jointly with plans 19, 16, 15 and 20; each rerun must converge with no duplicate tokens or commits and the root `sourceRef` invariant of §17.1 rule 5.
- **Lab E2E** (`make e2e-lab`): the `existing` provider on QEMU VMs (plan 05 §14.6) through `configure` and, at F10+, `seed` with the `lab-small` profile.
- **Fresh-VPS E2E** (`make e2e-fresh-vps`, opt-in, paid, manual or scheduled): P12. Runs from a clean clone on a clean runner with only `bootstrap.env`, `SC_CONFIRM_STORAGE`, `SC_CONFIRM_PLAN`, `SC_CONFIRM_BUDGET` and `SC_RECOVERY_PASSPHRASE_FILE` set, followed by `./bootstrap destroy` (plan 49).

## 23. Acceptance criteria

### GATE-F08

- **50-AC-1** `make test` passes the `src/internal/bootstrap/...` packages, with coverage of every row of §9.2 (golden test `TestEnvNormalization/*` count ≥ number of rows).
- **50-AC-2** `./bootstrap --env-file test/fixtures/bootstrap/invalid-unknown-key.env preflight` exits 2 and prints `PF-02` naming the unknown key and a suggestion.
- **50-AC-3** For every golden env file in `test/fixtures/bootstrap/valid/*.env`, the normalized output validates against `contracts/bootstrap-config.schema.yaml`, and `grep -F` finds none of the fixture secret values in it.
- **50-AC-4** `make e2e-bootstrap-fake` passes. For each of the ≥ 30 injected crash points (SIGKILL semantics, §22), the rerun (after automatic same-host lock takeover) exits 0, `jq` comparison of the final journal (timestamps and run ids stripped) equals the reference, `git log --format=%s` contains exactly one `bootstrap(<cluster>): installation config` commit, and the fake provider's create counter is 1.
- **50-AC-5** Two concurrent `./bootstrap` runs against the same fake install: exactly one exits 6, and the lock object content names the other run's id.
- **50-AC-6** The journal-conflict fixture (equal seq, different content) makes `./bootstrap status` exit 7 and writes both copies under `runs/<id>/journal-conflict/`.
- **50-AC-7** `SC_TEST_CRASH_AT` set together with provider `ovh` exits 2 (`TEST_MODE_REFUSED_PAID`).
- **50-AC-8** The redactor property test: 10 000 random secret strings injected into structured logs, tool logs and the report produce zero occurrences in outputs.
- **50-AC-9** `./bootstrap status --json | python3 -c '<validate against bootstrap-journal.schema.yaml>'` succeeds for every journal produced in 50-AC-4.
- **50-AC-10** The `plan.bundle-confirm` test: a wrong re-entered passphrase three times exits 8 and the journal shows `recovery_bundle.status = uploaded` (not `confirmed`), and `provision` never starts (fake provider create counter 0).
- **50-AC-11** `./bootstrap plan` on a fresh fake install creates no S3 bucket, no object and no Git commit (`s3-emulator` bucket list and `git rev-parse HEAD` unchanged).
- **50-AC-16** Freeze (BOOT-2): fake install with stubbed post-transfer phases where `verify` fails once; the rerun reports seed, forge-init and flux-bootstrap `skipped`, the stub root `sourceRef` stays `GitRepository`, and `./bootstrap --from-phase seed` exits 2 with `PHASE_FROZEN`.
- **50-AC-17** Stale lock (BOOT-5): after SIGKILL during `plan.storage-apply`, a rerun on the same host exits 0 and the journal shows `stale-lock-takeover`; a rerun with a different `host` in the lock object exits 6.
- **50-AC-18** Abandon (BOOT-6): fake install, three wrong passphrase confirmations (exit 8), `./bootstrap abandon-identity`, rerun with a new passphrase ⇒ reaches `planned` with the **same** buckets (`list-buckets` unchanged), a new `install_uid`, and `journal/abandoned-<old-uid>.json` present. After a `paid-footprint` confirmation, `abandon-identity` exits with `ABANDON_REFUSED_COMPUTE_CONFIRMED`.
- **50-AC-19** New machine (BOOT-7): crash after forge-init S16, fresh workspace (clone of GitHub at the install's parent commit), rerun ⇒ local `main` fast-forwarded to `F`, no new forge-init token minted (Forgejo token list unchanged), no divergent commit.
- **50-AC-29** Ownership (BOOT-9): after provision, a rerun creates no new commit and `cluster-vars` `SC_NODE_IP_1` is unchanged.
- **50-AC-30** Storage confirmation (BOOT-12): `--non-interactive` without `--confirm-storage` exits 5 and the emulator shows **no** bucket; with it, buckets are created after a `storage-footprint` confirmation whose timestamp precedes the first `CreateBucket`.
- **50-AC-31** Unattended confirmations (BOOT-1): a fake install with `SC_CONFIRM_STORAGE`, `SC_CONFIRM_PLAN`, `SC_CONFIRM_BUDGET` and `SC_RECOVERY_PASSPHRASE_FILE` and no TTY reaches the end of the stubbed pipeline with exit 0 and zero prompts (the prompt counter in the run log is 0).
- **50-AC-32** State protection (BOOT-3): with `S3_CREDENTIAL_MODE=shared` and no `S3_STATE_*`, `plan.storage-credentials` exits 3 with `OBJSTORE_STATE_UNPROTECTED`; with `S3_STATE_*` the emulator shows B1/B2 in the second account and the in-cluster key gets `AccessDenied`/`NoSuchBucket` on them; with `S3_ALLOW_UNPROTECTED_STATE=true` it proceeds, the journal shows `accept-state-unprotected`, and `verify` reports the warning.
- **50-AC-34** Traefik ClusterIP derivation (CD-37, PF-30): the rendered `cluster-vars` has `SC_TRAEFIK_CLUSTER_IP=10.43.0.20` for the default `SERVICE_CIDR=10.43.0.0/16` and `10.96.0.20` for `SERVICE_CIDR=10.96.0.0/12`; `SERVICE_CIDR=10.43.0.0/27` makes preflight exit 3 with `SERVICE_CIDR_TOO_SMALL`; a fixture whose cluster DNS IP (plan 07 k3s `cluster-dns`) is set to the derived address exits 3 with `SERVICE_CIDR_IP_COLLISION`.
- **50-AC-35** Egress variables (PF-28): with a public `S3_ENDPOINT` on 443, `cluster-vars` contains no `SC_S3_CIDR`/`SC_S3_PORT`; with `S3_ENDPOINT=https://10.20.0.5:9000` and `S3_PRIVATE_CIDR=10.20.0.0/24` it contains `SC_S3_CIDR=10.20.0.0/24` and `SC_S3_PORT=9000`; the same endpoint without `S3_PRIVATE_CIDR` exits 3 with `S3_ENDPOINT_PLACEMENT_UNSUPPORTED`. After provision on a fake node whose discovery reports resolvers `192.0.2.53` and `192.0.2.54`, `cluster-vars` has `SC_DNS_UPSTREAM_1=192.0.2.53`, `SC_DNS_UPSTREAM_2=192.0.2.54`, `SC_DNS_UPSTREAM_3=192.0.2.53`.
- **50-AC-36** Provider spend backstops (PF-29, BUD-B6): with a fixture enabled provider `monthlyUsd=100` and no attestation, preflight exits 5; `backstop <provider> 125` (or `--confirm-backstops <file>`) passes and records `provider-backstops.yaml` with the console-MFA attestation and a `provider-backstop` journal entry; `backstop <provider> 200` is rejected (> 1.25 x `monthlyUsd`); a file lacking the MFA attestation is rejected.
- **50-AC-12** `make config-docs-check` passes, and modifying `bootstrap.env.example` to drop a key makes it fail.
- **50-AC-13** CLI coverage (CD-14): `TestCLIIndexCoverage` extracts every `./bootstrap …` and `sc-bootstrap …` invocation from `plans/*.md` and `procedures/*.md` and parses each with the real command tree (`--help` dry parse); zero unknown commands or flags. Removing a row's command from the parser makes the test fail.
- **50-AC-14** Recovery commit scope: a fixture recovery commit that changes `policy/budgets.yaml` `spec.emergencyStop` to `workers` passes the scope check; the same commit also changing `spec.global.monthlyUsd` fails with `COMMIT_SCOPE_VIOLATION`, and an install (non-recovery) commit touching `policy/budgets.yaml` fails.
- **50-AC-15** With a fixture `ANTHROPIC_API_KEY` carrying a forbidden prefix, preflight exits 3 with `PF-19 MODEL_KEY_FORBIDDEN_PREFIX`; with a valid fixture and `--non-interactive` but no `budget` confirmation, the run exits 5 (PF-26).

### GATE-P12 (co-owned with plan 19; this plan owns the report, verify aggregation, SPEC §130 and the memory budget)

- **50-AC-20** Fresh-VPS E2E: from a clean clone at a release tag on a clean runner, `./bootstrap --profile self-hosting-minimum --non-interactive` with `SC_CONFIRM_STORAGE`, `SC_CONFIRM_PLAN`, `SC_CONFIRM_BUDGET` and `SC_RECOVERY_PASSPHRASE_FILE` exits 0 with zero prompts, including `verify.dr-prime` and every V-check required at P12 (CD-19). Wall time is a **recorded measurement** with a per-phase breakdown in `report.json`; > 180 min is a `warn` (BOOT-15), not a failure.
- **50-AC-21** Line 1 of stdout's final report block is exactly `CONTROL TRANSFER COMPLETE` (`grep -x 'CONTROL TRANSFER COMPLETE'` matches once), and `report.json` contains every field listed in §14.11 (schema-checked by the E2E harness).
- **50-AC-22** `verify.json` shows every required check `pass`: `jq '[.[] | select(.required and .status != "pass")] | length'` = 0, and the IDs V-01..V-08 (SPEC §35) are all present.
- **50-AC-23** V-30 evidence contains numeric `platform_ws`, `alloc`, `worker_max`, `ci_job` and `min_mem_available` for every node, and the recomputed inequality holds. The recorded node shape equals the provider output.
- **50-AC-24** V-31 passes. Each SPEC §130 item listed in §14.8.2 maps to a passing check ID in `verify.json`, and the journal contains only allowlisted confirmation kinds.
- **50-AC-25** Running `./bootstrap` a second time on the transferred install exits 0, performs no commit (`git rev-parse HEAD` unchanged) and no seed or transfer (journal shows those phases `skipped`), and prints the report again.
- **50-AC-26** The E2E log scan finds no value from the bundle (passphrase-decrypted in the harness) in `.bootstrap/runs/**`, in S3 `runs/**`, or in the report.
- **50-AC-27** `./bootstrap status --json | jq -r .installation.state` = `transferred`, and `.recovery_bundle.status` = `confirmed`.
- **50-AC-33** Crash and resume across the platform phases (BOOT-5): in `make e2e-handoff` (k3d) and `make e2e-lab`, `SC_TEST_CRASH_AT` (SIGKILL) is injected at **every** step boundary of seed (U1–U6), forge-init (S00–S19), transfer (T0–T8, including `flux-bootstrap.sso-a0`), sso-init and ci-init. After each crash, a plain `./bootstrap` rerun exits 0 and: no bot, agent or deploy-key token is minted twice (Forgejo/Woodpecker token lists equal a crash-free reference); `git log` shows each `bootstrap(<cluster>): …` commit exactly once; after `flux-bootstrap.switched`, the root `sourceRef` is `GitRepository` in every observation; the final journal equals the reference modulo timestamps and run ids. Owned jointly with plans 19, 16, 15 and 20.
- **50-AC-28** On an 8 GiB node (when tested), V-30 either passes or fails with the ADR-0020 message. It never reports `skip`.

## 24. Explicit non-goals

- Running the orchestrator in-cluster or as a service. sc-bootstrap is an operator CLI.
- A GUI or web installer.
- Managing multiple installations in one run (one `bootstrap.env` = one cluster).
- Fresh installs directly into `small-ha` (install `self-hosting-minimum`, then `expand`; plan 03 §9).
- Automatic remediation of post-transfer drift. Remediation goes through Git and procedures.
- Windows hosts (WSL2 with Docker is best effort, not tested).

## 25. Open questions

| # | Question | Blocking | Proposed resolution | Owner |
|---|---|---|---|---|
| OQ-50-1 | Is prompting for the recovery passphrase on every mutating run acceptable operator friction? | no | Yes (ADR-0101): it doubles as a periodic passphrase test (SPEC §22 "periodically tested"). Revisit if operators complain. | 50 |
| OQ-50-2 | (Resolved, CD-19) Internal images at P12 come from the upstream release on GHCR, digests in `versions/internal-images.lock.yaml`, verified against the published release signatures (V-26). The installation's own release chain is post-transfer (P11 → C01). | no | — | 23 |
| OQ-50-3 | (Resolved at consolidation) V-08 uses plan 17's `mirror verify` probe **tag**, which is deleted on both sides by the check itself. | no | — | 17 |
| OQ-50-4 | `infra/bootstrap/settings.yaml` lacks its own contract file. | no | Add a `BootstrapSettings` `$defs` entry to `bootstrap-config.schema.yaml` at implementation (F08), or a separate contract if the consolidation pass prefers. | 50 |
| OQ-50-5 | DAG edges required by this plan's fix round (BOOT-11, GC-01): GATE-F08 now includes plan 03's `fake` provider and plan 06's emulator-level state library, so `dag.yaml` F08 must list plans 03 and 06 (no new edge); F10's lab ACs (plans 07/08) need F08 and F09 (`F10 → F08, F09`). | no | The lead updates `dag.yaml` and master plan §13/§14. | lead |
