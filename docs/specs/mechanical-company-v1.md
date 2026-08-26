# The Mechanical Company

Autonomous Engineering Control Plane — v1 Specification

> Status: PART 1 OF 2 received. Part 2 pending — do not treat as complete.
> Received 2026-08-26. The substrate this builds on is the `strange-company`
> Helm chart (see `strange-company-helm-chart.md`), already delivered.

## 1. Purpose

Build a self-hosted system on an existing K3s cluster in which small, inexpensive
Hermes agents autonomously perform the day-to-day coordination of software work.

A Hermes agent should be able to:

1. notice that work is available;
2. atomically claim one Kanban card;
3. inspect its specification and execution policy;
4. determine which phase of work is required;
5. invoke an appropriately priced Claude Code or Codex coding agent;
6. collect deterministic evidence from that execution;
7. attach artifacts and costs to the card;
8. move the card to its next legitimate state;
9. escalate when policy says to escalate;
10. terminate.

The agent is not trusted to decide whether its own work is correct.
**The model proposes. The system disposes.**

Governing rule:

> If the intelligence is probabilistic, move the certainty outside the intelligence.

Every card must have externally observable acceptance criteria before
implementation begins. A card cannot enter In Progress before its criteria exist,
and is Done only when those criteria are observably satisfied.

## 2. Design Principles

### 2.1 Hermes is the foreman

Hermes is not normally the coding agent. It performs inexpensive cognitive
coordination: find work, read policy, claim work, choose the next allowed action,
invoke a specialist, interpret structured results, update workflow state,
escalate when required.

Default Hermes model: `foreman-cheap` — initially a cheap cloud API such as
DeepSeek. Provider and model must be a **versioned configuration value**, not
embedded in prompts or source.

### 2.2 Coding agents are specialists

Source-code work is performed by Claude Code or Codex, both accessed through a
common **coding-runner** interface so the company does not care which harness
performed the work. Both support noninteractive execution, structured output,
model selection, turn limits, and explicit allowed/disallowed tools.

### 2.3 Intelligence is allocated according to ambiguity

| Work | Default intelligence |
|---|---|
| Routine coordination | Cheap Hermes / DeepSeek-class |
| Genuine ambiguity and specification | Claude Fable 5, conversational |
| Implementation planning | Claude Opus 5 |
| Writing acceptance tests | Claude Sonnet 5 |
| Implementation attempts 1–3 | Claude Haiku 4.5 |
| Implementation attempts 4–6 | Claude Sonnet 5 |
| Implementation attempt 7 | Claude Opus 5 |
| Objective verification | No model |
| Routine code review | Sonnet 5 |
| Unresolved ambiguity/failure | Human + optionally Fable |

Model names are **policy aliases, not application logic**. A later configuration
may substitute a Codex model for one or more implementation tiers without
changing the workflow.

### 2.4 Do not spend intelligence on mechanical work

No model call is permitted when deterministic software can answer the question:
is a dependency Done; did the test command exit zero; has the card exceeded its
attempt limit; which Ready card is oldest; is a worker lease still valid; is this
command in the allowlist; did the branch change; what did this run cost; can this
state transition legally occur.

### 2.5 Rules live in files, not prompts

Model routing, permissions, priority rules, retry counts, risk policy, acceptance
rubrics and management thresholds are version-controlled YAML/JSON/TOML.
Models may read policies. Models may propose changes. Models may **not** silently
rewrite policy.

## 3. Major Components

```
                             HUMAN
                               │
                 ┌─────────────┴─────────────┐
                 │                           │
          Hermes Dashboard              Vikunja
          conversation/UI              Kanban UI
                 │                           │
                 │                           │ webhooks/API
                 ▼                           ▼
           Hermes Gateway             CONTROL PLANE
         cheap foreman model          + PostgreSQL
                 │                           │
                 │ MCP / HTTP tools          │
                 └─────────────┬─────────────┘
                               ▼
                        CODING RUNNER
                               │
                 creates isolated K3s Job
                ┌──────────────┴──────────────┐
                ▼                             ▼
          Claude Code                       Codex
      + obra/superpowers              + obra/superpowers
                └──────────────┬──────────────┘
                               ▼
                       repo / agent branch
                               ▼
                 compiler / tests / linters
                               ▼
                         objective result
                               ▼
                         CONTROL PLANE
```

## 4. Kanban

### 4.1 Selected application

Vikunja for v1: self-hosted, mature Kanban UI, REST API, API-token auth, project
webhooks, signed webhook payloads, less machinery than a larger PM suite.

**Important constraint:** Vikunja webhook delivery is one-shot and does not retry
failures. Therefore **webhooks are hints, not the source of truth**, and the
control plane MUST additionally perform periodic reconciliation against Vikunja.

Default reconciliation interval: **60 seconds**. No valid state change may become
permanently lost because a webhook failed.

### 4.2 Board columns

`Backlog`, `Ready`, `In Progress`, `Review`, `Done`, `Blocked`, `Needs Human`.

**Ready** means: specification exists, acceptance criteria exist, and all required
dependencies are satisfied.

### 4.3 Vikunja versus control-plane ownership

Vikunja owns: title, description, human-visible labels, visible board column,
comments, links, due dates.

The control-plane database owns: canonical execution state, phase, atomic claim,
worker lease, model attempts, model routing, execution budget, repository
metadata, branch, artifacts, deterministic test results, policy decisions, cost
ledger, immutable state-transition history.

Linked by `vikunja_task_id`. A control-plane state change MUST be mirrored to
Vikunja. A human movement in Vikunja MUST be received through webhook or
reconciliation and validated against the state machine before becoming canonical.

## 5. Card Schema

```yaml
id: uuid
vikunja_task_id: integer
title: string

source:
  type: github_issue | manual | pm_bot | child_card
  url: string | null
  external_id: string | null

repo:
  url: string
  base_ref: string
  runner_image: string | null
  bootstrap_command: string | null

branch: string | null

state: Backlog | Ready | InProgress | Review | Done | Blocked | NeedsHuman

phase: specification | planning | tests | implementation | verification | review | complete

spec_uri: string | null
plan_uri: string | null

acceptance_criteria:
  - id: string
    text: string
    verification: string

dependencies:
  - card_id

permitted_actions:
  files:
    include: []
    exclude: []
  commands: []
  endpoints: []
  network: []

risk_class: R0 | R1 | R2 | R3

claimed_by: string | null
lease_expires_at: timestamp | null

implementation_attempt: integer
infrastructure_failures: integer

max_cost_usd: decimal | null
cost_usd: decimal

artifacts: []
test_results: []
history: []

created_at: timestamp
updated_at: timestamp
```

## 6. Atomic Claiming

Two agents must never execute the same card concurrently. Claiming is handled by
PostgreSQL, not by Hermes.

```sql
BEGIN;

SELECT id
FROM cards
WHERE state = 'Ready'
  AND claimed_by IS NULL
ORDER BY effective_priority, created_at
FOR UPDATE SKIP LOCKED
LIMIT 1;

UPDATE cards
SET state = 'InProgress',
    claimed_by = :worker_id,
    lease_expires_at = NOW() + INTERVAL '10 minutes'
WHERE id = :id;

INSERT INTO card_history (...);

COMMIT;
```

Worker heartbeats once per minute. A lease may be reclaimed only after expiry.
Claiming MUST be safe with at least ten simultaneous claim attempts against a
single Ready card.

Acceptance criterion: **exactly one caller receives the card**.

## 7. The Meeseeks Worker

Each ordinary Hermes worker is intentionally short-lived. Internal role name:
`meeseeks`.

> Claim one thing. Make the thing stop being your problem. Cease to exist.

A worker MUST NOT become a long-running personal assistant with an ever-growing
pile of unrelated context.

### 7.1 Worker lifecycle

```
SPAWN → claim_ready_card() ─(none)─► EXIT
          │
          ▼
   load_card() / load_policy()
          ▼
   determine_phase()
          ▼
   perform exactly one workflow
          ▼
   attach evidence / transition card
          ▼
   release claim → EXIT
```

A card may require several Meeseeks over its lifetime. That is desirable.
Workflow state lives in the system, not in a model's memory.

## 8. Hermes Integration

### 8.1 Hermes services

**Hermes Gateway** — agent runs, session IDs, streaming events, run status, stop
control, cheap default model, MCP access to company tools. Exposes a runs API
with submission, polling, SSE events and cancellation; this is the preferred
control-plane interface.

**Hermes Dashboard** — interactive human chat, Fable specification conversations,
session inspection, cost/usage inspection, model/profile management, skills
management, logs.

### 8.2 Hermes profiles

At minimum `foreman`, `specifier`, `pm`, `scrum`.

- **foreman** — model `foreman-cheap`; tools: company MCP only, optional
  web/search if a card permits. No raw Kubernetes credentials, no GitHub write
  credential, no production credentials, no unrestricted shell.
- **specifier** — model `spec-frontier = Claude Fable 5`; interactive human ↔
  Fable specification work. May read issue/card, read repository, write spec
  draft, write acceptance criteria draft, perform ambiguity audit. It cannot
  independently mark the resulting spec approved.
- **pm** — weekly portfolio bot.
- **scrum** — daily board-management bot.

## 9. Company MCP Server

A deliberately small tool surface:

```
cards.list_ready   cards.get       cards.claim
cards.heartbeat    cards.release   cards.transition
cards.comment      cards.request_human
artifacts.attach   artifacts.list
coding.plan        coding.write_tests
coding.implement   coding.review
verification.run
cost.get_card      cost.get_run
portfolio.scan     portfolio.get_policy   scrum.get_policy
```

Hermes MUST NOT be handed a generic Kubernetes tool. Hermes asks
`coding.implement(card_id)`; the control plane decides what pod, image,
credentials, model and sandbox that implies. **This is a core security boundary.**

## 10. Specification Gate

A Backlog card cannot become Ready merely because an LLM says it is ready.
Deterministic checks first: specification exists; every required spec section
exists; at least one acceptance criterion exists; every criterion has a stated
verification method; repository exists; dependencies are Done; permitted-actions
policy exists.

### 10.1 Cheap ambiguity screening

The cheap foreman may classify `0` mechanical / `1` minor interpretation /
`2` material ambiguity / `3` fundamental product ambiguity. It may recommend
escalation. It may NOT resolve score 2–3 ambiguity by inventing requirements.

- **0–1** — proceed if all deterministic requirements pass.
- **2–3** — card stays Backlog or becomes Blocked with `reason = SPEC_REQUIRED`;
  UI presents "Spec with Fable"; the human initiates an interactive Fable session.

### 10.2 Fable specification conversation

Fable receives the original request, repository context, current spec, existing
acceptance criteria and the ambiguity report. The goal is conversation, not
autonomous implementation.

Output `/specs/<card-id>.md` with required sections: Context, Task, Evidence
available, Interfaces, Constraints, Invariants, Permitted actions, Forbidden
actions, Acceptance criteria, Out of scope, Failure behavior.

Human approves the completed spec. Only then may the control plane promote the
card to Ready.

## 11. Coding Pipeline

```
SPEC → OPUS PLAN → SONNET TESTS → DETERMINISTIC RED CHECK
     → HAIKU IMPLEMENT ×3 → SONNET IMPLEMENT ×3 → OPUS IMPLEMENT ×1
     → DETERMINISTIC GREEN CHECK → SONNET REVIEW → REVIEW
```

### 11.1 Planning phase

Model `Claude Opus 5`, harness Claude Code, read-only/plan-oriented where
possible. Input: approved spec, repository, current branch/base, acceptance
criteria. Output `/artifacts/<card-id>/implementation-plan.md`.

The plan MUST map each criterion to implementation work, name likely files,
identify migrations/interfaces, identify verification commands, identify
dependencies, call out risk, and avoid changing scope.

A planner may declare `SPEC_INSUFFICIENT`, returning the card to `NeedsHuman` or
specification work. It may not guess.

### 11.2 Test-writing phase

Model `Claude Sonnet 5`. Input: spec, plan, repository, acceptance criteria.
Required workflow: Superpowers test-driven-development. Output: test changes,
criterion → test mapping, test command.

**The test-writing agent MUST NOT implement the requested feature.**

### 11.3 Red gate

After tests are written, no model grades them. The runner executes them against
the unimplemented feature state. Required: newly introduced acceptance tests
fail; failure is attributable to missing behavior; pre-existing test baseline
remains understood.

If the new tests pass without implementation, the test phase fails. If they fail
because the tests are malformed, the test phase fails. The card does not proceed
until a valid red state exists.

## 12. Implementation Escalation Policy

Default policy file `/policy/models.yaml`:

```yaml
foreman:
  model: foreman-cheap

specification:
  model: anthropic/claude-fable-5
  interactive: true
  requires_human: true

planning:
  model: anthropic/claude-opus-5
  max_attempts: 1

tests:
  model: anthropic/claude-sonnet-5
  max_attempts: 2

implementation:
  - model: anthropic/claude-haiku-4-5
    max_attempts: 3
  - model: anthropic/claude-sonnet-5
    max_attempts: 3
  - model: anthropic/claude-opus-5
    max_attempts: 1

review:
  model: anthropic/claude-sonnet-5
```

### 12.1 Attempt semantics

An implementation attempt means: the coding agent received valid context;
performed work; the runner regained control; objective verification ran; and
verification did not pass.

NOT implementation failures: provider outage, Kubernetes scheduling failure,
runner image pull failure, authentication outage, GitHub outage. Those increment
`infrastructure_failures` and do not burn a Haiku/Sonnet/Opus attempt.

### 12.2 Feedback

After a failed attempt the next agent receives: original spec, implementation
plan, current branch, failing test output, compiler/linter output, previous
attempt summary, diff since previous attempt. Raw deterministic errors are
preserved verbatim where practical.

The model does not receive seven pages of previous model monologue. It receives
evidence.

### 12.3 Escalation

```
Haiku ×3 → Sonnet ×3 → Opus ×1 → Needs Human
```

No eighth implementation attempt occurs automatically. Fable is not silently
invoked as an ultra-expensive eighth coder. If the ladder is exhausted, something
about the specification, plan, architecture or problem deserves human attention.

## 13. Claude Code Adapter

```
claude -p \
  --model <policy-model> \
  --output-format stream-json \
  --max-turns <policy-limit> \
  --allowedTools <generated-allowlist> \
  <task>
```

The adapter MUST generate permissions from the card. It MUST NOT use unrestricted
permission bypass simply because execution is autonomous.

## 14. Codex Adapter

```
codex exec --sandbox workspace-write --json <task>
```

Codex must run inside the already isolated worker pod. Do not use
unrestricted/yolo execution. The adapter parses JSONL/structured output and
normalizes it to the same `CodingRunResult` returned by the Claude adapter:

```json
{
  "status": "completed",
  "harness": "codex",
  "model": "...",
  "changed_files": [],
  "summary": "...",
  "usage": {},
  "exit_code": 0
}
```

## 15. obra/superpowers

Both coding harnesses receive Superpowers, treated as a development skill layer,
not as the company control plane. Use brainstorming only during approved
specification work; writing-plans during planning; using-git-worktrees where
appropriate; test-driven-development during test/implementation work;
systematic-debugging after failure; verification-before-completion;
requesting-code-review; receiving-code-review.

The control plane still overrides any agent skill where the two disagree.
Superpowers may think the task is finished; the control plane sees failing tests;
the task is not finished.

## 16. Kubernetes Coding Jobs

Every coding execution occurs in an isolated Kubernetes Job in namespace
`agent-runs`. The Hermes foreman never receives permission to create arbitrary
pods. The control plane owns that permission.

### 16.1 Job characteristics

<!-- PART 1 ENDS MID-SECTION. Part 2 to be appended here. -->
