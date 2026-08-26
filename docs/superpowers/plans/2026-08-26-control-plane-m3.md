# Control Plane M3 Implementation Plan — the coding runner

> REQUIRED SUB-SKILL: superpowers:executing-plans.

**Goal:** Milestone 3 — a worker can invoke either coding harness on a
repository and collect structured results, in an isolated Kubernetes Job.

**Spec:** `docs/specs/strange-company-control-plane-v1.md` §2.2, §11, §13, §14,
§16, §20. Harness contracts: `docs/reference/coding-harness-notes.md`.

## Global Constraints

- One `CodingRunResult` regardless of harness (§2.2): the company must not care
  which CLI did the work.
- Adapters are built against **recorded real output**
  (`control-plane/internal/runner/testdata/`), never from recollection. Four of
  six M0/M1 defects came from guessing an API shape.
- **Close stdin.** Codex blocks forever on it; Claude Code stalls 3s. Verified.
- **Codex needs a git repo** or `--skip-git-repo-check`.
- Never use `--dangerously-skip-permissions` or Codex's unrestricted mode
  (§13, §14). Permissions are generated from the card's allowlist.
- Jobs: non-root, read-only root filesystem where practical, ephemeral
  workspace, no hostPath, no privileged, no cluster-admin,
  `automountServiceAccountToken: false`, CPU/memory limits and a wall-clock
  timeout (§16.1).
- Git is the persistent workspace (§16.2). No PVC per card. Branch
  `agent/<card-id>-<slug>`; each phase commits.
- The control plane owns Job creation. Hermes never gets a Kubernetes tool (§9).

## Phasing

**M3a — the harness abstraction.** Pure Go, no cluster, testable immediately
against the recorded fixtures. This is the piece that must be right.

**M3b — the Job controller and RBAC.** The chart finally grants the control
plane a narrowly scoped Role in `agent-runs`, and only then.

**M3c — the runner image and git branches.** A published image carrying git and
both CLIs; branch creation, per-phase commits, push.

---

## M3a

### Task 1: `CodingRunResult` and the harness interface

**Files:** `control-plane/internal/runner/result.go`, `runner.go`, `result_test.go`

```go
type Status string // "completed" | "failed" | "policy_violation" | "timeout" | "infra_error"

type Denial struct{ Tool, Detail string }

type Usage struct {
    InputTokens, OutputTokens, CachedInputTokens, CacheCreationTokens, ReasoningTokens int
}

type CodingRunResult struct {
    Status       Status
    Harness      string
    Model        string
    SessionID    string
    Summary      string
    ChangedFiles []string
    Usage        Usage
    CostUSD      *float64 // nil when the harness does not report cost
    Denials      []Denial
    ExitCode     int
    DurationMS   int64
    Raw          []byte
}

type Adapter interface {
    Name() string
    Command(req Request) []string          // argv, never a shell string
    Parse(stdout []byte, exitCode int, dur time.Duration) (*CodingRunResult, error)
}
```

`CostUSD` is a pointer on purpose: Claude Code reports `total_cost_usd`, Codex
reports tokens only. Making the difference visible in the type stops the cost
ledger silently recording zero for Codex.

- [ ] Tests first: a result with no cost is distinguishable from one costing
      zero; `Status` classification is total.

### Task 2: Claude Code adapter

**Files:** `control-plane/internal/runner/claudecode.go`, `claudecode_test.go`

Parses `--output-format stream-json --verbose`. The terminal `result` event
carries `is_error`, `subtype`, `stop_reason`, `session_id`, `total_cost_usd`,
`usage`, `modelUsage`, `permission_denials`, `result`.

Command generation from the card's allowlist:
`claude -p <task> --model <policy model> --output-format stream-json --verbose
--max-turns <limit> --allowedTools <generated>`, stdin closed. Never
`--dangerously-skip-permissions`.

- [ ] **Load-bearing test:** a run whose `permission_denials` is non-empty
      parses to `StatusPolicyViolation`, because §24 says an attempted forbidden
      action blocks the card rather than being retried harder.
- [ ] Parse the recorded `testdata/claude-code-success.jsonl` and assert the
      whole result: status, session id, cost, tokens, summary.
- [ ] A truncated/malformed stream is an error, not a silent success.
- [ ] A non-zero exit with no terminal event is `StatusInfraError`, not
      `StatusFailed` — §12.1 says infrastructure failures must not burn a
      model-tier attempt.

### Task 3: Codex adapter

**Files:** `control-plane/internal/runner/codex.go`, `codex_test.go`

Parses `thread.started` / `turn.started` / `item.completed` / `turn.completed`.
`turn.completed.usage` has `input_tokens`, `cached_input_tokens`,
`cache_write_input_tokens`, `output_tokens`, `reasoning_output_tokens`.

Command: `codex exec --json --sandbox workspace-write <task>`, stdin closed.
Never unrestricted mode.

- [ ] Parse `testdata/codex-success.jsonl`; assert `CostUSD` is **nil**, not 0.
- [ ] Success comes from exit code plus a terminal `turn.completed`; a stream
      ending without one is a failure even on exit 0.
- [ ] Document in code that Codex reports no denials, so policy-violation
      detection is weaker there — stated, not hidden.

### Task 4: Both adapters satisfy one contract

**Files:** `control-plane/internal/runner/contract_test.go`

- [ ] A table-driven test running the *same* assertions across both adapters:
      both populate Harness, Status, Usage and Summary; neither panics on empty
      input; neither returns a nil result with a nil error.

---

## M3b (next)

Job controller, `agent-runs` namespace, narrowly scoped RBAC (create/get/list/
watch/delete Jobs and read pod logs, in one namespace, nothing else), result
collection by reading pod logs through the same adapters.

## M3c — the runner image

### The log-interleaving problem

Kubernetes merges a container's stdout and stderr into one log stream. The
adapters parse JSONL and **error on any malformed line** — deliberately, so a
truncated stream is never mistaken for success. Put those two facts together and
a single line of `git clone` progress output corrupts the run result.

So the entrypoint frames the harness stream:

```
::STRANGE-COMPANY-STREAM-BEGIN::
{...harness JSONL, verbatim, nothing else...}
::STRANGE-COMPANY-STREAM-END::
```

Everything else — clone, checkout, commit, push, diagnostics — goes outside the
markers and is ignored by the parser but stays visible to a human reading
`kubectl logs`. The control plane extracts the framed region before parsing, and
**errors if the markers are absent**: a missing end marker means the Job was
killed mid-run, which is an infrastructure failure (§12.1), not a code failure.

### Task 1 — stream extraction (control plane)

`control-plane/internal/runner/stream.go`

```go
const StreamBegin = "::STRANGE-COMPANY-STREAM-BEGIN::"
const StreamEnd   = "::STRANGE-COMPANY-STREAM-END::"

func ExtractStream(podLog []byte) ([]byte, error)
var ErrStreamMissing, ErrStreamTruncated error
```

Tests: extracts a framed stream surrounded by noise; `ErrStreamTruncated` when
the end marker is missing; `ErrStreamMissing` when neither appears; tolerates
CRLF; ignores marker-like text *inside* the JSON payload.

### Task 2 — the runner image

`runner/Dockerfile`, `runner/entrypoint.sh`

Carries git and both CLIs. The entrypoint:

1. clones `REPO_URL` at `BASE_REF` into the emptyDir workspace (shallow),
2. creates or checks out `agent/<card-id>-<slug>` (§16.2),
3. runs the harness argv with **stdin closed** — Codex hangs forever otherwise,
   and it needs a git repo, which by this point it has,
4. frames the harness stream in the markers above,
5. commits any changes with a phase-appropriate message (§16.2),
6. pushes the agent branch,
7. exits with the harness's exit code, so §12.1 classification stays intact.

Non-root, no shell in the final image beyond what the entrypoint needs, git
credentials from a referenced Secret. `main` is never pushed to; only
`agent/*`.

### Task 3 — publish the runner image

Its own workflow, same shape as the control-plane image job.
