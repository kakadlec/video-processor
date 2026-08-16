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
	owner := mustUserID(t, "owner")
	job := mustRestoreJob(t, "job-001", owner, domain.JobStatusProcessing, time.Now())
	if err := repo.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	useCase := application.NewGetJobStatus(repo, fakeVideoJobIDParser{})

	result, err := useCase.Execute(context.Background(), application.GetJobStatusInput{
		RequestingUserID: owner,
		JobID:            job.ID().String(),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.JobID != job.ID().String() || result.Status != domain.JobStatusProcessing {
		t.Fatalf("result = %#v, want processing job %q", result, job.ID())
	}
	if result.FrameCount != 0 || result.ErrorReason != "" || result.StorageKey != "" {
		t.Fatalf("unexpected processing result fields: %#v", result)
	}
}

func TestGetJobStatus_CompletedJobIncludesStorageKey(t *testing.T) {
	repo := newFakeVideoJobRepository()
	owner := mustUserID(t, "owner")
	job := mustRestoreJob(t, "job-001", owner, domain.JobStatusCompleted, time.Now())
	if err := repo.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	useCase := application.NewGetJobStatus(repo, fakeVideoJobIDParser{})

	result, err := useCase.Execute(context.Background(), application.GetJobStatusInput{
		RequestingUserID: owner,
		JobID:            job.ID().String(),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.StorageKey != job.StorageKey().String() || result.FrameCount != job.FrameCount() {
		t.Fatalf("completed result = %#v, want storage %q and %d frames", result, job.StorageKey(), job.FrameCount())
	}
}

func TestGetJobStatus_NonOwnerIsIndistinguishableFromMissingJob(t *testing.T) {
	repo := newFakeVideoJobRepository()
	job := mustRestoreJob(t, "job-001", mustUserID(t, "owner"), domain.JobStatusPending, time.Now())
	if err := repo.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	useCase := application.NewGetJobStatus(repo, fakeVideoJobIDParser{})

	_, err := useCase.Execute(context.Background(), application.GetJobStatusInput{
		RequestingUserID: mustUserID(t, "another-user"),
		JobID:            job.ID().String(),
	})
	if !errors.Is(err, domain.ErrVideoJobNotFound) {
		t.Fatalf("Execute() error = %v, want %v", err, domain.ErrVideoJobNotFound)
	}
}

func TestGetJobStatus_MissingJobReturnsNotFound(t *testing.T) {
	useCase := application.NewGetJobStatus(newFakeVideoJobRepository(), fakeVideoJobIDParser{})
	_, err := useCase.Execute(context.Background(), application.GetJobStatusInput{
		RequestingUserID: mustUserID(t, "owner"),
		JobID:            "missing-job",
	})
	if !errors.Is(err, domain.ErrVideoJobNotFound) {
		t.Fatalf("Execute() error = %v, want %v", err, domain.ErrVideoJobNotFound)
	}
}

func TestGetJobStatus_MalformedIDDoesNotQueryRepository(t *testing.T) {
	wantErr := errors.New("malformed job id")
	repo := newFakeVideoJobRepository()
	useCase := application.NewGetJobStatus(repo, fakeVideoJobIDParser{err: wantErr})

	_, err := useCase.Execute(context.Background(), application.GetJobStatusInput{
		RequestingUserID: mustUserID(t, "owner"),
		JobID:            "not-a-valid-id",
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Execute() error = %v, want parser error %v", err, wantErr)
	}
	if repo.findByIDCalls != 0 {
		t.Fatalf("FindByID calls = %d, want 0", repo.findByIDCalls)
	}
}
