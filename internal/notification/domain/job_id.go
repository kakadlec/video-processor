package domain

import "errors"

// ErrInvalidJobID is returned when a value fails JobID construction.
var ErrInvalidJobID = errors.New("notification: invalid job id")

// JobID is Notification's own opaque identifier for the video job a terminal
// event reports on — a distinct Go type from internal/video/domain.VideoJobID
// for the same reason UserID is distinct from internal/identity's: the
// ddd-architecture rule forbids Notification importing Video Processing, and
// it holds at rest rather than only while an event is being handled.
//
// This context never mints one. A JobID always arrives inside a consumed
// terminal event, so there is neither a generator nor a parser port here;
// the composition root translates the message's job_id field into this type,
// which is the sanctioned crossing.
type JobID struct {
	value string
}

// NewJobID wraps an identifier carried by a consumed event. It enforces only
// this context's own invariant (non-empty) — the value's shape is Video
// Processing's business, and duplicating its UUID rule here would be a
// second place to keep in sync for no gain.
func NewJobID(value string) (JobID, error) {
	if value == "" {
		return JobID{}, ErrInvalidJobID
	}
	return JobID{value: value}, nil
}

// String returns the identifier's canonical representation.
func (id JobID) String() string {
	return id.value
}

// IsZero reports whether the JobID is the unset zero value.
func (id JobID) IsZero() bool {
	return id.value == ""
}

// Equal reports whether two JobIDs identify the same job.
func (id JobID) Equal(other JobID) bool {
	return id.value == other.value
}
