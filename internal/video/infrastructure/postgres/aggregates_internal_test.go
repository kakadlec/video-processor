package postgres

import (
	"context"
	"database/sql"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// This file is in-package deliberately: what it asserts is the statements
// themselves — the literals they carry and the plans they produce — and both
// are unexported.

func explainTestDB(t *testing.T) *sql.DB {
	t.Helper()

	dsn := os.Getenv("VIDEO_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("VIDEO_POSTGRES_TEST_DSN not set; skipping PostgreSQL integration test")
	}
	db, err := Open(Config{DSN: dsn})
	if err != nil {
		t.Fatalf("opening the database failed: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrating the schema failed: %v", err)
	}
	if _, err := db.ExecContext(ctx, "TRUNCATE TABLE video_jobs, video_job_outbox"); err != nil {
		t.Fatalf("truncating the tables failed: %v", err)
	}
	return db
}

// TestEachAggregatePredicateIsWrittenAsALiteral pins the device the index
// match depends on. A partial index whose predicate is a literal is matched
// only when the planner can prove the query's predicate implies it, which it
// cannot do for a value it does not yet have — so a WHERE status = $1
// rewritten for tidiness would forfeit both partial indexes while every
// behavioural test kept passing.
func TestEachAggregatePredicateIsWrittenAsALiteral(t *testing.T) {
	for _, state := range InFlightStates() {
		if !strings.Contains(inFlightAggregateQuery, "status = '"+state+"'") {
			t.Errorf("the in-flight aggregate does not carry a literal predicate for %q", state)
		}
	}
	for _, eventType := range RelayedEventTypes() {
		if !strings.Contains(unpublishedOutboxAggregateQuery, "event_type = '"+eventType+"'") {
			t.Errorf("the outbox aggregate does not carry a literal predicate for %q", eventType)
		}
	}
	for _, query := range []string{inFlightAggregateQuery, unpublishedOutboxAggregateQuery} {
		if strings.Contains(query, "status = $") || strings.Contains(query, "event_type = $") {
			t.Errorf("an aggregate supplies a partial index's predicate as a parameter:\n%s", query)
		}
	}
}

// TestTheOutboxAggregateNamesExactlyTheRelayedEventTypes reconciles the
// literals in the statement with the constants the rest of the package moves
// with. A generation bump changes the constant and cannot change the
// statement, so without this the aggregate would silently report on a
// generation nothing writes any more.
func TestTheOutboxAggregateNamesExactlyTheRelayedEventTypes(t *testing.T) {
	named := regexp.MustCompile(`event_type = '([^']+)'`).FindAllStringSubmatch(unpublishedOutboxAggregateQuery, -1)
	found := map[string]bool{}
	for _, match := range named {
		found[match[1]] = true
	}
	for _, eventType := range RelayedEventTypes() {
		if !found[eventType] {
			t.Errorf("the outbox aggregate does not name the relayed event type %q", eventType)
		}
		delete(found, eventType)
	}
	for extra := range found {
		t.Errorf("the outbox aggregate names %q, which no relay claims on", extra)
	}
	if strings.Contains(unpublishedOutboxAggregateQuery, videoJobCreatedEventType+"'") {
		t.Error("the outbox aggregate names the creation event type, whose rows keep published_at NULL permanently")
	}
}

// TestEachAggregateIsServedByItsPartialIndex is the assertion that cannot be
// replaced by reading the SQL. An unindexed aggregate returns exactly the
// right number and reads the whole table on every scrape, forever — a defect
// that looks like a working system from every other angle.
func TestEachAggregateIsServedByItsPartialIndex(t *testing.T) {
	db := explainTestDB(t)
	ctx := context.Background()
	seedAggregateRows(t, db)

	t.Run("in-flight jobs", func(t *testing.T) {
		plan := explain(t, db, ctx, inFlightAggregateQuery)
		requireIndexed(t, plan, "video_jobs",
			"video_jobs_queued_created_at_idx",
			"video_jobs_processing_created_at_idx")
		requireSingleRowLookup(t, plan, "video_jobs_queued_created_at_idx")
		requireSingleRowLookup(t, plan, "video_jobs_processing_created_at_idx")
	})

	t.Run("unpublished outbox events", func(t *testing.T) {
		plan := explain(t, db, ctx, unpublishedOutboxAggregateQuery)
		requireIndexed(t, plan, "video_job_outbox", "video_job_outbox_unpublished_idx")
		requireSingleRowLookup(t, plan, "video_job_outbox_unpublished_idx")
	})
}

// seedAggregateRows populates both tables in the shape production has, and
// the shape is the point rather than the volume. In-flight work is a small
// fraction of a job history that only grows, and unpublished dispatches are a
// small fraction of an outbox whose creation rows are never claimed — so a
// bounded scan with a filter would read the whole table to find the few rows
// that match, which is precisely the cost these partial indexes buy off.
//
// Seeding it the other way round — most of the table in flight — makes a
// sequential scan genuinely the cheaper plan and the assertion below
// meaningless, because the planner would then be right to choose one.
func seedAggregateRows(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()

	statements := []string{
		`INSERT INTO video_jobs (id, user_id, original_filename, status, created_at)
		 SELECT gen_random_uuid(), 'u', 'f.mp4', 'queued', now() - make_interval(secs => n)
		 FROM generate_series(1, 200) AS n`,
		`INSERT INTO video_jobs (id, user_id, original_filename, status, created_at)
		 SELECT gen_random_uuid(), 'u', 'f.mp4', 'processing', now() - make_interval(secs => n)
		 FROM generate_series(1, 60) AS n`,
		`INSERT INTO video_jobs (id, user_id, original_filename, status, created_at)
		 SELECT gen_random_uuid(), 'u', 'f.mp4', 'completed', now() - make_interval(secs => n)
		 FROM generate_series(1, 40000) AS n`,
		`INSERT INTO video_job_outbox (id, event_type, payload, occurred_at, published_at)
		 SELECT gen_random_uuid(), 'video_job.created', '{}'::jsonb, now() - make_interval(secs => n), NULL
		 FROM generate_series(1, 40000) AS n`,
		`INSERT INTO video_job_outbox (id, event_type, payload, occurred_at, published_at)
		 SELECT gen_random_uuid(), 'video_job.queued.v2', '{}'::jsonb, now() - make_interval(secs => n), NULL
		 FROM generate_series(1, 60) AS n`,
		`INSERT INTO video_job_outbox (id, event_type, payload, occurred_at, published_at)
		 SELECT gen_random_uuid(), 'video_job.completed.v1', '{}'::jsonb, now() - make_interval(secs => n), NULL
		 FROM generate_series(1, 40) AS n`,
		`INSERT INTO video_job_outbox (id, event_type, payload, occurred_at, published_at)
		 SELECT gen_random_uuid(), 'video_job.failed.v1', '{}'::jsonb, now() - make_interval(secs => n), NULL
		 FROM generate_series(1, 10) AS n`,
		`ANALYZE video_jobs`,
		`ANALYZE video_job_outbox`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seeding failed: %v", err)
		}
	}
}

func explain(t *testing.T, db *sql.DB, ctx context.Context, query string) string {
	t.Helper()

	rows, err := db.QueryContext(ctx, "EXPLAIN (ANALYZE, TIMING OFF) "+query, InFlightCountBound)
	if err != nil {
		t.Fatalf("EXPLAIN failed: %v", err)
	}
	defer rows.Close()

	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("reading the plan failed: %v", err)
		}
		plan.WriteString(line)
		plan.WriteString("\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading the plan failed: %v", err)
	}
	return plan.String()
}

func requireIndexed(t *testing.T, plan, table string, indexes ...string) {
	t.Helper()

	for _, index := range indexes {
		if !strings.Contains(plan, index) {
			t.Errorf("the plan does not use %s:\n%s", index, plan)
		}
	}
	if strings.Contains(plan, "Seq Scan on "+table) {
		t.Errorf("the plan reads all of %s sequentially:\n%s", table, plan)
	}
}

// requireSingleRowLookup asserts that the cheapest scan of this index read
// exactly one row — the oldest-age lookup. The count subquery scans the same
// index and reads as many entries as match, so the assertion is about the
// minimum rather than about every node.
//
// Every state and every relayed event type is seeded above precisely so that
// minimum is 1 rather than 0: an empty set's age lookup legitimately reads
// nothing, and an assertion that accepted zero would pass against an
// unindexed plan over an empty table.
func requireSingleRowLookup(t *testing.T, plan, index string) {
	t.Helper()

	actualRows := regexp.MustCompile(`\(actual rows=([0-9.]+)`)
	smallest := -1
	for _, line := range strings.Split(plan, "\n") {
		if !strings.Contains(line, index) {
			continue
		}
		match := actualRows.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		read, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			continue
		}
		if smallest < 0 || int(read) < smallest {
			smallest = int(read)
		}
	}
	switch {
	case smallest < 0:
		t.Errorf("no scan of %s reported the rows it read:\n%s", index, plan)
	case smallest != 1:
		t.Errorf("the cheapest scan of %s read %d rows; an oldest-age lookup must read a single ordered row:\n%s", index, smallest, plan)
	}
}
