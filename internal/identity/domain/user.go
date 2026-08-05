package domain

import (
	"errors"
	"time"
)

// ErrUserIDRequired is returned when constructing a User without a valid UserID.
var ErrUserIDRequired = errors.New("identity: user id is required")

// ErrUserIDGeneratorRequired is returned when NewUser is called without a UserIDGenerator.
var ErrUserIDGeneratorRequired = errors.New("identity: user id generator is required")

// User is the Identity bounded context's aggregate root.
type User struct {
	id           UserID
	email        Email
	passwordHash PasswordHash
	createdAt    time.Time
}

// NewUser creates a brand-new User, minting its UserID through the supplied generator.
func NewUser(generator UserIDGenerator, email Email, passwordHash PasswordHash, createdAt time.Time) (*User, error) {
	if generator == nil {
		return nil, ErrUserIDGeneratorRequired
	}
	return RestoreUser(generator.NewUserID(), email, passwordHash, createdAt)
}

// RestoreUser reconstructs a User from already-known, already-validated values, e.g. from storage.
func RestoreUser(id UserID, email Email, passwordHash PasswordHash, createdAt time.Time) (*User, error) {
	if id.IsZero() {
		return nil, ErrUserIDRequired
	}
	return &User{
		id:           id,
		email:        email,
		passwordHash: passwordHash,
		createdAt:    createdAt,
	}, nil
}

// ID returns the user's opaque identifier.
func (u *User) ID() UserID {
	return u.id
}

// Email returns the user's validated email address.
func (u *User) Email() Email {
	return u.email
}

// PasswordHash returns the user's opaque password hash.
func (u *User) PasswordHash() PasswordHash {
	return u.passwordHash
}

// CreatedAt returns when the user was created.
func (u *User) CreatedAt() time.Time {
	return u.createdAt
}
