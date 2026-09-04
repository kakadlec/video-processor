package domain

import "errors"

// ErrInvalidEventType is returned when a value is outside the closed set of
// event types this context reacts to.
var ErrInvalidEventType = errors.New("notification: invalid event type")

// The event types a NotificationPreference may name. These literals are
// deliberately declared here rather than imported from
// internal/video/infrastructure/postgres, which already exports the same two
// strings as VideoJobCompletedEventType/VideoJobFailedEventType: the
// ddd-architecture rule that Notification must not couple to Video
// Processing internals is a rule about imports and it holds at rest, not
// only while an event is being handled.
//
// The duplication is pinned rather than left loose. cmd/api is the
// composition root and the only package that legitimately sees both
// contexts, so TestNotificationEventTypesMatchTheEmittedTerminalEventTypes
// lives there and fails if either side is renamed or re-versioned alone.
// Without it the drift would be silent: a consumer would resolve every
// delivered event against an event type no stored preference names.
const (
	EventTypeVideoJobCompleted = "video_job.completed.v1"
	EventTypeVideoJobFailed    = "video_job.failed.v1"
)

// EventType is one of the terminal video-job outcomes a user can ask to be
// told about.
type EventType struct {
	value string
}

// ParseEventType accepts exactly the two recognized event types. Anything
// else — an unversioned name, a future generation, an arbitrary string — is
// rejected, because a preference stored under an event type nothing emits
// would never be honoured and is indistinguishable to the user from one that
// works.
func ParseEventType(raw string) (EventType, error) {
	switch raw {
	case EventTypeVideoJobCompleted, EventTypeVideoJobFailed:
		return EventType{value: raw}, nil
	default:
		return EventType{}, ErrInvalidEventType
	}
}

// String returns the event type's canonical representation.
func (e EventType) String() string {
	return e.value
}

// IsZero reports whether the EventType is the unset zero value.
func (e EventType) IsZero() bool {
	return e.value == ""
}
