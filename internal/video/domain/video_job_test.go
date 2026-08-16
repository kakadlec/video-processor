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

	job, err := domain.NewVideoJob(gen, userID, filename, now)
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
	if !job.CreatedAt().Equal(now) {
		t.Fatalf("job.CreatedAt() = %v, want %v", job.CreatedAt(), now)
	}
}

func TestNewVideoJob_NilGenerator(t *testing.T) {
	_, err := domain.NewVideoJob(nil, validVideoJobUserID(t), validVideoJobFilename(t), time.Now())
	if !errors.Is(err, domain.ErrVideoJobIDGeneratorRequired) {
		t.Fatalf("error = %v, want %v", err, domain.ErrVideoJobIDGeneratorRequired)
	}
}

func TestRestoreVideoJob_RequiresID(t *testing.T) {
	_, err := domain.RestoreVideoJob(domain.VideoJobID{}, validVideoJobUserID(t), validVideoJobFilename(t), domain.StorageKey{}, 0, "", domain.JobStatusPending, time.Now())
	if !errors.Is(err, domain.ErrVideoJobIDRequired) {
		t.Fatalf("error = %v, want %v", err, domain.ErrVideoJobIDRequired)
	}
}

func TestRestoreVideoJob_RequiresUserID(t *testing.T) {
	id, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	_, err := domain.RestoreVideoJob(id, domain.UserID{}, validVideoJobFilename(t), domain.StorageKey{}, 0, "", domain.JobStatusPending, time.Now())
	if !errors.Is(err, domain.ErrVideoJobUserIDRequired) {
		t.Fatalf("error = %v, want %v", err, domain.ErrVideoJobUserIDRequired)
	}
}

func TestRestoreVideoJob_RequiresOriginalFilename(t *testing.T) {
	id, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	_, err := domain.RestoreVideoJob(id, validVideoJobUserID(t), domain.OriginalFilename{}, domain.StorageKey{}, 0, "", domain.JobStatusPending, time.Now())
	if !errors.Is(err, domain.ErrOriginalFilenameRequired) {
		t.Fatalf("error = %v, want %v", err, domain.ErrOriginalFilenameRequired)
	}
}

func TestRestoreVideoJob_InvalidStatusRejected(t *testing.T) {
	id, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	_, err := domain.RestoreVideoJob(id, validVideoJobUserID(t), validVideoJobFilename(t), domain.StorageKey{}, 0, "", domain.JobStatus("bogus"), time.Now())
	if !errors.Is(err, domain.ErrInvalidJobStatus) {
		t.Fatalf("error = %v, want %v", err, domain.ErrInvalidJobStatus)
	}
}

func TestRestoreVideoJob_StorageKeySetWithoutCompletedStatusRejected(t *testing.T) {
	id, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	storageKey, _ := domain.NewStorageKey("outputs/frames_123.zip")
	_, err := domain.RestoreVideoJob(id, validVideoJobUserID(t), validVideoJobFilename(t), storageKey, 0, "", domain.JobStatusPending, time.Now())
	if !errors.Is(err, domain.ErrStorageKeyRequiresCompletedStatus) {
		t.Fatalf("error = %v, want %v", err, domain.ErrStorageKeyRequiresCompletedStatus)
	}
}

func TestRestoreVideoJob_CompletedStatusWithoutStorageKeyRejected(t *testing.T) {
	id, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	_, err := domain.RestoreVideoJob(id, validVideoJobUserID(t), validVideoJobFilename(t), domain.StorageKey{}, 0, "", domain.JobStatusCompleted, time.Now())
	if !errors.Is(err, domain.ErrStorageKeyRequiresCompletedStatus) {
		t.Fatalf("error = %v, want %v", err, domain.ErrStorageKeyRequiresCompletedStatus)
	}
}

func TestRestoreVideoJob_NegativeFrameCountRejected(t *testing.T) {
	id, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	_, err := domain.RestoreVideoJob(id, validVideoJobUserID(t), validVideoJobFilename(t), domain.StorageKey{}, -1, "", domain.JobStatusPending, time.Now())
	if !errors.Is(err, domain.ErrFrameCountNegative) {
		t.Fatalf("error = %v, want %v", err, domain.ErrFrameCountNegative)
	}
}

func TestRestoreVideoJob_NonZeroFrameCountRequiresCompletedStatus(t *testing.T) {
	id, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	_, err := domain.RestoreVideoJob(id, validVideoJobUserID(t), validVideoJobFilename(t), domain.StorageKey{}, 10, "", domain.JobStatusPending, time.Now())
	if !errors.Is(err, domain.ErrFrameCountRequiresCompletedStatus) {
		t.Fatalf("error = %v, want %v", err, domain.ErrFrameCountRequiresCompletedStatus)
	}
}

func TestRestoreVideoJob_ErrorReasonRequiresFailedStatus(t *testing.T) {
	id, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	_, err := domain.RestoreVideoJob(id, validVideoJobUserID(t), validVideoJobFilename(t), domain.StorageKey{}, 0, "ffmpeg exploded", domain.JobStatusPending, time.Now())
	if !errors.Is(err, domain.ErrErrorReasonRequiresFailedStatus) {
		t.Fatalf("error = %v, want %v", err, domain.ErrErrorReasonRequiresFailedStatus)
	}
}

func TestRestoreVideoJob_CompletedJobWithStorageKeyAndFrameCount(t *testing.T) {
	id, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	storageKey, _ := domain.NewStorageKey("outputs/frames_123.zip")

	job, err := domain.RestoreVideoJob(id, validVideoJobUserID(t), validVideoJobFilename(t), storageKey, 42, "", domain.JobStatusCompleted, time.Now())
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

	job, err := domain.RestoreVideoJob(id, validVideoJobUserID(t), validVideoJobFilename(t), domain.StorageKey{}, 0, "ffmpeg exploded", domain.JobStatusFailed, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if job.ErrorReason() != "ffmpeg exploded" {
		t.Fatalf("job.ErrorReason() = %q, want %q", job.ErrorReason(), "ffmpeg exploded")
	}
}
