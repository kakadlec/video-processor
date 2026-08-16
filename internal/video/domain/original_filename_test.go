package domain_test

import (
	"errors"
	"testing"

	"video-processor/internal/video/domain"
)

func TestNewOriginalFilename_AcceptsSupportedExtensions(t *testing.T) {
	for _, value := range []string{
		"video.mp4", "video.avi", "video.mov", "video.mkv",
		"video.wmv", "video.flv", "video.webm", "VIDEO.MP4",
	} {
		t.Run(value, func(t *testing.T) {
			filename, err := domain.NewOriginalFilename(value)
			if err != nil {
				t.Fatalf("NewOriginalFilename(%q) error = %v", value, err)
			}
			if filename.String() != value || filename.IsZero() {
				t.Fatalf("filename = %q, IsZero = %v", filename.String(), filename.IsZero())
			}
		})
	}
}

func TestNewOriginalFilename_RejectsEmptyAndUnsupportedExtensions(t *testing.T) {
	for _, value := range []string{"", "video", "video.", "video.txt", "video.mp3", "video.png", "video.mp4.exe"} {
		t.Run(value, func(t *testing.T) {
			filename, err := domain.NewOriginalFilename(value)
			if !errors.Is(err, domain.ErrInvalidOriginalFilename) {
				t.Fatalf("NewOriginalFilename(%q) error = %v, want %v", value, err, domain.ErrInvalidOriginalFilename)
			}
			if !filename.IsZero() {
				t.Fatalf("failed construction returned nonzero filename %q", filename)
			}
		})
	}
}

func TestOriginalFilename_Equal(t *testing.T) {
	a, _ := domain.NewOriginalFilename("video.mp4")
	same, _ := domain.NewOriginalFilename("video.mp4")
	b, _ := domain.NewOriginalFilename("other.mp4")
	if !a.Equal(same) || a.Equal(b) {
		t.Fatal("Equal() returned unexpected values")
	}
}
