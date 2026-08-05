package domain

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrInvalidUserID       = errors.New("invalid user ID")
	ErrInvalidEmail        = errors.New("invalid email")
	ErrInvalidPasswordHash = errors.New("invalid password hash")
)

// UserID identifies a user without exposing Identity's aggregate to other contexts.
type UserID string

func NewUserID(value string) (UserID, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if !isUUID(value) {
		return "", ErrInvalidUserID
	}
	return UserID(value), nil
}

func NewUserIDRandom() (UserID, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate user ID: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return NewUserID(fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(raw[0:4]),
		hex.EncodeToString(raw[4:6]),
		hex.EncodeToString(raw[6:8]),
		hex.EncodeToString(raw[8:10]),
		hex.EncodeToString(raw[10:16]),
	))
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

func NewUser(id UserID, email, passwordHash string, createdAt time.Time) (User, error) {
	normalizedID, err := NewUserID(id.String())
	if err != nil {
		return User{}, err
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
	return User{id: normalizedID, email: normalizedEmail, passwordHash: passwordHash, createdAt: createdAt.UTC()}, nil
}

func (u User) ID() UserID           { return u.id }
func (u User) Email() string        { return u.email }
func (u User) PasswordHash() string { return u.passwordHash }
func (u User) CreatedAt() time.Time { return u.createdAt }

func isUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for i, r := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return value[14] == '4' && (value[19] == '8' || value[19] == '9' || value[19] == 'a' || value[19] == 'b')
}
