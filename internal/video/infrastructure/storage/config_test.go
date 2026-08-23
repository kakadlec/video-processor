package storage_test

import (
	"errors"
	"strings"
	"testing"

	"video-processor/internal/video/infrastructure/storage"
)

// setAllRequired populates every required variable so a test can then clear
// or override exactly the one it cares about.
func setAllRequired(t *testing.T) {
	t.Helper()
	t.Setenv("VIDEO_MINIO_ENDPOINT", "localhost:9000")
	t.Setenv("VIDEO_MINIO_ACCESS_KEY", "access")
	t.Setenv("VIDEO_MINIO_SECRET_KEY", "secret")
	t.Setenv("VIDEO_MINIO_BUCKET", "bucket")
	t.Setenv("VIDEO_MINIO_USE_SSL", "")
}

func TestLoadConfigFromEnv_RequiresEachVariable(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		wantErr error
	}{
		{"endpoint", "VIDEO_MINIO_ENDPOINT", storage.ErrEndpointRequired},
		{"access key", "VIDEO_MINIO_ACCESS_KEY", storage.ErrAccessKeyRequired},
		{"secret key", "VIDEO_MINIO_SECRET_KEY", storage.ErrSecretKeyRequired},
		{"bucket", "VIDEO_MINIO_BUCKET", storage.ErrBucketRequired},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setAllRequired(t)
			t.Setenv(tt.env, "")

			_, err := storage.LoadConfigFromEnv()
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadConfigFromEnv_ReadsAllValues(t *testing.T) {
	t.Setenv("VIDEO_MINIO_ENDPOINT", "minio:9000")
	t.Setenv("VIDEO_MINIO_ACCESS_KEY", "the-access-key")
	t.Setenv("VIDEO_MINIO_SECRET_KEY", "the-secret-key")
	t.Setenv("VIDEO_MINIO_BUCKET", "the-bucket")
	t.Setenv("VIDEO_MINIO_USE_SSL", "")

	cfg, err := storage.LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := storage.Config{
		Endpoint:  "minio:9000",
		AccessKey: "the-access-key",
		SecretKey: "the-secret-key",
		Bucket:    "the-bucket",
		UseSSL:    false,
	}
	if cfg != want {
		t.Fatalf("cfg = %+v, want %+v", cfg, want)
	}
}

func TestLoadConfigFromEnv_UseSSLDefaultsFalse(t *testing.T) {
	setAllRequired(t)

	cfg, err := storage.LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.UseSSL {
		t.Fatal("UseSSL = true, want false when VIDEO_MINIO_USE_SSL is unset")
	}
}

// Covers more than the literal "true": ParseBool accepts several spellings,
// and an operator writing any of them means TLS.
func TestLoadConfigFromEnv_UseSSLHonored(t *testing.T) {
	for _, raw := range []string{"true", "TRUE", "True", "t", "T", "1"} {
		t.Run(raw, func(t *testing.T) {
			setAllRequired(t)
			t.Setenv("VIDEO_MINIO_USE_SSL", raw)

			cfg, err := storage.LoadConfigFromEnv()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !cfg.UseSSL {
				t.Fatalf("UseSSL = false, want true for %q", raw)
			}
		})
	}

	for _, raw := range []string{"false", "FALSE", "f", "0"} {
		t.Run(raw, func(t *testing.T) {
			setAllRequired(t)
			t.Setenv("VIDEO_MINIO_USE_SSL", raw)

			cfg, err := storage.LoadConfigFromEnv()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.UseSSL {
				t.Fatalf("UseSSL = true, want false for %q", raw)
			}
		})
	}
}

// A malformed value must not silently mean "no TLS": that would turn a
// deployment typo into a plaintext connection to an endpoint the operator
// configured believing it was TLS-protected.
func TestLoadConfigFromEnv_UseSSLRejectsMalformedValue(t *testing.T) {
	for _, raw := range []string{"ture", "yes", "on", "true "} {
		t.Run(raw, func(t *testing.T) {
			setAllRequired(t)
			t.Setenv("VIDEO_MINIO_USE_SSL", raw)

			cfg, err := storage.LoadConfigFromEnv()
			if err == nil {
				t.Fatalf("expected an error for %q, got cfg = %+v", raw, cfg)
			}
			if !strings.Contains(err.Error(), "VIDEO_MINIO_USE_SSL") {
				t.Fatalf("error %q does not name the variable", err)
			}
			if !strings.Contains(err.Error(), raw) {
				t.Fatalf("error %q does not name the offending value %q", err, raw)
			}
			if cfg != (storage.Config{}) {
				t.Fatalf("cfg = %+v, want the zero Config alongside the error", cfg)
			}
		})
	}
}
