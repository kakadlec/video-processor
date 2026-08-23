package application

import (
	"context"
	"log"
	"time"

	"video-processor/internal/video/domain"
)

// ListUserResultsInput carries the caller-supplied listing fields. It has no
// pagination: see domain.VideoJobRepository.FindCompletedByUserID.
type ListUserResultsInput struct {
	UserID string
}

// ListUserResultsItem describes one stored result artifact.
type ListUserResultsItem struct {
	StorageKey string
	Size       int64
	ModifiedAt time.Time
}

// ListUserResults returns the caller's completed jobs' stored results,
// newest first, pairing each job's StorageKey with the size and
// last-modified time of the object it names.
type ListUserResults struct {
	jobs    domain.VideoJobRepository
	results domain.ResultStorage
}

// NewListUserResults wires the ListUserResults use case to its ports.
func NewListUserResults(jobs domain.VideoJobRepository, results domain.ResultStorage) *ListUserResults {
	return &ListUserResults{jobs: jobs, results: results}
}

// Execute runs the result listing use case.
//
// A job whose object cannot be stated — missing, or a storage error — is
// omitted from the listing rather than failing it, mirroring how the
// filesystem-backed implementation skipped past a file it could not stat.
func (uc *ListUserResults) Execute(ctx context.Context, input ListUserResultsInput) ([]ListUserResultsItem, error) {
	userID, err := domain.NewUserID(input.UserID)
	if err != nil {
		return nil, err
	}

	jobs, err := uc.jobs.FindCompletedByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}

	items := make([]ListUserResultsItem, 0, len(jobs))
	for _, job := range jobs {
		key := job.StorageKey()
		size, modifiedAt, err := uc.results.Stat(ctx, key)
		if err != nil {
			log.Printf("stat result %s for job %s: %v", key.String(), job.ID().String(), err)
			continue
		}
		items = append(items, ListUserResultsItem{
			StorageKey: key.String(),
			Size:       size,
			ModifiedAt: modifiedAt,
		})
	}
	return items, nil
}
