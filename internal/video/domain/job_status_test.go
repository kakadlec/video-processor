package domain_test

import (
	"testing"

	"video-processor/internal/video/domain"
)

func TestJobStatus_CanTransitionTo_ValidForwardTransitions(t *testing.T) {
	tests := []struct {
		from domain.JobStatus
		to   domain.JobStatus
	}{
		{domain.JobStatusPending, domain.JobStatusQueued},
		{domain.JobStatusQueued, domain.JobStatusProcessing},
		{domain.JobStatusProcessing, domain.JobStatusCompleted},
		{domain.JobStatusProcessing, domain.JobStatusFailed},
	}

	for _, tt := range tests {
		if !tt.from.CanTransitionTo(tt.to) {
			t.Errorf("CanTransitionTo: %s -> %s should be valid", tt.from, tt.to)
		}
	}
}

func TestJobStatus_CanTransitionTo_TerminalStatesRejectAnyTransition(t *testing.T) {
	terminal := []domain.JobStatus{domain.JobStatusCompleted, domain.JobStatusFailed}
	targets := []domain.JobStatus{
		domain.JobStatusPending,
		domain.JobStatusQueued,
		domain.JobStatusProcessing,
		domain.JobStatusCompleted,
		domain.JobStatusFailed,
	}

	for _, from := range terminal {
		for _, to := range targets {
			if from.CanTransitionTo(to) {
				t.Errorf("CanTransitionTo: terminal status %s -> %s should be invalid", from, to)
			}
		}
	}
}

func TestJobStatus_CanTransitionTo_UndefinedTransitionsRejected(t *testing.T) {
	tests := []struct {
		from domain.JobStatus
		to   domain.JobStatus
	}{
		{domain.JobStatusPending, domain.JobStatusCompleted},
		{domain.JobStatusPending, domain.JobStatusProcessing},
		{domain.JobStatusPending, domain.JobStatusFailed},
		{domain.JobStatusQueued, domain.JobStatusCompleted},
		{domain.JobStatusQueued, domain.JobStatusFailed},
	}

	for _, tt := range tests {
		if tt.from.CanTransitionTo(tt.to) {
			t.Errorf("CanTransitionTo: %s -> %s should be invalid", tt.from, tt.to)
		}
	}
}

// TestJobStatus_CanTransitionTo_ProcessingToQueuedIsTheOneBackwardsEdge
// pins the table's only edge that walks a job back, and pins it as the only
// one: recovery returns an abandoned job to queued so the claim can be raced
// for again, and nothing else in the machine may reverse.
func TestJobStatus_CanTransitionTo_ProcessingToQueuedIsTheOneBackwardsEdge(t *testing.T) {
	if !domain.JobStatusProcessing.CanTransitionTo(domain.JobStatusQueued) {
		t.Error("CanTransitionTo: processing -> queued should be valid")
	}

	backwards := []struct {
		from domain.JobStatus
		to   domain.JobStatus
	}{
		{domain.JobStatusQueued, domain.JobStatusPending},
		{domain.JobStatusProcessing, domain.JobStatusPending},
		{domain.JobStatusCompleted, domain.JobStatusQueued},
		{domain.JobStatusFailed, domain.JobStatusQueued},
		{domain.JobStatusCompleted, domain.JobStatusProcessing},
		{domain.JobStatusFailed, domain.JobStatusProcessing},
	}
	for _, tt := range backwards {
		if tt.from.CanTransitionTo(tt.to) {
			t.Errorf("CanTransitionTo: %s -> %s should be invalid", tt.from, tt.to)
		}
	}
}
