# Coding harness output contracts

Captured from the real CLIs on 2026-08-26, not from documentation or
recollection. Sanitised fixtures live in
`control-plane/internal/runner/testdata/`.

- Claude Code `2.1.246` — `claude -p --output-format stream-json --verbose`
- Codex `codex-cli 0.149.1` — `codex exec --json --sandbox read-only`

Four of the six defects in M0/M1 came from guessing an API shape. These
adapters are built against recorded output instead.

## Operational gotchas — both would hang or fail a Kubernetes Job

1. **Both CLIs read stdin.** Codex blocks *indefinitely* on
   `Reading additional input from stdin...`; Claude Code warns and proceeds
   after a 3 second stall. A Job has no stdin, so **close it explicitly**
   (`< /dev/null`) or Codex never returns and every Claude run wastes 3s.
2. **Codex refuses to run outside a git repository**: `Not inside a trusted
   directory and --skip-git-repo-check was not specified`. Our Jobs check out an
   agent branch, so the workspace *is* a repo and this is satisfied — but the
   adapter must not assume it can run anywhere.
3. Claude Code emits `system/hook_started` and `system/hook_response` events,
   because non-`--bare` mode loads project hooks. Under subscription auth
   `--bare` is unavailable (it does not read `CLAUDE_CODE_OAUTH_TOKEN`), so an
   untrusted checkout's hooks and `CLAUDE.md` **will** be loaded. That is a
   prompt-injection and execution surface coming from repository content, and it
   is why spec §24's allowlist matters.

## Claude Code — event stream

Observed event types, in order:

```
system/init  system/hook_started  system/hook_response  assistant
rate_limit_event  result/success
```

The terminal `result` event is the adapter's contract:

| Field | Type | Use |
|---|---|---|
| `subtype` | string | `success` \| error variants |
| `is_error` | bool | authoritative success flag |
| `stop_reason` | string | why the turn ended |
| `num_turns` | number | against the policy turn limit |
| `duration_ms`, `duration_api_ms` | number | run timing |
| `session_id` | string | correlate with the card's run |
| `result` | string | final assistant text |
| `total_cost_usd` | number | **cost, reported directly** |
| `usage` | object | `input_tokens`, `output_tokens`, `cache_read_input_tokens`, `cache_creation_input_tokens`, … |
| `modelUsage` | object | per-model breakdown |
| `permission_denials` | array | **attempted forbidden actions** |

`permission_denials` is the important one: spec §24 requires that an attempted
forbidden action moves the card to `Blocked` with `POLICY_VIOLATION`. Claude Code
reports denials structurally, so that is detected rather than inferred.

`system/init` also advertises `tools`, `mcp_servers`, `model`, `permissionMode`,
`apiKeySource`, `claude_code_version` — useful for recording exactly what the
run was allowed to do.

## Codex — event stream

```
thread.started  turn.started  item.completed  turn.completed
```

| Event | Fields |
|---|---|
| `thread.started` | `thread_id` — the session correlator |
| `item.completed` | `item.{id, text, type}` — output items |
| `turn.completed` | `usage.{input_tokens, cached_input_tokens, cache_write_input_tokens, output_tokens, reasoning_output_tokens}` |

## The asymmetry the adapter has to absorb

Three differences the common `CodingRunResult` must normalise:

1. **Cost.** Claude Code reports `total_cost_usd`; Codex reports **tokens only**.
   Codex cost must be computed from a price table, so §22's ledger cannot simply
   read a field for both harnesses.
2. **Success.** Claude Code has an explicit `is_error` / `subtype`. Codex has no
   status field on `turn.completed` — success has to come from the **process exit
   code** plus the presence of a terminal `turn.completed`.
3. **Policy violations.** Claude Code has `permission_denials`. Codex has no
   equivalent, so a Codex violation is only visible via sandbox behaviour and
   exit code. Detection is therefore weaker for Codex, and that should be stated
   rather than papered over.
