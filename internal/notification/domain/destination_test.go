package domain_test

import (
	"errors"
	"testing"

	"video-processor/internal/notification/domain"
)

func TestNewDestination(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr error
	}{
		{"https accepted", "https://example.test/hooks/1", nil},
		{"http accepted for the TLS-less compose stack", "http://app:8080/hooks", nil},
		{"host and port accepted", "https://example.test:8443/hooks", nil},
		{"query string accepted", "https://example.test/hooks?token=abc", nil},
		{"empty rejected", "", domain.ErrInvalidDestination},
		{"relative path rejected", "/hooks/1", domain.ErrInvalidDestination},
		{"schemeless host rejected", "example.test/hooks", domain.ErrInvalidDestination},
		{"ftp rejected", "ftp://example.test/hooks", domain.ErrInvalidDestination},
		{"file rejected", "file:///etc/passwd", domain.ErrInvalidDestination},
		{"javascript rejected", "javascript:alert(1)", domain.ErrInvalidDestination},
		{"scheme with no host rejected", "http:///hooks", domain.ErrInvalidDestination},
		{"bare scheme rejected", "https://", domain.ErrInvalidDestination},
		{"unparseable rejected", "http://exa mple.test/%zz", domain.ErrInvalidDestination},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			destination, err := domain.NewDestination(tt.raw)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("NewDestination(%q) error = %v, want %v", tt.raw, err, tt.wantErr)
			}
			if tt.wantErr != nil {
				if !destination.IsZero() {
					t.Fatalf("NewDestination(%q) returned %q on a rejected value", tt.raw, destination)
				}
				return
			}
			if destination.String() != tt.raw {
				t.Fatalf("NewDestination(%q).String() = %q", tt.raw, destination.String())
			}
			if destination.IsZero() {
				t.Fatalf("NewDestination(%q) reported IsZero()", tt.raw)
			}
		})
	}
}

func TestDestination_ZeroValue(t *testing.T) {
	var zero domain.Destination
	if !zero.IsZero() {
		t.Fatal("zero-value Destination should report IsZero() == true")
	}
}
