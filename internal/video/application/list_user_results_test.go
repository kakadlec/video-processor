package application_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"video-processor/internal/video/application"
	"video-processor/internal/video/domain"
)

// seedCompletedJob persists a completed job for userID and stores an object
// of the given size under its derived key, so ListUserResults has both halves
// of what it joins.
func seedCompletedJob(t *testing.T, repo *fakeVideoJobRepository, results *fakeResultStorage, jobID, userID string, size int, createdAt time.Time) domain.StorageKey {
	t.Helper()

	id := newTestVideoJobID(t, jobID)
	key := domain.ResultStorageKey(id)
	filename, _ := domain.NewOriginalFilename("movie.mp4")
	job, err := domain.RestoreVideoJob(id, newTestVideoUserID(t, userID), filename, domain.StorageKey{}, "", key, 1, "", domain.JobStatusCompleted, createdAt)
	if err != nil {
		t.Fatalf("unexpected error building job: %v", err)
	}
	if err := repo.Create(context.Background(), job); err != nil {
		t.Fatalf("unexpected error persisting job: %v", err)
	}

	if size > 0 {
		path := filepath.Join(t.TempDir(), "frames.zip")
		if err := os.WriteFile(path, make([]byte, size), 0600); err != nil {
			t.Fatalf("write object payload: %v", err)
		}
		if err := results.Put(context.Background(), key, path); err != nil {
			t.Fatalf("store object: %v", err)
		}
	}
	return key
}

func TestListUserResults_ReturnsOnlyTheCallersCompletedJobs(t *testing.T) {
	repo := newFakeVideoJobRepository()
	results := newFakeResultStorage()

	now := time.Now()
	mine := seedCompletedJob(t, repo, results, "job-1", "user-1", 10, now)
	seedCompletedJob(t, repo, results, "job-2", "user-2", 20, now)
	// A non-completed job for the same user must not appear.
	newPendingRepoJob(t, repo, "job-3", "user-1")

	uc := application.NewListUserResults(repo, results)
	items, err := uc.Execute(context.Background(), application.ListUserResultsInput{UserID: "user-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1: %+v", len(items), items)
	}
	if items[0].StorageKey != mine.String() {
		t.Fatalf("items[0].StorageKey = %q, want %q", items[0].StorageKey, mine.String())
	}
	if items[0].Size != 10 {
		t.Fatalf("items[0].Size = %d, want 10", items[0].Size)
	}
	if items[0].ModifiedAt.IsZero() {
		t.Fatal("expected a non-zero ModifiedAt from the stored object")
	}
}

func TestListUserResults_PreservesRepositoryOrdering(t *testing.T) {
	repo := newFakeVideoJobRepository()
	results := newFakeResultStorage()

	now := time.Now()
	older := seedCompletedJob(t, repo, results, "job-old", "user-1", 5, now.Add(-time.Hour))
	newer := seedCompletedJob(t, repo, results, "job-new", "user-1", 7, now)

	uc := application.NewListUserResults(repo, results)
	items, err := uc.Execute(context.Background(), application.ListUserResultsInput{UserID: "user-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}
	if items[0].StorageKey != newer.String() || items[1].StorageKey != older.String() {
		t.Fatalf("ordering = [%q %q], want newest first [%q %q]", items[0].StorageKey, items[1].StorageKey, newer.String(), older.String())
	}
}

// TestListUserResults_OmitsJobsWhoseObjectCannotBeStated covers the case a
// caller must not see as an error: the job row says completed, but the object
// is gone. The listing skips it and still succeeds, mirroring how the
// filesystem-backed implementation skipped a file it could not stat.
func TestListUserResults_OmitsJobsWhoseObjectCannotBeStated(t *testing.T) {
	repo := newFakeVideoJobRepository()
	results := newFakeResultStorage()

	now := time.Now()
	present := seedCompletedJob(t, repo, results, "job-present", "user-1", 12, now)
	// size 0 stores no object at all, so Stat reports not-found.
	seedCompletedJob(t, repo, results, "job-missing", "user-1", 0, now.Add(-time.Minute))

	uc := application.NewListUserResults(repo, results)
	items, err := uc.Execute(context.Background(), application.ListUserResultsInput{UserID: "user-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 || items[0].StorageKey != present.String() {
		t.Fatalf("items = %+v, want only %q", items, present.String())
	}
}

func TestListUserResults_StatFailureIsNotFatal(t *testing.T) {
	repo := newFakeVideoJobRepository()
	results := newFakeResultStorage()
	seedCompletedJob(t, repo, results, "job-1", "user-1", 10, time.Now())
	results.statErr = errors.New("bucket unreachable")

	uc := application.NewListUserResults(repo, results)
	items, err := uc.Execute(context.Background(), application.ListUserResultsInput{UserID: "user-1"})
	if err != nil {
		t.Fatalf("expected a storage outage to degrade to an empty listing, got error: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("len(items) = %d, want 0", len(items))
	}
}

func TestListUserResults_RejectsEmptyUserID(t *testing.T) {
	uc := application.NewListUserResults(newFakeVideoJobRepository(), newFakeResultStorage())
	if _, err := uc.Execute(context.Background(), application.ListUserResultsInput{UserID: ""}); err == nil {
		t.Fatal("expected an error for an empty UserID")
	}
}
