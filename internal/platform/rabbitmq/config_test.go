package rabbitmq_test

import (
	"errors"
	"os"
	"testing"

	"video-processor/internal/platform/rabbitmq"
)

func TestLoadConfigFromEnv_MissingURLReturnsError(t *testing.T) {
	// t.Setenv first so the original value is restored on cleanup, then
	// unset to exercise the genuinely-absent case.
	t.Setenv("RABBITMQ_URL", "placeholder")
	if err := os.Unsetenv("RABBITMQ_URL"); err != nil {
		t.Fatalf("unset RABBITMQ_URL: %v", err)
	}

	cfg, err := rabbitmq.LoadConfigFromEnv()
	if !errors.Is(err, rabbitmq.ErrURLRequired) {
		t.Fatalf("expected ErrURLRequired, got %v", err)
	}
	if cfg != (rabbitmq.Config{}) {
		t.Fatalf("expected a zero Config alongside the error, got %+v", cfg)
	}
}

func TestLoadConfigFromEnv_EmptyURLReturnsError(t *testing.T) {
	t.Setenv("RABBITMQ_URL", "")

	cfg, err := rabbitmq.LoadConfigFromEnv()
	if !errors.Is(err, rabbitmq.ErrURLRequired) {
		t.Fatalf("expected ErrURLRequired for a set-but-empty value, got %v", err)
	}
	if cfg != (rabbitmq.Config{}) {
		t.Fatalf("expected a zero Config alongside the error, got %+v", cfg)
	}
}

func TestLoadConfigFromEnv_URLIsLoaded(t *testing.T) {
	const url = "amqp://video:video@broker:5672/"
	t.Setenv("RABBITMQ_URL", url)

	cfg, err := rabbitmq.LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("LoadConfigFromEnv: %v", err)
	}
	if cfg.URL != url {
		t.Fatalf("Config.URL = %q, want %q", cfg.URL, url)
	}
}
