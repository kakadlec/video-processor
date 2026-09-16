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
// 0x46494158 is "FIAX" in ASCII, the same class internal/notification's
// Migrate takes its own lock under (object 1 there). This is object 2 under
// that class, not 1: a shared class with distinct objects is what lets two
// contexts take non-conflicting locks without colliding, and this comment
// plus notification's own are the registry.
const (
	schemaMigrationLockClass = 0x46494158
	videoSchemaLockObj       = 2
)

// Migrate applies the video schema under an advisory lock held for the
// length of a transaction.
//
// CREATE TABLE/INDEX IF NOT EXISTS is idempotent once the relation exists,
// but it does not serialize two *first-time* creates: two replicas starting
// together can both find a relation absent, and one then fails on a catalog
// uniqueness violation — which at cmd/video-api or cmd/worker startup means
// a replica that refuses to boot. The lock makes the second one wait and
// then find the relation present.
//
// This mirrors internal/notification's Migrate. CLAUDE.md used to record
// this race as latent here (and in identity's adapter, which still has it);
// this lock is what closes it for video.
//
// schema.sql holds more than one statement, and PostgreSQL runs a
// multi-statement string only over the simple query protocol. pgx forces
// that protocol whenever an Exec carries no arguments, whatever exec mode
// the DSN configures — so the schema below must stay argument-free, and a
// parameter introduced into it would break startup rather than one table.
func Migrate(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("video: begin migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Released by COMMIT or ROLLBACK, so no path can leak it.
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1, $2)",
		schemaMigrationLockClass, videoSchemaLockObj); err != nil {
		return fmt.Errorf("video: acquire migration lock: %w", err)
	}
	if _, err := tx.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("video: migrate schema: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("video: commit migration: %w", err)
	}
	return nil
}
