package postgres_test

import (
	"errors"
	"testing"

	"video-processor/internal/identity/infrastructure/postgres"
)

func TestLoadConfigFromEnv_RequiresDSN(t *testing.T) {
	t.Setenv("IDENTITY_POSTGRES_DSN", "")

	_, err := postgres.LoadConfigFromEnv()
	if !errors.Is(err, postgres.ErrDSNRequired) {
		t.Fatalf("error = %v, want %v", err, postgres.ErrDSNRequired)
	}
}

func TestLoadConfigFromEnv_ReadsDSN(t *testing.T) {
	const dsn = "postgres://user:pass@localhost:5432/db"
	t.Setenv("IDENTITY_POSTGRES_DSN", dsn)

	cfg, err := postgres.LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DSN != dsn {
		t.Fatalf("DSN = %q, want %q", cfg.DSN, dsn)
	}
}
