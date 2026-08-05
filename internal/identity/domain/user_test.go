package domain

import (
	"errors"
	"testing"
	"time"
)

func validUser(t *testing.T) (UserID, Email, PasswordHash, time.Time) {
	t.Helper()

	id, err := NewUserID("550e8400-e29b-41d4-a716-446655440000")
	if err != nil {
		t.Fatalf("NewUserID() unexpected error: %v", err)
	}
	email, err := NewEmail("user@example.com")
	if err != nil {
		t.Fatalf("NewEmail() unexpected error: %v", err)
	}
	hash, err := NewPasswordHash("$argon2id$v=19$m=65536,t=3,p=2$c29tZXNhbHQ$aGFzaA")
	if err != nil {
		t.Fatalf("NewPasswordHash() unexpected error: %v", err)
	}

	return id, email, hash, time.Now()
}

func TestNewUser_Valid(t *testing.T) {
	id, email, hash, createdAt := validUser(t)

	user, err := NewUser(id, email, hash, createdAt)
	if err != nil {
		t.Fatalf("NewUser() unexpected error: %v", err)
	}

	if !user.ID().Equals(id) {
		t.Fatalf("ID() = %v, want %v", user.ID(), id)
	}
	if !user.Email().Equals(email) {
		t.Fatalf("Email() = %v, want %v", user.Email(), email)
	}
	if user.PasswordHash() != hash {
		t.Fatalf("PasswordHash() = %v, want %v", user.PasswordHash(), hash)
	}
	if !user.CreatedAt().Equal(createdAt) {
		t.Fatalf("CreatedAt() = %v, want %v", user.CreatedAt(), createdAt)
	}
}

func TestNewUser_InvalidUserID(t *testing.T) {
	_, email, hash, createdAt := validUser(t)

	_, err := NewUser(UserID{}, email, hash, createdAt)
	if !errors.Is(err, ErrInvalidUserID) {
		t.Fatalf("NewUser() error = %v, want ErrInvalidUserID", err)
	}
}

func TestNewUser_InvalidEmail(t *testing.T) {
	id, _, hash, createdAt := validUser(t)

	_, err := NewUser(id, Email{}, hash, createdAt)
	if !errors.Is(err, ErrInvalidEmail) {
		t.Fatalf("NewUser() error = %v, want ErrInvalidEmail", err)
	}
}

func TestNewUser_EmptyPasswordHash(t *testing.T) {
	id, email, _, createdAt := validUser(t)

	_, err := NewUser(id, email, PasswordHash{}, createdAt)
	if !errors.Is(err, ErrEmptyPasswordHash) {
		t.Fatalf("NewUser() error = %v, want ErrEmptyPasswordHash", err)
	}
}

func TestNewUser_ZeroCreatedAt(t *testing.T) {
	id, email, hash, _ := validUser(t)

	_, err := NewUser(id, email, hash, time.Time{})
	if !errors.Is(err, ErrInvalidCreatedAt) {
		t.Fatalf("NewUser() error = %v, want ErrInvalidCreatedAt", err)
	}
}

func TestNewPasswordHash_Empty(t *testing.T) {
	_, err := NewPasswordHash("")
	if !errors.Is(err, ErrEmptyPasswordHash) {
		t.Fatalf("NewPasswordHash() error = %v, want ErrEmptyPasswordHash", err)
	}
}

func TestNewPasswordHash_Valid(t *testing.T) {
	const value = "$argon2id$v=19$m=65536,t=3,p=2$c29tZXNhbHQ$aGFzaA"

	hash, err := NewPasswordHash(value)
	if err != nil {
		t.Fatalf("NewPasswordHash() unexpected error: %v", err)
	}
	if hash.IsZero() {
		t.Fatal("NewPasswordHash() reported as zero for a valid value")
	}
	if got := hash.String(); got != value {
		t.Fatalf("String() = %q, want %q", got, value)
	}
}
