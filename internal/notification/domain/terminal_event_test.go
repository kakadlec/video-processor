package domain_test

import (
	"errors"
	"testing"
	"time"

	"video-processor/internal/notification/domain"
)

func mustJobID(t *testing.T, value string) domain.JobID {
	t.Helper()
	jobID, err := domain.NewJobID(value)
	if err != nil {
		t.Fatalf("NewJobID(%q) error = %v", value, err)
	}
	return jobID
}

func mustUserID(t *testing.T, value string) domain.UserID {
	t.Helper()
	userID, err := domain.NewUserID(value)
	if err != nil {
		t.Fatalf("NewUserID(%q) error = %v", value, err)
	}
	return userID
}

func TestNewCompletedEvent(t *testing.T) {
	occurredAt := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

	event, err := domain.NewCompletedEvent(mustJobID(t, "job-1"), mustUserID(t, "user-1"), occurredAt, 42, "frames_job-1.zip")
	if err != nil {
		t.Fatalf("NewCompletedEvent error = %v", err)
	}
	if got := event.EventType().String(); got != domain.EventTypeVideoJobCompleted {
		t.Fatalf("EventType() = %q, want %q", got, domain.EventTypeVideoJobCompleted)
	}
	if event.JobID().String() != "job-1" || event.UserID().String() != "user-1" {
		t.Fatalf("identity = %s/%s, want job-1/user-1", event.JobID(), event.UserID())
	}
	if !event.OccurredAt().Equal(occurredAt) {
		t.Fatalf("OccurredAt() = %v, want %v", event.OccurredAt(), occurredAt)
	}
	if event.FrameCount() != 42 || event.StorageKey() != "frames_job-1.zip" {
		t.Fatalf("outcome = %d/%q, want 42/frames_job-1.zip", event.FrameCount(), event.StorageKey())
	}
	if event.Reason() != "" {
		t.Fatalf("Reason() = %q, want empty on a completion", event.Reason())
	}
	if event.IsZero() {
		t.Fatal("IsZero() = true for a built event")
	}
}

func TestNewFailedEvent(t *testing.T) {
	occurredAt := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

	event, err := domain.NewFailedEvent(mustJobID(t, "job-2"), mustUserID(t, "user-1"), occurredAt, "ffmpeg exited 1")
	if err != nil {
		t.Fatalf("NewFailedEvent error = %v", err)
	}
	if got := event.EventType().String(); got != domain.EventTypeVideoJobFailed {
		t.Fatalf("EventType() = %q, want %q", got, domain.EventTypeVideoJobFailed)
	}
	if event.Reason() != "ffmpeg exited 1" {
		t.Fatalf("Reason() = %q, want %q", event.Reason(), "ffmpeg exited 1")
	}
	if event.FrameCount() != 0 || event.StorageKey() != "" {
		t.Fatalf("completion fields = %d/%q, want zero on a failure", event.FrameCount(), event.StorageKey())
	}
}

// TestNewFailedEvent_AcceptsAnEmptyReason pins the deliberate asymmetry with
// the completion constructor: a reason is informational text from another
// context, so refusing the event over a missing one would drop a
// notification its owner asked for.
func TestNewFailedEvent_AcceptsAnEmptyReason(t *testing.T) {
	occurredAt := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

	if _, err := domain.NewFailedEvent(mustJobID(t, "job-2"), mustUserID(t, "user-1"), occurredAt, ""); err != nil {
		t.Fatalf("NewFailedEvent with an empty reason error = %v, want nil", err)
	}
}

func TestNewCompletedEvent_RejectsIncompleteInput(t *testing.T) {
	occurredAt := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	jobID := mustJobID(t, "job-1")
	userID := mustUserID(t, "user-1")

	tests := []struct {
		name       string
		jobID      domain.JobID
		userID     domain.UserID
		occurredAt time.Time
		frameCount int
		storageKey string
		want       error
	}{
		{"no job id", domain.JobID{}, userID, occurredAt, 1, "k", domain.ErrTerminalEventJobIDRequired},
		{"no user id", jobID, domain.UserID{}, occurredAt, 1, "k", domain.ErrTerminalEventUserIDRequired},
		{"no occurred at", jobID, userID, time.Time{}, 1, "k", domain.ErrTerminalEventOccurredAtRequired},
		{"no storage key", jobID, userID, occurredAt, 1, "", domain.ErrTerminalEventStorageKeyRequired},
		{"negative frame count", jobID, userID, occurredAt, -1, "k", domain.ErrTerminalEventFrameCountInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := domain.NewCompletedEvent(tt.jobID, tt.userID, tt.occurredAt, tt.frameCount, tt.storageKey)
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestNewFailedEvent_RejectsIncompleteInput(t *testing.T) {
	occurredAt := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

	if _, err := domain.NewFailedEvent(domain.JobID{}, mustUserID(t, "user-1"), occurredAt, "boom"); !errors.Is(err, domain.ErrTerminalEventJobIDRequired) {
		t.Fatalf("error = %v, want %v", err, domain.ErrTerminalEventJobIDRequired)
	}
	if _, err := domain.NewFailedEvent(mustJobID(t, "job-1"), domain.UserID{}, occurredAt, "boom"); !errors.Is(err, domain.ErrTerminalEventUserIDRequired) {
		t.Fatalf("error = %v, want %v", err, domain.ErrTerminalEventUserIDRequired)
	}
	if _, err := domain.NewFailedEvent(mustJobID(t, "job-1"), mustUserID(t, "user-1"), time.Time{}, "boom"); !errors.Is(err, domain.ErrTerminalEventOccurredAtRequired) {
		t.Fatalf("error = %v, want %v", err, domain.ErrTerminalEventOccurredAtRequired)
	}
}

func TestNewJobID(t *testing.T) {
	if _, err := domain.NewJobID(""); !errors.Is(err, domain.ErrInvalidJobID) {
		t.Fatalf("NewJobID(\"\") error = %v, want %v", err, domain.ErrInvalidJobID)
	}
	jobID := mustJobID(t, "job-1")
	if jobID.IsZero() {
		t.Fatal("IsZero() = true for a built JobID")
	}
	if !jobID.Equal(mustJobID(t, "job-1")) || jobID.Equal(mustJobID(t, "job-2")) {
		t.Fatal("Equal does not compare by value")
	}
	if (domain.JobID{}).IsZero() != true {
		t.Fatal("IsZero() = false for the zero JobID")
	}
}
