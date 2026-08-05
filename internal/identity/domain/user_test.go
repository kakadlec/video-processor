package domain

import (
	"strings"
	"testing"
	"time"
)

func TestParseUserIDAcceptsCanonicalV4UUID(t *testing.T) {
	id, err := ParseUserID("550E8400-E29B-41D4-A716-446655440000")
	if err != nil {
		t.Fatalf("ParseUserID() error = %v", err)
	}
	if got, want := id.String(), "550e8400-e29b-41d4-a716-446655440000"; got != want {
		t.Fatalf("ParseUserID() = %q, want %q", got, want)
	}
}

func TestParseUserIDRejectsNonV4OrMalformedValues(t *testing.T) {
	cases := []string{"", "not-a-uuid", "550e8400-e29b-11d4-a716-446655440000", "550e8400-e29b-41d4-c716-446655440000"}
	for _, value := range cases {
		t.Run(value, func(t *testing.T) {
			if _, err := ParseUserID(value); err != ErrInvalidUserID {
				t.Fatalf("ParseUserID(%q) error = %v, want %v", value, err, ErrInvalidUserID)
			}
		})
	}
}

func TestNewRandomUserIDCreatesValidDistinctIDs(t *testing.T) {
	first, err := NewRandomUserID()
	if err != nil {
		t.Fatalf("first NewRandomUserID() error = %v", err)
	}
	second, err := NewRandomUserID()
	if err != nil {
		t.Fatalf("second NewRandomUserID() error = %v", err)
	}
	if first == second {
		t.Fatalf("generated duplicate IDs: %q", first)
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
		t.Run(strings.ReplaceAll(value, "@", "_at_"), func(t *testing.T) {
			if _, err := NormalizeEmail(value); err != ErrInvalidEmail {
				t.Fatalf("NormalizeEmail(%q) error = %v, want %v", value, err, ErrInvalidEmail)
			}
		})
	}
}

func TestNewUserNormalizesEmailAndKeepsAggregateEncapsulated(t *testing.T) {
	id, err := NewRandomUserID()
	if err != nil {
		t.Fatalf("NewRandomUserID() error = %v", err)
	}
	createdAt := time.Date(2026, time.August, 5, 1, 0, 0, 0, time.FixedZone("test", -3*60*60))
	user, err := NewUser(id, " Alice@Example.COM ", "hashed-password", createdAt)
	if err != nil {
		t.Fatalf("NewUser() error = %v", err)
	}
	if user.Email() != "alice@example.com" {
		t.Fatalf("Email() = %q, want normalized email", user.Email())
	}
	if user.PasswordHash() != "hashed-password" {
		t.Fatalf("PasswordHash() did not preserve adapter output")
	}
	if !user.CreatedAt().Equal(createdAt.UTC()) {
		t.Fatalf("CreatedAt() = %v, want %v", user.CreatedAt(), createdAt.UTC())
	}
}

func TestNewUserRejectsEmptyPasswordHashAndCreationTime(t *testing.T) {
	id, err := NewRandomUserID()
	if err != nil {
		t.Fatalf("NewRandomUserID() error = %v", err)
	}
	if _, err := NewUser(id, "alice@example.com", "", time.Now()); err != ErrInvalidPasswordHash {
		t.Fatalf("empty hash error = %v, want %v", err, ErrInvalidPasswordHash)
	}
	if _, err := NewUser(id, "alice@example.com", "hash", time.Time{}); err == nil {
		t.Fatal("zero creation time unexpectedly accepted")
	}
}
