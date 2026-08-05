package domain

import (
	"errors"
	"strings"
	"time"
)

var (
	ErrInvalidUserID       = errors.New("invalid user ID")
	ErrInvalidEmail        = errors.New("invalid email")
	ErrInvalidPasswordHash = errors.New("invalid password hash")
)

// UserID identifies a user without exposing Identity's aggregate to other contexts.
// Its concrete representation is owned by the infrastructure adapter.
type UserID string

// UserIDGenerator creates IDs without coupling the domain to a UUID library.
type UserIDGenerator interface {
	Generate() (UserID, error)
}

func (id UserID) String() string { return string(id) }

// NormalizeEmail defines the single lookup representation used by Identity.
func NormalizeEmail(email string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if strings.Count(normalized, "@") != 1 {
		return "", ErrInvalidEmail
	}
	at := strings.IndexByte(normalized, '@')
	if at <= 0 || at == len(normalized)-1 || strings.ContainsAny(normalized, " 	\r\n") {
		return "", ErrInvalidEmail
	}
	if strings.Contains(normalized[at+1:], "@") || !strings.Contains(normalized[at+1:], ".") {
		return "", ErrInvalidEmail
	}
	return normalized, nil
}

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
		return User{}, errors.New("invalid creation time")
	}
	return User{id: id, email: normalizedEmail, passwordHash: passwordHash, createdAt: createdAt.UTC()}, nil
}

func (u User) ID() UserID           { return u.id }
func (u User) Email() string        { return u.email }
func (u User) PasswordHash() string { return u.passwordHash }
func (u User) CreatedAt() time.Time { return u.createdAt }
