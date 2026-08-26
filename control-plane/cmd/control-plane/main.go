// Command control-plane is the strange-company control plane's HTTP entry
// point: it loads configuration, opens the database, wires up readiness
// checks for its dependencies, and serves the API until asked to shut down.
package main

import (
	"strings"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tuckermclean/strange-company/control-plane/internal/config"
	"github.com/tuckermclean/strange-company/control-plane/internal/health"
	"github.com/tuckermclean/strange-company/control-plane/internal/policy"
	"github.com/tuckermclean/strange-company/control-plane/internal/server"
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
	_ = pol // consumed by the worker in M3; loaded now so bad policy fails visibly at boot

	// Runs for the lifetime of the process, retrying until Vikunja is reachable.
	go runVikunjaSupervisor(ctx, logger, cfg, st)

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
