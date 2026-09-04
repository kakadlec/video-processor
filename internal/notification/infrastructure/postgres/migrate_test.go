package postgres_test

import (
	"context"
	"sync"
	"testing"

	"video-processor/internal/notification/infrastructure/postgres"
)

// Migrate's advisory lock exists for the first-time create, so the test has
// to start from a database where the table is absent. Dropping it is safe
// here because no other suite in this repository touches this table, and the
// cleanup re-creates it so a failure mid-test cannot leave the schema missing
// for whatever runs next.
func TestMigrate_ConcurrentFirstTimeCreatesBothSucceed(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS notification_preferences"); err != nil {
		t.Fatalf("unexpected error dropping table: %v", err)
	}
	t.Cleanup(func() {
		if err := postgres.Migrate(context.Background(), db); err != nil {
			t.Fatalf("unexpected error restoring schema: %v", err)
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

	var tables int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM information_schema.tables WHERE table_name = 'notification_preferences'").
		Scan(&tables); err != nil {
		t.Fatalf("unexpected error counting tables: %v", err)
	}
	if tables != 1 {
		t.Fatalf("table count = %d, want 1", tables)
	}
}

// Idempotence on the ordinary path: starting again against a database that
// already carries the schema succeeds and preserves what is stored.
func TestMigrate_IsIdempotent(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	repo := postgres.NewPreferenceRepository(db)
	intent := newIntent(t, "user-migrate", completedEventType, withSecret(testSecret))
	if _, err := repo.Set(ctx, intent, testNow()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := postgres.Migrate(ctx, db); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	views, err := repo.ListByUser(ctx, mustUserID(t, "user-migrate"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("len(views) = %d, want 1 — an existing preference must survive a re-migration", len(views))
	}
}
