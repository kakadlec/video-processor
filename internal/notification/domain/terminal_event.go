package domain

import (
	"errors"
	"time"
)

// ErrTerminalEventJobIDRequired is returned when building an event without a valid JobID.
var ErrTerminalEventJobIDRequired = errors.New("notification: terminal event job id is required")

// ErrTerminalEventUserIDRequired is returned when building an event without a valid UserID.
var ErrTerminalEventUserIDRequired = errors.New("notification: terminal event user id is required")

// ErrTerminalEventOccurredAtRequired is returned when building an event with
// a zero occurred-at. The value is the enrolment boundary a preference's
// created_at is compared against, so an unset one would silently deliver
// every event to every preference.
var ErrTerminalEventOccurredAtRequired = errors.New("notification: terminal event occurred at is required")

// ErrTerminalEventStorageKeyRequired is returned when building a completion without a result key.
var ErrTerminalEventStorageKeyRequired = errors.New("notification: terminal event storage key is required")

// ErrTerminalEventFrameCountInvalid is returned when building a completion with a negative frame count.
var ErrTerminalEventFrameCountInvalid = errors.New("notification: terminal event frame count must not be negative")

// TerminalEvent is this context's own model of a video job reaching a
// terminal state: what the notifier resolves against a user's preferences.
//
// It is deliberately not the decoded broker message. The message is a wire
// type owned by internal/notification/infrastructure/messaging, and the
// composition root is what turns one into a TerminalEvent — so nothing
// downstream of this type knows that a wire format, a broker, or a Video
// Processing generation suffix exists. Widening the message struct therefore
// cannot reach the delivery use case without someone deciding it should.
//
// The outcome-specific fields are carried on one struct rather than in two
// types because every consumer of this value branches on EventType anyway,
// and a sum type in Go costs an interface plus a type switch at every use.
// Which fields are meaningful is fixed by the constructor that built it:
// NewCompletedEvent populates the frame count and the result key,
// NewFailedEvent the reason, and each leaves the other's fields zero.
type TerminalEvent struct {
	eventType  EventType
	jobID      JobID
	userID     UserID
	occurredAt time.Time
	frameCount int
	storageKey string
	reason     string
}

// NewCompletedEvent builds the EventTypeVideoJobCompleted outcome.
func NewCompletedEvent(jobID JobID, userID UserID, occurredAt time.Time, frameCount int, storageKey string) (TerminalEvent, error) {
	if frameCount < 0 {
		return TerminalEvent{}, ErrTerminalEventFrameCountInvalid
	}
	if storageKey == "" {
		return TerminalEvent{}, ErrTerminalEventStorageKeyRequired
	}
	event, err := newTerminalEvent(EventType{value: EventTypeVideoJobCompleted}, jobID, userID, occurredAt)
	if err != nil {
		return TerminalEvent{}, err
	}
	event.frameCount = frameCount
	event.storageKey = storageKey
	return event, nil
}

// NewFailedEvent builds the EventTypeVideoJobFailed outcome.
//
// An empty reason is accepted: it is Video Processing's own error text, and
// a job can end without one. Refusing the event would drop a notification
// its owner asked for over a field that is only ever informational.
func NewFailedEvent(jobID JobID, userID UserID, occurredAt time.Time, reason string) (TerminalEvent, error) {
	event, err := newTerminalEvent(EventType{value: EventTypeVideoJobFailed}, jobID, userID, occurredAt)
	if err != nil {
		return TerminalEvent{}, err
	}
	event.reason = reason
	return event, nil
}

func newTerminalEvent(eventType EventType, jobID JobID, userID UserID, occurredAt time.Time) (TerminalEvent, error) {
	if jobID.IsZero() {
		return TerminalEvent{}, ErrTerminalEventJobIDRequired
	}
	if userID.IsZero() {
		return TerminalEvent{}, ErrTerminalEventUserIDRequired
	}
	if occurredAt.IsZero() {
		return TerminalEvent{}, ErrTerminalEventOccurredAtRequired
	}
	return TerminalEvent{
		eventType:  eventType,
		jobID:      jobID,
		userID:     userID,
		occurredAt: occurredAt,
	}, nil
}

// EventType returns which terminal outcome this is.
func (e TerminalEvent) EventType() EventType { return e.eventType }

// JobID returns the job the outcome belongs to.
func (e TerminalEvent) JobID() JobID { return e.jobID }

// UserID returns the owner the outcome is announced to.
func (e TerminalEvent) UserID() UserID { return e.userID }

// OccurredAt returns when the outcome happened. It is compared against a
// preference's creation time, never against its update time.
func (e TerminalEvent) OccurredAt() time.Time { return e.occurredAt }

// FrameCount returns how many frames were extracted, and is meaningful only
// on a completion.
func (e TerminalEvent) FrameCount() int { return e.frameCount }

// StorageKey returns the result object's key, and is meaningful only on a
// completion. It is a key, not a credential: the object behind it is
// retrievable only through the API's authenticated, owner-scoped route.
func (e TerminalEvent) StorageKey() string { return e.storageKey }

// Reason returns Video Processing's failure text, and is meaningful only on
// a failure.
func (e TerminalEvent) Reason() string { return e.reason }

// IsZero reports whether the TerminalEvent is the unset zero value.
func (e TerminalEvent) IsZero() bool { return e.eventType.IsZero() }
