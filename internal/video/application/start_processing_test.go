package application_test

import (
	"context"
	"errors"
	"testing"

	"video-processor/internal/video/application"
	"video-processor/internal/video/domain"
)

// newQueuedRepoJob persists a job already in queued — the state both
// StartProcessing and ProcessVideoJob expect, the latter because it no
// longer performs that transition itself. It seeds the state directly rather
// than going through Enqueue on purpose: this is test setup arriving at a
// state, not an exercise of the dispatch path, and going through Enqueue
// here would make every one of these tests also depend on the outbox write.
func newQueuedRepoJob(t *testing.T, repo *fakeVideoJobRepository, jobID, userID string) *domain.VideoJob {
	t.Helper()
	job := newPendingRepoJob(t, repo, jobID, userID)
	if err := job.Enqueue(); err != nil {
		t.Fatalf("unexpected error enqueuing job: %v", err)
	}
	repo.seed(job)
	return job
}

func TestStartProcessing_TransitionsAndPersists(t *testing.T) {
	repo := newFakeVideoJobRepository()
	newQueuedRepoJob(t, repo, "job-1", "user-1")

	uc := application.NewStartProcessing(repo, repo, fakeVideoJobIDParser{})
	result, err := uc.Execute(context.Background(), "job-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != string(domain.JobStatusProcessing) {
		t.Fatalf("result.Status = %q, want %q", result.Status, domain.JobStatusProcessing)
	}

	job, err := repo.FindByID(context.Background(), newTestVideoJobID(t, "job-1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if job.Status() != domain.JobStatusProcessing {
		t.Fatalf("job.Status() = %v, want %v", job.Status(), domain.JobStatusProcessing)
	}
}

func TestStartProcessing_NonexistentJob_ReturnsNotFound(t *testing.T) {
	repo := newFakeVideoJobRepository()
	uc := application.NewStartProcessing(repo, repo, fakeVideoJobIDParser{})

	_, err := uc.Execute(context.Background(), "missing-job")
	if !errors.Is(err, domain.ErrVideoJobNotFound) {
		t.Fatalf("error = %v, want %v", err, domain.ErrVideoJobNotFound)
	}
}

func TestStartProcessing_InvalidTransition_ReturnsError(t *testing.T) {
	repo := newFakeVideoJobRepository()
	newPendingRepoJob(t, repo, "job-1", "user-1")

	uc := application.NewStartProcessing(repo, repo, fakeVideoJobIDParser{})
	if _, err := uc.Execute(context.Background(), "job-1"); !errors.Is(err, domain.ErrInvalidStatusTransition) {
		t.Fatalf("error = %v, want %v", err, domain.ErrInvalidStatusTransition)
	}
}
