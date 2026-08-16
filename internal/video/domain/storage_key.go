package domain

import "errors"

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
