package domain

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidStorageKey is returned when a value fails StorageKey construction.
var ErrInvalidStorageKey = errors.New("video: invalid storage key")

// StorageKey identifies where a VideoJob's result artifact is stored. It is
// only set once a job reaches completed; unset (zero value) otherwise.
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
