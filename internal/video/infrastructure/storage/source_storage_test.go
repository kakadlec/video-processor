package storage_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"video-processor/internal/video/domain"
	"video-processor/internal/video/infrastructure/storage"
)

// newSourceStorage wires a SourceStorage against the configured test
// instance, provisioning its own bucket so these tests never touch one
// another's objects.
func newSourceStorage(t *testing.T) *storage.SourceStorage {
	t.Helper()

	client, _ := testClient(t)
	bucket := uniqueBucket(t, "source")
	if err := storage.EnsureBucket(context.Background(), client, bucket); err != nil {
		t.Fatalf("ensure bucket: %v", err)
	}
	t.Cleanup(func() { removeBucket(t, client, bucket) })
	return storage.NewSourceStorage(client, bucket)
}

func readLocal(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	return content
}

func TestSourceStorage_PutThenGet_RoundTripsExactBytes(t *testing.T) {
	sources := newSourceStorage(t)
	key := testKey(t, "uploads/round-trip_movie.mp4")
	content := []byte("\x00\x00\x00\x18ftypmp42 pretend this is a video")

	if err := sources.Put(context.Background(), key, bytes.NewReader(content)); err != nil {
		t.Fatalf("put: %v", err)
	}

	local := filepath.Join(t.TempDir(), "source")
	if err := sources.Get(context.Background(), key, local); err != nil {
		t.Fatalf("get: %v", err)
	}

	if got := readLocal(t, local); !bytes.Equal(got, content) {
		t.Fatalf("read %d bytes, want %d identical bytes", len(got), len(content))
	}
}

// TestSourceStorage_PutThenGet_RoundTripsAcrossMultipleParts is the only
// test that exercises the multipart path at all: every other payload here,
// and every test video in the repository, is far below the configured part
// size, so they all take the single-part branch.
func TestSourceStorage_PutThenGet_RoundTripsAcrossMultipleParts(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping multi-part upload in short mode")
	}

	sources := newSourceStorage(t)
	key := testKey(t, "uploads/multipart_movie.mp4")

	// Just over the 16MiB part size, so the upload spans two parts.
	content := make([]byte, 16*1024*1024+1024)
	if _, err := rand.Read(content); err != nil {
		t.Fatalf("generate payload: %v", err)
	}

	if err := sources.Put(context.Background(), key, bytes.NewReader(content)); err != nil {
		t.Fatalf("put: %v", err)
	}

	local := filepath.Join(t.TempDir(), "source")
	if err := sources.Get(context.Background(), key, local); err != nil {
		t.Fatalf("get: %v", err)
	}

	if got := readLocal(t, local); !bytes.Equal(got, content) {
		t.Fatalf("multi-part round trip returned %d bytes that differ from the %d written", len(got), len(content))
	}
}

func TestSourceStorage_Get_AbsentKeyReturnsErrSourceNotFound(t *testing.T) {
	sources := newSourceStorage(t)

	err := sources.Get(context.Background(), testKey(t, "uploads/absent_movie.mp4"), filepath.Join(t.TempDir(), "source"))
	if !errors.Is(err, domain.ErrSourceNotFound) {
		t.Fatalf("get = %v, want ErrSourceNotFound", err)
	}
}

// TestSourceStorage_Delete_AbsentKeyIsNotAnError pins the contract
// handleVideoUpload's single deferred cleanup depends on: that defer runs on
// paths where an earlier step already removed the object, so an absent key
// must not surface as a failure.
func TestSourceStorage_Delete_AbsentKeyIsNotAnError(t *testing.T) {
	sources := newSourceStorage(t)

	if err := sources.Delete(context.Background(), testKey(t, "uploads/never-stored_movie.mp4")); err != nil {
		t.Fatalf("delete of an absent key = %v, want nil", err)
	}
}

func TestSourceStorage_Delete_RemovesTheObject(t *testing.T) {
	sources := newSourceStorage(t)
	key := testKey(t, "uploads/deleted_movie.mp4")

	if err := sources.Put(context.Background(), key, bytes.NewReader([]byte("video"))); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := sources.Delete(context.Background(), key); err != nil {
		t.Fatalf("delete: %v", err)
	}

	err := sources.Get(context.Background(), key, filepath.Join(t.TempDir(), "source"))
	if !errors.Is(err, domain.ErrSourceNotFound) {
		t.Fatalf("get after delete = %v, want ErrSourceNotFound", err)
	}
}

func TestSourceStorage_Put_UnreachableEndpointReportsStoreFailure(t *testing.T) {
	sources := storage.NewSourceStorage(unreachableClient(t), "bucket")

	err := sources.Put(context.Background(), testKey(t, "uploads/unreachable_movie.mp4"), bytes.NewReader([]byte("video")))
	if !errors.Is(err, domain.ErrSourceStoreFailed) {
		t.Fatalf("put = %v, want ErrSourceStoreFailed", err)
	}
}
