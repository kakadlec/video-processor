package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"video-processor/internal/video/application"
	"video-processor/internal/video/domain"
)

// seedJobAt stores a job in an arbitrary status at an arbitrary epoch. The
// terminal use cases' interesting cases are exactly the ones no legal
// sequence of transitions reaches from a single caller's point of view — a
// row another actor has already moved — so they have to be built, not driven.
func seedJobAt(t *testing.T, repo *fakeVideoJobRepository, jobID string, status domain.JobStatus, epoch int64, storageKey domain.StorageKey, frameCount int, errorReason string) *domain.VideoJob {
	t.Helper()
	job := buildJob(t, jobID, status, epoch, storageKey, frameCount, errorReason)
	repo.seed(job)
	return job
}

func buildJob(t *testing.T, jobID string, status domain.JobStatus, epoch int64, storageKey domain.StorageKey, frameCount int, errorReason string) *domain.VideoJob {
	t.Helper()
	filename, err := domain.NewOriginalFilename("movie.mp4")
	if err != nil {
		t.Fatalf("unexpected error building filename: %v", err)
	}
	job, err := domain.RestoreVideoJob(newTestVideoJobID(t, jobID), newTestVideoUserID(t, "user-1"), filename, testSourceKey(t), "", storageKey, frameCount, errorReason, status, time.Now(), epoch)
	if err != nil {
		t.Fatalf("unexpected error building job: %v", err)
	}
	return job
}

func resultKeyFor(t *testing.T, jobID string) domain.StorageKey {
	t.Helper()
	return domain.ResultStorageKey(newTestVideoJobID(t, jobID))
}

// TestTerminalUseCases_PassTheCallersEpochNotTheLoadedJobs is the assertion
// that the fence is a fence at all. Reading the epoch off the loaded job
// would make every conditional write pass in exactly the case it exists to
// reject: the row already carries the successor's epoch by the time the
// superseded holder gets there.
func TestTerminalUseCases_PassTheCallersEpochNotTheLoadedJobs(t *testing.T) {
	t.Run("CompleteJob", func(t *testing.T) {
		repo := newFakeVideoJobRepository()
		seedJobAt(t, repo, "job-1", domain.JobStatusProcessing, 4, domain.StorageKey{}, 0, "")

		uc := application.NewCompleteJob(repo, repo, fakeVideoJobIDParser{})
		if _, err := uc.Execute(context.Background(), application.CompleteJobInput{
			JobID:      "job-1",
			StorageKey: resultKeyFor(t, "job-1").String(),
			FrameCount: 2,
			LeaseEpoch: 4,
		}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if repo.lastUpdateEpoch != 4 {
			t.Fatalf("lastUpdateEpoch = %d, want 4", repo.lastUpdateEpoch)
		}
	})

	t.Run("FailJob", func(t *testing.T) {
		repo := newFakeVideoJobRepository()
		seedJobAt(t, repo, "job-1", domain.JobStatusProcessing, 4, domain.StorageKey{}, 0, "")

		uc := application.NewFailJob(repo, repo, fakeVideoJobIDParser{})
		if _, err := uc.Execute(context.Background(), application.FailJobInput{
			JobID:      "job-1",
			Reason:     "boom",
			LeaseEpoch: 4,
		}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if repo.lastUpdateEpoch != 4 {
			t.Fatalf("lastUpdateEpoch = %d, want 4", repo.lastUpdateEpoch)
		}
	})
}

// TestTerminalUseCases_ClassifyARefusedTransition walks the whole decision
// table classifyRefusedTransition owns. The distinction has teeth in the
// worker: ErrJobFenced rejects the delivery and keeps the source object,
// ErrInvalidStatusTransition is a defect, and an applied=false success acks
// and cleans up.
func TestTerminalUseCases_ClassifyARefusedTransition(t *testing.T) {
	const jobID = "job-1"

	cases := []struct {
		name    string
		status  domain.JobStatus
		epoch   int64
		key     domain.StorageKey
		frames  int
		reason  string
		held    int64
		wantErr error
		// wantUpdateCalls separates the two layers the fence lives in. A
		// refusal the aggregate can see never reaches a statement; a row
		// that moved on underneath it is caught only by the conditional
		// write, which is called and applies nothing.
		wantUpdateCalls int
	}{
		{
			name:            "requeued mid-run and re-claimed",
			status:          domain.JobStatusProcessing,
			epoch:           2,
			held:            1,
			wantErr:         domain.ErrJobFenced,
			wantUpdateCalls: 1,
		},
		{
			name:    "another actor recorded a different outcome at this epoch",
			status:  domain.JobStatusFailed,
			epoch:   1,
			reason:  "video processing was interrupted and could not be recovered",
			held:    1,
			wantErr: domain.ErrJobFenced,
		},
		{
			name:    "nothing ever enqueued it",
			status:  domain.JobStatusPending,
			epoch:   0,
			held:    0,
			wantErr: domain.ErrInvalidStatusTransition,
		},
		{
			name:   "a stale queued read at this caller's own epoch",
			status: domain.JobStatusQueued,
			epoch:  1,
			held:   1,
			// Not a fence: a real requeue always advances the epoch, so an
			// equal-epoch queued row can only be a cache artifact. The raw
			// refusal is what the caller sees.
			wantErr: domain.ErrInvalidStatusTransition,
		},
	}

	for _, tc := range cases {
		t.Run("CompleteJob/"+tc.name, func(t *testing.T) {
			repo := newFakeVideoJobRepository()
			seedJobAt(t, repo, jobID, tc.status, tc.epoch, tc.key, tc.frames, tc.reason)

			uc := application.NewCompleteJob(repo, repo, fakeVideoJobIDParser{})
			_, err := uc.Execute(context.Background(), application.CompleteJobInput{
				JobID:      jobID,
				StorageKey: resultKeyFor(t, jobID).String(),
				FrameCount: 3,
				LeaseEpoch: tc.held,
			})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if repo.updateCalls != tc.wantUpdateCalls {
				t.Fatalf("updateCalls = %d, want %d", repo.updateCalls, tc.wantUpdateCalls)
			}
		})

		t.Run("FailJob/"+tc.name, func(t *testing.T) {
			repo := newFakeVideoJobRepository()
			seedJobAt(t, repo, jobID, tc.status, tc.epoch, tc.key, tc.frames, tc.reason)

			uc := application.NewFailJob(repo, repo, fakeVideoJobIDParser{})
			_, err := uc.Execute(context.Background(), application.FailJobInput{
				JobID:      jobID,
				Reason:     "ffmpeg exploded",
				LeaseEpoch: tc.held,
			})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if repo.updateCalls != tc.wantUpdateCalls {
				t.Fatalf("updateCalls = %d, want %d", repo.updateCalls, tc.wantUpdateCalls)
			}
		})
	}
}

// TestTerminalUseCases_ARetryFindingItsOwnOutcomeReportsSuccess is what makes
// the worker's acknowledgement retryable: the response to the first commit
// can be lost, and the redelivery has to reach the same disposition — ack,
// delete the source, clear the key — rather than dead-lettering a job that
// finished.
func TestTerminalUseCases_ARetryFindingItsOwnOutcomeReportsSuccess(t *testing.T) {
	const jobID = "job-1"

	t.Run("CompleteJob", func(t *testing.T) {
		repo := newFakeVideoJobRepository()
		seedJobAt(t, repo, jobID, domain.JobStatusCompleted, 1, resultKeyFor(t, jobID), 3, "")

		uc := application.NewCompleteJob(repo, repo, fakeVideoJobIDParser{})
		result, err := uc.Execute(context.Background(), application.CompleteJobInput{
			JobID:      jobID,
			StorageKey: resultKeyFor(t, jobID).String(),
			FrameCount: 3,
			LeaseEpoch: 1,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Applied {
			t.Fatal("Applied = true, want false — the row already carried this outcome")
		}
		if result.Status != string(domain.JobStatusCompleted) {
			t.Fatalf("Status = %q, want %q", result.Status, domain.JobStatusCompleted)
		}
	})

	t.Run("FailJob", func(t *testing.T) {
		repo := newFakeVideoJobRepository()
		seedJobAt(t, repo, jobID, domain.JobStatusFailed, 1, domain.StorageKey{}, 0, "ffmpeg exploded")

		uc := application.NewFailJob(repo, repo, fakeVideoJobIDParser{})
		result, err := uc.Execute(context.Background(), application.FailJobInput{
			JobID:      jobID,
			Reason:     "ffmpeg exploded",
			LeaseEpoch: 1,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Applied {
			t.Fatal("Applied = true, want false — the row already carried this outcome")
		}
	})
}

// TestStartProcessing_ClaimsARequeuedJobBehindAStaleCachedRecord is task
// 7.5a: the reader/writer split applied to the claim, not only to the
// terminal writes. Write-throughs are not ordered with respect to one
// another, so a claim's cache write delayed past a requeue's leaves
// processing cached against a queued row. Reading the cache there would
// refuse processing -> processing and dead-letter a recovery that had
// already succeeded — permanently, since the sweep only scans processing.
func TestStartProcessing_ClaimsARequeuedJobBehindAStaleCachedRecord(t *testing.T) {
	repo := newFakeVideoJobRepository()
	seedJobAt(t, repo, "job-1", domain.JobStatusQueued, 1, domain.StorageKey{}, 0, "")
	cached := staleReader{
		fakeVideoJobRepository: repo,
		stale:                  buildJob(t, "job-1", domain.JobStatusProcessing, 0, domain.StorageKey{}, 0, ""),
	}

	uc := application.NewStartProcessing(repo, cached, fakeVideoJobIDParser{})
	result, err := uc.Execute(context.Background(), "job-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != string(domain.JobStatusProcessing) {
		t.Fatalf("Status = %q, want %q", result.Status, domain.JobStatusProcessing)
	}
}

// TestTerminalUseCases_CommitBehindAStaleCachedRecord is 7.5's application
// half: the load has to come from the authoritative reader, or a cache entry
// left at queued makes the aggregate refuse the transition before any
// statement runs and the rightful holder can never commit at all.
func TestTerminalUseCases_CommitBehindAStaleCachedRecord(t *testing.T) {
	const jobID = "job-1"

	repo := newFakeVideoJobRepository()
	seedJobAt(t, repo, jobID, domain.JobStatusProcessing, 1, domain.StorageKey{}, 0, "")
	cached := staleReader{
		fakeVideoJobRepository: repo,
		stale:                  buildJob(t, jobID, domain.JobStatusQueued, 1, domain.StorageKey{}, 0, ""),
	}

	uc := application.NewCompleteJob(repo, cached, fakeVideoJobIDParser{})
	result, err := uc.Execute(context.Background(), application.CompleteJobInput{
		JobID:      jobID,
		StorageKey: resultKeyFor(t, jobID).String(),
		FrameCount: 3,
		LeaseEpoch: 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Applied {
		t.Fatal("Applied = false, want true")
	}
}
