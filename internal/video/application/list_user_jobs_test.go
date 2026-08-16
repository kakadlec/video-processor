package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"video-processor/internal/video/application"
	"video-processor/internal/video/domain"
)

func storeJobs(t testing.TB, repo *fakeVideoJobRepository, jobs ...*domain.VideoJob) {
	t.Helper()
	for _, job := range jobs {
		if err := repo.Create(context.Background(), job); err != nil {
			t.Fatalf("Create(%q): %v", job.ID(), err)
		}
	}
}

func TestListUserJobs_IsScopedAndOrderedNewestFirst(t *testing.T) {
	repo := newFakeVideoJobRepository()
	owner := mustUserID(t, "owner")
	other := mustUserID(t, "other")
	base := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	storeJobs(t, repo,
		mustRestoreJob(t, "job-old", owner, domain.JobStatusPending, base),
		mustRestoreJob(t, "job-new", owner, domain.JobStatusProcessing, base.Add(time.Hour)),
		mustRestoreJob(t, "job-other", other, domain.JobStatusPending, base.Add(2*time.Hour)),
	)
	useCase := application.NewListUserJobs(repo)

	result, err := useCase.Execute(context.Background(), application.ListUserJobsInput{
		UserID: owner,
		Offset: 0,
		Limit:  100,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("len(result) = %d, want 2", len(result))
	}
	if result[0].JobID != "job-new" || result[1].JobID != "job-old" {
		t.Fatalf("job order = [%q, %q], want [job-new, job-old]", result[0].JobID, result[1].JobID)
	}
}

func TestListUserJobs_UsesJobIDAsStableTieBreaker(t *testing.T) {
	repo := newFakeVideoJobRepository()
	owner := mustUserID(t, "owner")
	createdAt := time.Now()
	storeJobs(t, repo,
		mustRestoreJob(t, "job-c", owner, domain.JobStatusPending, createdAt),
		mustRestoreJob(t, "job-a", owner, domain.JobStatusPending, createdAt),
		mustRestoreJob(t, "job-b", owner, domain.JobStatusPending, createdAt),
	)

	result, err := application.NewListUserJobs(repo).Execute(
		context.Background(),
		application.ListUserJobsInput{UserID: owner, Limit: 100},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"job-a", "job-b", "job-c"}
	for i := range want {
		if result[i].JobID != want[i] {
			t.Fatalf("result[%d].JobID = %q, want %q", i, result[i].JobID, want[i])
		}
	}
}

func TestListUserJobs_AppliesOffsetAndLimit(t *testing.T) {
	repo := newFakeVideoJobRepository()
	owner := mustUserID(t, "owner")
	base := time.Now()
	for i, id := range []string{"job-0", "job-1", "job-2", "job-3"} {
		storeJobs(t, repo, mustRestoreJob(t, id, owner, domain.JobStatusPending, base.Add(time.Duration(i)*time.Minute)))
	}

	result, err := application.NewListUserJobs(repo).Execute(
		context.Background(),
		application.ListUserJobsInput{UserID: owner, Offset: 1, Limit: 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 || result[0].JobID != "job-2" || result[1].JobID != "job-1" {
		t.Fatalf("page = %#v, want job-2 then job-1", result)
	}
}

func TestListUserJobs_RejectsInvalidPaginationWithoutQuerying(t *testing.T) {
	tests := []struct {
		name    string
		offset  int
		limit   int
		wantErr error
	}{
		{name: "negative offset", offset: -1, limit: 10, wantErr: application.ErrInvalidListOffset},
		{name: "zero limit", offset: 0, limit: 0, wantErr: application.ErrInvalidListLimit},
		{name: "limit above maximum", offset: 0, limit: 101, wantErr: application.ErrInvalidListLimit},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := newFakeVideoJobRepository()
			_, err := application.NewListUserJobs(repo).Execute(
				context.Background(),
				application.ListUserJobsInput{
					UserID: mustUserID(t, "owner"),
					Offset: test.offset,
					Limit:  test.limit,
				},
			)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Execute() error = %v, want %v", err, test.wantErr)
			}
			if repo.findByUserIDCalls != 0 {
				t.Fatalf("FindByUserID calls = %d, want 0", repo.findByUserIDCalls)
			}
		})
	}
}
