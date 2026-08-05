package domain

import "context"

// PasswordHasher keeps password implementation details outside the domain.
type PasswordHasher interface {
	Hash(ctx context.Context, plaintext string) (string, error)
	Compare(ctx context.Context, passwordHash, plaintext string) error
}
