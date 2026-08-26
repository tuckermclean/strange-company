// Package store provides access to the control plane's PostgreSQL-backed
// persistence layer, including schema migrations and connection management.
package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store wraps a pgx connection pool used to talk to the control-plane
// database.
type Store struct {
	pool *pgxpool.Pool
}

// Open creates a new connection pool for the given DSN and verifies
// connectivity with a Ping before returning.
func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: create pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping database: %w", err)
	}

	return &Store{pool: pool}, nil
}

// Close releases all resources held by the underlying connection pool.
func (s *Store) Close() {
	if s.pool != nil {
		s.pool.Close()
	}
}

// Pool returns the underlying pgx connection pool.
func (s *Store) Pool() *pgxpool.Pool {
	return s.pool
}
