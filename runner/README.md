# runner — the coding-runner image

The container image that runs inside every `agent-runs` Kubernetes Job (spec
§16). It carries git and both coding-CLI harnesses; `entrypoint.sh` clones
the target repository, creates or continues the agent branch, runs the
harness, and pushes the result — never touching `main`.

This is the image referenced by `control-plane/internal/jobs/spec.go`'s
`Spec.Image`; that package builds the Job manifest, this image is what runs
inside it.

## What's in the image

- **Base:** `node:20.18.1-bookworm-slim` — Debian, pinned by exact tag (not
  `20`, not `lts`, not `latest`), because both harnesses are npm packages
  and Debian makes `git` a one-line `apt-get install`.
- **git**, **ca-certificates**, **openssh-client** via `apt-get`.
- **Claude Code** `@anthropic-ai/claude-code@2.1.246`
- **Codex** `@openai/codex@0.149.1`

  Both versions are pinned exactly, matching the versions the behaviour in
  `docs/reference/coding-harness-notes.md` was actually captured against.
  Never bump to `latest` — an unpinned harness is an unreviewed, silent
  change to what every coding Job in the cluster does.
- **Non-root user**, uid/gid `65532:65532` — the exact `nonRootUID`/
  `nonRootGID` constants in `control-plane/internal/jobs/spec.go`, which
  the Job's `PodSecurityContext` also sets. Kubernetes enforces this
  regardless of what the image declares; the image declares it too so a
  bare `docker run` (no `securityContext`) still refuses to run as root.
- **No bash.** The entrypoint is written in plain POSIX `sh` (dash, already
  part of `bookworm-slim`) and no bash is installed anywhere in the image.

## The entrypoint's env-var contract

Names use the `SC_` prefix that `control-plane/internal/jobs/spec.go`
already uses for the run-identifying variables it sets unconditionally
(`buildEnv`'s `SC_CARD_ID` / `SC_RUN_ID` / `SC_HARNESS` / `SC_MODEL` /
`SC_REPO_URL` / `SC_BRANCH`). Everything below that isn't one of those six
is this script's own addition — called out explicitly, not silently
assumed.

### Required — the script fails fast, naming what's missing, if any of these are empty

| Variable | Set by `jobs.Build`? | Used for |
|---|---|---|
| `SC_CARD_ID` | yes, always (`Build` validates it non-empty) | commit message |
| `SC_RUN_ID` | yes, always (`Build` validates it non-empty) | logging/correlation |
| `SC_REPO_URL` | yes, when `Spec.RepoURL` is set — **not validated non-empty by `jobs.Build` itself** | `git clone` target |
| `SC_BRANCH` | yes, when `Spec.Branch` is set — **not validated non-empty by `jobs.Build` itself** | agent branch name (see below) |
| harness argv (`"$@"`) | — passed as the container's `command`/`args`, not an env var | what actually runs |

`SC_REPO_URL` and `SC_BRANCH` are listed as required by *this script*
even though `jobs.Build` doesn't reject an empty value for either at the
Go layer — a runner with no repository or no branch to work on cannot do
anything useful, so the entrypoint treats their absence as the same class
of fatal misconfiguration as a missing card ID.

### Optional, with defaults

| Variable | Default | Notes |
|---|---|---|
| `SC_HARNESS` | — | informational only; warned if absent, not fatal |
| `SC_MODEL` | — | informational only; warned if absent, not fatal |
| `SC_BASE_REF` | `main` | **known gap, see below** |
| `SC_GIT_TOKEN` | unset → no credential helper configured | see "Git credentials" |
| `SC_GIT_USERNAME` | `x-access-token` | paired with `SC_GIT_TOKEN`; `x-access-token` is the conventional username for a GitHub App installation token |
| `SC_GIT_AUTHOR_NAME` | `Strange Company Agent` | commit author |
| `SC_GIT_AUTHOR_EMAIL` | `agent@strange-company.invalid` | commit author |
| `SC_PHASE` | `wip` | commit message prefix, e.g. `plan`, `test`, `wip`, `feat` (spec §16.2) |
| `SC_ATTEMPT` | `1` | used in the default commit summary when `SC_COMMIT_SUMMARY` isn't set |
| `SC_COMMIT_SUMMARY` | `implementation attempt ${SC_ATTEMPT}` | overrides the summary half of the commit message, e.g. `attach implementation plan` |
| `SC_WORKSPACE_ROOT` | `/workspace` | must match `jobs.go`'s `workspaceMountPath` |

### Known gap: `SC_BASE_REF`, `SC_PHASE`, `SC_ATTEMPT`, `SC_COMMIT_SUMMARY` are not yet emitted by `jobs.Build`

`docs/superpowers/plans/2026-08-26-control-plane-m3.md` (M3c) describes the
entrypoint cloning "`REPO_URL` at `BASE_REF`" and committing with a
phase-and-attempt-aware message, but
`control-plane/internal/jobs/spec.go`'s `Spec`/`buildEnv` — as it exists as
of this task — has no field for the base ref, the phase, the attempt
number, or a free-text commit summary. Rather than make the whole image
non-functional against today's control plane by hard-failing on their
absence, the entrypoint defaults `SC_BASE_REF` to `main` (with a warning
to stderr) and defaults the commit message to
`wip(card-<id>): implementation attempt 1`. **A follow-up should add these
to `jobs.Spec` and `buildEnv`** so the control plane can actually drive
which base branch to clone and which phase/attempt a given run represents,
rather than this image guessing.

## The stream-marker protocol, and why it exists

Kubernetes merges a container's stdout and stderr into one log stream. The
`runner` package's adapters (`control-plane/internal/runner/claudecode.go`,
`codex.go`) parse the harness's JSONL output and deliberately **error on any
malformed line** — a truncated or corrupted stream must never be mistaken
for a successful run. A single line of `git clone` progress output landing
on stdout in the middle of that JSONL would corrupt the parse.

So the entrypoint frames the harness's stdout, and only the harness's
stdout, between two markers:

```
::STRANGE-COMPANY-STREAM-BEGIN::
{...harness JSONL, one line per event, verbatim...}
::STRANGE-COMPANY-STREAM-END::
```

Every other line this script produces — clone progress, checkout/commit/push
diagnostics, warnings — is written to stderr, so it stays visible to a human
running `kubectl logs`, but is invisible to the control plane's parser,
which extracts only the framed region (`control-plane/internal/runner`'s
stream-extraction logic, M3c Task 1).

**The end marker is deliberately *not* printed if the script is killed**
(`SIGTERM`/`SIGINT` — a Job hitting its `activeDeadlineSeconds`, or an
eviction). The entrypoint traps those signals, forwards them to the running
harness, waits for it, and exits without ever writing
`::STRANGE-COMPANY-STREAM-END::`. A stream with a begin marker and no end
marker is exactly how the control plane recognizes "this Job was killed
mid-run" as an infrastructure failure (spec §12.1) rather than a harness
failure — faking an end marker on a kill would hide that signal, not
preserve it.

On a normal exit — including a harness that ran to completion and *failed*
— the end marker is always printed. A completed-but-failing run is still a
complete stream; only a truncated one is ambiguous.

## Protected-branch refusal

Before any git command runs, the entrypoint checks `SC_BRANCH` against a
hard denylist/allowlist:

- Exactly `main`, `master`, or `develop` → refuse (exit non-zero, no clone
  attempted at all).
- Anything not starting with `agent/` → refuse.

The branch name itself is never recomputed here — the control plane already
derived it (`jobs.AgentBranch`, spec §16.2: `agent/<card-id>-<slug>`) and
passes it in via `SC_BRANCH`. This check exists purely as a last line of
defence: spec §24 forbids modifying `main` under any circumstance, and a
compromised or misconfigured caller passing the wrong branch name must not
be able to make this script touch it.

## Git credentials

If `SC_GIT_TOKEN` is set, the entrypoint configures a git credential helper
as an inline shell function:

```sh
git config --global credential.helper \
  '!f() { echo "username=${SC_GIT_USERNAME:-x-access-token}"; echo "password=$SC_GIT_TOKEN"; }; f'
```

The single quotes matter: the `$SC_GIT_TOKEN` reference reaches `.git/config`
as literal text, and is expanded only when git later *invokes* the helper —
at which point it reads directly from that process's environment (inherited
unchanged from the container, which got it from a Kubernetes Secret via
`jobs.Spec.Env`, per spec §29). The result:

- The token is **never** interpolated into any file this script writes —
  only the *name* of the env var is written to `.gitconfig`, never its
  value.
- The token is **never** embedded in the remote URL, so it cannot leak via
  `git remote -v`, `.git/config`'s `[remote "origin"]` section, or a clone
  URL echoed into a log line.
- The token is **never** echoed by this script itself.

If `SC_GIT_TOKEN` is absent, no credential helper is configured at all —
clone/fetch/push then either work unauthenticated (a public repo) or fail
with git's own auth error, surfaced on stderr like any other git failure.

`GIT_TERMINAL_PROMPT=0` is set unconditionally so a missing/wrong credential
fails immediately instead of hanging the Job waiting for interactive input
that will never come — the same class of bug as the stdin-closing
requirement below.

Only HTTPS remotes are supported by this credential mechanism. `openssh-client`
is installed for completeness, but no SSH key provisioning is wired up; an
`ssh://` or `git@`-style `SC_REPO_URL` is out of scope for this task.

## Operational gotchas this entrypoint exists to work around

Both taken directly from `docs/reference/coding-harness-notes.md`, captured
from the real CLIs rather than assumed:

- **Both CLIs read stdin unless it's explicitly closed.** Codex blocks
  *indefinitely* on `Reading additional input from stdin...`; Claude Code
  stalls ~3 seconds before proceeding with a warning. A Kubernetes Job has
  no stdin at all, so the entrypoint always runs the harness as
  `"$@" </dev/null`.
- **Codex refuses to start outside a git repository.** The entrypoint always
  `cd`s into the freshly cloned/checked-out repo before running the harness
  argv, so by the time `"$@"` executes, the working directory both requires
  and has a `.git`.

One more, specific to this image rather than the CLIs themselves:

- **`readOnlyRootFilesystem: true`** (set by `jobs.Build`'s
  `ContainerSecurityContext`, spec §16.1) plus **a single writable mount**
  (the `workspace` `emptyDir` at `/workspace`) means git's global config,
  both CLIs' cache/config directories, and any temp files they create all
  need somewhere writable that isn't the container's root filesystem. The
  entrypoint redirects `HOME`, `XDG_CACHE_HOME`, `XDG_CONFIG_HOME`, and
  `TMPDIR` to subdirectories of `/workspace`, and clones the repository into
  `/workspace/repo` (a sibling, not `/workspace` itself) so none of that
  redirected state shows up as untracked noise in `git status` inside the
  checkout.

## Assumptions made for this task

- Git identity defaults (`Strange Company Agent` /
  `agent@strange-company.invalid`) are placeholders; override via
  `SC_GIT_AUTHOR_NAME` / `SC_GIT_AUTHOR_EMAIL` if the org wants a different
  bot identity.
- `SC_GIT_USERNAME` defaults to `x-access-token`, the conventional username
  GitHub expects alongside a GitHub App installation token used as the
  password; a personal-access-token setup typically ignores the username
  value entirely, so this default is harmless either way.
- Kubernetes' default `emptyDir` mount is world-writable (mode `0777`)
  regardless of the mounting container's uid, which is why `mkdir -p` under
  `/workspace` as uid `65532` is expected to succeed without the Job needing
  a `fsGroup`. `jobs.Build` does not set one today; if that ever changes to
  a CSI-backed volume or a stricter default, the entrypoint's `mkdir -p`
  failure path (`die "cannot create writable state under ..."`) surfaces the
  problem immediately rather than failing silently later.
- Only a shallow, single-branch clone is attempted at `SC_BASE_REF`; a
  `SC_BASE_REF` that is a bare commit SHA rather than a branch/tag name is
  not supported (matches `git clone --branch`'s own limitation).
