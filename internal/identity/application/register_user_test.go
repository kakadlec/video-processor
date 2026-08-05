package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"video-processor/internal/identity/application"
	"video-processor/internal/identity/domain"
)

func newTestUserID(t *testing.T) domain.UserID {
	t.Helper()
	id, err := domain.NewUserID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return id
}

func TestRegisterUser_Execute(t *testing.T) {
	repo := newFakeUserRepository()
	id := newTestUserID(t)
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	uc := application.NewRegisterUser(repo, fakeUserIDGenerator{id: id}, fakePasswordHasher{}, fakeClock{now: now})

	result, err := uc.Execute(context.Background(), application.RegisterUserInput{
		Email:    "User@Example.com",
		Password: "correct-horse",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.UserID != id.String() {
		t.Fatalf("UserID = %q, want %q", result.UserID, id.String())
	}
	if result.Email != "User@example.com" {
		t.Fatalf("Email = %q, want %q", result.Email, "User@example.com")
	}
	if !result.CreatedAt.Equal(now) {
		t.Fatalf("CreatedAt = %v, want %v", result.CreatedAt, now)
	}

	stored, err := repo.FindByNormalizedEmail(context.Background(), "user@example.com")
	if err != nil {
		t.Fatalf("unexpected error looking up stored user: %v", err)
	}
	if stored.PasswordHash().String() == "correct-horse" {
		t.Fatal("plaintext password must not be stored")
	}
}

func TestRegisterUser_InvalidEmail(t *testing.T) {
	repo := newFakeUserRepository()
	uc := application.NewRegisterUser(repo, fakeUserIDGenerator{id: newTestUserID(t)}, fakePasswordHasher{}, fakeClock{now: time.Now()})

	_, err := uc.Execute(context.Background(), application.RegisterUserInput{Email: "not-an-email", Password: "correct-horse"})
	if !errors.Is(err, domain.ErrInvalidEmail) {
		t.Fatalf("error = %v, want %v", err, domain.ErrInvalidEmail)
	}
}

func TestRegisterUser_PasswordTooShort(t *testing.T) {
	repo := newFakeUserRepository()
	uc := application.NewRegisterUser(repo, fakeUserIDGenerator{id: newTestUserID(t)}, fakePasswordHasher{}, fakeClock{now: time.Now()})

	_, err := uc.Execute(context.Background(), application.RegisterUserInput{Email: "user@example.com", Password: "short"})
	if !errors.Is(err, application.ErrPasswordTooShort) {
		t.Fatalf("error = %v, want %v", err, application.ErrPasswordTooShort)
	}
}

func TestRegisterUser_DuplicateEmail_DoesNotOverwrite(t *testing.T) {
	repo := newFakeUserRepository()
	now := time.Now()
	uc := application.NewRegisterUser(repo, fakeUserIDGenerator{id: newTestUserID(t)}, fakePasswordHasher{}, fakeClock{now: now})
	ctx := context.Background()

	if _, err := uc.Execute(ctx, application.RegisterUserInput{Email: "user@example.com", Password: "correct-horse"}); err != nil {
		t.Fatalf("unexpected error on first registration: %v", err)
	}

	_, err := uc.Execute(ctx, application.RegisterUserInput{Email: "USER@EXAMPLE.COM", Password: "another-password"})
	if !errors.Is(err, domain.ErrUserAlreadyExists) {
		t.Fatalf("error = %v, want %v", err, domain.ErrUserAlreadyExists)
	}

	stored, err := repo.FindByNormalizedEmail(ctx, "user@example.com")
	if err != nil {
		t.Fatalf("unexpected error looking up stored user: %v", err)
	}
	if err := (fakePasswordHasher{}).Compare(stored.PasswordHash(), "another-password"); err == nil {
		t.Fatal("duplicate registration must not overwrite the existing account's password")
	}
}
