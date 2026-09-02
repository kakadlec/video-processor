package application_test

import (
	"context"
	"errors"
	"testing"

	"video-processor/internal/video/application"
	"video-processor/internal/video/domain"
)

func newProcessingRepoJob(t *testing.T, repo *fakeVideoJobRepository, jobID, userID string) *domain.VideoJob {
	t.Helper()
	job := newQueuedRepoJob(t, repo, jobID, userID)
	if err := job.StartProcessing(); err != nil {
		t.Fatalf("unexpected error starting processing: %v", err)
	}
	repo.seed(job)
	return job
}

func TestCompleteJob_TransitionsAndPersistsResult(t *testing.T) {
	repo := newFakeVideoJobRepository()
	newProcessingRepoJob(t, repo, "job-1", "user-1")

	uc := application.NewCompleteJob(repo, repo, fakeVideoJobIDParser{})
	result, err := uc.Execute(context.Background(), application.CompleteJobInput{
		JobID:      "job-1",
		StorageKey: "outputs/frames_job-1.zip",
		FrameCount: 5,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != string(domain.JobStatusCompleted) {
		t.Fatalf("result.Status = %q, want %q", result.Status, domain.JobStatusCompleted)
	}

	job, err := repo.FindByID(context.Background(), newTestVideoJobID(t, "job-1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if job.Status() != domain.JobStatusCompleted {
		t.Fatalf("job.Status() = %v, want %v", job.Status(), domain.JobStatusCompleted)
	}
	if job.StorageKey().String() != "outputs/frames_job-1.zip" {
		t.Fatalf("job.StorageKey() = %q, want %q", job.StorageKey().String(), "outputs/frames_job-1.zip")
	}
	if job.FrameCount() != 5 {
		t.Fatalf("job.FrameCount() = %d, want 5", job.FrameCount())
	}
}

func TestCompleteJob_NonexistentJob_ReturnsNotFound(t *testing.T) {
	repo := newFakeVideoJobRepository()
	uc := application.NewCompleteJob(repo, repo, fakeVideoJobIDParser{})

	_, err := uc.Execute(context.Background(), application.CompleteJobInput{
		JobID:      "missing-job",
		StorageKey: "outputs/frames.zip",
		FrameCount: 1,
	})
	if !errors.Is(err, domain.ErrVideoJobNotFound) {
		t.Fatalf("error = %v, want %v", err, domain.ErrVideoJobNotFound)
	}
}

func TestCompleteJob_InvalidTransition_ReturnsError(t *testing.T) {
	repo := newFakeVideoJobRepository()
	newPendingRepoJob(t, repo, "job-1", "user-1")

	uc := application.NewCompleteJob(repo, repo, fakeVideoJobIDParser{})
	_, err := uc.Execute(context.Background(), application.CompleteJobInput{
		JobID:      "job-1",
		StorageKey: "outputs/frames_job-1.zip",
		FrameCount: 1,
	})
	if !errors.Is(err, domain.ErrInvalidStatusTransition) {
		t.Fatalf("error = %v, want %v", err, domain.ErrInvalidStatusTransition)
	}
}
