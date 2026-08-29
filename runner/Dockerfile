# The coding-runner image: git plus both coding CLIs, run as a fixed
# non-root user, for the Kubernetes Jobs built by
# control-plane/internal/jobs/spec.go (spec 16). See runner/README.md for
# the full env-var contract the entrypoint expects.
#
# Base: a Debian-based Node image (not Alpine) because both CLIs are npm
# packages that assume a glibc/Debian userland is fine, and Debian makes
# installing git trivial via apt. Pinned by exact tag, not "20", "lts", or
# "latest" — a base image bump is a deliberate, reviewable change here.
FROM node:20.18.1-bookworm-slim

# Pinned CLI versions, captured from real behaviour in
# docs/reference/coding-harness-notes.md (2026-08-26). Never "latest": an
# unpinned harness version is an unreviewed, silent change to what every
# coding Job in the cluster runs.
ARG CLAUDE_CODE_VERSION=2.1.246
ARG CODEX_VERSION=0.149.1
ARG OPENCODE_VERSION=1.18.25

# git: the persistent workspace (spec 16.2) — this image's whole reason to
# exist. ca-certificates: TLS for git-over-https and both CLIs' API calls.
# openssh-client: present for completeness (git can be pointed at an ssh
# remote), though the entrypoint's credential helper only speaks https.
# No extra shells or interpreters are installed: /bin/sh (dash, already
# part of bookworm-slim) is all the entrypoint needs, and bash is not
# installed at all — the entrypoint is plain POSIX sh.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
       git \
       ca-certificates \
       openssh-client \
    && rm -rf /var/lib/apt/lists/*

# Installed as root, before the user switch below, so the packages live in
# the image's (read-only at runtime) filesystem rather than needing to be
# writable. --omit=dev keeps transitive dev tooling out of the layer.
RUN npm install -g --omit=dev \
       @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION} \
       @openai/codex@${CODEX_VERSION} \
       opencode-ai@${OPENCODE_VERSION} \
    && npm cache clean --force

# Non-root user at the exact uid/gid the Job's PodSecurityContext requires
# (control-plane/internal/jobs/spec.go: nonRootUID = nonRootGID = 65532).
# Kubernetes' runAsUser/runAsGroup would enforce this regardless of what
# the image declares, but declaring it here too means `docker run` without
# any Kubernetes securityContext still refuses to run as root, and file
# ownership below is correct from the first `docker build` on.
RUN groupadd --gid 65532 runner \
    && useradd --uid 65532 --gid 65532 --create-home --shell /usr/sbin/nologin runner \
    && mkdir -p /workspace \
    && chown -R runner:runner /workspace

COPY --chown=runner:runner entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 0555 /usr/local/bin/entrypoint.sh

USER runner:runner
WORKDIR /workspace

# The card's spec/plan/task text and the harness's own flags are passed as
# argv to the entrypoint by the control plane (they are not baked into the
# image), so there is no CMD here — only the fixed ENTRYPOINT.
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
