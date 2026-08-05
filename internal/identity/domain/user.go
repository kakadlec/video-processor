package domain

import (
	"strings"
	"time"
)

// User is the Identity aggregate. Plaintext credentials never belong in this type.
type User struct {
	id           UserID
	email        string
	passwordHash string
	createdAt    time.Time
}

func NewUser(generator UserIDGenerator, email, passwordHash string, createdAt time.Time) (User, error) {
	if generator == nil {
		return User{}, ErrInvalidUserID
	}
	id, err := generator.Generate()
	if err != nil {
		return User{}, err
	}
	return restoreUser(id, email, passwordHash, createdAt)
}

func RestoreUser(id UserID, email, passwordHash string, createdAt time.Time) (User, error) {
	return restoreUser(id, email, passwordHash, createdAt)
}

func restoreUser(id UserID, email, passwordHash string, createdAt time.Time) (User, error) {
	if strings.TrimSpace(id.String()) == "" {
		return User{}, ErrInvalidUserID
	}
	normalizedEmail, err := NormalizeEmail(email)
	if err != nil {
		return User{}, err
	}
	if strings.TrimSpace(passwordHash) == "" {
		return User{}, ErrInvalidPasswordHash
	}
	if createdAt.IsZero() {
		return User{}, ErrInvalidCreatedAt
	}
	return User{id: id, email: normalizedEmail, passwordHash: passwordHash, createdAt: createdAt.UTC()}, nil
}

func (u User) ID() UserID           { return u.id }
func (u User) Email() string        { return u.email }
func (u User) PasswordHash() string { return u.passwordHash }
func (u User) CreatedAt() time.Time { return u.createdAt }
