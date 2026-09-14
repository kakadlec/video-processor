package cache_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/redis/go-redis/v9"

	"video-processor/internal/video/domain"
	"video-processor/internal/video/infrastructure/cache"
)

// unavailableRepository fails every method with an error carrying
// domain.ErrRepositoryUnavailable, wrapped the way the PostgreSQL adapter
// wraps it, so the decorator is asked the only question this file is about.
type unavailableRepository struct{ err error }

func newUnavailableRepository() *unavailableRepository {
	return &unavailableRepository{err: fmt.Errorf("video: claim video job for processing: %w: dial tcp: connection refused", domain.ErrRepositoryUnavailable)}
}

func (r *unavailableRepository) Create(context.Context, *domain.VideoJob) error { return r.err }
func (r *unavailableRepository) FindByID(context.Context, domain.VideoJobID) (*domain.VideoJob, error) {
	return nil, r.err
}
func (r *unavailableRepository) FindByUserID(context.Context, domain.UserID, int, int) ([]*domain.VideoJob, error) {
	return nil, r.err
}
func (r *unavailableRepository) FindCompletedByUserID(context.Context, domain.UserID) ([]*domain.VideoJob, error) {
	return nil, r.err
}
func (r *unavailableRepository) Update(context.Context, *domain.VideoJob, int64) (bool, error) {
	return false, r.err
}
func (r *unavailableRepository) Enqueue(context.Context, *domain.VideoJob) error { return r.err }
func (r *unavailableRepository) ClaimForProcessing(context.Context, *domain.VideoJob) (bool, int64, error) {
	return false, 0, r.err
}
func (r *unavailableRepository) Requeue(context.Context, *domain.VideoJob, int64) (bool, error) {
	return false, r.err
}
func (r *unavailableRepository) FindProcessing(context.Context, domain.VideoJobID, int) ([]*domain.VideoJob, error) {
	return nil, r.err
}

var _ domain.VideoJobRepository = (*unavailableRepository)(nil)

// TestCachedVideoJobRepository_PassesTheUnavailabilitySentinelThrough pins a
// property the decorator has today by inheritance — it returns the inner
// error unmodified — rather than by intent. An inherited property nothing
// pins is one refactor from being lost, and a decorator that swallowed this
// sentinel would silently restore the defect for every call site reading
// through the cache.
func TestCachedVideoJobRepository_PassesTheUnavailabilitySentinelThrough(t *testing.T) {
	client := newTestClient(t)
	inner := newUnavailableRepository()
	repo := cache.NewCachedVideoJobRepository(inner, client, idParser{})
	ctx := context.Background()

	job := newTestJob(t)

	calls := map[string]func() error{
		"Create":                func() error { return repo.Create(ctx, job) },
		"FindByID":              func() error { _, err := repo.FindByID(ctx, job.ID()); return err },
		"FindByUserID":          func() error { _, err := repo.FindByUserID(ctx, job.UserID(), 0, 10); return err },
		"FindCompletedByUserID": func() error { _, err := repo.FindCompletedByUserID(ctx, job.UserID()); return err },
		"Update":                func() error { _, err := repo.Update(ctx, job, 0); return err },
		"Enqueue":               func() error { return repo.Enqueue(ctx, job) },
		"ClaimForProcessing":    func() error { _, _, err := repo.ClaimForProcessing(ctx, job); return err },
		"Requeue":               func() error { _, err := repo.Requeue(ctx, job, 0); return err },
		"FindProcessing":        func() error { _, err := repo.FindProcessing(ctx, domain.VideoJobID{}, 10); return err },
	}

	for name, call := range calls {
		err := call()
		if !errors.Is(err, domain.ErrRepositoryUnavailable) {
			t.Errorf("%s: decorated call lost the unavailability sentinel: %v", name, err)
		}
		if !errors.Is(err, inner.err) {
			t.Errorf("%s: decorated call did not return the inner error: %v", name, err)
		}
	}
}

// TestCachedVideoJobRepository_ACacheStoreFailureIsNotUnavailability is the
// other half of the contract: the authoritative store answered, so a Redis
// that cannot be reached must stay invisible to the caller rather than be
// reported as the repository being down.
func TestCachedVideoJobRepository_ACacheStoreFailureIsNotUnavailability(t *testing.T) {
	// A port nothing listens on, with retries disabled so a failing command
	// costs one refused dial rather than go-redis's default backoff ladder.
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })

	inner := newFakeRepository()
	repo := cache.NewCachedVideoJobRepository(inner, client, idParser{})
	ctx := context.Background()

	job := newTestJob(t)

	if err := repo.Create(ctx, job); err != nil {
		t.Fatalf("Create: %v", err)
	}
	found, err := repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if found.ID() != job.ID() {
		t.Fatalf("FindByID returned %s, want %s", found.ID(), job.ID())
	}
	if errors.Is(err, domain.ErrRepositoryUnavailable) {
		t.Error("a cache-store failure was reported as repository unavailability")
	}
}
