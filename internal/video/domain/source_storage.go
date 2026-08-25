package domain

import (
	"context"
	"errors"
	"io"
)

// ErrSourceNotFound is returned by SourceStorage when no object is stored
// under the requested StorageKey, mirroring ErrResultNotFound so a caller
// can tell "not stored" from "storage failed" without importing
// infrastructure.
var ErrSourceNotFound = errors.New("video: source video not found")

// ErrSourceStoreFailed marks a failure to store a source video, so a caller
// can classify it without inspecting the underlying client's error text.
// That text names the endpoint and bucket, which must not reach a
// user-facing message or a persisted failure reason.
var ErrSourceStoreFailed = errors.New("video: failed to store source video")

// SourceStorage is the port through which an uploaded source video is
// stored, retrieved for processing, and removed.
type SourceStorage interface {
	// Put stores everything r yields under key.
	//
	// It takes a reader rather than a local path — the mirror image of
	// ResultStorage.Put — because a source has no local existence at upload
	// time: it is streamed straight from the request body. Requiring a path
	// would reintroduce the local file this port exists to remove.
	Put(ctx context.Context, key StorageKey, r io.Reader) error

	// Get writes the stored object to localPath. It writes a file rather
	// than returning a reader because its only consumer needs a path to
	// hand to ffmpeg, which cannot read from an io.Reader.
	Get(ctx context.Context, key StorageKey, localPath string) error

	// Delete removes the object stored under key.
	//
	// Deleting a key that holds no object is NOT an error: the handler's
	// cleanup is a single deferred call covering every exit path, including
	// ones where an earlier step already removed the object.
	Delete(ctx context.Context, key StorageKey) error
}
