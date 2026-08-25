package application_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"video-processor/internal/video/application"
	"video-processor/internal/video/domain"
)

// fakeFrameExtractor returns a pre-set result or error, for deterministic
// ProcessVideoJob tests without shelling out to ffmpeg. zipPath names a real
// file so the use case's cleanup of the extracted zip is observable.
type fakeFrameExtractor struct {
	zipPath    string
	frameCount int
	imageNames []string
	err        error
}

func (f fakeFrameExtractor) ExtractFrames(_ context.Context, _ domain.VideoJobID, _ string) (string, int, []string, error) {
	if f.err != nil {
		return "", 0, nil, f.err
	}
	return f.zipPath, f.frameCount, f.imageNames, nil
}

// writeTestZip creates a stand-in for an extracted zip and returns its path.
func writeTestZip(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "frames.zip")
	if err := os.WriteFile(path, []byte("zip-bytes"), 0600); err != nil {
		t.Fatalf("write test zip: %v", err)
	}
	return path
}

func newProcessVideoJobUseCase(repo *fakeVideoJobRepository, extractor domain.FrameExtractor, sources domain.SourceStorage, results domain.ResultStorage) *application.ProcessVideoJob {
	parser := fakeVideoJobIDParser{}
	return application.NewProcessVideoJob(
		application.NewEnqueueVideoJob(repo, parser),
		application.NewStartProcessing(repo, parser),
		application.NewFailJob(repo, parser),
		extractor,
		sources,
		results,
		parser,
	)
}

// testSourceKey is the key every test in this file processes from.
func testSourceKey(t *testing.T) domain.StorageKey {
	t.Helper()
	return domain.SourceStorageKey("upload-1", "video.mp4")
}

// seededSources returns a SourceStorage already holding testSourceKey, and
// registers cleanup of the temp/ directory ProcessVideoJob downloads into.
// That directory is relative to the package under test, so without the
// cleanup a test run would leave it behind in the source tree.
func seededSources(t *testing.T) *fakeSourceStorage {
	t.Helper()
	sources := newFakeSourceStorage()
	sources.store(testSourceKey(t), []byte("\x00\x00\x00\x18ftypmp42 pretend video"))
	t.Cleanup(func() { _ = os.RemoveAll("temp") })
	return sources
}

// localSourcePathFor mirrors ProcessVideoJob's own naming, so a test can
// assert the downloaded copy was removed without reaching into the package.
func localSourcePathFor(jobID string) string {
	return filepath.Join("temp", jobID+"_source")
}

func TestProcessVideoJob_Success_ExtractsAndLeavesJobProcessing(t *testing.T) {
	repo := newFakeVideoJobRepository()
	newPendingRepoJob(t, repo, "job-1", "user-1")

	zipPath := writeTestZip(t)
	extractor := fakeFrameExtractor{zipPath: zipPath, frameCount: 3, imageNames: []string{"frame_0001.png", "frame_0002.png", "frame_0003.png"}}
	sources := seededSources(t)
	results := newFakeResultStorage()

	uc := newProcessVideoJobUseCase(repo, extractor, sources, results)
	result, err := uc.Execute(context.Background(), "job-1", testSourceKey(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected Success=true, got %+v", result)
	}
	if result.FrameCount != 3 {
		t.Fatalf("result.FrameCount = %d, want 3", result.FrameCount)
	}
	if len(result.ImageNames) != 3 {
		t.Fatalf("len(result.ImageNames) = %d, want 3", len(result.ImageNames))
	}
	wantKey := domain.ResultStorageKey(newTestVideoJobID(t, "job-1")).String()
	if result.StorageKey != wantKey {
		t.Fatalf("result.StorageKey = %q, want %q", result.StorageKey, wantKey)
	}
	if !results.has(wantKey) {
		t.Fatalf("expected the extracted zip to be stored under %q", wantKey)
	}
	if _, err := os.Stat(zipPath); !os.IsNotExist(err) {
		t.Fatalf("expected the local zip at %s to be removed, os.Stat err = %v", zipPath, err)
	}

	// ProcessVideoJob still leaves the job "processing" for its caller to
	// complete. That split no longer guards a caller failure branch — see
	// ProcessVideoJob's doc comment — it just hasn't been collapsed yet.
	job, err := repo.FindByID(context.Background(), newTestVideoJobID(t, "job-1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if job.Status() != domain.JobStatusProcessing {
		t.Fatalf("job.Status() = %v, want %v", job.Status(), domain.JobStatusProcessing)
	}
}

func TestProcessVideoJob_ExtractionFailure_FailsJob(t *testing.T) {
	repo := newFakeVideoJobRepository()
	newPendingRepoJob(t, repo, "job-1", "user-1")

	extractor := fakeFrameExtractor{err: errors.New("ffmpeg exploded")}
	sources := seededSources(t)

	uc := newProcessVideoJobUseCase(repo, extractor, sources, newFakeResultStorage())
	result, err := uc.Execute(context.Background(), "job-1", testSourceKey(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected Success=false, got %+v", result)
	}
	if result.FailureReason != "ffmpeg exploded" {
		t.Fatalf("result.FailureReason = %q, want %q", result.FailureReason, "ffmpeg exploded")
	}

	job, err := repo.FindByID(context.Background(), newTestVideoJobID(t, "job-1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if job.Status() != domain.JobStatusFailed {
		t.Fatalf("job.Status() = %v, want %v", job.Status(), domain.JobStatusFailed)
	}
	if job.ErrorReason() != "ffmpeg exploded" {
		t.Fatalf("job.ErrorReason() = %q, want %q", job.ErrorReason(), "ffmpeg exploded")
	}
}

func TestProcessVideoJob_ExtractionFailure_EmptyErrorMessage_UsesFallbackReason(t *testing.T) {
	repo := newFakeVideoJobRepository()
	newPendingRepoJob(t, repo, "job-1", "user-1")

	extractor := fakeFrameExtractor{err: errors.New("")}
	sources := seededSources(t)

	uc := newProcessVideoJobUseCase(repo, extractor, sources, newFakeResultStorage())
	result, err := uc.Execute(context.Background(), "job-1", testSourceKey(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FailureReason == "" {
		t.Fatalf("expected a non-empty fallback FailureReason")
	}

	job, err := repo.FindByID(context.Background(), newTestVideoJobID(t, "job-1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if job.ErrorReason() == "" {
		t.Fatalf("expected a non-empty ErrorReason on the persisted job")
	}
}

// TestProcessVideoJob_StorageFailure_FailsJobAndRemovesLocalZip covers the
// failure mode this change introduces: extraction succeeded, so a zip exists
// on disk, but it could not be stored. The job must end failed rather than
// reporting a StorageKey for an object that was never written, and the local
// zip must not survive — the cleanup is registered before the store attempt
// precisely so this path still runs it.
func TestProcessVideoJob_StorageFailure_FailsJobAndRemovesLocalZip(t *testing.T) {
	repo := newFakeVideoJobRepository()
	newPendingRepoJob(t, repo, "job-1", "user-1")

	zipPath := writeTestZip(t)
	extractor := fakeFrameExtractor{zipPath: zipPath, frameCount: 3, imageNames: []string{"frame_0001.png"}}
	sources := seededSources(t)
	results := newFakeResultStorage()
	results.putErr = errors.New("dial tcp 10.0.0.5:9000: connection refused, bucket \"video-results\"")

	uc := newProcessVideoJobUseCase(repo, extractor, sources, results)
	result, err := uc.Execute(context.Background(), "job-1", testSourceKey(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected Success=false, got %+v", result)
	}
	if result.StorageKey != "" {
		t.Fatalf("result.StorageKey = %q, want empty for an unstored result", result.StorageKey)
	}
	// The reason is persisted on the job and echoed to the uploader, so it
	// must carry none of the adapter's own error text — that names the
	// endpoint and bucket.
	if result.FailureReason == "" {
		t.Fatal("expected a non-empty FailureReason")
	}
	if strings.Contains(result.FailureReason, "9000") || strings.Contains(result.FailureReason, "video-results") {
		t.Fatalf("result.FailureReason = %q, must not leak the storage endpoint or bucket", result.FailureReason)
	}

	job, err := repo.FindByID(context.Background(), newTestVideoJobID(t, "job-1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if job.Status() != domain.JobStatusFailed {
		t.Fatalf("job.Status() = %v, want %v", job.Status(), domain.JobStatusFailed)
	}
	if strings.Contains(job.ErrorReason(), "9000") || strings.Contains(job.ErrorReason(), "video-results") {
		t.Fatalf("job.ErrorReason() = %q, must not leak the storage endpoint or bucket", job.ErrorReason())
	}
	if !job.StorageKey().IsZero() {
		t.Fatalf("job.StorageKey() = %q, want unset", job.StorageKey().String())
	}

	if _, err := os.Stat(zipPath); !os.IsNotExist(err) {
		t.Fatalf("expected the local zip at %s to be removed on the storage-failure path, os.Stat err = %v", zipPath, err)
	}
}

// TestProcessVideoJob_Success_RemovesDownloadedSource pins the other half of
// the local-copy contract: the downloaded video is this use case's own
// responsibility, and nothing of it survives a successful run.
func TestProcessVideoJob_Success_RemovesDownloadedSource(t *testing.T) {
	repo := newFakeVideoJobRepository()
	newPendingRepoJob(t, repo, "job-1", "user-1")

	extractor := fakeFrameExtractor{zipPath: writeTestZip(t), frameCount: 1, imageNames: []string{"frame_0001.png"}}
	sources := seededSources(t)

	uc := newProcessVideoJobUseCase(repo, extractor, sources, newFakeResultStorage())
	if _, err := uc.Execute(context.Background(), "job-1", testSourceKey(t)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(localSourcePathFor("job-1")); !os.IsNotExist(err) {
		t.Fatalf("expected the downloaded source to be removed, os.Stat err = %v", err)
	}
}

// TestProcessVideoJob_ExtractionFailure_RemovesDownloadedSource is the case
// the defer ordering exists for: the extraction-error path returns, so a
// cleanup registered after ExtractFrames would never run.
func TestProcessVideoJob_ExtractionFailure_RemovesDownloadedSource(t *testing.T) {
	repo := newFakeVideoJobRepository()
	newPendingRepoJob(t, repo, "job-1", "user-1")

	extractor := fakeFrameExtractor{err: errors.New("ffmpeg exploded")}
	sources := seededSources(t)

	uc := newProcessVideoJobUseCase(repo, extractor, sources, newFakeResultStorage())
	if _, err := uc.Execute(context.Background(), "job-1", testSourceKey(t)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(localSourcePathFor("job-1")); !os.IsNotExist(err) {
		t.Fatalf("expected the downloaded source to be removed on the extraction-failure path, os.Stat err = %v", err)
	}
}

// TestProcessVideoJob_FetchFailure_FailsJobWithoutInvokingFfmpeg covers the
// step this change adds ahead of extraction. The job must fail, ffmpeg must
// never run, and the persisted reason must carry none of the adapter's own
// error text — which names the endpoint and bucket.
func TestProcessVideoJob_FetchFailure_FailsJobWithoutInvokingFfmpeg(t *testing.T) {
	repo := newFakeVideoJobRepository()
	newPendingRepoJob(t, repo, "job-1", "user-1")

	extractor := &countingFrameExtractor{}
	sources := newFakeSourceStorage()
	sources.getErr = errors.New("dial tcp 10.0.0.5:9000: connection refused, bucket \"video-results\"")

	uc := newProcessVideoJobUseCase(repo, extractor, sources, newFakeResultStorage())
	result, err := uc.Execute(context.Background(), "job-1", testSourceKey(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected Success=false, got %+v", result)
	}
	if extractor.calls != 0 {
		t.Fatalf("ExtractFrames called %d times, want 0 — a source that could not be fetched must never reach ffmpeg", extractor.calls)
	}

	job, err := repo.FindByID(context.Background(), newTestVideoJobID(t, "job-1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if job.Status() != domain.JobStatusFailed {
		t.Fatalf("job.Status() = %v, want %v", job.Status(), domain.JobStatusFailed)
	}
	if job.ErrorReason() == "" {
		t.Fatal("expected a non-empty ErrorReason")
	}
	if strings.Contains(job.ErrorReason(), "9000") || strings.Contains(job.ErrorReason(), "video-results") {
		t.Fatalf("job.ErrorReason() = %q, must not leak the storage endpoint or bucket", job.ErrorReason())
	}
}

// countingFrameExtractor records whether ffmpeg would have been invoked.
type countingFrameExtractor struct {
	calls int
}

func (f *countingFrameExtractor) ExtractFrames(_ context.Context, _ domain.VideoJobID, _ string) (string, int, []string, error) {
	f.calls++
	return "", 0, nil, errors.New("should not be called")
}
