package application

import (
	"context"
	"errors"
	"time"

	"video-processor/internal/video/domain"
)

var (
	ErrInvalidListOffset = errors.New("video: list offset must not be negative")
	ErrInvalidListLimit  = errors.New("video: list limit must be between 1 and 100")
)

// ListUserJobsInput identifies the owner and requested page.
type ListUserJobsInput struct {
	UserID domain.UserID
	Offset int
	Limit  int
}

// ListUserJobsItem describes one job in a user's page.
type ListUserJobsItem struct {
	JobID            string
	OriginalFilename string
	Status           domain.JobStatus
	FrameCount       int
	ErrorReason      string
	StorageKey       string
	CreatedAt        time.Time
}

// ListUserJobs returns one validated page of a user's jobs.
type ListUserJobs struct {
	jobs domain.VideoJobRepository
}

// NewListUserJobs wires the use case to its repository.
func NewListUserJobs(jobs domain.VideoJobRepository) *ListUserJobs {
	return &ListUserJobs{jobs: jobs}
}

// Execute validates pagination before querying the repository.
func (uc *ListUserJobs) Execute(
	ctx context.Context,
	input ListUserJobsInput,
) ([]ListUserJobsItem, error) {
	if input.Offset < 0 {
		return nil, ErrInvalidListOffset
	}
	if input.Limit < 1 || input.Limit > 100 {
		return nil, ErrInvalidListLimit
	}

	jobs, err := uc.jobs.FindByUserID(ctx, input.UserID, input.Offset, input.Limit)
	if err != nil {
		return nil, err
	}

	result := make([]ListUserJobsItem, 0, len(jobs))
	for _, job := range jobs {
		result = append(result, ListUserJobsItem{
			JobID:            job.ID().String(),
			OriginalFilename: job.OriginalFilename().String(),
			Status:           job.Status(),
			FrameCount:       job.FrameCount(),
			ErrorReason:      job.ErrorReason(),
			StorageKey:       job.StorageKey().String(),
			CreatedAt:        job.CreatedAt(),
		})
	}
	return result, nil
}
