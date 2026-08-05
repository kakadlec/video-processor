package domain

import (
	"errors"
	"testing"
)

func TestNewUserID(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"empty", ""},
		{"not a uuid", "not-a-uuid"},
		{"missing hyphens", "550e8400e29b41d4a716446655440000"},
		{"segment too short", "550e8400-e29b-41d4-a716-44665544000"},
		{"non-hex characters", "550e8400-e29b-41d4-a716-44665544000z"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := NewUserID(tt.value)
			if err == nil {
				t.Fatalf("NewUserID(%q) = nil error, want ErrInvalidUserID", tt.value)
			}
			if !errors.Is(err, ErrInvalidUserID) {
				t.Fatalf("NewUserID(%q) error = %v, want ErrInvalidUserID", tt.value, err)
			}
			if !id.IsZero() {
				t.Fatalf("NewUserID(%q) returned a non-zero id on error", tt.value)
			}
		})
	}
}

func TestNewUserID_Valid(t *testing.T) {
	const value = "550e8400-e29b-41d4-a716-446655440000"

	id, err := NewUserID(value)
	if err != nil {
		t.Fatalf("NewUserID(%q) unexpected error: %v", value, err)
	}
	if id.IsZero() {
		t.Fatalf("NewUserID(%q) reported as zero", value)
	}
	if got := id.String(); got != value {
		t.Fatalf("String() = %q, want %q", got, value)
	}
}

func TestUserID_Equals(t *testing.T) {
	a, _ := NewUserID("550e8400-e29b-41d4-a716-446655440000")
	b, _ := NewUserID("550e8400-e29b-41d4-a716-446655440000")
	c, _ := NewUserID("550e8400-e29b-41d4-a716-446655440001")

	if !a.Equals(b) {
		t.Fatal("Equals() = false for identical values, want true")
	}
	if a.Equals(c) {
		t.Fatal("Equals() = true for different values, want false")
	}
}

func TestUserID_ZeroValue(t *testing.T) {
	var id UserID
	if !id.IsZero() {
		t.Fatal("IsZero() = false for zero value, want true")
	}
}
