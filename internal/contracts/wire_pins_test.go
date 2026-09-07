package contracts_test

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	notificationdomain "video-processor/internal/notification/domain"
	notificationmessaging "video-processor/internal/notification/infrastructure/messaging"
	videodomain "video-processor/internal/video/domain"
	"video-processor/internal/video/infrastructure/idgen"
	videomessaging "video-processor/internal/video/infrastructure/messaging"
	videopostgres "video-processor/internal/video/infrastructure/postgres"
)

// Pins the literals internal/notification/domain deliberately declares
// itself rather than importing, the dependency rules having forbidden the
// import. No composition root imports two contexts any more, so there is no
// process left that could compare them; this test-only package is where the
// two can still be seen at once, on the terms doc.go states.
//
// Without it a rename or a generation bump on one side would drift silently:
// a delivery consumer would resolve every terminal event against an event
// type no stored preference names. In the spirit of
// TestRoutingKeyMatchesTheOutboxEventType.
func TestNotificationEventTypesMatchTheEmittedTerminalEventTypes(t *testing.T) {
	tests := []struct {
		name         string
		notification string
		emitted      string
	}{
		{"completed", notificationdomain.EventTypeVideoJobCompleted, videopostgres.VideoJobCompletedEventType},
		{"failed", notificationdomain.EventTypeVideoJobFailed, videopostgres.VideoJobFailedEventType},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.notification != tt.emitted {
				t.Fatalf("notification event type %q does not match the emitted event type %q", tt.notification, tt.emitted)
			}
		})
	}
}

// The topology half of the same pin, for the copy
// internal/notification/infrastructure/messaging carries of the terminal
// names. It needs nothing running: the descriptors are values, so this one
// can never be skipped, and it is a separate test from the payload pin below
// for exactly that reason.
//
// reflect.DeepEqual over the whole descriptor rather than a field-by-field
// comparison, because "equal in every field" is what DeepEqual actually
// asserts — including a field added to rabbitmq.Topology later that only one
// of the two copies is taught to set.
//
// The bounds matter as much as the names, and less obviously. RabbitMQ
// refuses to redeclare an existing queue whose arguments differ, so a drift
// in WorkMaxLength or either dead-letter bound does not produce a consumer
// reading a slightly different queue — it produces a notifier whose every
// dial fails at the declaration, while a pin comparing only names stays
// green.
func TestNotificationTerminalTopologyMatchesTheEmittedTopology(t *testing.T) {
	copied := notificationmessaging.TerminalEventsTopology()
	emitted := videomessaging.TerminalEventsTopology()

	if !reflect.DeepEqual(copied, emitted) {
		t.Fatalf("the copied terminal topology has drifted from the emitted one:\n copied = %+v\nemitted = %+v", copied, emitted)
	}
}

// The payload half: the bytes internal/video/infrastructure/postgres actually
// stores, decoded by the Notification context's own message structs.
//
// Not a fixture written from those structs, which would pass whatever the
// producer does, and not a comparison of struct tags either. The producer's
// payload types are unexported, so the only honest way to see what it writes
// is to write one — the assertion that matters is that every field arrives
// populated, since a renamed field decodes as its zero value and returns no
// error at all. A notifier reading such a message would announce job "" and
// link to no artifact.
//
// Skipped rather than failed without VIDEO_POSTGRES_TEST_DSN. This package
// has no TestMain and needs none: it is the only test here that touches a
// database, and every other assertion in it compares values that are always
// available. Failing hard would make a whole class of contract pin
// unrunnable on a machine with no PostgreSQL, to gate one test.
func TestNotificationTerminalMessagesDecodeTheEmittedPayloads(t *testing.T) {
	db := videoPinTestDB(t)
	ctx := context.Background()

	ids := idgen.New()
	repo := videopostgres.NewRepository(db, ids)

	t.Run("completed", func(t *testing.T) {
		job, epoch := seedProcessingVideoJob(t, repo, ids, "movie.mp4")
		storageKey := videodomain.ResultStorageKey(job.ID())
		if err := job.Complete(storageKey, 42); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if _, err := repo.Update(ctx, job, epoch); err != nil {
			t.Fatalf("Update: %v", err)
		}

		msg, err := notificationmessaging.ParseJobCompletedMessage(
			readEmittedTerminalPayload(t, db, videopostgres.VideoJobCompletedEventType, job.ID().String()))
		if err != nil {
			t.Fatalf("ParseJobCompletedMessage: %v", err)
		}
		if msg.Type != notificationdomain.EventTypeVideoJobCompleted {
			t.Errorf("Type = %q, want %q", msg.Type, notificationdomain.EventTypeVideoJobCompleted)
		}
		if msg.JobID != job.ID().String() {
			t.Errorf("JobID = %q, want %q", msg.JobID, job.ID().String())
		}
		// The owner is what the whole delivery resolves against: preferences
		// are read by user, and a zero value here would resolve every event
		// to nobody.
		if msg.UserID != job.UserID().String() {
			t.Errorf("UserID = %q, want %q", msg.UserID, job.UserID().String())
		}
		if msg.FrameCount != 42 {
			t.Errorf("FrameCount = %d, want 42", msg.FrameCount)
		}
		if msg.StorageKey != storageKey.String() {
			t.Errorf("StorageKey = %q, want %q", msg.StorageKey, storageKey.String())
		}
		// The enrolment boundary compares this against the preference's
		// created_at, so a zero value would deliver a job's outcome to a
		// preference registered long after it.
		if msg.OccurredAt.IsZero() {
			t.Error("OccurredAt is zero — the timestamp did not survive the round trip")
		}
	})

	t.Run("failed", func(t *testing.T) {
		job, epoch := seedProcessingVideoJob(t, repo, ids, "broken.mp4")
		const reason = "ffmpeg exited with status 1"
		if err := job.Fail(reason); err != nil {
			t.Fatalf("Fail: %v", err)
		}
		if _, err := repo.Update(ctx, job, epoch); err != nil {
			t.Fatalf("Update: %v", err)
		}

		msg, err := notificationmessaging.ParseJobFailedMessage(
			readEmittedTerminalPayload(t, db, videopostgres.VideoJobFailedEventType, job.ID().String()))
		if err != nil {
			t.Fatalf("ParseJobFailedMessage: %v", err)
		}
		if msg.Type != notificationdomain.EventTypeVideoJobFailed {
			t.Errorf("Type = %q, want %q", msg.Type, notificationdomain.EventTypeVideoJobFailed)
		}
		if msg.JobID != job.ID().String() {
			t.Errorf("JobID = %q, want %q", msg.JobID, job.ID().String())
		}
		if msg.UserID != job.UserID().String() {
			t.Errorf("UserID = %q, want %q", msg.UserID, job.UserID().String())
		}
		if msg.ErrorReason != reason {
			t.Errorf("ErrorReason = %q, want %q", msg.ErrorReason, reason)
		}
		if msg.OccurredAt.IsZero() {
			t.Error("OccurredAt is zero — the timestamp did not survive the round trip")
		}
	})
}

// videoPinTestDatabase is this test's own database, created beside the one
// VIDEO_POSTGRES_TEST_DSN names, for the reason
// internal/video/infrastructure/messaging keeps one: `go test ./...` runs
// packages in parallel and internal/video/infrastructure/postgres truncates
// video_jobs and video_job_outbox before each of its own tests. Sharing would
// let it wipe the row this test just wrote, as an unrelated flake.
const videoPinTestDatabase = "notification_wire_pin"

func videoPinTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("VIDEO_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("VIDEO_POSTGRES_TEST_DSN not set; skipping the terminal payload pin")
	}

	admin, err := videopostgres.Open(videopostgres.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	ctx := context.Background()
	// The name is a compile-time constant, not caller input, and CREATE
	// DATABASE takes no parameters. Already-exists is the normal case after
	// the first run.
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+videoPinTestDatabase); err != nil && !strings.Contains(err.Error(), "already exists") {
		_ = admin.Close()
		t.Fatalf("create %s: %v", videoPinTestDatabase, err)
	}
	_ = admin.Close()

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse VIDEO_POSTGRES_TEST_DSN: %v", err)
	}
	parsed.Path = "/" + videoPinTestDatabase

	db, err := videopostgres.Open(videopostgres.Config{DSN: parsed.String()})
	if err != nil {
		t.Fatalf("open %s: %v", videoPinTestDatabase, err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := videopostgres.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, "TRUNCATE TABLE video_jobs, video_job_outbox"); err != nil {
		t.Fatalf("truncate tables: %v", err)
	}
	return db
}

// seedProcessingVideoJob creates, enqueues, and claims a job, returning it
// with the epoch its claim won — the state a terminal write starts from, and
// the only state from which Repository.Update emits a terminal outbox row.
func seedProcessingVideoJob(t *testing.T, repo *videopostgres.Repository, ids videodomain.VideoJobIDGenerator, name string) (*videodomain.VideoJob, int64) {
	t.Helper()
	ctx := context.Background()

	userID, err := videodomain.NewUserID("user-1")
	if err != nil {
		t.Fatalf("NewUserID: %v", err)
	}
	filename, err := videodomain.NewOriginalFilename(name)
	if err != nil {
		t.Fatalf("NewOriginalFilename: %v", err)
	}
	sourceKey, err := videodomain.NewStorageKey("uploads/upload-1_" + name)
	if err != nil {
		t.Fatalf("NewStorageKey: %v", err)
	}

	job, err := videodomain.NewVideoJob(ids, userID, filename, sourceKey,
		"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", time.Now().UTC().Truncate(time.Microsecond))
	if err != nil {
		t.Fatalf("NewVideoJob: %v", err)
	}
	if err := repo.Create(ctx, job); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := job.Enqueue(); err != nil {
		t.Fatalf("job.Enqueue: %v", err)
	}
	if err := repo.Enqueue(ctx, job); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := job.StartProcessing(); err != nil {
		t.Fatalf("job.StartProcessing: %v", err)
	}
	claimed, epoch, err := repo.ClaimForProcessing(ctx, job)
	if err != nil {
		t.Fatalf("ClaimForProcessing: %v", err)
	}
	if !claimed {
		t.Fatal("ClaimForProcessing did not claim a freshly queued job")
	}
	return job, epoch
}

func readEmittedTerminalPayload(t *testing.T, db *sql.DB, eventType, jobID string) []byte {
	t.Helper()

	var payload []byte
	if err := db.QueryRowContext(context.Background(),
		`SELECT payload FROM video_job_outbox WHERE event_type = $1 AND payload->>'job_id' = $2`,
		eventType, jobID,
	).Scan(&payload); err != nil {
		t.Fatalf("read %s payload: %v", eventType, err)
	}
	return payload
}
