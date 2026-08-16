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
