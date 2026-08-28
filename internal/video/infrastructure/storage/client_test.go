package storage_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"

	"video-processor/internal/video/infrastructure/storage"
)

// unreachableEndpoint points at a port nothing listens on, so Open succeeds
// and only the later round trip fails.
const unreachableEndpoint = "127.0.0.1:1"

func testConfig(t *testing.T) storage.Config {
	t.Helper()

	endpoint := os.Getenv("VIDEO_MINIO_TEST_ENDPOINT")
	accessKey := os.Getenv("VIDEO_MINIO_TEST_ACCESS_KEY")
	secretKey := os.Getenv("VIDEO_MINIO_TEST_SECRET_KEY")
	bucket := os.Getenv("VIDEO_MINIO_TEST_BUCKET")

	if endpoint == "" || accessKey == "" || secretKey == "" || bucket == "" {
		t.Skip("VIDEO_MINIO_TEST_ENDPOINT/ACCESS_KEY/SECRET_KEY/BUCKET not all set; skipping MinIO integration test")
	}

	// PublicEndpoint is set explicitly rather than left zero: these tests
	// build a Config literal instead of going through LoadConfigFromEnv, so
	// they do not get its defaulting, and minio.New rejects an empty
	// endpoint.
	return storage.Config{
		Endpoint:       endpoint,
		AccessKey:      accessKey,
		SecretKey:      secretKey,
		Bucket:         bucket,
		PublicEndpoint: endpoint,
	}
}

func testClient(t *testing.T) (*minio.Client, storage.Config) {
	t.Helper()

	cfg := testConfig(t)
	client, err := storage.Open(cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return client, cfg
}

func unreachableClient(t *testing.T) *minio.Client {
	t.Helper()

	client, err := storage.Open(storage.Config{
		Endpoint:       unreachableEndpoint,
		AccessKey:      "access",
		SecretKey:      "secret",
		Bucket:         "bucket",
		PublicEndpoint: unreachableEndpoint,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return client
}

// uniqueBucket derives a name that is valid for S3 (lowercase, no
// underscores) and unique per test, so parallel or repeated runs never
// collide on a bucket another test is creating or deleting.
func uniqueBucket(t *testing.T, prefix string) string {
	t.Helper()

	name := strings.ToLower(t.Name())
	name = strings.NewReplacer("/", "-", "_", "-", " ", "-").Replace(name)
	full := fmt.Sprintf("%s-%s-%d", prefix, name, time.Now().UnixNano()%1e6)
	if len(full) > 63 {
		full = full[:63]
	}
	return strings.Trim(full, "-")
}

// removeBucket drains bucket and deletes it. The drain matters for buckets
// that actually hold objects (the ResultStorage tests): RemoveBucket refuses
// a non-empty bucket, and these run against a shared MinIO instance whose
// data lives in a named volume, so anything left behind accumulates across
// every future suite run.
func removeBucket(t *testing.T, client *minio.Client, bucket string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for object := range client.ListObjects(ctx, bucket, minio.ListObjectsOptions{Recursive: true}) {
		if object.Err != nil {
			t.Logf("cleanup: list %q: %v", bucket, object.Err)
			continue
		}
		if err := client.RemoveObject(ctx, bucket, object.Key, minio.RemoveObjectOptions{}); err != nil {
			t.Logf("cleanup: remove %q/%q: %v", bucket, object.Key, err)
		}
	}
	if err := client.RemoveBucket(ctx, bucket); err != nil {
		t.Logf("cleanup: remove bucket %q: %v", bucket, err)
	}
}

func TestOpen_SucceedsWithoutConnecting(t *testing.T) {
	client, err := storage.Open(storage.Config{
		Endpoint:       unreachableEndpoint,
		AccessKey:      "access",
		SecretKey:      "secret",
		Bucket:         "bucket",
		PublicEndpoint: unreachableEndpoint,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client == nil {
		t.Fatal("client is nil")
	}
}

func TestOpen_RejectsMalformedEndpoint(t *testing.T) {
	// Verified against minio-go v7.3.0: a fully qualified path and an
	// invalid host character are both rejected at construction. A bare
	// "http://host:port" is NOT — see
	// TestOpen_ToleratesSchemePrefixedEndpoint below.
	for _, endpoint := range []string{"localhost:9000/path", "local host:9000"} {
		t.Run(endpoint, func(t *testing.T) {
			client, err := storage.Open(storage.Config{
				Endpoint:  endpoint,
				AccessKey: "access",
				SecretKey: "secret",
				Bucket:    "bucket",
			})
			if err == nil {
				t.Fatalf("expected an error for %q, got client %v", endpoint, client)
			}
			if client != nil {
				t.Fatalf("client = %v, want nil alongside the error", client)
			}
		})
	}
}

// TestOpen_ToleratesSchemePrefixedEndpoint asserts what
// minio-infrastructure's "Endpoint validation is the client library's, not
// this adapter's" scenario specifies: a scheme-prefixed endpoint is accepted
// by the pinned client, and Open adds no validation of its own. Recorded
// only as a comment when that capability shipped; asserted here so a future
// client upgrade that starts rejecting it cannot pass silently.
func TestOpen_ToleratesSchemePrefixedEndpoint(t *testing.T) {
	client, err := storage.Open(storage.Config{
		Endpoint:  "http://localhost:9000",
		AccessKey: "access",
		SecretKey: "secret",
		Bucket:    "bucket",
	})
	if err != nil {
		t.Fatalf("expected the pinned client to tolerate a scheme-prefixed endpoint, got: %v", err)
	}
	if client == nil {
		t.Fatal("client is nil")
	}
}

func TestPing_SucceedsAgainstRunningMinIO(t *testing.T) {
	client, cfg := testClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := storage.Ping(ctx, client, cfg.Bucket); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPing_FailsAgainstUnreachableMinIO(t *testing.T) {
	client := unreachableClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := storage.Ping(ctx, client, "bucket"); err == nil {
		t.Fatal("expected an error against an unreachable instance")
	}
}

func TestPing_HonorsCanceledContext(t *testing.T) {
	client := unreachableClient(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := storage.Ping(ctx, client, "bucket"); err == nil {
		t.Fatal("expected an error for an already-canceled context")
	}
}

func TestEnsureBucket_CreatesWhenAbsent(t *testing.T) {
	client, cfg := testClient(t)
	bucket := uniqueBucket(t, cfg.Bucket)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := storage.EnsureBucket(ctx, client, bucket); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer removeBucket(t, client, bucket)

	exists, err := client.BucketExists(ctx, bucket)
	if err != nil {
		t.Fatalf("bucket exists check: %v", err)
	}
	if !exists {
		t.Fatalf("bucket %q was not created", bucket)
	}
}

func TestEnsureBucket_NoOpWhenPresent(t *testing.T) {
	client, cfg := testClient(t)
	bucket := uniqueBucket(t, cfg.Bucket)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := storage.EnsureBucket(ctx, client, bucket); err != nil {
		t.Fatalf("first call: %v", err)
	}
	defer removeBucket(t, client, bucket)

	if err := storage.EnsureBucket(ctx, client, bucket); err != nil {
		t.Fatalf("second call: %v", err)
	}
}

func TestEnsureBucket_ConcurrentCallsBothSucceed(t *testing.T) {
	client, cfg := testClient(t)
	bucket := uniqueBucket(t, cfg.Bucket)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = storage.EnsureBucket(ctx, client, bucket)
		}()
	}
	wg.Wait()
	defer removeBucket(t, client, bucket)

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent call %d: %v", i, err)
		}
	}
}

func TestEnsureBucket_FailsAgainstUnreachableMinIO(t *testing.T) {
	client := unreachableClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := storage.EnsureBucket(ctx, client, "bucket"); err == nil {
		t.Fatal("expected an error against an unreachable instance")
	}
}

func TestBucketRegion_ReportsTheServersRegion(t *testing.T) {
	client, _ := testClient(t)
	bucket := uniqueBucket(t, "region")
	if err := storage.EnsureBucket(context.Background(), client, bucket); err != nil {
		t.Fatalf("ensure bucket: %v", err)
	}
	t.Cleanup(func() { removeBucket(t, client, bucket) })

	region, err := storage.BucketRegion(context.Background(), client, bucket)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Not compared against a literal: the value is whatever the server
	// reports, and hardcoding MinIO's answer here would be the same mistake
	// as hardcoding it in the configuration.
	if region == "" {
		t.Fatal("region is empty; the presigning client would fall back to discovering it over the network")
	}
}

// The presign-only client is built against a host the server is not expected
// to reach, so construction must not dial — the same contract Open already
// has, asserted separately because this is the client that depends on it.
func TestOpenPresigner_SucceedsWithoutConnecting(t *testing.T) {
	client, err := storage.OpenPresigner(storage.Config{
		Endpoint:       "minio:9000",
		AccessKey:      "access",
		SecretKey:      "secret",
		Bucket:         "bucket",
		PublicEndpoint: "downloads.invalid:9000",
	}, "us-east-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client == nil {
		t.Fatal("client is nil")
	}
}

func TestOpenPresigner_UsesThePublicEndpointNotTheInternalOne(t *testing.T) {
	client, err := storage.OpenPresigner(storage.Config{
		Endpoint:       "minio:9000",
		AccessKey:      "access",
		SecretKey:      "secret",
		Bucket:         "bucket",
		PublicEndpoint: "downloads.example.com:9000",
		PublicUseSSL:   true,
	}, "us-east-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	endpoint := client.EndpointURL()
	if endpoint.Host != "downloads.example.com:9000" {
		t.Fatalf("host = %q, want the public endpoint", endpoint.Host)
	}
	if endpoint.Scheme != "https" {
		t.Fatalf("scheme = %q, want https from PublicUseSSL", endpoint.Scheme)
	}
}
