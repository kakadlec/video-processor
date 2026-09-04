package domain_test

import (
	"errors"
	"testing"

	"video-processor/internal/notification/domain"
)

func TestNewUserID(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr error
	}{
		{"non-empty value accepted", "user-123", nil},
		{"empty string rejected", "", domain.ErrInvalidUserID},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := domain.NewUserID(tt.value)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("NewUserID(%q) error = %v, want %v", tt.value, err, tt.wantErr)
			}
			if tt.wantErr == nil && id.String() != tt.value {
				t.Fatalf("NewUserID(%q).String() = %q, want %q", tt.value, id.String(), tt.value)
			}
		})
	}
}

func TestUserID_IsZero(t *testing.T) {
	var zero domain.UserID
	if !zero.IsZero() {
		t.Fatal("zero-value UserID should report IsZero() == true")
	}

	id, err := domain.NewUserID("user-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id.IsZero() {
		t.Fatal("valid UserID should report IsZero() == false")
	}
}

func TestUserID_Equal(t *testing.T) {
	a, _ := domain.NewUserID("user-123")
	b, _ := domain.NewUserID("user-123")
	c, _ := domain.NewUserID("user-456")

	if !a.Equal(b) {
		t.Fatal("expected equal UserIDs built from the same value to be Equal")
	}
	if a.Equal(c) {
		t.Fatal("expected different UserIDs to not be Equal")
	}
}
