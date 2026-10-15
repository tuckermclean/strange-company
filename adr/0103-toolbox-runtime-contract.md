# ADR-0103: Toolbox runtime contract — pull-by-digest-or-build via a git-computed inputs id; file-mounted credentials; no host relabeling

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 02

## Context
ADR-0013 makes the toolbox locally buildable and pullable by digest. The wrapper `./bootstrap` must decide which to use with only POSIX sh, Git and Docker or Podman on the host, must work with rootful Docker, rootless Docker, rootless Podman and SELinux-enforcing hosts, and must not leak credentials or damage host files.

## Decision
- **Inputs id.** The wrapper computes a Git object id over the sorted `<blob-id> <path>` list of tracked and untracked-not-ignored files in `toolbox/`, `src/` (minus tests and test data) and `.dockerignore`, excluding `toolbox/image.lock`. If it equals `image.lock`'s `inputsId`, the published image is pulled **by digest**; otherwise (development checkout) a local image tagged with the inputs id is reused or built. A failed pull falls back to a build unless `SC_TOOLBOX_REQUIRE_PULL=1`. The Strange Company binaries are compiled inside the image build, reproducibly.
- **Credentials as files.** `bootstrap.env` and the SSH key are bind-mounted read-only under `/run/sc-input/`; the key is copied into the container tmpfs `/run/sc` with mode 0600. Nothing secret appears in `run` arguments or the initial environment.
- **Identity.** The container runs as the invoking host user (`--user uid:gid`, Podman `--userns=keep-id`, or uid 0 under rootless Docker); `nss_wrapper` supplies a passwd entry so OpenSSH works with arbitrary uids. Host files written by the toolbox are owned by the host user.
- **SELinux.** The wrapper uses `--security-opt label=disable` for the toolbox container and never `:z`/`:Z`; it never relabels, chowns or chmods host paths.
- **Same-path repository mount.** The repository is mounted at its host absolute path (worktree common dirs too); the black-start directory at `/blackstart`.
- **Hardening.** `--cap-drop ALL`, `no-new-privileges`, no engine socket in the toolbox image.

## Consequences
- Release checkouts pull in about a minute; development checkouts build once per inputs change (about 10 minutes).
- The published image's integrity rests on the digest pinned in Git; the wrapper does not verify signatures.
- Disabling SELinux labeling for this one container means SELinux does not confine the toolbox; accepted because the toolbox is trusted operator code holding the operator's authority anyway.

## Alternatives rejected
- Tag-based pulls (`:v1.2.3`): mutable.
- Building sc-bootstrap at container start from source: needs Go in the toolbox and slows every run.
- `:Z` relabeling of the repository and `~/.ssh`: breaks sshd's `ssh_home_t` expectations and other tools.
- Passing `bootstrap.env` with `--env-file`: Docker's parser differs from plan 50's grammar and exposes values in `docker inspect`.

## Verification
F03 gate (plan 02): 02-AC-4 (pull/build equivalence), 02-AC-5 (ownership, tmpfs mode, unchanged SELinux labels on three engine modes), 02-AC-6 (no canary value in inspect output, arguments or environment), 02-AC-11 (env file never evaluated).
