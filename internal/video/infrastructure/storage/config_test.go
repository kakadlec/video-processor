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
	t.Setenv("VIDEO_MINIO_PUBLIC_ENDPOINT", "")
	t.Setenv("VIDEO_MINIO_PUBLIC_USE_SSL", "")
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
	t.Setenv("VIDEO_MINIO_PUBLIC_ENDPOINT", "storage.example:9000")
	t.Setenv("VIDEO_MINIO_PUBLIC_USE_SSL", "true")

	cfg, err := storage.LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := storage.Config{
		Endpoint:       "minio:9000",
		AccessKey:      "the-access-key",
		SecretKey:      "the-secret-key",
		Bucket:         "the-bucket",
		UseSSL:         false,
		PublicEndpoint: "storage.example:9000",
		PublicUseSSL:   true,
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

// TestLoadConfigFromEnv_PublicEndpointDefaultsToInternal covers the shape
// most deployments have: one address reachable from both the server and its
// clients, declared once.
func TestLoadConfigFromEnv_PublicEndpointDefaultsToInternal(t *testing.T) {
	setAllRequired(t)

	cfg, err := storage.LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.PublicEndpoint != cfg.Endpoint {
		t.Fatalf("PublicEndpoint = %q, want it to default to Endpoint %q", cfg.PublicEndpoint, cfg.Endpoint)
	}
}

func TestLoadConfigFromEnv_PublicEndpointHonored(t *testing.T) {
	setAllRequired(t)
	t.Setenv("VIDEO_MINIO_PUBLIC_ENDPOINT", "downloads.example.com")

	cfg, err := storage.LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.PublicEndpoint != "downloads.example.com" {
		t.Fatalf("PublicEndpoint = %q, want %q", cfg.PublicEndpoint, "downloads.example.com")
	}
	if cfg.Endpoint != "localhost:9000" {
		t.Fatalf("Endpoint = %q, want the public value not to have overwritten it", cfg.Endpoint)
	}
}

// The default the naive implementation gets wrong: PublicUseSSL follows the
// *resolved* UseSSL, not false. Defaulting to false would silently sign
// http:// URLs for a deployment that declared TLS once and reasonably
// expected it to apply to both audiences.
func TestLoadConfigFromEnv_PublicUseSSLDefaultsToInternalUseSSL(t *testing.T) {
	setAllRequired(t)
	t.Setenv("VIDEO_MINIO_USE_SSL", "true")

	cfg, err := storage.LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.PublicUseSSL {
		t.Fatal("PublicUseSSL = false, want it to follow UseSSL when VIDEO_MINIO_PUBLIC_USE_SSL is unset")
	}
}

// The two are independent when both are declared: TLS terminates in front of
// the browser-facing address while the server talks plaintext inside its own
// network. Asserting the direction that is not merely "both true".
func TestLoadConfigFromEnv_PublicUseSSLIndependentOfInternal(t *testing.T) {
	setAllRequired(t)
	t.Setenv("VIDEO_MINIO_USE_SSL", "false")
	t.Setenv("VIDEO_MINIO_PUBLIC_USE_SSL", "true")

	cfg, err := storage.LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.UseSSL {
		t.Fatal("UseSSL = true, want false")
	}
	if !cfg.PublicUseSSL {
		t.Fatal("PublicUseSSL = false, want true")
	}
}

func TestLoadConfigFromEnv_PublicUseSSLRejectsMalformedValue(t *testing.T) {
	setAllRequired(t)
	t.Setenv("VIDEO_MINIO_PUBLIC_USE_SSL", "ture")

	cfg, err := storage.LoadConfigFromEnv()
	if err == nil {
		t.Fatalf("expected an error, got cfg = %+v", cfg)
	}
	if !strings.Contains(err.Error(), "VIDEO_MINIO_PUBLIC_USE_SSL") {
		t.Fatalf("error %q does not name the variable", err)
	}
	if cfg != (storage.Config{}) {
		t.Fatalf("cfg = %+v, want the zero Config alongside the error", cfg)
	}
}
