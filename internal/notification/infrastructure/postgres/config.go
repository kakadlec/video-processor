package postgres

import (
	"errors"
	"os"
)

// ErrDSNRequired is returned when no PostgreSQL connection string is
// configured. Startup must fail clearly rather than silently running
// without persistence.
var ErrDSNRequired = errors.New("notification: NOTIFICATION_POSTGRES_DSN environment variable is required")

// Config holds PostgreSQL connection configuration.
type Config struct {
	DSN string
}

// LoadConfigFromEnv reads the PostgreSQL connection string from the
// environment. It reads only this context's own variable: IDENTITY_POSTGRES_DSN
// and VIDEO_POSTGRES_DSN are never a fallback, because a shared server is a
// deployment decision and must not become an assumption in the code.
func LoadConfigFromEnv() (Config, error) {
	dsn := os.Getenv("NOTIFICATION_POSTGRES_DSN")
	if dsn == "" {
		return Config{}, ErrDSNRequired
	}
	return Config{DSN: dsn}, nil
}
