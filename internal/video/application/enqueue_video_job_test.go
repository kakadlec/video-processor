package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"video-processor/internal/video/application"
	"video-processor/internal/video/domain"
)

func newPendingRepoJob(t *testing.T, repo *fakeVideoJobRepository, jobID, userID string) *domain.VideoJob {
	t.Helper()
	filename, _ := domain.NewOriginalFilename("movie.mp4")
	job, err := domain.RestoreVideoJob(newTestVideoJobID(t, jobID), newTestVideoUserID(t, userID), filename, domain.StorageKey{}, 0, "", domain.JobStatusPending, time.Now())
	if err != nil {
		t.Fatalf("unexpected error building job: %v", err)
	}
	if err := repo.Create(context.Background(), job); err != nil {
		t.Fatalf("unexpected error persisting job: %v", err)
	}
	return job
}

func TestEnqueueVideoJob_TransitionsAndPersists(t *testing.T) {
	repo := newFakeVideoJobRepository()
	newPendingRepoJob(t, repo, "job-1", "user-1")

	uc := application.NewEnqueueVideoJob(repo, fakeVideoJobIDParser{})
	result, err := uc.Execute(context.Background(), "job-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != string(domain.JobStatusQueued) {
		t.Fatalf("result.Status = %q, want %q", result.Status, domain.JobStatusQueued)
	}

	job, err := repo.FindByID(context.Background(), newTestVideoJobID(t, "job-1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if job.Status() != domain.JobStatusQueued {
		t.Fatalf("job.Status() = %v, want %v", job.Status(), domain.JobStatusQueued)
	}
}

func TestEnqueueVideoJob_NonexistentJob_ReturnsNotFound(t *testing.T) {
	repo := newFakeVideoJobRepository()
	uc := application.NewEnqueueVideoJob(repo, fakeVideoJobIDParser{})

	_, err := uc.Execute(context.Background(), "missing-job")
	if !errors.Is(err, domain.ErrVideoJobNotFound) {
		t.Fatalf("error = %v, want %v", err, domain.ErrVideoJobNotFound)
	}
}

func TestEnqueueVideoJob_InvalidTransition_DoesNotPersist(t *testing.T) {
	repo := newFakeVideoJobRepository()
	newPendingRepoJob(t, repo, "job-1", "user-1")

	uc := application.NewEnqueueVideoJob(repo, fakeVideoJobIDParser{})
	if _, err := uc.Execute(context.Background(), "job-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// A second Enqueue from queued is invalid.
	if _, err := uc.Execute(context.Background(), "job-1"); !errors.Is(err, domain.ErrInvalidStatusTransition) {
		t.Fatalf("error = %v, want %v", err, domain.ErrInvalidStatusTransition)
	}
}
