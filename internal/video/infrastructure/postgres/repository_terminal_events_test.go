package postgres_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"video-processor/internal/video/domain"
	"video-processor/internal/video/infrastructure/idgen"
	"video-processor/internal/video/infrastructure/postgres"
)

// The terminal generation's event types, spelled out rather than imported for
// the same reason queuedEventType is: these tests assert what a consumer of
// the outbox would see, and a constant shared with the code under test would
// move with it silently.
const (
	completedEventType = "video_job.completed.v1"
	failedEventType    = "video_job.failed.v1"
)

// terminalOutboxRow is one row as a consumer would read it.
type terminalOutboxRow struct {
	eventType  string
	payload    map[string]any
	occurredAt time.Time
	published  bool
}

// terminalOutboxRowsFor returns every terminal row naming job, oldest first.
func terminalOutboxRowsFor(t *testing.T, db *sql.DB, job *domain.VideoJob) []terminalOutboxRow {
	t.Helper()

	rows, err := db.QueryContext(context.Background(),
		`SELECT event_type, payload, occurred_at, published_at IS NOT NULL
		 FROM video_job_outbox
		 WHERE event_type = ANY($1::text[]) AND payload->>'job_id' = $2
		 ORDER BY occurred_at`,
		[]string{completedEventType, failedEventType}, job.ID().String(),
	)
	if err != nil {
		t.Fatalf("reading terminal outbox rows: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var out []terminalOutboxRow
	for rows.Next() {
		var (
			row  terminalOutboxRow
			body []byte
		)
		if err := rows.Scan(&row.eventType, &body, &row.occurredAt, &row.published); err != nil {
			t.Fatalf("scanning terminal outbox row: %v", err)
		}
		if err := json.Unmarshal(body, &row.payload); err != nil {
			t.Fatalf("decoding terminal outbox payload: %v", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading terminal outbox rows: %v", err)
	}
	return out
}

func onlyTerminalRow(t *testing.T, db *sql.DB, job *domain.VideoJob) terminalOutboxRow {
	t.Helper()

	rows := terminalOutboxRowsFor(t, db, job)
	if len(rows) != 1 {
		t.Fatalf("terminal outbox rows = %d, want exactly 1", len(rows))
	}
	return rows[0]
}

// TestRepository_Update_CommitsTheTerminalEventWithTheOutcome replaces the
// test asserting Update wrote no outbox row. That exclusion was Phase 3's
// deliberate deferral of the terminal payload's shape, and Phase 7 settles it
// on its own terms: the outcome and the event announcing it now commit
// together, so a reader can never see a terminal job with nothing announcing
// it.
func TestRepository_Update_CommitsTheTerminalEventWithTheOutcome(t *testing.T) {
	for _, tc := range []struct {
		name      string
		eventType string
		finish    func(t *testing.T, job *domain.VideoJob)
		assert    func(t *testing.T, payload map[string]any, job *domain.VideoJob)
	}{
		{
			name:      "completed",
			eventType: completedEventType,
			finish: func(t *testing.T, job *domain.VideoJob) {
				t.Helper()
				if err := job.Complete(domain.ResultStorageKey(job.ID()), 12); err != nil {
					t.Fatalf("Complete: %v", err)
				}
			},
			assert: func(t *testing.T, payload map[string]any, job *domain.VideoJob) {
				t.Helper()
				if got, want := payload["frame_count"], float64(12); got != want {
					t.Errorf("frame_count = %v, want %v", got, want)
				}
				if got, want := payload["storage_key"], domain.ResultStorageKey(job.ID()).String(); got != want {
					t.Errorf("storage_key = %v, want %v", got, want)
				}
				if _, present := payload["error_reason"]; present {
					t.Error("a completion payload carries error_reason; it names an outcome that did not happen")
				}
			},
		},
		{
			name:      "failed",
			eventType: failedEventType,
			finish: func(t *testing.T, job *domain.VideoJob) {
				t.Helper()
				if err := job.Fail("ffmpeg exited with status 1"); err != nil {
					t.Fatalf("Fail: %v", err)
				}
			},
			assert: func(t *testing.T, payload map[string]any, _ *domain.VideoJob) {
				t.Helper()
				if got, want := payload["error_reason"], "ffmpeg exited with status 1"; got != want {
					t.Errorf("error_reason = %v, want %v", got, want)
				}
				if _, present := payload["frame_count"]; present {
					t.Error("a failure payload carries frame_count; there are no frames")
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t)
			ids := idgen.New()
			repo := postgres.NewRepository(db, ids)
			ctx := context.Background()

			job, epoch := seedJobInStatusAtEpoch(t, repo, ids, domain.JobStatusProcessing)
			if rows := terminalOutboxRowsFor(t, db, job); len(rows) != 0 {
				t.Fatalf("terminal outbox rows before the write = %d, want 0", len(rows))
			}

			tc.finish(t, job)
			applied, err := repo.Update(ctx, job, epoch)
			if err != nil {
				t.Fatalf("Update: %v", err)
			}
			if !applied {
				t.Fatal("applied = false, want true")
			}

			row := onlyTerminalRow(t, db, job)
			if row.eventType != tc.eventType {
				t.Errorf("event_type = %q, want %q", row.eventType, tc.eventType)
			}
			if row.published {
				t.Error("published_at is set; a freshly written event is the relay's to publish")
			}
			if got, want := row.payload["type"], tc.eventType; got != want {
				t.Errorf("payload type = %v, want %v", got, want)
			}
			if got, want := row.payload["job_id"], job.ID().String(); got != want {
				t.Errorf("job_id = %v, want %v", got, want)
			}
			if got, want := row.payload["user_id"], job.UserID().String(); got != want {
				t.Errorf("user_id = %v, want %v", got, want)
			}
			tc.assert(t, row.payload, job)

			// occurred_at is generated once per outcome: the column and the
			// payload field are the same instant, not two calls to now().
			payloadOccurred, err := time.Parse(time.RFC3339Nano, row.payload["occurred_at"].(string))
			if err != nil {
				t.Fatalf("parsing the payload's occurred_at: %v", err)
			}
			if !payloadOccurred.Equal(row.occurredAt) {
				t.Errorf("payload occurred_at = %s, column occurred_at = %s; they must be one value", payloadOccurred, row.occurredAt)
			}
		})
	}
}

// TestRepository_Update_FencedWriteRecordsNoEvent pins the half of the
// emission rule that cannot be tested by writing an event: the actor whose
// statement affected no row announces nothing, because the actor who won the
// outcome already did.
func TestRepository_Update_FencedWriteRecordsNoEvent(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	job, epoch := seedJobInStatusAtEpoch(t, repo, ids, domain.JobStatusProcessing)

	// A requeue advances the epoch, leaving the caller below holding a
	// superseded one.
	if err := job.Requeue(); err != nil {
		t.Fatalf("job.Requeue: %v", err)
	}
	if _, err := repo.Requeue(ctx, job, epoch); err != nil {
		t.Fatalf("Requeue: %v", err)
	}

	stale, err := domain.RestoreVideoJob(job.ID(), job.UserID(), job.OriginalFilename(), job.SourceKey(), job.ContentHash(), domain.StorageKey{}, 0, "", domain.JobStatusProcessing, job.CreatedAt(), epoch)
	if err != nil {
		t.Fatalf("RestoreVideoJob: %v", err)
	}
	if err := stale.Complete(domain.ResultStorageKey(job.ID()), 3); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	applied, err := repo.Update(ctx, stale, epoch)
	if !errors.Is(err, domain.ErrJobFenced) {
		t.Fatalf("error = %v, want %v", err, domain.ErrJobFenced)
	}
	if applied {
		t.Fatal("applied = true, want false")
	}
	if rows := terminalOutboxRowsFor(t, db, job); len(rows) != 0 {
		t.Fatalf("terminal outbox rows = %d, want 0 — a fenced write announces nothing", len(rows))
	}
}

// TestRepository_Update_ARetriedOutcomeRecordsNoSecondEvent covers the other
// zero-row path. The worker retries its terminal write after a lost response,
// and that retry must not announce the outcome a second time.
func TestRepository_Update_ARetriedOutcomeRecordsNoSecondEvent(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	job, epoch := seedJobInStatusAtEpoch(t, repo, ids, domain.JobStatusProcessing)
	if err := job.Complete(domain.ResultStorageKey(job.ID()), 5); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, err := repo.Update(ctx, job, epoch); err != nil {
		t.Fatalf("first Update: %v", err)
	}

	applied, err := repo.Update(ctx, job, epoch)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if applied {
		t.Fatal("applied = true on a retry, want false")
	}
	if rows := terminalOutboxRowsFor(t, db, job); len(rows) != 1 {
		t.Fatalf("terminal outbox rows = %d, want exactly 1", len(rows))
	}
}

// TestRepository_Update_ANonTerminalStatusIsRefusedWithoutAnyWrite pins the
// refusal that keeps the state write and the event type in step. Without it a
// non-terminal job would reach the statement and be reported as fenced — a
// lost race, for what is a caller defect.
func TestRepository_Update_ANonTerminalStatusIsRefusedWithoutAnyWrite(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	job, epoch := seedJobInStatusAtEpoch(t, repo, ids, domain.JobStatusProcessing)

	var outboxBefore int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM video_job_outbox`).Scan(&outboxBefore); err != nil {
		t.Fatalf("counting outbox rows: %v", err)
	}

	// Still processing in memory: nothing transitioned it.
	applied, err := repo.Update(ctx, job, epoch)
	if err == nil {
		t.Fatal("error = nil, want a refusal")
	}
	if errors.Is(err, domain.ErrJobFenced) {
		t.Fatalf("error = %v, want a refusal distinct from the fence", err)
	}
	if applied {
		t.Fatal("applied = true, want false")
	}

	found, err := repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if found.Status() != domain.JobStatusProcessing {
		t.Fatalf("Status = %v, want %v", found.Status(), domain.JobStatusProcessing)
	}
	var outboxAfter int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM video_job_outbox`).Scan(&outboxAfter); err != nil {
		t.Fatalf("counting outbox rows: %v", err)
	}
	if outboxAfter != outboxBefore {
		t.Fatalf("outbox rows went from %d to %d; a refused write records nothing", outboxBefore, outboxAfter)
	}
}
