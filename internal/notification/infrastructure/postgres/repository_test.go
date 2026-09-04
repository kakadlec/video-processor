package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"video-processor/internal/notification/domain"
	"video-processor/internal/notification/infrastructure/postgres"
)

const (
	completedEventType = domain.EventTypeVideoJobCompleted
	failedEventType    = domain.EventTypeVideoJobFailed

	testSecret        = "a-signing-secret-of-sufficient-length"
	testReplaceSecret = "a-different-signing-secret-entirely"

	testDestination        = "https://hooks.example.com/first"
	testOtherDestination   = "https://hooks.example.com/second"
	testUpdatedDestination = "https://hooks.example.com/updated"
)

// testDB skips the test unless NOTIFICATION_POSTGRES_TEST_DSN is explicitly
// set: the default unit-test path must not require a live external service.
// The rules this adapter enforces — above all "a create with no secret is
// refused" — live in the interaction between two statements and a stored
// row, so a fake repository cannot stand in for this instance.
func testDB(t *testing.T) *sql.DB {
	t.Helper()

	dsn := os.Getenv("NOTIFICATION_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("NOTIFICATION_POSTGRES_TEST_DSN not set; skipping PostgreSQL integration test")
	}

	db, err := postgres.Open(postgres.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("unexpected error opening database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()
	if err := postgres.Migrate(ctx, db); err != nil {
		t.Fatalf("unexpected error migrating schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, "TRUNCATE TABLE notification_preferences"); err != nil {
		t.Fatalf("unexpected error truncating table: %v", err)
	}

	return db
}

func testNow() time.Time {
	// Microsecond truncation: TIMESTAMPTZ is microsecond-resolution, so a
	// finer timestamp would not round-trip and every equality below would
	// fail for a reason that has nothing to do with this adapter.
	return time.Now().UTC().Truncate(time.Microsecond)
}

func mustUserID(t *testing.T, value string) domain.UserID {
	t.Helper()
	userID, err := domain.NewUserID(value)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return userID
}

type intentOption func(*intentOptions)

type intentOptions struct {
	destination string
	secret      *string
}

func withSecret(raw string) intentOption {
	return func(o *intentOptions) { o.secret = &raw }
}

func withDestination(raw string) intentOption {
	return func(o *intentOptions) { o.destination = raw }
}

// newIntent builds a write intent for one triple. A secret is attached only
// when withSecret is passed, because omission — not emptiness — is what the
// repository branches on.
func newIntent(t *testing.T, userID, eventTypeValue string, opts ...intentOption) domain.PreferenceIntent {
	t.Helper()

	options := intentOptions{destination: testDestination}
	for _, opt := range opts {
		opt(&options)
	}

	eventType, err := domain.ParseEventType(eventTypeValue)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	channel, err := domain.ParseChannel(domain.ChannelWebhook)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	destination, err := domain.NewDestination(options.destination)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var secret *domain.Secret
	if options.secret != nil {
		parsed, err := domain.NewSecret(*options.secret)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		secret = &parsed
	}

	intent, err := domain.NewPreferenceIntent(mustUserID(t, userID), eventType, channel, true, destination, secret)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return intent
}

// storedSecret reads the column no production path may read. Asserting on
// has_secret alone would not distinguish a preserved secret from one
// overwritten with different bytes, which is exactly what the omitted-secret
// statement must never do.
func storedSecret(t *testing.T, db *sql.DB, userID, eventTypeValue string) string {
	t.Helper()

	var secret string
	err := db.QueryRowContext(context.Background(),
		`SELECT secret FROM notification_preferences WHERE user_id = $1 AND event_type = $2 AND channel = $3`,
		userID, eventTypeValue, domain.ChannelWebhook).Scan(&secret)
	if err != nil {
		t.Fatalf("unexpected error reading stored secret: %v", err)
	}
	return secret
}

func countPreferences(t *testing.T, db *sql.DB) int {
	t.Helper()

	var count int
	if err := db.QueryRowContext(context.Background(),
		"SELECT count(*) FROM notification_preferences").Scan(&count); err != nil {
		t.Fatalf("unexpected error counting preferences: %v", err)
	}
	return count
}

func TestPreferenceRepository_SetCreatesWithASecret(t *testing.T) {
	db := testDB(t)
	repo := postgres.NewPreferenceRepository(db)
	ctx := context.Background()
	now := testNow()

	view, err := repo.Set(ctx, newIntent(t, "user-a", completedEventType, withSecret(testSecret)), now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if view.EventType.String() != completedEventType {
		t.Fatalf("EventType = %q, want %q", view.EventType, completedEventType)
	}
	if view.Channel.String() != domain.ChannelWebhook {
		t.Fatalf("Channel = %q, want %q", view.Channel, domain.ChannelWebhook)
	}
	if !view.Enabled {
		t.Fatal("Enabled = false, want true")
	}
	if view.Destination.String() != testDestination {
		t.Fatalf("Destination = %q, want %q", view.Destination, testDestination)
	}
	if !view.HasSecret {
		t.Fatal("HasSecret = false, want true")
	}
	if !view.CreatedAt.Equal(now) || !view.UpdatedAt.Equal(now) {
		t.Fatalf("CreatedAt/UpdatedAt = %v/%v, want both %v", view.CreatedAt, view.UpdatedAt, now)
	}
	if got := storedSecret(t, db, "user-a", completedEventType); got != testSecret {
		t.Fatalf("stored secret = %q, want the submitted one", got)
	}
}

func TestPreferenceRepository_SetUpdatingWithoutASecretPreservesTheStoredOne(t *testing.T) {
	db := testDB(t)
	repo := postgres.NewPreferenceRepository(db)
	ctx := context.Background()
	created := testNow()

	if _, err := repo.Set(ctx, newIntent(t, "user-a", completedEventType, withSecret(testSecret)), created); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := created.Add(time.Minute)
	view, err := repo.Set(ctx, newIntent(t, "user-a", completedEventType, withDestination(testUpdatedDestination)), updated)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if view.Destination.String() != testUpdatedDestination {
		t.Fatalf("Destination = %q, want %q", view.Destination, testUpdatedDestination)
	}
	if !view.HasSecret {
		t.Fatal("HasSecret = false, want true — an update omitting a secret must preserve the stored one")
	}
	if !view.CreatedAt.Equal(created) {
		t.Fatalf("CreatedAt = %v, want the original %v", view.CreatedAt, created)
	}
	if !view.UpdatedAt.Equal(updated) {
		t.Fatalf("UpdatedAt = %v, want %v", view.UpdatedAt, updated)
	}
	if got := storedSecret(t, db, "user-a", completedEventType); got != testSecret {
		t.Fatalf("stored secret = %q, want it byte-identical to the one first written", got)
	}
}

func TestPreferenceRepository_SetCreatingWithoutASecretIsRefused(t *testing.T) {
	db := testDB(t)
	repo := postgres.NewPreferenceRepository(db)
	ctx := context.Background()

	view, err := repo.Set(ctx, newIntent(t, "user-a", completedEventType), testNow())
	if !errors.Is(err, domain.ErrSecretRequired) {
		t.Fatalf("error = %v, want %v", err, domain.ErrSecretRequired)
	}
	if view != (domain.PreferenceView{}) {
		t.Fatalf("view = %+v, want the zero value", view)
	}
	if got := countPreferences(t, db); got != 0 {
		t.Fatalf("stored preferences = %d, want 0 — a refused create must store nothing", got)
	}
}

func TestPreferenceRepository_SetReplacesTheSecretThroughTheInsertPath(t *testing.T) {
	db := testDB(t)
	repo := postgres.NewPreferenceRepository(db)
	ctx := context.Background()
	created := testNow()

	if _, err := repo.Set(ctx, newIntent(t, "user-a", completedEventType, withSecret(testSecret)), created); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	replaced := created.Add(time.Minute)
	view, err := repo.Set(ctx, newIntent(t, "user-a", completedEventType, withSecret(testReplaceSecret)), replaced)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !view.HasSecret {
		t.Fatal("HasSecret = false, want true")
	}
	if !view.CreatedAt.Equal(created) {
		t.Fatalf("CreatedAt = %v, want the original %v", view.CreatedAt, created)
	}
	if got := storedSecret(t, db, "user-a", completedEventType); got != testReplaceSecret {
		t.Fatalf("stored secret = %q, want the replacement", got)
	}
	if got := countPreferences(t, db); got != 1 {
		t.Fatalf("stored preferences = %d, want 1", got)
	}
}

func TestPreferenceRepository_ConcurrentCreatesConvergeOnOneRow(t *testing.T) {
	db := testDB(t)
	repo := postgres.NewPreferenceRepository(db)
	ctx := context.Background()
	now := testNow()

	intents := []domain.PreferenceIntent{
		newIntent(t, "user-a", completedEventType, withSecret(testSecret), withDestination(testDestination)),
		newIntent(t, "user-a", completedEventType, withSecret(testReplaceSecret), withDestination(testOtherDestination)),
	}

	var (
		wg    sync.WaitGroup
		errs  [2]error
		views [2]domain.PreferenceView
		start = make(chan struct{})
	)
	for i := range intents {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			views[i], errs[i] = repo.Set(ctx, intents[i], now)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: unexpected error: %v", i, err)
		}
		if !views[i].HasSecret {
			t.Fatalf("writer %d: HasSecret = false, want true", i)
		}
	}
	if got := countPreferences(t, db); got != 1 {
		t.Fatalf("stored preferences = %d, want exactly 1", got)
	}

	stored := storedSecret(t, db, "user-a", completedEventType)
	if stored != testSecret && stored != testReplaceSecret {
		t.Fatalf("stored secret = %q, want one of the two submitted", stored)
	}
}

func TestPreferenceRepository_ListByUserIsOrderedAndOwnerScoped(t *testing.T) {
	db := testDB(t)
	repo := postgres.NewPreferenceRepository(db)
	ctx := context.Background()
	now := testNow()

	// Written failed-first so the assertion below tests the ORDER BY rather
	// than insertion order.
	if _, err := repo.Set(ctx, newIntent(t, "user-a", failedEventType, withSecret(testSecret), withDestination(testOtherDestination)), now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := repo.Set(ctx, newIntent(t, "user-a", completedEventType, withSecret(testSecret)), now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := repo.Set(ctx, newIntent(t, "user-b", completedEventType, withSecret(testSecret)), now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	views, err := repo.ListByUser(ctx, mustUserID(t, "user-a"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("len(views) = %d, want 2", len(views))
	}
	if views[0].EventType.String() != completedEventType || views[1].EventType.String() != failedEventType {
		t.Fatalf("order = %q, %q; want %q before %q",
			views[0].EventType, views[1].EventType, completedEventType, failedEventType)
	}
	for i, view := range views {
		if view.UserID.String() != "user-a" {
			t.Fatalf("views[%d].UserID = %q, want user-a", i, view.UserID)
		}
		if !view.HasSecret {
			t.Fatalf("views[%d].HasSecret = false, want true", i)
		}
	}
	if views[1].Destination.String() != testOtherDestination {
		t.Fatalf("Destination = %q, want %q", views[1].Destination, testOtherDestination)
	}
}

func TestPreferenceRepository_ListByUserWithNothingStoredIsEmptyAndNotAnError(t *testing.T) {
	db := testDB(t)
	repo := postgres.NewPreferenceRepository(db)

	views, err := repo.ListByUser(context.Background(), mustUserID(t, "user-with-nothing"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(views) != 0 {
		t.Fatalf("len(views) = %d, want 0 — an absent preference means not subscribed", len(views))
	}
}

// A write to one triple must leave every other triple alone, which is what
// makes the write route safe to call repeatedly from a client that holds
// only the preference it is editing.
func TestPreferenceRepository_SetLeavesOtherTriplesIntact(t *testing.T) {
	db := testDB(t)
	repo := postgres.NewPreferenceRepository(db)
	ctx := context.Background()
	now := testNow()

	if _, err := repo.Set(ctx, newIntent(t, "user-a", failedEventType, withSecret(testSecret), withDestination(testOtherDestination)), now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := repo.Set(ctx, newIntent(t, "user-a", completedEventType, withSecret(testReplaceSecret), withDestination(testUpdatedDestination)), now.Add(time.Minute)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := storedSecret(t, db, "user-a", failedEventType); got != testSecret {
		t.Fatalf("stored secret for the untouched triple = %q, want the one it was written with", got)
	}

	views, err := repo.ListByUser(ctx, mustUserID(t, "user-a"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("len(views) = %d, want 2", len(views))
	}
	if views[1].Destination.String() != testOtherDestination {
		t.Fatalf("untouched destination = %q, want %q", views[1].Destination, testOtherDestination)
	}
}
