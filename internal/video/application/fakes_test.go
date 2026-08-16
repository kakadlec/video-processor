package application_test

import (
	"context"
	"sort"
	"sync"
	"time"

	"video-processor/internal/video/domain"
)

type fakeVideoJobRepository struct {
	mu          sync.Mutex
	byID        map[string]*domain.VideoJob
	createErr   error
	findByIDErr error
	listErr     error
	findCalls   int
	listCalls   int
}

func newFakeVideoJobRepository() *fakeVideoJobRepository {
	return &fakeVideoJobRepository{byID: make(map[string]*domain.VideoJob)}
}

func (r *fakeVideoJobRepository) Create(_ context.Context, job *domain.VideoJob) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.createErr != nil {
		return r.createErr
	}
	r.byID[job.ID().String()] = job
	return nil
}

func (r *fakeVideoJobRepository) FindByID(_ context.Context, id domain.VideoJobID) (*domain.VideoJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.findCalls++
	if r.findByIDErr != nil {
		return nil, r.findByIDErr
	}
	job, ok := r.byID[id.String()]
	if !ok {
		return nil, domain.ErrVideoJobNotFound
	}
	return job, nil
}

func (r *fakeVideoJobRepository) FindByUserID(_ context.Context, userID domain.UserID, offset, limit int) ([]*domain.VideoJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listCalls++
	if r.listErr != nil {
		return nil, r.listErr
	}

	jobs := make([]*domain.VideoJob, 0)
	for _, job := range r.byID {
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

type fakeVideoJobIDGenerator struct{ id domain.VideoJobID }

func (f fakeVideoJobIDGenerator) NewVideoJobID() domain.VideoJobID { return f.id }

type fakeVideoJobIDParser struct {
	id    domain.VideoJobID
	err   error
	calls int
}

func (f *fakeVideoJobIDParser) ParseVideoJobID(string) (domain.VideoJobID, error) {
	f.calls++
	return f.id, f.err
}

type fakeClock struct{ now time.Time }

func (f fakeClock) Now() time.Time { return f.now }

func testUserID(t testingT, value string) domain.UserID {
	t.Helper()
	id, err := domain.NewUserID(value)
	if err != nil {
		t.Fatalf("NewUserID(%q): %v", value, err)
	}
	return id
}

func testVideoJobID(t testingT, value string) domain.VideoJobID {
	t.Helper()
	id, err := domain.NewVideoJobID(value)
	if err != nil {
		t.Fatalf("NewVideoJobID(%q): %v", value, err)
	}
	return id
}

func testFilename(t testingT, value string) domain.OriginalFilename {
	t.Helper()
	filename, err := domain.NewOriginalFilename(value)
	if err != nil {
		t.Fatalf("NewOriginalFilename(%q): %v", value, err)
	}
	return filename
}

type testingT interface {
	Helper()
	Fatalf(format string, args ...any)
}

func restoreTestJob(t testingT, id, user string, createdAt time.Time, status domain.JobStatus) *domain.VideoJob {
	t.Helper()
	var storageKey domain.StorageKey
	frameCount := 0
	errorReason := ""
	if status == domain.JobStatusCompleted {
		storageKey, _ = domain.NewStorageKey("results/" + id + ".zip")
		frameCount = 3
	}
	if status == domain.JobStatusFailed {
		errorReason = "processing failed"
	}
	job, err := domain.RestoreVideoJob(
		testVideoJobID(t, id),
		testUserID(t, user),
		testFilename(t, "video.mp4"),
		storageKey,
		frameCount,
		errorReason,
		status,
		createdAt,
	)
	if err != nil {
		t.Fatalf("RestoreVideoJob: %v", err)
	}
	return job
}
