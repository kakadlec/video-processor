package domain

import (
	"errors"
	"path/filepath"
	"strings"
)

var ErrInvalidOriginalFilename = errors.New("video: invalid original filename")

var supportedVideoExtensions = map[string]struct{}{
	".mp4":  {},
	".avi":  {},
	".mov":  {},
	".mkv":  {},
	".wmv":  {},
	".flv":  {},
	".webm": {},
}

type OriginalFilename struct {
	value string
}

func NewOriginalFilename(value string) (OriginalFilename, error) {
	if value == "" {
		return OriginalFilename{}, ErrInvalidOriginalFilename
	}
	if _, ok := supportedVideoExtensions[strings.ToLower(filepath.Ext(value))]; !ok {
		return OriginalFilename{}, ErrInvalidOriginalFilename
	}
	return OriginalFilename{value: value}, nil
}

func (f OriginalFilename) String() string {
	return f.value
}

func (f OriginalFilename) IsZero() bool {
	return f.value == ""
}

func (f OriginalFilename) Equal(other OriginalFilename) bool {
	return f.value == other.value
}
