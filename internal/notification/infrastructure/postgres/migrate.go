package postgres

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
)

//go:embed schema.sql
var schemaSQL string

// Advisory-lock key this context owns, as a two-int key rather than one
// bigint so the space stays allocatable instead of magic: the class names a
// purpose every context shares, the object names the context.
//
// 0x46494158 is "FIAX" in ASCII, distinctive enough not to collide with
// another application sharing the server. Nothing else in this repository
// takes an advisory lock today; a second context adding one takes a new
// object id under the same class, and this comment is the registry.
const (
	schemaMigrationLockClass  = 0x46494158
	notificationSchemaLockObj = 1
)

// Migrate applies the notification schema under an advisory lock held for
// the length of a transaction.
//
// CREATE TABLE IF NOT EXISTS is idempotent once the table exists, but it
// does not serialize two *first-time* creates: two replicas starting
// together can both find the table absent, and one then fails on a catalog
// uniqueness violation — which at cmd/api startup means a replica that
// refuses to boot. The lock makes the second one wait and then find the
// table present.
//
// This is deliberately stricter than the identity and video adapters, which
// take no lock. That is a latent race in those two rather than a reason to
// copy them.
//
// schema.sql holds more than one statement, and PostgreSQL runs a
// multi-statement string only over the simple query protocol. pgx forces
// that protocol whenever an Exec carries no arguments, whatever exec mode
// the DSN configures — so the schema below must stay argument-free, and a
// parameter introduced into it would break startup rather than one table.
func Migrate(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("notification: begin migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Released by COMMIT or ROLLBACK, so no path can leak it.
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1, $2)",
		schemaMigrationLockClass, notificationSchemaLockObj); err != nil {
		return fmt.Errorf("notification: acquire migration lock: %w", err)
	}
	if _, err := tx.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("notification: migrate schema: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("notification: commit migration: %w", err)
	}
	return nil
}
