package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"video-processor/internal/video/application"
	"video-processor/internal/video/domain"
)

func TestListUserJobs_ScopesOrdersAndPaginates(t *testing.T) {
	repo := newFakeVideoJobRepository()
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	for _, job := range []*domain.VideoJob{
		restoreTestJob(t, "job-c", "user-1", now.Add(-time.Hour), domain.JobStatusPending),
		restoreTestJob(t, "job-b", "user-1", now, domain.JobStatusPending),
		restoreTestJob(t, "job-a", "user-1", now, domain.JobStatusPending),
		restoreTestJob(t, "job-z", "user-2", now.Add(time.Hour), domain.JobStatusPending),
	} {
		if err := repo.Create(context.Background(), job); err != nil {
			t.Fatal(err)
		}
	}
	uc := application.NewListUserJobs(repo)

	result, err := uc.Execute(context.Background(), application.ListUserJobsInput{
		UserID: testUserID(t, "user-1"),
		Offset: 1,
		Limit:  2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Jobs) != 2 || result.Jobs[0].ID().String() != "job-b" || result.Jobs[1].ID().String() != "job-c" {
		t.Fatalf("unexpected ordered page: %v, %v", result.Jobs[0].ID(), result.Jobs[1].ID())
	}
}

func TestListUserJobs_InvalidPaginationDoesNotQueryRepository(t *testing.T) {
	tests := []struct {
		name   string
		offset int
		limit  int
	}{
		{"negative offset", -1, 10},
		{"zero limit", 0, 0},
		{"limit above maximum", 0, 101},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeVideoJobRepository()
			uc := application.NewListUserJobs(repo)
			_, err := uc.Execute(context.Background(), application.ListUserJobsInput{
				UserID: testUserID(t, "user-1"),
				Offset: tt.offset,
				Limit:  tt.limit,
			})
			if !errors.Is(err, application.ErrInvalidPagination) {
				t.Fatalf("error = %v, want %v", err, application.ErrInvalidPagination)
			}
			if repo.listCalls != 0 {
				t.Fatalf("repository was queried %d times", repo.listCalls)
			}
		})
	}
}

func TestListUserJobs_RepositoryFailureIsPropagated(t *testing.T) {
	wantErr := errors.New("repository unavailable")
	repo := newFakeVideoJobRepository()
	repo.listErr = wantErr
	uc := application.NewListUserJobs(repo)
	_, err := uc.Execute(context.Background(), application.ListUserJobsInput{UserID: testUserID(t, "user-1"), Limit: 10})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}
