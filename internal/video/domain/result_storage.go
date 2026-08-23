package domain

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrResultNotFound is returned by ResultStorage when no artifact is stored
// under the requested StorageKey. It is deliberately the domain's own
// sentinel rather than the storage client's error type, so callers can tell
// "not stored" apart from "storage failed" without importing infrastructure.
var ErrResultNotFound = errors.New("video: result artifact not found")

// ErrResultStoreFailed marks a failure to store a result artifact, so a
// caller can classify it without inspecting the underlying client's error
// text. That text names the endpoint and bucket, which must not reach a
// user-facing message or a persisted failure reason.
var ErrResultStoreFailed = errors.New("video: failed to store result artifact")

// ResultStorage is the port through which a VideoJob's result artifact is
// made durable and read back. The domain depends on this interface;
// infrastructure supplies the concrete implementation (MinIO).
type ResultStorage interface {
	// Put stores the file at localPath under key.
	Put(ctx context.Context, key StorageKey, localPath string) error

	// Open returns a reader over the stored artifact and its size in bytes.
	// Implementations MUST confirm the artifact exists before returning, so
	// a missing or unreachable object surfaces here rather than partway
	// through a caller's response body. The size is returned alongside the
	// reader so an HTTP caller can set Content-Length without a second
	// round trip. The caller closes the reader.
	Open(ctx context.Context, key StorageKey) (io.ReadCloser, int64, error)

	// Stat reports the stored artifact's size in bytes and last-modified
	// time without reading its contents.
	Stat(ctx context.Context, key StorageKey) (int64, time.Time, error)
}
