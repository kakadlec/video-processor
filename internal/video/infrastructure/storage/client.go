package storage

import (
	"context"
	"fmt"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Open constructs a MinIO client from cfg without verifying connectivity,
// matching postgres.Open's and platform/redis.Open's lazy-connection
// behavior. The error return covers endpoint parsing and transport
// construction only — credentials.NewStaticV4 performs no key-shape
// validation, so bad credentials surface on the first server operation, not
// here. Callers verify reachability with Ping.
func Open(cfg Config) (*minio.Client, error) {
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("video: open minio client: %w", err)
	}
	return client, nil
}

// Ping issues a real round trip to the server so a caller's context governs
// the check. minio-go's IsOnline reports a cached observation from a
// background goroutine instead, which would answer for a different moment
// than the caller asked about.
func Ping(ctx context.Context, client *minio.Client, bucket string) error {
	if _, err := client.BucketExists(ctx, bucket); err != nil {
		return fmt.Errorf("video: minio ping: %w", err)
	}
	return nil
}

// EnsureBucket creates bucket if it does not already exist. A concurrent
// creation losing the race is success, not failure: replicas starting
// simultaneously is the normal case here.
//
// Only BucketAlreadyOwnedByYou is that benign race. BucketAlreadyExists is
// the opposite outcome — the name is taken in the globally shared bucket
// namespace by a different account — so this client cannot use the
// configured bucket at all, and swallowing it would report successful
// provisioning for a bucket every later operation will be denied. Verified
// against MinIO: a duplicate create with the same credentials returns
// BucketAlreadyOwnedByYou (409), which is what replicas of this service
// racing each other actually produce.
func EnsureBucket(ctx context.Context, client *minio.Client, bucket string) error {
	exists, err := client.BucketExists(ctx, bucket)
	if err != nil {
		return fmt.Errorf("video: check bucket %q: %w", bucket, err)
	}
	if exists {
		return nil
	}

	if err := client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
		if minio.ToErrorResponse(err).Code == "BucketAlreadyOwnedByYou" {
			return nil
		}
		return fmt.Errorf("video: create bucket %q: %w", bucket, err)
	}
	return nil
}

// BucketRegion asks the server which region bucket lives in, so
// OpenPresigner can be handed a concrete value instead of leaving the
// presigning client to discover it over the network — see that function for
// why discovering it there is not an option.
func BucketRegion(ctx context.Context, client *minio.Client, bucket string) (string, error) {
	region, err := client.GetBucketLocation(ctx, bucket)
	if err != nil {
		return "", fmt.Errorf("video: bucket region %q: %w", bucket, err)
	}
	return region, nil
}

// OpenPresigner constructs a client used for one thing only: signing URLs
// that a browser will follow. It is built from cfg.PublicEndpoint and
// cfg.PublicUseSSL, because SigV4 covers the Host header and a signed URL's
// host therefore cannot be corrected after issuance.
//
// The client is deliberately never pinged and never used for a bucket or
// object operation: in the general deployment it points at a host the
// server cannot reach at all. Open's contract already promises no
// connectivity check, so nothing here talks to the network.
//
// region is required rather than optional. Probed against minio-go v7.3.0:
// on a client with no configured region, PresignedGetObject issues a
// GetBucketLocation round trip against its own endpoint before it can sign,
// which fails outright ("dial tcp: lookup ... no such host") when that
// endpoint is unreachable from the server. With the region set, signing is
// local arithmetic.
func OpenPresigner(cfg Config, region string) (*minio.Client, error) {
	client, err := minio.New(cfg.PublicEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.PublicUseSSL,
		Region: region,
	})
	if err != nil {
		return nil, fmt.Errorf("video: open minio presigning client: %w", err)
	}
	return client, nil
}
