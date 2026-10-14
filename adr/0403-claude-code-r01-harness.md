# ADR-0403: Claude Code is the R01 real coding harness

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 28

## Context
The Self-Hosting Threshold requires one real coding harness (SPEC §64, §117). Candidates are Claude Code, Codex and OpenCode. The integration depends on: a structured terminal event with success/failure (L-2), reporting of attempted forbidden actions, tool allowlisting, a way to start without loading repository hooks, settings, MCP servers or instruction files (L-2), a base-URL setting to route through the Model Gateway with an API key (ADR-0004, SPEC §77), and evidence from real use.

## Decision
Claude Code is enabled at R01 (`policy/harnesses.yaml` entry `claude`, adapter `claude-code`, API family `anthropic-messages`). It runs headless (`-p --output-format stream-json --verbose`), with `--bare` repository-config isolation and an empty strict MCP configuration, API-key mode with the gateway token in `ANTHROPIC_API_KEY`, `ANTHROPIC_BASE_URL` pointing at the Gateway, tool allow/deny lists narrowed by role, and model-class environment variables pinned to the routed model(s). Codex and OpenCode adapters are specified but disabled until post-MVP.

Comparison at decision time:
| Need | Claude Code | Codex | OpenCode |
|---|---|---|---|
| Terminal result event with status | yes (`result`, `is_error`, `subtype`) | usage only (`turn.completed`), no status (L-2) | unreliable in containers (L-3) |
| Permission-denial report | yes (`permission_denials`) | no | no |
| Tool allowlist | yes | no | permission config |
| Repo-config isolation | `--bare` (verify per version) | partial (`project_doc_max_bytes`, verify) | verify |
| Gateway base URL + API key | yes | yes (custom provider) | yes |
| Verified prior use (L-2) | yes | yes | partial |

## Consequences
- R01 depends on an Anthropic API key (`SC-MODEL-API-KEYS` instance `anthropic`) and on the version-sensitive flags being re-verified by the image self-test and the injection test (28-AC-20, 28-AC-23).
- If the pinned version cannot be isolated from repository configuration, R01 is blocked and this ADR is revisited.
- Routing (plan 31) constrains the `claude` harness to provider `anthropic`, so there is no cross-provider fallback for it.

## Alternatives rejected
- Codex first: no failure status or cost in its terminal event and no tool allowlist; acceptable later, weaker for the threshold.
- OpenCode first: dropped usage/terminal events in containers (L-3) and less isolation evidence.
- Building an SC-native agent loop: product invention SPEC does not ask for, and it would make SC's own harness define "the agent" (SPEC §64).

## Verification
28-AC-20…28-AC-27 (image, parsing, gateway-only egress, injection isolation, smoke test, memory, enablement, API-key mode).
