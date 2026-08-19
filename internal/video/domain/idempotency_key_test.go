package domain_test

import (
	"errors"
	"testing"

	"video-processor/internal/video/domain"
)

func TestNewIdempotencyKey(t *testing.T) {
	tests := []struct {
		name        string
		userID      string
		contentHash string
		wantErr     error
		wantValue   string
	}{
		{"non-empty values accepted", "user-1", "abc123", nil, "idempotency:user-1:abc123"},
		{"empty user id rejected", "", "abc123", domain.ErrInvalidIdempotencyKey, ""},
		{"empty content hash rejected", "user-1", "", domain.ErrInvalidIdempotencyKey, ""},
		{"both empty rejected", "", "", domain.ErrInvalidIdempotencyKey, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k, err := domain.NewIdempotencyKey(tt.userID, tt.contentHash)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("NewIdempotencyKey(%q, %q) error = %v, want %v", tt.userID, tt.contentHash, err, tt.wantErr)
			}
			if tt.wantErr == nil && k.String() != tt.wantValue {
				t.Fatalf("NewIdempotencyKey(%q, %q).String() = %q, want %q", tt.userID, tt.contentHash, k.String(), tt.wantValue)
			}
		})
	}
}

func TestIdempotencyKey_IsZero(t *testing.T) {
	var zero domain.IdempotencyKey
	if !zero.IsZero() {
		t.Fatal("zero-value IdempotencyKey should report IsZero() == true")
	}

	k, err := domain.NewIdempotencyKey("user-1", "abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if k.IsZero() {
		t.Fatal("valid IdempotencyKey should report IsZero() == false")
	}
}
