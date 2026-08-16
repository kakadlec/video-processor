package domain_test

import (
	"errors"
	"testing"

	"video-processor/internal/video/domain"
)

func TestNewStorageKey(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr error
	}{
		{"non-empty value accepted", "outputs/frames_123.zip", nil},
		{"empty string rejected", "", domain.ErrInvalidStorageKey},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k, err := domain.NewStorageKey(tt.value)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("NewStorageKey(%q) error = %v, want %v", tt.value, err, tt.wantErr)
			}
			if tt.wantErr == nil && k.String() != tt.value {
				t.Fatalf("NewStorageKey(%q).String() = %q, want %q", tt.value, k.String(), tt.value)
			}
		})
	}
}

func TestStorageKey_IsZero(t *testing.T) {
	var zero domain.StorageKey
	if !zero.IsZero() {
		t.Fatal("zero-value StorageKey should report IsZero() == true")
	}

	k, err := domain.NewStorageKey("outputs/frames_123.zip")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if k.IsZero() {
		t.Fatal("valid StorageKey should report IsZero() == false")
	}
}
