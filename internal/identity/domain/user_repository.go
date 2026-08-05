package domain

import "context"

// UserRepository persists and retrieves Identity aggregates by their stable keys.
type UserRepository interface {
	FindByID(ctx context.Context, id UserID) (User, error)
	FindByEmail(ctx context.Context, normalizedEmail string) (User, error)
	Create(ctx context.Context, user User) error
}
