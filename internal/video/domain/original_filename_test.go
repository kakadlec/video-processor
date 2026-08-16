package domain_test

import (
	"errors"
	"testing"

	"video-processor/internal/video/domain"
)

func TestNewOriginalFilename(t *testing.T) {
	for _, filename := range []string{
		"video.mp4", "video.avi", "video.mov", "video.mkv", "video.wmv", "video.flv", "video.webm", "VIDEO.MP4",
	} {
		t.Run(filename, func(t *testing.T) {
			got, err := domain.NewOriginalFilename(filename)
			if err != nil || got.String() != filename || got.IsZero() {
				t.Fatalf("NewOriginalFilename(%q) = (%v, %v)", filename, got, err)
			}
		})
	}

	for _, filename := range []string{"", "video", "video.txt", "video.mp4.exe", ".mp4.txt"} {
		t.Run("reject "+filename, func(t *testing.T) {
			if _, err := domain.NewOriginalFilename(filename); !errors.Is(err, domain.ErrInvalidOriginalFilename) {
				t.Fatalf("NewOriginalFilename(%q) error = %v, want %v", filename, err, domain.ErrInvalidOriginalFilename)
			}
		})
	}
}

func TestOriginalFilename_Equal(t *testing.T) {
	a, _ := domain.NewOriginalFilename("video.mp4")
	b, _ := domain.NewOriginalFilename("video.mp4")
	c, _ := domain.NewOriginalFilename("other.mp4")
	if !a.Equal(b) || a.Equal(c) {
		t.Fatal("OriginalFilename equality behavior is incorrect")
	}
}
