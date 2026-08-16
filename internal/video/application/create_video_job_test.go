package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"video-processor/internal/video/application"
	"video-processor/internal/video/domain"
)

func TestCreateVideoJob_Success(t *testing.T) {
	repo := newFakeVideoJobRepository()
	id := testVideoJobID(t, "job-1")
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	uc := application.NewCreateVideoJob(repo, fakeVideoJobIDGenerator{id: id}, fakeClock{now: now})

	result, err := uc.Execute(context.Background(), application.CreateVideoJobInput{
		UserID:           testUserID(t, "user-1"),
		OriginalFilename: testFilename(t, "clip.mp4"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.JobID != "job-1" || result.UserID != "user-1" || result.OriginalFilename != "clip.mp4" ||
		result.Status != domain.JobStatusPending || result.FrameCount != 0 || result.ErrorReason != "" || !result.CreatedAt.Equal(now) {
		t.Fatalf("unexpected result: %+v", result)
	}
	persisted, err := repo.FindByID(context.Background(), id)
	if err != nil {
		t.Fatalf("created job was not persisted: %v", err)
	}
	if persisted.ID().String() != result.JobID || persisted.Status() != domain.JobStatusPending {
		t.Fatal("persisted job does not match the result")
	}
}

func TestCreateVideoJob_RepositoryFailureIsPropagated(t *testing.T) {
	wantErr := errors.New("repository unavailable")
	repo := newFakeVideoJobRepository()
	repo.createErr = wantErr
	uc := application.NewCreateVideoJob(
		repo,
		fakeVideoJobIDGenerator{id: testVideoJobID(t, "job-1")},
		fakeClock{now: time.Now()},
	)

	_, err := uc.Execute(context.Background(), application.CreateVideoJobInput{
		UserID:           testUserID(t, "user-1"),
		OriginalFilename: testFilename(t, "clip.mp4"),
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}
