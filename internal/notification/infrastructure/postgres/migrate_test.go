package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"video-processor/internal/notification/domain"
	"video-processor/internal/notification/infrastructure/postgres"
)

// Migrate's advisory lock exists for the first-time create, so the test has
// to start from a database where the tables are absent. Dropping them is safe
// here because no other suite in this repository touches them, and the
// cleanup re-creates them so a failure mid-test cannot leave the schema
// missing for whatever runs next.
//
// Both tables are dropped, not one. The embedded schema is several
// statements executed as a single Exec inside the locked transaction, so the
// serialization either covers all of them or none of them; dropping one
// would leave the other's first-time create untested and the claim in
// Migrate's own comment unverified.
func TestMigrate_ConcurrentFirstTimeCreatesBothSucceed(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS notification_preferences, notification_deliveries"); err != nil {
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

	for _, table := range []string{"notification_preferences", "notification_deliveries"} {
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

// Idempotence on the ordinary path: starting again against a database that
// already carries the schema succeeds and preserves what is stored.
func TestMigrate_IsIdempotent(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	now := testNow()
	repo := postgres.NewPreferenceRepository(db)
	intent := newIntent(t, "user-migrate", completedEventType, withSecret(testSecret))
	if _, err := repo.Set(ctx, intent, now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// A delivery record too: a re-migration that dropped or recreated this
	// table would lose the deduplication history and let every stored job be
	// notified a second time.
	deliveries := postgres.NewDeliveryRepository(db)
	identity := mustDeliveryIdentity(t, "user-migrate", completedEventType, "job-migrate")
	if _, outcome, err := deliveries.ClaimDelivery(ctx, identity, now, now.Add(-reclaimBound)); err != nil || outcome != domain.ClaimGranted {
		t.Fatalf("claim: outcome = %v, err = %v", outcome, err)
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

	if _, outcome, err := deliveries.ClaimDelivery(ctx, identity, now.Add(time.Second), now.Add(time.Second-reclaimBound)); err != nil || outcome != domain.ClaimHeldByAnother {
		t.Fatalf("outcome = %v, err = %v, want %v — the delivery record did not survive", outcome, err, domain.ClaimHeldByAnother)
	}
}
