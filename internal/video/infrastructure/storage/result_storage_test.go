package storage_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"video-processor/internal/video/domain"
	"video-processor/internal/video/infrastructure/storage"
)

// newResultStorage wires a ResultStorage against the configured test
// instance, provisioning its own bucket so these tests never touch one
// another's objects.
func newResultStorage(t *testing.T) *storage.ResultStorage {
	t.Helper()

	client, _ := testClient(t)
	bucket := uniqueBucket(t, "result")
	if err := storage.EnsureBucket(context.Background(), client, bucket); err != nil {
		t.Fatalf("ensure bucket: %v", err)
	}
	// Registered before the first Put: these run against a shared MinIO
	// instance whose data directory is a named volume, so a bucket left
	// behind here accumulates across every future suite run. Cleanup runs
	// on failed tests too, which is why it is a t.Cleanup and not a defer
	// after the assertions.
	t.Cleanup(func() { removeBucket(t, client, bucket) })
	return storage.NewResultStorage(client, bucket)
}

func testKey(t *testing.T, value string) domain.StorageKey {
	t.Helper()
	key, err := domain.NewStorageKey(value)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return key
}

func writeLocalZip(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "frames.zip")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatalf("write local zip: %v", err)
	}
	return path
}

func TestResultStorage_PutThenOpen_RoundTripsExactBytesAndSize(t *testing.T) {
	results := newResultStorage(t)
	key := testKey(t, "frames_round-trip.zip")
	content := []byte("PK\x03\x04 pretend this is a zip")

	if err := results.Put(context.Background(), key, writeLocalZip(t, content)); err != nil {
		t.Fatalf("put: %v", err)
	}

	reader, size, err := results.Open(context.Background(), key)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer reader.Close()

	if size != int64(len(content)) {
		t.Fatalf("size = %d, want %d", size, len(content))
	}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("read %q, want %q", got, content)
	}
}

func TestResultStorage_Stat_ReportsSizeAndModifiedTime(t *testing.T) {
	results := newResultStorage(t)
	key := testKey(t, "frames_stat.zip")
	content := []byte("some bytes")

	before := time.Now().Add(-1 * time.Minute)
	if err := results.Put(context.Background(), key, writeLocalZip(t, content)); err != nil {
		t.Fatalf("put: %v", err)
	}

	size, modifiedAt, err := results.Stat(context.Background(), key)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if size != int64(len(content)) {
		t.Fatalf("size = %d, want %d", size, len(content))
	}
	if modifiedAt.Before(before) {
		t.Fatalf("modifiedAt = %v, want a time at or after %v", modifiedAt, before)
	}
}

// TestResultStorage_Open_MissingKeyFailsBeforeReturningAReader pins the
// behavior Open exists to normalize. minio-go's GetObject returns a lazy
// stream: the HTTP request is issued on first Stat/Read, so a missing key
// does not surface from GetObject itself. Open calls Stat before handing
// anything back precisely so the error arrives here rather than partway
// through an HTTP handler's response body.
func TestResultStorage_Open_MissingKeyFailsBeforeReturningAReader(t *testing.T) {
	results := newResultStorage(t)

	reader, size, err := results.Open(context.Background(), testKey(t, "frames_absent.zip"))
	if !errors.Is(err, domain.ErrResultNotFound) {
		t.Fatalf("error = %v, want it to match domain.ErrResultNotFound", err)
	}
	if reader != nil {
		reader.Close()
		t.Fatal("expected no reader for a missing object")
	}
	if size != 0 {
		t.Fatalf("size = %d, want 0", size)
	}
}

func TestResultStorage_Stat_MissingKeyReportsNotFound(t *testing.T) {
	results := newResultStorage(t)

	if _, _, err := results.Stat(context.Background(), testKey(t, "frames_absent.zip")); !errors.Is(err, domain.ErrResultNotFound) {
		t.Fatalf("error = %v, want it to match domain.ErrResultNotFound", err)
	}
}

// TestResultStorage_UnreachableEndpoint_IsNotReportedAsNotFound is the
// discriminating case for the sentinel: a caller maps ErrResultNotFound to a
// 404, so an outage must never take that path and report someone's existing
// artifact as gone.
func TestResultStorage_UnreachableEndpoint_IsNotReportedAsNotFound(t *testing.T) {
	results := storage.NewResultStorage(unreachableClient(t), "bucket")
	key := testKey(t, "frames_unreachable.zip")

	if _, _, err := results.Open(context.Background(), key); err == nil {
		t.Fatal("expected an error against an unreachable endpoint")
	} else if errors.Is(err, domain.ErrResultNotFound) {
		t.Fatalf("error = %v, must not be reported as not-found", err)
	}

	if _, _, err := results.Stat(context.Background(), key); err == nil {
		t.Fatal("expected an error against an unreachable endpoint")
	} else if errors.Is(err, domain.ErrResultNotFound) {
		t.Fatalf("error = %v, must not be reported as not-found", err)
	}

	if err := results.Put(context.Background(), key, writeLocalZip(t, []byte("x"))); err == nil {
		t.Fatal("expected an error against an unreachable endpoint")
	}
}

func TestResultStorage_Put_OverwritesAnExistingObject(t *testing.T) {
	results := newResultStorage(t)
	key := testKey(t, "frames_overwrite.zip")

	if err := results.Put(context.Background(), key, writeLocalZip(t, []byte("first"))); err != nil {
		t.Fatalf("first put: %v", err)
	}
	if err := results.Put(context.Background(), key, writeLocalZip(t, []byte("second-longer"))); err != nil {
		t.Fatalf("second put: %v", err)
	}

	size, _, err := results.Stat(context.Background(), key)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if size != int64(len("second-longer")) {
		t.Fatalf("size = %d, want %d", size, len("second-longer"))
	}
}

func TestResultStorage_Put_MissingLocalFileReturnsError(t *testing.T) {
	results := newResultStorage(t)

	if err := results.Put(context.Background(), testKey(t, "frames_nofile.zip"), filepath.Join(t.TempDir(), "absent.zip")); err == nil {
		t.Fatal("expected an error when the local file does not exist")
	}
}
