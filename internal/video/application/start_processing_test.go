package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

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

// claimRepoJob runs the real StartProcessing use case against repo's own
// queued row and returns the epoch its claim won. It exists so a test that
// needs a genuinely processing row — not merely one whose in-memory pointer
// was transitioned without ever reaching the repository's own conditional
// write — gets one the same way any real caller does.
func claimRepoJob(t *testing.T, repo *fakeVideoJobRepository, jobID string) int64 {
	t.Helper()
	claim, err := application.NewStartProcessing(repo, repo, fakeVideoJobIDParser{}).Execute(context.Background(), jobID)
	if err != nil {
		t.Fatalf("claim %s: %v", jobID, err)
	}
	return claim.LeaseEpoch
}

// newQueuedRepoJobAtEpoch persists a queued job that has already been
// requeued epoch times, standing in for a job about to be claimed for
// another attempt after that many prior recoveries or storage retries. It is
// built through domain.RestoreVideoJob directly, bypassing Enqueue/Requeue,
// because there is no transition sequence that reaches a nonzero epoch
// without also visiting processing in between, which would leave the row in
// the wrong status for this helper's one purpose: seeding a bound test.
func newQueuedRepoJobAtEpoch(t *testing.T, repo *fakeVideoJobRepository, jobID, userID string, epoch int64) *domain.VideoJob {
	t.Helper()
	filename, err := domain.NewOriginalFilename("movie.mp4")
	if err != nil {
		t.Fatalf("unexpected error building filename: %v", err)
	}
	job, err := domain.RestoreVideoJob(newTestVideoJobID(t, jobID), newTestVideoUserID(t, userID), filename, testSourceKey(t), "", domain.StorageKey{}, 0, "", domain.JobStatusQueued, time.Now(), epoch)
	if err != nil {
		t.Fatalf("unexpected error building job: %v", err)
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

// TestStartProcessing_RepositoryUnavailable_PropagatesTheMarkerUnconverted
// pins task 3.4's decision that this use case stays unchanged. It propagates
// whatever the repository reported, marker and all; deciding that an
// unanswerable claim means this caller never learned its outcome belongs to
// ProcessVideoJob, which is the one component that knows the failure happened
// at the claim step rather than after it. Converting here as well would put
// that decision in two places, and the second one would apply to callers that
// are not the worker.
func TestStartProcessing_RepositoryUnavailable_PropagatesTheMarkerUnconverted(t *testing.T) {
	cases := []struct {
		name  string
		setup func(repo *fakeVideoJobRepository)
	}{
		{
			name: "the load could not be answered",
			setup: func(repo *fakeVideoJobRepository) {
				repo.findErr = unavailable("dial tcp 127.0.0.1:5432: connect: connection refused")
			},
		},
		{
			name: "the claim could not be answered",
			setup: func(repo *fakeVideoJobRepository) {
				repo.claimErr = unavailable("unexpected EOF")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeVideoJobRepository()
			newQueuedRepoJob(t, repo, "job-1", "user-1")
			tc.setup(repo)

			uc := application.NewStartProcessing(repo, repo, fakeVideoJobIDParser{})
			_, err := uc.Execute(context.Background(), "job-1")

			if !errors.Is(err, domain.ErrRepositoryUnavailable) {
				t.Fatalf("error = %v, want it to carry %v", err, domain.ErrRepositoryUnavailable)
			}
			if errors.Is(err, domain.ErrJobClaimOutcomeUnknown) {
				t.Fatalf("error = %v, want it not to carry %v — the conversion belongs to ProcessVideoJob alone", err, domain.ErrJobClaimOutcomeUnknown)
			}
			if errors.Is(err, domain.ErrJobClaimLost) {
				t.Fatalf("error = %v, want it not to carry %v", err, domain.ErrJobClaimLost)
			}
		})
	}
}
