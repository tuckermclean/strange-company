# Strange Company — Autonomous Engineering Control Plane

v1 Specification

> Naming: this document was originally drafted as "The Mechanical Company".
> The canonical name is **strange-company** throughout — chart, namespace,
> repository, OCI artifact and identifiers. Decided 2026-08-26.

> Status: COMPLETE (parts 1 and 2 received 2026-08-26). The substrate this builds on is the `strange-company`
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

**Important constraint:** webhooks are **hints, not the source of truth**, and
the control plane MUST additionally perform periodic reconciliation against
Vikunja.

> Correction, verified against upstream v2.5.0 on 2026-08-26: the original claim
> that delivery is "one-shot and does not retry" is **wrong** -- Vikunja wraps
> delivery in watermill retry middleware (5 retries, exponential backoff up to an
> hour, then a poison queue). The conclusion still holds, but for a different
> reason: the pub/sub is the in-memory `gochannel` implementation, so anything
> queued is lost on process restart. Reconciliation is required because delivery
> does not survive a restart, not because it is never retried.
> See `docs/reference/vikunja-api-notes.md`.

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

Each Job receives: card ID, run ID, model alias, harness selection, repository
URL, branch, spec, plan, permitted-action file, and model credentials appropriate
to that run.

Each Job has: CPU limit, memory limit, wall-clock timeout, non-root user,
read-only root filesystem where practical, ephemeral workspace, no hostPath, no
privileged mode, no cluster-admin credentials, `automountServiceAccountToken: false`.

### 16.2 Repository persistence

Do not require a persistent PVC per card in v1. **Git is the persistent
workspace.**

Card branch: `agent/<card-id>-<slug>`

Each successful intermediate phase commits to the agent branch:

```
plan(card-123): attach implementation plan
test(card-123): add acceptance tests
wip(card-123): implementation attempt 1
wip(card-123): implementation attempt 2
feat(card-123): satisfy acceptance criteria
```

Broken intermediate work may exist on the isolated agent branch. `main` remains
protected. Every attempt gets a durable, inspectable history without sticky
worker storage.

## 17. Verification

Verification is owned by the runner. Never ask "did you fix it?" Ask:

```
$ test-command
exit 0
```

May include: compile/build, unit tests, integration tests, acceptance tests,
lint, formatting check, type check, security scan, generated artifact validation.
The card defines its required verification commands.

A successful model process with failed verification is an unsuccessful attempt.
A failed model process with a passing implementation may be examined by the
runner, but MUST NOT automatically succeed unless all expected artifacts and
checks exist.

## 18. Automated Review

After the deterministic green gate, run one independent Sonnet review. The
reviewer receives the approved spec, implementation plan, acceptance criteria,
final diff and passing verification summary. The reviewer does NOT receive the
implementer's private reasoning.

Two stages: specification compliance, then code quality.

Results: `PASS` | `CORRECTABLE` | `BLOCKING`

`CORRECTABLE` sends the card back into implementation without resetting global
escalation history unless policy explicitly permits. `BLOCKING` sends the card to
`NeedsHuman`. **Automated review cannot move a card to Done.**

## 19. Human Review and Merge

For v1 the human remains the final merge authority. When all gates pass: agent
branch is pushed, a pull request is created, the card moves to Review, and the
PR, test evidence, cost, artifacts and review result appear on the card.

- Approval: `Review → merge → Done`
- Rejection: `Review → Ready`, with the reason attached.

Implementation-attempt policy may reset according to a versioned review policy.
A future R0 risk class may permit auto-merge after sufficient evidence — out of
scope for v1.

## 20. Artifacts

Types: `spec`, `ambiguity-report`, `implementation-plan`, `test-mapping`,
`test-output`, `diff`, `compiler-output`, `linter-output`, `security-output`,
`review`, `cost-report`, `failure-summary`, `human-decision`.

Metadata: `id`, `card_id`, `attempt_id`, `type`, `created_at`, `actor`, `model`,
`commit_sha`, `content_type`, `storage_uri`, `sha256`.

For v1: small text artifacts may live in PostgreSQL; source changes live in Git;
large logs are capped and compressed. Introduce S3/MinIO only when artifact size
requires it.

## 21. Audit Log

Every state transition produces an immutable record: `timestamp`, `card_id`,
`from`, `to`, `actor_type`, `actor_id`, `reason`, `run_id`, `evidence`.

The stakeholder view must answer "what happened to card X?" **without exposing
model chain-of-thought** — based on states, actions, diffs, tests, artifacts,
costs and decisions.

## 22. Cost Ledger

Every model call records: `provider`, `model`, `harness`, `phase`, `card_id`,
`attempt`, `input_tokens`, `output_tokens`, `cached_tokens`, `started_at`,
`duration_ms`, `estimated_cost_usd`.

Each card displays a per-phase breakdown and total.

The system MUST make it possible to answer: how often does Haiku finish before
escalation; what percentage require Sonnet; how often does Opus rescue a failed
task; how much does specification cost versus implementation; what is average
cost per completed card; which repositories consume the most model spend.

That data is the empirical test of the model-tiering thesis.

## 23. Budgets

A card may contain `max_cost_usd`. Before invoking the next tier the control
plane computes whether the run may exceed the remaining budget. If not:

```
InProgress → NeedsHuman
reason = BUDGET_ESCALATION_REQUIRED
```

**A model may never increase its own budget.**

## 24. Forbidden Actions

Every card has an allowlist; the worker sandbox enforces it.

Permitted by default: read repository; modify files inside workspace; execute
project tests; execute approved build tools; create commits on `agent/*`; push
own agent branch.

Forbidden by default: force push shared branches; modify `main`; delete remote
branches; access unrelated repositories; read cluster Secrets; `kubectl`; SSH to
arbitrary systems; production database access; production deployment; billing
changes; DNS changes; privilege escalation.

An attempted forbidden action causes `card → Blocked, reason = POLICY_VIOLATION`.
It is logged. It is not something another model should "try harder" to circumvent.

## 25. GitHub Integration

**Inbound.** A GitHub issue labeled `agent-ready` is eligible for ingestion.
Within 60 seconds: create/update canonical card; create Vikunja task; attach
source URL; parse spec reference; parse acceptance criteria. If required
information is absent: `state = Backlog or Blocked, reason = SPEC_REQUIRED`.

**Outbound.** On verified implementation: push `agent/<card-id>-slug`;
create/update pull request; include card link, acceptance-criterion checklist,
verification summary, automated review result and cost summary.

GitHub never becomes the agent execution database.

## 26. Management Bots

**PM bot** — weekly. "Which projects deserve attention?" Most repository scanning
is deterministic. The model receives scan facts plus a version-controlled
portfolio rubric and returns **exactly three** MOVE recommendations; everything
else appears under IGNORE. A fourth recommendation is a failure.

**Scrum bot** — daily. "Within the work we've chosen, what happens next?" Reports
movement, stuck cards, Needs Human, WIP; orders Ready cards using deterministic
rules; proposes splits for oversized cards; produces a two-week sprint review.

Deterministic ordering: cards that unblock others; PM-bot recommendations by
rank; oldest Ready card. Pinned cards override automation.

Management bots advise and organize. They do not get broad executive authority
merely because they are called managers. That is a feature.

## 27. K3s Deployment

Namespaces: `strange-company`, `agent-runs`. (§27 originally said
`mechanical-company`; superseded by the naming decision above.)

```
strange-company/        agent-runs/
  postgres                ephemeral claude-code jobs
  vikunja                 ephemeral codex jobs
  company-control-plane
  hermes-gateway
  hermes-dashboard
```

- **postgres** — persistent volume; canonical workflow database.
- **vikunja** — persistent application data; ingress `kanban.<domain>`.
- **company-control-plane** — 2 replicas permitted once claim logic is proven.
  Owns: state machine, board sync, GitHub integration, atomic claiming, Hermes
  run dispatch, coding Job creation, verification, audit, cost ledger, MCP
  server. Ingress/API generally internal except authenticated GitHub/Vikunja
  callbacks.
- **hermes-gateway** — not necessarily public; used by the control plane through
  cluster networking.
- **hermes-dashboard** — ingress `hermes.<domain>`, authenticated.

## 28. Network Policy

Default stance: **deny**.

- **Hermes** may reach: control plane, configured model provider, approved Hermes
  tool providers. May not reach: Kubernetes API, Postgres directly, arbitrary
  internal services.
- **Coding Job** may reach: model provider, GitHub, explicitly required
  dependency registries. May not reach: Kubernetes API, control-plane database,
  unrelated internal applications, production infrastructure.
- **Control plane** may reach: Kubernetes API for Job management, Postgres,
  Vikunja, Hermes, GitHub.

## 29. Secrets

Kubernetes Secrets contain: Hermes provider credentials, Anthropic credentials,
OpenAI credentials, GitHub App credentials, Vikunja API token, webhook HMAC
secrets, database credentials.

Coding Jobs receive only credentials required by that run. A Haiku Claude Code
Job should not receive an OpenAI key; a Codex Job should not receive an Anthropic
key; neither receives the control-plane database password.

## 30. Control-Plane API

```
GET    /cards
GET    /cards/{id}
POST   /cards/{id}/claim
POST   /cards/{id}/heartbeat
POST   /cards/{id}/release
POST   /cards/{id}/transition
POST   /cards/{id}/approve-spec
POST   /cards/{id}/approve
POST   /cards/{id}/reject
GET    /cards/{id}/artifacts
GET    /cards/{id}/attempts
GET    /cards/{id}/cost
POST   /webhooks/vikunja
POST   /webhooks/github
POST   /runs/{card}/plan
POST   /runs/{card}/tests
POST   /runs/{card}/implement
POST   /runs/{card}/review
GET    /health
GET    /ready
```

Hermes should normally access these through the narrower MCP interface rather
than improvising HTTP.

## 31. Observability

```
cards_total{state}            cards_completed_total     cards_needs_human_total
worker_claims_total           worker_active
runs_total{phase,model,harness,result}                  run_duration_seconds
run_cost_usd                  implementation_attempts{model}
escalations_total{from_model,to_model}
verification_total{result}    policy_violations_total
card_cycle_time_seconds       card_cost_usd
```

Logs must include card ID, run ID, attempt ID, worker ID. Do not log API keys. Do
not expose hidden model reasoning as an operational requirement.

## 32. Failure Classification

Every failure is classified before retrying:

`CODE_FAILURE` `TEST_FAILURE` `SPEC_FAILURE` `POLICY_FAILURE` `INFRA_FAILURE`
`PROVIDER_FAILURE` `BUDGET_FAILURE` `HUMAN_REQUIRED`

- CODE/TEST — use the model escalation ladder.
- SPEC — stop implementation, return to specification.
- POLICY — block immediately.
- INFRA/PROVIDER — retry per infrastructure policy; do not burn model-tier attempts.
- BUDGET / HUMAN_REQUIRED — Needs Human.

This prevents the very AI-like behavior of responding to every kind of problem
with "try again, but harder."

## 33. User Experience

**Kanban card**, in this order: title; state / current phase; why it exists;
acceptance criteria; current worker (`Meeseeks #8f2c — Implementation — Haiku
attempt 2/3`); latest result (`8/9 tests passed. auth_timeout_test failed.`);
cost (`$0.41 so far`); artifacts (spec, plan, tests, diff, logs, PR); history
with timestamps.

No chain-of-thought dump. No need to watch a terminal scroll by. The product is
visibility into work.

**Hermes dashboard** is where the human talks to the company: what's stuck; why
did card 143 escalate; show me this week's expensive cards; let's spec card 208
with Fable; why did PM bot choose Stele instead of Monolith; spawn workers for
the Ready queue. Hermes answers by inspecting deterministic company state and
invokes tools when action is requested.

## 34. Build Sequence

- **Milestone 0 — Infrastructure.** Postgres, Vikunja, Hermes gateway, Hermes
  dashboard, TLS/auth, control-plane skeleton. *Exit: all services reachable and
  healthy.*
- **Milestone 1 — Deterministic board.** Canonical card schema, state machine,
  Vikunja synchronization, immutable history, atomic claims, leases. *Exit: ten
  workers race for one Ready card; exactly one gets it.*
- **Milestone 2 — One Meeseeks.** Company MCP, cheap Hermes foreman, claim →
  inspect → update → exit, Hermes run/session linked to card. *Exit: a Hermes
  worker autonomously claims a dummy card, performs a deterministic tool action,
  records evidence, and moves it to Review.*
- **Milestone 3 — Coding runner.** Kubernetes Job controller, Claude Code
  adapter, Codex adapter, Git agent branches, artifacts, Superpowers
  installation. *Exit: a worker can invoke either coding harness on a repository
  and collect structured results.*
- **Milestone 4 — Spec → plan → tests.** Fable specification profile, spec
  approval, Opus planner, Sonnet test writer, deterministic red gate. *Exit: a
  real feature reaches a valid failing acceptance test without implementation
  code.*
- **Milestone 5 — Escalation ladder.** Haiku ×3 → Sonnet ×3 → Opus ×1, plus
  deterministic verification and failure summaries. *Exit: a deliberately chosen
  task exercises at least one escalation boundary and the logged attempt/model
  sequence exactly matches policy.*
- **Milestone 6 — Review.** Sonnet independent review, PR creation, human
  approve/reject, Done transition. *Exit: one real GitHub issue travels the whole
  pipeline without manual orchestration between steps.*
- **Milestone 7 — Management.** PM bot, Scrum bot, daily digest, weekly portfolio
  brief, cost reports. *Exit: the company chooses and orders its own eligible
  engineering work under version-controlled policy.*

## 35. v1 Definition of Done

1. A GitHub issue labeled `agent-ready` becomes a board card within 60 seconds.
2. A card cannot enter Ready without a specification and acceptance criteria.
3. Two simultaneous agents cannot claim the same card.
4. A cheap Hermes worker can claim a card without human intervention.
5. Hermes can invoke Claude Code through the coding-runner abstraction.
6. Hermes can invoke Codex through the same abstraction.
7. Fable is used for interactive unresolved ambiguity rather than ordinary implementation.
8. Opus creates the implementation plan.
9. Sonnet creates acceptance tests.
10. Those tests are proven red before implementation begins.
11. Implementation begins with Haiku.
12. Three failed Haiku attempts escalate to Sonnet.
13. Three failed Sonnet attempts escalate to Opus.
14. One failed Opus attempt moves the card to Needs Human.
15. Infrastructure failures do not consume implementation attempts.
16. No model determines that its own tests passed.
17. A deterministic runner performs final verification.
18. Every attempt records model, harness, result, evidence, tokens where available, and cost.
19. A forbidden action is rejected by software and recorded.
20. Passing work creates or updates a pull request.
21. Passing work moves to Review, never directly to Done.
22. Human approval merges and moves the card to Done.
23. Human rejection records a reason and returns the card for additional work.
24. A nontechnical observer can inspect a card and explain what happened without reading model transcripts.
25. Every state transition has timestamp, actor, and reason.
26. The PM bot can produce exactly three evidence-backed weekly MOVE recommendations.
27. The Scrum bot can produce a daily board digest and deterministically order eligible Ready work.
28. One real issue has completed the entire pipeline.
29. One real issue has exhausted or crossed at least one model tier so the escalation mechanism has been demonstrated rather than merely unit-tested.
30. An agent has moved a real card to Review without a human directing the intermediate steps.
