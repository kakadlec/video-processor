package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"video-processor/internal/video/application"
	"video-processor/internal/video/domain"
)

func TestCreateVideoJob_Execute(t *testing.T) {
	repo := newFakeVideoJobRepository()
	id, err := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	uc := application.NewCreateVideoJob(repo, fakeVideoJobIDGenerator{id: id}, fakeClock{now: now})

	result, err := uc.Execute(context.Background(), application.CreateVideoJobInput{
		UserID:           "user-1",
		OriginalFilename: "movie.mp4",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.JobID != id.String() {
		t.Fatalf("JobID = %q, want %q", result.JobID, id.String())
	}
	if result.Status != string(domain.JobStatusPending) {
		t.Fatalf("Status = %q, want %q", result.Status, domain.JobStatusPending)
	}
	if !result.CreatedAt.Equal(now) {
		t.Fatalf("CreatedAt = %v, want %v", result.CreatedAt, now)
	}

	userID, err := domain.NewUserID("user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stored, err := repo.FindByID(context.Background(), id)
	if err != nil {
		t.Fatalf("unexpected error looking up stored job: %v", err)
	}
	if !stored.UserID().Equal(userID) {
		t.Fatalf("stored job UserID = %v, want %v", stored.UserID(), userID)
	}
	if stored.FrameCount() != 0 {
		t.Fatalf("stored job FrameCount = %d, want 0", stored.FrameCount())
	}
	if stored.ErrorReason() != "" {
		t.Fatalf("stored job ErrorReason = %q, want empty", stored.ErrorReason())
	}
}

func TestCreateVideoJob_InvalidOriginalFilename(t *testing.T) {
	repo := newFakeVideoJobRepository()
	id, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	uc := application.NewCreateVideoJob(repo, fakeVideoJobIDGenerator{id: id}, fakeClock{now: time.Now()})

	_, err := uc.Execute(context.Background(), application.CreateVideoJobInput{
		UserID:           "user-1",
		OriginalFilename: "notes.txt",
	})
	if !errors.Is(err, domain.ErrInvalidOriginalFilename) {
		t.Fatalf("error = %v, want %v", err, domain.ErrInvalidOriginalFilename)
	}
}

func TestCreateVideoJob_InvalidUserID(t *testing.T) {
	repo := newFakeVideoJobRepository()
	id, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	uc := application.NewCreateVideoJob(repo, fakeVideoJobIDGenerator{id: id}, fakeClock{now: time.Now()})

	_, err := uc.Execute(context.Background(), application.CreateVideoJobInput{
		UserID:           "",
		OriginalFilename: "movie.mp4",
	})
	if !errors.Is(err, domain.ErrInvalidUserID) {
		t.Fatalf("error = %v, want %v", err, domain.ErrInvalidUserID)
	}
}

type failingCreateRepository struct {
	*fakeVideoJobRepository
	err error
}

func (r *failingCreateRepository) Create(_ context.Context, _ *domain.VideoJob) error {
	return r.err
}

func TestCreateVideoJob_RepositoryFailure_IsPropagated(t *testing.T) {
	repoErr := errors.New("boom")
	repo := &failingCreateRepository{fakeVideoJobRepository: newFakeVideoJobRepository(), err: repoErr}
	id, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	uc := application.NewCreateVideoJob(repo, fakeVideoJobIDGenerator{id: id}, fakeClock{now: time.Now()})

	_, err := uc.Execute(context.Background(), application.CreateVideoJobInput{
		UserID:           "user-1",
		OriginalFilename: "movie.mp4",
	})
	if !errors.Is(err, repoErr) {
		t.Fatalf("error = %v, want %v", err, repoErr)
	}
}
