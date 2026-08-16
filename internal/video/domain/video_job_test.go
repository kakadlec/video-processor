package domain_test

import (
	"errors"
	"testing"
	"time"

	"video-processor/internal/video/domain"
)

func validVideoJobValues(t *testing.T) (domain.VideoJobID, domain.UserID, domain.OriginalFilename) {
	t.Helper()
	id, err := domain.NewVideoJobID("job-1")
	if err != nil {
		t.Fatal(err)
	}
	userID, err := domain.NewUserID("user-1")
	if err != nil {
		t.Fatal(err)
	}
	filename, err := domain.NewOriginalFilename("video.mp4")
	if err != nil {
		t.Fatal(err)
	}
	return id, userID, filename
}

func TestNewVideoJob(t *testing.T) {
	id, userID, filename := validVideoJobValues(t)
	createdAt := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

	job, err := domain.NewVideoJob(stubVideoJobIDGenerator{id: id}, userID, filename, createdAt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !job.ID().Equal(id) || !job.UserID().Equal(userID) || !job.OriginalFilename().Equal(filename) {
		t.Fatal("new job did not preserve its identifiers and filename")
	}
	if job.Status() != domain.JobStatusPending || job.FrameCount() != 0 || job.ErrorReason() != "" || !job.StorageKey().IsZero() {
		t.Fatal("new job did not start in the required pending state")
	}
	if !job.CreatedAt().Equal(createdAt) {
		t.Fatalf("CreatedAt = %v, want %v", job.CreatedAt(), createdAt)
	}
}

func TestNewVideoJob_RequiresGenerator(t *testing.T) {
	_, userID, filename := validVideoJobValues(t)
	_, err := domain.NewVideoJob(nil, userID, filename, time.Now())
	if !errors.Is(err, domain.ErrVideoJobIDGeneratorRequired) {
		t.Fatalf("error = %v, want %v", err, domain.ErrVideoJobIDGeneratorRequired)
	}
}

func TestRestoreVideoJob_RoundTripsAllFields(t *testing.T) {
	id, userID, filename := validVideoJobValues(t)
	storageKey, _ := domain.NewStorageKey("results/job-1.zip")
	createdAt := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

	job, err := domain.RestoreVideoJob(id, userID, filename, storageKey, 42, "", domain.JobStatusCompleted, createdAt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !job.ID().Equal(id) || !job.UserID().Equal(userID) || !job.OriginalFilename().Equal(filename) ||
		!job.StorageKey().Equal(storageKey) || job.FrameCount() != 42 || job.ErrorReason() != "" ||
		job.Status() != domain.JobStatusCompleted || !job.CreatedAt().Equal(createdAt) {
		t.Fatal("restored job did not round-trip all fields")
	}

	failed, err := domain.RestoreVideoJob(id, userID, filename, domain.StorageKey{}, 0, "decoder crashed", domain.JobStatusFailed, createdAt)
	if err != nil {
		t.Fatalf("unexpected error restoring failed job: %v", err)
	}
	if failed.ErrorReason() != "decoder crashed" || failed.Status() != domain.JobStatusFailed || !failed.StorageKey().IsZero() {
		t.Fatal("restored failed job did not preserve its failure fields")
	}
}

func TestRestoreVideoJob_ValidatesAggregateInvariants(t *testing.T) {
	id, userID, filename := validVideoJobValues(t)
	storageKey, _ := domain.NewStorageKey("results/job-1.zip")
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
		{"missing job id", domain.VideoJobID{}, userID, filename, domain.StorageKey{}, 0, "", domain.JobStatusPending, domain.ErrVideoJobIDRequired},
		{"missing user id", id, domain.UserID{}, filename, domain.StorageKey{}, 0, "", domain.JobStatusPending, domain.ErrVideoJobUserIDRequired},
		{"missing filename", id, userID, domain.OriginalFilename{}, domain.StorageKey{}, 0, "", domain.JobStatusPending, domain.ErrOriginalFilenameRequired},
		{"unknown status", id, userID, filename, domain.StorageKey{}, 0, "", domain.JobStatus("unknown"), domain.ErrInvalidJobStatus},
		{"negative frames", id, userID, filename, storageKey, -1, "", domain.JobStatusCompleted, domain.ErrInvalidFrameCount},
		{"frames before completion", id, userID, filename, domain.StorageKey{}, 1, "", domain.JobStatusProcessing, domain.ErrInvalidFrameCount},
		{"missing failed reason", id, userID, filename, domain.StorageKey{}, 0, "", domain.JobStatusFailed, domain.ErrInvalidErrorReason},
		{"reason outside failure", id, userID, filename, domain.StorageKey{}, 0, "failure", domain.JobStatusProcessing, domain.ErrInvalidErrorReason},
		{"missing completed storage", id, userID, filename, domain.StorageKey{}, 1, "", domain.JobStatusCompleted, domain.ErrInvalidResultStorageKey},
		{"storage before completion", id, userID, filename, storageKey, 0, "", domain.JobStatusProcessing, domain.ErrInvalidResultStorageKey},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := domain.RestoreVideoJob(tt.id, tt.userID, tt.filename, tt.storageKey, tt.frameCount, tt.errorReason, tt.status, now)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
