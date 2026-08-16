package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"video-processor/internal/video/application"
	"video-processor/internal/video/domain"
)

func newTestVideoJobID(t *testing.T, value string) domain.VideoJobID {
	t.Helper()
	id, err := domain.NewVideoJobID(value)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return id
}

func newTestVideoUserID(t *testing.T, value string) domain.UserID {
	t.Helper()
	id, err := domain.NewUserID(value)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return id
}

func TestGetJobStatus_OwnerRetrievesOwnJob(t *testing.T) {
	repo := newFakeVideoJobRepository()
	jobID := newTestVideoJobID(t, "job-1")
	ownerID := newTestVideoUserID(t, "user-1")
	filename, _ := domain.NewOriginalFilename("movie.mp4")
	job, err := domain.RestoreVideoJob(jobID, ownerID, filename, domain.StorageKey{}, 0, "", domain.JobStatusPending, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.Create(context.Background(), job); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	uc := application.NewGetJobStatus(repo, fakeVideoJobIDParser{})
	result, err := uc.Execute(context.Background(), application.GetJobStatusInput{
		RequestingUserID: "user-1",
		JobID:            "job-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != string(domain.JobStatusPending) {
		t.Fatalf("Status = %q, want %q", result.Status, domain.JobStatusPending)
	}
	if result.FrameCount != 0 {
		t.Fatalf("FrameCount = %d, want 0", result.FrameCount)
	}
	if result.ErrorReason != "" {
		t.Fatalf("ErrorReason = %q, want empty", result.ErrorReason)
	}
}

func TestGetJobStatus_CompletedJob_IncludesStorageKey(t *testing.T) {
	repo := newFakeVideoJobRepository()
	jobID := newTestVideoJobID(t, "job-1")
	ownerID := newTestVideoUserID(t, "user-1")
	filename, _ := domain.NewOriginalFilename("movie.mp4")
	storageKey, _ := domain.NewStorageKey("outputs/frames_job-1.zip")
	job, err := domain.RestoreVideoJob(jobID, ownerID, filename, storageKey, 42, "", domain.JobStatusCompleted, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.Create(context.Background(), job); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	uc := application.NewGetJobStatus(repo, fakeVideoJobIDParser{})
	result, err := uc.Execute(context.Background(), application.GetJobStatusInput{
		RequestingUserID: "user-1",
		JobID:            "job-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.StorageKey != storageKey.String() {
		t.Fatalf("StorageKey = %q, want %q", result.StorageKey, storageKey.String())
	}
	if result.FrameCount != 42 {
		t.Fatalf("FrameCount = %d, want 42", result.FrameCount)
	}
}

func TestGetJobStatus_NonOwner_RejectedAsNotFound(t *testing.T) {
	repo := newFakeVideoJobRepository()
	jobID := newTestVideoJobID(t, "job-1")
	ownerID := newTestVideoUserID(t, "user-1")
	filename, _ := domain.NewOriginalFilename("movie.mp4")
	job, err := domain.RestoreVideoJob(jobID, ownerID, filename, domain.StorageKey{}, 0, "", domain.JobStatusPending, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.Create(context.Background(), job); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	uc := application.NewGetJobStatus(repo, fakeVideoJobIDParser{})
	_, err = uc.Execute(context.Background(), application.GetJobStatusInput{
		RequestingUserID: "user-2",
		JobID:            "job-1",
	})
	if !errors.Is(err, domain.ErrVideoJobNotFound) {
		t.Fatalf("error = %v, want %v", err, domain.ErrVideoJobNotFound)
	}
}

func TestGetJobStatus_NonexistentJobID_RejectedAsNotFound(t *testing.T) {
	repo := newFakeVideoJobRepository()
	uc := application.NewGetJobStatus(repo, fakeVideoJobIDParser{})

	_, err := uc.Execute(context.Background(), application.GetJobStatusInput{
		RequestingUserID: "user-1",
		JobID:            "job-does-not-exist",
	})
	if !errors.Is(err, domain.ErrVideoJobNotFound) {
		t.Fatalf("error = %v, want %v", err, domain.ErrVideoJobNotFound)
	}
}

func TestGetJobStatus_MalformedJobID_RejectedAsInvalidInput(t *testing.T) {
	repo := newFakeVideoJobRepository()
	parseErr := domain.ErrInvalidVideoJobID
	uc := application.NewGetJobStatus(repo, fakeVideoJobIDParser{err: parseErr})

	_, err := uc.Execute(context.Background(), application.GetJobStatusInput{
		RequestingUserID: "user-1",
		JobID:            "not-a-valid-id",
	})
	if !errors.Is(err, parseErr) {
		t.Fatalf("error = %v, want %v", err, parseErr)
	}
	if errors.Is(err, domain.ErrVideoJobNotFound) {
		t.Fatal("malformed id error must be distinct from not-found")
	}
}
