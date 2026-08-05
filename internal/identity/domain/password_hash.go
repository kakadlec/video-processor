package domain

import "errors"

// ErrEmptyPasswordHash is returned when constructing a PasswordHash from an empty value.
var ErrEmptyPasswordHash = errors.New("identity: password hash must not be empty")

// PasswordHash is an opaque adaptive password hash. The domain never sees or
// stores a plaintext password; hashing and verification happen behind the
// PasswordHasher port.
type PasswordHash struct {
	value string
}

// NewPasswordHash wraps an already-computed hash value.
func NewPasswordHash(value string) (PasswordHash, error) {
	if value == "" {
		return PasswordHash{}, ErrEmptyPasswordHash
	}
	return PasswordHash{value: value}, nil
}

// String returns the opaque hash value, as produced by the configured PasswordHasher.
func (h PasswordHash) String() string {
	return h.value
}
