package domain_test

import (
	"errors"
	"testing"

	"video-processor/internal/video/domain"
)

func TestNewOriginalFilename(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr error
	}{
		{"mp4 accepted", "movie.mp4", nil},
		{"avi accepted", "movie.avi", nil},
		{"mov accepted", "movie.mov", nil},
		{"mkv accepted", "movie.mkv", nil},
		{"wmv accepted", "movie.wmv", nil},
		{"flv accepted", "movie.flv", nil},
		{"webm accepted", "movie.webm", nil},
		{"uppercase extension accepted", "movie.MP4", nil},
		{"empty string rejected", "", domain.ErrInvalidOriginalFilename},
		{"unsupported extension rejected", "notes.txt", domain.ErrInvalidOriginalFilename},
		{"no extension rejected", "movie", domain.ErrInvalidOriginalFilename},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := domain.NewOriginalFilename(tt.value)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("NewOriginalFilename(%q) error = %v, want %v", tt.value, err, tt.wantErr)
			}
			if tt.wantErr == nil && f.String() != tt.value {
				t.Fatalf("NewOriginalFilename(%q).String() = %q, want %q", tt.value, f.String(), tt.value)
			}
		})
	}
}

func TestOriginalFilename_IsZero(t *testing.T) {
	var zero domain.OriginalFilename
	if !zero.IsZero() {
		t.Fatal("zero-value OriginalFilename should report IsZero() == true")
	}

	f, err := domain.NewOriginalFilename("movie.mp4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.IsZero() {
		t.Fatal("valid OriginalFilename should report IsZero() == false")
	}
}
