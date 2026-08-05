package postgres

import (
	"database/sql"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Open returns a database handle configured from cfg. It does not establish
// a connection or apply the schema — callers are responsible for verifying
// connectivity (e.g. db.PingContext) and calling Migrate during startup.
func Open(cfg Config) (*sql.DB, error) {
	db, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("identity: open postgres: %w", err)
	}
	return db, nil
}
