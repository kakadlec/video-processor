package application_test

import (
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
	mu           sync.Mutex
	byID         map[string]*domain.VideoJob
	updateCalls  int
	enqueueCalls int
	claimCalls   int
	// claimLoses makes ClaimForProcessing report a lost claim without
	// writing, standing in for another consumer that committed between the
	// caller's read and its own write.
	claimLoses bool
	claimErr   error
	// lastUpdateEpoch records the epoch the most recent Update was fenced
	// against, so a test can assert the use case passed the claim's epoch
	// rather than reading one off the job it loaded.
	lastUpdateEpoch int64
	updateErr       error
	requeueCalls    int
	requeueErr      error
}

func newFakeVideoJobRepository() *fakeVideoJobRepository {
	return &fakeVideoJobRepository{byID: make(map[string]*domain.VideoJob)}
}

func cloneVideoJob(job *domain.VideoJob) *domain.VideoJob {
	clone, err := domain.RestoreVideoJob(job.ID(), job.UserID(), job.OriginalFilename(), job.SourceKey(), job.ContentHash(), job.StorageKey(), job.FrameCount(), job.ErrorReason(), job.Status(), job.CreatedAt(), job.LeaseEpoch())
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

// Update mirrors the real adapter's conditional statement: it writes only
// when the stored row is still processing at the caller's epoch, and reports
// whether it did. Every other outcome goes through the same three-way
// classification the PostgreSQL adapter performs, so a use case cannot pass
// here by treating a fenced write as an applied one.
func (r *fakeVideoJobRepository) Update(_ context.Context, job *domain.VideoJob, epoch int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.updateCalls++
	r.lastUpdateEpoch = epoch
	if r.updateErr != nil {
		return false, r.updateErr
	}
	stored, ok := r.byID[job.ID().String()]
	if !ok {
		return false, domain.ErrVideoJobNotFound
	}
	if stored.LeaseEpoch() != epoch || stored.Status() != domain.JobStatusProcessing {
		return false, classifyRefusedFakeUpdate(stored, job, epoch)
	}
	r.byID[job.ID().String()] = cloneVideoJob(job)
	return true, nil
}

// Requeue mirrors the recovery edge: conditional on the stored row still
// being processing at the observed epoch, and advancing the epoch when it
// writes.
func (r *fakeVideoJobRepository) Requeue(_ context.Context, job *domain.VideoJob, observedEpoch int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.requeueCalls++
	if r.requeueErr != nil {
		return false, r.requeueErr
	}
	stored, ok := r.byID[job.ID().String()]
	if !ok {
		return false, domain.ErrVideoJobNotFound
	}
	if stored.Status() != domain.JobStatusProcessing || stored.LeaseEpoch() != observedEpoch {
		return false, nil
	}
	r.byID[job.ID().String()] = cloneVideoJob(job)
	return true, nil
}

func (r *fakeVideoJobRepository) FindProcessing(_ context.Context, after domain.VideoJobID, limit int) ([]*domain.VideoJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var matches []*domain.VideoJob
	for _, job := range r.byID {
		if job.Status() != domain.JobStatusProcessing {
			continue
		}
		if !after.IsZero() && job.ID().String() <= after.String() {
			continue
		}
		matches = append(matches, cloneVideoJob(job))
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].ID().String() < matches[j].ID().String()
	})
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, nil
}

// classifyRefusedFakeUpdate reproduces the PostgreSQL adapter's reading of a
// refused conditional write, so that an in-memory test sees the same errors a
// real one would.
func classifyRefusedFakeUpdate(stored, want *domain.VideoJob, epoch int64) error {
	if stored.LeaseEpoch() != epoch {
		return domain.ErrJobFenced
	}
	terminal := stored.Status() == domain.JobStatusCompleted || stored.Status() == domain.JobStatusFailed
	if !terminal {
		return domain.ErrJobFenced
	}
	sameOutcome := stored.Status() == want.Status() &&
		stored.StorageKey().String() == want.StorageKey().String() &&
		stored.FrameCount() == want.FrameCount() &&
		stored.ErrorReason() == want.ErrorReason()
	if sameOutcome {
		return nil
	}
	return domain.ErrJobFenced
}

// Enqueue records that it, rather than Update, was the path taken — the
// distinction matters because only Enqueue writes the outbox row the relay
// publishes from, so a use case that quietly fell back to Update would still
// pass every status assertion while dispatching nothing.
func (r *fakeVideoJobRepository) Enqueue(_ context.Context, job *domain.VideoJob) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.enqueueCalls++
	return r.persistLocked(job)
}

// ClaimForProcessing mirrors the real adapter: it persists only when the
// stored row is still queued, and reports whether it did.
func (r *fakeVideoJobRepository) ClaimForProcessing(_ context.Context, job *domain.VideoJob) (bool, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.claimCalls++
	if r.claimErr != nil {
		return false, 0, r.claimErr
	}
	stored, ok := r.byID[job.ID().String()]
	if !ok {
		return false, 0, domain.ErrVideoJobNotFound
	}
	if r.claimLoses || stored.Status() != domain.JobStatusQueued {
		return false, 0, nil
	}
	r.byID[job.ID().String()] = cloneVideoJob(job)
	return true, stored.LeaseEpoch(), nil
}

// seed stores a job in whatever state a test has built it, bypassing the
// conditional writes the port exposes. Test setup arrives at a state; it
// does not exercise the paths that reach it, and Update in particular will
// refuse anything that is not a terminal write at the held epoch.
func (r *fakeVideoJobRepository) seed(job *domain.VideoJob) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.byID[job.ID().String()] = cloneVideoJob(job)
}

// staleReader wraps a repository so that FindByID answers from a fixed
// record while every write still goes to the wrapped store. It is the shape
// of the caching decorator when a write-through has been delayed past a
// later one: the cached record is stale, the row underneath is not.
type staleReader struct {
	*fakeVideoJobRepository
	stale *domain.VideoJob
}

func (r staleReader) FindByID(context.Context, domain.VideoJobID) (*domain.VideoJob, error) {
	return cloneVideoJob(r.stale), nil
}

// fakeJobLeaseStore is an in-memory domain.JobLeaseStore recording every
// call, with injectable failures so both halves of the lease posture — the
// fail-open acquire/renew and the fail-closed liveness read — are testable.
type fakeJobLeaseStore struct {
	mu         sync.Mutex
	held       map[string]int64
	acquireErr error
	renewErr   error
	heldErr    error
	acquires   int
	renews     int
	releases   int
}

func newFakeJobLeaseStore() *fakeJobLeaseStore {
	return &fakeJobLeaseStore{held: make(map[string]int64)}
}

func (s *fakeJobLeaseStore) Acquire(_ context.Context, id domain.VideoJobID, epoch int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.acquires++
	if s.acquireErr != nil {
		return false, s.acquireErr
	}
	if current, ok := s.held[id.String()]; ok && current > epoch {
		return false, nil
	}
	s.held[id.String()] = epoch
	return true, nil
}

func (s *fakeJobLeaseStore) Renew(_ context.Context, id domain.VideoJobID, epoch int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.renews++
	if s.renewErr != nil {
		return false, s.renewErr
	}
	current, ok := s.held[id.String()]
	if !ok || current != epoch {
		return false, nil
	}
	return true, nil
}

func (s *fakeJobLeaseStore) Release(_ context.Context, id domain.VideoJobID, epoch int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.releases++
	if current, ok := s.held[id.String()]; ok && current == epoch {
		delete(s.held, id.String())
	}
	return nil
}

func (s *fakeJobLeaseStore) Held(_ context.Context, id domain.VideoJobID, epoch int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.heldErr != nil {
		return false, s.heldErr
	}
	current, ok := s.held[id.String()]
	return ok && current == epoch, nil
}

// drop removes the lease without touching the call counters, standing in for
// the two ways a run loses one without being superseded: an initial acquire
// that failed open, and a key that expired while Redis was unreachable.
func (s *fakeJobLeaseStore) drop(id domain.VideoJobID) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.held, id.String())
}

// takeOver stores a lease at epoch, standing in for the successor a requeue
// produced.
func (s *fakeJobLeaseStore) takeOver(id domain.VideoJobID, epoch int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.held[id.String()] = epoch
}

func (s *fakeJobLeaseStore) epochHeld(id domain.VideoJobID) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	epoch, ok := s.held[id.String()]
	return epoch, ok
}

func (s *fakeJobLeaseStore) counts() (acquires, renews, releases int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.acquires, s.renews, s.releases
}

func (r *fakeVideoJobRepository) persistLocked(job *domain.VideoJob) error {
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

	putErr     error
	statErr    error
	presignErr error
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

// PresignGet mimics the real adapter's offline signing: no object lookup, so
// an absent key yields a URL just as it does against MinIO.
func (s *fakeResultStorage) PresignGet(_ context.Context, key domain.StorageKey, ttl time.Duration, _ string) (string, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.presignErr != nil {
		return "", time.Time{}, s.presignErr
	}
	return "https://storage.test/" + key.String() + "?signature=fake", time.Now().Add(ttl), nil
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
	// getCalls counts downloads, so a test can assert that a use case
	// abandoned a job before spending the transfer rather than merely
	// cleaning up after one.
	getCalls int

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
	s.mu.Lock()
	s.getCalls++
	s.mu.Unlock()
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

func (s *fakeSourceStorage) downloads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getCalls
}

// store seeds an object so a test can process a source it did not upload.
func (s *fakeSourceStorage) store(key domain.StorageKey, content []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key.String()] = content
}
