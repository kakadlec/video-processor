package cache

import (
	"encoding/json"
	"testing"
	"time"

	"video-processor/internal/video/domain"
)

// stubIDParser wraps domain.NewVideoJobID's own (non-empty-only) invariant,
// standing in for a real UUID-format parser — this package doesn't care
// which concrete ID scheme is used, only that toVideoJob re-validates
// through whatever parser it's given.
type stubIDParser struct{}

func (stubIDParser) ParseVideoJobID(value string) (domain.VideoJobID, error) {
	return domain.NewVideoJobID(value)
}

func mustVideoJob(t *testing.T, status domain.JobStatus, storageKey string, frameCount int, errorReason string) *domain.VideoJob {
	t.Helper()

	id, err := domain.NewVideoJobID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("NewVideoJobID: %v", err)
	}
	userID, err := domain.NewUserID("user-1")
	if err != nil {
		t.Fatalf("NewUserID: %v", err)
	}
	filename, err := domain.NewOriginalFilename("video.mp4")
	if err != nil {
		t.Fatalf("NewOriginalFilename: %v", err)
	}
	var key domain.StorageKey
	if storageKey != "" {
		key, err = domain.NewStorageKey(storageKey)
		if err != nil {
			t.Fatalf("NewStorageKey: %v", err)
		}
	}
	job, err := domain.RestoreVideoJob(id, userID, filename, domain.StorageKey{}, key, frameCount, errorReason, status, time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("RestoreVideoJob: %v", err)
	}
	return job
}

func TestCachedJobRecord_RoundTripsEachStatus(t *testing.T) {
	tests := []struct {
		name        string
		status      domain.JobStatus
		storageKey  string
		frameCount  int
		errorReason string
	}{
		{"pending", domain.JobStatusPending, "", 0, ""},
		{"queued", domain.JobStatusQueued, "", 0, ""},
		{"processing", domain.JobStatusProcessing, "", 0, ""},
		{"completed", domain.JobStatusCompleted, "frames_job-1.zip", 42, ""},
		{"failed", domain.JobStatusFailed, "", 0, "ffmpeg exited with an error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := mustVideoJob(t, tt.status, tt.storageKey, tt.frameCount, tt.errorReason)

			data, err := json.Marshal(newCachedJobRecord(original))
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}

			var rec cachedJobRecord
			if err := json.Unmarshal(data, &rec); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}

			restored, err := rec.toVideoJob(stubIDParser{})
			if err != nil {
				t.Fatalf("toVideoJob: %v", err)
			}

			if !restored.ID().Equal(original.ID()) {
				t.Errorf("ID = %q, want %q", restored.ID().String(), original.ID().String())
			}
			if !restored.UserID().Equal(original.UserID()) {
				t.Errorf("UserID = %q, want %q", restored.UserID().String(), original.UserID().String())
			}
			if restored.OriginalFilename() != original.OriginalFilename() {
				t.Errorf("OriginalFilename = %q, want %q", restored.OriginalFilename(), original.OriginalFilename())
			}
			if restored.StorageKey() != original.StorageKey() {
				t.Errorf("StorageKey = %q, want %q", restored.StorageKey(), original.StorageKey())
			}
			if restored.FrameCount() != original.FrameCount() {
				t.Errorf("FrameCount = %d, want %d", restored.FrameCount(), original.FrameCount())
			}
			if restored.ErrorReason() != original.ErrorReason() {
				t.Errorf("ErrorReason = %q, want %q", restored.ErrorReason(), original.ErrorReason())
			}
			if restored.Status() != original.Status() {
				t.Errorf("Status = %q, want %q", restored.Status(), original.Status())
			}
			if !restored.CreatedAt().Equal(original.CreatedAt()) {
				t.Errorf("CreatedAt = %v, want %v", restored.CreatedAt(), original.CreatedAt())
			}
		})
	}
}

func TestCachedJobRecord_ToVideoJob_RejectsInvalidStatus(t *testing.T) {
	rec := cachedJobRecord{
		ID:               "11111111-1111-4111-8111-111111111111",
		UserID:           "user-1",
		OriginalFilename: "video.mp4",
		Status:           "not-a-real-status",
		CreatedAt:        time.Now(),
	}

	if _, err := rec.toVideoJob(stubIDParser{}); err == nil {
		t.Fatal("toVideoJob succeeded with an invalid status, want an error")
	}
}

func TestCachedJobRecord_ToVideoJob_RejectsEmptyUserID(t *testing.T) {
	rec := cachedJobRecord{
		ID:               "11111111-1111-4111-8111-111111111111",
		UserID:           "",
		OriginalFilename: "video.mp4",
		Status:           string(domain.JobStatusPending),
		CreatedAt:        time.Now(),
	}

	if _, err := rec.toVideoJob(stubIDParser{}); err == nil {
		t.Fatal("toVideoJob succeeded with an empty user id, want an error")
	}
}

func TestCachedJobRecord_ToVideoJob_RejectsInvalidJobID(t *testing.T) {
	rec := cachedJobRecord{
		ID:               "",
		UserID:           "user-1",
		OriginalFilename: "video.mp4",
		Status:           string(domain.JobStatusPending),
		CreatedAt:        time.Now(),
	}

	if _, err := rec.toVideoJob(stubIDParser{}); err == nil {
		t.Fatal("toVideoJob succeeded with an empty job id, want an error")
	}
}
