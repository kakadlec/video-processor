package domain_test

import (
	"testing"

	"video-processor/internal/video/domain"
)

func TestJobStatus_CanTransitionTo(t *testing.T) {
	tests := []struct {
		name string
		from domain.JobStatus
		to   domain.JobStatus
		want bool
	}{
		{"pending to queued", domain.JobStatusPending, domain.JobStatusQueued, true},
		{"queued to processing", domain.JobStatusQueued, domain.JobStatusProcessing, true},
		{"processing to completed", domain.JobStatusProcessing, domain.JobStatusCompleted, true},
		{"processing to failed", domain.JobStatusProcessing, domain.JobStatusFailed, true},
		{"pending skips to completed", domain.JobStatusPending, domain.JobStatusCompleted, false},
		{"processing goes backwards", domain.JobStatusProcessing, domain.JobStatusQueued, false},
		{"completed is terminal", domain.JobStatusCompleted, domain.JobStatusProcessing, false},
		{"failed is terminal", domain.JobStatusFailed, domain.JobStatusProcessing, false},
		{"same state", domain.JobStatusQueued, domain.JobStatusQueued, false},
		{"unknown source", domain.JobStatus("unknown"), domain.JobStatusQueued, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.from.CanTransitionTo(tt.to); got != tt.want {
				t.Fatalf("%q.CanTransitionTo(%q) = %v, want %v", tt.from, tt.to, got, tt.want)
			}
		})
	}
}

func TestJobStatus_IsValid(t *testing.T) {
	for _, status := range []domain.JobStatus{
		domain.JobStatusPending,
		domain.JobStatusQueued,
		domain.JobStatusProcessing,
		domain.JobStatusCompleted,
		domain.JobStatusFailed,
	} {
		if !status.IsValid() {
			t.Fatalf("status %q should be valid", status)
		}
	}
	if domain.JobStatus("unknown").IsValid() {
		t.Fatal("unknown status should be invalid")
	}
}
