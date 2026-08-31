package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"video-processor/internal/video/application"
	"video-processor/internal/video/domain"
)

// newPendingRepoJob persists a pending job carrying a source key, which
// Enqueue requires and every transition downstream of it therefore needs.
func newPendingRepoJob(t *testing.T, repo *fakeVideoJobRepository, jobID, userID string) *domain.VideoJob {
	t.Helper()
	filename, _ := domain.NewOriginalFilename("movie.mp4")
	job, err := domain.RestoreVideoJob(newTestVideoJobID(t, jobID), newTestVideoUserID(t, userID), filename, testSourceKey(t), "", domain.StorageKey{}, 0, "", domain.JobStatusPending, time.Now())
	if err != nil {
		t.Fatalf("unexpected error building job: %v", err)
	}
	if err := repo.Create(context.Background(), job); err != nil {
		t.Fatalf("unexpected error persisting job: %v", err)
	}
	return job
}

// TestEnqueueVideoJob_PersistsThroughEnqueueNotUpdate is the assertion that
// the outbox row is actually written: Enqueue is the only repository method
// that writes one, so a use case that fell back to Update would still leave
// the job in queued and pass every status check above while dispatching
// nothing.
func TestEnqueueVideoJob_PersistsThroughEnqueueNotUpdate(t *testing.T) {
	repo := newFakeVideoJobRepository()
	newPendingRepoJob(t, repo, "job-1", "user-1")

	uc := application.NewEnqueueVideoJob(repo, fakeVideoJobIDParser{})
	if _, err := uc.Execute(context.Background(), "job-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if repo.enqueueCalls != 1 {
		t.Fatalf("repo.enqueueCalls = %d, want 1", repo.enqueueCalls)
	}
	if repo.updateCalls != 0 {
		t.Fatalf("repo.updateCalls = %d, want 0", repo.updateCalls)
	}
}

// TestEnqueueVideoJob_NoSourceKey_RejectedAndNotPersisted covers the job
// POST /api/video-jobs creates: a filename with no stored object behind it.
func TestEnqueueVideoJob_NoSourceKey_RejectedAndNotPersisted(t *testing.T) {
	repo := newFakeVideoJobRepository()
	filename, _ := domain.NewOriginalFilename("movie.mp4")
	job, err := domain.RestoreVideoJob(newTestVideoJobID(t, "job-1"), newTestVideoUserID(t, "user-1"), filename, domain.StorageKey{}, "", domain.StorageKey{}, 0, "", domain.JobStatusPending, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.Create(context.Background(), job); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	uc := application.NewEnqueueVideoJob(repo, fakeVideoJobIDParser{})
	if _, err := uc.Execute(context.Background(), "job-1"); !errors.Is(err, domain.ErrSourceKeyRequiredToEnqueue) {
		t.Fatalf("error = %v, want %v", err, domain.ErrSourceKeyRequiredToEnqueue)
	}
	if repo.enqueueCalls != 0 {
		t.Fatalf("repo.enqueueCalls = %d, want 0", repo.enqueueCalls)
	}
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
