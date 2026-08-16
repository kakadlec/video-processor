package postgres

import (
	"errors"
	"os"
)

// ErrDSNRequired is returned when no PostgreSQL connection string is
// configured. Startup must fail clearly rather than silently running
// without persistence.
var ErrDSNRequired = errors.New("video: VIDEO_POSTGRES_DSN environment variable is required")

// Config holds PostgreSQL connection configuration.
type Config struct {
	DSN string
}

// LoadConfigFromEnv reads the PostgreSQL connection string from the environment.
func LoadConfigFromEnv() (Config, error) {
	dsn := os.Getenv("VIDEO_POSTGRES_DSN")
	if dsn == "" {
		return Config{}, ErrDSNRequired
	}
	return Config{DSN: dsn}, nil
}
