package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"video-processor/internal/identity/application"
)

func registerTestUser(t *testing.T, repo *fakeUserRepository, now time.Time) {
	t.Helper()
	uc := application.NewRegisterUser(repo, fakeUserIDGenerator{id: newTestUserID(t)}, fakePasswordHasher{}, fakeClock{now: now})
	if _, err := uc.Execute(context.Background(), application.RegisterUserInput{
		Email:    "user@example.com",
		Password: "correct-horse",
	}); err != nil {
		t.Fatalf("unexpected error registering test user: %v", err)
	}
}

func TestAuthenticateUser_Success(t *testing.T) {
	repo := newFakeUserRepository()
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	registerTestUser(t, repo, now)

	uc := application.NewAuthenticateUser(repo, fakePasswordHasher{}, fakeTokenIssuer{}, fakeClock{now: now})
	result, err := uc.Execute(context.Background(), application.AuthenticateUserInput{
		Email:    "User@Example.com",
		Password: "correct-horse",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantToken := "token-for-" + newTestUserID(t).String()
	if result.AccessToken != wantToken {
		t.Fatalf("AccessToken = %q, want %q", result.AccessToken, wantToken)
	}
	wantExpiry := now.Add(application.AccessTokenTTL)
	if !result.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("ExpiresAt = %v, want %v", result.ExpiresAt, wantExpiry)
	}
}

func TestAuthenticateUser_UnknownEmail(t *testing.T) {
	repo := newFakeUserRepository()
	uc := application.NewAuthenticateUser(repo, fakePasswordHasher{}, fakeTokenIssuer{}, fakeClock{now: time.Now()})

	_, err := uc.Execute(context.Background(), application.AuthenticateUserInput{
		Email:    "nobody@example.com",
		Password: "whatever1",
	})
	if !errors.Is(err, application.ErrAuthenticationFailed) {
		t.Fatalf("error = %v, want %v", err, application.ErrAuthenticationFailed)
	}
}

func TestAuthenticateUser_WrongPassword(t *testing.T) {
	repo := newFakeUserRepository()
	now := time.Now()
	registerTestUser(t, repo, now)

	uc := application.NewAuthenticateUser(repo, fakePasswordHasher{}, fakeTokenIssuer{}, fakeClock{now: now})
	_, err := uc.Execute(context.Background(), application.AuthenticateUserInput{
		Email:    "user@example.com",
		Password: "wrong-password",
	})
	if !errors.Is(err, application.ErrAuthenticationFailed) {
		t.Fatalf("error = %v, want %v", err, application.ErrAuthenticationFailed)
	}
}

func TestAuthenticateUser_UnknownUserAndWrongPassword_SameFailureShape(t *testing.T) {
	repo := newFakeUserRepository()
	now := time.Now()
	registerTestUser(t, repo, now)

	uc := application.NewAuthenticateUser(repo, fakePasswordHasher{}, fakeTokenIssuer{}, fakeClock{now: now})
	ctx := context.Background()

	_, unknownErr := uc.Execute(ctx, application.AuthenticateUserInput{Email: "nobody@example.com", Password: "correct-horse"})
	_, wrongPasswordErr := uc.Execute(ctx, application.AuthenticateUserInput{Email: "user@example.com", Password: "wrong-password"})

	if !errors.Is(unknownErr, application.ErrAuthenticationFailed) || !errors.Is(wrongPasswordErr, application.ErrAuthenticationFailed) {
		t.Fatalf("expected both failures to be ErrAuthenticationFailed, got %v and %v", unknownErr, wrongPasswordErr)
	}
	if unknownErr.Error() != wrongPasswordErr.Error() {
		t.Fatalf("expected identical external failure shape, got %q vs %q", unknownErr.Error(), wrongPasswordErr.Error())
	}
}

func TestAuthenticateUser_TokenIssuanceErrorPropagates(t *testing.T) {
	repo := newFakeUserRepository()
	now := time.Now()
	registerTestUser(t, repo, now)

	wantErr := errors.New("signing key unavailable")
	uc := application.NewAuthenticateUser(repo, fakePasswordHasher{}, fakeTokenIssuer{err: wantErr}, fakeClock{now: now})
	_, err := uc.Execute(context.Background(), application.AuthenticateUserInput{
		Email:    "user@example.com",
		Password: "correct-horse",
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}
