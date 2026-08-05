package domain

import "errors"

// ErrPasswordMismatch is returned when a plaintext password does not match a stored hash.
var ErrPasswordMismatch = errors.New("identity: password does not match")

// PasswordHasher is the port for turning plaintext passwords into opaque hashes and back-checking them.
// The domain never implements this itself — the concrete algorithm lives in infrastructure.
type PasswordHasher interface {
	Hash(plaintext string) (PasswordHash, error)
	Compare(hash PasswordHash, plaintext string) error
}
