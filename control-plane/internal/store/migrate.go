package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"strings"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

const migrationsDir = "migrations"

// createMigrationsTableSQL creates the bookkeeping table used to track which
// migrations have already been applied. It is idempotent.
const createMigrationsTableSQL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    text PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT now()
);`

// Migrate applies any embedded migrations that have not yet been recorded in
// the schema_migrations table, in lexical filename order. Each migration file
// is applied together with its bookkeeping INSERT inside a single
// transaction, so a failure partway through a migration leaves no partial
// record of it having been applied.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, createMigrationsTableSQL); err != nil {
		return fmt.Errorf("store: create schema_migrations table: %w", err)
	}

	applied := make(map[string]struct{})
	rows, err := s.pool.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("store: query applied migrations: %w", err)
	}
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			rows.Close()
			return fmt.Errorf("store: scan applied migration version: %w", err)
		}
		applied[version] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("store: iterate applied migrations: %w", err)
	}
	rows.Close()

	entries, err := fs.ReadDir(migrationFiles, migrationsDir)
	if err != nil {
		return fmt.Errorf("store: read embedded migrations: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		version := entry.Name()
		if _, ok := applied[version]; ok {
			continue
		}

		contents, err := fs.ReadFile(migrationFiles, migrationsDir+"/"+version)
		if err != nil {
			return fmt.Errorf("store: read migration %s: %w", version, err)
		}

		if err := s.applyMigration(ctx, version, string(contents)); err != nil {
			return err
		}
	}

	return nil
}

// applyMigration runs a single migration's SQL and records it as applied,
// both inside one transaction.
func (s *Store) applyMigration(ctx context.Context, version, sql string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin transaction for migration %s: %w", version, err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, sql); err != nil {
		return fmt.Errorf("store: apply migration %s: %w", version, err)
	}

	if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
		return fmt.Errorf("store: record migration %s: %w", version, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit migration %s: %w", version, err)
	}

	return nil
}
