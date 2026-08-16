package domain

import "errors"

// ErrInvalidVideoJobID is returned when a job identifier is empty or malformed.
var ErrInvalidVideoJobID = errors.New("video: invalid video job id")

// VideoJobID is an opaque identifier for a VideoJob.
type VideoJobID struct {
	value string
}

// NewVideoJobID wraps an already-known job identifier.
func NewVideoJobID(value string) (VideoJobID, error) {
	if value == "" {
		return VideoJobID{}, ErrInvalidVideoJobID
	}
	return VideoJobID{value: value}, nil
}

// String returns the identifier's canonical representation.
func (id VideoJobID) String() string { return id.value }

// IsZero reports whether the identifier is unset.
func (id VideoJobID) IsZero() bool { return id.value == "" }

// Equal reports whether two identifiers represent the same job.
func (id VideoJobID) Equal(other VideoJobID) bool { return id.value == other.value }

// VideoJobIDGenerator mints new VideoJob identifiers.
type VideoJobIDGenerator interface {
	NewVideoJobID() VideoJobID
}

// VideoJobIDParser validates external identifiers using the configured ID scheme.
type VideoJobIDParser interface {
	ParseVideoJobID(value string) (VideoJobID, error)
}
