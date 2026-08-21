package ratelimit_test

import (
	"testing"

	"video-processor/internal/platform/ratelimit"
)

func TestLoadConfigFromEnv_DefaultsWhenUnset(t *testing.T) {
	t.Setenv("RATE_LIMIT_MAX_REQUESTS", "")
	t.Setenv("RATE_LIMIT_WINDOW_SECONDS", "")

	cfg, err := ratelimit.LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.MaxRequests != 60 {
		t.Errorf("MaxRequests = %d, want 60", cfg.MaxRequests)
	}
	if cfg.WindowSeconds != 60 {
		t.Errorf("WindowSeconds = %d, want 60", cfg.WindowSeconds)
	}
}

func TestLoadConfigFromEnv_ExplicitValuesOverrideDefaults(t *testing.T) {
	t.Setenv("RATE_LIMIT_MAX_REQUESTS", "10")
	t.Setenv("RATE_LIMIT_WINDOW_SECONDS", "30")

	cfg, err := ratelimit.LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.MaxRequests != 10 {
		t.Errorf("MaxRequests = %d, want 10", cfg.MaxRequests)
	}
	if cfg.WindowSeconds != 30 {
		t.Errorf("WindowSeconds = %d, want 30", cfg.WindowSeconds)
	}
}

func TestLoadConfigFromEnv_MalformedMaxRequestsReturnsError(t *testing.T) {
	t.Setenv("RATE_LIMIT_MAX_REQUESTS", "abc")

	_, err := ratelimit.LoadConfigFromEnv()
	if err == nil {
		t.Fatal("expected an error for a malformed RATE_LIMIT_MAX_REQUESTS value")
	}
}

func TestLoadConfigFromEnv_MalformedWindowSecondsReturnsError(t *testing.T) {
	t.Setenv("RATE_LIMIT_WINDOW_SECONDS", "abc")

	_, err := ratelimit.LoadConfigFromEnv()
	if err == nil {
		t.Fatal("expected an error for a malformed RATE_LIMIT_WINDOW_SECONDS value")
	}
}

func TestLoadConfigFromEnv_ZeroMaxRequestsReturnsError(t *testing.T) {
	t.Setenv("RATE_LIMIT_MAX_REQUESTS", "0")

	_, err := ratelimit.LoadConfigFromEnv()
	if err == nil {
		t.Fatal("expected an error for a zero RATE_LIMIT_MAX_REQUESTS value")
	}
}

func TestLoadConfigFromEnv_NegativeMaxRequestsReturnsError(t *testing.T) {
	t.Setenv("RATE_LIMIT_MAX_REQUESTS", "-1")

	_, err := ratelimit.LoadConfigFromEnv()
	if err == nil {
		t.Fatal("expected an error for a negative RATE_LIMIT_MAX_REQUESTS value")
	}
}

func TestLoadConfigFromEnv_ZeroWindowSecondsReturnsError(t *testing.T) {
	t.Setenv("RATE_LIMIT_WINDOW_SECONDS", "0")

	_, err := ratelimit.LoadConfigFromEnv()
	if err == nil {
		t.Fatal("expected an error for a zero RATE_LIMIT_WINDOW_SECONDS value")
	}
}

func TestLoadConfigFromEnv_NegativeWindowSecondsReturnsError(t *testing.T) {
	t.Setenv("RATE_LIMIT_WINDOW_SECONDS", "-1")

	_, err := ratelimit.LoadConfigFromEnv()
	if err == nil {
		t.Fatal("expected an error for a negative RATE_LIMIT_WINDOW_SECONDS value")
	}
}
