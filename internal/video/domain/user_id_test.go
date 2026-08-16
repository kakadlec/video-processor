package domain_test

import (
	"errors"
	"testing"

	"video-processor/internal/video/domain"
)

func TestNewUserID(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr error
	}{
		{"non-empty value accepted", "3fa85f64-5717-4562-b3fc-2c963f66afa6", nil},
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

func TestUserID_IsZeroAndEqual(t *testing.T) {
	var zero domain.UserID
	if !zero.IsZero() {
		t.Fatal("zero-value UserID should be zero")
	}

	a, _ := domain.NewUserID("user-a")
	b, _ := domain.NewUserID("user-a")
	c, _ := domain.NewUserID("user-c")
	if a.IsZero() || !a.Equal(b) || a.Equal(c) {
		t.Fatal("UserID zero/equality behavior is incorrect")
	}
}
