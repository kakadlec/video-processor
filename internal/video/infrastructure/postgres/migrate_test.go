package postgres_test

import (
	"context"
	"sync"
	"testing"

	"video-processor/internal/video/infrastructure/postgres"
)

// Migrate's advisory lock exists for the first-time create, so the test has
// to start from a database where the relations are absent. Dropping both
// tables is safe here because no other suite in this repository touches
// them concurrently, and the cleanup re-migrates so a failure mid-test
// cannot leave the schema missing for whatever runs next.
//
// video_job_outbox is dropped alongside video_jobs, not on its own: the
// embedded schema is several statements executed as a single Exec inside
// the locked transaction, so the serialization either covers all of them or
// none of them, and dropping only one table would leave the other's
// first-time create untested.
func TestMigrate_ConcurrentFirstTimeCreatesBothSucceed(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS video_jobs, video_job_outbox"); err != nil {
		t.Fatalf("unexpected error dropping tables: %v", err)
	}
	t.Cleanup(func() {
		// Errorf rather than Fatalf: Fatalf's runtime.Goexit does not belong
		// in a cleanup function, which runs outside the test's own call
		// stack.
		if err := postgres.Migrate(context.Background(), db); err != nil {
			t.Errorf("unexpected error restoring schema: %v", err)
		}
	})

	const replicas = 2
	var (
		wg    sync.WaitGroup
		errs  [replicas]error
		start = make(chan struct{})
	)
	for i := 0; i < replicas; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = postgres.Migrate(ctx, db)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("replica %d: unexpected error: %v", i, err)
		}
	}

	for _, table := range []string{"video_jobs", "video_job_outbox"} {
		var count int
		if err := db.QueryRowContext(ctx,
			"SELECT count(*) FROM information_schema.tables WHERE table_name = $1", table).
			Scan(&count); err != nil {
			t.Fatalf("unexpected error counting %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("%s count = %d, want 1", table, count)
		}
	}
}
