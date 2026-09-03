package application_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"video-processor/internal/video/application"
	"video-processor/internal/video/domain"
)

// manualTicker is the heartbeat's clock under test control. Renewal happens
// on a 30-second period in production; waiting that out in wall-clock time
// would make the test both slow and flaky, and a test that waits for less
// than one period asserts nothing at all.
type manualTicker struct {
	mu      sync.Mutex
	c       chan time.Time
	period  time.Duration
	stopped bool
}

func newManualTicker() *manualTicker {
	return &manualTicker{c: make(chan time.Time, 1)}
}

func (t *manualTicker) Ticks() <-chan time.Time { return t.c }

func (t *manualTicker) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopped = true
}

func (t *manualTicker) wasStopped() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stopped
}

func (t *manualTicker) tick() { t.c <- time.Now() }

// blockingFrameExtractor stands in for an extraction long enough to outlive
// several renewal periods: it signals that it has started and waits to be
// released.
type blockingFrameExtractor struct {
	started chan struct{}
	release chan struct{}
	zipPath string
}

func newBlockingFrameExtractor(t *testing.T) *blockingFrameExtractor {
	t.Helper()
	return &blockingFrameExtractor{
		started: make(chan struct{}),
		release: make(chan struct{}),
		zipPath: writeTestZip(t),
	}
}

func (f *blockingFrameExtractor) ExtractFrames(_ context.Context, _ domain.VideoJobID, _ string) (string, int, []string, error) {
	close(f.started)
	<-f.release
	return f.zipPath, 2, []string{"frame_0001.png", "frame_0002.png"}, nil
}

// TestProcessVideoJob_RenewsTheLeaseForTheWholeRun is task 7.7a. The
// adapter's own tests all pass against a use case that acquires once and
// never renews; what hides behind that is a healthy extraction being swept
// the moment it outlives the TTL, and then processed twice.
func TestProcessVideoJob_RenewsTheLeaseForTheWholeRun(t *testing.T) {
	repo := newFakeVideoJobRepository()
	newQueuedRepoJob(t, repo, "job-1", "user-1")

	leases := newFakeJobLeaseStore()
	extractor := newBlockingFrameExtractor(t)
	ticker := newManualTicker()

	uc := newProcessVideoJobUseCaseWithLeases(
		repo, extractor, seededSources(t), newFakeResultStorage(), leases,
		application.WithLeaseTicker(func(period time.Duration) application.LeaseTicker {
			ticker.mu.Lock()
			ticker.period = period
			ticker.mu.Unlock()
			return ticker
		}),
	)

	done := make(chan error, 1)
	go func() {
		_, err := uc.Execute(context.Background(), "job-1", testSourceKey(t))
		done <- err
	}()

	<-extractor.started
	if acquires, _, _ := leases.counts(); acquires != 1 {
		t.Fatalf("acquires = %d, want 1", acquires)
	}

	// Three periods' worth of heartbeat inside one extraction. Each send
	// blocks until the previous tick has been consumed, so observing the
	// third renewal proves the loop kept going rather than firing once.
	const beats = 3
	for i := 0; i < beats; i++ {
		ticker.tick()
	}
	waitForCondition(t, "three lease renewals", func() bool {
		_, renews, _ := leases.counts()
		return renews >= beats
	})

	close(extractor.release)
	if err := <-done; err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The heartbeat must not outlive the run. Execute's stop function
	// cancels and joins the goroutine, so by the time it has returned the
	// ticker is stopped and no further renewal can happen.
	if !ticker.wasStopped() {
		t.Fatal("the renewal ticker was not stopped when Execute returned")
	}
	_, renewsAtReturn, _ := leases.counts()
	ticker.tick()
	time.Sleep(20 * time.Millisecond)
	if _, renewsAfter, _ := leases.counts(); renewsAfter != renewsAtReturn {
		t.Fatalf("renews = %d after Execute returned, want it left at %d — the heartbeat goroutine leaked", renewsAfter, renewsAtReturn)
	}
}

// TestProcessVideoJob_ReacquiresALeaseThatLapsedMidRun covers the half of a
// refused renewal that is not a takeover. The store answers false for an
// absent key just as it does for a superseded one, and an absent key is what
// a failed initial acquire or a lapse during a Redis outage leaves behind.
// Treating both as a takeover would end the heartbeat on the one path where
// the run is still the rightful holder, leaving a live extraction invisible
// to the sweep for the rest of its life — and then requeued underneath
// itself (caught during review, Copilot PR #201).
func TestProcessVideoJob_ReacquiresALeaseThatLapsedMidRun(t *testing.T) {
	repo := newFakeVideoJobRepository()
	job := newQueuedRepoJob(t, repo, "job-1", "user-1")

	leases := newFakeJobLeaseStore()
	extractor := newBlockingFrameExtractor(t)
	ticker := newManualTicker()

	uc := newProcessVideoJobUseCaseWithLeases(
		repo, extractor, seededSources(t), newFakeResultStorage(), leases,
		application.WithLeaseTicker(func(time.Duration) application.LeaseTicker { return ticker }),
	)

	done := make(chan error, 1)
	go func() {
		_, err := uc.Execute(context.Background(), "job-1", testSourceKey(t))
		done <- err
	}()

	<-extractor.started
	leases.drop(job.ID())

	ticker.tick()
	waitForCondition(t, "the lapsed lease to be re-acquired", func() bool {
		acquires, _, _ := leases.counts()
		epoch, held := leases.epochHeld(job.ID())
		return acquires == 2 && held && epoch == 0
	})

	// The heartbeat has to still be running: a re-acquire that ended the
	// loop would leave the lease to expire again with nothing renewing it.
	ticker.tick()
	waitForCondition(t, "renewal to continue after the re-acquire", func() bool {
		_, renews, _ := leases.counts()
		return renews >= 2
	})

	close(extractor.release)
	if err := <-done; err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestProcessVideoJob_StopsRenewingOnceTakenOverAtANewerEpoch is the other
// half: a lease naming a newer epoch is a real successor, the re-acquire is
// refused, and this run must stop rather than keep the successor's lease
// alive on its behalf.
func TestProcessVideoJob_StopsRenewingOnceTakenOverAtANewerEpoch(t *testing.T) {
	repo := newFakeVideoJobRepository()
	job := newQueuedRepoJob(t, repo, "job-1", "user-1")

	leases := newFakeJobLeaseStore()
	extractor := newBlockingFrameExtractor(t)
	ticker := newManualTicker()

	uc := newProcessVideoJobUseCaseWithLeases(
		repo, extractor, seededSources(t), newFakeResultStorage(), leases,
		application.WithLeaseTicker(func(time.Duration) application.LeaseTicker { return ticker }),
	)

	done := make(chan error, 1)
	go func() {
		_, err := uc.Execute(context.Background(), "job-1", testSourceKey(t))
		done <- err
	}()

	<-extractor.started
	leases.takeOver(job.ID(), 1)

	ticker.tick()
	waitForCondition(t, "the refused re-acquire", func() bool {
		acquires, renews, _ := leases.counts()
		return acquires == 2 && renews == 1
	})

	// One more tick with nothing consuming it: the buffered channel takes
	// it, and a heartbeat that had kept running would renew again.
	ticker.tick()
	time.Sleep(20 * time.Millisecond)
	if _, renews, _ := leases.counts(); renews != 1 {
		t.Fatalf("renews = %d, want 1 — the heartbeat kept running after being taken over", renews)
	}
	if epoch, held := leases.epochHeld(job.ID()); !held || epoch != 1 {
		t.Fatalf("stored lease = (%d, %v), want the successor's epoch 1 left intact", epoch, held)
	}

	close(extractor.release)
	<-done
}

// TestProcessVideoJob_RenewalPeriodStaysUnderTheLeaseTTL pins the pair that
// makes the heartbeat a margin rather than a coin flip. The TTL lives in the
// adapter and the period in this layer, so nothing but a test connects them.
func TestProcessVideoJob_RenewalPeriodStaysUnderTheLeaseTTL(t *testing.T) {
	repo := newFakeVideoJobRepository()
	newQueuedRepoJob(t, repo, "job-1", "user-1")

	ticker := newManualTicker()
	var period time.Duration
	uc := newProcessVideoJobUseCaseWithLeases(
		repo, fakeFrameExtractor{zipPath: writeTestZip(t), frameCount: 1}, seededSources(t), newFakeResultStorage(), newFakeJobLeaseStore(),
		application.WithLeaseTicker(func(d time.Duration) application.LeaseTicker {
			period = d
			return ticker
		}),
	)
	if _, err := uc.Execute(context.Background(), "job-1", testSourceKey(t)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if period*2 > leaseTTLUnderTest {
		t.Fatalf("renewal period %v leaves no margin under the %v lease TTL — a single missed beat would expire a live job", period, leaseTTLUnderTest)
	}
}

// TestProcessVideoJob_PropagatesAFenceFromItsOwnFailure covers the case where
// the run broke and the recovery sweep had already abandoned the job: the
// failure write is fenced, and the worker has to learn that rather than see a
// plain failure, because a fenced job's source object belongs to its
// successor. The local copy is still removed — that file belongs to this
// process whatever the row says.
func TestProcessVideoJob_PropagatesAFenceFromItsOwnFailure(t *testing.T) {
	repo := newFakeVideoJobRepository()
	job := newQueuedRepoJob(t, repo, "job-1", "user-1")
	recovered, err := domain.RestoreVideoJob(job.ID(), job.UserID(), job.OriginalFilename(), job.SourceKey(), job.ContentHash(), job.StorageKey(), job.FrameCount(), job.ErrorReason(), job.Status(), job.CreatedAt(), 2)
	if err != nil {
		t.Fatalf("restore recovered job: %v", err)
	}
	repo.seed(recovered)

	// The successor: by the time this run fails, the row has been requeued
	// and re-claimed at a newer epoch.
	fenceAfterClaim := &fencingRepository{fakeVideoJobRepository: repo, atEpoch: 3}

	sources := seededSources(t)
	uc := newProcessVideoJobUseCaseWithLeases(
		fenceAfterClaim,
		fakeFrameExtractor{err: errors.New("ffmpeg exploded")},
		sources,
		newFakeResultStorage(),
		newFakeJobLeaseStore(),
	)

	result, err := uc.Execute(context.Background(), "job-1", testSourceKey(t))
	if !errors.Is(err, domain.ErrJobFenced) {
		t.Fatalf("error = %v, want %v", err, domain.ErrJobFenced)
	}
	if result.JobID != "job-1" || result.LeaseEpoch != 2 {
		t.Fatalf("fenced result = {JobID: %q, LeaseEpoch: %d}, want job-1 at the held epoch 2", result.JobID, result.LeaseEpoch)
	}

	localCopy := filepath.Join("temp", "job-1_source")
	if _, statErr := os.Stat(localCopy); !os.IsNotExist(statErr) {
		t.Fatalf("os.Stat(%q) = %v, want the local copy removed even on a fenced failure", localCopy, statErr)
	}
}

// fencingRepository advances the stored epoch the moment the claim is won,
// standing in for a sweep that requeued the job and a successor that
// re-claimed it while this run was still working.
type fencingRepository struct {
	*fakeVideoJobRepository
	atEpoch int64
}

func (r *fencingRepository) ClaimForProcessing(ctx context.Context, job *domain.VideoJob) (bool, int64, error) {
	claimed, epoch, err := r.fakeVideoJobRepository.ClaimForProcessing(ctx, job)
	if !claimed || err != nil {
		return claimed, epoch, err
	}
	stored, findErr := r.fakeVideoJobRepository.FindByID(ctx, job.ID())
	if findErr != nil {
		return false, 0, findErr
	}
	successor, restoreErr := domain.RestoreVideoJob(stored.ID(), stored.UserID(), stored.OriginalFilename(), stored.SourceKey(), stored.ContentHash(), stored.StorageKey(), stored.FrameCount(), stored.ErrorReason(), stored.Status(), stored.CreatedAt(), r.atEpoch)
	if restoreErr != nil {
		return false, 0, restoreErr
	}
	r.fakeVideoJobRepository.seed(successor)
	return claimed, epoch, nil
}

func waitForCondition(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// leaseTTLUnderTest mirrors internal/video/infrastructure/lease.TTL. It is
// duplicated rather than imported because the application layer must not
// depend on an infrastructure adapter, which is the same reason the renewal
// period is a separate constant in the first place.
const leaseTTLUnderTest = 90 * time.Second
