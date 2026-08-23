package application

import (
	"context"
	"log"
	"sync"
	"time"

	"video-processor/internal/video/domain"
)

// statConcurrency bounds how many result objects are stated at once.
//
// The listing query is deliberately unpaginated (GET /api/status takes no
// pagination parameters, and bounding it would silently hide a user's older
// results), so a long-lived user's listing costs one network round trip per
// completed job. Serially that latency grows linearly with their whole
// history. A small fixed fan-out keeps the wall time roughly flat without
// letting one request open an unbounded number of connections to the object
// store — the failure mode an unlimited fan-out would trade it for.
const statConcurrency = 8

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

	// Each job's result is stated into its own slot, so the repository's
	// ordering survives the concurrent fan-out; found marks the slots that
	// have a readable object.
	stated := make([]ListUserResultsItem, len(jobs))
	found := make([]bool, len(jobs))

	var wg sync.WaitGroup
	slots := make(chan struct{}, statConcurrency)
	for i, job := range jobs {
		wg.Add(1)
		go func(i int, job *domain.VideoJob) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()

			key := job.StorageKey()
			size, modifiedAt, err := uc.results.Stat(ctx, key)
			if err != nil {
				log.Printf("stat result %s for job %s: %v", key.String(), job.ID().String(), err)
				return
			}
			stated[i] = ListUserResultsItem{
				StorageKey: key.String(),
				Size:       size,
				ModifiedAt: modifiedAt,
			}
			found[i] = true
		}(i, job)
	}
	wg.Wait()

	items := make([]ListUserResultsItem, 0, len(jobs))
	for i := range stated {
		if found[i] {
			items = append(items, stated[i])
		}
	}
	return items, nil
}
