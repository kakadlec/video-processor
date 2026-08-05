package postgres

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
)

//go:embed schema.sql
var schemaSQL string

// Migrate applies the identity schema. It is idempotent (CREATE TABLE IF NOT
// EXISTS), so it is safe to call on every application startup.
func Migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("identity: migrate schema: %w", err)
	}
	return nil
}
