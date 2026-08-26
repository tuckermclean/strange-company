# Control Plane M2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans.
> Steps use checkbox (`- [ ]`) syntax.

**Goal:** Milestone 2 — the Company MCP server and one Meeseeks worker that
claims a card, does a deterministic tool action, records evidence, and exits.
Plus the provider/credential policy layer that keeps every model choice a
configuration decision rather than a code change.

**Architecture:** Three additions to the existing binary. A policy package that
loads version-controlled YAML (never prompts) and answers "which provider, which
model, which credentials for this phase". An MCP server exposing a deliberately
small tool surface. A worker loop that is pure deterministic control flow.

**Tech Stack:** Go 1.25, existing `internal/store` and `internal/card`,
`gopkg.in/yaml.v3` for policy, MCP over HTTP.

**Spec:** `docs/specs/strange-company-control-plane-v1.md` §2.3, §2.4, §2.5,
§7, §9, §12, §29.

## Global Constraints

- **No model call anywhere in M2.** §2.4: no model call is permitted when
  deterministic software can answer the question. The entire Meeseeks lifecycle
  is deterministic control flow; CI must pass with no provider credential of any
  kind present.
- **Providers are configuration, decided at deploy time, not in code.** Nothing
  in the control plane may name Anthropic, OpenAI, DeepSeek or Ollama as a
  special case. Adding a provider is a YAML edit.
- **Credentials are least-privilege by construction** (§29). A run receives
  exactly the environment variables its declared provider asks for and nothing
  else. This is enforced by the resolver, not by reviewer discipline.
- **Models may read policy; models may not rewrite it** (§2.5). Policy is
  mounted read-only.
- Hermes never gets a generic Kubernetes tool (§9). It asks for
  `coding.implement(card_id)`; the control plane decides what that implies.
- Every state change still goes through `card.CanTransition` and appends exactly
  one `card_history` row.

## Why the policy layer exists

The operator's requirement is "allow anything — API key, OAuth, Codex, DeepSeek,
Ollama, whatever". Three facts make that a design constraint rather than a
preference:

- Claude subscription OAuth is **scoped to the Claude Code binary**. A
  third-party caller (Hermes) cannot legitimately use it, so at least two
  distinct credential kinds must coexist from day one.
- `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY` are read by the *same*
  harness, so "which credential" is not derivable from "which harness".
- §29 requires that a run not receive credentials it does not need.

So the unit of configuration is a **provider**: a harness, a credential set, and
optionally a base URL. A **model alias** points at a provider plus a model
string. Phases reference aliases. Nothing else in the system knows a provider
name.

## File Structure

```
policy/
├── providers.yaml            # provider -> harness + credentials + base URL
└── models.yaml               # phase -> ordered ladder of model aliases

control-plane/internal/policy/
├── policy.go                 # types + Load + Resolve
├── policy_test.go
└── testdata/…

control-plane/internal/mcp/
├── server.go                 # transport + tool dispatch
├── tools.go                  # the §9 tool surface
└── server_test.go

control-plane/internal/worker/
├── meeseeks.go               # claim -> inspect -> act -> transition -> exit
└── meeseeks_test.go

charts/strange-company/templates/policy-configmap.yaml
```

---

### Task 1: Policy types, loading and resolution

**Files:** `control-plane/internal/policy/policy.go`, `policy_test.go`,
`policy/providers.yaml`, `policy/models.yaml`

**Interfaces produced:**

```go
type Harness string   // "claude-code" | "codex" | "hermes"

type CredentialRef struct {
    Secret string `yaml:"secret"`
    Key    string `yaml:"key"`
}

type Provider struct {
    Harness  Harness                  `yaml:"harness"`
    BaseURL  string                   `yaml:"baseUrl,omitempty"`
    Env      map[string]CredentialRef `yaml:"env"`      // env var -> where it comes from
    PlainEnv map[string]string        `yaml:"plainEnv"` // non-secret settings
}

type Alias struct {
    Provider string `yaml:"provider"`
    Model    string `yaml:"model"`
}

type Rung struct {
    Alias       string `yaml:"model"`
    MaxAttempts int    `yaml:"max_attempts"`
}

type Policy struct {
    Providers map[string]Provider `yaml:"providers"`
    Aliases   map[string]Alias    `yaml:"aliases"`
    Phases    map[string][]Rung   `yaml:"phases"`
}

type Resolution struct {
    Phase, Alias, ProviderName, Model string
    Harness  Harness
    BaseURL  string
    Env      map[string]CredentialRef
    PlainEnv map[string]string
    Attempt  int
}

func Load(providersYAML, modelsYAML []byte) (*Policy, error)
func LoadDir(dir string) (*Policy, error)
func (p *Policy) Resolve(phase string, attempt int) (*Resolution, error)
func (p *Policy) Validate() error
var ErrLadderExhausted, ErrUnknownPhase, ErrUnknownAlias, ErrUnknownProvider error
```

`Resolve("implementation", n)` walks the ladder: attempts 1-3 the first rung,
4-6 the second, 7 the third, and returns `ErrLadderExhausted` at 8 — which is
what moves a card to `NeedsHuman` (§12.3). The ladder shape lives entirely in
YAML; the walk is arithmetic.

- [ ] **Step 1: Write the failing tests**

```go
func TestResolveWalksTheLadderExactlyAsPolicySays(t *testing.T) {
    p := loadTestPolicy(t) // haiku x3, sonnet x3, opus x1
    for _, tc := range []struct{ attempt int; wantAlias string }{
        {1, "implement-cheap"}, {3, "implement-cheap"},
        {4, "implement-mid"},   {6, "implement-mid"},
        {7, "implement-frontier"},
    } {
        got, err := p.Resolve("implementation", tc.attempt)
        if err != nil { t.Fatalf("attempt %d: %v", tc.attempt, err) }
        if got.Alias != tc.wantAlias {
            t.Errorf("attempt %d: want %s, got %s", tc.attempt, tc.wantAlias, got.Alias)
        }
    }
}

// Spec 12.3: no eighth implementation attempt happens automatically.
func TestTheLadderRunsOutRatherThanEscalatingForever(t *testing.T) {
    p := loadTestPolicy(t)
    if _, err := p.Resolve("implementation", 8); !errors.Is(err, ErrLadderExhausted) {
        t.Fatalf("want ErrLadderExhausted, got %v", err)
    }
}

// Spec 29: a run must not receive credentials it does not need.
func TestResolutionCarriesOnlyItsOwnProvidersCredentials(t *testing.T) {
    p := loadTestPolicy(t) // anthropic + openai providers both defined
    got, err := p.Resolve("implementation", 1)
    if err != nil { t.Fatal(err) }
    for name := range got.Env {
        if strings.Contains(strings.ToUpper(name), "OPENAI") {
            t.Fatalf("an Anthropic run was handed %s", name)
        }
    }
}

func TestValidateRejectsAnAliasPointingAtAnUnknownProvider(t *testing.T)
func TestValidateRejectsAPhaseReferencingAnUnknownAlias(t *testing.T)
func TestTheSameHarnessCanCarryDifferentCredentialKinds(t *testing.T)
    // claude-code via ANTHROPIC_API_KEY and via CLAUDE_CODE_OAUTH_TOKEN,
    // proving credential choice is not derived from harness.
func TestAProviderNeedingNoCredentialsIsValid(t *testing.T)
    // e.g. a local Ollama with only a baseUrl.
```

- [ ] **Step 2: Push, confirm RED** (`undefined: Load`)
- [ ] **Step 3: Implement**, then ship the real `policy/*.yaml` mirroring §12:
      `foreman-cheap`, `spec-frontier`, planning, tests, the three implementation
      rungs, review.
- [ ] **Step 4: Push, confirm GREEN**
- [ ] **Step 5: Commit**

---

### Task 2: Company MCP server

**Files:** `control-plane/internal/mcp/server.go`, `tools.go`, `server_test.go`

Expose exactly the §9 surface, no more:

```
cards.list_ready  cards.get  cards.claim  cards.heartbeat  cards.release
cards.transition  cards.comment  cards.request_human
artifacts.attach  artifacts.list
verification.run
cost.get_card  cost.get_run
```

`coding.*` and `portfolio/scrum.*` are declared but return
`ErrNotImplementedYet` in M2 — they belong to M3 and M7. Declaring them now
fixes the contract Hermes sees; implementing them now would be speculative.

- [ ] **Step 1: Write the failing tests**

The load-bearing one, because it is the security boundary in §9:

```go
func TestTheToolSurfaceIsExactlyTheSpecifiedSet(t *testing.T) {
    got := ToolNames()
    // No kubernetes.*, no exec, no shell, no arbitrary HTTP.
    for _, name := range got {
        if strings.HasPrefix(name, "kubernetes.") || name == "exec" || name == "shell" {
            t.Fatalf("spec 9: Hermes must never be handed %q", name)
        }
    }
    assertSetEqual(t, want, got)
}

func TestClaimReturnsNoWorkRatherThanAnErrorWhenTheQueueIsEmpty(t *testing.T)
func TestTransitionSurfacesTheStateMachinesRejection(t *testing.T)
func TestEveryToolValidatesItsArguments(t *testing.T)
func TestUnknownToolIsRejected(t *testing.T)
```

- [ ] **Steps 2-5:** RED, implement, GREEN, commit.

---

### Task 3: The Meeseeks worker

**Files:** `control-plane/internal/worker/meeseeks.go`, `meeseeks_test.go`

```go
type Outcome string // "no_work" | "completed" | "released" | "escalated"

func (m *Meeseeks) RunOnce(ctx context.Context) (Outcome, error)
```

Exactly §7.1: claim one card, load it and the policy, determine the phase,
perform exactly one workflow step, attach evidence, transition, release, exit.
It must not loop over cards, and must not hold a card across steps.

- [ ] **Step 1: Write the failing tests**

```go
// Spec 7: claim one thing, make it stop being your problem, cease to exist.
func TestAWorkerHandlesExactlyOneCardThenExits(t *testing.T)
func TestNoClaimableWorkExitsCleanlyRatherThanSpinning(t *testing.T)
func TestTheLeaseIsReleasedEvenWhenTheStepFails(t *testing.T)
func TestAnExhaustedLadderSendsTheCardToNeedsHuman(t *testing.T)
func TestEvidenceIsAttachedBeforeTheTransition(t *testing.T)
    // ordering matters: a card must never reach a new state with no evidence.
func TestHeartbeatKeepsTheLeaseAliveDuringALongStep(t *testing.T)
```

- [ ] **Steps 2-5:** RED, implement, GREEN, commit.

---

### Task 4: Chart wiring

**Files:** `charts/strange-company/templates/policy-configmap.yaml`,
`values.yaml`, `values.schema.json`, `control-plane-deployment.yaml`,
`tests/contract_test.yaml`

- Mount `policy/` as a **read-only** ConfigMap at `/policy` (§2.5: models may
  read policy, never silently rewrite it).
- `providers.<name>.credentials` in values maps a provider to an existing
  Secret, so operators bring their own credentials and the chart never invents
  one.
- Unit test: the policy volume is `readOnly: true`.

- [ ] **Steps 1-5:** as above, ending with a green chart run.

---

### Task 5: CI

- [ ] Assert in the chart job that `/policy` is mounted read-only and the
      control plane logs the resolved ladder at boot.
- [ ] Assert the whole M2 suite passes **with no provider credential set**,
      which is the standing §2.4/§26 guarantee.

## Self-Review

**Spec coverage:** §2.3 aliases — Task 1. §2.4 no model calls — Global
Constraints, enforced by the absence of any provider client. §2.5 policy in
files — Tasks 1 and 4. §7 Meeseeks — Task 3. §9 MCP surface — Task 2. §12
ladder — Task 1. §29 least-privilege credentials — Task 1.

**Deferred:** §10 specification gate, §11/§13/§14 coding pipeline and adapters,
§16 Jobs and RBAC, §17-§19 verification/review/merge, §20-§23 artifacts and
cost, §25 GitHub, §26 management bots.

**Open question for the operator, not a blocker:** which provider should
`foreman-cheap` resolve to on first deploy. The policy file needs *a* default;
anything else can be added by editing YAML.
