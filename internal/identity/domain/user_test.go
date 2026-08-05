package domain

import (
	"errors"
	"testing"
	"time"
)

type fixedUserIDGenerator struct {
	id  UserID
	err error
}

func (g fixedUserIDGenerator) Generate() (UserID, error) { return g.id, g.err }

func TestNewUserUsesInjectedIDGenerator(t *testing.T) {
	createdAt := time.Date(2026, time.August, 5, 1, 0, 0, 0, time.UTC)
	user, err := NewUser(fixedUserIDGenerator{id: "uuid-from-adapter"}, " Alice@Example.COM ", "hashed-password", createdAt)
	if err != nil {
		t.Fatalf("NewUser() error = %v", err)
	}
	if user.ID() != UserID("uuid-from-adapter") {
		t.Fatalf("ID() = %q, want adapter-provided ID", user.ID())
	}
	if user.Email() != "alice@example.com" {
		t.Fatalf("Email() = %q, want normalized email", user.Email())
	}
	if user.PasswordHash() != "hashed-password" {
		t.Fatalf("PasswordHash() did not preserve adapter output")
	}
	if !user.CreatedAt().Equal(createdAt) {
		t.Fatalf("CreatedAt() = %v, want %v", user.CreatedAt(), createdAt)
	}
}

func TestNewUserPropagatesIDGeneratorFailure(t *testing.T) {
	generatorError := errors.New("generator unavailable")
	_, err := NewUser(fixedUserIDGenerator{err: generatorError}, "alice@example.com", "hash", time.Now())
	if !errors.Is(err, generatorError) {
		t.Fatalf("NewUser() error = %v, want %v", err, generatorError)
	}
}

func TestNewUserRejectsNilGenerator(t *testing.T) {
	_, err := NewUser(nil, "alice@example.com", "hash", time.Now())
	if err != ErrInvalidUserID {
		t.Fatalf("NewUser() error = %v, want %v", err, ErrInvalidUserID)
	}
}

func TestRestoreUserAcceptsPersistedID(t *testing.T) {
	createdAt := time.Date(2026, time.August, 5, 1, 0, 0, 0, time.UTC)
	user, err := RestoreUser("uuid-from-database", "alice@example.com", "hashed-password", createdAt)
	if err != nil {
		t.Fatalf("RestoreUser() error = %v", err)
	}
	if user.ID() != UserID("uuid-from-database") {
		t.Fatalf("ID() = %q, want persisted ID", user.ID())
	}
}

func TestRestoreUserRejectsEmptyID(t *testing.T) {
	_, err := RestoreUser("", "alice@example.com", "hash", time.Now())
	if err != ErrInvalidUserID {
		t.Fatalf("RestoreUser() error = %v, want %v", err, ErrInvalidUserID)
	}
}

func TestNormalizeEmailTrimsAndLowercases(t *testing.T) {
	got, err := NormalizeEmail("  Alice@Example.COM ")
	if err != nil {
		t.Fatalf("NormalizeEmail() error = %v", err)
	}
	if got != "alice@example.com" {
		t.Fatalf("NormalizeEmail() = %q, want %q", got, "alice@example.com")
	}
}

func TestNormalizeEmailRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"", "alice", "@example.com", "alice@example", "alice @example.com", "alice@example.com@other.com"} {
		t.Run(value, func(t *testing.T) {
			if _, err := NormalizeEmail(value); err != ErrInvalidEmail {
				t.Fatalf("NormalizeEmail(%q) error = %v, want %v", value, err, ErrInvalidEmail)
			}
		})
	}
}

func TestNewUserRejectsEmptyPasswordHashAndCreationTime(t *testing.T) {
	generator := fixedUserIDGenerator{id: "generated-id"}
	if _, err := NewUser(generator, "alice@example.com", "", time.Now()); err != ErrInvalidPasswordHash {
		t.Fatalf("empty hash error = %v, want %v", err, ErrInvalidPasswordHash)
	}
	if _, err := NewUser(generator, "alice@example.com", "hash", time.Time{}); err == nil {
		t.Fatal("zero creation time unexpectedly accepted")
	}
}
