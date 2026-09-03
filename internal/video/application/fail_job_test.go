package application_test

import (
	"context"
	"errors"
	"testing"

	"video-processor/internal/video/application"
	"video-processor/internal/video/domain"
)

func TestFailJob_TransitionsAndPersistsReason(t *testing.T) {
	repo := newFakeVideoJobRepository()
	newProcessingRepoJob(t, repo, "job-1", "user-1")

	uc := application.NewFailJob(repo, repo, fakeVideoJobIDParser{})
	result, err := uc.Execute(context.Background(), application.FailJobInput{
		JobID:  "job-1",
		Reason: "ffmpeg exploded",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != string(domain.JobStatusFailed) {
		t.Fatalf("result.Status = %q, want %q", result.Status, domain.JobStatusFailed)
	}

	job, err := repo.FindByID(context.Background(), newTestVideoJobID(t, "job-1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if job.Status() != domain.JobStatusFailed {
		t.Fatalf("job.Status() = %v, want %v", job.Status(), domain.JobStatusFailed)
	}
	if job.ErrorReason() != "ffmpeg exploded" {
		t.Fatalf("job.ErrorReason() = %q, want %q", job.ErrorReason(), "ffmpeg exploded")
	}
}

func TestFailJob_NonexistentJob_ReturnsNotFound(t *testing.T) {
	repo := newFakeVideoJobRepository()
	uc := application.NewFailJob(repo, repo, fakeVideoJobIDParser{})

	_, err := uc.Execute(context.Background(), application.FailJobInput{JobID: "missing-job", Reason: "boom"})
	if !errors.Is(err, domain.ErrVideoJobNotFound) {
		t.Fatalf("error = %v, want %v", err, domain.ErrVideoJobNotFound)
	}
}

func TestFailJob_EmptyReason_ReturnsError(t *testing.T) {
	repo := newFakeVideoJobRepository()
	newProcessingRepoJob(t, repo, "job-1", "user-1")

	uc := application.NewFailJob(repo, repo, fakeVideoJobIDParser{})
	_, err := uc.Execute(context.Background(), application.FailJobInput{JobID: "job-1", Reason: ""})
	if !errors.Is(err, domain.ErrFailureReasonRequired) {
		t.Fatalf("error = %v, want %v", err, domain.ErrFailureReasonRequired)
	}
}
