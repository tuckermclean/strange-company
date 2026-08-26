package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// GetServiceCredential returns the secret stored under name in the
// service_credentials table. It returns an empty string and a nil error when
// no credential is stored under that name, so callers can treat "absent" and
// "empty" identically without a separate existence check.
func (s *Store) GetServiceCredential(ctx context.Context, name string) (string, error) {
	var secret string
	err := s.pool.QueryRow(ctx,
		`SELECT secret FROM service_credentials WHERE name = $1`,
		name,
	).Scan(&secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: get service credential %q: %w", name, err)
	}

	return secret, nil
}

// PutServiceCredential upserts the secret and metadata stored under name,
// creating the row if it does not already exist and otherwise overwriting
// its secret, metadata and updated_at. The secret itself is never included
// in any error this function returns.
func (s *Store) PutServiceCredential(ctx context.Context, name, secret string, metadata map[string]any) error {
	if metadata == nil {
		metadata = map[string]any{}
	}

	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("store: marshal metadata for service credential %q: %w", name, err)
	}

	_, err = s.pool.Exec(ctx, `
INSERT INTO service_credentials (name, secret, metadata, updated_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (name) DO UPDATE
SET secret = EXCLUDED.secret,
    metadata = EXCLUDED.metadata,
    updated_at = now()
`, name, secret, metadataJSON)
	if err != nil {
		return fmt.Errorf("store: put service credential %q: %w", name, err)
	}

	return nil
}
