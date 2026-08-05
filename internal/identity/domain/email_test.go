package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestNewEmail_Valid(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"already normalized", "user@example.com", "user@example.com"},
		{"domain is lowered, local part is preserved", "Alice@Example.COM", "Alice@example.com"},
		{"surrounding whitespace is trimmed", "  user@example.com  ", "user@example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			email, err := NewEmail(tt.raw)
			if err != nil {
				t.Fatalf("NewEmail(%q) unexpected error: %v", tt.raw, err)
			}
			if got := email.String(); got != tt.want {
				t.Fatalf("String() = %q, want %q", got, tt.want)
			}
			if email.IsZero() {
				t.Fatalf("NewEmail(%q) reported as zero", tt.raw)
			}
		})
	}
}

func TestNewEmail_Invalid(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"missing at sign", "user.example.com"},
		{"missing local part", "@example.com"},
		{"missing domain", "user@"},
		{"double at sign", "user@@example.com"},
		{"display name is rejected", "User <user@example.com>"},
		{"too long", strings.Repeat("a", 255) + "@example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			email, err := NewEmail(tt.raw)
			if err == nil {
				t.Fatalf("NewEmail(%q) = nil error, want ErrInvalidEmail", tt.raw)
			}
			if !errors.Is(err, ErrInvalidEmail) {
				t.Fatalf("NewEmail(%q) error = %v, want ErrInvalidEmail", tt.raw, err)
			}
			if !email.IsZero() {
				t.Fatalf("NewEmail(%q) returned a non-zero email on error", tt.raw)
			}
		})
	}
}

func TestEmail_Equals(t *testing.T) {
	a, _ := NewEmail("user@example.com")
	b, _ := NewEmail("user@Example.com")
	c, _ := NewEmail("other@example.com")
	d, _ := NewEmail("User@example.com")

	if !a.Equals(b) {
		t.Fatal("Equals() = false for domain-case-variant of the same email, want true")
	}
	if a.Equals(c) {
		t.Fatal("Equals() = true for different emails, want false")
	}
	if a.Equals(d) {
		t.Fatal("Equals() = true for local-part-case-variant (\"user\" vs \"User\"), want false")
	}
}

func TestEmail_ZeroValue(t *testing.T) {
	var email Email
	if !email.IsZero() {
		t.Fatal("IsZero() = false for zero value, want true")
	}
}
