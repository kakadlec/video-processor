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
