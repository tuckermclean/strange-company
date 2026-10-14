# 02 — Bootstrap toolbox

**Status:** Stage-0 plan (draft)
**DAG nodes:** F03 (owner)
**Related plans:** [01](01-repository.md), [03](03-provider-interface.md), [05](05-existing-nodes.md), [06](06-opentofu-state.md), [07](07-k3s.md), [12](12-cnpg.md), [15](15-authentik.md), [18](18-flux.md), [23](23-supply-chain.md), [35](35-secrets.md), [50](50-bootstrap-orchestrator.md), [52](52-ci-strategy.md)
**Contracts:** consumes `bootstrap-config.schema.yaml` (SSH and env-file fields); defines the lock kinds `ToolboxLock` and `ToolboxImageLock` (schemas in `contracts/version-locks.schema.yaml`, plan 01 §8.2)
**ADRs:** relies on ADR-0002, ADR-0013, ADR-0018, ADR-0101; introduces ADR-0103 (toolbox runtime contract)

## 1. Purpose

Every bootstrap and operator tool runs from one version-pinned container image, so an operator needs only Git, Docker or Podman, SSH key material and credentials (SPEC §7, §9). This plan defines:

- the **toolbox** image (operator runtime) and the **ci** image (toolbox plus build/test tools) built from one `toolbox/Containerfile`;
- how every tool is pinned by version and sha256 (`toolbox/tools.lock.yaml`);
- the `./bootstrap` POSIX `sh` wrapper: engine selection, pull-by-digest-or-build, and how it runs the container (Docker rootful/rootless, rootless Podman, SELinux, uid mapping);
- how the repository, the black-start directory, `bootstrap.env` and SSH material enter the container;
- `./bootstrap shell` and `./bootstrap exec -- <cmd>` (the forms every procedure uses);
- the toolbox self-check (preflight PF-01) and the image record used in-cluster (plan 15 platform probes).

## 2. Requirements derived from SPEC.md

1. Bootstrap tooling executes from a version-pinned toolbox image [SPEC §9].
2. Tools include OpenTofu, Ansible, kubectl, Helm, Flux CLI, SOPS, age, SSH, curl, jq, yq [SPEC §9]; plus `age-keygen`, `ssh-keygen`, `git`, `sc-bootstrap` and an S3 CLI (assignment; plans 06, 16, 49 use `aws s3api`), and `kubectl cnpg` (plan 12).
3. Tool versions are version-controlled [SPEC §9].
4. Local prerequisites are limited to Git, Docker or Podman, SSH keys and credentials; the operator never installs OpenTofu, Ansible, kubectl, Helm, Flux, SOPS, age, jq or yq [SPEC §7, §130].
5. Black start must not depend on any registry that Strange Company itself runs [SPEC §111, §112; ADR-0013].
6. Pinned dependencies and immutable references [SPEC §37].
7. No secret in logs or process arguments [SPEC §94, §98].

Derived requirements:
- **02-R-1** Every binary in either image is pinned by version **and** sha256 per architecture, or comes from a Debian snapshot pinned by timestamp (apt verifies signed Release files), or from pip in hash-checking mode. A build with any unpinned download fails.
- **02-R-2** The pull path and the build path yield the same tool versions (identical `selfcheck --json` tool rows) and byte-identical Strange Company binaries (reproducible Go builds, plan 01 §6.2). Only the image digest may differ (ADR-0013).
- **02-R-3** Credentials enter the container only as **read-only file mounts** (`bootstrap.env`, SSH key) or via the ssh-agent socket. They never appear in `docker run` arguments, container environment at start, image layers, or `docker inspect` output.
- **02-R-4** The wrapper never relabels, chowns or chmods host files, and never writes outside the repository and the black-start directory.
- **02-R-5** Files written by the container in the repository and the black-start directory are owned by the invoking host user (on all supported engine modes).
- **02-R-6** The wrapper parses `bootstrap.env` without shell evaluation (plan 50 §9.1).

## 3. Inputs

| Input | Source |
|---|---|
| Tool pins | `toolbox/tools.lock.yaml` |
| Python pins | `toolbox/requirements.txt` (hash-locked) |
| Published image digests | `toolbox/image.lock` |
| Image definition | `toolbox/Containerfile`, `toolbox/bin/*`, `toolbox/shellrc`, root `.dockerignore` |
| Strange Company binaries | `src/` (built inside the image build) |
| Operator files | `bootstrap.env` (or `--env-file`), SSH key paths named in it, `SSH_AUTH_SOCK` |
| Engine | `docker` or `podman` on `PATH` |
| Wrapper settings (env) | `SC_CONTAINER_ENGINE`, `SC_TOOLBOX_BUILD`, `SC_TOOLBOX_REQUIRE_PULL`, `SC_BLACKSTART_DIR`, `SC_TOOLBOX_NETWORK`, `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` |

## 4. Outputs

- Images `ghcr.io/<owner>/strange-company-toolbox` and `ghcr.io/<owner>/strange-company-ci` (MVP: `linux/amd64` only; `linux/arm64` is a post-MVP addition, §19), published by plan 23's release pipeline, digests recorded in `toolbox/image.lock`.
- Locally built images `localhost/strange-company-toolbox:in-<inputs12>` / `localhost/strange-company-ci:in-<inputs12>` when the pull path is not used.
- Optional OCI archive `.bootstrap/toolbox/<inputs-id>.oci.tar` for import into node containerd (§14.8).
- `selfcheck` JSON (`.bootstrap/runs/<run-id>/tools/selfcheck.json`) consumed by PF-01 and the report.

## 5. Dependencies

- **Hard (DAG):** F03 depends on F01 (layout, Makefile, `contracts/version-locks.schema.yaml`).
- **Consumers:** F08 (sc-bootstrap runs in it), F10 (Ansible), every procedure, every Make target (ci image, plan 01 §8.1), plan 15 platform probes (toolbox image in-cluster).
- **Upstream availability at build time:** Debian snapshot archive, GitHub releases and vendor download sites of each tool, Go module proxy, PyPI. Pull time: GHCR. A full upstream outage prevents only *local builds*; the published image remains pullable (and vice versa) — the two paths are independent fallbacks.

## 6. Owned state

| State | Location | Writers |
|---|---|---|
| Tool pins | `toolbox/tools.lock.yaml`, `toolbox/requirements.txt` | Renovate / human PR (protected path, plan 01 §13.2) |
| Published digests | `toolbox/image.lock` | release pipeline PR (plan 23) |
| Local image cache | the engine's image store | wrapper |
| OCI archive for node import | `.bootstrap/toolbox/` (newest inputs id only) | wrapper |

## 7. External interfaces

### 7.1 Wrapper CLI (the subset this plan defines; plan 50 §7.1 owns the full command list)

```text
./bootstrap [--engine docker|podman] [--toolbox-build] [--env-file PATH] <command> [args...]
./bootstrap shell [--image toolbox|ci] [--network bridge|host|none] [--unlock]
./bootstrap exec  [--image toolbox|ci] [--network bridge|host|none] [--unlock] [--no-kube] [--kvm] -- <cmd> [args...]
```

- Wrapper flags are consumed by the wrapper; everything else is passed to `sc-bootstrap` unchanged.
- `exec` passes stdin through and allocates a TTY only when both stdin and stdout are TTYs, so pipes work: `./bootstrap exec -- kubectl get cm x -o yaml | ./bootstrap exec -- kubectl apply -f -`.
- `exec` returns the exit code of `<cmd>`. Wrapper-level failures use plan 50's codes: `2` usage / not a repository, `3` engine missing, image pull+build failed, or toolbox stale.
- `--image ci` selects the ci image (Make targets, lab). `--kvm` (ci image only) adds `--device /dev/kvm` and the device's group (plan 05 lab).
- `--network` defaults to `SC_TOOLBOX_NETWORK` or `bridge`. `host` is required for the lab (plan 05) and is Linux-only.
- `--unlock` prompts once for the recovery passphrase and exposes bundle-derived material (§14.6). Without it no bundle-derived secret is available.

### 7.2 In-container environment contract

| Variable / path | Meaning |
|---|---|
| `SC_IN_CONTAINER=1` | set in both images (Makefile re-exec guard, plan 01 §8.1) |
| `SC_REPO_ROOT` | repository path; identical to the host path (§14.2) |
| `SC_TOOLBOX_SOURCE` | `pulled` \| `built` \| `built-fallback` (pull failed) |
| `SC_TOOLBOX_IMAGE_REF` | `<repo>@sha256:<digest>` (pulled) or `localhost/…:in-<inputs12>@<image-id>` (built) |
| `SC_ENGINE` | `docker`, `docker-rootless` or `podman-rootless` / `podman-rootful` |
| `/run/sc/` | tmpfs, mode 0700, owned by the container user; holds derived secrets (plan 50 §10.2), `ssh/id_key` (copied, 0600), `aws/`, `kubeconfig`, `home/` |
| `/run/sc-input/` | read-only mounts: `bootstrap.env`, `ssh/id_key`, `ssh/id_key.pub`, `ssh-agent.sock` |
| `/blackstart` | host `${SC_BLACKSTART_DIR:-$HOME/.strange-company}` (rw) |
| `HOME=/run/sc/home` | writable home on tmpfs (ssh, helm, kube caches are ephemeral; plugin and chart caches use `$SC_REPO_ROOT/.cache/`) |
| `/usr/share/strange-company/tools.lock.yaml` | the lock the image was built from |
| `/usr/share/strange-company/image-info.json` | `{inputs_id, built_at, source_commit, target: toolbox|ci, arch}` |

## 8. Internal interfaces

- `sc-bootstrap toolbox fetch --lock <file> --arch <amd64|arm64> --dest <dir> [--set runtime|ci]`: downloads each tool, verifies sha256 before extracting, installs it. Used only inside the image build (§14.1).
- `sc-bootstrap toolbox selfcheck [--json]`: runs every tool's `versionCommand`, matches `versionRegex`, compares the embedded lock with `$SC_REPO_ROOT/toolbox/tools.lock.yaml` (sha256 equality), reports `{tool, expected, actual, ok}`. PF-01 calls it.
- `sc-bootstrap toolbox init` (container entrypoint helper; §14.5).
- `sc-bootstrap env <kube|s3|unlock|tofu <module>>` (aliased `sc-env` in the shell): prints `export` lines for the requested scope (§14.6). `tofu <module>` requires a prior `unlock`; it prepares `.bootstrap/work/<module>/` exactly as plan 06 `PrepareWorkdir` + `InitRemote` do and exports `TF_ENCRYPTION` and the backend credentials for read-only inspection by an operator.

## 9. Configuration

### 9.1 Wrapper settings

| Setting | Default | Notes |
|---|---|---|
| `--engine` / `SC_CONTAINER_ENGINE` | auto: `docker` if `docker info` succeeds within 10 s, else `podman` if `podman info` succeeds, else exit 3 `WRAPPER_ENGINE_MISSING` | explicit value wins |
| `--toolbox-build` / `SC_TOOLBOX_BUILD=1` | off | force local build |
| `SC_TOOLBOX_REQUIRE_PULL=1` | off | forbid build fallback (release verification, fresh-VPS E2E) |
| `SC_BLACKSTART_DIR` | `$HOME/.strange-company` | created with mode 0700 if absent; refused if group/world-writable |
| `SC_TOOLBOX_NETWORK` | `bridge` | `host` for lab |

### 9.2 `toolbox/tools.lock.yaml` (`kind: ToolboxLock`)

```yaml
apiVersion: strange.company/v1alpha1
kind: ToolboxLock
spec:
  baseImages:
    debian: {ref: docker.io/library/debian:trixie-slim, digest: sha256:…}
    golang: {ref: docker.io/library/golang:<go-minor>-trixie, digest: sha256:…}   # build stage only
  debianSnapshot: 20261001T000000Z          # snapshot.debian.org timestamp for every apt install
  aptPackages:
    runtime: [bash, ca-certificates, curl, git, openssh-client, python3, python3-venv, tini, libnss-wrapper,
              less, vim-tiny, procps, iproute2, dnsutils, netcat-openbsd, openssl, unzip, xz-utils]
    ci: [make, shellcheck, qemu-system-x86, qemu-system-arm, qemu-utils, cloud-image-utils, genisoimage, docker-cli]
  tools:
    - name: opentofu
      set: runtime
      version: "1.11.x"                     # illustrative
      selectedBy: "tofu:latest-stable>=1.11"
      url: https://github.com/opentofu/opentofu/releases/download/v{version}/tofu_{version}_linux_{arch}.tar.gz
      sha256: {amd64: "…", arm64: "…"}
      install: {extract: tofu, to: /usr/local/bin/tofu}
      versionCommand: [tofu, version, -json]
      versionRegex: '"terraform_version":\s*"{version}"'
      verifiedAt: 2026-10-01
    # … one entry per tool in §9.4
  pythonRequirements: toolbox/requirements.txt
  ansibleCollections:
    - {name: ansible.posix, version: "x.y.z", url: …, sha256: …}
    - {name: community.general, version: "x.y.z", url: …, sha256: …}
```

### 9.3 `toolbox/image.lock` (`kind: ToolboxImageLock`)

Deliberately a **flat** YAML subset (one `key: value` per line, no nesting) so the POSIX wrapper can read it with `sed` alone:

```yaml
apiVersion: strange.company/v1alpha1
kind: ToolboxImageLock
inputsId: 4b825dc642cb6eb9a060e54bf8d69288fbee4904
toolboxImage: ghcr.io/example-org/strange-company-toolbox
toolboxDigest: sha256:…
ciImage: ghcr.io/example-org/strange-company-ci
ciDigest: sha256:…
sourceCommit: 0123456789abcdef0123456789abcdef01234567
publishedAt: 2026-10-13T12:00:00Z
```

An empty `toolboxDigest` (before the first release) means "always build".

### 9.4 Tool inventory and selection rules

Versions are selected at implementation time by the stated rule and re-verified by 02-AC-* and the consumer plans' tests. "(verify)" marks behavior that must be re-checked when the pin changes.

| Tool | Set | Source | Selection rule | Consumer |
|---|---|---|---|---|
| OpenTofu `tofu` | runtime | GitHub release tarball + sha256 | latest stable ≥ 1.11 (S3 `use_lockfile`, state encryption with `fallback`, ephemeral variables; plans 03, 06) | 03, 06 |
| `ansible-core` + `ansible.posix`, `community.general` | runtime | pip (hash-locked) in `/opt/sc/ansible-venv`; collections by tarball + sha256 | latest ansible-core whose controller supports the image's Python and whose target support includes Python 3.12 (Ubuntu 24.04) and 3.13 (Debian 13) (verify) | 07, 08, 09, 05 |
| `kubectl` | runtime | `dl.k8s.io` binary + sha256 | same minor as `versions/k3s.lock.yaml`, latest patch (skew 0, plan 01 §6.3) | all |
| `helm` | runtime | `get.helm.sh` tarball + sha256 | latest stable of the Helm major used by the pinned Flux helm-controller (verify at pin time) | 54, debugging |
| `flux` | runtime | GitHub release + sha256 | exactly `versions/platform.lock.yaml` `flux.version` | 18, 19 |
| `sops` | runtime | GitHub release binary + sha256 | latest 3.x | 35, 50 |
| `age`, `age-keygen` | runtime | GitHub release tarball + sha256 | latest 1.x | 35, 50 |
| `ssh`, `ssh-keygen`, `ssh-keyscan` | runtime | Debian `openssh-client` (snapshot) | Debian 13 version | 50, 05, 07 |
| `curl`, `git`, `bash`, `python3` | runtime | Debian (snapshot) | Debian 13 version | all |
| `jq` | runtime | GitHub release static binary + sha256 | latest 1.x | procedures |
| `yq` (mikefarah) | runtime | GitHub release + sha256 | latest v4 | procedures |
| `aws` (AWS CLI v2) — the S3 CLI | runtime | `awscli.amazonaws.com` zip + sha256 | latest 2.x (verify: `endpoint_url` in profiles; checksum env vars below) | 06, 14, 49, procedures |
| `kubectl-cnpg` | runtime | GitHub release + sha256 | equals the CNPG operator version in `platform.lock.yaml` (plan 12) | 12, procedures |
| `cosign` | runtime | GitHub release + sha256 | latest stable | 23 (verify signatures from operator side, signing-key rotation) |
| `syft`, `trivy` | runtime | GitHub releases + sha256 | latest stable each (trivy vulnerability DB is fetched at run time, not baked in) | 23 (SBOM and scan from the operator side, `supply-chain init`) |
| `gitleaks` | runtime | GitHub release + sha256 | latest stable | 35 (`sc-bootstrap secrets scan`), 01 `secrets-scan` |
| `etcdutl` | runtime | etcd release tarball + sha256 | equals the etcd minor embedded in the pinned k3s (`versions/k3s.lock.yaml` `etcdctl.version`) | 07 (`restore-etcd.md` snapshot inspection), 45 |
| `cmctl` | runtime | GitHub release + sha256 | equals the cert-manager version in `versions/platform.lock.yaml` | 10 (`certificate-failure.md`) |
| `sc-bootstrap`, `sc-gitops`, `sc-lint`, `sc-contracts` | runtime | built from `src/` in the `gobuild` stage | repository HEAD | 50, 18, 01 |
| `tini`, `libnss-wrapper` | runtime | Debian (snapshot) | — | §14.5 |
| Go toolchain | ci | golang base image by digest | `src/go.mod` `toolchain` directive | `make test/build` |
| `golangci-lint`, `shfmt`, `kubeconform`, `k3d`, `promtool`, `helm-unittest` (Helm plugin), `goimports`, `actionlint`, `woodpecker-cli` (version = pinned Woodpecker server), envtest assets (`kube-apiserver`, `etcd` binaries for the pinned Kubernetes minor via `setup-envtest`) | ci | GitHub releases + sha256 (Go tools may be `go install` at a pinned version with `GOFLAGS=-mod=mod` and sum-DB verification) | latest stable each | plan 01 §7.1 targets |
| `yamllint`, `ansible-lint`, `jsonschema` (crosscheck) | ci | pip hash-locked | latest | 01, 07 |
| QEMU, `cloud-localds`, `genisoimage` | ci | Debian (snapshot) | Debian 13 version | 05 lab |
| SeaweedFS `weed` (the `s3-emulator`, ADR-0205/CD-12) | ci | upstream release binary + sha256 | the version that passes plan 14's `make test-s3-emulator` | 05 lab, 06, 14, 49, 50 |
| `sc-lab` | ci | built from `src/` | repository HEAD | 05 lab |
| `docker` CLI | ci | Debian `docker-cli` (snapshot) | — | k3d targets (host socket mounted by plan 52) |

Defaults baked into the image environment: `AWS_REQUEST_CHECKSUM_CALCULATION=when_required` and `AWS_RESPONSE_CHECKSUM_VALIDATION=when_required` (AWS CLI ≥ 2.23 sends new default checksums that several S3-compatible providers reject (verify)); `ANSIBLE_CONFIG=$SC_REPO_ROOT/infra/ansible/ansible.cfg`; `TF_PLUGIN_CACHE_DIR=$SC_REPO_ROOT/.cache/tofu-plugins`; `HELM_CACHE_HOME=$SC_REPO_ROOT/.cache/helm`; `KUBECTL_EXTERNAL_DIFF` unset; `PATH=/opt/sc/bin:/opt/sc/ansible-venv/bin:/usr/local/bin:/usr/bin:/bin`.

Image size budgets (uncompressed, amd64): toolbox ≤ 1.6 GiB, ci ≤ 3.5 GiB. 02-AC-8 measures.

## 10. Secrets

Consumed (never stored by the toolbox itself): `SC-SSH-OPERATOR` (read-only mount, copied into tmpfs), everything in `bootstrap.env` (read-only mount; parsed by sc-bootstrap), `SC-RECOVERY-PASSPHRASE` (TTY prompt, `--unlock` / mutating commands), and bundle-derived material placed in `/run/sc` (plan 50 §10.2, ADR-0101). `SC-LAB-SSH` (plan 05) is mounted the same way as the operator key. No new secret class. The images contain no secret; `make toolbox-check` scans the built image filesystem with `gitleaks` (02-AC-9).

## 11. Storage

- Engine image store: ≈ 1.6 GiB (toolbox) + ≈ 3.5 GiB (ci, developers and CI only).
- `.bootstrap/toolbox/<inputs-id>.oci.tar`: ≈ 0.5 GiB compressed, only when a built toolbox must be imported into nodes (§14.8). Retention: only the current inputs id is kept; older archives are deleted by the wrapper at start.
- `.cache/` (repository, gitignored): plugin, chart, Go caches shared between runs.
- Nothing persists inside the container; `/run/sc` and `/tmp` are tmpfs.

## 12. Resource requirements

Operator host: 2 CPU, 4 GiB RAM free, 5 GiB disk for the toolbox path (plan 50 §12); a local build needs ≈ 10 GiB disk peak and ≈ 10 min on 4 CPUs with a warm network. Containers run with no CPU/memory limit flags (operator machine).

## 13. Security boundary

- The toolbox holds the operator's full authority (provider, DNS, S3 admin, cluster-admin) while a command runs; it is trusted code from the checked-out commit (plan 50 §13).
- **Container hardening:** `--cap-drop ALL`, `--security-opt no-new-privileges`, non-root user (§14.4), read-write only on the repository, `/blackstart`, and tmpfs mounts. No engine socket is ever mounted into the toolbox image. The ci image gets the host engine socket only in plan 52's `e2e-*` CI jobs (k3d), never on operator machines via `./bootstrap`.
- **SELinux (ADR-0103):** on hosts with SELinux enforcing (`/sys/fs/selinux/enforce` = `1`) and always with Podman, the wrapper passes `--security-opt label=disable` for the toolbox container and never uses `:z`/`:Z` volume options. Relabeling the repository, `$HOME/.ssh` (which sshd requires to keep `ssh_home_t`) or the black-start directory would break host tools or log-ins (02-R-4). The toolbox is trusted operator code, so SELinux container separation adds no protection that justifies relabeling.
- **Credential exposure:** secrets never enter `run` arguments or the initial environment (02-R-3); `sc-bootstrap` registers every secret value with its redactor (plan 50 §10.2). `ps` inside the container shows no secret arguments (tools receive secrets via env of their own subprocess or files in `/run/sc`).
- **Supply chain:** pull path = digest pin in Git (GHCR cannot substitute content). Build path = digest-pinned base images, snapshot-pinned apt, sha256-pinned downloads, hash-pinned pip, sum-DB-verified Go modules. Cosign signature verification of the published toolbox is not performed by the wrapper (no host cosign; the digest pin is the integrity anchor); plan 23 signs releases for third-party verification.

## 14. Happy path

### 14.1 Image build (`toolbox/Containerfile`, stages)

1. `gobuild` — `FROM golang@<digest>`; `go build` (plan 01 §6.2 flags) of `sc-bootstrap`, `sc-gitops`, `sc-lint`, `sc-contracts` for `$TARGETARCH`.
2. `base` — `FROM debian@<digest>`; apt sources replaced by `deb [check-valid-until=no] http://snapshot.debian.org/archive/debian/<debianSnapshot>/ trixie main` (+ `trixie-security` snapshot); install `aptPackages.runtime`; create user `sc` uid/gid `10001`.
3. `fetch` — copy `sc-bootstrap` from `gobuild`; `sc-bootstrap toolbox fetch --set runtime` into `/opt/sc/bin`; create the Ansible venv and `pip install --require-hashes --no-deps -r requirements.txt`; install collections from verified tarballs (`ansible-galaxy collection install <tarball> -p /opt/sc/ansible-collections`).
4. `toolbox` (final) — `FROM base`; copy `/opt/sc` and the Go binaries; copy `tools.lock.yaml` and write `image-info.json`; `ENV SC_IN_CONTAINER=1 …`; `USER 10001:10001`; `ENTRYPOINT ["/usr/bin/tini","--","/opt/sc/bin/sc-entrypoint"]`; label `strange.company/toolbox-inputs-id=<id>` (build-arg).
5. `ci` — `FROM toolbox`; `USER 0` for apt (`aptPackages.ci`) and `sc-bootstrap toolbox fetch --set ci`; Go toolchain copied from `gobuild`; back to `USER 10001`.
6. Final assertion step in both targets: `sc-bootstrap toolbox selfcheck --embedded-only` must pass, or the build fails.

The build context is the repository root, restricted by the root `.dockerignore` to `toolbox/**` and `src/**` minus `**/testdata/**` and `**/*_test.go`.

### 14.2 Wrapper algorithm (`./bootstrap`, POSIX sh)

1. `set -eu`. Resolve `REPO=$(git -C "$(dirname "$0")" rev-parse --show-toplevel)`; not a work tree → exit 2 `WRAPPER_NOT_A_REPO`. Refuse to run as host root unless `SC_ALLOW_ROOT=1` (files would become root-owned).
2. Parse wrapper flags; determine the env file (default `$REPO/bootstrap.env`; may be absent for `exec`, `shell`, `version`).
3. Read from the env file, **without evaluating it** (02-R-6): `SSH_PRIVATE_KEY_PATH`, `SSH_PUBLIC_KEY_PATH`, `SSH_USE_AGENT`. Line grammar: `^[A-Z][A-Z0-9_]*=` then the value with one level of matching single or double quotes removed; leading `~/` → `$HOME/`. Nothing else is interpreted.
4. Select the engine (§9.1) and its mode: `docker` (rootful), `docker-rootless` (`docker info --format '{{.SecurityOptions}}'` contains `rootless`), `podman-rootless` (`podman info --format '{{.Host.Security.Rootless}}'` = `true`), or `podman-rootful`.
5. Compute the **inputs id**: for every file in `git ls-files -co --exclude-standard -- toolbox src .dockerignore` excluding `toolbox/image.lock`, `**/testdata/**` and `**/*_test.go`, sorted, emit `<git hash-object FILE> <path>`; the inputs id is `git hash-object --stdin` of that listing. Only Git is needed, and uncommitted edits are included.
6. Resolve the image for the requested target (`toolbox` or `ci`):
   - (a) `--toolbox-build` → build (§14.1), tag `localhost/strange-company-<target>:in-<inputs12>`, label with the full inputs id. Source `built`.
   - (b) Else if `image.lock` `inputsId` equals the computed id and the digest is non-empty → use `<image>@<digest>`: if absent locally, `<engine> pull <image>@<digest>`. Source `pulled`. If the pull fails and `SC_TOOLBOX_REQUIRE_PULL` is unset → build, source `built-fallback`, with a stderr notice; otherwise exit 3 `WRAPPER_TOOLBOX_PULL_FAILED`.
   - (c) Else (inputs differ, e.g. a development checkout) → reuse `localhost/…:in-<inputs12>` if it exists with a matching label, otherwise build. Source `built`.
   A build failure exits 3 `WRAPPER_TOOLBOX_BUILD_FAILED` with the engine's log path.
7. If the source is `built*`, the target is `toolbox`, and the command is `run`, `expand` or `recover`, ensure `.bootstrap/toolbox/<inputs-id>.oci.tar` exists (`<engine> save --format oci-archive` for Podman, `docker save` for Docker) (§14.8).
8. Prepare mounts (§14.3, §14.4) and run the container (§14.5).

### 14.3 Mounts and the black-start directory

| Host | Container | Mode |
|---|---|---|
| `$REPO` | `$REPO` (same absolute path; `-w` = host cwd if inside `$REPO`, else `$REPO`) | rw |
| `git rev-parse --git-common-dir` when outside `$REPO` (worktrees) | same absolute path | rw |
| `${SC_BLACKSTART_DIR:-$HOME/.strange-company}` (created 0700) | `/blackstart` | rw |
| env file | `/run/sc-input/bootstrap.env` | ro |
| `SSH_PRIVATE_KEY_PATH`, `SSH_PUBLIC_KEY_PATH` | `/run/sc-input/ssh/id_key`, `/run/sc-input/ssh/id_key.pub` | ro |
| `$SSH_AUTH_SOCK` (if `SSH_USE_AGENT=true`) | `/run/sc-input/ssh-agent.sock` | rw (socket); macOS Docker Desktop: `/run/host-services/ssh-auth.sock` (verify) |
| tmpfs | `/run/sc` (`size=64m,mode=0700`, owner = container user; §14.4) and `/tmp` (`size=1g`) | rw |

Mounting the repository at its host path keeps Git worktrees, absolute paths in error messages and host-path references in logs consistent. The black-start directory lives outside the repository so nothing in it can be committed (plan 50 §4).

### 14.4 User and uid mapping

| Engine mode | Run flags | Container uid | Host owner of written files |
|---|---|---|---|
| `docker` (rootful, Linux) | `--user $(id -u):$(id -g)` | host uid | host user |
| `docker` (Docker Desktop macOS/Windows-WSL2) | `--user $(id -u):$(id -g)` | host uid | host user (file sharing maps ownership) |
| `docker-rootless` | `--user 0:0` | 0 in the user namespace | host user (userns root = host user) |
| `podman-rootless` | `--userns=keep-id` | host uid | host user |
| `podman-rootful` | `--user $(id -u):$(id -g)` | host uid | host user |

- Arbitrary uids have no `/etc/passwd` entry, and OpenSSH refuses to run without one ("No user exists for uid"). The entrypoint therefore writes `/run/sc/passwd` and `/run/sc/group` (user `sc-operator`, home `/run/sc/home`) and exports `LD_PRELOAD=libnss_wrapper.so NSS_WRAPPER_PASSWD=/run/sc/passwd NSS_WRAPPER_GROUP=/run/sc/group` for itself and all children. Podman `keep-id` already adds an entry; the wrapper output is harmless there.
- `/run/sc` must be owned by the container uid: Docker `--tmpfs /run/sc:rw,nosuid,nodev,size=64m,mode=0700,uid=<uid>,gid=<gid>` (verify that the engine passes `uid=`/`gid=` to the tmpfs mount); Podman adds the `U` option. If the engine rejects them, the wrapper falls back to `mode=1777` and the entrypoint creates `/run/sc/u<uid>` (0700) and points every `/run/sc/…` path there through `SC_RUN_DIR`. 02-AC-5 asserts the resulting mode and owner.
- `GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=safe.directory GIT_CONFIG_VALUE_0=$REPO` is set so Git accepts the repository under any mapping.
- The SSH private key is **copied** by the entrypoint from `/run/sc-input/ssh/id_key` to `/run/sc/ssh/id_key` (mode 0600, owned by the container user), which is the path `bootstrap-config.yaml` references (`file: /run/sc/ssh/id_key`). This removes host permission and ownership differences. PF-06 checks the host-side mode via the wrapper: a private key readable by group or others makes the wrapper warn, and PF-06 fails.

### 14.5 Container start and entrypoint

Run flags common to all modes: `--rm -i [-t] --cap-drop ALL --security-opt no-new-privileges [--security-opt label=disable] --network <mode> --hostname sc-toolbox --name sc-toolbox-<pid>`, environment `SC_REPO_ROOT`, `SC_TOOLBOX_SOURCE`, `SC_TOOLBOX_IMAGE_REF`, `SC_ENGINE`, `SC_HOST_OS`, `TERM`, `LANG=C.UTF-8`, `TZ=UTC`, and pass-through of `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY` if set. No other host environment is forwarded (no `SC_RECOVERY_PASSPHRASE_FILE` unless set, in which case the *file* is mounted read-only at `/run/sc-input/passphrase` and the variable rewritten).

`sc-entrypoint` (shell script in `toolbox/bin/`) performs `sc-bootstrap toolbox init`:
1. create `/run/sc/{home,ssh,aws,tmp}`, nss_wrapper files, copy the SSH key;
2. if `/run/sc-input/ssh-agent.sock` exists: `SSH_AUTH_SOCK=/run/sc-input/ssh-agent.sock`;
3. dispatch: `run|plan|preflight|status|verify|expand|destroy|recover|report|rotate|state|unlock|version …` → `exec sc-bootstrap --env-file /run/sc-input/bootstrap.env "$@"`; `shell` → §14.6; `exec` → §14.6.

When the image runs without the wrapper (in-cluster, plan 15), `SC_REPO_ROOT` is unset and the entrypoint simply `exec`s its arguments (no operator initialization).

### 14.6 `./bootstrap shell` and `./bootstrap exec -- <cmd>`

Both build the same environment:

- **kube (automatic unless `--no-kube`):** if `/blackstart/<cluster>/kubeconfig` and `gitops/clusters/<cluster>/inventory.yaml` exist (`<cluster>` = `CLUSTER_NAME` from the env file), start `ssh -f -N -o ExitOnForwardFailure=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=4 -o StrictHostKeyChecking=yes -o UserKnownHostsFile=$REPO/.bootstrap/known_hosts -L 127.0.0.1:<port>:<internal_ip>:6443 <login>@<host> -p <port>` against the first server node that answers (inventory order; `--api-node` pins one), where `<login>` is the node's `ssh.admin_user` from `inventory.yaml` if set, else `sc-admin` is tried first and then `ssh.user` (BOOT-8), with `<port>` the first free port in 16443–16499. Write `/run/sc/kubeconfig` (copy of the black-start kubeconfig with `server: https://127.0.0.1:<port>`, `tls-server-name: kubernetes`) and export `KUBECONFIG`. If no node answers within 15 s, print `kube: tunnel unavailable (<reason>)` on stderr and continue without `KUBECONFIG`.
- **s3 (automatic if the env file exists):** write `/run/sc/aws/config` and `/run/sc/aws/credentials` with profile `sc-admin` (`S3_ENDPOINT` as `endpoint_url`, `S3_REGION`, path-style per `S3_FORCE_PATH_STYLE`, keys from `S3_ACCESS_KEY_ID`/`S3_SECRET_ACCESS_KEY`). Export `AWS_CONFIG_FILE` and `AWS_SHARED_CREDENTIALS_FILE` but **no default profile**: every command must say `--profile sc-admin` (or a bundle profile), so a stray `aws s3 rb` cannot run with admin rights by accident.
- **unlock (`--unlock`, or `eval "$(sc-env unlock)"` in a shell):** prompts for the passphrase, opens the bundle in memory (plan 50 §10.2), writes the cluster age identity to `/run/sc/age/cluster.agekey`, exports `SOPS_AGE_KEY_FILE`, adds AWS profiles `sc-<instance>` for each `SC-OBJECT-STORAGE` instance in the bundle (plan 14 names), and makes `sc-bootstrap state …`/`sc-env tofu <module>` able to build `TF_ENCRYPTION`. Nothing is written outside `/run/sc`.

`./bootstrap shell` then runs `bash --rcfile /usr/share/strange-company/shellrc -i`: prompt `[sc <cluster>|<context>] \w $`, aliases `k=kubectl`, `sc-env`, and a banner listing the active scopes, the toolbox source and digest, and the reminder "secrets in this shell vanish when it exits".

`./bootstrap exec … -- <cmd>` runs `<cmd>` with that environment via `exec`, so its exit code and signals propagate. The SSH tunnel is torn down when the container exits.

### 14.7 PF-01 toolbox self-check

At the start of every mutating command (plan 50 §14.2), `sc-bootstrap toolbox selfcheck --json`:
- every tool row `ok` (actual version matches `versionRegex`);
- sha256 of the embedded lock equals sha256 of `$REPO/toolbox/tools.lock.yaml`, else `TOOLBOX_STALE` (fail): the running image was not built from this checkout's lock (e.g. a manual image override);
- records `SC_TOOLBOX_IMAGE_REF` and `SC_TOOLBOX_SOURCE` in the journal run entry and the report (plan 50 §14.11);
- PF-25 (warn) when `SC_TOOLBOX_SOURCE` is `built` or `built-fallback` although `image.lock` names a published digest for the same inputs id.

### 14.8 Toolbox image inside the cluster (plan 15 interface)

The toolbox image also runs `sc-bootstrap probes serve` in `monitoring/platform-probes` (plan 15 §21.2). Requirements this plan guarantees:
- default `USER 10001:10001`, runs with `runAsNonRoot`, `readOnlyRootFilesystem: true`, all capabilities dropped, given `emptyDir` mounts at `/tmp` and `/run/sc`;
- the entrypoint does no operator initialization without `SC_REPO_ROOT` (§14.5).

Image availability: if the install used `SC_TOOLBOX_SOURCE=pulled`, `config/toolbox-image.yaml` records `<toolboxImage>@<toolboxDigest>` from `image.lock`. If it used a built image, sc-bootstrap imports `.bootstrap/toolbox/<inputs-id>.oci.tar` into every node's containerd during `configure` (`ssh node sudo k3s ctr -n k8s.io images import --digests -`; verify the flag) and records the resulting manifest digest as `localhost/strange-company-toolbox@sha256:<digest>` with `imagePullPolicy: Never` semantics noted for plan 15. Plan 47 repeats the import for nodes added by `expand`.

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Neither Docker nor Podman usable | wrapper §9.1 | nothing runs | exit 3 `WRAPPER_ENGINE_MISSING` | bootstrap-preflight-failure.md#toolbox |
| GHCR unreachable / digest missing | pull error | — | build fallback (unless `SC_TOOLBOX_REQUIRE_PULL`), `built-fallback` recorded | bootstrap-preflight-failure.md#toolbox |
| Upstream download changed (sha256 mismatch) | `toolbox fetch` | build fails | exit 3; no image produced | bootstrap-preflight-failure.md#toolbox |
| Upstream download unavailable and no published image | build fails | cannot run | exit 3 | bootstrap-preflight-failure.md#toolbox (wait, or use a mirror via `HTTPS_PROXY`) |
| Image built from a different lock | PF-01 `TOOLBOX_STALE` | — | exit 3 | rebuild with `--toolbox-build` |
| Rootless Podman without subuid range | `podman run --userns=keep-id` error | nothing runs | exit 3 with `/etc/subuid` hint | bootstrap-preflight-failure.md#toolbox |
| SELinux denies volume access | EACCES on `$REPO` inside container | — | should not happen with `label=disable`; wrapper prints `getenforce`/AVC hint | bootstrap-preflight-failure.md#toolbox |
| SSH key permissions too open | wrapper / PF-06 | — | warn / fail | bootstrap-preflight-failure.md#ssh |
| Host runs as root | wrapper step 1 | — | exit 2 unless `SC_ALLOW_ROOT=1` | — |
| Kube tunnel cannot start | §14.6 | kubectl unavailable in shell | warning; command continues | bootstrap-resume.md (node reachability) |
| Disk full during build | engine error | — | exit 3 | free disk, rerun |

## 16. Retry behavior

- Image pull: 3 attempts (10 s, 30 s) before falling back to build.
- `toolbox fetch` downloads: 3 attempts per file with 5 s/15 s backoff; a checksum mismatch is never retried (permanent).
- Kube tunnel: tries each server node once per start; sc-bootstrap's own long-running phases re-establish the tunnel on failure (plan 50 §14.4).

## 17. Idempotency behavior

- The inputs id is a pure function of the tracked toolbox and src content; the same checkout always resolves to the same image (pulled digest or cached local tag). Building twice reuses the engine cache; a cache-less rebuild yields identical tool versions and identical Go binaries (02-R-2).
- `exec`/`shell` leave no state behind except files the command itself writes into the repository or `/blackstart`.

## 18. Bootstrap behavior

The toolbox is the first thing `./bootstrap` resolves. On a fresh machine with no image, the first run either pulls (release tags: ≈ 1–3 min) or builds (development checkouts: ≈ 10 min). Black start (plan 45) uses exactly the same path from the GitHub clone; it needs GHCR **or** the upstream download sites, not any Strange Company–run registry (ADR-0013).

## 19. Upgrade behavior

- Tool upgrades are PRs editing `toolbox/tools.lock.yaml` (Renovate custom managers, plan 23). CI runs `make toolbox` (fresh build), `make toolbox-check`, and the consumers' tests that depend on version-sensitive behavior (plans 03, 06, 07, 18). kubectl and Flux CLI pins move only together with `versions/` (plan 01 §6.3 cross-checks).
- Publishing: plan 23's release pipeline builds both targets for `linux/amd64` (MVP; nodes are amd64-only per plan 08, and arm64 operator machines run the amd64 image under emulation or build locally — arm64 publication is post-MVP), pushes to GHCR, and opens a PR updating `toolbox/image.lock` (`inputsId` equals the release commit's inputs id). Because `image.lock` is excluded from the inputs, that PR does not invalidate itself.
- Base image and Debian snapshot bumps are monthly Renovate PRs (security updates arrive through the snapshot timestamp).

## 20. Recovery behavior

Nothing to recover: images are reproducible from Git. A corrupted local image is removed with `<engine> rmi` and rebuilt or re-pulled on the next run. If GHCR deleted a published digest, the build path still works, and a new release republishes.

## 21. Observability

- Wrapper messages are prefixed `bootstrap:` on stderr: engine and mode, image source and reference, build progress (the engine's own output is captured to `.bootstrap/toolbox/build-<inputs12>.log`).
- `./bootstrap version` prints sc-bootstrap version, toolbox reference and source, and the selfcheck table (plan 50 §7.1).
- No metrics or alerts (off-cluster tool).

## 22. Testing strategy

- **Wrapper unit tests** (`test/e2e/wrapper/`, run by `make toolbox-check` with a stub engine script on `PATH` that records its arguments): flag parsing; env-file parsing (quotes, `~/`, comments, `$()` and backticks are **not** evaluated); inputs-id determinism; image resolution branches (a)–(c); per-mode run flags (§14.4 table); TTY detection; no secret in the recorded engine arguments.
- `shellcheck -s sh` and `shfmt -p` on `bootstrap` (plan 01 `lint`, `fmt-check`).
- **Build tests** (`make toolbox`, tier E): fresh build of both targets for amd64 (the MVP's only published platform); selfcheck passes; size budgets; `gitleaks` over the image filesystem.
- **Engine matrix** (nightly CI, plan 52): Docker rootful on Ubuntu, rootless Podman on Ubuntu, rootless Podman on a Fedora runner/VM with SELinux enforcing (lab VM, plan 05) — run `./bootstrap exec -- true`, file-ownership and tmpfs-mode probes, and `./bootstrap exec -- ssh -V`.
- **Reproducibility test:** the pulled image and a local build of the same commit produce identical `selfcheck --json` tool rows and identical sha256 of `/opt/sc/bin/sc-bootstrap`.

## 23. Acceptance criteria

### GATE-F03

- **02-AC-1** `make toolbox` builds `toolbox` and `ci` from scratch (`--no-cache`) for linux/amd64, and `./bootstrap --toolbox-build exec -- sc-bootstrap toolbox selfcheck --json | jq -e 'all(.tools[]; .ok)'` exits 0.
- **02-AC-2** Every tool listed in SPEC §9 plus `age-keygen`, `ssh-keygen`, `ssh-keyscan`, `git`, `aws`, `kubectl-cnpg`, `etcdutl`, `cmctl`, `gitleaks`, `cosign`, `syft`, `trivy`, `sc-bootstrap` is present (and in the ci image additionally `go`, `actionlint`, `woodpecker-cli`, `kubeconform`, `k3d`, `promtool`, `helm unittest`, `weed`, and the envtest assets under `$KUBEBUILDER_ASSETS`): `./bootstrap exec -- sh -c 'for t in tofu ansible-playbook kubectl helm flux sops age age-keygen ssh ssh-keygen ssh-keyscan curl jq yq git aws kubectl-cnpg etcdutl cmctl gitleaks cosign syft trivy sc-bootstrap; do command -v $t >/dev/null || exit 1; done'` exits 0.
- **02-AC-3** Checksum enforcement: a fixture lock with one wrong sha256 makes the build fail with `TOOLBOX_CHECKSUM_MISMATCH:<tool>` and no image is tagged.
- **02-AC-4** Pull/build equivalence (ADR-0013): for the release commit, `selfcheck --json` tool rows from the pulled image and from `--toolbox-build` are identical, and `sha256sum /opt/sc/bin/sc-bootstrap` is identical in both.
- **02-AC-5** On Docker rootful (Ubuntu), rootless Podman (Ubuntu), and rootless Podman with SELinux enforcing (Fedora): `./bootstrap exec -- sh -c 'touch $SC_REPO_ROOT/out/own-probe && stat -c "%a %u" /run/sc && id -un'` exits 0, the host `stat -c %u out/own-probe` equals `id -u`, `/run/sc` mode is `700` and owned by the container uid, and `id -un` prints `sc-operator` (or the host user name under Podman `keep-id`). On the SELinux host, `ls -Z` labels of `$REPO` and `~/.ssh` are unchanged before versus after.
- **02-AC-6** Credentials are not exposed: while a long-running `./bootstrap exec -- sleep 30` runs with a fixture `bootstrap.env` containing canary values, `docker inspect`/`podman inspect` output and the engine argument log contain none of the canary values, and `./bootstrap exec -- sh -c 'env'` contains none of them either (the env file is parsed only by sc-bootstrap and `sc-env`).
- **02-AC-7** Pipes and exit codes: `./bootstrap exec -- sh -c 'exit 7'; echo $?` prints `7`; `echo hi | ./bootstrap exec -- cat` prints `hi` with no TTY error.
- **02-AC-8** Size budgets: `<engine> image inspect --format '{{.Size}}'` ≤ 1.6 GiB (toolbox) and ≤ 3.5 GiB (ci).
- **02-AC-9** `gitleaks dir` over the exported toolbox filesystem finds nothing, and the image config has no `Env` entry matching `(?i)(secret|token|password|key)=`.
- **02-AC-10** Stale-lock detection: running a toolbox image built from a different `tools.lock.yaml` makes `./bootstrap preflight` exit 3 with `PF-01 TOOLBOX_STALE`.
- **02-AC-11** Env-file safety: a fixture with `SSH_PRIVATE_KEY_PATH=$(touch /tmp/pwned)` results in the literal path being used (and PF-06 failing), and `/tmp/pwned` does not exist on the host.
- **02-AC-12** In-cluster compatibility (k3d, plan 52 `e2e-k3d`): a Pod running the toolbox image with PSA `restricted` settings (`runAsNonRoot`, `readOnlyRootFilesystem`, `capabilities.drop: [ALL]`, `seccompProfile: RuntimeDefault`) and `args: ["sc-bootstrap","version"]` completes with exit 0.
- **02-AC-13** No engine socket: `./bootstrap exec -- sh -c 'ls /var/run/docker.sock /run/podman/podman.sock 2>/dev/null'` prints nothing.

## 24. Explicit non-goals

- Native (non-container) installation of the tools on the operator machine.
- Windows hosts outside WSL2 (plan 50 §24).
- Verifying the published image's cosign signature in the wrapper.
- Using the toolbox as a general development environment beyond `make` targets (editors and language servers are the developer's choice).
- A separate image per tool.

## 25. Open questions

| # | Question | Blocking | Proposed resolution | Owner |
|---|---|---|---|---|
| OQ-02-1 | Do Docker and Podman both honor `uid=`/`gid=` options on `--tmpfs`? | no | Fallback in §14.4 (`mode=1777` + per-uid subdirectory); 02-AC-5 verifies the result on each engine. | 02 |
| OQ-02-2 | The in-cluster platform-probes Deployment (plan 15) pulls a ≈ 1.6 GiB image. | no | Accepted for MVP (Q-15-03). Post-P11 plan 23 may publish a slim `sc-probes` image built from the same Go binary. | 15 / 23 |
| OQ-02-3 | Is `snapshot.debian.org` reliable enough as the only apt source for local builds? | no | Yes for builds (published images do not need it). If unavailable, the wrapper's pull path still works; a build-time mirror can be supplied through `HTTPS_PROXY`. Re-evaluate if nightly `make toolbox` fails on snapshot availability more than twice per quarter. | 02 / 52 |
| OQ-02-4 | AWS CLI v2 bundles its own Python and adds ≈ 200 MiB. A smaller S3 CLI (`s5cmd`, `mc`) would not support the `s3api` operations plans 06 and 49 use (`list-object-versions`, `get-bucket-versioning`). | no | Keep AWS CLI v2. | 02 |
| OQ-02-5 | Plan 19 §8.4 and plan 16 §3 refer to `.bootstrap/secrets/…` files. ADR-0101 forbids generated secrets at rest outside the bundle and SOPS files. | no | Read those paths as `/run/sc/secrets/…` (tmpfs, per run). **Resolved (CD-08):** plans 16 and 19 aligned at consolidation. | 16 / 19 |
