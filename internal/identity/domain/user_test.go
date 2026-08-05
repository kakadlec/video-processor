package domain_test

import (
	"errors"
	"testing"
	"time"

	"video-processor/internal/identity/domain"
)

func validEmail(t *testing.T) domain.Email {
	t.Helper()
	email, err := domain.NewEmail("user@example.com")
	if err != nil {
		t.Fatalf("unexpected error building test email: %v", err)
	}
	return email
}

func validPasswordHash(t *testing.T) domain.PasswordHash {
	t.Helper()
	hash, err := domain.NewPasswordHash("$2a$10$abcdefghijklmnopqrstuv")
	if err != nil {
		t.Fatalf("unexpected error building test password hash: %v", err)
	}
	return hash
}

func TestNewUser(t *testing.T) {
	id, err := domain.NewUserID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	gen := stubUserIDGenerator{id: id}
	email := validEmail(t)
	hash := validPasswordHash(t)
	now := time.Now()

	user, err := domain.NewUser(gen, email, hash, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !user.ID().Equal(id) {
		t.Fatalf("user.ID() = %v, want %v", user.ID(), id)
	}
	if !user.Email().Equal(email) {
		t.Fatalf("user.Email() = %v, want %v", user.Email(), email)
	}
	if user.PasswordHash().String() != hash.String() {
		t.Fatalf("user.PasswordHash() = %v, want %v", user.PasswordHash(), hash)
	}
	if !user.CreatedAt().Equal(now) {
		t.Fatalf("user.CreatedAt() = %v, want %v", user.CreatedAt(), now)
	}
}

func TestNewUser_NilGenerator(t *testing.T) {
	_, err := domain.NewUser(nil, validEmail(t), validPasswordHash(t), time.Now())
	if !errors.Is(err, domain.ErrUserIDGeneratorRequired) {
		t.Fatalf("error = %v, want %v", err, domain.ErrUserIDGeneratorRequired)
	}
}

func TestRestoreUser_RequiresID(t *testing.T) {
	_, err := domain.RestoreUser(domain.UserID{}, validEmail(t), validPasswordHash(t), time.Now())
	if !errors.Is(err, domain.ErrUserIDRequired) {
		t.Fatalf("error = %v, want %v", err, domain.ErrUserIDRequired)
	}
}

func TestRestoreUser(t *testing.T) {
	id, err := domain.NewUserID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	email := validEmail(t)
	hash := validPasswordHash(t)
	now := time.Now()

	user, err := domain.RestoreUser(id, email, hash, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !user.ID().Equal(id) {
		t.Fatalf("user.ID() = %v, want %v", user.ID(), id)
	}
}
