package domain_test

import (
	"errors"
	"testing"
	"time"

	"video-processor/internal/video/domain"
)

func validVideoJobValues(t testing.TB) (domain.VideoJobID, domain.UserID, domain.OriginalFilename) {
	t.Helper()
	id, _ := domain.NewVideoJobID("job-001")
	userID, _ := domain.NewUserID("user-001")
	filename, _ := domain.NewOriginalFilename("video.mp4")
	return id, userID, filename
}

func TestNewVideoJob_ProducesPendingInitialState(t *testing.T) {
	id, userID, filename := validVideoJobValues(t)
	createdAt := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

	job, err := domain.NewVideoJob(stubVideoJobIDGenerator{id: id}, userID, filename, createdAt)
	if err != nil {
		t.Fatalf("NewVideoJob() error = %v", err)
	}
	if !job.ID().Equal(id) || !job.UserID().Equal(userID) || !job.OriginalFilename().Equal(filename) {
		t.Fatalf("new job identifiers or filename do not match inputs")
	}
	if job.Status() != domain.JobStatusPending || job.FrameCount() != 0 {
		t.Fatalf("initial state = (%q, %d), want (pending, 0)", job.Status(), job.FrameCount())
	}
	if job.ErrorReason() != "" || !job.StorageKey().IsZero() {
		t.Fatalf("new job optional result fields must be empty")
	}
	if !job.CreatedAt().Equal(createdAt) {
		t.Fatalf("CreatedAt = %v, want %v", job.CreatedAt(), createdAt)
	}
}

func TestNewVideoJob_RequiresGenerator(t *testing.T) {
	_, userID, filename := validVideoJobValues(t)
	_, err := domain.NewVideoJob(nil, userID, filename, time.Now())
	if !errors.Is(err, domain.ErrVideoJobIDGeneratorRequired) {
		t.Fatalf("NewVideoJob() error = %v, want %v", err, domain.ErrVideoJobIDGeneratorRequired)
	}
}

func TestRestoreVideoJob_RoundTripsCompletedJob(t *testing.T) {
	id, userID, filename := validVideoJobValues(t)
	storageKey, _ := domain.NewStorageKey("results/job-001.zip")
	createdAt := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

	job, err := domain.RestoreVideoJob(
		id, userID, filename, storageKey, 120, "", domain.JobStatusCompleted, createdAt,
	)
	if err != nil {
		t.Fatalf("RestoreVideoJob() error = %v", err)
	}
	if !job.ID().Equal(id) || !job.UserID().Equal(userID) || !job.OriginalFilename().Equal(filename) {
		t.Fatal("restored identity fields do not match")
	}
	if !job.StorageKey().Equal(storageKey) || job.FrameCount() != 120 {
		t.Fatalf("restored result = (%q, %d), want (%q, 120)", job.StorageKey(), job.FrameCount(), storageKey)
	}
	if job.ErrorReason() != "" || job.Status() != domain.JobStatusCompleted || !job.CreatedAt().Equal(createdAt) {
		t.Fatalf("restored state does not match inputs")
	}
}

func TestRestoreVideoJob_EnforcesAggregateInvariants(t *testing.T) {
	id, userID, filename := validVideoJobValues(t)
	storageKey, _ := domain.NewStorageKey("results/job-001.zip")
	now := time.Now()
	tests := []struct {
		name        string
		id          domain.VideoJobID
		userID      domain.UserID
		filename    domain.OriginalFilename
		storageKey  domain.StorageKey
		frameCount  int
		errorReason string
		status      domain.JobStatus
		wantErr     error
	}{
		{"missing id", domain.VideoJobID{}, userID, filename, domain.StorageKey{}, 0, "", domain.JobStatusPending, domain.ErrVideoJobIDRequired},
		{"missing user", id, domain.UserID{}, filename, domain.StorageKey{}, 0, "", domain.JobStatusPending, domain.ErrVideoJobUserIDRequired},
		{"missing filename", id, userID, domain.OriginalFilename{}, domain.StorageKey{}, 0, "", domain.JobStatusPending, domain.ErrOriginalFilenameRequired},
		{"unknown status", id, userID, filename, domain.StorageKey{}, 0, "", domain.JobStatus("unknown"), domain.ErrInvalidJobStatus},
		{"negative frame count", id, userID, filename, storageKey, -1, "", domain.JobStatusCompleted, domain.ErrInvalidFrameCount},
		{"frames before completion", id, userID, filename, domain.StorageKey{}, 1, "", domain.JobStatusProcessing, domain.ErrFrameCountBeforeCompletion},
		{"completed without key", id, userID, filename, domain.StorageKey{}, 1, "", domain.JobStatusCompleted, domain.ErrStorageKeyRequired},
		{"key before completion", id, userID, filename, storageKey, 0, "", domain.JobStatusProcessing, domain.ErrStorageKeyBeforeCompletion},
		{"failed without reason", id, userID, filename, domain.StorageKey{}, 0, "", domain.JobStatusFailed, domain.ErrErrorReasonRequired},
		{"reason before failure", id, userID, filename, domain.StorageKey{}, 0, "boom", domain.JobStatusProcessing, domain.ErrErrorReasonBeforeFailure},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := domain.RestoreVideoJob(
				test.id, test.userID, test.filename, test.storageKey,
				test.frameCount, test.errorReason, test.status, now,
			)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("RestoreVideoJob() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}
