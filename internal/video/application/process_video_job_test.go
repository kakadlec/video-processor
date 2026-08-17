package application_test

import (
	"context"
	"errors"
	"testing"

	"video-processor/internal/video/application"
	"video-processor/internal/video/domain"
)

// fakeFrameExtractor returns a pre-set result or error, for deterministic
// ProcessVideoJob tests without shelling out to ffmpeg.
type fakeFrameExtractor struct {
	storageKey domain.StorageKey
	frameCount int
	imageNames []string
	err        error
}

func (f fakeFrameExtractor) ExtractFrames(_ context.Context, _ domain.VideoJobID, _ string) (domain.StorageKey, int, []string, error) {
	if f.err != nil {
		return domain.StorageKey{}, 0, nil, f.err
	}
	return f.storageKey, f.frameCount, f.imageNames, nil
}

func newProcessVideoJobUseCase(repo *fakeVideoJobRepository, extractor domain.FrameExtractor) *application.ProcessVideoJob {
	parser := fakeVideoJobIDParser{}
	return application.NewProcessVideoJob(
		application.NewEnqueueVideoJob(repo, parser),
		application.NewStartProcessing(repo, parser),
		application.NewCompleteJob(repo, parser),
		application.NewFailJob(repo, parser),
		extractor,
		parser,
	)
}

func TestProcessVideoJob_Success_CompletesJob(t *testing.T) {
	repo := newFakeVideoJobRepository()
	newPendingRepoJob(t, repo, "job-1", "user-1")

	storageKey, _ := domain.NewStorageKey("outputs/frames_job-1.zip")
	extractor := fakeFrameExtractor{storageKey: storageKey, frameCount: 3, imageNames: []string{"frame_0001.png", "frame_0002.png", "frame_0003.png"}}

	uc := newProcessVideoJobUseCase(repo, extractor)
	result, err := uc.Execute(context.Background(), "job-1", "uploads/video.mp4")
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
	if result.StorageKey != "outputs/frames_job-1.zip" {
		t.Fatalf("result.StorageKey = %q, want %q", result.StorageKey, "outputs/frames_job-1.zip")
	}

	job, err := repo.FindByID(context.Background(), newTestVideoJobID(t, "job-1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if job.Status() != domain.JobStatusCompleted {
		t.Fatalf("job.Status() = %v, want %v", job.Status(), domain.JobStatusCompleted)
	}
}

func TestProcessVideoJob_ExtractionFailure_FailsJob(t *testing.T) {
	repo := newFakeVideoJobRepository()
	newPendingRepoJob(t, repo, "job-1", "user-1")

	extractor := fakeFrameExtractor{err: errors.New("ffmpeg exploded")}

	uc := newProcessVideoJobUseCase(repo, extractor)
	result, err := uc.Execute(context.Background(), "job-1", "uploads/video.mp4")
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

	uc := newProcessVideoJobUseCase(repo, extractor)
	result, err := uc.Execute(context.Background(), "job-1", "uploads/video.mp4")
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
