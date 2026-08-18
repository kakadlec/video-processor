package application_test

import (
	"context"
	"sort"
	"sync"
	"time"

	"video-processor/internal/video/domain"
)

// fakeVideoJobRepository is an in-memory domain.VideoJobRepository used to
// unit test use cases without depending on any real persistence adapter.
// FindByUserID implements the CreatedAt-descending/VideoJobID-ascending-
// tie-breaker ordering domain.VideoJobRepository requires, so pagination
// tests are meaningful. Create/FindByID/Update all store and return
// independent clones — like the real PostgreSQL adapter, which always
// reconstructs a fresh *domain.VideoJob from scanned rows — so mutating a
// job a caller holds (e.g. via job.Enqueue()) never changes what's in the
// repository unless the caller actually calls Update. Without that, a use
// case that forgot to call Update could still pass its own tests purely
// because it mutated the same pointer the repository already held.
type fakeVideoJobRepository struct {
	mu   sync.Mutex
	byID map[string]*domain.VideoJob
}

func newFakeVideoJobRepository() *fakeVideoJobRepository {
	return &fakeVideoJobRepository{byID: make(map[string]*domain.VideoJob)}
}

func cloneVideoJob(job *domain.VideoJob) *domain.VideoJob {
	clone, err := domain.RestoreVideoJob(job.ID(), job.UserID(), job.OriginalFilename(), job.StorageKey(), job.FrameCount(), job.ErrorReason(), job.Status(), job.CreatedAt())
	if err != nil {
		panic("fakeVideoJobRepository: failed to clone video job: " + err.Error())
	}
	return clone
}

func (r *fakeVideoJobRepository) Create(_ context.Context, job *domain.VideoJob) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.byID[job.ID().String()] = cloneVideoJob(job)
	return nil
}

func (r *fakeVideoJobRepository) FindByID(_ context.Context, id domain.VideoJobID) (*domain.VideoJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	job, ok := r.byID[id.String()]
	if !ok {
		return nil, domain.ErrVideoJobNotFound
	}
	return cloneVideoJob(job), nil
}

func (r *fakeVideoJobRepository) FindByUserID(_ context.Context, userID domain.UserID, offset, limit int) ([]*domain.VideoJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var matches []*domain.VideoJob
	for _, job := range r.byID {
		if job.UserID().Equal(userID) {
			matches = append(matches, cloneVideoJob(job))
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		if !matches[i].CreatedAt().Equal(matches[j].CreatedAt()) {
			return matches[i].CreatedAt().After(matches[j].CreatedAt())
		}
		return matches[i].ID().String() < matches[j].ID().String()
	})

	if offset >= len(matches) {
		return []*domain.VideoJob{}, nil
	}
	end := offset + limit
	if end > len(matches) {
		end = len(matches)
	}
	return matches[offset:end], nil
}

func (r *fakeVideoJobRepository) Update(_ context.Context, job *domain.VideoJob) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.byID[job.ID().String()]; !ok {
		return domain.ErrVideoJobNotFound
	}
	r.byID[job.ID().String()] = cloneVideoJob(job)
	return nil
}

// fakeVideoJobIDGenerator always returns the same pre-set VideoJobID, for deterministic assertions.
type fakeVideoJobIDGenerator struct {
	id domain.VideoJobID
}

func (f fakeVideoJobIDGenerator) NewVideoJobID() domain.VideoJobID {
	return f.id
}

// fakeVideoJobIDParser delegates to domain.NewVideoJobID by default (so any
// non-empty string round-trips), or returns a pre-set error to simulate
// malformed input.
type fakeVideoJobIDParser struct {
	err error
}

func (f fakeVideoJobIDParser) ParseVideoJobID(value string) (domain.VideoJobID, error) {
	if f.err != nil {
		return domain.VideoJobID{}, f.err
	}
	return domain.NewVideoJobID(value)
}

// fakeClock always returns the same pre-set time, for deterministic assertions.
type fakeClock struct {
	now time.Time
}

func (f fakeClock) Now() time.Time {
	return f.now
}
