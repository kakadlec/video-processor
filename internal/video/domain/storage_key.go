package domain

import "errors"

var ErrInvalidStorageKey = errors.New("video: invalid storage key")

type StorageKey struct {
	value string
}

func NewStorageKey(value string) (StorageKey, error) {
	if value == "" {
		return StorageKey{}, ErrInvalidStorageKey
	}
	return StorageKey{value: value}, nil
}

func (key StorageKey) String() string {
	return key.value
}

func (key StorageKey) IsZero() bool {
	return key.value == ""
}

func (key StorageKey) Equal(other StorageKey) bool {
	return key.value == other.value
}
