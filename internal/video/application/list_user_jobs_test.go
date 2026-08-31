package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"video-processor/internal/video/application"
	"video-processor/internal/video/domain"
)

func createTestJob(t *testing.T, repo *fakeVideoJobRepository, jobID, userID string, createdAt time.Time) *domain.VideoJob {
	t.Helper()
	id := newTestVideoJobID(t, jobID)
	owner := newTestVideoUserID(t, userID)
	filename, _ := domain.NewOriginalFilename("movie.mp4")
	job, err := domain.RestoreVideoJob(id, owner, filename, domain.StorageKey{}, "", domain.StorageKey{}, 0, "", domain.JobStatusPending, createdAt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.Create(context.Background(), job); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return job
}

func TestListUserJobs_ScopedToCaller(t *testing.T) {
	repo := newFakeVideoJobRepository()
	now := time.Now()
	createTestJob(t, repo, "job-a1", "user-a", now)
	createTestJob(t, repo, "job-b1", "user-b", now)

	uc := application.NewListUserJobs(repo)
	results, err := uc.Execute(context.Background(), application.ListUserJobsInput{UserID: "user-a", Offset: 0, Limit: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].JobID != "job-a1" {
		t.Fatalf("JobID = %q, want %q", results[0].JobID, "job-a1")
	}
}

func TestListUserJobs_OrderedNewestFirst(t *testing.T) {
	repo := newFakeVideoJobRepository()
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	createTestJob(t, repo, "job-1", "user-a", base)
	createTestJob(t, repo, "job-2", "user-a", base.Add(time.Hour))
	createTestJob(t, repo, "job-3", "user-a", base.Add(2*time.Hour))

	uc := application.NewListUserJobs(repo)
	results, err := uc.Execute(context.Background(), application.ListUserJobsInput{UserID: "user-a", Offset: 0, Limit: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"job-3", "job-2", "job-1"}
	if len(results) != len(want) {
		t.Fatalf("len(results) = %d, want %d", len(results), len(want))
	}
	for i, id := range want {
		if results[i].JobID != id {
			t.Fatalf("results[%d].JobID = %q, want %q", i, results[i].JobID, id)
		}
	}
}

func TestListUserJobs_TiesBrokenByVideoJobIDAscending(t *testing.T) {
	repo := newFakeVideoJobRepository()
	same := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	createTestJob(t, repo, "job-z", "user-a", same)
	createTestJob(t, repo, "job-a", "user-a", same)

	uc := application.NewListUserJobs(repo)
	results, err := uc.Execute(context.Background(), application.ListUserJobsInput{UserID: "user-a", Offset: 0, Limit: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"job-a", "job-z"}
	if len(results) != len(want) {
		t.Fatalf("len(results) = %d, want %d", len(results), len(want))
	}
	for i, id := range want {
		if results[i].JobID != id {
			t.Fatalf("results[%d].JobID = %q, want %q (tie should break by ascending VideoJobID)", i, results[i].JobID, id)
		}
	}
}

func TestListUserJobs_OffsetAndLimitBoundThePage(t *testing.T) {
	repo := newFakeVideoJobRepository()
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		createTestJob(t, repo, "job-"+string(rune('a'+i)), "user-a", base.Add(time.Duration(i)*time.Hour))
	}

	uc := application.NewListUserJobs(repo)
	results, err := uc.Execute(context.Background(), application.ListUserJobsInput{UserID: "user-a", Offset: 1, Limit: 2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Newest-first order is job-e, job-d, job-c, job-b, job-a; offset 1, limit 2 -> job-d, job-c.
	want := []string{"job-d", "job-c"}
	if len(results) != len(want) {
		t.Fatalf("len(results) = %d, want %d", len(results), len(want))
	}
	for i, id := range want {
		if results[i].JobID != id {
			t.Fatalf("results[%d].JobID = %q, want %q", i, results[i].JobID, id)
		}
	}
}

func TestListUserJobs_OutOfRangeLimit_Rejected(t *testing.T) {
	repo := newFakeVideoJobRepository()
	uc := application.NewListUserJobs(repo)

	for _, limit := range []int{0, 101} {
		_, err := uc.Execute(context.Background(), application.ListUserJobsInput{UserID: "user-a", Offset: 0, Limit: limit})
		if !errors.Is(err, application.ErrLimitOutOfRange) {
			t.Fatalf("limit=%d: error = %v, want %v", limit, err, application.ErrLimitOutOfRange)
		}
	}
}

func TestListUserJobs_NegativeOffset_Rejected(t *testing.T) {
	repo := newFakeVideoJobRepository()
	uc := application.NewListUserJobs(repo)

	_, err := uc.Execute(context.Background(), application.ListUserJobsInput{UserID: "user-a", Offset: -1, Limit: 10})
	if !errors.Is(err, application.ErrOffsetNegative) {
		t.Fatalf("error = %v, want %v", err, application.ErrOffsetNegative)
	}
}
