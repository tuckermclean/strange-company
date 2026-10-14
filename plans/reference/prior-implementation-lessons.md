# Prior-implementation lessons (Stage-0 input)

**Status:** reference input, non-normative.
**Source:** the implementation that existed before commit `eb02a6c` ("Wipe project; new SPEC.md"). Every file cited below is recoverable with `git show eb02a6c~1:<path>`.

The repository was wiped and restarted from `SPEC.md`. The earlier code is not carried forward, but a few facts it recorded were *verified against real systems* and are expensive to rediscover. Stage-0 plans treat them as evidence about third-party behavior. Where a plan relies on one, it cites this file and keeps a test that re-verifies the fact at implementation time; third-party behavior changes between versions.

---

## L-1 Hermes is the upstream Nous Research Hermes Agent

Source: `docs/reference/hermes-integration-notes.md`, `charts/strange-company/values.yaml` (verified 2026-08-26/27 on a live cluster).

- Image `nousresearch/hermes-agent`, tag-pinned (`v2026.8.19` observed). It must be digest-pinned for production (SPEC §37).
- Bare `hermes` is an interactive TUI and crash-loops without a TTY. The server mode is `hermes gateway`.
- The gateway API server listens on `API_SERVER_PORT` (8642 observed) and **refuses to start without `API_SERVER_KEY`**. It is bearer-authenticated, and wrong or missing keys return 401.
- The dashboard is a separate listener (`HERMES_DASHBOARD=1`, port 9119). It supports native OIDC through `HERMES_DASHBOARD_OIDC_CLIENT_ID`, `HERMES_DASHBOARD_OIDC_ISSUER` and `HERMES_DASHBOARD_PUBLIC_URL`. It fails closed on a non-loopback bind if no auth provider is configured. It rejects the gateway bearer key.
- State lives in `/opt/data` (`HERMES_HOME`). This holds sessions, `.env`, `config.yaml`, `state.db` and `auth.json`. **It is not a cache.**
- **Credential precedence hazard:** `~/.hermes/.env` loads with `override=True`, and dashboard writes (`PUT /api/env`) persist on the PVC. A hand-entered key therefore silently beats container environment variables and survives redeploys.
- **Managed scope fixes this:** `hermes_cli/managed_scope.py` loads a root-owned directory (`$HERMES_MANAGED_DIR`, default `/etc/hermes`) last, with `override=True`, per leaf key. A read-only projected Secret mounted there pins operator-controlled keys.
- Gateway sessions: `POST /api/sessions` creates a session without a model call. Sessions can be read, updated, deleted and forked. Each turn goes to `POST /api/sessions/{id}/chat`.
- **Silent failure modes:** an unknown model string is accepted at create time and only fails at the first turn. **Backend errors come back as HTTP 200** with `finish_reason: "error"` and the error text in `content`. Clients must check `finish_reason` before trusting the content.
- **The gateway ignores a requested `model`** and routes by its own global configuration (`route_source: "global"`). `GET /v1/models` only advertises `hermes-agent`. *Consequence:* Hermes must not sit in the path of policy-selected worker model calls. Policy-routed calls go to the provider named by policy (in this plan set, through the Model Gateway; see ADR-0004).
- Profiles are a URL prefix (`/p/<profile>/...`) and only work with multiplexing on. A wrong prefix is silently served by the default profile, so don't depend on profiles.

## L-2 Coding-harness CLI behavior

Source: `docs/reference/coding-harness-notes.md`, `runner/README.md` (Claude Code 2.1.246 and Codex CLI 0.149.1, captured 2026-08-26).

- **Both CLIs read stdin.** Codex blocks forever and Claude Code stalls for 3 seconds. Job entrypoints must redirect `< /dev/null`.
- Codex refuses to run outside a git repository unless given `--skip-git-repo-check`.
- Claude Code in non-`--bare` mode loads project hooks and `CLAUDE.md` from the checkout. That is a **prompt-injection and code-execution surface coming from repository content**. Worker sandboxing must assume repository content is hostile.
- Claude Code `-p --output-format stream-json --verbose` ends with a `result` event carrying `is_error`, `subtype`, `num_turns`, `total_cost_usd`, `usage` and `permission_denials`. Codex `exec --json` ends with `turn.completed` carrying usage only: no status and no cost. Its success comes from the exit code plus the presence of a terminal event.
- Kubernetes merges stdout and stderr. The earlier runner framed harness output between `::STRANGE-COMPANY-STREAM-BEGIN::` and `::STRANGE-COMPANY-STREAM-END::`. A missing END marker meant "killed mid-run", which is an infrastructure failure, not a harness verdict.
- The image ran non-root as uid/gid 65532, with no bash, and pinned harness CLI versions exactly.

## L-3 Harness-reported cost is unreliable

Source: `docs/reference/opencode-cost-is-unreported.md`, `docs/reference/deepseek-pricing.md`.

- `opencode run --format json` drops its `step_finish` (usage and cost) events when run in a container. The ledger recorded $0 for real work.
- **A budget enforced against a blind ledger is not enforced.** Totals that include unpriced invocations are *floors* and must say so (`cost_complete: false`). Treat unknown spend as unknown, never as zero.
- Some providers (DeepSeek) have no per-request cost API. Cost must be computed from token usage and a versioned rate card.
- **Time-of-day pricing exists** (DeepSeek off-peak is half price on a weekday UTC schedule). Pricing tables must support time-windowed rates. A flat rate is the wrong number, and a malformed schedule must be rejected at load.
- Cache accounting must be disjoint. Some APIs include cached tokens inside `prompt_tokens`, so normalization must avoid double-charging.
- "Zero does not mean free": an absent rate card must not be confused with a genuinely zero price. The pricing schema distinguishes `unpriced` from `0`.
- *Consequence:* these lessons drive ADR-0004. A Strange Company–owned Model Gateway measures usage from provider responses itself instead of trusting harness self-reports.

## L-4 Operational control lessons

Source: `docs/reference/stopping-a-card.md`, `docs/specs/policy-selected-models.md`, `docs/specs/single-source-credentials.md`.

- Operators need a discoverable stop lever. "Move to Blocked" worked because an agent could not undo it: `Blocked → *` transitions were **human-only**.
- Separate *infrastructure failures* (evictions, provider timeouts, killed pods) from *merit failures* (tests fail). Retry budgets must not burn merit attempts on infrastructure noise, but consecutive infrastructure failures must also be bounded.
- "Policy selects model X" must be the model that actually answers. Log the requested model **and** the provider-reported model, and alert on mismatch.
- A fresh install must not depend on hand-created Secrets whose names are only discoverable from compiled code. A Secret holding an empty string is worse than a missing one. Fail closed with a clear error.

## L-5 Storage

Source: `charts/strange-company/values.yaml`.

- On Longhorn, a single-replica `Recreate` Deployment with an RWO volume sometimes stuck on Multi-Attach after rollout because of detach lag. Plans for Longhorn-backed singletons (Forgejo, Hermes) must include this failure mode and its recovery.

## L-6 GitHub credentials

Source: `docs/reference/github-app.md`.

- Prefer GitHub App installation tokens (1-hour, per-repository) over PATs where automation needs GitHub write access.
- The `workflows` permission lets a credential change the checks that verify the work. Grant it only where unavoidable. Note that Forgejo's push mirror needs it if `.github/workflows/**` is mirrored.
