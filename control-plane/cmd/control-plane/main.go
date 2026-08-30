// Command control-plane is the strange-company control plane's HTTP entry
// point: it loads configuration, opens the database, wires up readiness
// checks for its dependencies, and serves the API until asked to shut down.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/codingrun"
	"github.com/tuckermclean/strange-company/control-plane/internal/config"
	"github.com/tuckermclean/strange-company/control-plane/internal/credentials"
	"github.com/tuckermclean/strange-company/control-plane/internal/decompose"
	"github.com/tuckermclean/strange-company/control-plane/internal/dispatch"
	"github.com/tuckermclean/strange-company/control-plane/internal/ghverify"
	"github.com/tuckermclean/strange-company/control-plane/internal/github"
	"github.com/tuckermclean/strange-company/control-plane/internal/health"
	"github.com/tuckermclean/strange-company/control-plane/internal/hermes"
	"github.com/tuckermclean/strange-company/control-plane/internal/implstep"
	"github.com/tuckermclean/strange-company/control-plane/internal/ingest"
	"github.com/tuckermclean/strange-company/control-plane/internal/kube"
	"github.com/tuckermclean/strange-company/control-plane/internal/mcp"
	"github.com/tuckermclean/strange-company/control-plane/internal/plan"
	"github.com/tuckermclean/strange-company/control-plane/internal/policy"
	"github.com/tuckermclean/strange-company/control-plane/internal/promote"
	"github.com/tuckermclean/strange-company/control-plane/internal/providerclient"
	"github.com/tuckermclean/strange-company/control-plane/internal/reviewstep"
	"github.com/tuckermclean/strange-company/control-plane/internal/server"
	"github.com/tuckermclean/strange-company/control-plane/internal/specapproval"
	"github.com/tuckermclean/strange-company/control-plane/internal/specsession"
	"github.com/tuckermclean/strange-company/control-plane/internal/onboard"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
	"github.com/tuckermclean/strange-company/control-plane/internal/ui"
	"github.com/tuckermclean/strange-company/control-plane/internal/teststep"
	"github.com/tuckermclean/strange-company/control-plane/internal/vikunja"
	"github.com/tuckermclean/strange-company/control-plane/internal/worker"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

// Bounds for the startup database-connection retry loop: Postgres may still
// be starting when this process does (e.g. a fresh Helm install), so boot
// failures there are retried with capped exponential backoff instead of
// crash-looping the pod immediately.
const (
	dbRetryInitialDelay = 1 * time.Second
	dbRetryMaxDelay     = 30 * time.Second
	dbRetryTotalBudget  = 5 * time.Minute
)

// Timeouts for the HTTP server. An explicit *http.Server with these set is
// used instead of http.ListenAndServe(addr, nil), which has no timeouts at
// all and is vulnerable to slow-client (slowloris) attacks.
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 60 * time.Second
	shutdownTimeout   = 15 * time.Second
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		// err already enumerates every missing/invalid variable; print it
		// as-is rather than wrapping it.
		logger.Error(err.Error())
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := openStoreWithRetry(ctx, logger, cfg.DSN())
	if err != nil {
		logger.Error("giving up opening database connection", "error", err)
		os.Exit(1)
	}

	if err := st.Migrate(ctx); err != nil {
		logger.Error("failed to run database migrations", "error", err)
		st.Close()
		os.Exit(1)
	}

	pol := loadPolicy(logger, cfg)

	// Runs for the lifetime of the process, retrying until Vikunja is
	// reachable -- when there is one. An install that has retired the board
	// simply never starts it, rather than retrying forever against nothing.
	if cfg.VikunjaURL != "" {
		go runVikunjaSupervisor(ctx, logger, cfg, st, pol)
	} else {
		logger.Info("no Vikunja configured; the board projection is off and the console at /ui is the surface")
	}

	// Screens specifications and opens the human conversation for the ones
	// that need it (spec 10.1, 10.2).
	go runSpecificationSupervisor(ctx, logger, cfg, st, pol)

	// Turns labelled GitHub issues into cards (spec 25).
	go runIngestSupervisor(ctx, logger, cfg, st, pol)

	// Moves approved cards into Ready when the deterministic gate agrees
	// (spec 10.2).
	go runPromotionSupervisor(ctx, logger, cfg, st)

	// Spawns the short-lived workers that actually do the work (spec 7).
	go runWorkerSupervisor(ctx, logger, cfg, st, pol)

	// Readiness names only what this process actually needs.
	//
	// Vikunja is probed only when one is configured. Leaving it in the list
	// unconditionally meant that retiring the board would fail the readiness
	// probe, Kubernetes would take the pod out of service, and the whole
	// engine would stop -- because a projection nobody had asked for was
	// unreachable.
	checks := []health.Checker{
		&postgresChecker{store: st},
		health.HTTPReachable("hermes-gateway", cfg.HermesGatewayURL, nil),
	}
	if cfg.VikunjaURL != "" {
		checks = append(checks, health.HTTPReachable("vikunja", cfg.VikunjaURL+"/api/v1/info", nil))
	}

	addr := fmt.Sprintf(":%d", cfg.Port)

	api := server.New(cfg, checks, version).
		SetCards(cardStore{st}, storeErrorClassifier{}).
		SetMCP(mcp.NewServer(mcpCards{st}).SetEvidence(st).Handler())

	// Day-0 repository setup, only when a credential carrying GitHub's
	// `workflow` scope has been supplied. Absent, the endpoint says so
	// rather than failing in a way that reads as a bug: writing
	// .github/workflows is more power than anything else here holds, and it
	// should be an explicit choice to grant it.
	if cfg.GitHubDayZeroToken != "" {
		dayZero, err := github.New(cfg.GitHubAPIURL, cfg.GitHubDayZeroToken, nil)
		if err != nil {
			// Not fatal. A misconfigured day-0 credential must not stop the
			// engine running for the repositories already prepared.
			logger.Error("repository import is off; the day-0 credential could not be used", "error", err)
		} else {
			api = api.SetImporter(onboard.New(dayZero, cfg.GitHubIngestLabel, nil, logger))
		}
	} else {
		logger.Info("repository import is off; no day-0 credential with the GitHub workflow scope is configured")
	}

	// The console is not load-bearing: the API and every supervisor work
	// without it, so a template that failed to parse must not stop the
	// engine from running.
	if console, err := ui.New(st, logger); err != nil {
		logger.Error("the operator console will not be served", "error", err)
	} else {
		api = api.SetUI(console.WithDashboard(cfg.HermesDashboardPublicURL).Routes)
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	logger.Info("starting control plane",
		"version", version,
		"addr", addr,
		"config", cfg.Redacted(),
	)

	serveErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutting down", "reason", ctx.Err())
	case err := <-serveErr:
		if err != nil {
			logger.Error("server failed", "error", err)
		}
		st.Close()
		if err != nil {
			os.Exit(1)
		}
		return
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
	} else {
		logger.Info("shutdown complete")
	}

	st.Close()
}

// openStoreWithRetry opens the store, retrying with capped exponential
// backoff for up to dbRetryTotalBudget. This tolerates Postgres still coming
// up (for example right after a fresh Helm install) instead of exiting on
// the first failed connection attempt.
func openStoreWithRetry(ctx context.Context, logger *slog.Logger, dsn string) (*store.Store, error) {
	deadline := time.Now().Add(dbRetryTotalBudget)
	delay := dbRetryInitialDelay

	for attempt := 1; ; attempt++ {
		st, err := store.Open(ctx, dsn)
		if err == nil {
			return st, nil
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("could not open database after %s: %w", dbRetryTotalBudget, err)
		}

		logger.Warn("database not reachable yet, retrying",
			"attempt", attempt,
			"retry_in", delay.String(),
			"error", err,
		)

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}

		delay *= 2
		if delay > dbRetryMaxDelay {
			delay = dbRetryMaxDelay
		}
	}
}

// loadPolicy resolves the model and provider policy and reports what it found.
//
// Policy is loaded at startup rather than lazily so that a malformed file is a
// loud failure an operator sees immediately, not a surprise the first time a
// card escalates. The ladder is logged because "which model runs attempt 4" is
// the question people actually ask, and reading it from a log beats inferring
// it from YAML.
func loadPolicy(logger *slog.Logger, cfg *config.Config) *policy.Policy {
	pol, operatorSupplied, err := policy.LoadOrDefaults(cfg.PolicyDir)
	if err != nil {
		logger.Error("policy is invalid; refusing to start with an unknown escalation ladder",
			"policy_dir", cfg.PolicyDir, "error", err)
		os.Exit(1)
	}

	source := "built-in defaults"
	if operatorSupplied {
		source = cfg.PolicyDir
	}

	var ladder []string
	for attempt := 1; ; attempt++ {
		res, err := pol.Resolve("implementation", attempt)
		if err != nil {
			break
		}
		ladder = append(ladder, fmt.Sprintf("%d=%s/%s", attempt, res.ProviderName, res.Model))
	}
	logger.Info("policy loaded", "source", source, "implementation_ladder", strings.Join(ladder, " "))
	return pol
}

// runVikunjaSupervisor owns everything that talks to Vikunja, and keeps
// retrying until it works.
//
// This deliberately does NOT happen once at boot. The control plane routinely
// starts before Vikunja is listening, and a one-shot bootstrap then fails with
// "connection refused" and never recovers -- which is exactly the bug this
// replaces. Vikunja is a projection, so the same principle the spec applies to
// webhooks applies here: do not assume a delivery worked, reconcile until it has.
//
// Nothing here is fatal. If Vikunja never comes back, the control plane is still
// the source of truth and keeps serving.
func runVikunjaSupervisor(ctx context.Context, logger *slog.Logger, cfg *config.Config, st *store.Store, pol *policy.Policy) {
	const retryInterval = 10 * time.Second

	token := cfg.VikunjaToken
	if token != "" {
		logger.Info("using the supplied Vikunja token")
	}
	if token == "" && (cfg.VikunjaBootstrapUsername == "" || cfg.VikunjaBootstrapPassword == "") {
		logger.Info("no Vikunja credentials configured; board reconciliation disabled")
		return
	}

	var (
		client     *vikunja.Client
		board      *vikunja.Board
		reconciler *vikunja.Reconciler

		// approvals turns a label on the board into a §10.2 approval. It
		// needs the same client, so it appears once the board is ready.
		approvals *specapproval.Reconciler
	)

	for {
		// Each stage is attempted only once the previous one has succeeded, and
		// every stage is retried on its own schedule.
		if token == "" {
			t, err := vikunja.NewBootstrapper(cfg.VikunjaURL, st,
				cfg.VikunjaBootstrapUsername, cfg.VikunjaBootstrapPassword, nil).EnsureToken(ctx)
			if err != nil {
				logger.Warn("waiting to bootstrap a Vikunja token", "error", err)
			} else {
				token = t
				logger.Info("bootstrapped a Vikunja API token")
			}
		}

		if token != "" && client == nil {
			client = vikunja.New(cfg.VikunjaURL, token, nil)
		}

		if client != nil && board == nil {
			b, err := client.EnsureBoard(ctx, vikunjaProjectTitle)
			if err != nil {
				logger.Warn("waiting to prepare the Vikunja board", "error", err)
			} else {
				board = b
				reconciler = vikunja.NewReconciler(client, board, st, logger).WithLadder(pol)

				// Without this the project belongs to the bootstrap
				// user alone and no human can see the cards. Not
				// fatal: an unknown username should leave a working
				// board that one person cannot see, not no board.
				if err := client.EnsureProjectShares(ctx, board.ProjectID,
					cfg.VikunjaBoardShareWith, cfg.VikunjaBoardPermission); err != nil {
					logger.Error("could not share the board; it will only be visible to the bootstrap user",
						"error", err)
				} else if len(cfg.VikunjaBoardShareWith) > 0 {
					logger.Info("board shared",
						"with", cfg.VikunjaBoardShareWith,
						"permission", cfg.VikunjaBoardPermission)
				} else {
					logger.Warn("nobody is shared on the board; set vikunja.board.shareWith or no human will see the cards",
						"project_id", b.ProjectID)
				}

				approvals = specapproval.New(st, client, cfg.SpecApprovalLabel, specScreeningLimit, logger)

				logger.Info("vikunja board ready",
					"project_id", board.ProjectID,
					"kanban_view_id", board.KanbanViewID,
					"buckets", len(board.BucketByState))
			}
		}

		interval := retryInterval
		if reconciler != nil {
			if res, err := reconciler.RunOnce(ctx); err != nil {
				logger.Warn("board reconciliation pass failed", "error", err)
			} else if res.Pushed+res.Accepted+res.Rejected > 0 {
				logger.Info("board reconciled",
					"checked", res.Checked, "pushed", res.Pushed,
					"accepted", res.Accepted, "rejected", res.Rejected)
			}
			interval = cfg.ReconcileInterval
		}

		if approvals != nil {
			if res, err := approvals.RunOnce(ctx); err != nil {
				logger.Warn("approval pass failed", "error", err)
			} else if res.Approved+res.Failed > 0 {
				logger.Info("approvals from the board",
					"approved", res.Approved, "failed", res.Failed)
			}
		}

		select {
		case <-ctx.Done():
			logger.Info("vikunja supervisor stopping")
			return
		case <-time.After(interval):
		}
	}
}

// vikunjaProjectTitle is the single project this control plane owns.
const vikunjaProjectTitle = "strange-company"

// postgresChecker adapts the store's connection pool to health.Checker by
// pinging it directly, which exercises the live pool rather than opening a
// separate connection.
type postgresChecker struct {
	store *store.Store
}

func (c *postgresChecker) Name() string {
	return "postgres"
}

func (c *postgresChecker) Check(ctx context.Context) health.Status {
	now := time.Now()
	if err := c.store.Pool().Ping(ctx); err != nil {
		return health.Status{
			Name:      "postgres",
			OK:        false,
			Detail:    err.Error(),
			CheckedAt: now,
		}
	}
	return health.Status{
		Name:      "postgres",
		OK:        true,
		Detail:    "ok",
		CheckedAt: now,
	}
}

// storeErrorClassifier lets the server map a *store.Store error to an HTTP
// status without the server package importing store (and therefore pgx).
// It exists only to satisfy server.ErrorClassifier by comparing against the
// store package's own sentinel errors with errors.Is.
type storeErrorClassifier struct{}

func (storeErrorClassifier) IsNoWork(err error) bool {
	return errors.Is(err, store.ErrNoWork)
}

func (storeErrorClassifier) IsNotClaimant(err error) bool {
	return errors.Is(err, store.ErrNotClaimant)
}

func (storeErrorClassifier) IsNotFound(err error) bool {
	return errors.Is(err, store.ErrCardNotFound)
}

// specScreeningLimit bounds how many specifications one pass will screen.
//
// Each one is a model call, so this is the difference between a large backlog
// costing a bounded amount per pass and costing whatever the backlog happens
// to be. Cards not reached this pass are reached on the next.
const specScreeningLimit = 10

// runSpecificationSupervisor screens unscreened specifications and opens the
// Hermes conversation for the ones a human has to resolve.
//
// Like the Vikunja supervisor, nothing here is fatal and nothing is done once
// at boot: the gateway is routinely not listening yet, and a specification
// that could not be screened this minute is screened the next.
func runSpecificationSupervisor(ctx context.Context, logger *slog.Logger, cfg *config.Config, st *store.Store, pol *policy.Policy) {
	log := logger.With("supervisor", "specification")

	if cfg.HermesGatewayURL == "" {
		log.Info("no Hermes gateway configured; specification screening disabled")
		return
	}

	// Screening runs on the foreman rung: it is a cheap, high-volume
	// classification, and paying frontier prices to be told a card is
	// mechanical is the failure this tiering exists to avoid.
	screenRes, err := pol.Resolve("foreman", 1)
	if err != nil {
		log.Error("no foreman model in policy; specification screening disabled", "error", err)
		return
	}
	specRes, err := pol.Resolve("specification", 1)
	if err != nil {
		log.Error("no specification model in policy; specification screening disabled", "error", err)
		return
	}

	// Straight to the provider policy named -- NOT through the Hermes
	// gateway. The gateway ignores the requested model and answers from its
	// own global route, so routing this call through it meant policy chose
	// deepseek-v4-flash while claude-opus-4-6 answered, at frontier prices,
	// with the logs reporting the cheap model. See
	// docs/specs/policy-selected-models.md.
	mc, err := providerclient.New(screenRes, credentials.Dir(cfg.CredentialsDir))
	if err != nil {
		// Refused rather than degraded: the alternative is calling some
		// other endpoint and reporting success, which is the bug this
		// replaces.
		log.Error("specification screening disabled: the foreman provider cannot be called",
			"provider", screenRes.ProviderName, "model", screenRes.Model, "error", err)
		return
	}
	gateway, err := hermes.New(cfg.HermesGatewayURL, cfg.HermesAPIKey)
	if err != nil {
		log.Error("could not build a Hermes client; specification screening disabled", "error", err)
		return
	}

	rec := specsession.NewReconciler(
		st,
		specsession.NewModelScreener(mc, screenRes.Model),
		specsession.NewOpener(gateway, st, specRes.Model),
		specScreeningLimit,
		log,
	)

	// The endpoint host is logged so "which model actually answered" is
	// answerable from the startup log alone, which it previously was not.
	log.Info("specification screening enabled",
		"screening_provider", screenRes.ProviderName,
		"screening_model", screenRes.Model,
		"screening_endpoint", endpointHost(screenRes.BaseURL),
		"conversation_model", specRes.Model,
		"conversation_endpoint", endpointHost(cfg.HermesGatewayURL))

	for {
		if res, err := rec.RunOnce(ctx); err != nil {
			log.Warn("specification pass failed", "error", err)
		} else if res.Screened+res.Opened+res.Failed > 0 {
			log.Info("specifications reconciled",
				"screened", res.Screened, "opened", res.Opened, "failed", res.Failed)
		}

		select {
		case <-ctx.Done():
			log.Info("specification supervisor stopping")
			return
		case <-time.After(cfg.ReconcileInterval):
		}
	}
}

// endpointHost reduces a base URL to scheme and host for logging. A provider
// base URL is not a secret, but a path or query on one might carry a token.
func endpointHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(unparseable)"
	}
	return u.Scheme + "://" + u.Host
}

// runIngestSupervisor turns labelled GitHub issues into cards, for the life of
// the process.
//
// Spec §25 wants an eligible issue on the board within 60 seconds, which is
// the reconcile interval. This polls rather than waiting on a webhook because
// §4.3's rule about Vikunja applies here too: do not assume a delivery worked,
// reconcile until it has. A webhook can make this faster later; it cannot make
// it correct, and it would need public ingress this chart does not require.
func runIngestSupervisor(ctx context.Context, logger *slog.Logger, cfg *config.Config, st *store.Store, pol *policy.Policy) {
	log := logger.With("supervisor", "ingest")

	if len(cfg.GitHubRepositories) == 0 {
		log.Info("no GitHub repositories configured; issue ingestion disabled, so the board will only hold cards created by other means")
		return
	}
	if cfg.GitHubToken == "" {
		log.Error("GitHub repositories are configured but no token is set; issue ingestion disabled",
			"repositories", cfg.GitHubRepositories)
		return
	}

	client, err := github.New(cfg.GitHubAPIURL, cfg.GitHubToken, nil)
	if err != nil {
		log.Error("could not build a GitHub client; issue ingestion disabled", "error", err)
		return
	}

	// Every card is stamped with the allowlist at creation. A card without
	// one can never pass 10's gate, so it would sit on the board forever
	// looking like work nobody had got to.
	actions, err := json.Marshal(pol.DefaultPermittedActions())
	if err != nil {
		log.Error("could not render the permitted-actions allowlist; issue ingestion disabled", "error", err)
		return
	}

	rec := ingest.New(client, st, cfg.GitHubRepositories, cfg.GitHubIngestLabel, actions, log)
	log.Info("issue ingestion enabled",
		"repositories", cfg.GitHubRepositories,
		"label", cfg.GitHubIngestLabel,
		"endpoint", endpointHost(cfg.GitHubAPIURL))

	for {
		if res, err := rec.RunOnce(ctx); err != nil {
			log.Warn("ingestion pass failed", "error", err)
		} else if res.Created+res.Failed > 0 {
			log.Info("issues ingested",
				"seen", res.Seen, "created", res.Created,
				"updated", res.Updated, "failed", res.Failed)
		}

		select {
		case <-ctx.Done():
			log.Info("ingest supervisor stopping")
			return
		case <-time.After(cfg.ReconcileInterval):
		}
	}
}

// runPromotionSupervisor moves approved cards into Ready.
//
// Spec §10.2 makes approval the human input and promotion the control plane's
// consequence of it, which is why this is a supervisor rather than an
// endpoint: an endpoint that promoted directly would be a way around the
// deterministic gate.
func runPromotionSupervisor(ctx context.Context, logger *slog.Logger, cfg *config.Config, st *store.Store) {
	log := logger.With("supervisor", "promotion")
	rec := promote.New(st, specScreeningLimit, log).
		WithAutoApproval(cfg.Autonomy == config.AutonomyAutoApproveSpecs)

	for {
		if res, err := rec.RunOnce(ctx); err != nil {
			log.Warn("promotion pass failed", "error", err)
		} else if res.Promoted+res.Blocked+res.Failed+res.Approved > 0 {
			log.Info("promotion pass",
				"considered", res.Considered, "approved", res.Approved, "promoted", res.Promoted,
				"blocked", res.Blocked, "failed", res.Failed)
		}

		select {
		case <-ctx.Done():
			log.Info("promotion supervisor stopping")
			return
		case <-time.After(cfg.ReconcileInterval):
		}
	}
}

// cardStore adapts *store.Store to server.CardStore.
//
// Only the list endpoints need adapting: the server declares its own Artifact
// and Attempt so that package never imports the concrete storage engine, which
// is the same reason CardStore itself is declared there rather than here.
type cardStore struct{ *store.Store }

func (c cardStore) ListArtifacts(ctx context.Context, cardID uuid.UUID) ([]server.Artifact, error) {
	stored, err := c.Store.ListArtifacts(ctx, cardID)
	if err != nil {
		return nil, err
	}
	out := make([]server.Artifact, 0, len(stored))
	for _, a := range stored {
		out = append(out, server.Artifact{
			ID: a.ID.String(), Type: a.Type, Actor: a.Actor, Model: a.Model,
			CommitSHA: a.CommitSHA, ContentType: a.ContentType, StorageURI: a.StorageURI,
			Content: a.Content, SHA256: a.SHA256, SizeBytes: a.SizeBytes, Truncated: a.Truncated,
		})
	}
	return out, nil
}

func (c cardStore) ListAttempts(ctx context.Context, cardID uuid.UUID) ([]server.Attempt, error) {
	stored, err := c.Store.ListAttempts(ctx, cardID)
	if err != nil {
		return nil, err
	}
	out := make([]server.Attempt, 0, len(stored))
	for _, a := range stored {
		out = append(out, server.Attempt{
			ID: a.ID, RunID: a.RunID, Phase: a.Phase, Number: a.AttemptNumber,
			ModelAlias: a.ModelAlias, Provider: a.Provider, Harness: a.Harness, Model: a.Model,
			Status: string(a.Status), CountedAsAttempt: a.CountedAsAttempt, Summary: a.Summary,
			InputTokens: a.InputTokens, OutputTokens: a.OutputTokens, CachedTokens: a.CachedTokens,
			CostUSD: a.CostUSD, DurationMS: a.DurationMS,
			StartedAt: a.StartedAt, CreatedAt: a.CreatedAt,
		})
	}
	return out, nil
}

func (c cardStore) ListHistory(ctx context.Context, cardID uuid.UUID, limit int) ([]server.HistoryEntry, error) {
	stored, err := c.Store.ListHistory(ctx, cardID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]server.HistoryEntry, 0, len(stored))
	for _, e := range stored {
		out = append(out, server.HistoryEntry{
			At: e.At, From: e.From, To: e.To,
			ActorType: e.ActorType, ActorID: e.ActorID, Reason: e.Reason,
		})
	}
	return out, nil
}

// workerCards adapts *store.Store to worker.CardStore.
//
// Two things need translating and both are load-bearing: store.ErrNoWork must
// become worker.ErrNoWork (the worker package declares its own so it never
// imports the storage engine), and worker.Evidence must become the store's,
// which is where §21's "a card never arrives in a new state unexplained"
// actually lands.
type workerCards struct{ *store.Store }

func (w workerCards) ClaimReady(ctx context.Context, workerID string, lease time.Duration) (*card.Card, error) {
	c, err := w.Store.ClaimReady(ctx, workerID, lease)
	if errors.Is(err, store.ErrNoWork) {
		return nil, worker.ErrNoWork
	}
	return c, err
}

func (w workerCards) AttachEvidence(ctx context.Context, cardID uuid.UUID, ev worker.Evidence) error {
	return w.Store.AttachEvidence(ctx, cardID, store.CardEvidence{
		ActorID: "meeseeks",
		Summary: ev.Summary,
		Detail:  ev.Detail,
	})
}

// runWorkerSupervisor spawns one short-lived Meeseeks per tick.
//
// §7: "Claim one thing. Make the thing stop being your problem. Cease to
// exist." Each RunOnce claims at most one card, performs exactly one workflow
// step, and exits -- so a card moving through planning, tests and
// implementation is carried by a succession of workers, never one.
func runWorkerSupervisor(ctx context.Context, logger *slog.Logger, cfg *config.Config, st *store.Store, pol *policy.Policy) {
	log := logger.With("supervisor", "worker")

	// Every step this control plane knows how to run. A phase absent here
	// sends its card to a human rather than being retried forever; see
	// internal/dispatch.
	steps := map[card.Phase]worker.Step{
		card.PhaseDecomposition: decompose.New(st, st, st, func(res *policy.Resolution) (decompose.Completer, error) {
			return providerclient.New(res, credentials.Dir(cfg.CredentialsDir))
		}, log),
		card.PhasePlanning: plan.New(st, st, func(res *policy.Resolution) (plan.Completer, error) {
			return providerclient.New(res, credentials.Dir(cfg.CredentialsDir))
		}, log),
	}

	// The coding phases need somewhere to run. Both values are required
	// rather than defaulted: without a namespace there is nowhere to put a
	// Job, and there is no sensible default runner image -- a wrong one
	// fails at pod start rather than at configuration time.
	switch {
	case cfg.AgentRunsNamespace == "" || cfg.RunnerImage == "":
		log.Warn("coding phases disabled: set controlPlane.agentRuns and the runner image",
			"agent_runs_namespace", cfg.AgentRunsNamespace, "runner_image", cfg.RunnerImage)
	default:
		kc, err := kube.InCluster(cfg.ServiceAccountDir)
		if err != nil {
			// Not fatal. Everything up to Ready still works, and a
			// board that fills but does not build is far better than a
			// control plane that will not start.
			log.Error("coding phases disabled: no Kubernetes access", "error", err)
			break
		}
		runs := codingrun.New(kc, cfg.AgentRunsNamespace, cfg.RunnerImage, codingRunPoll, log)

		// §18's review and the GitHub Actions gate both need a client.
		// Without a token a reviewed card would pass review and have
		// nowhere to open the pull request §19 requires, so the review
		// phase stays absent and the dispatcher sends such a card to a
		// human instead.
		var gh *github.Client
		if cfg.GitHubToken != "" {
			if gh, err = github.New(cfg.GitHubAPIURL, cfg.GitHubToken, nil); err != nil {
				log.Error("could not build a GitHub client", "error", err)
				gh = nil
			}
		}

		// Which backend answers §11.3 and §19. Reading CI is the default:
		// a repository with workflows has already declared its tests, and
		// a second declaration in the repository is the one that drifts.
		var verifier teststep.Verifier = runs
		switch {
		case cfg.VerificationMode == config.VerificationTestCommand:
			log.Info("verification reads " + codingrun.TestCommandPath)
		case gh != nil:
			verifier = ghverify.New(gh, checksPoll, checksWait, log)
			log.Info("verification reads GitHub Actions checks")
		default:
			log.Warn("verification falls back to " + codingrun.TestCommandPath +
				": GitHub Actions was asked for but there is no GitHub token")
		}

		// The credential a coding Job pushes its agent branch with. Without
		// it the Job clones and never pushes: no agent branch, no checks
		// on it, and the red gate has nothing to compare.
		git := codingrun.GitIdentity{
			Username: cfg.GitUsername, AuthorName: cfg.GitAuthorName, AuthorEmail: cfg.GitAuthorEmail,
		}
		if cfg.GitTokenSecret != "" && cfg.GitTokenKey != "" {
			git.Token = &policy.CredentialRef{Secret: cfg.GitTokenSecret, Key: cfg.GitTokenKey}
		} else {
			log.Warn("no git credential configured for coding Jobs; agents will clone but cannot push their branch",
				"set", "controlPlane.gitCredential")
		}

		steps[card.PhaseTests] = teststep.New(st, st, st, runs, verifier, git, log)
		steps[card.PhaseImplementation] = implstep.New(st, st, st, runs, verifier, git, log)
		log.Info("coding phases enabled",
			"namespace", cfg.AgentRunsNamespace, "image", cfg.RunnerImage)

		if gh == nil {
			log.Warn("review phase disabled: no GitHub client")
			break
		}
		steps[card.PhaseReview] = reviewstep.New(st, st, st, gh, func(res *policy.Resolution) (reviewstep.Completer, error) {
			return providerclient.New(res, credentials.Dir(cfg.CredentialsDir))
		}, log)
		log.Info("review phase enabled")
	}
	step := dispatch.New(steps, log)

	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "control-plane"
	}

	log.Info("worker supervisor running", "phases_implemented", len(steps))

	for n := 0; ; n++ {
		id := fmt.Sprintf("meeseeks-%s-%d", host, n)
		outcome, err := worker.New(id, workerCards{st}, pol, step, log, workerLease).RunOnce(ctx)
		switch {
		case err != nil:
			log.Warn("worker exited with an error", "worker", id, "outcome", outcome, "error", err)
		case outcome != worker.OutcomeNoWork:
			log.Info("worker finished", "worker", id, "outcome", outcome)
		}

		// A worker that found work looks again immediately: a board with a
		// queue should drain at the speed of the work, not the tick.
		delay := cfg.ReconcileInterval
		if outcome != worker.OutcomeNoWork && err == nil {
			delay = 0
		}

		select {
		case <-ctx.Done():
			log.Info("worker supervisor stopping")
			return
		case <-time.After(delay):
		}
	}
}

// workerLease is how long a Meeseeks holds a card before another may reclaim
// it. Long enough for a model call, short enough that a dead worker does not
// strand a card for an hour.
const workerLease = 10 * time.Minute

// codingRunPoll is how often a running coding Job's status is checked. Coding
// runs take minutes, so a tighter poll only adds API calls.
const codingRunPoll = 10 * time.Second

// checksPoll and checksWait bound how long a card waits on CI. A workflow run
// takes minutes; waiting past checksWait leaves the outcome incomplete, which
// the gate reads as inconclusive rather than as a failure of the work.
const (
	checksPoll = 15 * time.Second
	checksWait = 20 * time.Minute
)

// mcpCards adapts *store.Store to mcp.CardService. Only ClaimReady needs
// translating: the MCP package declares its own ErrNoWork so it never imports
// the storage engine.
type mcpCards struct{ *store.Store }

func (m mcpCards) ClaimReady(ctx context.Context, workerID string, lease time.Duration) (*card.Card, error) {
	c, err := m.Store.ClaimReady(ctx, workerID, lease)
	if errors.Is(err, store.ErrNoWork) {
		return nil, mcp.ErrNoWork
	}
	return c, err
}
