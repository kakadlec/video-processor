package domain

import (
	"errors"
	"path/filepath"
	"strings"
)

// ErrInvalidOriginalFilename is returned for empty or unsupported filenames.
var ErrInvalidOriginalFilename = errors.New("video: invalid original filename")

var supportedVideoExtensions = map[string]struct{}{
	".mp4": {}, ".avi": {}, ".mov": {}, ".mkv": {},
	".wmv": {}, ".flv": {}, ".webm": {},
}

// OriginalFilename is the validated client-supplied video filename.
type OriginalFilename struct {
	value string
}

// NewOriginalFilename validates that a filename has a supported video extension.
func NewOriginalFilename(value string) (OriginalFilename, error) {
	if value == "" {
		return OriginalFilename{}, ErrInvalidOriginalFilename
	}
	extension := strings.ToLower(filepath.Ext(value))
	if _, supported := supportedVideoExtensions[extension]; !supported {
		return OriginalFilename{}, ErrInvalidOriginalFilename
	}
	return OriginalFilename{value: value}, nil
}

// String returns the filename exactly as supplied.
func (filename OriginalFilename) String() string { return filename.value }

// IsZero reports whether the filename is unset.
func (filename OriginalFilename) IsZero() bool { return filename.value == "" }

// Equal reports whether two filenames contain the same value.
func (filename OriginalFilename) Equal(other OriginalFilename) bool {
	return filename.value == other.value
}
