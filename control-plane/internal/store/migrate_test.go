package store

import (
	"context"
	"os"
	"testing"
	"time"
)

// openTestStore returns a Store connected to the database identified by the
// TEST_DATABASE_DSN environment variable. If that variable is not set, the
// test is skipped so this suite runs cleanly in environments without a
// database available. The schema is dropped and recreated (empty) before the
// Store is returned so that each test starts from a clean, independent
// state.
func openTestStore(t *testing.T) *Store {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("TEST_DATABASE_DSN not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open(%q) returned error %v, want nil", dsn, err)
	}
	t.Cleanup(s.Close)

	dropCtx, dropCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer dropCancel()

	// Reset by rebuilding the schema rather than dropping a hardcoded table
	// list: with a list, every new migration silently breaks this helper and
	// the failure shows up as "relation already exists" in unrelated tests.
	const dropSQL = `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`
	if _, err := s.Pool().Exec(dropCtx, dropSQL); err != nil {
		t.Fatalf("failed to reset schema before test: got error %v, want nil", err)
	}

	return s
}

func TestMigrateCreatesTheCardTables(t *testing.T) {
	s := openTestStore(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() returned error %v, want nil", err)
	}

	wantTables := []string{
		"cards",
		"acceptance_criteria",
		"card_dependencies",
		"card_history",
		"schema_migrations",
	}

	for _, table := range wantTables {
		queryCtx, queryCancel := context.WithTimeout(context.Background(), 10*time.Second)
		var found string
		err := s.Pool().QueryRow(queryCtx,
			`SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1`,
			table,
		).Scan(&found)
		queryCancel()
		if err != nil {
			t.Errorf("table %q: query returned error %v, want table to exist", table, err)
			continue
		}
		if found != table {
			t.Errorf("table %q: got %q from information_schema.tables, want %q", table, found, table)
		}
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := openTestStore(t)

	firstCtx, firstCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer firstCancel()
	if err := s.Migrate(firstCtx); err != nil {
		t.Fatalf("first Migrate() returned error %v, want nil", err)
	}

	countCtx, countCancel := context.WithTimeout(context.Background(), 10*time.Second)
	var firstCount int
	if err := s.Pool().QueryRow(countCtx, `SELECT count(*) FROM schema_migrations`).Scan(&firstCount); err != nil {
		countCancel()
		t.Fatalf("counting schema_migrations after first Migrate() returned error %v, want nil", err)
	}
	countCancel()

	secondCtx, secondCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer secondCancel()
	if err := s.Migrate(secondCtx); err != nil {
		t.Fatalf("second Migrate() returned error %v, want nil", err)
	}

	countCtx2, countCancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer countCancel2()
	var secondCount int
	if err := s.Pool().QueryRow(countCtx2, `SELECT count(*) FROM schema_migrations`).Scan(&secondCount); err != nil {
		t.Fatalf("counting schema_migrations after second Migrate() returned error %v, want nil", err)
	}

	if secondCount != firstCount {
		t.Errorf("schema_migrations row count after second Migrate() = %d, want %d (unchanged)", secondCount, firstCount)
	}
}

func TestClaimableIndexExists(t *testing.T) {
	s := openTestStore(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() returned error %v, want nil", err)
	}

	queryCtx, queryCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer queryCancel()

	var indexName string
	err := s.Pool().QueryRow(queryCtx,
		`SELECT indexname FROM pg_indexes WHERE schemaname = 'public' AND indexname = $1`,
		"cards_claimable_idx",
	).Scan(&indexName)
	if err != nil {
		t.Fatalf("querying pg_indexes for cards_claimable_idx returned error %v, want index to exist", err)
	}

	if indexName != "cards_claimable_idx" {
		t.Errorf("got index name %q, want %q", indexName, "cards_claimable_idx")
	}
}
