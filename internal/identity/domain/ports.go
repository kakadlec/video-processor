package domain

import "context"

// UserRepository persists and retrieves Identity aggregates by their stable keys.
type UserRepository interface {
	FindByID(ctx context.Context, id UserID) (User, error)
	FindByEmail(ctx context.Context, normalizedEmail string) (User, error)
	Create(ctx context.Context, user User) error
}

// PasswordHasher keeps password implementation details outside the domain.
type PasswordHasher interface {
	Hash(ctx context.Context, plaintext string) (string, error)
	Compare(ctx context.Context, passwordHash, plaintext string) error
}

// TokenService issues and verifies access tokens without coupling Identity to a token format.
type TokenService interface {
	Issue(ctx context.Context, userID UserID) (string, error)
	Verify(ctx context.Context, token string) (UserID, error)
}
