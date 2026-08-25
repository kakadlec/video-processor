package application_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
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

func (r *fakeVideoJobRepository) FindCompletedByUserID(_ context.Context, userID domain.UserID) ([]*domain.VideoJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var matches []*domain.VideoJob
	for _, job := range r.byID {
		if job.UserID().Equal(userID) && job.Status() == domain.JobStatusCompleted {
			matches = append(matches, cloneVideoJob(job))
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		if !matches[i].CreatedAt().Equal(matches[j].CreatedAt()) {
			return matches[i].CreatedAt().After(matches[j].CreatedAt())
		}
		return matches[i].ID().String() < matches[j].ID().String()
	})
	return matches, nil
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

// fakeResultStorage is an in-memory domain.ResultStorage for use-case tests,
// with injectable failures so the storage-failure path can be exercised
// without an unreachable bucket.
type fakeResultStorage struct {
	mu      sync.Mutex
	objects map[string][]byte
	times   map[string]time.Time

	putErr  error
	statErr error
	openErr error
}

func newFakeResultStorage() *fakeResultStorage {
	return &fakeResultStorage{objects: make(map[string][]byte), times: make(map[string]time.Time)}
}

func (s *fakeResultStorage) Put(_ context.Context, key domain.StorageKey, localPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.putErr != nil {
		return s.putErr
	}
	data, err := os.ReadFile(localPath) // #nosec G304
	if err != nil {
		return err
	}
	s.objects[key.String()] = data
	s.times[key.String()] = time.Now()
	return nil
}

func (s *fakeResultStorage) Open(_ context.Context, key domain.StorageKey) (io.ReadCloser, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.openErr != nil {
		return nil, 0, s.openErr
	}
	data, ok := s.objects[key.String()]
	if !ok {
		return nil, 0, domain.ErrResultNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
}

func (s *fakeResultStorage) Stat(_ context.Context, key domain.StorageKey) (int64, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.statErr != nil {
		return 0, time.Time{}, s.statErr
	}
	data, ok := s.objects[key.String()]
	if !ok {
		return 0, time.Time{}, domain.ErrResultNotFound
	}
	return int64(len(data)), s.times[key.String()], nil
}

func (s *fakeResultStorage) has(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.objects[key]
	return ok
}

// fakeSourceStorage is an in-memory domain.SourceStorage for use-case tests,
// so ProcessVideoJob's fetch step can be exercised without a MinIO instance.
type fakeSourceStorage struct {
	mu      sync.Mutex
	objects map[string][]byte

	putErr    error
	getErr    error
	deleteErr error
}

func newFakeSourceStorage() *fakeSourceStorage {
	return &fakeSourceStorage{objects: make(map[string][]byte)}
}

func (s *fakeSourceStorage) Put(_ context.Context, key domain.StorageKey, r io.Reader) error {
	if s.putErr != nil {
		return s.putErr
	}
	content, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key.String()] = content
	return nil
}

func (s *fakeSourceStorage) Get(_ context.Context, key domain.StorageKey, localPath string) error {
	if s.getErr != nil {
		return s.getErr
	}
	s.mu.Lock()
	content, ok := s.objects[key.String()]
	s.mu.Unlock()
	if !ok {
		return domain.ErrSourceNotFound
	}
	if err := os.MkdirAll(filepath.Dir(localPath), 0750); err != nil {
		return err
	}
	return os.WriteFile(localPath, content, 0600)
}

func (s *fakeSourceStorage) Delete(_ context.Context, key domain.StorageKey) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key.String())
	return nil
}

// store seeds an object so a test can process a source it did not upload.
func (s *fakeSourceStorage) store(key domain.StorageKey, content []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key.String()] = content
}
