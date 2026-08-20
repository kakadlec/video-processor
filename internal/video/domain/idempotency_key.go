package domain

import "errors"

// ErrInvalidIdempotencyKey is returned when a value fails IdempotencyKey
// construction.
var ErrInvalidIdempotencyKey = errors.New("video: invalid idempotency key")

// IdempotencyKey identifies a POST /upload submission by the authenticated
// user who made it and a content hash of the uploaded bytes, so a retry with
// the same content from the same user is recognized as a duplicate of a
// specific prior submission, never across users.
type IdempotencyKey struct {
	value string
}

// NewIdempotencyKey builds an IdempotencyKey from an authenticated user's ID
// and a content hash (e.g. hex-encoded SHA-256), both of which must be
// non-empty.
func NewIdempotencyKey(userID, contentHash string) (IdempotencyKey, error) {
	if userID == "" || contentHash == "" {
		return IdempotencyKey{}, ErrInvalidIdempotencyKey
	}
	return IdempotencyKey{value: "idempotency:" + userID + ":" + contentHash}, nil
}

// String returns the key's canonical representation, suitable for use as a
// Redis key.
func (k IdempotencyKey) String() string {
	return k.value
}

// IsZero reports whether the IdempotencyKey is the unset zero value.
func (k IdempotencyKey) IsZero() bool {
	return k.value == ""
}
