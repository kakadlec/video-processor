package postgres_test

import (
	"errors"
	"strings"
	"testing"

	"video-processor/internal/notification/infrastructure/postgres"
)

func TestLoadConfigFromEnv_RequiresDSN(t *testing.T) {
	t.Setenv("NOTIFICATION_POSTGRES_DSN", "")

	_, err := postgres.LoadConfigFromEnv()
	if !errors.Is(err, postgres.ErrDSNRequired) {
		t.Fatalf("error = %v, want %v", err, postgres.ErrDSNRequired)
	}
	if !strings.Contains(err.Error(), "NOTIFICATION_POSTGRES_DSN") {
		t.Fatalf("error = %q, want it to name NOTIFICATION_POSTGRES_DSN", err)
	}
}

func TestLoadConfigFromEnv_ReadsDSN(t *testing.T) {
	const dsn = "postgres://user:pass@localhost:5432/notification"
	t.Setenv("NOTIFICATION_POSTGRES_DSN", dsn)

	cfg, err := postgres.LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DSN != dsn {
		t.Fatalf("DSN = %q, want %q", cfg.DSN, dsn)
	}
}

// The context must not borrow another context's connection string: a
// deployment may point all three at one server, but the code may not be the
// thing that assumes it.
func TestLoadConfigFromEnv_DoesNotFallBackToAnotherContextsDSN(t *testing.T) {
	t.Setenv("NOTIFICATION_POSTGRES_DSN", "")
	t.Setenv("IDENTITY_POSTGRES_DSN", "postgres://user:pass@localhost:5432/identity")
	t.Setenv("VIDEO_POSTGRES_DSN", "postgres://user:pass@localhost:5432/video")

	if _, err := postgres.LoadConfigFromEnv(); !errors.Is(err, postgres.ErrDSNRequired) {
		t.Fatalf("error = %v, want %v", err, postgres.ErrDSNRequired)
	}
}
