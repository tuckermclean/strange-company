#!/bin/sh
# runner/entrypoint.sh — the coding-runner container's entrypoint.
#
# Written in plain POSIX sh on purpose (no bashisms, and no bash is even
# installed in runner/Dockerfile): this script has exactly one job to do
# per invocation and does not need an interpreter richer than dash.
#
# What this script does, in order (see runner/README.md for the full
# rationale and env-var contract):
#
#   1. Validate required input; fail fast, naming what's missing.
#   2. Configure a bot git identity and, if a token is present, a
#      credential helper that reads it live from an env var.
#   3. Shallow-clone SC_REPO_URL at SC_BASE_REF.
#   4. Create-or-check-out the agent branch named by SC_BRANCH, refusing
#      outright if it is not a safe agent/* name.
#   5. Run the harness argv ("$@") with stdin closed and the repo as cwd,
#      framing its stdout in the STREAM_BEGIN/STREAM_END markers with
#      nothing else in between.
#   6. If the tree changed, commit and push the agent branch. A push
#      failure is logged but never overwrites the harness's exit code.
#   7. Exit with the harness's exit code.
#
# Everything this script itself prints goes to stderr (fd 2), so the
# framed region on stdout (fd 1) between the markers is nothing but the
# harness's own JSONL, verbatim — see the "log-interleaving problem" in
# docs/superpowers/plans/2026-08-26-control-plane-m3.md, M3c.

set -eu

STREAM_BEGIN='::STRANGE-COMPANY-STREAM-BEGIN::'
STREAM_END='::STRANGE-COMPANY-STREAM-END::'

log() {
  printf 'entrypoint: %s\n' "$*" >&2
}

die() {
  # $2, if given, overrides the default exit code of 1.
  printf 'entrypoint: FATAL: %s\n' "$1" >&2
  exit "${2:-1}"
}

# ---------------------------------------------------------------------
# 1. Validate required input.
# ---------------------------------------------------------------------
#
# Required, and set unconditionally by control-plane/internal/jobs/spec.go
# whenever a real coding Job is built (SC_CARD_ID, SC_RUN_ID) or is the
# minimum this script itself needs to do anything useful (SC_REPO_URL,
# SC_BRANCH — spec.go's Build does not currently mark these two required
# at the Go layer, but a runner with no repository or no branch to work on
# cannot proceed, so this script treats them as required regardless).
missing=""
for var in SC_CARD_ID SC_RUN_ID SC_REPO_URL SC_BRANCH; do
  eval "val=\${${var}:-}"
  if [ -z "$val" ]; then
    missing="${missing} ${var}"
  fi
done
if [ "$#" -eq 0 ]; then
  missing="${missing} <harness argv (\$@)>"
fi
if [ -n "$missing" ]; then
  die "missing required input(s):${missing}"
fi

# SC_HARNESS and SC_MODEL are informational (labelling, commit context):
# jobs/spec.go only sets them on the manifest when non-empty, so their
# absence is not fatal here, only worth a note in the pod log.
[ -n "${SC_HARNESS:-}" ] || log "warning: SC_HARNESS not set"
[ -n "${SC_MODEL:-}" ] || log "warning: SC_MODEL not set"

# SC_BASE_REF is NOT currently emitted by control-plane/internal/jobs/spec.go
# (see runner/README.md, "Known gap"). Default it rather than fail, since
# failing fast here would make this image non-functional against today's
# control plane; a future spec.go change should add it explicitly.
if [ -z "${SC_BASE_REF:-}" ]; then
  SC_BASE_REF="main"
  log "warning: SC_BASE_REF not set, defaulting to '${SC_BASE_REF}'"
fi

# ---------------------------------------------------------------------
# 4 (part one). Protected-branch refusal — checked before any git command
# runs, not just before the final push. Spec 24 forbids modifying main;
# this is the last line of defence, so it must be a hard, early failure.
# ---------------------------------------------------------------------
case "$SC_BRANCH" in
  main | master | develop)
    die "refusing to run: SC_BRANCH='${SC_BRANCH}' is a protected branch name"
    ;;
esac
case "$SC_BRANCH" in
  agent/*) ;;
  *)
    die "refusing to run: SC_BRANCH='${SC_BRANCH}' does not start with 'agent/'"
    ;;
esac

# ---------------------------------------------------------------------
# Writable-state setup.
#
# jobs/spec.go's PodSecurityContext sets readOnlyRootFilesystem: true, and
# the only writable mount is the emptyDir at /workspace (workspaceMountPath
# in spec.go). git, npm-installed CLIs, and node itself all expect to be
# able to write to $HOME (.gitconfig, .claude, .codex, .npm) and to a temp
# directory. None of that is optional, so every one of those locations is
# redirected under /workspace here, and the repo itself is checked out to
# a sibling directory rather than /workspace's root — keeping our own
# state directories out of `git status` inside the checkout.
# ---------------------------------------------------------------------
WORKSPACE_ROOT="${SC_WORKSPACE_ROOT:-/workspace}"
REPO_DIR="${WORKSPACE_ROOT}/repo"
export HOME="${WORKSPACE_ROOT}/home"
export XDG_CACHE_HOME="${WORKSPACE_ROOT}/.cache"
export XDG_CONFIG_HOME="${WORKSPACE_ROOT}/.config"
export TMPDIR="${WORKSPACE_ROOT}/tmp"
export GIT_TERMINAL_PROMPT=0

{
  mkdir -p "$HOME" "$XDG_CACHE_HOME" "$XDG_CONFIG_HOME" "$TMPDIR" "$REPO_DIR"
} 1>&2 || die "cannot create writable state under ${WORKSPACE_ROOT} (must be a writable volume)"

# ---------------------------------------------------------------------
# 2. Git identity, and a credential helper if a token was injected.
#
# The identity is a bot identity by default, overridable per-run. The
# credential — if SC_GIT_TOKEN is set at all — is wired through a
# credential helper that reads it from the environment at the moment git
# needs it. The token is never interpolated into this script's output,
# never written into any file (the helper script text names the env var,
# it does not contain the secret), and never placed in the remote URL, so
# it cannot leak into `git remote -v`, `.git/config`, or this container's
# own logs.
# ---------------------------------------------------------------------
{
  git config --global user.name "${SC_GIT_AUTHOR_NAME:-Strange Company Agent}"
  git config --global user.email "${SC_GIT_AUTHOR_EMAIL:-agent@strange-company.invalid}"
  git config --global init.defaultBranch "$SC_BASE_REF"
  git config --global --add safe.directory "$REPO_DIR"

  if [ -n "${SC_GIT_TOKEN:-}" ]; then
    # Single-quoted: the '$SC_GIT_USERNAME' / '$SC_GIT_TOKEN' references
    # below must reach disk as literal text and be expanded only when git
    # invokes this helper later (by which point it is reading directly
    # from its own process environment, inherited unchanged from this
    # container) — never expanded by this script at config-write time.
    git config --global credential.helper \
      '!f() { echo "username=${SC_GIT_USERNAME:-x-access-token}"; echo "password=$SC_GIT_TOKEN"; }; f'
  else
    log "SC_GIT_TOKEN not set; proceeding without a credential helper (clone/push must work unauthenticated or not at all)"
  fi
} 1>&2

# ---------------------------------------------------------------------
# 3. Shallow-clone REPO_URL at BASE_REF.
# ---------------------------------------------------------------------
{
  log "cloning ${SC_REPO_URL} at ${SC_BASE_REF} into ${REPO_DIR}"
  git clone --depth 1 --no-tags --single-branch --branch "$SC_BASE_REF" \
    -- "$SC_REPO_URL" "$REPO_DIR" </dev/null
} 1>&2 || die "git clone of ${SC_REPO_URL} at ${SC_BASE_REF} failed"

cd "$REPO_DIR" || die "cannot cd into cloned repository ${REPO_DIR}"

# ---------------------------------------------------------------------
# 4 (part two). Create or check out the agent branch. Its name is taken
# exactly as given in SC_BRANCH — it is not recomputed here, the control
# plane already derived it (jobs.AgentBranch, spec 16.2).
#
# If the branch already exists on the remote (a previous attempt left
# work there), fetch and continue it. Otherwise branch fresh off the
# already-checked-out base ref.
# ---------------------------------------------------------------------
{
  if git fetch --depth 1 origin "$SC_BRANCH" </dev/null; then
    log "continuing existing remote branch ${SC_BRANCH}"
    git checkout -B "$SC_BRANCH" FETCH_HEAD
  else
    log "creating new branch ${SC_BRANCH} from ${SC_BASE_REF}"
    git checkout -b "$SC_BRANCH"
  fi
} 1>&2 || die "could not create or check out ${SC_BRANCH}"

# ---------------------------------------------------------------------
# 5. Run the harness, stdin closed, cwd = the git repo, stdout framed.
#
# Both CLIs read stdin unless it is explicitly closed (Codex blocks
# forever, Claude Code stalls ~3s) — docs/reference/coding-harness-notes.md.
# The harness runs in the background so a TERM/INT delivered to this
# script (a Kubernetes Job hitting its activeDeadlineSeconds, or an
# eviction) can be forwarded to it and waited on, rather than this script
# exiting out from under a still-running child.
#
# On a normal exit (however the harness itself failed), STREAM_END is
# always printed — a completed-but-failing run is still a complete
# stream. On a killed run, no STREAM_END marker is printed: a missing end
# marker is exactly how the control plane's stream extraction (spec
# §12.1 infra-failure classification) detects that a run was cut short,
# so deliberately not faking one here is load-bearing, not an oversight.
# ---------------------------------------------------------------------
printf '%s\n' "$STREAM_BEGIN"

"$@" </dev/null &
harness_pid=$!

term_handler() {
  log "received termination signal; forwarding to harness (pid ${harness_pid})"
  kill -TERM "$harness_pid" 2>/dev/null || true
  wait "$harness_pid" 2>/dev/null || true
  # Deliberately no STREAM_END here — see comment above.
  exit 143
}
trap term_handler TERM INT

set +e
wait "$harness_pid"
harness_exit=$?
set -e
trap - TERM INT

printf '%s\n' "$STREAM_END"
log "harness exited with code ${harness_exit}"

# ---------------------------------------------------------------------
# 6. Commit and push if the working tree changed. A push failure is
# reported but must never turn a harness failure into a false success,
# nor a harness success into a false failure — $harness_exit, captured
# above, is untouched by anything below.
# ---------------------------------------------------------------------
{
  if [ -n "$(git status --porcelain)" ]; then
    git add -A
    phase="${SC_PHASE:-wip}"
    if [ -n "${SC_COMMIT_SUMMARY:-}" ]; then
      summary="$SC_COMMIT_SUMMARY"
    else
      summary="implementation attempt ${SC_ATTEMPT:-1}"
    fi
    commit_msg="${phase}(card-${SC_CARD_ID}): ${summary}"

    if git commit -m "$commit_msg"; then
      log "committed: ${commit_msg}"
      if git push origin "HEAD:refs/heads/${SC_BRANCH}" </dev/null; then
        log "pushed ${SC_BRANCH}"
      else
        log "WARNING: push to ${SC_BRANCH} failed; preserving harness exit code ${harness_exit}"
      fi
    else
      log "WARNING: git commit failed; nothing to push"
    fi
  else
    log "working tree unchanged; nothing to commit"
  fi
} 1>&2

# ---------------------------------------------------------------------
# 7. Exit with the harness's own exit code, so the control plane's
# infra-vs-failure classification (spec §12.1) stays intact.
# ---------------------------------------------------------------------
exit "$harness_exit"
