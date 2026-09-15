package application_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"video-processor/internal/video/domain"
)

// unavailable builds an error shaped like the one the PostgreSQL adapter
// returns for a server that could not answer: the domain sentinel wrapping
// whatever the driver reported.
func unavailable(detail string) error {
	return fmt.Errorf("%w: %w", domain.ErrRepositoryUnavailable, errors.New(detail))
}

// claimThenFailRepository performs the queued -> processing claim and only
// then reports the repository as unavailable, standing in for the branch a
// real database cannot be made to take on demand: a connection lost between
// the statement committing and its result being read.
//
// It exists because the fake repository's claimErr short-circuits before the
// write, which builds the other branch — the statement that never reached the
// server — and the two are only distinguishable by what the stored row does
// afterwards.
type claimThenFailRepository struct {
	*fakeVideoJobRepository
	err error
}

func (r *claimThenFailRepository) ClaimForProcessing(ctx context.Context, job *domain.VideoJob) (bool, int64, error) {
	if _, _, err := r.fakeVideoJobRepository.ClaimForProcessing(ctx, job); err != nil {
		return false, 0, err
	}
	return false, 0, r.err
}

// TestProcessVideoJob_ClaimOutcomeUnknown_StopsBeforeAnySideEffect covers the
// origins the sentinel admits, because they differ only in respects this use
// case may not depend on: whether a claim was attempted at all, and what the
// stored row carries afterwards. Execute behaves identically under each. What
// it asserts of all of them is the intersection — no lease, no download, no
// extraction, no terminal write — which is the licence the worker's
// disposition rests on.
func TestProcessVideoJob_ClaimOutcomeUnknown_StopsBeforeAnySideEffect(t *testing.T) {
	cases := []struct {
		name string
		// repoFor wraps the seeded fake in whatever stands in for the
		// unavailable server on this branch.
		repoFor func(repo *fakeVideoJobRepository) domain.VideoJobRepository
		// wantClaimCalls is how many claim statements were issued, which
		// is what separates the load origin from the two claim ones.
		wantClaimCalls int
		// wantStatus is what the stored row carries afterwards. It pins
		// the stub rather than the use case: Execute neither reads the
		// row on this path nor asserts anything about it, and the value
		// is here only to prove the claim branches were really built.
		// Empty means the case asserts nothing about the row.
		wantStatus domain.JobStatus
	}{
		{
			name: "the authoritative load before the claim could not be answered",
			repoFor: func(repo *fakeVideoJobRepository) domain.VideoJobRepository {
				repo.findErr = unavailable("dial tcp 127.0.0.1:5432: connect: connection refused")
				return repo
			},
			wantClaimCalls: 0,
		},
		{
			name: "the claim statement never reached the server",
			repoFor: func(repo *fakeVideoJobRepository) domain.VideoJobRepository {
				repo.claimErr = unavailable("dial tcp 127.0.0.1:5432: connect: connection refused")
				return repo
			},
			wantClaimCalls: 1,
			wantStatus:     domain.JobStatusQueued,
		},
		{
			name: "the claim committed and its result was lost",
			repoFor: func(repo *fakeVideoJobRepository) domain.VideoJobRepository {
				return &claimThenFailRepository{
					fakeVideoJobRepository: repo,
					err:                    unavailable("unexpected EOF"),
				}
			},
			wantClaimCalls: 1,
			wantStatus:     domain.JobStatusProcessing,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeVideoJobRepository()
			newQueuedRepoJob(t, repo, "job-1", "user-1")

			extractor := &countingFrameExtractor{}
			sources := seededSources(t)
			leases := newFakeJobLeaseStore()

			uc := newProcessVideoJobUseCaseWithLeases(tc.repoFor(repo), extractor, sources, newFakeResultStorage(), leases)
			_, err := uc.Execute(context.Background(), "job-1", testSourceKey(t))

			if !errors.Is(err, domain.ErrJobClaimOutcomeUnknown) {
				t.Fatalf("error = %v, want it to carry %v", err, domain.ErrJobClaimOutcomeUnknown)
			}
			// The original stays reachable, so the layer that raised
			// the condition is not lost to the conversion.
			if !errors.Is(err, domain.ErrRepositoryUnavailable) {
				t.Fatalf("error = %v, want it to also carry %v", err, domain.ErrRepositoryUnavailable)
			}
			// The two sentinels answer different questions, and the
			// worker gives them opposite dispositions on that
			// difference alone.
			if errors.Is(err, domain.ErrJobClaimLost) {
				t.Fatalf("error = %v, want it not to carry %v", err, domain.ErrJobClaimLost)
			}

			if repo.claimCalls != tc.wantClaimCalls {
				t.Fatalf("repo.claimCalls = %d, want %d", repo.claimCalls, tc.wantClaimCalls)
			}
			if leases.acquires != 0 {
				t.Fatalf("lease acquires = %d, want 0 — a claim whose outcome is unknown holds nothing", leases.acquires)
			}
			if sources.downloads() != 0 {
				t.Fatalf("source downloaded %d times, want 0", sources.downloads())
			}
			if extractor.calls != 0 {
				t.Fatalf("ExtractFrames called %d times, want 0", extractor.calls)
			}
			if repo.updateCalls != 0 {
				t.Fatalf("repo.updateCalls = %d, want 0 — no FailJob may be attempted here", repo.updateCalls)
			}

			if tc.wantStatus == "" {
				return
			}
			job, err := repo.FindByID(context.Background(), newTestVideoJobID(t, "job-1"))
			if err != nil {
				t.Fatalf("unexpected error loading the job: %v", err)
			}
			if job.Status() != tc.wantStatus {
				t.Fatalf("job.Status() = %v, want %v", job.Status(), tc.wantStatus)
			}
		})
	}
}

// TestProcessVideoJob_FailJobUnavailable_IsNotAnUnknownClaimOutcome is the
// test that keeps the conversion site-scoped. The repository marks
// unavailability on every method, so the same evidence reaches Execute from
// the terminal write as from the claim — and there it means the opposite:
// the claim was won, the row is processing, and a redelivery could only lose
// the claim. The worker must keep dead-lettering it and the sweeper must keep
// owning the row.
//
// It fails if the conversion is moved into StartProcessing's sibling FailJob,
// or into the repository as a blanket marking the caller branches on wherever
// it appears.
func TestProcessVideoJob_FailJobUnavailable_IsNotAnUnknownClaimOutcome(t *testing.T) {
	repo := newFakeVideoJobRepository()
	newQueuedRepoJob(t, repo, "job-1", "user-1")
	repo.updateErr = unavailable("dial tcp 127.0.0.1:5432: connect: connection refused")

	extractor := fakeFrameExtractor{err: errors.New("ffmpeg exited with status 1")}
	uc := newProcessVideoJobUseCase(repo, extractor, seededSources(t), newFakeResultStorage())

	_, err := uc.Execute(context.Background(), "job-1", testSourceKey(t))

	// The claim was won and the failure write is what could not commit, so
	// the marker is present and the conversion is not.
	if !errors.Is(err, domain.ErrRepositoryUnavailable) {
		t.Fatalf("error = %v, want it to carry %v", err, domain.ErrRepositoryUnavailable)
	}
	if errors.Is(err, domain.ErrJobClaimOutcomeUnknown) {
		t.Fatalf("error = %v, want it not to carry %v — the claim was won, so its outcome is known", err, domain.ErrJobClaimOutcomeUnknown)
	}
	if repo.updateCalls == 0 {
		t.Fatal("repo.updateCalls = 0, want the failure write to have been attempted")
	}
}

// TestProcessVideoJob_MalformedJobID_IsNotAnUnknownClaimOutcome pins the
// other end of the same boundary. Execute parses the identifier before it
// reaches the claim step at all, so a message carrying a malformed one is a
// permanent decode failure that belongs with the caller's own, not with a
// dependency outage.
func TestProcessVideoJob_MalformedJobID_IsNotAnUnknownClaimOutcome(t *testing.T) {
	repo := newFakeVideoJobRepository()
	extractor := &countingFrameExtractor{}

	uc := newProcessVideoJobUseCase(repo, extractor, seededSources(t), newFakeResultStorage())
	_, err := uc.Execute(context.Background(), "", testSourceKey(t))

	if !errors.Is(err, domain.ErrInvalidVideoJobID) {
		t.Fatalf("error = %v, want %v", err, domain.ErrInvalidVideoJobID)
	}
	if errors.Is(err, domain.ErrJobClaimOutcomeUnknown) {
		t.Fatalf("error = %v, want it not to carry %v", err, domain.ErrJobClaimOutcomeUnknown)
	}
	if errors.Is(err, domain.ErrRepositoryUnavailable) {
		t.Fatalf("error = %v, want it not to carry %v", err, domain.ErrRepositoryUnavailable)
	}
	if repo.claimCalls != 0 {
		t.Fatalf("repo.claimCalls = %d, want 0 — the identifier is rejected before the claim step", repo.claimCalls)
	}
	if extractor.calls != 0 {
		t.Fatalf("ExtractFrames called %d times, want 0", extractor.calls)
	}
}

// TestProcessVideoJob_ClaimStepErrorsThatAreNotUnavailability_AreNotConverted
// pins the gate on the conversion rather than its location. The rejected
// design is "requeue anything that fails before the claim", and nothing else
// in this package fails under it: the lost-claim tests assert the sentinel is
// present, which stays true when the error is additionally wrapped.
//
// The second case is the one that rejected design is rejected for. A row the
// aggregate cannot be reconstructed from, or a value database/sql cannot
// convert, fails permanently on a perfectly healthy server. Converting it
// would tell the worker to ask for the message again forever, and at a
// prefetch of one that blocks every replica against healthy work.
func TestProcessVideoJob_ClaimStepErrorsThatAreNotUnavailability_AreNotConverted(t *testing.T) {
	cases := []struct {
		name  string
		setup func(repo *fakeVideoJobRepository)
	}{
		{
			name:  "a lost claim stays a lost claim",
			setup: func(repo *fakeVideoJobRepository) { repo.claimLoses = true },
		},
		{
			name: "a row that was received and could not be used is permanent",
			setup: func(repo *fakeVideoJobRepository) {
				// Deliberately unmarked: the server answered, so the
				// repository refuses it the unavailability sentinel and
				// this use case must refuse it the conversion.
				repo.findErr = errors.New("sql: Scan error on column index 7: converting NULL to string is unsupported")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeVideoJobRepository()
			newQueuedRepoJob(t, repo, "job-1", "user-1")
			tc.setup(repo)

			extractor := &countingFrameExtractor{}
			uc := newProcessVideoJobUseCase(repo, extractor, seededSources(t), newFakeResultStorage())
			_, err := uc.Execute(context.Background(), "job-1", testSourceKey(t))

			if err == nil {
				t.Fatal("error = nil, want the claim step to have failed")
			}
			if errors.Is(err, domain.ErrJobClaimOutcomeUnknown) {
				t.Fatalf("error = %v, want it not to carry %v — only a server that could not answer converts", err, domain.ErrJobClaimOutcomeUnknown)
			}
			if errors.Is(err, domain.ErrRepositoryUnavailable) {
				t.Fatalf("error = %v, want it not to carry %v", err, domain.ErrRepositoryUnavailable)
			}
			if extractor.calls != 0 {
				t.Fatalf("ExtractFrames called %d times, want 0", extractor.calls)
			}
		})
	}
}
