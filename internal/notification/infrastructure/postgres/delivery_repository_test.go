package postgres_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"video-processor/internal/notification/domain"
	"video-processor/internal/notification/infrastructure/postgres"
)

// reclaimBound is the age at which a pending claim becomes reclaimable in
// these tests. The real value is configuration; here it only has to be long
// enough that "now" is comfortably inside it and short enough to step over
// with arithmetic rather than by sleeping.
const reclaimBound = 2 * time.Minute

func mustDeliveryIdentity(t *testing.T, userID, eventTypeValue, jobID string) domain.DeliveryIdentity {
	t.Helper()

	eventType, err := domain.ParseEventType(eventTypeValue)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	channel, err := domain.ParseChannel(domain.ChannelWebhook)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	identity, err := domain.NewDeliveryIdentity(mustUserID(t, userID), eventType, channel, mustJobID(t, jobID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return identity
}

func mustJobID(t *testing.T, value string) domain.JobID {
	t.Helper()
	jobID, err := domain.NewJobID(value)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return jobID
}

func mustDeliveryStatus(t *testing.T, raw string) domain.DeliveryStatus {
	t.Helper()
	status, err := domain.ParseDeliveryStatus(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return status
}

// storedDelivery reads a delivery row straight out of the table, bypassing
// the adapter. The assertions that matter here are about what is stored, and
// a read through the code under test could not distinguish "stored" from
// "returned".
type storedDelivery struct {
	deliveryID string
	claimToken string
	status     string
	attempts   int
	claimedAt  time.Time
	resolvedAt sql.NullTime
	reason     sql.NullString
}

func readDelivery(t *testing.T, db *sql.DB, identity domain.DeliveryIdentity) storedDelivery {
	t.Helper()

	var got storedDelivery
	err := db.QueryRowContext(context.Background(), `
		SELECT delivery_id, claim_token, status, attempts, claimed_at, resolved_at, reason
		  FROM notification_deliveries
		 WHERE user_id = $1 AND event_type = $2 AND channel = $3 AND job_id = $4`,
		identity.UserID().String(), identity.EventType().String(),
		identity.Channel().String(), identity.JobID().String(),
	).Scan(&got.deliveryID, &got.claimToken, &got.status, &got.attempts, &got.claimedAt, &got.resolvedAt, &got.reason)
	if err != nil {
		t.Fatalf("unexpected error reading delivery: %v", err)
	}
	return got
}

// countDeliveriesFor counts the rows one identity has, which is what the
// reclaim assertions are about. countDeliveries below is the whole table, and
// the subtests here share one database.
func countDeliveriesFor(t *testing.T, db *sql.DB, identity domain.DeliveryIdentity) int {
	t.Helper()

	var count int
	if err := db.QueryRowContext(context.Background(), `
		SELECT count(*)
		  FROM notification_deliveries
		 WHERE user_id = $1 AND event_type = $2 AND channel = $3 AND job_id = $4`,
		identity.UserID().String(), identity.EventType().String(),
		identity.Channel().String(), identity.JobID().String(),
	).Scan(&count); err != nil {
		t.Fatalf("unexpected error counting deliveries: %v", err)
	}
	return count
}

func countDeliveries(t *testing.T, db *sql.DB) int {
	t.Helper()

	var count int
	if err := db.QueryRowContext(context.Background(), "SELECT count(*) FROM notification_deliveries").Scan(&count); err != nil {
		t.Fatalf("unexpected error counting deliveries: %v", err)
	}
	return count
}

// TestClaimDelivery_CoversEveryInputToTheConflictClause is the verification
// the conflict clause's WHERE actually behaves as read. It asserts the
// outcome *value* in each case rather than only that a claim was refused,
// because the two refusals call for opposite dispositions and a test that
// conflated them would pass against the bug the three-valued outcome exists
// to prevent.
//
// The equivalent assumption about ON CONFLICT was wrong once already in this
// context: PostgreSQL evaluates a proposed row's constraints before it
// detects the uniqueness conflict, which broke an earlier design that had
// looked correct on paper.
func TestClaimDelivery_CoversEveryInputToTheConflictClause(t *testing.T) {
	db := testDB(t)
	repo := postgres.NewDeliveryRepository(db)
	ctx := context.Background()
	now := testNow()

	t.Run("no row is granted", func(t *testing.T) {
		identity := mustDeliveryIdentity(t, "user-claim-1", completedEventType, "job-1")

		delivery, outcome, err := repo.ClaimDelivery(ctx, identity, now, now.Add(-reclaimBound))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if outcome != domain.ClaimGranted {
			t.Fatalf("outcome = %v, want %v", outcome, domain.ClaimGranted)
		}
		if delivery.ID().IsZero() || delivery.ClaimToken().IsZero() {
			t.Fatal("a granted claim carries no id or no token")
		}
		if delivery.Attempts() != 0 {
			t.Fatalf("Attempts() = %d, want 0", delivery.Attempts())
		}
		if !delivery.ClaimedAt().Equal(now) {
			t.Fatalf("ClaimedAt() = %v, want %v", delivery.ClaimedAt(), now)
		}

		stored := readDelivery(t, db, identity)
		if stored.deliveryID != delivery.ID().String() || stored.claimToken != delivery.ClaimToken().String() {
			t.Fatal("the stored row does not carry the granted id and token")
		}
		if stored.status != domain.DeliveryStatusPending {
			t.Fatalf("stored status = %q, want %q", stored.status, domain.DeliveryStatusPending)
		}
		if stored.resolvedAt.Valid || stored.reason.Valid {
			t.Fatal("a fresh claim stored a resolution")
		}
	})

	t.Run("a fresh pending row is held by another", func(t *testing.T) {
		identity := mustDeliveryIdentity(t, "user-claim-2", completedEventType, "job-1")

		first, outcome, err := repo.ClaimDelivery(ctx, identity, now, now.Add(-reclaimBound))
		if err != nil || outcome != domain.ClaimGranted {
			t.Fatalf("first claim: outcome = %v, err = %v", outcome, err)
		}

		// A second later: well inside the bound, which is the case a
		// redelivery after a crashed consumer actually lands in.
		second := now.Add(time.Second)
		_, outcome, err = repo.ClaimDelivery(ctx, identity, second, second.Add(-reclaimBound))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if outcome != domain.ClaimHeldByAnother {
			t.Fatalf("outcome = %v, want %v", outcome, domain.ClaimHeldByAnother)
		}

		stored := readDelivery(t, db, identity)
		if stored.claimToken != first.ClaimToken().String() || !stored.claimedAt.Equal(now) {
			t.Fatal("a refused claim changed the stored row")
		}
	})

	t.Run("a stale pending row is reclaimed", func(t *testing.T) {
		identity := mustDeliveryIdentity(t, "user-claim-3", completedEventType, "job-1")

		first, outcome, err := repo.ClaimDelivery(ctx, identity, now, now.Add(-reclaimBound))
		if err != nil || outcome != domain.ClaimGranted {
			t.Fatalf("first claim: outcome = %v, err = %v", outcome, err)
		}

		later := now.Add(reclaimBound + time.Second)
		second, outcome, err := repo.ClaimDelivery(ctx, identity, later, later.Add(-reclaimBound))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if outcome != domain.ClaimGranted {
			t.Fatalf("outcome = %v, want %v", outcome, domain.ClaimGranted)
		}
		if !second.ID().Equal(first.ID()) {
			t.Fatalf("delivery id = %q, want the preserved %q — a receiver may already have deduplicated on it",
				second.ID(), first.ID())
		}
		if second.ClaimToken().Equal(first.ClaimToken()) {
			t.Fatal("the reclaim reused the previous claim token; nothing then fences the previous holder out")
		}
		if second.Attempts() != 0 {
			t.Fatalf("Attempts() = %d, want 0 — a reclaim restarts the budget", second.Attempts())
		}
		if got := countDeliveriesFor(t, db, identity); got != 1 {
			t.Fatalf("row count for the identity = %d, want 1 — the reclaim created a second row", got)
		}
	})

	t.Run("a resolved row is already resolved", func(t *testing.T) {
		identity := mustDeliveryIdentity(t, "user-claim-4", completedEventType, "job-1")

		claim, outcome, err := repo.ClaimDelivery(ctx, identity, now, now.Add(-reclaimBound))
		if err != nil || outcome != domain.ClaimGranted {
			t.Fatalf("claim: outcome = %v, err = %v", outcome, err)
		}

		applied, err := repo.ResolveDelivery(ctx, claim.ID(), claim.ClaimToken(),
			mustDeliveryStatus(t, domain.DeliveryStatusDelivered), 1, "", now.Add(time.Second))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !applied {
			t.Fatal("the current claimant's resolve was refused")
		}

		// Past the bound as well as inside it: a resolved row is finished
		// whatever its age, so neither the status predicate nor the age
		// predicate may let a claim through.
		for _, at := range []time.Time{now.Add(2 * time.Second), now.Add(reclaimBound * 2)} {
			_, outcome, err = repo.ClaimDelivery(ctx, identity, at, at.Add(-reclaimBound))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if outcome != domain.ClaimAlreadyResolved {
				t.Fatalf("outcome at %v = %v, want %v", at, outcome, domain.ClaimAlreadyResolved)
			}
		}
	})
}

// TestResolveDelivery_FencesTheSupersededClaimant is the fifth case of the
// verification above, and the one the whole two-identifier design exists
// for: the reclaim bound proves a claim is old, not that the process holding
// it stopped.
func TestResolveDelivery_FencesTheSupersededClaimant(t *testing.T) {
	db := testDB(t)
	repo := postgres.NewDeliveryRepository(db)
	ctx := context.Background()
	now := testNow()
	identity := mustDeliveryIdentity(t, "user-fence", completedEventType, "job-1")

	superseded, outcome, err := repo.ClaimDelivery(ctx, identity, now, now.Add(-reclaimBound))
	if err != nil || outcome != domain.ClaimGranted {
		t.Fatalf("first claim: outcome = %v, err = %v", outcome, err)
	}

	later := now.Add(reclaimBound + time.Second)
	successor, outcome, err := repo.ClaimDelivery(ctx, identity, later, later.Add(-reclaimBound))
	if err != nil || outcome != domain.ClaimGranted {
		t.Fatalf("reclaim: outcome = %v, err = %v", outcome, err)
	}

	// The superseded claimant is still running — that is the whole premise —
	// and reports the outcome it observed.
	applied, err := repo.ResolveDelivery(ctx, superseded.ID(), superseded.ClaimToken(),
		mustDeliveryStatus(t, domain.DeliveryStatusFailed), 3, "notification: delivery failed", later.Add(time.Second))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if applied {
		t.Fatal("the superseded claimant's resolve was applied")
	}

	stored := readDelivery(t, db, identity)
	if stored.status != domain.DeliveryStatusPending {
		t.Fatalf("stored status = %q, want %q — the refused write changed the row", stored.status, domain.DeliveryStatusPending)
	}
	if stored.claimToken != successor.ClaimToken().String() || stored.attempts != 0 || stored.resolvedAt.Valid || stored.reason.Valid {
		t.Fatal("the refused write changed the successor's row")
	}

	// The successor owns the outcome, and its write goes through.
	resolvedAt := later.Add(2 * time.Second)
	applied, err = repo.ResolveDelivery(ctx, successor.ID(), successor.ClaimToken(),
		mustDeliveryStatus(t, domain.DeliveryStatusDelivered), 1, "", resolvedAt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !applied {
		t.Fatal("the successor's resolve was refused")
	}

	stored = readDelivery(t, db, identity)
	if stored.status != domain.DeliveryStatusDelivered || stored.attempts != 1 {
		t.Fatalf("stored status/attempts = %q/%d, want %q/1", stored.status, stored.attempts, domain.DeliveryStatusDelivered)
	}
	if !stored.resolvedAt.Valid || !stored.resolvedAt.Time.Equal(resolvedAt) {
		t.Fatalf("stored resolved_at = %v, want %v", stored.resolvedAt, resolvedAt)
	}
	if stored.reason.Valid {
		t.Fatalf("stored reason = %q, want NULL for an outcome that carries none", stored.reason.String)
	}
}

// TestResolveDelivery_StoresTheReasonOfAFailure pins the other half of the
// nullable column: a failure records why, and it is text this system wrote.
func TestResolveDelivery_StoresTheReasonOfAFailure(t *testing.T) {
	db := testDB(t)
	repo := postgres.NewDeliveryRepository(db)
	ctx := context.Background()
	now := testNow()
	identity := mustDeliveryIdentity(t, "user-reason", failedEventType, "job-1")

	claim, outcome, err := repo.ClaimDelivery(ctx, identity, now, now.Add(-reclaimBound))
	if err != nil || outcome != domain.ClaimGranted {
		t.Fatalf("claim: outcome = %v, err = %v", outcome, err)
	}

	const reason = "notification: delivery failed (unexpected_status: 500)"
	applied, err := repo.ResolveDelivery(ctx, claim.ID(), claim.ClaimToken(),
		mustDeliveryStatus(t, domain.DeliveryStatusFailed), 3, reason, now.Add(time.Second))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !applied {
		t.Fatal("the current claimant's resolve was refused")
	}

	stored := readDelivery(t, db, identity)
	if !stored.reason.Valid || stored.reason.String != reason {
		t.Fatalf("stored reason = %v, want %q", stored.reason, reason)
	}
	if stored.attempts != 3 {
		t.Fatalf("stored attempts = %d, want 3", stored.attempts)
	}
}

// TestResolveDelivery_RefusesANonTerminalStatus pins the guard that keeps the
// table readable: a row whose status is pending while resolved_at is set is
// the shape domain.RestoreDelivery rejects, so writing one would store
// something nothing can read back.
func TestResolveDelivery_RefusesANonTerminalStatus(t *testing.T) {
	db := testDB(t)
	repo := postgres.NewDeliveryRepository(db)
	ctx := context.Background()
	now := testNow()
	identity := mustDeliveryIdentity(t, "user-nonterminal", completedEventType, "job-1")

	claim, outcome, err := repo.ClaimDelivery(ctx, identity, now, now.Add(-reclaimBound))
	if err != nil || outcome != domain.ClaimGranted {
		t.Fatalf("claim: outcome = %v, err = %v", outcome, err)
	}

	applied, err := repo.ResolveDelivery(ctx, claim.ID(), claim.ClaimToken(),
		mustDeliveryStatus(t, domain.DeliveryStatusPending), 1, "", now.Add(time.Second))
	if err == nil {
		t.Fatal("resolving to a non-terminal status was accepted")
	}
	if applied {
		t.Fatal("a refused resolve reported the write as applied")
	}

	stored := readDelivery(t, db, identity)
	if stored.resolvedAt.Valid {
		t.Fatal("the refused write stamped a resolution time on a pending row")
	}
}

// TestClaimDelivery_ConcurrentClaimsGrantExactlyOne is the guarantee the
// single statement exists for. Two notifier processes reading "not delivered"
// separately and both proceeding is the duplicate the record prevents, and
// only the database can settle it.
func TestClaimDelivery_ConcurrentClaimsGrantExactlyOne(t *testing.T) {
	db := testDB(t)
	repo := postgres.NewDeliveryRepository(db)
	identity := mustDeliveryIdentity(t, "user-concurrent", completedEventType, "job-1")
	now := testNow()

	const claimants = 2
	var (
		wg       sync.WaitGroup
		outcomes [claimants]domain.ClaimOutcome
		errs     [claimants]error
		start    = make(chan struct{})
	)
	for i := range claimants {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, outcomes[i], errs[i] = repo.ClaimDelivery(context.Background(), identity, now, now.Add(-reclaimBound))
		}(i)
	}
	close(start)
	wg.Wait()

	granted, held := 0, 0
	for i, err := range errs {
		if err != nil {
			t.Fatalf("claimant %d: unexpected error: %v", i, err)
		}
		switch outcomes[i] {
		case domain.ClaimGranted:
			granted++
		case domain.ClaimHeldByAnother:
			held++
		default:
			t.Fatalf("claimant %d: outcome = %v, want granted or held", i, outcomes[i])
		}
	}
	if granted != 1 || held != 1 {
		t.Fatalf("granted = %d, held = %d, want 1 and 1", granted, held)
	}
	if countDeliveries(t, db) != 1 {
		t.Fatalf("delivery count = %d, want 1", countDeliveries(t, db))
	}
}

// TestClaimDelivery_ScopesTheRecordToTheWholeQuadruple pins that the primary
// key is the identity: the same job notified on a different event type, or
// for a different user, is a different delivery and claims independently.
func TestClaimDelivery_ScopesTheRecordToTheWholeQuadruple(t *testing.T) {
	db := testDB(t)
	repo := postgres.NewDeliveryRepository(db)
	ctx := context.Background()
	now := testNow()

	identities := []domain.DeliveryIdentity{
		mustDeliveryIdentity(t, "user-scope-a", completedEventType, "job-1"),
		mustDeliveryIdentity(t, "user-scope-b", completedEventType, "job-1"),
		mustDeliveryIdentity(t, "user-scope-a", failedEventType, "job-1"),
		mustDeliveryIdentity(t, "user-scope-a", completedEventType, "job-2"),
	}
	for _, identity := range identities {
		_, outcome, err := repo.ClaimDelivery(ctx, identity, now, now.Add(-reclaimBound))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if outcome != domain.ClaimGranted {
			t.Fatalf("outcome = %v, want %v", outcome, domain.ClaimGranted)
		}
	}

	if got := countDeliveries(t, db); got != len(identities) {
		t.Fatalf("delivery count = %d, want %d", got, len(identities))
	}
}

// TestFindDeliverableLoadsTheSecretThatListByUserCannot states both halves of
// the narrowed rule in one place: the delivery path receives a usable secret,
// and the read behind the preference routes still cannot produce one.
func TestFindDeliverableLoadsTheSecretThatListByUserCannot(t *testing.T) {
	db := testDB(t)
	repo := postgres.NewPreferenceRepository(db)
	ctx := context.Background()
	now := testNow()
	userID := mustUserID(t, "user-deliverable")

	if _, err := repo.Set(ctx, newIntent(t, "user-deliverable", completedEventType, withSecret(testSecret)), now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	eventType, err := domain.ParseEventType(completedEventType)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	preferences, err := repo.FindDeliverable(ctx, userID, eventType)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(preferences) != 1 {
		t.Fatalf("len(preferences) = %d, want 1", len(preferences))
	}
	if got := preferences[0].Secret().Reveal(); got != testSecret {
		t.Fatalf("Secret().Reveal() = %q, want the stored secret", got)
	}
	if preferences[0].Destination().String() != testDestination || !preferences[0].Enabled() {
		t.Fatal("the loaded aggregate does not carry what was stored")
	}
	if !preferences[0].CreatedAt().Equal(now) || !preferences[0].UpdatedAt().Equal(now) {
		t.Fatal("the loaded aggregate does not carry the stored timestamps")
	}

	// The complementary half: PreferenceView has no secret field at all, so
	// the strongest available statement is that the value never reaches this
	// result. The source-level assertion in this package's rules test is what
	// pins that the query does not even select the column.
	views, err := repo.ListByUser(ctx, userID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(views) != 1 || !views[0].HasSecret {
		t.Fatalf("ListByUser did not report the stored preference as carrying a secret")
	}
}

// TestFindDeliverableSkipsWhatMustNotBeDeliveredTo pins the three exclusions
// the statement itself makes, so none of them can quietly move into a caller
// that might forget one: a disabled preference, another user's, and another
// event type's.
func TestFindDeliverableSkipsWhatMustNotBeDeliveredTo(t *testing.T) {
	db := testDB(t)
	repo := postgres.NewPreferenceRepository(db)
	ctx := context.Background()
	now := testNow()

	// Enabled, then disabled through an update that carries no secret — the
	// ordinary way a preference is switched off, and the one that leaves the
	// stored secret in place.
	if _, err := repo.Set(ctx, newIntent(t, "user-skip", completedEventType, withSecret(testSecret)), now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"UPDATE notification_preferences SET enabled = false WHERE user_id = $1", "user-skip"); err != nil {
		t.Fatalf("unexpected error disabling preference: %v", err)
	}
	// A different event type for the same user, and a different user, both
	// enabled.
	if _, err := repo.Set(ctx, newIntent(t, "user-skip", failedEventType, withSecret(testSecret)), now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := repo.Set(ctx, newIntent(t, "user-other", completedEventType, withSecret(testSecret)), now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	eventType, err := domain.ParseEventType(completedEventType)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	preferences, err := repo.FindDeliverable(ctx, mustUserID(t, "user-skip"), eventType)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(preferences) != 0 {
		t.Fatalf("len(preferences) = %d, want 0 — a disabled preference is not deliverable", len(preferences))
	}
}

// TestFindDeliverableWithNothingRegisteredIsEmptyAndNotAnError: an absent
// preference means not subscribed, which is an ordinary state.
func TestFindDeliverableWithNothingRegisteredIsEmptyAndNotAnError(t *testing.T) {
	db := testDB(t)
	repo := postgres.NewPreferenceRepository(db)

	eventType, err := domain.ParseEventType(completedEventType)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	preferences, err := repo.FindDeliverable(context.Background(), mustUserID(t, "user-empty"), eventType)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(preferences) != 0 {
		t.Fatalf("len(preferences) = %d, want 0", len(preferences))
	}
}
