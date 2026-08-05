package domain_test

import (
	"errors"
	"testing"

	"video-processor/internal/identity/domain"
)

func TestNewEmail(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr error
	}{
		{"simple valid address", "user@example.com", "user@example.com", nil},
		{"trims surrounding whitespace", "  user@example.com  ", "user@example.com", nil},
		{"lowercases domain only", "User.Name@Example.COM", "User.Name@example.com", nil},
		{"empty string", "", "", domain.ErrInvalidEmail},
		{"missing at sign", "not-an-email", "", domain.ErrInvalidEmail},
		{"missing domain", "user@", "", domain.ErrInvalidEmail},
		{"missing local part", "@example.com", "", domain.ErrInvalidEmail},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			email, err := domain.NewEmail(tt.raw)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("NewEmail(%q) error = %v, want %v", tt.raw, err, tt.wantErr)
			}
			if tt.wantErr == nil && email.String() != tt.want {
				t.Fatalf("NewEmail(%q).String() = %q, want %q", tt.raw, email.String(), tt.want)
			}
		})
	}
}

func TestEmail_NormalizedForLookup(t *testing.T) {
	email, err := domain.NewEmail("User.Name@Example.COM")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got, want := email.String(), "User.Name@example.com"; got != want {
		t.Fatalf("String() = %q, want %q (local part casing must be preserved)", got, want)
	}
	if got, want := email.NormalizedForLookup(), "user.name@example.com"; got != want {
		t.Fatalf("NormalizedForLookup() = %q, want %q", got, want)
	}
}

func TestEmail_Equal(t *testing.T) {
	a, _ := domain.NewEmail("User@Example.com")
	b, _ := domain.NewEmail("user@example.com")
	c, _ := domain.NewEmail("other@example.com")

	if !a.Equal(b) {
		t.Fatal("expected emails differing only by case to be Equal for lookup purposes")
	}
	if a.Equal(c) {
		t.Fatal("expected different emails to not be Equal")
	}
}
