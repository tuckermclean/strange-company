// Command control-plane is the strange-company control plane's HTTP entry
// point: it loads configuration, opens the database, wires up readiness
// checks for its dependencies, and serves the API until asked to shut down.
package main

import (
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
	"github.com/tuckermclean/strange-company/control-plane/internal/server"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
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
