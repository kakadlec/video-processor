package postgres_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"video-processor/internal/video/domain"
	"video-processor/internal/video/infrastructure/idgen"
	"video-processor/internal/video/infrastructure/postgres"
)

// seedCreatedOutboxRows inserts count unpublished video_job.created rows —
// the permanent backlog every real database carries, since nothing publishes
// that event type. They are given older timestamps than anything a test
// enqueues afterwards, so an unfiltered claim ordered by occurred_at would
// return them first.
func seedCreatedOutboxRows(t *testing.T, db *sql.DB, count int) {
	t.Helper()
	ctx := context.Background()
	base := time.Now().UTC().Add(-24 * time.Hour)
	for i := 0; i < count; i++ {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO video_job_outbox (id, event_type, payload, occurred_at) VALUES ($1, 'video_job.created', $2, $3)`,
			uuid.NewString(), []byte(`{"type":"video_job.created"}`), base.Add(time.Duration(i)*time.Second),
		); err != nil {
			t.Fatalf("unexpected error seeding outbox backlog: %v", err)
		}
	}
}

func enqueuedJob(t *testing.T, db *sql.DB, userID, filename string) *domain.VideoJob {
	t.Helper()
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	job := newTestJob(t, ids, userID, filename, time.Now().UTC())
	if err := repo.Create(ctx, job); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := job.Enqueue(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.Enqueue(ctx, job); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return job
}

func TestRepository_Enqueue_WritesStatusAndOutboxRow(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	job := enqueuedJob(t, db, "user-1", "movie.mp4")

	repo := postgres.NewRepository(db, idgen.New())
	found, err := repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found.Status() != domain.JobStatusQueued {
		t.Fatalf("Status = %v, want %v", found.Status(), domain.JobStatusQueued)
	}

	var (
		eventType   string
		payload     []byte
		publishedAt sql.NullTime
	)
	if err := db.QueryRowContext(ctx,
		`SELECT event_type, payload, published_at FROM video_job_outbox WHERE event_type = $1 AND payload->>'job_id' = $2`,
		postgres.VideoJobQueuedEventType, job.ID().String(),
	).Scan(&eventType, &payload, &publishedAt); err != nil {
		t.Fatalf("unexpected error reading the outbox row: %v", err)
	}
	if publishedAt.Valid {
		t.Fatalf("published_at = %v, want NULL until the relay publishes it", publishedAt.Time)
	}

	var body struct {
		Type       string    `json:"type"`
		JobID      string    `json:"job_id"`
		UserID     string    `json:"user_id"`
		SourceKey  string    `json:"source_key"`
		OccurredAt time.Time `json:"occurred_at"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("unexpected error unmarshaling payload: %v", err)
	}
	if body.Type != postgres.VideoJobQueuedEventType {
		t.Fatalf("payload type = %q, want %q", body.Type, postgres.VideoJobQueuedEventType)
	}
	if body.JobID != job.ID().String() {
		t.Fatalf("payload job_id = %q, want %q", body.JobID, job.ID().String())
	}
	if body.UserID != job.UserID().String() {
		t.Fatalf("payload user_id = %q, want %q", body.UserID, job.UserID().String())
	}
	// The pair a consumer actually needs: without source_key it cannot
	// fetch the video the dispatch names.
	if body.SourceKey != job.SourceKey().String() {
		t.Fatalf("payload source_key = %q, want %q", body.SourceKey, job.SourceKey().String())
	}
	if body.OccurredAt.IsZero() {
		t.Fatal("payload occurred_at is zero")
	}
}

// TestRepository_Enqueue_OutboxFailureRollsBackTheStatus proves the two
// writes share one transaction. The outbox table is dropped so its insert
// fails after the status update has already run inside the transaction: if
// they were separate writes, the job would be left queued with no dispatch
// ever recorded for it.
func TestRepository_Enqueue_OutboxFailureRollsBackTheStatus(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)

	job := newTestJob(t, ids, "user-1", "movie.mp4", time.Now().UTC())
	if err := repo.Create(ctx, job); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := db.ExecContext(ctx, "DROP TABLE video_job_outbox"); err != nil {
		t.Fatalf("unexpected error dropping the outbox table: %v", err)
	}
	t.Cleanup(func() {
		// Restored for whatever runs next: Migrate is idempotent and
		// recreates only what is missing.
		if err := postgres.Migrate(context.Background(), db); err != nil {
			t.Fatalf("unexpected error restoring the outbox table: %v", err)
		}
	})

	if err := job.Enqueue(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.Enqueue(ctx, job); err == nil {
		t.Fatal("expected an error when the outbox insert fails")
	}

	found, err := repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found.Status() != domain.JobStatusPending {
		t.Fatalf("Status = %v, want the transaction rolled back to %v", found.Status(), domain.JobStatusPending)
	}
}

// TestOutboxRepository_Claim_SkipsTheCreatedBacklog is the starvation
// scenario the event_type predicate exists for: the backlog is larger than
// one batch and permanently unpublished, so an unfiltered claim would fill
// every batch with rows nothing ever publishes and never reach the queued
// row.
func TestOutboxRepository_Claim_SkipsTheCreatedBacklog(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	const batchSize = 5
	seedCreatedOutboxRows(t, db, batchSize+1)
	job := enqueuedJob(t, db, "user-1", "movie.mp4")

	batch, err := postgres.NewOutboxRepository(db).Claim(ctx, []string{postgres.VideoJobQueuedEventType}, batchSize)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer batch.Rollback()

	messages := batch.Messages()
	if len(messages) != 1 {
		t.Fatalf("len(messages) = %d, want 1", len(messages))
	}
	var body struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(messages[0].Payload, &body); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body.JobID != job.ID().String() {
		t.Fatalf("claimed job_id = %q, want %q", body.JobID, job.ID().String())
	}
}

// TestOutboxRepository_UnpublishedIndexLeadsWithEventType asserts the index
// itself, from PostgreSQL's own catalog. Every functional test above passes
// whether or not the index exists, and passes just as well with occurred_at
// leading — which is the regression that matters: the claim stays correct
// while scanning the whole permanent created backlog on every idle poll, and
// that scan grows with every job ever created.
func TestOutboxRepository_UnpublishedIndexLeadsWithEventType(t *testing.T) {
	db := testDB(t)

	var indexDef string
	if err := db.QueryRowContext(context.Background(),
		`SELECT indexdef FROM pg_indexes WHERE indexname = 'video_job_outbox_unpublished_idx'`,
	).Scan(&indexDef); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			t.Fatal("video_job_outbox_unpublished_idx does not exist")
		}
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(indexDef, "(event_type, occurred_at)") {
		t.Fatalf("indexdef = %q, want the key columns to be (event_type, occurred_at)", indexDef)
	}
	if !strings.Contains(indexDef, "published_at IS NULL") {
		t.Fatalf("indexdef = %q, want a WHERE published_at IS NULL predicate", indexDef)
	}
}

func TestOutboxRepository_MarkPublished_StampsOnlyTheGivenRows(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	outbox := postgres.NewOutboxRepository(db)

	enqueuedJob(t, db, "user-1", "first.mp4")
	enqueuedJob(t, db, "user-1", "second.mp4")

	batch, err := outbox.Claim(ctx, []string{postgres.VideoJobQueuedEventType}, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	messages := batch.Messages()
	if len(messages) != 2 {
		batch.Rollback()
		t.Fatalf("len(messages) = %d, want 2", len(messages))
	}
	if err := batch.MarkPublished(ctx, []string{messages[0].ID}); err != nil {
		batch.Rollback()
		t.Fatalf("unexpected error: %v", err)
	}
	if err := batch.Commit(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The unstamped row is still claimable, which is what makes a refused
	// publish retry rather than vanish.
	next, err := outbox.Claim(ctx, []string{postgres.VideoJobQueuedEventType}, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer next.Rollback()
	remaining := next.Messages()
	if len(remaining) != 1 {
		t.Fatalf("len(remaining) = %d, want 1", len(remaining))
	}
	if remaining[0].ID != messages[1].ID {
		t.Fatalf("remaining id = %q, want %q", remaining[0].ID, messages[1].ID)
	}
}

// TestOutboxRepository_Claim_ConcurrentClaimsSplitTheRows exercises FOR
// UPDATE SKIP LOCKED against a real database, in parallel goroutines. A fake
// proves nothing here: row locking is the entire mechanism, and it exists
// only in PostgreSQL.
func TestOutboxRepository_Claim_ConcurrentClaimsSplitTheRows(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	outbox := postgres.NewOutboxRepository(db)

	const rows = 6
	for i := 0; i < rows; i++ {
		enqueuedJob(t, db, "user-1", fmt.Sprintf("movie-%d.mp4", i))
	}

	// Each claim takes at most half, so neither can absorb everything and
	// the second is forced to meet rows the first still holds.
	const perClaim = rows / 2

	var (
		mu      sync.Mutex
		claimed []string
	)
	// Each claimer reports that it has claimed, then holds its locks until
	// released. That is what makes the two claims genuinely overlap: without
	// the hold they could serialize, and the test would pass without ever
	// exercising SKIP LOCKED.
	claimedSignal := make(chan error, 2)
	release := make(chan struct{})
	done := make(chan struct{}, 2)

	for i := 0; i < 2; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			batch, err := outbox.Claim(ctx, []string{postgres.VideoJobQueuedEventType}, perClaim)
			if err != nil {
				claimedSignal <- err
				return
			}
			defer batch.Rollback()
			mu.Lock()
			for _, msg := range batch.Messages() {
				claimed = append(claimed, msg.ID)
			}
			mu.Unlock()
			claimedSignal <- nil
			<-release
		}()
	}

	for i := 0; i < 2; i++ {
		select {
		case err := <-claimedSignal:
			if err != nil {
				close(release)
				t.Fatalf("unexpected error: %v", err)
			}
		case <-time.After(10 * time.Second):
			close(release)
			// A claim that blocks behind the other one's locks instead of
			// skipping them is exactly the failure this test exists to
			// catch, and it presents as a hang rather than a wrong value.
			t.Fatal("timed out waiting for both claims; one is blocking on the other's row locks instead of skipping them")
		}
	}
	close(release)
	for i := 0; i < 2; i++ {
		<-done
	}

	if len(claimed) != rows {
		t.Fatalf("claimed %d rows in total, want %d", len(claimed), rows)
	}
	seen := make(map[string]struct{}, len(claimed))
	for _, id := range claimed {
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("row %s was claimed twice; SKIP LOCKED should make claims disjoint", id)
		}
		seen[id] = struct{}{}
	}
}

// previousGenerationEventType is the event_type the pre-cutover build wrote,
// spelled out rather than derived: it is exactly the literal schema.sql's
// cutoff statement names, and a test that computed it from the current
// constant would move with the constant and stop testing the cutoff at all.
const previousGenerationEventType = "video_job.queued"

// seedOutboxRowOfType inserts one unpublished outbox row under an explicit
// event_type, and returns its id.
func seedOutboxRowOfType(t *testing.T, db *sql.DB, eventType string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO video_job_outbox (id, event_type, payload, occurred_at) VALUES ($1, $2, $3, now())`,
		id, eventType, []byte(`{"type":"`+eventType+`"}`),
	); err != nil {
		t.Fatalf("unexpected error seeding outbox row: %v", err)
	}
	return id
}

func outboxPublishedAt(t *testing.T, db *sql.DB, id string) sql.NullTime {
	t.Helper()
	var stamped sql.NullTime
	if err := db.QueryRowContext(context.Background(),
		`SELECT published_at FROM video_job_outbox WHERE id = $1`, id,
	).Scan(&stamped); err != nil {
		t.Fatalf("unexpected error reading published_at: %v", err)
	}
	return stamped
}

// TestMigrate_CutoffStampsOnlyThePreviousGenerationsUnpublishedRows covers
// the migration this change ships. Rows written under the previous dispatch
// generation can no longer be delivered — nothing declares that generation's
// exchange or queue any more — so leaving them unstamped would have every
// relay re-attempt them forever.
//
// The negative halves carry the weight. A statement that matched the current
// event type would retire live dispatches, and one that matched on
// published_at alone would retire the permanent video_job.created backlog,
// which is a record rather than a queue.
func TestMigrate_CutoffStampsOnlyThePreviousGenerationsUnpublishedRows(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	previous := seedOutboxRowOfType(t, db, previousGenerationEventType)
	current := seedOutboxRowOfType(t, db, postgres.VideoJobQueuedEventType)
	created := seedOutboxRowOfType(t, db, "video_job.created")

	// testDB already migrated; this is the run under test, over rows that
	// exist by the time it executes.
	if err := postgres.Migrate(ctx, db); err != nil {
		t.Fatalf("unexpected error migrating schema: %v", err)
	}

	if !outboxPublishedAt(t, db, previous).Valid {
		t.Fatal("expected the previous generation's unpublished row to be stamped")
	}
	if outboxPublishedAt(t, db, current).Valid {
		t.Fatal("the current generation's row must not be stamped — it is a live dispatch")
	}
	if outboxPublishedAt(t, db, created).Valid {
		t.Fatal("video_job.created rows are a permanent record, not a backlog to retire")
	}
}

// TestMigrate_CutoffIsRerunnableAndDoesNotRestampOrReachForward pins the two
// properties that make it safe for Migrate to run the whole file on every
// startup of every process.
func TestMigrate_CutoffIsRerunnableAndDoesNotRestampOrReachForward(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	previous := seedOutboxRowOfType(t, db, previousGenerationEventType)
	if err := postgres.Migrate(ctx, db); err != nil {
		t.Fatalf("unexpected error migrating schema: %v", err)
	}
	first := outboxPublishedAt(t, db, previous)
	if !first.Valid {
		t.Fatal("expected the previous generation's row to be stamped")
	}

	// A row written after the cutoff ran, by a replica of the previous build
	// not yet redeployed. It is undeliverable for the same reason, so the
	// next run stamping it is the intended behaviour, not an accident.
	late := seedOutboxRowOfType(t, db, previousGenerationEventType)

	if err := postgres.Migrate(ctx, db); err != nil {
		t.Fatalf("unexpected error re-migrating schema: %v", err)
	}

	if got := outboxPublishedAt(t, db, previous); !got.Valid || !got.Time.Equal(first.Time) {
		t.Fatalf("published_at = %v, want the first run's %v — an already-stamped row must not be rewritten", got, first)
	}
	if !outboxPublishedAt(t, db, late).Valid {
		t.Fatal("expected a late previous-generation row to be stamped by the next run")
	}
}

// TestOutboxRepository_Claim_IsIsolatedToOneGeneration is the regression test
// bumping the exchange alone would not have produced. The two generations
// never meet at the broker: they meet in this one table, and the claim
// filters on event_type and nothing else. An already-deployed relay of the
// previous build cannot be taught a new predicate, so the only thing that
// keeps it away from this build's dispatches is a literal it will never
// match.
func TestOutboxRepository_Claim_IsIsolatedToOneGeneration(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	previous := seedOutboxRowOfType(t, db, previousGenerationEventType)
	current := seedOutboxRowOfType(t, db, postgres.VideoJobQueuedEventType)

	// Discriminating pre-assertion: the previous-generation row has to be
	// genuinely claimable at this point. testDB migrates before truncating,
	// so nothing has stamped it — but if that ordering ever changed, the
	// claim below would return nothing for the wrong reason and the test
	// would pass while proving nothing about the predicate.
	if outboxPublishedAt(t, db, previous).Valid {
		t.Fatal("test setup: the previous generation's row is already stamped, so this proves nothing about the claim")
	}

	batch, err := postgres.NewOutboxRepository(db).Claim(ctx, []string{postgres.VideoJobQueuedEventType}, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer batch.Rollback()

	messages := batch.Messages()
	if len(messages) != 1 {
		t.Fatalf("claimed %d messages, want exactly 1 — only the current generation's row is this relay's to dispatch", len(messages))
	}
	if messages[0].ID != current {
		t.Fatalf("claimed row %s, want %s", messages[0].ID, current)
	}
}

// seedTypedOutboxRow writes one row of an arbitrary event type at an explicit
// instant, so a test can pin the order a multi-type claim returns.
func seedTypedOutboxRow(t *testing.T, db *sql.DB, eventType string, occurredAt time.Time) string {
	t.Helper()

	id := uuid.NewString()
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO video_job_outbox (id, event_type, payload, occurred_at) VALUES ($1, $2, $3, $4)`,
		id, eventType, []byte(`{"type":"`+eventType+`"}`), occurredAt,
	); err != nil {
		t.Fatalf("unexpected error seeding a %s row: %v", eventType, err)
	}
	return id
}

// TestOutboxRepository_Claim_OverASetReturnsBothTypesOldestFirst is the
// terminal relay's claim: two event types, one ordering, and nothing outside
// the set. The dispatch rows and the permanent video_job.created backlog are
// both present, and both must be left alone — the first because another relay
// owns it, the second because nothing publishes it at all.
func TestOutboxRepository_Claim_OverASetReturnsBothTypesOldestFirst(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	base := time.Now().UTC().Add(-time.Hour)
	seedCreatedOutboxRows(t, db, 3)
	enqueuedJob(t, db, "user-1", "movie.mp4")

	failedID := seedTypedOutboxRow(t, db, postgres.VideoJobFailedEventType, base)
	completedID := seedTypedOutboxRow(t, db, postgres.VideoJobCompletedEventType, base.Add(time.Second))

	batch, err := postgres.NewOutboxRepository(db).Claim(ctx,
		[]string{postgres.VideoJobCompletedEventType, postgres.VideoJobFailedEventType}, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer batch.Rollback()

	messages := batch.Messages()
	if len(messages) != 2 {
		t.Fatalf("len(messages) = %d, want 2 — the set's rows and nothing else", len(messages))
	}
	// Oldest first across the whole set, not grouped by type: the failure
	// was written first even though it is the second type in the set.
	if messages[0].ID != failedID || messages[1].ID != completedID {
		t.Fatalf("claimed ids = [%s %s], want [%s %s] — ordering is by occurred_at across the set",
			messages[0].ID, messages[1].ID, failedID, completedID)
	}
	if messages[0].EventType != postgres.VideoJobFailedEventType {
		t.Errorf("messages[0].EventType = %q, want %q", messages[0].EventType, postgres.VideoJobFailedEventType)
	}
	if messages[1].EventType != postgres.VideoJobCompletedEventType {
		t.Errorf("messages[1].EventType = %q, want %q", messages[1].EventType, postgres.VideoJobCompletedEventType)
	}
}

// TestOutboxRepository_Claim_TheTwoRelaysDoNotSeeEachOthersRows is the
// disjointness the two relays depend on. Each claims its own set with the
// other's rows present and unpublished.
func TestOutboxRepository_Claim_TheTwoRelaysDoNotSeeEachOthersRows(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	outbox := postgres.NewOutboxRepository(db)

	enqueuedJob(t, db, "user-1", "movie.mp4")
	terminalID := seedTypedOutboxRow(t, db, postgres.VideoJobCompletedEventType, time.Now().UTC())

	dispatch, err := outbox.Claim(ctx, []string{postgres.VideoJobQueuedEventType}, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, msg := range dispatch.Messages() {
		if msg.ID == terminalID {
			dispatch.Rollback()
			t.Fatal("the dispatch relay claimed a terminal row")
		}
		if msg.EventType != postgres.VideoJobQueuedEventType {
			dispatch.Rollback()
			t.Fatalf("dispatch claim returned event type %q", msg.EventType)
		}
	}
	dispatch.Rollback()

	terminal, err := outbox.Claim(ctx,
		[]string{postgres.VideoJobCompletedEventType, postgres.VideoJobFailedEventType}, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer terminal.Rollback()
	messages := terminal.Messages()
	if len(messages) != 1 || messages[0].ID != terminalID {
		t.Fatalf("terminal claim = %v, want exactly the terminal row %s", messages, terminalID)
	}
}

// TestOutboxRepository_Claim_RefusesAnEmptySet guards the one way a relay
// could be pointed at everything: an empty predicate. It is a caller defect,
// not "claim all", and it would publish the permanent video_job.created
// backlog onto a broker.
func TestOutboxRepository_Claim_RefusesAnEmptySet(t *testing.T) {
	db := testDB(t)

	batch, err := postgres.NewOutboxRepository(db).Claim(context.Background(), nil, 10)
	if err == nil {
		batch.Rollback()
		t.Fatal("error = nil, want a refusal for an empty event-type set")
	}
	if batch != nil {
		t.Fatal("batch is non-nil on a refused claim; its transaction would leak")
	}
}

// TestOutboxRepository_MultiTypeClaimCanUseTheUnpublishedIndex is task 3.4's
// check rather than its assumption: the partial index must be able to answer
// `event_type = ANY($1::text[])`, not just the single-value equality it was
// created for. A btree answers ANY through a ScalarArrayOpExpr, but the index
// would be unusable for it if the key columns were ordered the other way.
//
// It disables sequential scans for the plan rather than seeding a table large
// enough for the planner to prefer the index on its own. At any row count a
// test can seed in reasonable time, a seq scan is genuinely cheaper — the
// index is partial on `published_at IS NULL` and every seeded row qualifies,
// so it covers the whole table. Forcing the choice asks the question that
// actually matters, which is whether the index *can* serve this predicate.
func TestOutboxRepository_MultiTypeClaimCanUseTheUnpublishedIndex(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	seedCreatedOutboxRows(t, db, 200)
	base := time.Now().UTC()
	for i := 0; i < 20; i++ {
		seedTypedOutboxRow(t, db, postgres.VideoJobCompletedEventType, base.Add(time.Duration(i)*time.Second))
	}
	if _, err := db.ExecContext(ctx, `ANALYZE video_job_outbox`); err != nil {
		t.Fatalf("ANALYZE: %v", err)
	}

	// One connection for both statements: enable_seqscan is a session
	// setting, and a pooled handle could otherwise run the EXPLAIN on a
	// different connection than the SET.
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `SET enable_seqscan = off`); err != nil {
		t.Fatalf("disable seq scans: %v", err)
	}

	rows, err := conn.QueryContext(ctx,
		`EXPLAIN SELECT id, event_type, payload FROM video_job_outbox
		 WHERE event_type = ANY($1::text[]) AND published_at IS NULL
		 ORDER BY occurred_at LIMIT $2`,
		[]string{postgres.VideoJobCompletedEventType, postgres.VideoJobFailedEventType}, 100,
	)
	if err != nil {
		t.Fatalf("EXPLAIN: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scanning plan: %v", err)
		}
		plan.WriteString(line)
		plan.WriteString("\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading plan: %v", err)
	}
	if !strings.Contains(plan.String(), "video_job_outbox_unpublished_idx") {
		t.Fatalf("the partial index cannot answer a multi-type claim:\n%s", plan.String())
	}
}
