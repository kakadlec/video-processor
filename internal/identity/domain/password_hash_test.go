package domain_test

import (
	"errors"
	"testing"

	"video-processor/internal/identity/domain"
)

func TestNewPasswordHash(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr error
	}{
		{"non-empty hash", "$2a$10$abcdefghijklmnopqrstuv", nil},
		{"empty hash rejected", "", domain.ErrEmptyPasswordHash},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash, err := domain.NewPasswordHash(tt.value)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("NewPasswordHash(%q) error = %v, want %v", tt.value, err, tt.wantErr)
			}
			if tt.wantErr == nil && hash.String() != tt.value {
				t.Fatalf("NewPasswordHash(%q).String() = %q, want %q", tt.value, hash.String(), tt.value)
			}
		})
	}
}
