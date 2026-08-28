package domain

import (
	"context"
	"errors"
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

// ErrResultPresignFailed marks a failure to issue a delegated read grant for
// a result artifact, and exists for the same reason ErrResultStoreFailed
// does: the wrapped client error names the endpoint and bucket, so it is for
// the log and never for a response body or a persisted failure reason.
var ErrResultPresignFailed = errors.New("video: failed to presign result artifact")

// ResultStorage is the port through which a VideoJob's result artifact is
// made durable and handed back. The domain depends on this interface;
// infrastructure supplies the concrete implementation (MinIO).
type ResultStorage interface {
	// Put stores the file at localPath under key.
	Put(ctx context.Context, key StorageKey, localPath string) error

	// PresignGet issues a URL that grants read access to the artifact under
	// key, for ttl, to whoever holds it — the returned URL carries its own
	// authorization and is a credential in its own right. downloadFilename
	// is the name the storage service is asked to present to the browser;
	// it is a parameter rather than derived here so key derivation stays in
	// one place.
	//
	// The returned instant is the one the storage service will enforce, read
	// back off the issued grant rather than computed alongside it, and it
	// bounds *request admission*: a request that arrives after it is
	// refused, while a transfer already in flight runs to completion. Clock
	// skew between this process and the storage service moves the effective
	// instant in either direction.
	//
	// Implementations sign offline and therefore succeed for a key holding
	// no object; a caller that needs the artifact to exist must Stat it.
	PresignGet(ctx context.Context, key StorageKey, ttl time.Duration, downloadFilename string) (string, time.Time, error)

	// Stat reports the stored artifact's size in bytes and last-modified
	// time without reading its contents.
	Stat(ctx context.Context, key StorageKey) (int64, time.Time, error)
}
