package application_test

import (
	"context"
	"sort"
	"sync"
	"time"

	"video-processor/internal/video/domain"
)

type fakeVideoJobRepository struct {
	mu                sync.Mutex
	byID              map[string]*domain.VideoJob
	createErr         error
	findByIDErr       error
	findByUserIDErr   error
	findByIDCalls     int
	findByUserIDCalls int
}

func newFakeVideoJobRepository() *fakeVideoJobRepository {
	return &fakeVideoJobRepository{byID: make(map[string]*domain.VideoJob)}
}

func (repo *fakeVideoJobRepository) Create(_ context.Context, job *domain.VideoJob) error {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if repo.createErr != nil {
		return repo.createErr
	}
	repo.byID[job.ID().String()] = job
	return nil
}

func (repo *fakeVideoJobRepository) FindByID(
	_ context.Context,
	id domain.VideoJobID,
) (*domain.VideoJob, error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	repo.findByIDCalls++
	if repo.findByIDErr != nil {
		return nil, repo.findByIDErr
	}
	job, found := repo.byID[id.String()]
	if !found {
		return nil, domain.ErrVideoJobNotFound
	}
	return job, nil
}

func (repo *fakeVideoJobRepository) FindByUserID(
	_ context.Context,
	userID domain.UserID,
	offset int,
	limit int,
) ([]*domain.VideoJob, error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	repo.findByUserIDCalls++
	if repo.findByUserIDErr != nil {
		return nil, repo.findByUserIDErr
	}

	jobs := make([]*domain.VideoJob, 0)
	for _, job := range repo.byID {
		if job.UserID().Equal(userID) {
			jobs = append(jobs, job)
		}
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].CreatedAt().Equal(jobs[j].CreatedAt()) {
			return jobs[i].ID().String() < jobs[j].ID().String()
		}
		return jobs[i].CreatedAt().After(jobs[j].CreatedAt())
	})

	if offset >= len(jobs) {
		return []*domain.VideoJob{}, nil
	}
	end := min(offset+limit, len(jobs))
	return jobs[offset:end], nil
}

type fakeVideoJobIDGenerator struct {
	id domain.VideoJobID
}

func (generator fakeVideoJobIDGenerator) NewVideoJobID() domain.VideoJobID {
	return generator.id
}

type fakeVideoJobIDParser struct {
	err error
}

func (parser fakeVideoJobIDParser) ParseVideoJobID(value string) (domain.VideoJobID, error) {
	if parser.err != nil {
		return domain.VideoJobID{}, parser.err
	}
	return domain.NewVideoJobID(value)
}

type fakeClock struct {
	now time.Time
}

func (clock fakeClock) Now() time.Time {
	return clock.now
}
