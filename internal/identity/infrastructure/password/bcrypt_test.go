package password_test

import (
	"errors"
	"testing"

	"video-processor/internal/identity/domain"
	"video-processor/internal/identity/infrastructure/password"
)

var _ domain.PasswordHasher = password.Adapter{}

func TestAdapter_HashAndCompare_RoundTrip(t *testing.T) {
	adapter := password.New()

	hash, err := adapter.Hash("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hash.String() == "correct-horse-battery-staple" {
		t.Fatal("hash must not equal the plaintext password")
	}

	if err := adapter.Compare(hash, "correct-horse-battery-staple"); err != nil {
		t.Fatalf("expected matching password to compare successfully, got: %v", err)
	}
}

func TestAdapter_Compare_WrongPassword(t *testing.T) {
	adapter := password.New()

	hash, err := adapter.Hash("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = adapter.Compare(hash, "wrong-password")
	if !errors.Is(err, domain.ErrPasswordMismatch) {
		t.Fatalf("error = %v, want %v", err, domain.ErrPasswordMismatch)
	}
}

func TestAdapter_Compare_MalformedHash(t *testing.T) {
	adapter := password.New()

	malformed, err := domain.NewPasswordHash("not-a-real-bcrypt-hash")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = adapter.Compare(malformed, "anything")
	if !errors.Is(err, domain.ErrPasswordMismatch) {
		t.Fatalf("error = %v, want %v (malformed hashes must not leak a different error shape)", err, domain.ErrPasswordMismatch)
	}
}
