// Package password implements domain.PasswordHasher using bcrypt, an
// adaptive password-hashing algorithm.
package password

import (
	"golang.org/x/crypto/bcrypt"

	"video-processor/internal/identity/domain"
)

// Adapter implements domain.PasswordHasher using bcrypt.
type Adapter struct {
	cost int
}

// New returns a bcrypt adapter using bcrypt's recommended default cost.
func New() Adapter {
	return Adapter{cost: bcrypt.DefaultCost}
}

// Hash produces a new bcrypt hash for plaintext. The result never contains
// the plaintext password itself.
func (a Adapter) Hash(plaintext string) (domain.PasswordHash, error) {
	hashed, err := bcrypt.GenerateFromPassword([]byte(plaintext), a.cost)
	if err != nil {
		return domain.PasswordHash{}, err
	}
	return domain.NewPasswordHash(string(hashed))
}

// Compare reports whether plaintext matches hash, returning
// domain.ErrPasswordMismatch on any mismatch or malformed hash.
func (Adapter) Compare(hash domain.PasswordHash, plaintext string) error {
	if err := bcrypt.CompareHashAndPassword([]byte(hash.String()), []byte(plaintext)); err != nil {
		return domain.ErrPasswordMismatch
	}
	return nil
}
