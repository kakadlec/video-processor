package domain_test

import (
	"errors"
	"testing"
	"time"

	"video-processor/internal/video/domain"
)

func validVideoJobUserID(t *testing.T) domain.UserID {
	t.Helper()
	id, err := domain.NewUserID("user-1")
	if err != nil {
		t.Fatalf("unexpected error building test user id: %v", err)
	}
	return id
}

func validVideoJobFilename(t *testing.T) domain.OriginalFilename {
	t.Helper()
	f, err := domain.NewOriginalFilename("movie.mp4")
	if err != nil {
		t.Fatalf("unexpected error building test filename: %v", err)
	}
	return f
}

func TestNewVideoJob(t *testing.T) {
	id, err := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	gen := stubVideoJobIDGenerator{id: id}
	userID := validVideoJobUserID(t)
	filename := validVideoJobFilename(t)
	now := time.Now()
	sourceKey, err := domain.NewStorageKey("uploads/upload-1_input.mp4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	job, err := domain.NewVideoJob(gen, userID, filename, sourceKey, "", now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !job.ID().Equal(id) {
		t.Fatalf("job.ID() = %v, want %v", job.ID(), id)
	}
	if !job.UserID().Equal(userID) {
		t.Fatalf("job.UserID() = %v, want %v", job.UserID(), userID)
	}
	if job.OriginalFilename() != filename {
		t.Fatalf("job.OriginalFilename() = %v, want %v", job.OriginalFilename(), filename)
	}
	if job.Status() != domain.JobStatusPending {
		t.Fatalf("job.Status() = %v, want %v", job.Status(), domain.JobStatusPending)
	}
	if job.FrameCount() != 0 {
		t.Fatalf("job.FrameCount() = %d, want 0", job.FrameCount())
	}
	if job.ErrorReason() != "" {
		t.Fatalf("job.ErrorReason() = %q, want empty", job.ErrorReason())
	}
	if !job.StorageKey().IsZero() {
		t.Fatalf("job.StorageKey() = %v, want unset", job.StorageKey())
	}
	// Asserted against a value that could not be confused with the result
	// key above: the two fields are the same type and adjacent in both
	// constructors, so a transposition compiles silently.
	if !job.SourceKey().Equal(sourceKey) {
		t.Fatalf("job.SourceKey() = %v, want %v", job.SourceKey(), sourceKey)
	}
	if !job.CreatedAt().Equal(now) {
		t.Fatalf("job.CreatedAt() = %v, want %v", job.CreatedAt(), now)
	}
}

func TestNewVideoJob_NilGenerator(t *testing.T) {
	_, err := domain.NewVideoJob(nil, validVideoJobUserID(t), validVideoJobFilename(t), domain.StorageKey{}, "", time.Now())
	if !errors.Is(err, domain.ErrVideoJobIDGeneratorRequired) {
		t.Fatalf("error = %v, want %v", err, domain.ErrVideoJobIDGeneratorRequired)
	}
}

func TestRestoreVideoJob_RequiresID(t *testing.T) {
	_, err := domain.RestoreVideoJob(domain.VideoJobID{}, validVideoJobUserID(t), validVideoJobFilename(t), domain.StorageKey{}, "", domain.StorageKey{}, 0, "", domain.JobStatusPending, time.Now())
	if !errors.Is(err, domain.ErrVideoJobIDRequired) {
		t.Fatalf("error = %v, want %v", err, domain.ErrVideoJobIDRequired)
	}
}

func TestRestoreVideoJob_RequiresUserID(t *testing.T) {
	id, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	_, err := domain.RestoreVideoJob(id, domain.UserID{}, validVideoJobFilename(t), domain.StorageKey{}, "", domain.StorageKey{}, 0, "", domain.JobStatusPending, time.Now())
	if !errors.Is(err, domain.ErrVideoJobUserIDRequired) {
		t.Fatalf("error = %v, want %v", err, domain.ErrVideoJobUserIDRequired)
	}
}

func TestRestoreVideoJob_RequiresOriginalFilename(t *testing.T) {
	id, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	_, err := domain.RestoreVideoJob(id, validVideoJobUserID(t), domain.OriginalFilename{}, domain.StorageKey{}, "", domain.StorageKey{}, 0, "", domain.JobStatusPending, time.Now())
	if !errors.Is(err, domain.ErrOriginalFilenameRequired) {
		t.Fatalf("error = %v, want %v", err, domain.ErrOriginalFilenameRequired)
	}
}

func TestRestoreVideoJob_InvalidStatusRejected(t *testing.T) {
	id, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	_, err := domain.RestoreVideoJob(id, validVideoJobUserID(t), validVideoJobFilename(t), domain.StorageKey{}, "", domain.StorageKey{}, 0, "", domain.JobStatus("bogus"), time.Now())
	if !errors.Is(err, domain.ErrInvalidJobStatus) {
		t.Fatalf("error = %v, want %v", err, domain.ErrInvalidJobStatus)
	}
}

func TestRestoreVideoJob_StorageKeySetWithoutCompletedStatusRejected(t *testing.T) {
	id, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	storageKey, _ := domain.NewStorageKey("outputs/frames_123.zip")
	_, err := domain.RestoreVideoJob(id, validVideoJobUserID(t), validVideoJobFilename(t), domain.StorageKey{}, "", storageKey, 0, "", domain.JobStatusPending, time.Now())
	if !errors.Is(err, domain.ErrStorageKeyRequiresCompletedStatus) {
		t.Fatalf("error = %v, want %v", err, domain.ErrStorageKeyRequiresCompletedStatus)
	}
}

func TestRestoreVideoJob_CompletedStatusWithoutStorageKeyRejected(t *testing.T) {
	id, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	_, err := domain.RestoreVideoJob(id, validVideoJobUserID(t), validVideoJobFilename(t), domain.StorageKey{}, "", domain.StorageKey{}, 0, "", domain.JobStatusCompleted, time.Now())
	if !errors.Is(err, domain.ErrStorageKeyRequiresCompletedStatus) {
		t.Fatalf("error = %v, want %v", err, domain.ErrStorageKeyRequiresCompletedStatus)
	}
}

func TestRestoreVideoJob_FailedStatusWithoutErrorReasonRejected(t *testing.T) {
	id, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	_, err := domain.RestoreVideoJob(id, validVideoJobUserID(t), validVideoJobFilename(t), domain.StorageKey{}, "", domain.StorageKey{}, 0, "", domain.JobStatusFailed, time.Now())
	if !errors.Is(err, domain.ErrErrorReasonRequiresFailedStatus) {
		t.Fatalf("error = %v, want %v", err, domain.ErrErrorReasonRequiresFailedStatus)
	}
}

func TestRestoreVideoJob_NegativeFrameCountRejected(t *testing.T) {
	id, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	_, err := domain.RestoreVideoJob(id, validVideoJobUserID(t), validVideoJobFilename(t), domain.StorageKey{}, "", domain.StorageKey{}, -1, "", domain.JobStatusPending, time.Now())
	if !errors.Is(err, domain.ErrFrameCountNegative) {
		t.Fatalf("error = %v, want %v", err, domain.ErrFrameCountNegative)
	}
}

func TestRestoreVideoJob_NonZeroFrameCountRequiresCompletedStatus(t *testing.T) {
	id, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	_, err := domain.RestoreVideoJob(id, validVideoJobUserID(t), validVideoJobFilename(t), domain.StorageKey{}, "", domain.StorageKey{}, 10, "", domain.JobStatusPending, time.Now())
	if !errors.Is(err, domain.ErrFrameCountRequiresCompletedStatus) {
		t.Fatalf("error = %v, want %v", err, domain.ErrFrameCountRequiresCompletedStatus)
	}
}

func TestRestoreVideoJob_ErrorReasonRequiresFailedStatus(t *testing.T) {
	id, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	_, err := domain.RestoreVideoJob(id, validVideoJobUserID(t), validVideoJobFilename(t), domain.StorageKey{}, "", domain.StorageKey{}, 0, "ffmpeg exploded", domain.JobStatusPending, time.Now())
	if !errors.Is(err, domain.ErrErrorReasonRequiresFailedStatus) {
		t.Fatalf("error = %v, want %v", err, domain.ErrErrorReasonRequiresFailedStatus)
	}
}

func TestRestoreVideoJob_CompletedJobWithStorageKeyAndFrameCount(t *testing.T) {
	id, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	storageKey, _ := domain.NewStorageKey("outputs/frames_123.zip")

	job, err := domain.RestoreVideoJob(id, validVideoJobUserID(t), validVideoJobFilename(t), domain.StorageKey{}, "", storageKey, 42, "", domain.JobStatusCompleted, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if job.FrameCount() != 42 {
		t.Fatalf("job.FrameCount() = %d, want 42", job.FrameCount())
	}
	if job.StorageKey() != storageKey {
		t.Fatalf("job.StorageKey() = %v, want %v", job.StorageKey(), storageKey)
	}
}

func TestRestoreVideoJob_FailedJobWithErrorReason(t *testing.T) {
	id, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")

	job, err := domain.RestoreVideoJob(id, validVideoJobUserID(t), validVideoJobFilename(t), domain.StorageKey{}, "", domain.StorageKey{}, 0, "ffmpeg exploded", domain.JobStatusFailed, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if job.ErrorReason() != "ffmpeg exploded" {
		t.Fatalf("job.ErrorReason() = %q, want %q", job.ErrorReason(), "ffmpeg exploded")
	}
}

// newPendingVideoJob builds a pending job that carries a source key, which
// is what every transition below needs: Enqueue rejects a job without one.
func newPendingVideoJob(t *testing.T) *domain.VideoJob {
	t.Helper()
	id, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	sourceKey, err := domain.NewStorageKey("uploads/upload-1_input.mp4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	job, err := domain.RestoreVideoJob(id, validVideoJobUserID(t), validVideoJobFilename(t), sourceKey, "", domain.StorageKey{}, 0, "", domain.JobStatusPending, time.Now())
	if err != nil {
		t.Fatalf("unexpected error building pending job: %v", err)
	}
	return job
}

func TestVideoJob_Enqueue_PendingToQueued(t *testing.T) {
	job := newPendingVideoJob(t)
	if err := job.Enqueue(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if job.Status() != domain.JobStatusQueued {
		t.Fatalf("job.Status() = %v, want %v", job.Status(), domain.JobStatusQueued)
	}
}

func TestVideoJob_Enqueue_RejectsAJobWithNoSourceKey(t *testing.T) {
	id, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	job, err := domain.RestoreVideoJob(id, validVideoJobUserID(t), validVideoJobFilename(t), domain.StorageKey{}, "", domain.StorageKey{}, 0, "", domain.JobStatusPending, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := job.Enqueue(); !errors.Is(err, domain.ErrSourceKeyRequiredToEnqueue) {
		t.Fatalf("error = %v, want %v", err, domain.ErrSourceKeyRequiredToEnqueue)
	}
	if job.Status() != domain.JobStatusPending {
		t.Fatalf("job.Status() = %v, want the job left in %v", job.Status(), domain.JobStatusPending)
	}
}

// TestRestoreVideoJob_QueuedWithNoSourceKeyIsLoadable pins a deliberate
// *absence* of validation, which is why it exists as its own test: without
// this note it reads like an oversight next to the StorageKey<->completed and
// ErrorReason<->failed pairings a few tests above.
//
// The source_key column ships with an empty default and cannot be backfilled
// — the key embeds a generated uploadID that exists nowhere else — and a row
// can legitimately already be sitting in queued or processing, because POST
// /upload drives the whole sequence inside one request and a crash or client
// disconnect strands one. Pairing the field here would turn every such row
// into a FindByID error at deploy time. The invariant lives on Enqueue
// instead, where the caller that has to satisfy it can be checked.
func TestRestoreVideoJob_QueuedWithNoSourceKeyIsLoadable(t *testing.T) {
	id, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")

	job, err := domain.RestoreVideoJob(id, validVideoJobUserID(t), validVideoJobFilename(t), domain.StorageKey{}, "", domain.StorageKey{}, 0, "", domain.JobStatusQueued, time.Now())
	if err != nil {
		t.Fatalf("unexpected error restoring a pre-migration queued row: %v", err)
	}
	if !job.SourceKey().IsZero() {
		t.Fatalf("job.SourceKey() = %v, want unset", job.SourceKey())
	}
	if job.Status() != domain.JobStatusQueued {
		t.Fatalf("job.Status() = %v, want %v", job.Status(), domain.JobStatusQueued)
	}
}

// TestRestoreVideoJob_RoundTripsBothKeys guards the transposition the two
// adjacent StorageKey parameters invite: swapping them compiles, and for a
// completed job it also passes RestoreVideoJob's own validation, surfacing
// only as GET /download/:filename rejecting every result.
func TestRestoreVideoJob_RoundTripsBothKeys(t *testing.T) {
	id, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	sourceKey, err := domain.NewStorageKey("uploads/upload-1_input.mp4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resultKey := domain.ResultStorageKey(id)

	job, err := domain.RestoreVideoJob(id, validVideoJobUserID(t), validVideoJobFilename(t), sourceKey, "", resultKey, 7, "", domain.JobStatusCompleted, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !job.SourceKey().Equal(sourceKey) {
		t.Fatalf("job.SourceKey() = %v, want %v", job.SourceKey(), sourceKey)
	}
	if !job.StorageKey().Equal(resultKey) {
		t.Fatalf("job.StorageKey() = %v, want %v", job.StorageKey(), resultKey)
	}
}

func TestVideoJob_StartProcessing_QueuedToProcessing(t *testing.T) {
	job := newPendingVideoJob(t)
	if err := job.Enqueue(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := job.StartProcessing(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if job.Status() != domain.JobStatusProcessing {
		t.Fatalf("job.Status() = %v, want %v", job.Status(), domain.JobStatusProcessing)
	}
}

func TestVideoJob_Complete_ProcessingToCompleted(t *testing.T) {
	job := newPendingVideoJob(t)
	if err := job.Enqueue(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := job.StartProcessing(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	storageKey, _ := domain.NewStorageKey("outputs/frames_123.zip")
	if err := job.Complete(storageKey, 5); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if job.Status() != domain.JobStatusCompleted {
		t.Fatalf("job.Status() = %v, want %v", job.Status(), domain.JobStatusCompleted)
	}
	if job.StorageKey() != storageKey {
		t.Fatalf("job.StorageKey() = %v, want %v", job.StorageKey(), storageKey)
	}
	if job.FrameCount() != 5 {
		t.Fatalf("job.FrameCount() = %d, want 5", job.FrameCount())
	}
}

func TestVideoJob_Complete_ZeroStorageKeyRejected(t *testing.T) {
	job := newPendingVideoJob(t)
	_ = job.Enqueue()
	_ = job.StartProcessing()
	if err := job.Complete(domain.StorageKey{}, 5); err == nil {
		t.Fatalf("expected error for zero StorageKey, got nil")
	}
	if job.Status() != domain.JobStatusProcessing {
		t.Fatalf("job.Status() = %v, want unchanged %v", job.Status(), domain.JobStatusProcessing)
	}
}

func TestVideoJob_Complete_NegativeFrameCountRejected(t *testing.T) {
	job := newPendingVideoJob(t)
	_ = job.Enqueue()
	_ = job.StartProcessing()
	storageKey, _ := domain.NewStorageKey("outputs/frames_123.zip")
	if err := job.Complete(storageKey, -1); !errors.Is(err, domain.ErrFrameCountNegative) {
		t.Fatalf("error = %v, want %v", err, domain.ErrFrameCountNegative)
	}
}

func TestVideoJob_Fail_ProcessingToFailed(t *testing.T) {
	job := newPendingVideoJob(t)
	_ = job.Enqueue()
	_ = job.StartProcessing()
	if err := job.Fail("ffmpeg exploded"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if job.Status() != domain.JobStatusFailed {
		t.Fatalf("job.Status() = %v, want %v", job.Status(), domain.JobStatusFailed)
	}
	if job.ErrorReason() != "ffmpeg exploded" {
		t.Fatalf("job.ErrorReason() = %q, want %q", job.ErrorReason(), "ffmpeg exploded")
	}
}

func TestVideoJob_Fail_EmptyReasonRejected(t *testing.T) {
	job := newPendingVideoJob(t)
	_ = job.Enqueue()
	_ = job.StartProcessing()
	if err := job.Fail(""); !errors.Is(err, domain.ErrFailureReasonRequired) {
		t.Fatalf("error = %v, want %v", err, domain.ErrFailureReasonRequired)
	}
}

func TestVideoJob_OutOfOrderTransition_RejectedWithoutMutation(t *testing.T) {
	job := newPendingVideoJob(t)

	if err := job.StartProcessing(); !errors.Is(err, domain.ErrInvalidStatusTransition) {
		t.Fatalf("error = %v, want %v", err, domain.ErrInvalidStatusTransition)
	}
	storageKey, _ := domain.NewStorageKey("outputs/frames_123.zip")
	if err := job.Complete(storageKey, 1); !errors.Is(err, domain.ErrInvalidStatusTransition) {
		t.Fatalf("error = %v, want %v", err, domain.ErrInvalidStatusTransition)
	}
	if err := job.Fail("boom"); !errors.Is(err, domain.ErrInvalidStatusTransition) {
		t.Fatalf("error = %v, want %v", err, domain.ErrInvalidStatusTransition)
	}
	if job.Status() != domain.JobStatusPending {
		t.Fatalf("job.Status() = %v, want unchanged %v", job.Status(), domain.JobStatusPending)
	}
}
