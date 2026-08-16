package domain

import "errors"

// ErrInvalidStorageKey is returned when a storage key is empty.
var ErrInvalidStorageKey = errors.New("video: invalid storage key")

// StorageKey is an opaque location in the configured storage backend.
type StorageKey struct {
	value string
}

// NewStorageKey validates and wraps a storage location.
func NewStorageKey(value string) (StorageKey, error) {
	if value == "" {
		return StorageKey{}, ErrInvalidStorageKey
	}
	return StorageKey{value: value}, nil
}

// String returns the opaque storage location.
func (key StorageKey) String() string { return key.value }

// IsZero reports whether the key is unset.
func (key StorageKey) IsZero() bool { return key.value == "" }

// Equal reports whether two keys identify the same storage location.
func (key StorageKey) Equal(other StorageKey) bool { return key.value == other.value }
