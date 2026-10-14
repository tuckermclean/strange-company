# 27 — Worker contract (`sc-worker`)

**Status:** Stage-0 plan (draft)
**DAG nodes:** F07 (primary, with 28: worker SDK + fake harness), C02 (co-owner with 26 and 37: live worker Jobs)
**Related plans:** [21](21-tdd-workflow.md), [22](22-verification-policy.md), [25](25-context.md), [26](26-agent-runner.md), [28](28-harnesses.md), [29](29-task-state-machine.md), [30](30-handoffs.md), [34](34-security.md), [35](35-secrets.md), [36](36-network-policy.md), [37](37-admission-policy.md), [40](40-opentelemetry.md), [43](43-retention-gc.md), [53](53-model-gateway.md), [55](55-forge-workflow.md)
**Contracts:** owns `worker-input.schema.yaml`, `worker-result.schema.yaml`; consumes `context.schema.yaml` (25), `handoff.schema.yaml` (30), `harness.schema.yaml` (28), `runner-api.openapi.yaml` worker endpoints (26), `telemetry.schema.yaml` (40)
**ADRs:** relies on 0002, 0004, 0007, 0008, 0014, 0017, 0551, 0552; introduces ADR-0402 (sc-worker owns Git and credentials; the harness never commits or pushes)

> Every section below is mandatory (SPEC §121).

## 1. Purpose

`sc-worker` is the Strange Company–owned adapter binary (Go, ADR-0002, `src/cmd/sc-worker`) shipped in every harness image. Every worker pod has **two containers** from the same image (plan 26 §13; review TDD-14): `sc-worker run` (PID 1 of the trusted `sc-worker` container, which alone mounts the deploy key and the Runner task token) and `sc-worker shim` (PID 1 of the untrusted `harness` container, which runs the harness CLI and the repository's local validation commands and mounts only the gateway and egress tokens). They coordinate only through files in the shared `/workspace` emptyDir (§7.4). It implements the common worker lifecycle of SPEC §67 so that no coding-harness vendor defines what an agent is (SPEC §64):

```text
start → fetch packet (task token) → read mounted per-execution credentials → clone over SSH (deploy key, pinned known_hosts)
 → checkout exact start_commit → branch topology per role → materialize context outside the working tree
 → invoke harness through its adapter (stdin closed, framed output, timeout < pod deadline, signal forwarding)
 → collect the harness claim → advisory local validation → pre-checks → commit (identity, message, trailers)
 → push agent/<task-id> → post structured result → exit
```

The harness (Claude Code, Codex, OpenCode or the fake) only edits files in the working tree and writes a structured *claim*. Everything that touches Git remotes, credentials, the Runner and the result contract is done by `sc-worker` in its own container, which never executes repository-controlled code (ADR-0402 as amended). The Runner never trusts the result; it re-derives every Git fact (plan 26 §14.4). `sc-worker` pre-checks are a cheap, advisory first line that saves a quarantine round-trip.

## 2. Requirements derived from SPEC.md

1. Workers are short-lived Kubernetes Jobs that receive one bounded task, clone, perform one role, produce durable artifacts, report a structured result and exit [SPEC §4.5, §67].
2. The lifecycle is common to every harness: retrieve task, obtain scoped credentials, clone, checkout exact commit, create `agent/<task-id>`, load context/policy, perform role, run allowed local validation, commit, push, report, exit [SPEC §67].
3. The result is structured and carries a handoff with `what_i_did`, `what_remains`, `recommended_next_role`, `questions`, `known_risks` [SPEC §68, §69].
4. Clones, build trees and temporary outputs live in `emptyDir` [SPEC §28]; durable output belongs in Git [SPEC §28, §4.1].
5. Workers hold only a scoped Forge credential, a model credential and a task-specific Runner token [SPEC §86]; never cluster-admin, Flux, CNPG/Longhorn admin, signing, registry-push or deployment credentials [SPEC §38, §86].
6. Workers push only `agent/<task-id>` and never protected branches [SPEC §87, §100].
7. The tester's commit contains tests, fixtures, minimal test infrastructure and verification artifacts only, and is named `test(<task-id>): …` [SPEC §49]; implementation starts from the red commit or a descendant [SPEC §52].
8. Agent-local validation is advisory [SPEC §36]; trusted CI decides.
9. Workers run non-root, without privilege escalation, capabilities, SA token or hostPath, under Restricted PSA [SPEC §84] (Job built by plan 26).
10. Telemetry and logs never carry secret values or environment variables; prompts/completions never reach general telemetry [SPEC §94].
11. The tester never receives a proposed implementation [SPEC §48]; workers never receive unrestricted Hermes history [SPEC §66].

Derived requirements:

- **27-R-1** `sc-worker` is the only process in the pod that runs `git commit` / `git push` on behalf of the execution. Harness-made commits are folded into one `sc-worker` commit (§14.10).
- **27-R-2** Secrets are files, never pod environment variables. The `sc-worker` container mounts `task-token`, `deploy-key`, `known_hosts` at `/var/run/sc/secrets`; the `harness` container mounts only `gateway-token` and `egress-token` at `/var/run/sc/harness-secrets`. The shim places the gateway token into the harness *child-process* environment (or a 0600 config file under `$HOME`) per `HarnessRegistry.gateway.tokenDelivery`, and the egress token only inside the proxy URL (§14.7). The deploy key and the Runner task token are never present in the harness container.
- **27-R-3** All `sc-worker` output that reaches the pod log or the Runner is scrubbed against the exact values of every credential issued to the execution plus the shared pattern set (§13.4).
- **27-R-4** The harness timeout is strictly less than the pod deadline: `harness_timeout_seconds = profile.deadline − profile.harnessReserve` (resource-profile contract), re-clamped at run time to `deadline_at − now − 60 s`.
- **27-R-5** Harness output is framed between `::STRANGE-COMPANY-STREAM-BEGIN::` and `::STRANGE-COMPANY-STREAM-END::` (L-2). A missing END marker means "killed mid-run", an infrastructure failure, never a harness verdict.
- **27-R-6** Every terminal path that can still reach the Runner posts exactly one schema-valid result. The only paths that post nothing are: task token missing, packet unobtainable, SIGKILL.
- **27-R-7** Git operations by `sc-worker` after the harness ran never execute repository-controlled code: hooks, filter drivers, fsmonitor and alternates are neutralized (§13.3).
- **27-R-8** Context files are materialized **outside** the working tree and are never committed.

## 3. Inputs

| Input | Source | Format / location |
|---|---|---|
| Environment (non-secret) | Job spec (plan 26 builder) | `SC_RUNNER_URL`, `SC_TASK_ID`, `SC_EXECUTION_ID`, `SC_SECRETS_DIR=/var/run/sc/secrets`, `SC_WORKSPACE=/workspace`, `HOME=/home/worker`, `TMPDIR=/tmp` |
| Per-execution Secret | Runner, mounted read-only at `/var/run/sc/secrets` (`defaultMode 0440`) | files `task-token` (JWS), `gateway-token` (JWS), `deploy-key` (OpenSSH ed25519 private key), `known_hosts` (pinned Forgejo host key lines) |
| Task packet | `GET {SC_RUNNER_URL}/v1/worker/packet` | `contracts/worker-input.schema.yaml` (includes the `ContextPacket` of plan 25) |
| Heartbeat responses | `POST /v1/worker/heartbeat` | `{cancel_requested, deadline_at}` (runner-api) |
| Repository content | Forgejo SSH (`forgejo-ssh.forgejo.svc.cluster.local:22`) | Git |
| Harness output | harness process stdout/stderr pipes | adapter-specific line stream (plan 28) |
| Harness claim | file `/workspace/out/claim.json` written by the harness | `worker-result.schema.yaml#/$defs/HarnessClaim` |

## 4. Outputs

| Output | Consumer | Format |
|---|---|---|
| One commit on `refs/heads/agent/<task-id>` (pushing roles only) | Runner (verification), Forgejo, CI | Git; identity and trailers §14.10 |
| Worker result | Runner `POST /v1/worker/result` | `contracts/worker-result.schema.yaml` |
| Heartbeats | Runner | runner-api `postWorkerHeartbeat` |
| Pod log (stdout) | Runner log archiver → S3 `<prefix>-evidence/logs/<task>/<execution>/worker.log.gz` (plan 26 §14.5) | JSON lines from `sc-worker` + framed, scrubbed harness stream (§14.8) |
| OTel spans `sc.worker.run`, `sc.worker.clone`, `sc.worker.checkout`, `sc.worker.push`, `sc.worker.report`, `sc.harness.session`, `sc.tool.action`, `sc.test.invocation` | OTel Collector `otel-collector-workers.monitoring.svc:4318` (OTLP/HTTP; exporter env fixed by the Job builder, plan 40 §7.3) | `telemetry.schema.yaml` (plan 40) |
| Exit code | kubelet / Job status (diagnostic only) | §14.14 |

## 5. Dependencies

**Hard (DAG):**
- **F07** ← F02: `worker-input`, `worker-result`, `context`, `handoff`, `harness` schemas validated by contract tooling; the Go types are generated from them.
- **C02** ← C01 (Runner core and worker API), F07 (this binary and the fake harness image), F12 (admission and NetworkPolicy bundle that must admit the Job).

**Runtime:** Runner worker listener (`agent-runner.strange-company.svc:8082`), Forgejo SSH service, Model Gateway (`model-gateway.strange-company.svc.cluster.local:8080`) for the harness, OTel Collector (optional; export failures never affect the run), cluster DNS.

**Interfaces assumed from other plans:**
- Plan 26: worker endpoints, Job/Secret layout (§13 there), per-execution Secret key names `task-token`, `egress-token`, `gateway-token`, `deploy-key`, `known_hosts`, result ingestion rules.
- Plan 28: adapter interface (`Prepare`, `Start`, `Parse`, `Classify`) and per-harness argv/env.
- Plan 30: handoff schema `$id https://strange.company/contracts/handoff/v1alpha1` with at least the SPEC §69 fields (`what_i_did`, `what_remains`, `recommended_next_role`, `questions`, `known_risks`); additional fields are optional (assumption; if plan 30 adds a required field, `sc-worker`'s synthesized handoff in §14.9 must set it).
- Plan 36: `agent-jobs` egress allows DNS, Forgejo SSH 22, Runner 8082, Gateway 8080, OTel workers endpoint 4318 (plan 40) and the egress proxy `egress-proxy.strange-company.svc.cluster.local:3128` (package/source registries, ADR-0150), nothing else. Two tokens per execution (`contracts/task-token.schema.yaml`): the Runner token (`token_use: runner`, `aud: [sc-agent-runner-worker]`) in the `sc-worker` container, consumed by the Runner's worker listener; the egress token (`token_use: egress`, `aud: [sc-egress-proxy]`, claim `egress_allow`, plan 36 §5) in the `harness` container, consumed only by the egress proxy.
- Plan 21/29: the Runner chooses `branch_mode`, `start_commit` and `fetch_ref` (§14.5); `sc-worker` never decides topology.

## 6. Owned state

None durable. `sc-worker` is stateless; everything it produces is either pushed to Git, posted to the Runner (which persists it in `sc-pg` and S3) or written to the pod log (archived by the Runner). The emptyDir contents are discarded with the pod [SPEC §4.1, §28].

## 7. External interfaces

### 7.1 Runner worker API (consumed; defined in `runner-api.openapi.yaml`)

| Call | When | Timeout | Retry |
|---|---|---|---|
| `GET /v1/worker/packet` | once at start | 10 s per attempt | connect error, 429, 5xx: backoff 1, 2, 4, 8, 16, 30, 30, 30 s (≤ 120 s total). 401/403/404/409: no retry → exit 2 |
| `POST /v1/worker/heartbeat` | every `limits.heartbeat_interval_seconds` (30 s) from packet receipt until exit | 5 s | none (next tick); failures are logged, never fatal |
| `POST /v1/worker/result` | once at the end (identical replays allowed: the Runner keeps accepting the runner token on this endpoint only until ingestion completes, plan 26 §14.4 step 4) | 15 s per attempt | connect error, 429, 5xx: backoff 1, 2, 4, 8, 16, 30 s then every 30 s, for at most `min(600 s, deadline_at − now − 30 s)` (review RA-13). 202/200 → exit 0. 409/422 → exit 4 (no retry). The result is also written framed to stdout first (§14.12), so the Runner can recover it from the archived log |

All calls carry `Authorization: Bearer <task-token>`, `traceparent` from the packet (if present), `User-Agent: sc-worker/<version>`.

### 7.2 Harness-facing interface (provided by `sc-worker` to every harness)

| Item | Path / variable | Mode | Contract |
|---|---|---|---|
| Working tree (cwd) | `/workspace/repo` | rw | Git checkout at `start_commit` on `agent/<task-id>` (or detached for `readonly`) |
| Context directory | `/workspace/context` | ro (0444 files, 0555 dir) | `INDEX.md`, one file per context section, `task.json`; never inside the working tree |
| Output directory | `/workspace/out` | rw | harness writes `claim.json` (required) and optionally `notes/*.md` |
| Prompt | adapter argument (≤ 4 KiB) | — | fixed template: "Read /workspace/context/INDEX.md and follow it. Write your claim to /workspace/out/claim.json." plus the role name |
| Gateway base URL(s) | env named by `HarnessRegistry.gateway.baseUrlEnv[family]` | — | value from `packet.gateway.base_urls[family]` |
| Gateway token | env `HarnessRegistry.gateway.tokenDelivery.envVar`, or 0600 file `$HOME/<configPath>` | — | value of `/var/run/sc/secrets/gateway-token` |
| stdin | `/dev/null` | — | always closed (L-2) |
| Credentials visible to the harness | `/var/run/sc/harness-secrets/{gateway-token,egress-token}` | ro | no deploy key, no Runner task token |

The harness claim (`claim.json`) is `worker-result.schema.yaml#/$defs/HarnessClaim`: `{state, summary, role_output?, handoff, conflict?}`. `INDEX.md` instructs the model to write it; the role instructions (plan 25) restate the required `role_output` variant.

### 7.3 Git remote

`ssh://git@forgejo-ssh.forgejo.svc.cluster.local:22/<owner>/<repo>.git` only (`worker-input` pattern). `sc-worker` refuses `https://`, `file://`, `ext::` and any host other than the one in `known_hosts`.

### 7.4 Shim protocol (`sc-worker` container ↔ `harness` container)

Directory `/workspace/.sc-shim/` (created by `sc-worker`, mode 0770). All files are JSON, written atomically (write `*.tmp`, rename).

| File | Writer | Meaning |
|---|---|---|
| `req-<n>.json` | sc-worker | run request: `{id, kind: harness|validation, argv, env (values may reference `@file:gateway-token`, `@file:egress-token`, resolved by the shim from its mount), cwd, timeout_seconds, term_grace_seconds}` |
| `stream-<n>.log` | shim | the process's stdout and stderr lines, stderr prefixed `[stderr] `, appended as produced (never written to the shim's own stdout) |
| `res-<n>.json` | shim | `{id, exit_code, signal, started_at, finished_at, timed_out}` |
| `cancel-<n>` | sc-worker | terminate request: SIGTERM to the process group, SIGKILL after `term_grace_seconds` |
| `done` | sc-worker | the shim exits 0 (also on `abort`); the shim exits by itself if `sc-worker`'s heartbeat file `alive` is older than 120 s |

The shim enforces the timeout itself (`Setpgid`, SIGTERM/SIGKILL to the group) and forwards a container SIGTERM to the running process group. Everything the shim reports is untrusted (the harness shares its container); `sc-worker` treats exit codes and streams exactly as before: as claims to be judged, never as authority.

## 8. Internal interfaces

`sc-worker` internal packages (logical; Go package layout is implementation detail):

| Component | Responsibility |
|---|---|
| `bootstrap` | env/secret-file checks, workspace layout, signal handlers, PID-1 child reaping |
| `shim` (harness container) | §7.4 request execution, timeouts, process-group signalling, stream files; holds no Runner or Forgejo credential |
| `runnerclient` | packet, heartbeat, result calls (§7.1) |
| `gitops` | clone, fetch, checkout, topology, internals snapshot/restore, stage, commit, push, verify (absolute `/usr/bin/git`) |
| `contextfs` | context digest verification and materialization (§14.6) |
| `adapter` | the plan-28 interface: `Prepare(packet) → (argv, env, files)`, `Start`, `Parse(line) → events`, `Classify(exit, events) → HarnessVerdict` |
| `framer` | BEGIN/END markers, line truncation, log budget, scrubbing on the way to stdout |
| `scrub` | shared Go package `src/scrub` (also used by the Runner, plan 26 §14.4 step 7) |
| `precheck` | advisory mirror of Runner diff rules (§14.10) |
| `validate` | local validation command runner (§14.9) |
| `result` | result assembly, size fitting, schema validation (embedded schemas) |
| `telemetry` | OTel spans with the attribute allowlist of plan 40 |

The fake harness `sc-fake-harness` (plan 28) is a separate binary invoked through adapter `fake`.

## 9. Configuration

`sc-worker` has no configuration file. All behavior comes from the packet (policy-derived by the Runner) or compiled constants:

| Setting | Source | Default / value |
|---|---|---|
| harness timeout | `packet.limits.harness_timeout_seconds` | profile.deadline − harnessReserve |
| SIGTERM → SIGKILL grace for the harness | `packet.limits.term_grace_seconds` | 30 (on the kubelet-SIGTERM path clamped to 20, §14.12) |
| heartbeat interval | `packet.limits.heartbeat_interval_seconds` | 30 |
| max result bytes | `packet.limits.max_result_bytes` | 262144 |
| max handoff bytes | `packet.limits.max_handoff_bytes` | 32768 |
| max log bytes (harness stream to stdout) | `packet.limits.max_log_bytes` | 10485760 |
| clone timeout | constant | 300 s |
| push timeout | constant | 120 s |
| per-line log cap | constant | 65536 bytes |
| partial-diff evidence cap | constant | 1048576 bytes |
| report reserve (time kept free before the deadline for commit, push, report) | constant | 60 s |
| validation commands | `packet.policy.validation_commands` (from base-revision `validation.yaml`, plan 22) | as given |

Changing a compiled constant is a code change through the normal PR flow.

## 10. Secrets

| Class | File | Used by | Never |
|---|---|---|---|
| `SC-WORKER-TASK-TOKEN` | `/var/run/sc/secrets/task-token` | `runnerclient` only | passed to the harness, logged, put in the result |
| `SC-GATEWAY-TOKEN` (worker instance) | `/var/run/sc/secrets/gateway-token` | harness child env / config file (27-R-2) | in pod env, logs, result, commits |
| `SC-WORKER-DEPLOY-KEY` | `/var/run/sc/secrets/deploy-key` | `gitops`: copied once to `/var/sc/private/home/.ssh/id_ed25519` (0600, in the sc-worker container's memory-backed private volume) because OpenSSH rejects group-readable keys owned by the current user (verify at F07); deleted at exit | given to the harness environment |
| Forgejo host key (public) | `/var/run/sc/secrets/known_hosts` | `GIT_SSH_COMMAND=ssh -i /var/sc/private/home/.ssh/id_ed25519 -o IdentitiesOnly=yes -o UserKnownHostsFile=/var/run/sc/secrets/known_hosts -o StrictHostKeyChecking=yes -o BatchMode=yes` | — |

Missing or empty file handling (L-4): `task-token` missing/empty → exit 5 without any network call (nothing can be reported). Any other file missing/empty → result `failed` / `credentials_missing` naming the file (no clone attempted).

**Isolation (review TDD-14; closes the earlier same-uid residual risk).** The harness and every repository-controlled command run in the `harness` container, which does not mount the deploy key or the Runner task token, and the pod has no shared process namespace. A hostile harness therefore cannot push any ref, fetch other refs of the repository (SPEC §48), or post or forge a result. What it holds — the gateway token (bound to the execution's models and budget) and the egress token (bound to the proxy allowlist) — is exactly what its job requires. The Runner still re-derives every Git fact and revokes everything at result receipt (plan 26 §14.4), so a defect in `sc-worker` is not trusted either. Residual: the egress proxy does not check revocation (plan 26 Q8). New secret instance: the egress token is an instance of `SC-WORKER-TASK-TOKEN` (`token_use: egress`, `contracts/task-token.schema.yaml`); no new class.

## 11. Storage

Workspace layout (all `emptyDir`; sizes from the resource profile, plan 26 builder):

| Path | Volume | Size limit | Content | Lifetime |
|---|---|---|---|---|
| `/workspace/repo` | `workspace` | `profile.workspaceSize` (shared) | clone + build tree | pod |
| `/workspace/context` | `workspace` | ≤ packet `context.budget.max_bytes` + 64 KiB | materialized context (read-only files) | pod |
| `/workspace/out` | `workspace` | ≤ 2 MiB enforced by `sc-worker` (larger files ignored) | `claim.json`, harness notes | pod |
| `/workspace/.sc-shim` | `workspace` | ≤ 64 MiB (stream files rotated by sc-worker after framing) | shim protocol files (§7.4) | pod |
| `/var/sc/private` | `private` (sc-worker only, `medium: Memory`, 64 Mi) | 64 Mi | `sc-worker` private: SSH key copy, gitconfig, snapshot of `.git/config`/`.git/info/*`, commit message file, partial diff | pod |
| `/home/worker` | `home` | 512 Mi | `$HOME`: harness config files, `.ssh/`, caches | pod |
| `/tmp` | `tmp` | 1 Gi | `TMPDIR` | pod |
| `/var/run/sc/secrets` | per-execution Secret | — | credentials (read-only) | pod |

The root filesystem is read-only. Nothing is written to a PVC [SPEC §27, §28].

## 12. Resource requirements

`sc-worker` itself: < 50 MiB RSS and < 0.1 CPU outside of git operations; it runs inside the execution's profile limits (resource-profile contract). The harness and local validation consume the rest. No separate requests exist: the Job's container requests/limits are the profile's (memory request = limit, plan 26).

## 13. Security boundary

### 13.1 Trust

`sc-worker` is trusted code running in an **untrusted pod**: repository content, the model, the harness CLI and validation commands are all treated as hostile (L-2). `sc-worker`'s guarantees are therefore *advisory* for enforcement purposes; its real security contributions are (a) keeping the deploy key and Runner task token in a container the harness cannot see (two-container pod, §10), (b) scrubbing, (c) neutralizing repository-controlled code paths in its own Git operations, (d) deterministic framing that lets the Runner detect kills.

### 13.2 Pod-level boundary (built by plan 26, enforced by plan 37/36)

uid/gid 65532, `runAsNonRoot`, `allowPrivilegeEscalation: false`, all capabilities dropped, `RuntimeDefault` seccomp, read-only root FS, no SA token, no hostPath, `enableServiceLinks: false`, PSA restricted, VAP image allowlist. Egress only to DNS, Forgejo SSH, Runner 8082, Gateway 8080, OTel workers endpoint 4318 and the egress proxy 3128 (plan 36).

### 13.3 Git hardening (27-R-7)

All `sc-worker` git invocations run with:

- environment `GIT_CONFIG_NOSYSTEM=1`, `GIT_CONFIG_GLOBAL=/var/sc/private/gitconfig` (written by `sc-worker`), `GIT_TERMINAL_PROMPT=0`, `GIT_LFS_SKIP_SMUDGE=1`, `GIT_SSH_COMMAND` as in §10, `GIT_ALLOW_PROTOCOL=ssh`;
- flags `-c core.hooksPath=/dev/null -c core.fsmonitor=false -c protocol.allow=never -c protocol.ssh.allow=always -c submodule.recurse=false -c commit.gpgsign=false -c tag.gpgsign=false -c core.symlinks=true`.

Immediately after checkout `sc-worker` snapshots `.git/config`, `.git/info/attributes`, `.git/info/exclude` and lists `.git/hooks/`, `.git/objects/info/alternates` into `/var/sc/private/` (not in the shared workspace). Before any post-harness git command it **restores** those files from the snapshot, deletes any new file in `.git/hooks/`, deletes `.git/objects/info/alternates` if it did not exist, and records event `git_internals_restored{files}` when anything differed. A filter driver referenced by `.gitattributes` therefore has no configured command (git ignores an unconfigured driver; a `filter.<x>.required` driver can only be configured in the restored config, so it cannot exist).

### 13.4 Scrubbing (27-R-3)

Shared package `src/scrub` (also used by the Runner). Exact-match set, loaded at start: the full contents of `task-token`, `gateway-token`, `deploy-key` (the whole PEM block and every base64 body line ≥ 20 characters), plus every value `sc-worker` placed into the harness environment from a secret. Pattern set (plan 34 owns the authoritative list; minimum here): PEM private-key blocks (also with JSON-escaped `\n`), `sk-ant-[A-Za-z0-9_-]{20,}`, `sk-(proj-)?[A-Za-z0-9_-]{20,}`, JWS/JWT `eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`, `gh[pousr]_[A-Za-z0-9]{36,}`, `github_pat_[A-Za-z0-9_]{40,}`, `AKIA[0-9A-Z]{16}`, `xox[abprs]-[A-Za-z0-9-]{10,}`. Replacement: `[REDACTED:<kind>]`. The scrubber is applied to: every harness line before it is written to stdout; every string field of the result; validation output tails; the partial diff; `sc-worker`'s own log fields. The total count is reported as `scrub.redactions` in the result and `redactions` in each heartbeat. A non-zero count makes the Runner raise `SECRET_DETECTED` (plan 26 §14.4 step 7 consumes `scrub.redactions > 0` as a violation; interface for plan 26).

### 13.5 Forbidden actions and where each is enforced

| Forbidden action | First line (`sc-worker` / harness config) | Enforcement (independent) |
|---|---|---|
| push any ref other than `agent/<task-id>` | the `harness` container holds no deploy key; `sc-worker` pushes one explicit refspec | Runner ref audit `REF_VIOLATION` (ADR-0007), branch protection for protected branches |
| harness commits | deny-list; `sc-worker` folds commits (§14.10) | Runner `MULTIPLE_COMMITS` / `NOT_DESCENDANT_OF_START` |
| modify protected paths / tamper acceptance tests | `sc-worker` pre-check | Runner `PROTECTED_PATH_VIOLATION`, `TEST_TAMPER`; plan 22 CI checks |
| tester changes non-test paths | pre-check | Runner `ROLE_DIFF_VIOLATION` |
| leak credentials into output | scrubber | Runner secret scan `SECRET_DETECTED`, credential revocation |
| call provider APIs directly | no provider key exists in the pod | NetworkPolicy (plan 36), Gateway is the only egress |
| call Forgejo/K8s API | no API token, no SA token | NetworkPolicy, RBAC |
| exceed time | harness timeout, SIGTERM/SIGKILL of the process group | pod `activeDeadlineSeconds`, Job deadline |
| exceed resources | — | profile limits, LimitRange, ResourceQuota |
| run repository hooks/filters in `sc-worker`'s git | §13.3 | — |
| background processes outliving the harness | harness started in its own process group; the group is killed at harness end | container exit kills everything |
| forge a result | the `harness` container holds no Runner task token; single-shot result | Runner Git re-derivation |

## 14. Happy path

Example: execution `sc-184-x1`, role tester, harness `claude`.

### 14.1 Start (phase `starting`)

1. Install handlers for SIGTERM/SIGINT; set itself as child subreaper (it is PID 1; it reaps zombies).
2. Check env vars (§3). Any missing → exit 2 (logged `env_missing`).
3. Check secret files (§10). `task-token` missing/empty → exit 5.
4. Create `/workspace/{repo,context,out,.sc-shim}` and `/var/sc/private/{home,tmp}`; `chmod 0700 /var/sc/private`.
5. Start span `sc.worker.run` (parent from packet `execution.traceparent` once known; until then buffered).

### 14.2 Packet (phase `starting`)

1. `GET /v1/worker/packet` (§7.1). The first successful call moves the execution `STARTING → RUNNING` in the Runner.
2. Validate against the embedded `worker-input` schema (`schema_version: v1alpha1`). Check `execution.execution_id == SC_EXECUTION_ID` and `task.task_id == SC_TASK_ID`. Mismatch or schema failure → post `failed/packet_invalid` if the packet carried a parseable `execution` and `git` block, else exit 2.
3. Check the clock: if `now ≥ deadline_at − 120 s` → post `failed/harness_timeout` (detail `no_time_left`) and exit 0.
4. Start the heartbeat loop (phase updates per step: `cloning`, `materializing`, `harness`, `validating`, `committing`, `pushing`, `reporting`).
5. Remaining credential files checked; missing → `failed/credentials_missing`.

### 14.3 Credentials

Copy `deploy-key` to `/var/sc/private/home/.ssh/id_ed25519` (0600, sc-worker container only); write `/var/sc/private/gitconfig` (`user.name`, `user.email` from `git.commit`, `init.defaultBranch=main`, `advice.*=false`, `safe.directory=/workspace/repo`). Load the scrubber exact-match set. Nothing else is derived from secrets.

### 14.4 Clone (phase `cloning`, span `sc.worker.clone`)

```text
git init /workspace/repo
git -C /workspace/repo remote add origin <git.clone_url>
git -C /workspace/repo fetch --no-tags --prune origin +<fetch_ref>:refs/remotes/origin/<fetch_ref-without-refs/heads/>
```

Only `git.fetch_ref` is fetched (worker-input): the base branch for `create`/`readonly` at base, `refs/heads/agent/<task-id>` for `continue` and for reviewers. No tags, no other branches: this keeps archived implementations (`sc-archive/*`, plan 21) and other tasks' branches out of the tester's clone [SPEC §48]. Timeout 300 s; one retry after 5 s on transport errors; failure → `failed/clone_failed` (detail: scrubbed git stderr tail, ≤ 1 KiB). A host-key mismatch is never retried (detail `host_key_mismatch`).

### 14.5 Checkout and branch topology (span `sc.worker.checkout`)

1. `git cat-file -e <start_commit>^{commit}` and `git cat-file -e <base_commit>^{commit}`; then `git merge-base --is-ancestor <base_commit> <start_commit>`. Any failure → `failed/checkout_mismatch`.
2. Branch mode (decided by the Runner; `sc-worker` only verifies):

| `branch_mode` | Remote precondition (`git ls-remote origin refs/heads/agent/<task-id>`) | Local action | Push |
|---|---|---|---|
| `create` | must be absent → else `failed/branch_exists` | `git switch -c agent/<task-id> <start_commit>` (start = base) | `--force-with-lease=refs/heads/agent/<task-id>:` (lease: must not exist) |
| `continue` | must equal `start_commit` → else `failed/branch_moved` | `git switch -c agent/<task-id> <start_commit>` | `--force-with-lease=refs/heads/agent/<task-id>:<start_commit>` |
| `readonly` | not checked | `git switch --detach <start_commit>` | none |

3. Verify `git rev-parse HEAD == start_commit`. Snapshot git internals (§13.3).

Runner topology rules (authoritative: plan 21 §14.9; `sc-worker` merely executes them):

| Role / situation | `branch_mode` | `start_commit` | `fetch_ref` |
|---|---|---|---|
| first tester of generation 1 | `create` | `base_commit` | base branch |
| tester retry after `TEST_NEEDS_WORK` (no valid RED yet in the generation) or after `/sc strengthen-tests` from ALREADY_SATISFIED/NEEDS_INVESTIGATION | `continue` | current branch head (the previous tester commit) | `refs/heads/agent/<task-id>` |
| tester strengthening after a valid RED, before any implementer dispatch: **new generation `strengthened`** (the Runner tags the head `sc-archive/<task>/g<g>`, which equals red(g)) | `continue` | red(g) | `refs/heads/agent/<task-id>` |
| tester of generation g+1 after an **approved** TEST_SPEC_CONFLICT: the Runner first tags the head `sc-archive/<task>/g<g>`, deletes `agent/<task-id>` and recreates it at red(g) | `continue` | red(g), the previous red commit | `refs/heads/agent/<task-id>` |
| tester of generation g+1 after `respec` or exemption denial (Runner archives and deletes the branch) | `create` | `base_commit` | base branch |
| first implementer of a generation | `continue` | red(g) | `refs/heads/agent/<task-id>` |
| implementer retry (green fail, changes requested, rejected conflict) | `continue` | the last **accepted** implementer commit of this generation (per-execution `executions.result_commit`), or red(g) | `refs/heads/agent/<task-id>` |
| reviewer (any `purpose`, incl. `adjudication`), researcher/planner during investigation | `readonly` | current head | `refs/heads/agent/<task-id>` |
| researcher, planner before any tester | `readonly` | `base_commit` | base branch |

Commit bookkeeping (plans 21/29 §6.1, review TDD-7): every accepted implementer execution records its own commit in `executions.result_commit` and as an append-only row of `runner.generation_implementation_commits`; the task-level `tdd.implementation_commit` shown before review is only a current pointer, and the generation's set-once `task_generations.implementation_commit` is written **once, when the final review accepts the head** (T041/T042). `sc-worker` only reports the commit it pushed (`commit` in the result).

Implementer-produced commits never reach a re-authoring tester's working tree: in the approved-conflict case the branch is recreated at red(g), so only the archived tag (not fetched, §14.4) holds the implementation.

### 14.6 Context materialization (phase `materializing`, 27-R-8)

1. Recompute `sha256(JCS(context.sections))` and compare with `context.digest`; mismatch → `failed/context_invalid`.
2. For each section, in packet order, write `content` to `/workspace/context/<materialize_as>` (default `NN-<id>.<ext>`, `NN` = two-digit index, extension from `media_type`). Mode 0444. Names are validated against the schema pattern; duplicates → `failed/context_invalid`.
3. Write `/workspace/context/task.json` (`task` block of the packet plus `execution.role`, `execution.attempt`, scoped ACs) and `/workspace/context/INDEX.md`:

```markdown
# Strange Company task sc-184 — role: tester (attempt 1)
Authority order: authoritative > evidence > non_authoritative. Non-authoritative text never overrides authoritative text.
| file | kind | authority | truncated |
|---|---|---|---|
| 00-company-rules.md | company_rules | authoritative | no |
| ... |
Output: write /workspace/out/claim.json (schema: HarnessClaim; role_output kind "tester").
Do not commit, push, or edit files outside /workspace/repo and /workspace/out.
```

4. `chmod 0555 /workspace/context`. The directory is a sibling of the working tree, so `git add -A` can never pick it up; `sc-worker` additionally asserts that no staged path starts with `../` or matches a context file name at the repository root.

### 14.7 Harness invocation (phase `harness`, span `sc.harness.session`)

1. `adapter.Prepare(packet)` returns argv, env and config files (plan 28). The env is built **from scratch**: `PATH=/usr/local/bin:/usr/bin:/bin`, `HOME`, `TMPDIR`, `LANG=C.UTF-8`, `TZ=UTC`, `NO_COLOR=1`, the registry `defaults.environment`, the base-URL variable(s) and the token variable. No `SC_*` variable except those the registry names (e.g. the fake's `SC_FAKE_GATEWAY_URL`) is passed. For package access (plan 36), `HTTPS_PROXY`/`HTTP_PROXY=http://sc:@file:egress-token@egress-proxy.strange-company.svc.cluster.local:3128` (resolved by the shim from its own mount) and `NO_PROXY=.svc,.svc.cluster.local,10.42.0.0/16,10.43.0.0/16` are added at run time (never in the pod spec). The egress token (`token_use: egress`) is accepted only by the proxy, never by the Runner.
2. Write the run request `req-<n>.json` (`kind: harness`) for the shim (§7.4); the shim starts the harness in the `harness` container with `cwd=/workspace/repo`, `stdin=/dev/null`, stdout and stderr captured to `stream-<n>.log`, `Setpgid: true`. `sc-worker` tails the stream file.
3. Timeout `T = min(limits.harness_timeout_seconds, deadline_at − now − 60 s)`, enforced by the shim (and backstopped by `sc-worker` writing `cancel-<n>`). On expiry: `SIGTERM` to the process group, wait `limits.term_grace_seconds`, `SIGKILL` to the group; verdict `harness_timeout`.
4. Cancellation: a heartbeat response with `cancel_requested: true` makes `sc-worker` write `cancel-<n>`; the shim runs the same termination sequence; verdict `cancelled`.
5. Output is consumed line by line by the framer (§14.8) and by `adapter.Parse` (events: terminal result, tool use, permission denials, usage, gateway error codes).
6. When the harness exits, `adapter.Classify(exit_code, signal, events)` yields a `HarnessVerdict`: `ok`, `harness_error`, `harness_timeout`, `harness_stream_incomplete` (no terminal event), `malformed_harness_output`, `budget_denied`, `emergency_stop`, `model_unavailable`, `cancelled`. Gateway denial codes seen in the stream (`SC_BUDGET_EXHAUSTED`, `SC_BUDGET_UNDEFINED`, `SC_REQUEST_COST_CAP`, `SC_PRICE_UNKNOWN` → `budget_denied`; `SC_EMERGENCY_STOP`, `SC_TOKEN_REVOKED` → `emergency_stop`; `SC_UPSTREAM_UNREACHABLE`, `SC_UPSTREAM_TIMEOUT`, `SC_LEDGER_UNAVAILABLE`, `SC_PROVIDER_UNAVAILABLE` → `model_unavailable`) override `harness_error`. The Runner re-classifies from `ledger.denials` regardless (plan 26/53).

### 14.8 Framing and log budget (27-R-5)

```text
{"ts":"…","level":"info","src":"sc-worker","event":"harness_start","execution_id":"sc-184-x1","adapter":"claude-code","cli_version":"2.1.290"}
::STRANGE-COMPANY-STREAM-BEGIN:: {"execution_id":"sc-184-x1","adapter":"claude-code","started_at":"…"}
<harness stdout/stderr lines, scrubbed; stderr lines prefixed "[stderr] ">
::STRANGE-COMPANY-STREAM-END:: {"exit_code":0,"signal":null,"lines":1832,"bytes":2291004,"truncated_lines":3,"log_truncated":false}
{"ts":"…","level":"info","src":"sc-worker","event":"harness_exit","verdict":"ok","duration_ms":570000}
```

- Each line is parsed before truncation, then truncated to 64 KiB with suffix ` …[truncated N bytes]`.
- After `max_log_bytes` of harness output, further harness lines are parsed but not written; one line `{"src":"sc-worker","event":"log_budget_exhausted"}` is emitted and `log_truncated: true` is set in END.
- END is written on every path where `sc-worker` survives the harness (normal exit, timeout, cancel, SIGTERM path). Only a `sc-worker` SIGKILL (pod deadline grace exhausted, OOM, node loss) leaves it missing; the Runner records `killed_mid_run` (plan 26 §14.5).
- `sc-worker`'s own log lines are JSON with `src: sc-worker` and never contain content of prompts, completions, files or env values.

### 14.9 Claim, handoff and local validation (phase `validating`)

1. Read `/workspace/out/claim.json` (≤ 512 KiB; larger → treated as missing). Validate against `HarnessClaim`, then check:
   - `state` ∈ role `expectedOutput.allowedResultStates` (the packet carries the role; the Runner also re-checks);
   - `role_output.kind` equals the role's `expectedOutput.roleOutput` mapping (tester→`tester`, implementer→`implementer`, reviewer→`reviewer`, researcher→`research_report`, planner→`plan`);
   - `handoff` validates against the plan-30 schema and is ≤ `max_handoff_bytes` (canonical JSON).
   Invalid or missing claim with verdict `ok` → `failed/malformed_harness_output` with detail prefix `claim:` (the Runner maps this to failure class **merit**, code `OUTPUT_CONTRACT_VIOLATED`: the model did not follow the output contract). A stream without a terminal event → detail prefix `stream:` (class infrastructure).
2. If the claim is unusable or the verdict is not `ok`, `sc-worker` synthesizes the handoff: `what_i_did: "sc-worker: harness ended with <verdict>; no valid claim"`, `what_remains: "<role> work for <task-id> is incomplete"`, `recommended_next_role: null`, `questions: []`, `known_risks: ["<verdict>"]`.
3. Local validation runs only for pushing roles and only when the claim state is `completed` or `already_satisfied`: each `packet.policy.validation_commands[i]` is sent to the shim as `req-<n>.json` (`kind: validation`) and executed **in the harness container** (repository code never runs next to the credentials) without a shell (`argv`), cwd `/workspace/repo`, env = harness env minus the gateway token variable(s), stdin `/dev/null`, timeout `min(timeout_seconds, deadline_at − now − 90 s)`; if less than 30 s remain the command is skipped and recorded `timed_out: true, exit_code: -1`. Output tail (last 8 KiB, scrubbed) goes into `validation[i].output_tail`; span `sc.test.invocation` per command. Validation is advisory and never blocks the commit (a tester's validation is *expected* to fail) [SPEC §36].

### 14.10 Pre-checks and commit (phase `committing`)

Applies to pushing roles (`git.push_allowed: true`) when the claim state is `completed`, or `already_satisfied` (tester: tests are still committed and CI decides, SPEC §51). States `blocked`, `failed`, `test_spec_conflict` never commit.

1. Restore git internals (§13.3).
2. Fold harness commits: if `HEAD ≠ start_commit`: if `start_commit` is an ancestor of `HEAD` → `git reset --soft <start_commit>` (event `harness_commits_folded{count}`); otherwise → `failed/diff_precheck_failed`, detail `head_not_descendant`.
3. `git add -A` (pathspec `:/`), then compute `git diff --cached --name-status -M -z <start_commit>` and `--numstat`, plus total patch bytes.
4. Pre-checks (advisory mirror of plan 26 §14.4 step 6; the patterns come from the packet):
   - no changed path matches `policy.protected_paths` (plan 22 default set; see §25 Q2 for nested instruction files);
   - `policy.diff_mode`: `allowlist` → every path matches `diff_allow` and none `diff_deny`; `denylist` → none matches `diff_deny`; `none` → no changes at all;
   - deletions only if `allow_deletions`;
   - files changed ≤ `max_files_changed`, patch bytes ≤ `max_diff_bytes`;
   - secret scan of added lines (§13.4 sets) → on hit: `failed/secret_detected` (no push; the Runner also revokes credentials);
   - no change at all while the role's `commitRequiredOnCompleted` is true (tester, implementer) → `failed/diff_precheck_failed`, detail `no_changes` (Runner: class merit, code `NO_CHANGES`).
   Any other pre-check failure → `failed/diff_precheck_failed` with the rule name and up to 20 offending paths in `detail`.
5. Commit:
   - author = committer = `<git.commit.author_name> <<git.commit.author_email>>` (e.g. `Strange Company tester (sc-184-x1) <sc-184-x1@agents.strange-company.invalid>`); dates = now (UTC);
   - message file `/var/sc/private/COMMIT_MSG`:

```text
test(sc-184): <first line of claim.summary, control characters removed, ≤ 72 chars total>

<claim.summary, wrapped at 72, ≤ 2000 chars>

Acceptance criteria: AC-1, AC-2

SC-Task: sc-184
SC-Execution: sc-184-x1
SC-Role: tester
SC-Base: 0123456789abcdef0123456789abcdef01234567
```

   - prefix from `git.commit.subject_prefix` (Runner rule: tester `test(<task>): `, implementer `feat(<task>): ` — or `fix(<task>): ` when the task source labels it a bug, plan 55); trailers exactly `SC-Task`, `SC-Execution`, `SC-Role`, `SC-Base` from `git.commit.trailers`;
   - `git commit --no-verify --cleanup=verbatim -F /var/sc/private/COMMIT_MSG`; failure → `failed/commit_failed`.
6. Verify: `git rev-list --count <start_commit>..HEAD == 1` and `git rev-parse HEAD^ == start_commit`.

### 14.11 Push (phase `pushing`, span `sc.worker.push`)

`git push --porcelain --no-verify origin --force-with-lease=<lease per §14.5> HEAD:refs/heads/agent/<task-id>`, timeout 120 s. Transport errors (connection reset, timeout): retry after 2, 4, 8 s (3 retries). Lease or non-fast-forward rejection: no retry → `failed/push_rejected` (the Runner's ref audit then explains who moved the branch). After success, `git ls-remote origin refs/heads/agent/<task-id>` must equal `HEAD`; otherwise `failed/push_rejected` detail `remote_head_mismatch`. On success `pushed: true`, `commit: <sha>`.

### 14.12 Report (phase `reporting`, span `sc.worker.report`)

1. Assemble `WorkerResult` (`schema_version`, identity fields copied from the packet, `state` from the claim or `failed`, `branch` = `agent/<task-id>` for pushing roles else `null`, `base_commit`, `start_commit`, `commit`, `pushed`, `summary`, `failure`, `conflict`, `role_output`, `validation`, `harness_report` (adapter, cli_version, exit_code, signal, `stream_complete` = END written, `terminal_event_seen`, `is_error`, `num_turns`, `permission_denials`, `self_reported_usage`), `handoff`, `timings`, `scrub.redactions`).
2. Scrub every string field.
3. Size fitting to `max_result_bytes` (canonical JSON), deterministic order: (a) drop `validation[*].output_tail` from the last command backwards; (b) truncate `harness_report.permission_denials` to 20; (c) truncate `role_output` long text fields (`report_markdown`, finding texts) to fit, appending `[truncated by sc-worker]`; (d) truncate `summary` to 1000 chars. If still too large → replace with a minimal `failed/internal` result (detail `result_too_large`).
4. Validate against the embedded `worker-result` schema (which `$ref`s handoff). A validation failure here is an `sc-worker` bug → post a minimal `failed/internal` result instead and log `result_self_invalid`.
5. Write the final result to stdout framed as `::SC-RESULT-BEGIN:: {"execution_id":"…","bytes":<n>,"sha256":"<64 hex>"}`, the canonical JSON on one line, `::SC-RESULT-END::` (it is already scrubbed and size-bounded), so the Runner can ingest it from the archived log if the POST never lands (plan 26 §14.5; review RA-13).
6. `POST /v1/worker/result` (§7.1). 202 or 200 → write `done` for the shim, exit 0.

### 14.13 Partial progress

`sc-worker` never pushes partial work: a non-`completed` outcome leaves the remote branch where it was (SPEC §52 ordering stays provable). To keep the evidence (SPEC §4.8), when the working tree differs from `start_commit` and the outcome is not pushed, `sc-worker` writes the scrubbed binary-free patch (`git diff --no-ext-diff --no-textconv <start_commit>`, ≤ 1 MiB, truncated with a marker) to the pod log between `::SC-PARTIAL-DIFF-BEGIN::` / `::SC-PARTIAL-DIFF-END::` and adds its diffstat (paths and line counts) to `failure.detail`. The Runner archives it with the log; plan 25 exposes the diffstat (not the patch) in the next attempt's `prior_attempt_evidence`.

### 14.14 Termination, signals and exit codes

**SIGTERM to `sc-worker`** (pod `activeDeadlineSeconds` reached, or the Runner deleting the Job): forward SIGTERM to the harness group, wait `min(term_grace_seconds, 20 s)`, SIGKILL the group, write END, post `failed` (`harness_timeout` if `now ≥ deadline_at − 5 s`, else `cancelled`) with a single 5 s attempt, exit 143. The Job's `terminationGracePeriodSeconds: 30` (plan 26) covers 20 + 5 s.

| Exit | Meaning | Result posted? |
|---|---|---|
| 0 | result accepted by the Runner (any worker `state`) | yes |
| 2 | env missing, packet unobtainable or unusable | no |
| 3 | result post failed after the retry window (Runner unreachable; the framed result in the log remains recoverable) | no (attempted) |
| 4 | Runner answered 409 (another result already stored) or 422 (schema-invalid) | stored result is someone else's / rejected |
| 5 | `task-token` missing or empty | no |
| 70 | internal panic (recovered at top level; a best-effort `failed/internal` post is attempted first) | maybe |
| 143 | terminated by SIGTERM (after the best-effort post) | maybe |

The Runner never depends on the exit code; it depends on the stored result, the pod status and the END marker (plan 26 §15).

### 14.15 Result `failure.reason` → Runner outcome (interface for plans 26 and 29)

| `failure.reason` (detail) | Execution state | Failure class | Failure code |
|---|---|---|---|
| `packet_invalid`, `credentials_missing`, `clone_failed`, `checkout_mismatch`, `context_invalid`, `harness_start_failed`, `commit_failed`, `internal` | FAILED | infrastructure | upper-cased reason (e.g. `CLONE_FAILED`) |
| `branch_exists`, `branch_moved`, `push_rejected` | FAILED | infrastructure (the Runner's ref audit may upgrade to policy `REF_VIOLATION`) | `BRANCH_EXISTS`, `BRANCH_MOVED`, `PUSH_REJECTED` |
| `harness_error` (default) | FAILED | infrastructure | `HARNESS_ERROR` |
| `harness_error` (detail `max_turns:` or `claim_failed:`; the model reported it could not finish) | FAILED | merit | `HARNESS_GAVE_UP` |
| `harness_timeout` | TIMED_OUT | infrastructure | `HARNESS_TIMEOUT` |
| `harness_stream_incomplete`, `malformed_harness_output` (`stream:`) | FAILED | infrastructure | `STREAM_INCOMPLETE`, `MALFORMED_HARNESS_OUTPUT` |
| `malformed_harness_output` (`claim:`) | FAILED | merit | `OUTPUT_CONTRACT_VIOLATED` |
| `model_unavailable` | FAILED | infrastructure | `MODEL_UNAVAILABLE` |
| `budget_denied`, `emergency_stop` | FAILED (queued executions cancelled by an e-stop end `CANCELLED`) | budget | derived by the Runner from `ledger.denials` for the execution's jti, never from this claim (plan 33 §14.5): `BUDGET_EXECUTION_EXHAUSTED`, `BUDGET_TASK_EXHAUSTED`, `BUDGET_REQUEST_CAP`, `BUDGET_UNDEFINED`, `BUDGET_SHARED_EXHAUSTED`, `EMERGENCY_STOP`; a claim without a denial row → infrastructure `BUDGET_CLAIM_UNSUPPORTED` |
| `diff_precheck_failed` (default) / (`no_changes`) | FAILED | policy / merit | `DIFF_PRECHECK_FAILED` / `NO_CHANGES` |
| `secret_detected` | FAILED | policy | `SECRET_DETECTED` (credentials revoked) |
| `cancelled` | CANCELLED | — | `CANCELLED` |

Plan 26 §14.4 step 11 already maps reasons to classes; this table refines the `harness_error`, `malformed_harness_output` and `diff_precheck_failed` details (reported for consolidation). Plan 29 owns retry consequences.

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Runner unreachable at packet fetch | connect errors for 120 s | no work done | exit 2; Runner: `RESULT_MISSING` after pod end (infra) | `agent-runner-failure.md` |
| Runner unreachable at result post | retries exhausted | work pushed, result only in the log | exit 3; on recovery the Runner ingests the framed result from the archived log (plan 26 §14.5); `RESULT_MISSING` only if the block is absent or corrupt | `agent-runner-failure.md` |
| Packet schema mismatch (version skew) | schema validation | no work | `failed/packet_invalid` (infra); alert on rate via `SCExecutionInfraFailuresHigh` | upgrade rollback (plan 26 §19) |
| Forgejo SSH down | clone timeout | no work | `failed/clone_failed` (infra) | `restore-forgejo.md` |
| Host key mismatch | ssh error | no work | `failed/clone_failed` detail `host_key_mismatch`, never retried | investigate MITM / Forgejo host key rotation (plan 16) |
| Remote branch already exists (`create`) | ls-remote | no work | `failed/branch_exists` | Runner ref audit; plan 21 generation handling |
| Remote branch moved (`continue`) | ls-remote / lease rejection | no push | `failed/branch_moved` / `push_rejected` | `quarantine-worker.md` if unexplained |
| Context digest mismatch | §14.6 | no work | `failed/context_invalid` | file a bug (compiler/transport) |
| Harness binary missing / exec error | start error | — | `failed/harness_start_failed` | image build (plan 23/28) |
| Harness hangs | timeout | — | SIGTERM/SIGKILL group, `TIMED_OUT` | — |
| Harness ignores SIGTERM | grace expiry | — | SIGKILL group | — |
| `sc-worker` itself killed (OOM, node loss, deadline grace exceeded) | no END marker, no result | — | Runner `JOB_LOST`/`RESULT_MISSING` with `killed_mid_run` (infra) | — |
| Harness emits garbage / no terminal event | adapter parse | — | `failed/harness_stream_incomplete` or `malformed_harness_output` (`stream:`) | — |
| Model ignored the output contract | claim invalid | — | `failed/malformed_harness_output` (`claim:`, merit) | retry per plan 29 with evidence |
| Gateway denial (budget/e-stop) | stream codes | — | `failed/budget_denied` / `emergency_stop` | `budget-emergency-stop.md` |
| Harness modified `.git/config`/hooks | snapshot diff | none (restored) | event `git_internals_restored` | review log if repeated |
| Harness committed or pushed itself | HEAD moved / Runner ref audit | folded commit; pushes of other refs → `REF_VIOLATION` | fold; Runner quarantine | `quarantine-worker.md` |
| Secret in output | scrubber | redacted | count reported → Runner `SECRET_DETECTED`, revocation | `quarantine-worker.md` |
| Harness tries to push, fetch other refs or post a result | no credential in the `harness` container (§10) | attempt fails | none needed; `fake-forge-then-push` proves it (plan 28) | — |
| Workspace full | ENOSPC / emptyDir eviction | — | `failed/internal` (detail `enospc`) or pod eviction → `JOB_LOST` | resize profile (plan 51) |
| Clock skew vs Runner | `deadline_at` already past at start | — | `failed/harness_timeout` `no_time_left` | node time sync (plan 08) |

## 16. Retry behavior

- Inside one execution: only transport-level retries (§7.1, §14.4, §14.11). The harness is never restarted inside the same execution, and no step is retried after a semantic failure.
- Across executions: decided by the Runner and plan 29 from the failure class (§14.15). A new attempt is a new execution, new Job, new credentials and a new packet that carries `prior_attempt_evidence` (plan 25).

## 17. Idempotency behavior

- Packet fetch is a read (the first call's STARTING→RUNNING transition is idempotent in the Runner).
- Push uses `--force-with-lease` with the expected old value, so a duplicate push of the same commit is a no-op and a push over someone else's change fails.
- Result post: identical replay → 200; a different body → 409 (exit 4). `sc-worker` never posts two different results.
- A Job is never restarted (`backoffLimit: 0`, `restartPolicy: Never`), so the lifecycle runs at most once per execution.

## 18. Bootstrap behavior

N/A at platform bootstrap — `sc-worker` exists only inside harness images and starts only when the Runner dispatches (C02 onward). In Phase 1 (F07) it is exercised by `make test` and by a local docker/podman run of the fake image against a stub Runner and a test Git server (§22).

## 19. Upgrade behavior

- `sc-worker` is versioned with the Strange Company release and baked into every harness image (plan 28 §image). A harness image digest change in `policy/harnesses.yaml` (PR) is the only way a new `sc-worker` reaches workers.
- Contract compatibility: `worker-input`/`worker-result` are `v1alpha1`. A breaking change requires the Runner to serve both packet versions for one release (plan 26 §19) and `sc-worker` to reject unknown `schema_version` with exit 2.
- In-flight executions keep their image; nothing about an upgrade affects a running Job.

## 20. Recovery behavior

Nothing to recover in the worker: state is disposable. The Runner recovers executions (plan 26 §15, §20). A lost execution is retried as a new execution under plan 29's infrastructure budget.

## 21. Observability

- **Spans** (plan 40 conventions; no content attributes): `sc.worker.run` (root of the pod, child of the Runner's `sc.execution`), `sc.worker.clone`, `sc.worker.checkout`, `sc.harness.session`, `sc.tool.action` (one per harness tool event: `sc.tool.name`, `sc.tool.status`; never arguments), `sc.test.invocation` (per validation command: `sc.test.command_id` = command name, result, exit code), `sc.worker.push`, `sc.worker.report`. Trace parent: `execution.traceparent` in the packet (added to `worker-input` by this plan).
- **Logs:** JSON lines on stdout (`src: sc-worker`), framed harness stream; archived by the Runner; never shipped to Loki (ADR-0552).
- **Metrics:** none exported by the pod (short-lived). The Runner derives `sc_runner_results_total{failure_class}`, execution durations and phase timings from heartbeats and results (plan 26 §21).
- **Alerts:** none owned; worker problems surface through the Runner's `SCExecutionInfraFailuresHigh`, `SCWorkerPolicyViolation`, `SCWorkerQuarantined`, `SCExecutionStuck`.

## 22. Testing strategy

All deterministic, no paid model calls [SPEC §104].

- **Unit (Go):** scrubber corpus (each pattern, exact-match, JSON-escaped PEM, split lines); framer (BEGIN/END presence on every path, line truncation, budget); claim validation per role; result size fitting (golden outputs); pre-check rules (fixtures per rule); topology table; git-internals restore (fixtures with injected hooks, filter driver config and alternates); exit-code table.
- **Reviewer `inspected_paths`:** the claim may list paths the reviewer inspected beyond the packet diff (`ReviewerOutput.inspected_paths`, plan 29 guard `G_REVIEW_INPUT_COMPLETE`); `sc-worker` passes it through after validating each entry as a repository path.
- **Reviewer purpose:** for `execution.purpose: adjudication` (TEST_SPEC_CONFLICT, plan 21 §14.6), the claim's `role_output` must carry `purpose: adjudication` and `conflict_decision {verdict: test_valid|test_invalid|spec_ambiguous, ac_ids, rationale, proposed_test_change (required for test_invalid)}`; `sc-worker` rejects a claim without it as `malformed_harness_output` (`claim:`), and the Runner re-checks (schema `allOf`). A unit test covers both.
- **Contract (F02):** every result produced in tests validates against `worker-result.schema.yaml`; every packet fixture against `worker-input.schema.yaml`; the embedded copies equal the repository files (checksum test).
- **Component (F07, containerized, offline):** `make test-worker-component` starts a stub Runner (Go test server implementing the three worker endpoints and recording calls), a `gitea/gitea`-compatible test Git server is NOT used; instead a local `git daemon`-free SSH server container (`linuxserver/openssh-server`-style, digest-pinned; or the Forgejo test container used by C03 — selection at F07) serving a bare fixture repository, and runs the fake harness image for every scenario of plan 28 that does not need the Runner's Git verification. Assertions: posted result equals the scenario's expected `state`/`failure.reason`; pushed refs as expected; END marker present except for the kill scenarios; zero unscrubbed canaries in captured stdout.
- **Hostile-repository tests:** fixture repo containing `.git`-irrelevant hostile content (`.gitattributes` with `filter=evil`, `CLAUDE.md`, `.claude/settings.json` with hooks, `AGENTS.md` instructing to push `main`); assert `sc-worker` git operations run no repository code (a canary file `/tmp/pwned` never appears) and the harness is started with repository-config isolation (plan 28 adapter flags).
- **e2e (C02, k3d):** plan 26 26-AC-22..27 run real Jobs with this binary.

## 23. Acceptance criteria

### GATE-F07 (with plan 28)

- [ ] **27-AC-1** `go test ./src/worker/... ./src/scrub/...` passes in the toolbox/CI image with no network access (`--network none`).
- [ ] **27-AC-2** For each of the 6 exit paths in §14.14 a component test forces the condition and observes the documented exit code (table-driven test, 7 cases including 70 via an injected panic).
- [ ] **27-AC-3** Framing: for scenarios `success`, `failure`, `timeout`, `ignore-cancel` (SIGTERM path), the captured stdout contains exactly one BEGIN and one END line; for a test that SIGKILLs `sc-worker` mid-harness, BEGIN is present and END absent.
- [ ] **27-AC-4** Scrubbing: with canary values placed in all four secret files and a fake harness that prints each canary, plus a PEM block and an `sk-ant-` string, `grep -c` of each canary in captured stdout and in the posted result body is 0, and `scrub.redactions` ≥ 6.
- [ ] **27-AC-5** Environment and mount isolation: the fake harness dumps its environment and `find /var/run /var/sc -type f` to `/workspace/out`; the dump contains the gateway token variable named by the registry and `HTTPS_PROXY` carrying the **egress** token, and contains none of: `SC_RUNNER_URL`, `SC_TASK_ID`, the Runner task-token value, the deploy-key content, `KUBERNETES_SERVICE_HOST`; the file list contains only `/var/run/sc/harness-secrets/{gateway-token,egress-token}`.
- [ ] **27-AC-16** Shim protocol: a fake harness that ignores SIGTERM is killed by the shim within `term_grace_seconds + 2 s`; a validation command runs in the `harness` container (it writes `/proc/self/cgroup` to `/workspace/out`, which differs from `sc-worker`'s); `sc-worker`'s own stdout contains no line from the harness that bypassed the framer.
- [ ] **27-AC-17** Framed result: the captured `sc-worker` stdout of every component-test scenario that posts a result contains exactly one `::SC-RESULT-BEGIN::`…`::SC-RESULT-END::` block whose sha256 matches the posted body.
- [ ] **27-AC-6** Context outside the tree: after a `success` tester run, `git -C <bare fixture> show --name-only agent/sc-1` lists no path under `context/` and no file named `INDEX.md` or `task.json`; `/workspace/context` files have mode 0444.
- [ ] **27-AC-7** Topology: `create` with an existing remote `agent/sc-1` → `failed/branch_exists`; `continue` with remote head ≠ `start_commit` → `failed/branch_moved`; `readonly` → `pushed: false`, remote refs unchanged (ref list digest equal before/after).
- [ ] **27-AC-8** Commit format: the pushed commit's subject matches `^test\(sc-1\): .{1,63}$`, `git interpret-trailers --parse` yields exactly `SC-Task`, `SC-Execution`, `SC-Role`, `SC-Base` with the packet values, author email equals `git.commit.author_email`, and `git rev-parse <commit>^` equals `start_commit`.
- [ ] **27-AC-9** Harness commits are folded: a fake harness that makes 3 local commits yields one pushed commit whose parent is `start_commit` and the log contains event `harness_commits_folded` with `count: 3`.
- [ ] **27-AC-10** Git hardening: with the hostile fixture (`.gitattributes` filter, injected `.git/hooks/pre-commit` and `post-commit` written by the fake harness, `core.fsmonitor` set in `.git/config` by the harness), the push succeeds, `/tmp/pwned` does not exist, and event `git_internals_restored` is logged.
- [ ] **27-AC-11** Timeout: with `harness_timeout_seconds: 60` and scenario `timeout`, the result is `failed/harness_timeout` posted within 60 + `term_grace_seconds` + 15 s of harness start, and the process group has no surviving process (`/proc` scan in the test).
- [ ] **27-AC-12** Pre-checks: one fixture per rule (protected path, allowlist miss, denylist hit, deletion, max files, max bytes, secret in added line, no changes) produces `failed/diff_precheck_failed` (or `secret_detected`) with the rule name in `detail` and no remote change.
- [ ] **27-AC-13** Result size fitting: a fake claim with a 400 KiB `report_markdown` produces a posted result ≤ `max_result_bytes` that validates against `worker-result.schema.yaml` and ends the text with `[truncated by sc-worker]`.
- [ ] **27-AC-14** Embedded schemas: `sha256` of the schema copies embedded in `sc-worker` equals that of `contracts/worker-input.schema.yaml`, `worker-result.schema.yaml`, `context.schema.yaml` and the plan-30 handoff schema (unit test reading the repository files).
- [ ] **27-AC-15** `stdin` is closed: a fake harness that blocks reading stdin returns EOF immediately (scenario completes in < 5 s).

### GATE-C02 (worker side; Runner side in plan 26)

- [ ] **27-AC-20** On k3d with plan-37 admission and plan-36 NetworkPolicies, a `fake-success` tester execution's pod log (archived object) contains BEGIN and END, the Runner shows `SUCCEEDED`, and `agent_result.harness_report.stream_complete` is `true`.
- [ ] **27-AC-21** From a probe pod in `agent-jobs` carrying the worker Job's labels and spec (plan 36 matrix harness): TCP to `kubernetes.default.svc:443`, `sc-pg-rw.strange-company:5432`, `forgejo-http.forgejo:3000` and an external IP on 443 all fail; `forgejo-ssh.forgejo:22`, `agent-runner.strange-company:8082`, `model-gateway.strange-company:8080` and `egress-proxy.strange-company:3128` succeed.
- [ ] **27-AC-22** `kubectl -n agent-jobs get pod <pod> -o json | jq '.spec.containers[].env[].name'` contains no name with `TOKEN`, `KEY` or `SECRET`; `.spec.containers[] | select(.name=="harness") | .volumeMounts[].mountPath` does not contain `/var/run/sc/secrets`.
- [ ] **27-AC-23** A heartbeat with `cancel_requested: true` (via `agent_cancel`) leads to a posted `failed/cancelled` result within `heartbeat_interval_seconds + term_grace_seconds + 15 s`, and the execution ends `CANCELLED`.

## 24. Explicit non-goals

- No harness-specific behavior here (plan 28 adapters).
- No decision about branch topology, retries or failure-class retry budgets (plans 21, 26, 29).
- No authoritative enforcement: pre-checks are advisory; the Runner, CI and admission enforce.
- No pushing of partial work; no multi-commit histories per execution.
- No package-registry proxying logic (plan 36 owns the egress proxy; `sc-worker` only sets the proxy variables).
- No worker-to-worker communication [SPEC §127].

## 25. Open questions

1. **Package registries for local validation and tests** — resolved by plan 36 (ADR-0150): authenticated egress proxy with per-task `egress_allow`. Trusted-CI verification still runs with the CI proxy rules of plan 36 §9.5.
2. **Nested harness instruction files** (blocking: no). Claude Code, Codex and OpenCode can load `CLAUDE.md`, `AGENTS.md`, `.claude/**`, `.codex/**`, `.opencode/**` from subdirectories. Even with repository-config isolation (plan 28), the protected-path default set (plan 22) SHOULD include `**/AGENTS.md`, `**/CLAUDE.md`, `**/.claude/**`, `**/.codex/**`, `**/.opencode/**`, so that a tester commit cannot plant instructions for the implementer. Owner: 22.
3. **OpenSSH key-file permission check on Secret volumes** (blocking: no). The copy to `$HOME/.ssh` (0600) is the conservative choice; re-verified at F07 (27-AC-8 exercises the path).
4. **`scrub.redactions > 0` as a Runner violation** (blocking: no). Requires plan 26 §14.4 step 7 to treat the worker-reported count as `SECRET_DETECTED` even when the Runner's own scan finds nothing (because `sc-worker` already redacted). Proposed: yes (consistency fix recorded in plan 26 by this author group).
