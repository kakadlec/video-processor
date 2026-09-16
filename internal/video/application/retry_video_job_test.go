package application_test

import (
	"context"
	"errors"
	"testing"

	"video-processor/internal/video/application"
	"video-processor/internal/video/domain"
)

func TestRetryVideoJob_TransitionsProcessingToQueuedAndAdvancesEpoch(t *testing.T) {
	repo := newFakeVideoJobRepository()
	newQueuedRepoJob(t, repo, "job-1", "user-1")
	epoch := claimRepoJob(t, repo, "job-1")

	uc := application.NewRetryVideoJob(repo, repo, fakeVideoJobIDParser{})
	result, err := uc.Execute(context.Background(), "job-1", epoch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Applied {
		t.Fatalf("result.Applied = false, want true")
	}
	if result.Status != string(domain.JobStatusQueued) {
		t.Fatalf("result.Status = %q, want %q", result.Status, domain.JobStatusQueued)
	}

	stored, err := repo.FindByID(context.Background(), newTestVideoJobID(t, "job-1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stored.Status() != domain.JobStatusQueued {
		t.Fatalf("stored.Status() = %v, want %v", stored.Status(), domain.JobStatusQueued)
	}
	if stored.LeaseEpoch() != epoch+1 {
		t.Fatalf("stored.LeaseEpoch() = %d, want %d — a requeue always advances the fence", stored.LeaseEpoch(), epoch+1)
	}
	if repo.requeueCalls != 1 {
		t.Fatalf("repo.requeueCalls = %d, want 1 — this must go through Requeue, not Update", repo.requeueCalls)
	}
}

// TestRetryVideoJob_RowAlreadyAtAHigherEpoch_ReturnsFenced is the case the
// repository's own conditional predicate refuses, not the aggregate: the
// in-memory transition succeeds because the freshly-loaded row still reads
// processing, so classifyRefusedRequeue is never reached. Only the
// observedEpoch parameter — this caller's own held epoch, not the one the
// fresh load carries — can make the write disagree with the loaded status.
func TestRetryVideoJob_RowAlreadyAtAHigherEpoch_ReturnsFenced(t *testing.T) {
	repo := newFakeVideoJobRepository()
	newQueuedRepoJob(t, repo, "job-1", "user-1")
	epoch := claimRepoJob(t, repo, "job-1")

	// Stand in for a sweep (or an earlier retry) that requeued and
	// re-claimed the job while this caller was still working: the row is
	// processing again, but one epoch ahead of what this caller holds.
	stored, err := repo.FindByID(context.Background(), newTestVideoJobID(t, "job-1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := stored.Requeue(); err != nil {
		t.Fatalf("unexpected error requeueing: %v", err)
	}
	if requeued, err := repo.Requeue(context.Background(), stored, epoch); err != nil || !requeued {
		t.Fatalf("persist requeue: requeued=%v err=%v", requeued, err)
	}
	if _, err := application.NewStartProcessing(repo, repo, fakeVideoJobIDParser{}).Execute(context.Background(), "job-1"); err != nil {
		t.Fatalf("re-claim job-1: %v", err)
	}
	requeueCallsBefore := repo.requeueCalls

	uc := application.NewRetryVideoJob(repo, repo, fakeVideoJobIDParser{})
	if _, err := uc.Execute(context.Background(), "job-1", epoch); !errors.Is(err, domain.ErrJobFenced) {
		t.Fatalf("error = %v, want %v", err, domain.ErrJobFenced)
	}
	if repo.requeueCalls != requeueCallsBefore+1 {
		t.Fatalf("repo.requeueCalls = %d, want %d — the write must still be attempted and refused", repo.requeueCalls, requeueCallsBefore+1)
	}
}

// TestRetryVideoJob_JobAlreadyTerminal_ReturnsFenced covers the aggregate's
// own refusal: a job another actor already finished at this caller's held
// epoch is not processing any more, so job.Requeue() itself errors and
// classifyRefusedRequeue is what turns that into a fence.
func TestRetryVideoJob_JobAlreadyTerminal_ReturnsFenced(t *testing.T) {
	repo := newFakeVideoJobRepository()
	job := newProcessingRepoJob(t, repo, "job-1", "user-1")
	if err := job.Complete(domain.ResultStorageKey(newTestVideoJobID(t, "job-1")), 3); err != nil {
		t.Fatalf("unexpected error completing: %v", err)
	}
	if _, err := repo.Update(context.Background(), job, 0); err != nil {
		t.Fatalf("unexpected error persisting completion: %v", err)
	}

	uc := application.NewRetryVideoJob(repo, repo, fakeVideoJobIDParser{})
	if _, err := uc.Execute(context.Background(), "job-1", 0); !errors.Is(err, domain.ErrJobFenced) {
		t.Fatalf("error = %v, want %v", err, domain.ErrJobFenced)
	}
	if repo.requeueCalls != 0 {
		t.Fatalf("repo.requeueCalls = %d, want 0 — the aggregate refused before any write was attempted", repo.requeueCalls)
	}
}

// TestRetryVideoJob_PendingJob_ReturnsRawError pins the one refusal that is a
// genuine defect rather than a fence, mirroring classifyRefusedTransition's
// own exception for a terminal write: nothing enqueued this job, so nothing
// could have claimed it, and masking that as ErrJobFenced would hide a bug
// behind the ordinary recovery vocabulary.
func TestRetryVideoJob_PendingJob_ReturnsRawError(t *testing.T) {
	repo := newFakeVideoJobRepository()
	newPendingRepoJob(t, repo, "job-1", "user-1")

	uc := application.NewRetryVideoJob(repo, repo, fakeVideoJobIDParser{})
	_, err := uc.Execute(context.Background(), "job-1", 0)
	if err == nil || errors.Is(err, domain.ErrJobFenced) {
		t.Fatalf("error = %v, want the raw transition error, not %v", err, domain.ErrJobFenced)
	}
}
