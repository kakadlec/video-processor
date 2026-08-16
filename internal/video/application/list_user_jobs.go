package application

import (
	"context"
	"errors"

	"video-processor/internal/video/domain"
)

var ErrInvalidPagination = errors.New("video: offset must be non-negative and limit must be between 1 and 100")

type ListUserJobsInput struct {
	UserID domain.UserID
	Offset int
	Limit  int
}

type ListUserJobsResult struct {
	Jobs []*domain.VideoJob
}

type ListUserJobs struct {
	jobs domain.VideoJobRepository
}

func NewListUserJobs(jobs domain.VideoJobRepository) *ListUserJobs {
	return &ListUserJobs{jobs: jobs}
}

func (uc *ListUserJobs) Execute(ctx context.Context, input ListUserJobsInput) (ListUserJobsResult, error) {
	if input.Offset < 0 || input.Limit < 1 || input.Limit > 100 {
		return ListUserJobsResult{}, ErrInvalidPagination
	}

	jobs, err := uc.jobs.FindByUserID(ctx, input.UserID, input.Offset, input.Limit)
	if err != nil {
		return ListUserJobsResult{}, err
	}
	return ListUserJobsResult{Jobs: jobs}, nil
}
