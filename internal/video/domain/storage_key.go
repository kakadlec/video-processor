package domain

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// ErrInvalidStorageKey is returned when a value fails StorageKey construction.
var ErrInvalidStorageKey = errors.New("video: invalid storage key")

// StorageKey identifies an object in the configured bucket. Two key spaces
// use it: ResultStorageKey names a job's result artifact, SourceStorageKey
// names an uploaded source video.
//
// The "set only once a job reaches completed" rule belongs to the VideoJob's
// own storageKey field — where RestoreVideoJob and Complete actually enforce
// it via ErrStorageKeyRequiresCompletedStatus — not to this type. A source
// key is minted before its VideoJob exists and never reaches the aggregate.
type StorageKey struct {
	value string
}

// NewStorageKey validates raw as non-empty.
func NewStorageKey(raw string) (StorageKey, error) {
	if raw == "" {
		return StorageKey{}, ErrInvalidStorageKey
	}
	return StorageKey{value: raw}, nil
}

// String returns the storage key's canonical representation.
func (k StorageKey) String() string {
	return k.value
}

// IsZero reports whether the StorageKey is the unset zero value.
func (k StorageKey) IsZero() bool {
	return k.value == ""
}

// Equal reports whether two StorageKeys identify the same artifact.
func (k StorageKey) Equal(other StorageKey) bool {
	return k.value == other.value
}

// resultKeyPrefix and resultKeySuffix bracket a VideoJobID to form the
// result artifact's key.
//
// The key must never contain a path separator. It is returned to the browser
// as POST /upload's zip_path and used verbatim as GET /download/:filename's
// single path segment; a "/" percent-encodes to %2F, which is decoded back
// into the request path and stops that route parameter from matching. Any
// attempt to organize the bucket with per-user or per-date prefixes has to
// change cmd/api/web/app.js in the same breath.
const (
	resultKeyPrefix = "frames_"
	resultKeySuffix = ".zip"
)

// ResultStorageKey derives the storage key for jobID's result artifact.
// Deriving it here rather than in the storage adapter is what keeps the
// writing and reading sides on one convention.
func ResultStorageKey(jobID VideoJobID) StorageKey {
	return StorageKey{value: resultKeyPrefix + jobID.String() + resultKeySuffix}
}

// VideoJobIDFromStorageKey recovers the VideoJobID a result storage key was
// derived from, validating the embedded identifier through parser. It is the
// inverse of ResultStorageKey.
func VideoJobIDFromStorageKey(key StorageKey, parser VideoJobIDParser) (VideoJobID, error) {
	if parser == nil {
		return VideoJobID{}, fmt.Errorf("%w: no id parser supplied", ErrInvalidStorageKey)
	}

	raw := key.String()
	if !strings.HasPrefix(raw, resultKeyPrefix) || !strings.HasSuffix(raw, resultKeySuffix) {
		return VideoJobID{}, fmt.Errorf("%w: %q is not a result storage key", ErrInvalidStorageKey, raw)
	}

	id := raw[len(resultKeyPrefix) : len(raw)-len(resultKeySuffix)]
	parsed, err := parser.ParseVideoJobID(id)
	if err != nil {
		return VideoJobID{}, fmt.Errorf("%w: %s", ErrInvalidStorageKey, err.Error())
	}
	return parsed, nil
}

// sourceKeyPrefix namespaces uploaded source videos inside the same bucket
// that holds results.
//
// A prefix is safe here and not for results: a result key is handed back to
// the browser and used verbatim as GET /download/:filename's single path
// segment, so a "/" would percent-encode and break the route match. No route
// exposes a source key — /uploads was removed when uploads moved into the
// bucket. Anything that re-exposes source objects over HTTP has to drop this
// prefix in the same change.
const sourceKeyPrefix = "uploads/"

// SourceStorageKey derives the storage key for an uploaded source video.
// uploadID names the upload alone and is independent of the VideoJobID,
// which is minted only after the object is safely stored.
func SourceStorageKey(uploadID, originalFilename string) StorageKey {
	return StorageKey{value: sourceKeyPrefix + uploadID + "_" + path.Base(originalFilename)}
}
