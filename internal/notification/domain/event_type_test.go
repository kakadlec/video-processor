package domain_test

import (
	"errors"
	"testing"

	"video-processor/internal/notification/domain"
)

func TestParseEventType(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr error
	}{
		{"completed accepted", domain.EventTypeVideoJobCompleted, nil},
		{"failed accepted", domain.EventTypeVideoJobFailed, nil},
		{"unversioned completed rejected", "video_job.completed", domain.ErrInvalidEventType},
		{"unversioned failed rejected", "video_job.failed", domain.ErrInvalidEventType},
		{"future generation rejected", "video_job.completed.v2", domain.ErrInvalidEventType},
		{"dispatch event type rejected", "video_job.queued.v2", domain.ErrInvalidEventType},
		{"arbitrary string rejected", "whatever", domain.ErrInvalidEventType},
		{"empty rejected", "", domain.ErrInvalidEventType},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eventType, err := domain.ParseEventType(tt.raw)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ParseEventType(%q) error = %v, want %v", tt.raw, err, tt.wantErr)
			}
			if tt.wantErr != nil {
				if !eventType.IsZero() {
					t.Fatalf("ParseEventType(%q) returned %q on a rejected value", tt.raw, eventType)
				}
				return
			}
			if eventType.String() != tt.raw {
				t.Fatalf("ParseEventType(%q).String() = %q", tt.raw, eventType.String())
			}
			if eventType.IsZero() {
				t.Fatalf("ParseEventType(%q) reported IsZero()", tt.raw)
			}
		})
	}
}

func TestEventTypeConstantValues(t *testing.T) {
	// These literals are the wire contract with the Video Processing
	// context, which declares its own copies. cmd/api pins the two against
	// each other; this pins ours against the string itself, so a rename
	// here cannot pass by editing both sides of that comparison.
	if domain.EventTypeVideoJobCompleted != "video_job.completed.v1" {
		t.Fatalf("EventTypeVideoJobCompleted = %q", domain.EventTypeVideoJobCompleted)
	}
	if domain.EventTypeVideoJobFailed != "video_job.failed.v1" {
		t.Fatalf("EventTypeVideoJobFailed = %q", domain.EventTypeVideoJobFailed)
	}
}

func TestEventType_ZeroValue(t *testing.T) {
	var zero domain.EventType
	if !zero.IsZero() {
		t.Fatal("zero-value EventType should report IsZero() == true")
	}
	if zero.String() != "" {
		t.Fatalf("zero-value EventType String() = %q, want empty", zero.String())
	}
}
