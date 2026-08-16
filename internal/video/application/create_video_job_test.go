package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"video-processor/internal/video/application"
	"video-processor/internal/video/domain"
)

func mustUserID(t testing.TB, value string) domain.UserID {
	t.Helper()
	id, err := domain.NewUserID(value)
	if err != nil {
		t.Fatalf("NewUserID(%q): %v", value, err)
	}
	return id
}

func mustJobID(t testing.TB, value string) domain.VideoJobID {
	t.Helper()
	id, err := domain.NewVideoJobID(value)
	if err != nil {
		t.Fatalf("NewVideoJobID(%q): %v", value, err)
	}
	return id
}

func mustFilename(t testing.TB, value string) domain.OriginalFilename {
	t.Helper()
	filename, err := domain.NewOriginalFilename(value)
	if err != nil {
		t.Fatalf("NewOriginalFilename(%q): %v", value, err)
	}
	return filename
}

func mustRestoreJob(
	t testing.TB,
	id string,
	userID domain.UserID,
	status domain.JobStatus,
	createdAt time.Time,
) *domain.VideoJob {
	t.Helper()
	storageKey := domain.StorageKey{}
	frameCount := 0
	errorReason := ""
	if status == domain.JobStatusCompleted {
		storageKey, _ = domain.NewStorageKey("results/" + id + ".zip")
		frameCount = 42
	}
	if status == domain.JobStatusFailed {
		errorReason = "processing failed"
	}
	job, err := domain.RestoreVideoJob(
		mustJobID(t, id),
		userID,
		mustFilename(t, id+".mp4"),
		storageKey,
		frameCount,
		errorReason,
		status,
		createdAt,
	)
	if err != nil {
		t.Fatalf("RestoreVideoJob(%q): %v", id, err)
	}
	return job
}

func TestCreateVideoJob_ExecutePersistsPendingJob(t *testing.T) {
	repo := newFakeVideoJobRepository()
	jobID := mustJobID(t, "job-001")
	userID := mustUserID(t, "user-001")
	filename := mustFilename(t, "holiday.MP4")
	createdAt := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	useCase := application.NewCreateVideoJob(
		repo,
		fakeVideoJobIDGenerator{id: jobID},
		fakeClock{now: createdAt},
	)

	result, err := useCase.Execute(context.Background(), application.CreateVideoJobInput{
		UserID:           userID,
		OriginalFilename: filename,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.JobID != jobID.String() || result.UserID != userID.String() {
		t.Fatalf("result identifiers = (%q, %q), want (%q, %q)", result.JobID, result.UserID, jobID, userID)
	}
	if result.OriginalFilename != filename.String() {
		t.Fatalf("OriginalFilename = %q, want %q", result.OriginalFilename, filename.String())
	}
	if result.Status != domain.JobStatusPending || result.FrameCount != 0 {
		t.Fatalf("initial state = (%q, %d), want (pending, 0)", result.Status, result.FrameCount)
	}
	if result.ErrorReason != "" || result.StorageKey != "" {
		t.Fatalf("initial optional fields = (%q, %q), want empty", result.ErrorReason, result.StorageKey)
	}
	if !result.CreatedAt.Equal(createdAt) {
		t.Fatalf("CreatedAt = %v, want %v", result.CreatedAt, createdAt)
	}

	stored, err := repo.FindByID(context.Background(), jobID)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if stored != nil && stored.ID() != jobID {
		t.Fatalf("stored ID = %q, want %q", stored.ID(), jobID)
	}
}

func TestCreateVideoJob_RepositoryFailureIsPropagated(t *testing.T) {
	wantErr := errors.New("create failed")
	repo := newFakeVideoJobRepository()
	repo.createErr = wantErr
	useCase := application.NewCreateVideoJob(
		repo,
		fakeVideoJobIDGenerator{id: mustJobID(t, "job-001")},
		fakeClock{now: time.Now()},
	)

	result, err := useCase.Execute(context.Background(), application.CreateVideoJobInput{
		UserID:           mustUserID(t, "user-001"),
		OriginalFilename: mustFilename(t, "video.mp4"),
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Execute() error = %v, want %v", err, wantErr)
	}
	if result != (application.CreateVideoJobResult{}) {
		t.Fatalf("result = %#v, want zero value", result)
	}
}
