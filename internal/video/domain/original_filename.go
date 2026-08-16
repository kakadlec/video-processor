package domain

import (
	"errors"
	"path/filepath"
	"strings"
)

// ErrInvalidOriginalFilename is returned when a value fails
// OriginalFilename construction — empty, or an unsupported extension.
var ErrInvalidOriginalFilename = errors.New("video: invalid original filename")

// supportedVideoExtensions mirrors main.go's isValidVideoFile set, so no
// caller — HTTP or otherwise — can construct a VideoJob for an unsupported
// file type by going around the legacy handler.
var supportedVideoExtensions = map[string]bool{
	".mp4":  true,
	".avi":  true,
	".mov":  true,
	".mkv":  true,
	".wmv":  true,
	".flv":  true,
	".webm": true,
}

// OriginalFilename is the validated name of the video file a VideoJob was
// created from.
type OriginalFilename struct {
	value string
}

// NewOriginalFilename validates raw as non-empty with a supported video extension.
func NewOriginalFilename(raw string) (OriginalFilename, error) {
	if raw == "" {
		return OriginalFilename{}, ErrInvalidOriginalFilename
	}

	ext := strings.ToLower(filepath.Ext(raw))
	if !supportedVideoExtensions[ext] {
		return OriginalFilename{}, ErrInvalidOriginalFilename
	}

	return OriginalFilename{value: raw}, nil
}

// String returns the filename's canonical representation.
func (f OriginalFilename) String() string {
	return f.value
}

// IsZero reports whether the OriginalFilename is the unset zero value.
func (f OriginalFilename) IsZero() bool {
	return f.value == ""
}
