package cache_test

import (
	"context"
	"testing"

	"video-processor/internal/platform/metrics"
	"video-processor/internal/video/infrastructure/cache"
)

// lookupCount reads one outcome of the lookup counter out of the process-wide
// registry. The registry is shared by every test in this binary, so this test
// reads a delta and is not parallel.
func lookupCount(t *testing.T, outcome string) float64 {
	t.Helper()

	families, err := metrics.Gatherer().Gather()
	if err != nil {
		t.Fatalf("gathering the registry failed: %v", err)
	}
	for _, family := range families {
		if family.GetName() != "fiapx_job_status_cache_lookups_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, pair := range metric.GetLabel() {
				if pair.GetName() == "outcome" && pair.GetValue() == outcome {
					return metric.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// TestEveryLookupOutcomeIsCounted covers the three outcomes on the one path
// that reads the cache.
//
// The ordering matters and is the point of driving all three in one test: a
// miss populates the entry the hit then reads, so asserting a hit first would
// be asserting a race. The error outcome is driven from a malformed entry
// rather than from an unreachable Redis, because the distinction this counter
// draws is between an entry that is absent and an entry that could not be
// used — folding the second into a miss would hide a dependency's ill health
// behind a cache-warming statistic.
func TestEveryLookupOutcomeIsCounted(t *testing.T) {
	client := newTestClient(t)
	fake := newFakeRepository()
	job := newTestJob(t)
	if err := fake.Create(context.Background(), job); err != nil {
		t.Fatalf("fake.Create: %v", err)
	}
	repo := cache.NewCachedVideoJobRepository(fake, client, idParser{})
	ctx := context.Background()
	key := "videojob:status:" + job.ID().String()

	if err := client.Del(ctx, key).Err(); err != nil {
		t.Fatalf("Del (setup): %v", err)
	}
	beforeMiss := lookupCount(t, "miss")
	if _, err := repo.FindByID(ctx, job.ID()); err != nil {
		t.Fatalf("FindByID (miss): %v", err)
	}
	if got := lookupCount(t, "miss") - beforeMiss; got != 1 {
		t.Fatalf("an uncached job produced %g miss(es), want 1", got)
	}

	beforeHit := lookupCount(t, "hit")
	if _, err := repo.FindByID(ctx, job.ID()); err != nil {
		t.Fatalf("FindByID (hit): %v", err)
	}
	if got := lookupCount(t, "hit") - beforeHit; got != 1 {
		t.Fatalf("a repopulated entry produced %g hit(s), want 1", got)
	}

	if err := client.Set(ctx, key, "this is not json", 0).Err(); err != nil {
		t.Fatalf("Set (setup): %v", err)
	}
	beforeError := lookupCount(t, "error")
	if _, err := repo.FindByID(ctx, job.ID()); err != nil {
		t.Fatalf("FindByID (malformed entry): %v", err)
	}
	if got := lookupCount(t, "error") - beforeError; got != 1 {
		t.Fatalf("a malformed entry produced %g error(s), want 1", got)
	}
	if got := lookupCount(t, "miss") - beforeMiss; got != 1 {
		t.Fatalf("a malformed entry was also counted as a miss; the two outcomes are not distinguished")
	}
}
