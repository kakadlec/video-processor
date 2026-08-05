package domain

import (
	"context"
	"errors"
)

// ErrUserNotFound is returned when no user matches the requested lookup.
var ErrUserNotFound = errors.New("identity: user not found")

// ErrUserAlreadyExists is returned when creating a user whose email is already registered.
var ErrUserAlreadyExists = errors.New("identity: user already exists")

// UserRepository is the persistence port for the User aggregate.
// Implementations must treat email lookups as case-insensitive via Email.NormalizedForLookup.
type UserRepository interface {
	Create(ctx context.Context, user *User) error
	FindByID(ctx context.Context, id UserID) (*User, error)
	FindByNormalizedEmail(ctx context.Context, normalizedEmail string) (*User, error)
}
