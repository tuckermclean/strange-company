# ADR-0301: Woodpecker serves every pipeline configuration through an exclusive, trust-classifying config extension

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 20 (consumed by 22, 23, 52)

## Context
Woodpecker normally runs the `.woodpecker/*.yaml` found at the commit under test. Anyone who can push a ref can therefore choose:
- which workflows run;
- which secrets they request (Woodpecker secrets filter by **event** and **image**, never by branch);
- which agent labels they target (and so the build agent);
- the step `backend_options.kubernetes` (security context, resources, volumes, tolerations).

Workers hold a write deploy key that can push any unprotected ref, and tags (ADR-0007, SI-06). Humans open PRs from feature branches. SPEC §36–§38 require trusted CI: untrusted refs must never reach release secrets or the build lane, and ADR-0008 requires verification to use base-revision configuration.

Woodpecker 3.x supports a global configuration extension (`WOODPECKER_CONFIG_EXTENSION_ENDPOINT`) with an **exclusive** mode (`WOODPECKER_CONFIG_EXTENSION_EXCLUSIVE=true`). In that mode Woodpecker skips reading configuration from the forge and executes whatever the extension returns. Requests are signed with an ed25519 HTTP signature whose public key is served at `/api/signature/public-key`. This was verified against the 3.19 documentation on 2026-10-13, and the exact names are re-verified at P08.

## Decision
1. **Exclusive extension.** `sc-ci-config` (Go, namespace `woodpecker`) is the global configuration extension in exclusive mode for every repository. Woodpecker never executes YAML taken from the commit under test unless `sc-ci-config` selected that commit as trusted.
2. **Trust classification.** For each request, `sc-ci-config` computes `trust ∈ {protected, untrusted}` and the **config revision**:
   - `protected`: the event is `push` to a protected branch (`main`, `release/*`), `tag` whose commit is reachable from a protected branch, or `cron` whose branch is protected. The config revision is the commit itself (it was merged through review).
   - `untrusted`: every other event, including all `pull_request`, all `push` to unprotected refs, and all `manual` events. The config revision is the **head of the default branch**. The one exception is a Runner verification run with a registered intent (ADR-0302), whose config revision is the task's **base commit**.
3. **Untrusted configurations are filtered.** In untrusted mode, `sc-ci-config` drops any workflow that references `from_secret`, targets `sc-lane: build`, sets `privileged`, `volumes`, `backend_options.kubernetes.{volumes, serviceAccountName, runtimeClassName, hostUsers, nodeSelector, tolerations, affinity}` or a non-digest image. It records the drop in a metric. It then **forces** `labels: {sc-lane: ci}` on every remaining workflow.
4. **All configurations are normalized.** In both modes, `sc-ci-config` injects the restricted security context and the lane's resource defaults into every step (including an explicit pinned `clone` step), and requires digest-pinned images from the registry allowlist in `policy/ci.yaml`.
5. **Two lanes, two agents.**
   - `woodpecker-agent` (label `sc-lane=ci`) runs pods in `ci-jobs`: PSA `restricted`, no secrets.
   - `woodpecker-agent-build` (label `sc-lane=build`) runs pods in `ci-build`: plan 23 owns its admission exception.
   A workflow reaches the build lane only if it was served in protected mode and asks for `sc-lane: build`.
6. **Secrets defense in depth.** Publishing secrets are Woodpecker secrets restricted to events `[tag]` and to the `sc-release` image (plan 23; maintenance-only secrets may use `[cron]`). Each agent authenticates with its own registered token, and `sc-ci-maint` checks that build-lane workflows ran only on the registered build agent (plan 20 §8.7). Because untrusted configurations can neither reference them nor reach the build lane, the missing branch filter is compensated.
7. **Fail closed.** If the extension is unreachable, fails signature verification, finds repository settings that violate policy (any `trusted.*` flag true, timeout above policy) or cannot resolve a trusted revision, no pipeline runs.

## Consequences
- A PR cannot change its own CI. Changes to `.woodpecker/**` take effect after merge, and the PR's `make verify` covers them statically (`woodpecker-cli lint` inside `make lint`). This is the accepted cost.
- `sc-ci-config` is a critical-path component of trusted CI. It gets its own alerts and a gate.
- The extension needs read access to Forgejo repository contents. Bot `sc-ci-config` (read-only, plan 16) is used, never Woodpecker's netrc (`WOODPECKER_CONFIG_EXTENSION_NETRC=false`).
- Projects without `.woodpecker/` at the config revision get no pipelines. Verification for them uses the platform verify workflow (plan 20 §8.4).

## Alternatives rejected
- Repository "approval" gating (`require_approval: all_events`): humans would have to approve every agent and verification pipeline, and approval does not filter secrets.
- Woodpecker's trusted-repository flags alone: they control privileges, not which YAML runs.
- Per-branch secrets through a secret extension: it duplicates plan 23's secret store, and still leaves labels and backend options under untrusted control.
- Non-exclusive extension mode: Woodpecker would still fetch, and the request would carry, the untrusted files. A bug that returned 204 ("keep existing") would execute them.

## Verification
P08 gate (plan 20, 20-AC-12..20-AC-19): a PR that adds a `from_secret` workflow, a `sc-lane: build` label, a privileged step or `serviceAccountName` gets none of them executed; a tag on an unreachable commit runs no release workflow; an unsigned request to `sc-ci-config` gets 401; with `sc-ci-config` scaled to 0, a push creates no running pipeline; a repository with `trusted.security=true` is refused.
