package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"video-processor/internal/video/application"
	"video-processor/internal/video/domain"
)

func TestGetJobStatus_OwnerRetrievesJob(t *testing.T) {
	repo := newFakeVideoJobRepository()
	job := restoreTestJob(t, "job-1", "user-1", time.Now(), domain.JobStatusProcessing)
	if err := repo.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	parser := &fakeVideoJobIDParser{id: job.ID()}
	uc := application.NewGetJobStatus(repo, parser)

	result, err := uc.Execute(context.Background(), application.GetJobStatusInput{
		RequestingUserID: job.UserID(),
		JobID:            job.ID().String(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.JobID != "job-1" || result.Status != domain.JobStatusProcessing || result.FrameCount != 0 || result.ErrorReason != "" || result.StorageKey != "" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestGetJobStatus_CompletedJobIncludesStorageKey(t *testing.T) {
	repo := newFakeVideoJobRepository()
	job := restoreTestJob(t, "job-1", "user-1", time.Now(), domain.JobStatusCompleted)
	_ = repo.Create(context.Background(), job)
	uc := application.NewGetJobStatus(repo, &fakeVideoJobIDParser{id: job.ID()})

	result, err := uc.Execute(context.Background(), application.GetJobStatusInput{RequestingUserID: job.UserID(), JobID: "job-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.StorageKey != "results/job-1.zip" || result.FrameCount != 3 {
		t.Fatalf("completed result = %+v", result)
	}
}

func TestGetJobStatus_NonOwnerAndMissingAreIndistinguishable(t *testing.T) {
	repo := newFakeVideoJobRepository()
	job := restoreTestJob(t, "job-1", "owner", time.Now(), domain.JobStatusPending)
	_ = repo.Create(context.Background(), job)
	uc := application.NewGetJobStatus(repo, &fakeVideoJobIDParser{id: job.ID()})

	_, err := uc.Execute(context.Background(), application.GetJobStatusInput{RequestingUserID: testUserID(t, "other"), JobID: "job-1"})
	if !errors.Is(err, domain.ErrVideoJobNotFound) {
		t.Fatalf("non-owner error = %v, want %v", err, domain.ErrVideoJobNotFound)
	}

	missingID := testVideoJobID(t, "missing")
	uc = application.NewGetJobStatus(repo, &fakeVideoJobIDParser{id: missingID})
	_, err = uc.Execute(context.Background(), application.GetJobStatusInput{RequestingUserID: job.UserID(), JobID: "missing"})
	if !errors.Is(err, domain.ErrVideoJobNotFound) {
		t.Fatalf("missing error = %v, want %v", err, domain.ErrVideoJobNotFound)
	}
}

func TestGetJobStatus_MalformedIDDoesNotQueryRepository(t *testing.T) {
	wantErr := errors.New("malformed video job id")
	repo := newFakeVideoJobRepository()
	parser := &fakeVideoJobIDParser{err: wantErr}
	uc := application.NewGetJobStatus(repo, parser)

	_, err := uc.Execute(context.Background(), application.GetJobStatusInput{
		RequestingUserID: testUserID(t, "user-1"),
		JobID:            "bad",
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if repo.findCalls != 0 {
		t.Fatalf("repository was queried %d times", repo.findCalls)
	}
}
