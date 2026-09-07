package postgres_test

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"video-processor/internal/notification/domain"
	"video-processor/internal/notification/infrastructure/postgres"
)

// The three statements' create-versus-update decision follows from the
// request alone, and the case this file adds is the third one: no secret
// submitted on a channel that does not sign, which creates rather than
// refusing. It is stored as an empty string rather than a null, which is
// what the widened constraint permits and what has_secret reports on.
func TestPreferenceRepository_SetCreatesAnEmailPreferenceWithoutASecret(t *testing.T) {
	db := testDB(t)
	repo := postgres.NewPreferenceRepository(db)

	view, err := repo.Set(context.Background(),
		newIntent(t, "user-1", completedEventType, withChannel(domain.ChannelEmail)), testNow())
	if err != nil {
		t.Fatalf("Set() error = %v, want the preference created", err)
	}

	if view.Channel.String() != domain.ChannelEmail {
		t.Fatalf("stored channel = %q, want %q", view.Channel, domain.ChannelEmail)
	}
	if view.Destination.String() != testEmailDestination {
		t.Fatalf("stored destination = %q, want %q", view.Destination, testEmailDestination)
	}
	if view.HasSecret {
		t.Error("an email preference created without a secret should report HasSecret == false")
	}
	if got := storedSecretOn(t, db, "user-1", completedEventType, domain.ChannelEmail); got != "" {
		t.Errorf("stored secret = %q, want the empty string", got)
	}
	if got := countPreferences(t, db); got != 1 {
		t.Errorf("stored %d preferences, want 1", got)
	}
}

// The half of the third statement that is easiest to get wrong: its conflict
// clause must not name the secret column. A write submitting a secret stores
// it whatever the channel, so copying the signing upsert's
// "secret = EXCLUDED.secret" here would clear a stored value on every later
// write that simply did not resend it — silently, and only for this channel.
func TestPreferenceRepository_SecretlessEmailWriteDoesNotClearAStoredSecret(t *testing.T) {
	db := testDB(t)
	repo := postgres.NewPreferenceRepository(db)
	ctx := context.Background()

	created, err := repo.Set(ctx,
		newIntent(t, "user-1", completedEventType, withChannel(domain.ChannelEmail), withSecret(testSecret)), testNow())
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if !created.HasSecret {
		t.Fatal("the created preference should report a secret")
	}

	updated, err := repo.Set(ctx,
		newIntent(t, "user-1", completedEventType,
			withChannel(domain.ChannelEmail), withDestination(testOtherEmailDestination)),
		testNow())
	if err != nil {
		t.Fatalf("Set() error on the secret-less write = %v", err)
	}

	if updated.Destination.String() != testOtherEmailDestination {
		t.Errorf("destination = %q, want it replaced with %q", updated.Destination, testOtherEmailDestination)
	}
	if !updated.HasSecret {
		t.Error("the stored secret should have been preserved, not cleared")
	}
	if got := storedSecretOn(t, db, "user-1", completedEventType, domain.ChannelEmail); got != testSecret {
		t.Errorf("stored secret = %q, want it preserved as %q", got, testSecret)
	}
}

// The read path that the unconditional destination rule used to poison: a
// stored email row must come back through the listing rather than failing
// reconstruction, which would answer 500 for every preference that user has.
func TestPreferenceRepository_ListByUserReturnsAnEmailDestination(t *testing.T) {
	db := testDB(t)
	repo := postgres.NewPreferenceRepository(db)
	ctx := context.Background()

	if _, err := repo.Set(ctx,
		newIntent(t, "user-1", completedEventType, withChannel(domain.ChannelEmail)), testNow()); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if _, err := repo.Set(ctx,
		newIntent(t, "user-1", completedEventType, withSecret(testSecret)), testNow()); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	views, err := repo.ListByUser(ctx, mustUserID(t, "user-1"))
	if err != nil {
		t.Fatalf("ListByUser() error = %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("ListByUser() returned %d views, want 2", len(views))
	}

	// Ordered by event type then channel, and "email" sorts before "webhook".
	if views[0].Channel.String() != domain.ChannelEmail || views[0].Destination.String() != testEmailDestination {
		t.Errorf("first view = (%q, %q), want the email preference", views[0].Channel, views[0].Destination)
	}
	if views[1].Channel.String() != domain.ChannelWebhook {
		t.Errorf("second view channel = %q, want webhook", views[1].Channel)
	}
}

// The delivery read has to restore an aggregate that carries no secret at
// all. Failing here would surface as a repository error on the delivery
// path, which the consumer's disposition table reads as "attempted nothing"
// and requeues — so one such row would block the queue at prefetch 1 rather
// than fail anywhere an operator would look.
func TestPreferenceRepository_FindDeliverableRestoresAnEmailPreference(t *testing.T) {
	db := testDB(t)
	repo := postgres.NewPreferenceRepository(db)
	ctx := context.Background()

	if _, err := repo.Set(ctx,
		newIntent(t, "user-1", completedEventType, withChannel(domain.ChannelEmail)), testNow()); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	eventType, err := domain.ParseEventType(completedEventType)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	preferences, err := repo.FindDeliverable(ctx, mustUserID(t, "user-1"), eventType)
	if err != nil {
		t.Fatalf("FindDeliverable() error = %v", err)
	}
	if len(preferences) != 1 {
		t.Fatalf("FindDeliverable() returned %d preferences, want 1", len(preferences))
	}

	preference := preferences[0]
	if preference.Channel().String() != domain.ChannelEmail {
		t.Errorf("channel = %q, want email", preference.Channel())
	}
	if preference.Destination().String() != testEmailDestination {
		t.Errorf("destination = %q, want %q", preference.Destination(), testEmailDestination)
	}
	if !preference.Secret().IsZero() {
		t.Error("an email preference stored without a secret should restore carrying none")
	}
}

// The projection, not the aggregate: an email preference may carry a stored
// secret, and the statement must not hand it back. Declining to use a value
// that was selected and scanned is a weaker guarantee than declining to load
// it, and the row is read back directly afterwards to show the value is
// still in the column — the statement narrowed, it did not wipe anything.
func TestPreferenceRepository_FindDeliverableYieldsNoSecretForANonSigningChannel(t *testing.T) {
	db := testDB(t)
	repo := postgres.NewPreferenceRepository(db)
	ctx := context.Background()

	if _, err := repo.Set(ctx,
		newIntent(t, "user-1", completedEventType, withChannel(domain.ChannelEmail), withSecret(testSecret)),
		testNow()); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	eventType, err := domain.ParseEventType(completedEventType)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	preferences, err := repo.FindDeliverable(ctx, mustUserID(t, "user-1"), eventType)
	if err != nil {
		t.Fatalf("FindDeliverable() error = %v", err)
	}
	if len(preferences) != 1 {
		t.Fatalf("FindDeliverable() returned %d preferences, want 1", len(preferences))
	}
	if !preferences[0].Secret().IsZero() {
		t.Fatal("the delivery read handed back a secret for a channel that does not sign")
	}

	if got := storedSecretOn(t, db, "user-1", completedEventType, domain.ChannelEmail); got != testSecret {
		t.Fatalf("stored secret = %q, want it still present as %q — the projection narrowed, it did not delete", got, testSecret)
	}
}

// And the signing channel still gets its bytes, which is what the whole
// exception exists for. Without this the test above would pass just as well
// against a projection that never yields a secret to anyone.
func TestPreferenceRepository_FindDeliverableStillLoadsTheSecretForAWebhook(t *testing.T) {
	db := testDB(t)
	repo := postgres.NewPreferenceRepository(db)
	ctx := context.Background()

	if _, err := repo.Set(ctx,
		newIntent(t, "user-1", completedEventType, withSecret(testSecret)), testNow()); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	eventType, err := domain.ParseEventType(completedEventType)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	preferences, err := repo.FindDeliverable(ctx, mustUserID(t, "user-1"), eventType)
	if err != nil {
		t.Fatalf("FindDeliverable() error = %v", err)
	}
	if len(preferences) != 1 {
		t.Fatalf("FindDeliverable() returned %d preferences, want 1", len(preferences))
	}
	if preferences[0].Secret().Reveal() != testSecret {
		t.Fatal("the delivery read did not load the secret for a signing channel")
	}
}

// The database refuses an empty secret on a signing channel whatever code
// issued the statement, which is what makes the rule a property of the table
// rather than of the one package that writes it.
func TestPreferenceRepository_TheDatabaseRefusesAnEmptySecretOnASigningChannel(t *testing.T) {
	db := testDB(t)

	_, err := db.ExecContext(context.Background(),
		`INSERT INTO notification_preferences
		        (user_id, event_type, channel, enabled, destination, secret, created_at, updated_at)
		 VALUES ($1, $2, $3, true, $4, '', now(), now())`,
		"user-1", completedEventType, domain.ChannelWebhook, testDestination)
	if err == nil {
		t.Fatal("the database accepted a webhook preference with an empty secret")
	}
	if !strings.Contains(err.Error(), "notification_preferences_signing_secret_present") {
		t.Fatalf("refused by %v, want the signing-secret constraint", err)
	}
}

// The migration path that reaches a database created before this change: the
// previous, unconditional constraint is dropped and the conditional one put
// in its place. Reconstructed here rather than assumed, because the
// CREATE TABLE above hides it — a fresh database gets the new constraint
// directly and would never exercise this.
func TestMigrate_WidensThePreviousSecretConstraint(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `
		ALTER TABLE notification_preferences
			DROP CONSTRAINT notification_preferences_signing_secret_present;
		ALTER TABLE notification_preferences
			ADD CONSTRAINT notification_preferences_secret_not_empty CHECK (secret <> '');
	`); err != nil {
		t.Fatalf("unexpected error reconstructing the previous constraint: %v", err)
	}

	if err := postgres.Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	if _, err := postgres.NewPreferenceRepository(db).Set(ctx,
		newIntent(t, "user-1", completedEventType, withChannel(domain.ChannelEmail)), testNow()); err != nil {
		t.Fatalf("Set() error after migrating a pre-existing database = %v", err)
	}

	// And the old constraint is gone rather than merely shadowed: two CHECKs
	// on one column both apply, so leaving it would refuse the row above for
	// every future writer while this one happened to pass.
	var remaining int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM pg_constraint
		  WHERE conrelid = 'notification_preferences'::regclass
		    AND conname = 'notification_preferences_secret_not_empty'`).Scan(&remaining); err != nil {
		t.Fatalf("unexpected error reading pg_constraint: %v", err)
	}
	if remaining != 0 {
		t.Error("the previous unconditional constraint is still present after migrating")
	}
}

// Two literals name the signing channel in SQL — the schema's CHECK and the
// delivery read's CASE — because both files are executed without arguments
// for the channel. Neither has a compiler pinning it to domain.ChannelWebhook,
// so these two tests are what stop a rename from leaving a constraint and a
// projection judging a channel that no longer exists. Both read source rather
// than running anything, so neither needs a database.
func TestTheSchemaConstraintNamesTheSigningChannel(t *testing.T) {
	schema, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatalf("unexpected error reading schema: %v", err)
	}

	want := "CHECK (channel <> '" + domain.ChannelWebhook + "' OR secret <> '')"
	// Twice: once in the CREATE TABLE for a fresh database, once in the
	// guarded block that reaches one that already exists. A change to one and
	// not the other is exactly the drift worth catching.
	if got := strings.Count(string(schema), want); got != 2 {
		t.Fatalf("schema.sql contains %d occurrences of %q, want 2", got, want)
	}
}

func TestTheDeliverableProjectionNamesTheSigningChannel(t *testing.T) {
	want := regexp.MustCompile(`CASE\s+WHEN\s+channel\s*=\s*'` + regexp.QuoteMeta(domain.ChannelWebhook) + `'\s+THEN\s+secret\s+ELSE\s+''\s+END`)

	var found bool
	for _, lit := range packageSQLLiterals(t) {
		if lit.name != findDeliverableConstName {
			continue
		}
		found = true
		if !want.MatchString(lit.statement) {
			t.Fatalf("%s does not project the secret conditionally on %q", lit.name, domain.ChannelWebhook)
		}
	}
	if !found {
		t.Fatalf("no statement named %s was found", findDeliverableConstName)
	}
}
