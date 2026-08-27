// Command control-plane is the strange-company control plane's HTTP entry
// point: it loads configuration, opens the database, wires up readiness
// checks for its dependencies, and serves the API until asked to shut down.
package main

import (
	"net/url"
	"strings"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tuckermclean/strange-company/control-plane/internal/config"
	"github.com/tuckermclean/strange-company/control-plane/internal/credentials"
	"github.com/tuckermclean/strange-company/control-plane/internal/github"
	"github.com/tuckermclean/strange-company/control-plane/internal/health"
	"github.com/tuckermclean/strange-company/control-plane/internal/hermes"
	"github.com/tuckermclean/strange-company/control-plane/internal/ingest"
	"github.com/tuckermclean/strange-company/control-plane/internal/policy"
	"github.com/tuckermclean/strange-company/control-plane/internal/promote"
	"github.com/tuckermclean/strange-company/control-plane/internal/providerclient"
	"github.com/tuckermclean/strange-company/control-plane/internal/server"
	"github.com/tuckermclean/strange-company/control-plane/internal/specsession"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
	"github.com/tuckermclean/strange-company/control-plane/internal/vikunja"
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

	// Runs for the lifetime of the process, retrying until Vikunja is reachable.
	go runVikunjaSupervisor(ctx, logger, cfg, st)

	// Screens specifications and opens the human conversation for the ones
	// that need it (spec 10.1, 10.2).
	go runSpecificationSupervisor(ctx, logger, cfg, st, pol)

	// Turns labelled GitHub issues into cards (spec 25).
	go runIngestSupervisor(ctx, logger, cfg, st, pol)

	// Moves approved cards into Ready when the deterministic gate agrees
	// (spec 10.2).
	go runPromotionSupervisor(ctx, logger, cfg, st)

	checks := []health.Checker{
		&postgresChecker{store: st},
		health.HTTPReachable("vikunja", cfg.VikunjaURL+"/api/v1/info", nil),
		health.HTTPReachable("hermes-gateway", cfg.HermesGatewayURL, nil),
	}

	addr := fmt.Sprintf(":%d", cfg.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           server.New(cfg, checks, version).SetCards(st, storeErrorClassifier{}).Handler(),
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
func runVikunjaSupervisor(ctx context.Context, logger *slog.Logger, cfg *config.Config, st *store.Store) {
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
				reconciler = vikunja.NewReconciler(client, board, st, logger)

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
	rec := promote.New(st, specScreeningLimit, log)

	for {
		if res, err := rec.RunOnce(ctx); err != nil {
			log.Warn("promotion pass failed", "error", err)
		} else if res.Promoted+res.Blocked+res.Failed > 0 {
			log.Info("promotion pass",
				"considered", res.Considered, "promoted", res.Promoted,
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
