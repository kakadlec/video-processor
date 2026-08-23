// Package storage holds the Video Processing context's MinIO connection
// plumbing: configuration, client construction, a health check, and bucket
// provisioning. The StoragePort adapter built on top of it is added by a
// later change and lives here too, mirroring how
// internal/video/infrastructure/postgres keeps config.go/db.go beside
// repository.go.
//
// The package deliberately exposes no Close/teardown function: minio-go's
// *minio.Client has no teardown method and keeps its HTTP transport
// unexported, so a wrapper could only report success while releasing
// nothing.
package storage

import (
	"errors"
	"fmt"
	"os"
	"strconv"
)

const (
	envPrefix = "VIDEO_MINIO_"

	endpointEnv  = envPrefix + "ENDPOINT"
	accessKeyEnv = envPrefix + "ACCESS_KEY"
	secretKeyEnv = envPrefix + "SECRET_KEY"
	bucketEnv    = envPrefix + "BUCKET"
	useSSLEnv    = envPrefix + "USE_SSL"
)

// ErrEndpointRequired, ErrAccessKeyRequired, ErrSecretKeyRequired, and
// ErrBucketRequired are returned when their environment variable is unset or
// empty, so a caller can tell which one is missing.
var (
	ErrEndpointRequired  = errors.New("video: " + endpointEnv + " environment variable is required")
	ErrAccessKeyRequired = errors.New("video: " + accessKeyEnv + " environment variable is required")
	ErrSecretKeyRequired = errors.New("video: " + secretKeyEnv + " environment variable is required")
	ErrBucketRequired    = errors.New("video: " + bucketEnv + " environment variable is required")
)

// Config holds the MinIO connection settings.
type Config struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	UseSSL    bool
}

// LoadConfigFromEnv reads the VIDEO_MINIO_* variables. The first four are
// required; VIDEO_MINIO_USE_SSL is optional and defaults to false when unset.
func LoadConfigFromEnv() (Config, error) {
	endpoint := os.Getenv(endpointEnv)
	if endpoint == "" {
		return Config{}, ErrEndpointRequired
	}

	accessKey := os.Getenv(accessKeyEnv)
	if accessKey == "" {
		return Config{}, ErrAccessKeyRequired
	}

	secretKey := os.Getenv(secretKeyEnv)
	if secretKey == "" {
		return Config{}, ErrSecretKeyRequired
	}

	bucket := os.Getenv(bucketEnv)
	if bucket == "" {
		return Config{}, ErrBucketRequired
	}

	useSSL, err := boolFromEnv(useSSLEnv, false)
	if err != nil {
		return Config{}, err
	}

	return Config{
		Endpoint:  endpoint,
		AccessKey: accessKey,
		SecretKey: secretKey,
		Bucket:    bucket,
		UseSSL:    useSSL,
	}, nil
}

// boolFromEnv distinguishes "absent" from "present but invalid", the same way
// internal/platform/ratelimit's positiveIntFromEnv does. Falling back to the
// default on an unparseable value would let a typo in VIDEO_MINIO_USE_SSL
// silently downgrade a connection the operator configured as TLS.
func boolFromEnv(name string, fallback bool) (bool, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("video: %s: invalid boolean %q: %w", name, raw, err)
	}
	return value, nil
}
