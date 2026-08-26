# Control Plane M0 + M1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development
> (recommended) or superpowers:executing-plans to implement this plan task-by-task.
> Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Complete Milestone 0 (a real control-plane service replacing the CI
fixture) and Milestone 1 (the deterministic board: canonical card schema, state
machine, immutable history, atomic claiming with leases, Vikunja sync).

**Architecture:** A single Go binary. HTTP handlers are thin; all workflow
invariants live in PostgreSQL — legal transitions are enforced in code against a
table-driven state machine, and claiming is a `FOR UPDATE SKIP LOCKED`
transaction so correctness under concurrency is the database's job, not the
model's. Vikunja is treated as a projection: webhooks are hints, a 60-second
reconciler is the source of truth.

**Tech Stack:** Go 1.23, stdlib `net/http` (Go 1.22 method+pattern routing, no
web framework), `jackc/pgx/v5` + `pgxpool`, embedded SQL migrations, distroless
static base image, GitHub Actions with a pinned `postgres:17.11` service
container.

**Spec:** `docs/specs/strange-company-control-plane-v1.md`
**Substrate:** `docs/specs/strange-company-helm-chart.md` (already delivered)

## Global Constraints

- Deterministic software answers deterministic questions. **No model call is
  permitted** anywhere in M0 or M1 (spec §2.4).
- The control plane owns canonical execution state; Vikunja owns human-visible
  presentation (spec §4.3). Linked by `vikunja_task_id`.
- **Webhooks are hints, not the source of truth.** Reconciliation interval
  default **60 seconds** (spec §4.1). No valid state change may be permanently
  lost because a webhook failed.
- Claiming MUST be safe with **at least ten simultaneous claim attempts** against
  a single Ready card; **exactly one caller receives the card** (spec §6).
- Lease default **10 minutes**; heartbeat **once per minute**; a lease may be
  reclaimed only after expiry (spec §6).
- Every state transition produces an **immutable** record with timestamp,
  card_id, from, to, actor_type, actor_id, reason, run_id, evidence (spec §21).
- Board columns: `Backlog Ready InProgress Review Done Blocked NeedsHuman`.
  Phases: `specification planning tests implementation verification review complete`.
- **Automated review can never move a card to Done** (spec §18). In M1 this means
  `Review → Done` requires `actor_type = human`.
- Do not log API keys; do not expose model reasoning (spec §31).
- The container must satisfy the chart's existing securityContext: non-root
  **uid 65532**, `readOnlyRootFilesystem: true`, all capabilities dropped.
- Config contract variables are fixed by the chart and must not be renamed:
  `DATABASE_HOST DATABASE_PORT DATABASE_NAME DATABASE_USER DATABASE_PASSWORD`
  `VIKUNJA_URL VIKUNJA_TOKEN HERMES_GATEWAY_URL HERMES_API_KEY`
  and optional `HERMES_DASHBOARD_URL`.
- Probe paths: the chart probes `/healthz` and `/readyz`; spec §30 names
  `/health` and `/ready`. Serve **all four**; `/health` and `/healthz` are
  aliases, as are `/ready` and `/readyz`.

## File Structure

```
control-plane/
├── go.mod
├── cmd/control-plane/main.go        # wiring only: config → deps → server → signals
├── internal/config/config.go        # contract parsing + validation
├── internal/config/config_test.go
├── internal/health/checks.go        # dependency reachability probes
├── internal/health/checks_test.go
├── internal/server/server.go        # routes, middleware, JSON helpers
├── internal/server/health.go        # /healthz /readyz /health /ready /config /version
├── internal/server/cards.go         # M1 card endpoints
├── internal/server/server_test.go
├── internal/store/migrations/*.sql  # embedded, forward-only
├── internal/store/migrate.go        # tiny migrator + schema_migrations table
├── internal/store/store.go          # pgxpool wiring
├── internal/store/cards.go          # claim / heartbeat / release / transition
├── internal/store/cards_test.go     # integration, real postgres
├── internal/card/state.go           # state machine: legal transitions, phases
├── internal/card/state_test.go      # pure unit tests, no database
├── internal/vikunja/client.go       # task create/update, /api/v1/info
├── internal/vikunja/reconcile.go    # 60s loop
└── Dockerfile

.github/workflows/control-plane.yml  # vet, unit, integration(postgres), image build+push
```

Rationale: `internal/card` is pure logic with no I/O so the state machine is
testable without a database; `internal/store` owns every SQL statement so the
claim transaction lives in exactly one place; `internal/server` never contains
business rules.

---

## PHASE M0 — Control-plane skeleton

### Task 1: Config contract parsing and validation

**Files:**
- Create: `control-plane/go.mod`, `control-plane/internal/config/config.go`
- Test: `control-plane/internal/config/config_test.go`

**Interfaces:**
- Produces:
  - `type Config struct { DatabaseHost string; DatabasePort int; DatabaseName, DatabaseUser, DatabasePassword string; VikunjaURL, VikunjaToken, HermesGatewayURL, HermesAPIKey, HermesDashboardURL string; Port int; ReconcileInterval time.Duration }`
  - `func Load(getenv func(string) string) (*Config, error)`
  - `func (c *Config) DSN() string`
  - `func (c *Config) Redacted() map[string]string`

- [ ] **Step 1: Write the failing test**

```go
func TestLoadRequiresDatabaseHost(t *testing.T) {
    env := map[string]string{"DATABASE_PORT": "5432"}
    _, err := Load(func(k string) string { return env[k] })
    if err == nil || !strings.Contains(err.Error(), "DATABASE_HOST") {
        t.Fatalf("want error naming DATABASE_HOST, got %v", err)
    }
}

func TestRedactedNeverLeaksSecrets(t *testing.T) {
    env := map[string]string{
        "DATABASE_HOST": "pg", "DATABASE_PORT": "5432", "DATABASE_NAME": "sc",
        "DATABASE_USER": "sc", "DATABASE_PASSWORD": "hunter2",
        "VIKUNJA_URL": "http://v:3456", "VIKUNJA_TOKEN": "tok",
        "HERMES_GATEWAY_URL": "http://h:8642", "HERMES_API_KEY": "key",
    }
    c, err := Load(func(k string) string { return env[k] })
    if err != nil { t.Fatal(err) }
    for k, v := range c.Redacted() {
        if v == "hunter2" || v == "tok" || v == "key" {
            t.Fatalf("%s leaked a secret value", k)
        }
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd control-plane && go test ./internal/config/`
Expected: FAIL — `undefined: Load`

- [ ] **Step 3: Write minimal implementation**

`Load` reads each variable via the injected `getenv`, collects the names of all
missing required variables, and returns a single error listing them all (so an
operator fixes them in one pass rather than one per restart). `VIKUNJA_TOKEN`
and `HERMES_API_KEY` are **optional** — a fresh install legitimately has no
Vikunja token (spec §33 / chart NOTES). `HERMES_DASHBOARD_URL` is optional.
`Redacted()` returns every value but replaces any secret with `"***"` when
non-empty and `"(unset)"` when empty.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd control-plane && go test ./internal/config/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add control-plane/go.mod control-plane/internal/config
git commit -m "feat(control-plane): parse and validate the configuration contract"
```

---

### Task 2: Dependency health checks

**Files:**
- Create: `control-plane/internal/health/checks.go`
- Test: `control-plane/internal/health/checks_test.go`

**Interfaces:**
- Consumes: `config.Config`
- Produces:
  - `type Status struct { Name string; OK bool; Detail string; CheckedAt time.Time }`
  - `type Checker interface { Name() string; Check(ctx context.Context) Status }`
  - `func HTTPReachable(name, rawURL string, client *http.Client) Checker`
  - `func Aggregate(ctx context.Context, checks []Checker) (ready bool, statuses []Status)`

- [ ] **Step 1: Write the failing test**

Any HTTP response at all means reachable — including 401 and 404. The Hermes
gateway requires an API key and will legitimately reject an unauthenticated
probe; that still proves the socket and the process are alive, and it makes **no
model request** (spec §44 forbids requiring one).

```go
func TestHTTPReachableTreats401AsReachable(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusUnauthorized)
    }))
    defer srv.Close()
    got := HTTPReachable("hermes", srv.URL, srv.Client()).Check(context.Background())
    if !got.OK { t.Fatalf("401 must count as reachable, got %+v", got) }
}

func TestHTTPReachableFailsWhenConnectionRefused(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
    url := srv.URL
    srv.Close() // nothing is listening now
    got := HTTPReachable("hermes", url, http.DefaultClient).Check(context.Background())
    if got.OK { t.Fatal("closed listener must not be reachable") }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd control-plane && go test ./internal/health/`
Expected: FAIL — `undefined: HTTPReachable`

- [ ] **Step 3: Write minimal implementation**

`HTTPReachable` issues a `GET` with a 5s timeout; a transport error is `OK:false`
with the error string as `Detail`; any status code is `OK:true` with
`Detail: "HTTP <code>"`. `Aggregate` runs all checks concurrently and returns
`ready == true` only when every check is OK.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd control-plane && go test ./internal/health/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add control-plane/internal/health
git commit -m "feat(control-plane): dependency reachability checks"
```

---

### Task 3: HTTP server, probes and redacted config endpoint

**Files:**
- Create: `control-plane/internal/server/server.go`, `control-plane/internal/server/health.go`
- Test: `control-plane/internal/server/server_test.go`

**Interfaces:**
- Consumes: `config.Config`, `health.Checker`, `health.Aggregate`
- Produces:
  - `func New(cfg *config.Config, checks []health.Checker, version string) *Server`
  - `func (s *Server) Handler() http.Handler`

Routes: `GET /healthz`, `GET /health`, `GET /readyz`, `GET /ready`,
`GET /config`, `GET /version`.

- [ ] **Step 1: Write the failing test**

Liveness must not depend on dependencies — otherwise a Vikunja restart makes
Kubernetes kill a perfectly healthy control plane.

```go
func TestHealthzIsAliveEvenWhenDependenciesAreDown(t *testing.T) {
    s := New(testConfig(), []health.Checker{alwaysFailing{}}, "test")
    rec := httptest.NewRecorder()
    s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
    if rec.Code != 200 { t.Fatalf("liveness must not depend on dependencies, got %d", rec.Code) }
}

func TestReadyzReports503WhenADependencyIsDown(t *testing.T) {
    s := New(testConfig(), []health.Checker{alwaysFailing{}}, "test")
    rec := httptest.NewRecorder()
    s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
    if rec.Code != 503 { t.Fatalf("want 503, got %d", rec.Code) }
}

func TestConfigEndpointRedactsSecrets(t *testing.T) {
    s := New(testConfig(), nil, "test")
    rec := httptest.NewRecorder()
    s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/config", nil))
    if strings.Contains(rec.Body.String(), "hunter2") { t.Fatal("/config leaked the database password") }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd control-plane && go test ./internal/server/`
Expected: FAIL — `undefined: New`

- [ ] **Step 3: Write minimal implementation**

`/healthz` and `/health` always return `200 {"status":"ok"}`. `/readyz` and
`/ready` run `health.Aggregate` and return `200` or `503` with the per-dependency
status list as JSON. `/config` returns `cfg.Redacted()`. `/version` returns the
build version. Use `http.NewServeMux` with `"GET /healthz"` style patterns.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd control-plane && go test ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add control-plane/internal/server
git commit -m "feat(control-plane): probes, redacted config and version endpoints"
```

---

### Task 4: main, Dockerfile, and the image workflow

**Files:**
- Create: `control-plane/cmd/control-plane/main.go`, `control-plane/Dockerfile`
- Create: `.github/workflows/control-plane.yml`

- [ ] **Step 1: Write `main.go`**

Load config (exit non-zero with the full list of missing variables), build the
three checkers (postgres, vikunja, hermes gateway), start the server on
`:8080`, and shut down gracefully on `SIGTERM` with a 15s drain.

- [ ] **Step 2: Write the Dockerfile**

```dockerfile
FROM golang:1.23-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/control-plane ./cmd/control-plane

FROM gcr.io/distroless/static-debian12:nonroot
USER 65532:65532
COPY --from=build /out/control-plane /control-plane
EXPOSE 8080
ENTRYPOINT ["/control-plane"]
```

uid 65532 is `nonroot` in distroless and matches the chart's
`controlPlane.podSecurityContext.runAsUser`. A static binary needs no writable
filesystem, satisfying `readOnlyRootFilesystem: true`.

- [ ] **Step 3: Write the workflow**

Jobs: `test` (`go vet ./...`, `go test -race ./...`) and `image`
(build, then push to `ghcr.io/tuckermclean/strange-company-control-plane`
tagged with the short SHA and, on tags, the semver). Push only on `main` and tags.

- [ ] **Step 4: Verify in CI**

Expected: both jobs green; the package appears under the account.

- [ ] **Step 5: Commit**

```bash
git add control-plane .github/workflows/control-plane.yml
git commit -m "feat(control-plane): entrypoint, container image and CI"
```

---

### Task 5: Point the chart at the real image and prove it in the cluster

**Files:**
- Modify: `charts/strange-company/values.yaml` (`controlPlane.image.tag`)
- Modify: `.github/workflows/helm.yml` (new job `integration-real-control-plane`)

- [ ] **Step 1: Add a CI job that installs the chart with the real image**

Reuse the batteries fixture but override `controlPlane.image` with the freshly
built tag. Assert the deployment becomes Available and that `/readyz` returns
200 from inside the cluster — which is a stronger claim than `whoami` ever made,
because readiness now means *the control plane actually reached Postgres,
Vikunja and the Hermes gateway*.

```bash
kubectl -n strange-company run probe --rm -i --restart=Never --image=busybox:1.37.0 -- \
  wget -q -O- http://strange-company-control-plane:8080/readyz
```

- [ ] **Step 2: Keep `whoami` as the default CI fixture**

The existing `integration` job stays on `whoami` so chart-only changes do not
depend on the application image building. Only the new job uses the real image.

- [ ] **Step 3: Commit**

```bash
git add charts/strange-company/values.yaml .github/workflows/helm.yml
git commit -m "ci: install the chart with the real control-plane image"
```

**M0 exit:** all services reachable and healthy, with readiness proving real
dependency connectivity rather than a fixture returning 200 to everything.

---

## PHASE M1 — Deterministic board

### Task 6: Schema, embedded migrations and the migrator

**Files:**
- Create: `control-plane/internal/store/migrations/0001_cards.sql`
- Create: `control-plane/internal/store/migrate.go`, `control-plane/internal/store/store.go`
- Test: `control-plane/internal/store/migrate_test.go`

**Interfaces:**
- Produces: `func Open(ctx context.Context, dsn string) (*Store, error)`,
  `func (s *Store) Migrate(ctx context.Context) error`, `func (s *Store) Close()`

`0001_cards.sql` creates:

```sql
CREATE TABLE cards (
    id                     uuid PRIMARY KEY,
    vikunja_task_id        bigint UNIQUE,
    title                  text NOT NULL,
    source_type            text NOT NULL,
    source_url             text,
    source_external_id     text,
    repo_url               text,
    repo_base_ref          text,
    branch                 text,
    state                  text NOT NULL,
    phase                  text NOT NULL,
    spec_uri               text,
    plan_uri               text,
    risk_class             text NOT NULL DEFAULT 'R1',
    effective_priority     integer NOT NULL DEFAULT 100,
    claimed_by             text,
    lease_expires_at       timestamptz,
    implementation_attempt integer NOT NULL DEFAULT 0,
    infrastructure_failures integer NOT NULL DEFAULT 0,
    max_cost_usd           numeric(12,4),
    cost_usd               numeric(12,4) NOT NULL DEFAULT 0,
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE acceptance_criteria (
    id           text NOT NULL,
    card_id      uuid NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
    text         text NOT NULL,
    verification text NOT NULL,
    PRIMARY KEY (card_id, id)
);

CREATE TABLE card_dependencies (
    card_id    uuid NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
    depends_on uuid NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
    PRIMARY KEY (card_id, depends_on)
);

-- Immutable: no UPDATE or DELETE is ever issued against this table (spec §21).
CREATE TABLE card_history (
    id          bigserial PRIMARY KEY,
    card_id     uuid NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
    at          timestamptz NOT NULL DEFAULT now(),
    from_state  text,
    to_state    text NOT NULL,
    actor_type  text NOT NULL,
    actor_id    text NOT NULL,
    reason      text,
    run_id      text,
    evidence    jsonb
);

CREATE INDEX cards_claimable_idx
    ON cards (effective_priority, created_at)
    WHERE state = 'Ready' AND claimed_by IS NULL;
```

The partial index matches the claim query's `WHERE` exactly, so claiming stays
an index scan as the board grows.

- [ ] **Step 1: Write the failing test** — `Migrate` is idempotent:

```go
func TestMigrateIsIdempotent(t *testing.T) {
    s := openTestStore(t)           // skips unless TEST_DATABASE_DSN is set
    if err := s.Migrate(context.Background()); err != nil { t.Fatal(err) }
    if err := s.Migrate(context.Background()); err != nil { t.Fatalf("second run must be a no-op: %v", err) }
}
```

- [ ] **Step 2: Run it and watch it fail** — `undefined: Open`
- [ ] **Step 3: Implement** — `schema_migrations(version text primary key, applied_at timestamptz)`;
      apply each embedded file in lexical order inside one transaction, skipping
      already-recorded versions.
- [ ] **Step 4: Run against the CI postgres service** — PASS
- [ ] **Step 5: Commit**

```bash
git add control-plane/internal/store
git commit -m "feat(control-plane): card schema and embedded migrations"
```

---

### Task 7: The state machine

**Files:**
- Create: `control-plane/internal/card/state.go`
- Test: `control-plane/internal/card/state_test.go`

**Interfaces:**
- Produces:
  - `type State string`, `type Phase string`, `type ActorType string`
  - consts `Backlog Ready InProgress Review Done Blocked NeedsHuman`
  - consts `ActorHuman ActorAgent ActorSystem`
  - `func CanTransition(from, to State, actor ActorType) error`

- [ ] **Step 1: Write the failing tests**

```go
func TestAutomatedReviewCannotCompleteACard(t *testing.T) {
    if err := CanTransition(Review, Done, ActorAgent); err == nil {
        t.Fatal("spec §18: automated review must never move a card to Done")
    }
    if err := CanTransition(Review, Done, ActorHuman); err != nil {
        t.Fatalf("a human may complete a card: %v", err)
    }
}

func TestBacklogCannotJumpStraightToInProgress(t *testing.T) {
    if err := CanTransition(Backlog, InProgress, ActorSystem); err == nil {
        t.Fatal("a card must pass through Ready, which requires a spec")
    }
}

func TestRejectionReturnsAReviewedCardToReady(t *testing.T) {
    if err := CanTransition(Review, Ready, ActorHuman); err != nil { t.Fatal(err) }
}
```

- [ ] **Step 2: Run and watch fail** — `undefined: CanTransition`
- [ ] **Step 3: Implement** as a `map[State]map[State][]ActorType` table. Any pair
      absent from the table is illegal. `Blocked` and `NeedsHuman` are reachable
      from every active state; leaving them requires `ActorHuman`.
- [ ] **Step 4: Run** — PASS
- [ ] **Step 5: Commit**

```bash
git add control-plane/internal/card
git commit -m "feat(control-plane): card state machine with actor-aware transitions"
```

---

### Task 8: Atomic claiming, leases, heartbeat and release

**Files:**
- Create: `control-plane/internal/store/cards.go`
- Test: `control-plane/internal/store/cards_test.go`

**Interfaces:**
- Produces:
  - `func (s *Store) ClaimReady(ctx context.Context, workerID string, lease time.Duration) (*card.Card, error)` — returns `(nil, ErrNoWork)` when nothing is claimable
  - `func (s *Store) Heartbeat(ctx context.Context, cardID uuid.UUID, workerID string, lease time.Duration) error`
  - `func (s *Store) Release(ctx context.Context, cardID uuid.UUID, workerID, reason string) error`
  - `func (s *Store) Transition(ctx context.Context, cardID uuid.UUID, to card.State, actor card.ActorType, actorID, reason string) error`
  - `var ErrNoWork, ErrNotClaimant, ErrIllegalTransition error`

- [ ] **Step 1: Write the failing exit test — this is the milestone gate**

```go
func TestTenConcurrentWorkersClaimExactlyOnce(t *testing.T) {
    s := openTestStore(t)
    id := seedReadyCard(t, s)

    const workers = 10
    var wg sync.WaitGroup
    results := make(chan error, workers)
    start := make(chan struct{})

    for i := 0; i < workers; i++ {
        wg.Add(1)
        go func(n int) {
            defer wg.Done()
            <-start // release all ten at once
            _, err := s.ClaimReady(context.Background(), fmt.Sprintf("meeseeks-%d", n), 10*time.Minute)
            results <- err
        }(i)
    }
    close(start)
    wg.Wait()
    close(results)

    var won, empty int
    for err := range results {
        switch {
        case err == nil:            won++
        case errors.Is(err, ErrNoWork): empty++
        default:                    t.Fatalf("unexpected error: %v", err)
        }
    }
    if won != 1 { t.Fatalf("spec §6: exactly one caller must win, got %d", won) }
    if empty != workers-1 { t.Fatalf("the other %d must get ErrNoWork, got %d", workers-1, empty) }

    // and the win is recorded immutably
    if n := historyCount(t, s, id, "Ready", "InProgress"); n != 1 {
        t.Fatalf("want exactly 1 history row for the claim, got %d", n)
    }
}

func TestExpiredLeaseIsReclaimable(t *testing.T) {
    s := openTestStore(t)
    seedReadyCard(t, s)
    if _, err := s.ClaimReady(ctx, "first", -1*time.Minute); err != nil { t.Fatal(err) } // already expired
    if _, err := s.ClaimReady(ctx, "second", 10*time.Minute); err != nil {
        t.Fatalf("an expired lease must be reclaimable: %v", err)
    }
}

func TestHeartbeatFromNonClaimantIsRejected(t *testing.T) {
    s := openTestStore(t)
    id := seedReadyCard(t, s)
    if _, err := s.ClaimReady(ctx, "owner", 10*time.Minute); err != nil { t.Fatal(err) }
    if err := s.Heartbeat(ctx, id, "impostor", 10*time.Minute); !errors.Is(err, ErrNotClaimant) {
        t.Fatalf("want ErrNotClaimant, got %v", err)
    }
}
```

- [ ] **Step 2: Run and watch fail** — `undefined: ClaimReady`

- [ ] **Step 3: Implement the claim transaction**

```sql
SELECT id FROM cards
WHERE state = 'Ready'
  AND (claimed_by IS NULL OR lease_expires_at < now())
ORDER BY effective_priority, created_at
FOR UPDATE SKIP LOCKED
LIMIT 1;
```

then `UPDATE` state/claimed_by/lease_expires_at and `INSERT` the history row, all
in one `pgx.Tx`. `SKIP LOCKED` is what makes nine of ten callers see no row
rather than block — do not replace it with `NOWAIT` or an advisory lock.

- [ ] **Step 4: Run** — PASS, including `-race`
- [ ] **Step 5: Commit**

```bash
git add control-plane/internal/store/cards.go control-plane/internal/store/cards_test.go
git commit -m "feat(control-plane): atomic claiming with leases and immutable history"
```

---

### Task 9: Card HTTP endpoints

**Files:**
- Create: `control-plane/internal/server/cards.go`
- Test: extend `control-plane/internal/server/server_test.go`

Routes: `GET /cards`, `GET /cards/{id}`, `POST /cards/{id}/claim`,
`POST /cards/{id}/heartbeat`, `POST /cards/{id}/release`,
`POST /cards/{id}/transition`.

- [ ] **Step 1: Write the failing test** — an illegal transition is a `409`,
      not a `500`, and the body names the rule that rejected it.
- [ ] **Step 2: Run and watch fail**
- [ ] **Step 3: Implement** — handlers translate `ErrIllegalTransition → 409`,
      `ErrNotClaimant → 403`, `ErrNoWork → 204`, unknown card → `404`.
- [ ] **Step 4: Run** — PASS
- [ ] **Step 5: Commit**

---

### Task 10: Vikunja projection and the 60-second reconciler

**Files:**
- Create: `control-plane/internal/vikunja/client.go`, `control-plane/internal/vikunja/reconcile.go`
- Test: `control-plane/internal/vikunja/reconcile_test.go`

**Interfaces:**
- Produces: `func New(baseURL, token string, c *http.Client) *Client`,
  `func (c *Client) UpsertTask(ctx, card) (taskID int64, err error)`,
  `func (r *Reconciler) RunOnce(ctx context.Context) (changed int, err error)`

- [ ] **Step 1: Write the failing test** — the reconciler repairs a dropped
      webhook, which is the entire reason it exists (spec §4.1):

```go
func TestReconcilerRepairsAMissedWebhook(t *testing.T) {
    // Vikunja says the card sits in Done; the control plane never heard about it.
    // RunOnce must notice the divergence and validate it through the state
    // machine before accepting it as canonical.
    ...
    if changed != 1 { t.Fatalf("want 1 repaired card, got %d", changed) }
}

func TestReconcilerRejectsAnIllegalHumanMove(t *testing.T) {
    // A human dragging Backlog straight to Done in the Vikunja UI must not
    // become canonical; the card is pushed back and flagged.
}
```

- [ ] **Step 2–4:** watch fail, implement, watch pass.
- [ ] **Step 5: Commit**

```bash
git add control-plane/internal/vikunja
git commit -m "feat(control-plane): Vikunja projection and 60s reconciler"
```

---

### Task 11: Integration CI with a real PostgreSQL

**Files:**
- Modify: `.github/workflows/control-plane.yml`

- [ ] **Step 1: Add a `postgres:17.11` service container** and run the store and
      vikunja suites with `TEST_DATABASE_DSN` set. Same major/minor as the chart
      bundles, so the claim semantics are tested against the version that ships.
- [ ] **Step 2: Assert the milestone gate explicitly** — run
      `go test -race -run TestTenConcurrentWorkersClaimExactlyOnce ./internal/store/ -v`
      as its own step so the M1 exit criterion appears as a named green check.
- [ ] **Step 3: Commit**

**M1 exit:** ten workers race for one Ready card; exactly one gets it — proven by
a named CI step, against the same PostgreSQL version the chart deploys.

---

## Self-Review

**Spec coverage (M0/M1 scope only).** §2.4 no-model-calls — Global Constraints,
enforced by there being no model client in the module. §4.1 reconciliation —
Task 10. §4.2 columns / §4.3 ownership split — Tasks 6, 7, 10. §5 card schema —
Task 6. §6 atomic claiming — Task 8. §21 audit log — Tasks 6 and 8. §30 API —
Tasks 3 and 9. §34 M0/M1 exits — Tasks 5 and 11.

Deliberately **out of scope** here and deferred to their own plans: §9 MCP
server (M2), §11–§14 coding pipeline and adapters (M3–M5), §16 Kubernetes Jobs
and the RBAC the chart currently withholds (M3), §18–§19 review and merge (M6),
§22–§23 cost ledger and budgets (M5), §25 GitHub integration (M6), §26
management bots (M7), §28 network policy (hardening pass).

**Placeholder scan.** No TBDs. The two open questions are recorded below as
decisions to make, not as gaps in a task.

**Type consistency.** `card.State`, `card.ActorType` and `ErrNoWork` /
`ErrNotClaimant` / `ErrIllegalTransition` are defined in Tasks 7–8 and used with
those exact names in Tasks 9–10. `config.Config` field names match Task 1.


## Status as of 2026-08-26 (overnight run)

Branch `feat/control-plane-m0`. Every claim below is backed by a CI job, not by
inspection.

| Task | State | Evidence |
|---|---|---|
| 1 config contract | DONE | `ok internal/config` |
| 2 dependency checks | DONE | unit job green |
| 3 probes / config / version | DONE | unit job green |
| 4 entrypoint + image | DONE | image job builds; publishes on main and tags only |
| 5 chart with the real image | DONE | `chart with the real control-plane image` green: `/readyz` returned ready with postgres, vikunja and hermes-gateway all reachable |
| 6 schema + migrator | DONE | integration job vs postgres:17.11 |
| 7 state machine | DONE | unit job green |
| 8 atomic claiming | DONE | **`--- PASS: TestTenConcurrentWorkersClaimExactlyOnce`** |
| 9 card endpoints | DONE | unit job green |
| 10a Vikunja bootstrap | DONE | proven end to end: CI asserts the control plane minted and stored its own token against real Vikunja 2.5.0 |
| 10 reconciler | DONE | unit green; board creation asserted against real Vikunja |
| 11 integration CI | DONE | named M1 gate step |

**M0 exit: met.** All services reachable and healthy, and readiness now means the
control plane actually reached PostgreSQL, Vikunja and the Hermes gateway --
a stronger claim than the `whoami` fixture could make.

**M1 exit: met.** Ten workers race, exactly one wins, proven against the same
PostgreSQL minor version the chart ships. Vikunja synchronisation is in place:
the board is built from the seven states, and a human move in the UI is
validated as a human transition before it becomes canonical -- an illegal move
is pushed back rather than silently accepted.

### Bugs caught by CI and by reading upstream, not by review

1. **A card with an expired lease was unreachable forever.** The claim predicate
   filtered on `state = 'Ready'`, but claiming sets the state to `InProgress` --
   so once a worker died, nothing could ever pick that card up again. Since a
   Meeseeks is *expected* to die, that is the normal recovery path, not an edge
   case. Fixed by making the second branch explicit and adding an index for it.
2. **`expires_at` is required when minting a Vikunja token.** Upstream marks it
   `valid:"required"` in `pkg/models/api_tokens.go`; the first implementation
   omitted it and would have been rejected by a real server.
3. **Login cannot tell "no such user" from "wrong password".** Vikunja returns an
   identical 403/1011 for both, deliberately, and hashes a dummy value so timing
   cannot separate them either. The first implementation branched on 400/412 and
   would therefore never have registered the account on first boot. It now
   attempts registration on any auth failure and reports plainly if the account
   already exists.
4. **Setting `bucket_id` on a task update is a silent no-op.** `Task.BucketID` is
   `xorm:"-"`, so it never persists despite appearing in `colsToUpdate`. Moving a
   task between columns must use the bucket-tasks relation endpoint.
5. **The spec's webhook claim was wrong.** It says delivery is one-shot with no
   retry; upstream retries five times with backoff. The conclusion survives for a
   different reason: the pub/sub is in-memory, so nothing survives a restart.

## Decisions made 2026-08-26

1. **Naming: `strange-company` throughout.** The spec's `mechanical-company`
   namespace is superseded. No chart, artifact or Flux churn.
2. **The control plane provisions its own Vikunja token** (see Task 10a). It is
   stored in the control plane's own PostgreSQL, **not** a Kubernetes Secret, so
   this needs no new RBAC and the chart keeps `automountServiceAccountToken:
   false` on the control plane.

---

### Task 10a: Vikunja bootstrap — the control plane mints its own bot token

**Files:**
- Create: `control-plane/internal/vikunja/bootstrap.go`
- Create: `control-plane/internal/store/migrations/0002_service_credentials.sql`
- Test: `control-plane/internal/vikunja/bootstrap_test.go`

**Interfaces:**
- Produces: `func (b *Bootstrapper) EnsureToken(ctx context.Context) (string, error)`

**Verified upstream flow** (read from `go-vikunja/vikunja`, `veans/README.md` and
`veans/internal/client/`, 2026-08-26). Vikunja has a first-class **bot user**:
no password, no email, cannot log in interactively. A long-lived API token is
minted *for* that bot by its owner.

```
POST /api/v1/register   {username,email,password}   # first boot only, if absent
POST /api/v1/login      {username,password}         # transient human-ish session (JWT)
GET  /api/v1/routes                                 # discover real permission groups
PUT  /api/v1/tokens     {title, owner_id, permissions, expires_at}
```

Sequence on start-up:

1. Read the stored token from `service_credentials`. If present and a probe call
   succeeds, use it and stop — boot is idempotent.
2. Otherwise `POST /login` with `VIKUNJA_BOOTSTRAP_USERNAME` /
   `VIKUNJA_BOOTSTRAP_PASSWORD` (chart-generated, preserved across upgrades
   exactly like the PostgreSQL password). On "user does not exist", `POST
   /register` first, then log in.
3. Ensure the board project exists and create the buckets from spec §4.2:
   `Backlog Ready InProgress Review Done Blocked NeedsHuman`.
4. Create the bot user if absent, and share the project with it read+write.
5. `GET /routes`, then `PUT /tokens` with `owner_id` set to the bot and
   permissions **intersected with the route groups the server actually exposes**
   — never a hard-coded scope list, which is how upstream avoids drift.
6. Persist the bot token in `service_credentials`; discard the login JWT.

- [ ] **Step 1: Write the failing test** against an `httptest` server that
      mimics the four endpoints, asserting (a) a second `EnsureToken` performs
      **no** write calls because the stored token still validates, and (b) the
      requested permissions are a subset of what `GET /routes` returned.

```go
func TestEnsureTokenIsIdempotent(t *testing.T) {
    fake := newFakeVikunja(t)          // records every method+path
    b := New(fake.URL, store, creds)
    if _, err := b.EnsureToken(ctx); err != nil { t.Fatal(err) }
    fake.reset()
    if _, err := b.EnsureToken(ctx); err != nil { t.Fatal(err) }
    if n := fake.writeCalls(); n != 0 {
        t.Fatalf("second boot must not mint a second token, saw %d writes", n)
    }
}

func TestRequestedScopesNeverExceedServerRoutes(t *testing.T) {
    fake := newFakeVikunja(t)
    fake.routes = map[string]any{"tasks": ..., "labels": ...}   // no "teams"
    b := New(fake.URL, store, creds)
    if _, err := b.EnsureToken(ctx); err != nil { t.Fatal(err) }
    for group := range fake.lastTokenRequest.Permissions {
        if _, ok := fake.routes[group]; !ok {
            t.Fatalf("requested scope %q that this server does not expose", group)
        }
    }
}
```

- [ ] **Step 2: Run and watch fail** — `undefined: EnsureToken`
- [ ] **Step 3: Implement**, with `0002_service_credentials.sql`:

```sql
CREATE TABLE service_credentials (
    name       text PRIMARY KEY,       -- e.g. 'vikunja_bot_token'
    secret     text NOT NULL,
    metadata   jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
```

- [ ] **Step 4: Run** — PASS
- [ ] **Step 5: Chart change** — add `vikunja.bootstrap.username` (default
      `strange-company-bootstrap`) and a generated, upgrade-stable
      `vikunja.bootstrap.password`, surfaced to the control plane as
      `VIKUNJA_BOOTSTRAP_USERNAME` / `VIKUNJA_BOOTSTRAP_PASSWORD`. `VIKUNJA_TOKEN`
      stays supported and, when set, short-circuits the whole bootstrap.
- [ ] **Step 6: Commit**

```bash
git add control-plane/internal/vikunja control-plane/internal/store/migrations
git commit -m "feat(control-plane): provision the Vikunja bot token on first boot"
```

**Constraint worth stating plainly:** registration must be enabled on the Vikunja
instance for step 2's first boot (it is by default). If an operator has disabled
it, they set `VIKUNJA_TOKEN` explicitly and the bootstrap is skipped entirely.
