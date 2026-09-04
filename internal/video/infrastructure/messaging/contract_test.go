package messaging

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"video-processor/internal/platform/rabbitmq"
	"video-processor/internal/video/domain"
	"video-processor/internal/video/infrastructure/idgen"
	"video-processor/internal/video/infrastructure/postgres"
)

// TestJobQueuedMessageDecodesTheOutboxPayload closes the wire contract from
// the producer's end to the consumer's, through the bytes actually stored.
//
// postgres.videoJobQueuedPayload and JobQueuedMessage are separate types in
// separate packages, and infrastructure adapters do not import one another,
// so nothing in the compiler relates them. A field renamed on the producer's
// side would decode here as its zero value: the worker would fetch storage
// key "", or claim job "", and the dispatch would fail for a reason naming
// neither package. The assertion that matters is therefore field-by-field
// equality against the job that was enqueued — not that decoding returned no
// error, which it would not.
func TestJobQueuedMessageDecodesTheOutboxPayload(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)

	userID, err := domain.NewUserID("user-1")
	if err != nil {
		t.Fatalf("NewUserID: %v", err)
	}
	filename, err := domain.NewOriginalFilename("movie.mp4")
	if err != nil {
		t.Fatalf("NewOriginalFilename: %v", err)
	}
	sourceKey, err := domain.NewStorageKey("uploads/upload-1_movie.mp4")
	if err != nil {
		t.Fatalf("NewStorageKey: %v", err)
	}
	const contentHash = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	job, err := domain.NewVideoJob(ids, userID, filename, sourceKey, contentHash, createdAt)
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

	var payload []byte
	if err := db.QueryRowContext(ctx,
		`SELECT payload FROM video_job_outbox WHERE event_type = $1 AND payload->>'job_id' = $2`,
		postgres.VideoJobQueuedEventType, job.ID().String(),
	).Scan(&payload); err != nil {
		t.Fatalf("read outbox payload: %v", err)
	}

	msg, err := ParseJobQueuedMessage(payload)
	if err != nil {
		t.Fatalf("ParseJobQueuedMessage: %v", err)
	}
	if msg.Type != postgres.VideoJobQueuedEventType {
		t.Fatalf("Type = %q, want %q", msg.Type, postgres.VideoJobQueuedEventType)
	}
	if msg.JobID != job.ID().String() {
		t.Fatalf("JobID = %q, want %q", msg.JobID, job.ID().String())
	}
	if msg.UserID != job.UserID().String() {
		t.Fatalf("UserID = %q, want %q", msg.UserID, job.UserID().String())
	}
	if msg.SourceKey != job.SourceKey().String() {
		t.Fatalf("SourceKey = %q, want %q", msg.SourceKey, job.SourceKey().String())
	}
	// The content hash is what lets the worker clear the idempotency key of
	// a job it failed, so a duplicate submission of the same bytes is
	// processed again instead of being handed the failure for 24 hours.
	if msg.ContentHash != contentHash {
		t.Fatalf("ContentHash = %q, want %q", msg.ContentHash, contentHash)
	}
	if msg.OccurredAt.IsZero() {
		t.Fatal("OccurredAt is zero — the timestamp did not survive the round trip")
	}
}

// TestJobDispatch_PreviousGenerationRoutingKeyReachesNoQueue is the broker
// half of the generation bump. The database half — a relay of the previous
// build never claiming this build's rows — is
// TestOutboxRepository_Claim_IsIsolatedToOneGeneration in the postgres
// package; this one covers the message that slips through anyway.
//
// Because every publish is mandatory, an unroutable one comes back as a
// basic.return rather than being silently discarded, so "reached no queue"
// is observable as "not reported as published".
func TestJobDispatch_PreviousGenerationRoutingKeyReachesNoQueue(t *testing.T) {
	conn := openTestConn(t)
	topo := testTopology(t, conn, 10)
	if err := rabbitmq.DeclareTopology(conn, topo); err != nil {
		t.Fatalf("declare topology: %v", err)
	}

	// The current generation's exchange, addressed with the previous
	// generation's routing key. The binding carries the generation too, so
	// there is nothing for it to match.
	previousKey := topo.RoutingKeys[0] + ".previous"
	publisher, err := NewPublisher(conn, topo.Exchange)
	if err != nil {
		t.Fatalf("open publisher: %v", err)
	}
	defer func() { _ = publisher.Close() }()

	published, err := publisher.Publish(context.Background(), []Message{{ID: "00000000-0000-0000-0000-000000000001", RoutingKey: previousKey, Body: []byte(`{}`)}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(published) != 0 {
		t.Fatalf("published = %v, want none — a previous-generation routing key must reach no queue of this generation", published)
	}
	if depth := queueDepth(t, conn, topo.WorkQueue); depth != 0 {
		t.Fatalf("work queue depth = %d, want 0", depth)
	}
	// Not dead-lettered either: an unroutable message never entered a queue,
	// so there is nothing for a dead-letter policy to act on.
	if depth := queueDepth(t, conn, topo.DeadQueue); depth != 0 {
		t.Fatalf("dead-letter queue depth = %d, want 0", depth)
	}
}

// TestJobDispatchTopology_DeclaresAgainstALiveBroker is the check the pinned
// name constants cannot make: that the production descriptor is one RabbitMQ
// will actually accept, arguments and all.
//
// It declares the real names, so it deletes nothing afterwards — a queue
// deleted here would be recreated by the next process to start, and deleting
// a production-named queue that a running worker holds is not something a
// test should do. Redeclaring an identical entity is a no-op.
func TestJobDispatchTopology_DeclaresAgainstALiveBroker(t *testing.T) {
	conn := openTestConn(t)
	if err := rabbitmq.DeclareTopology(conn, JobDispatchTopology()); err != nil {
		t.Fatalf("declare the production topology: %v", err)
	}
	// Declaring twice is what every relay and consumer does on every dial.
	if err := rabbitmq.DeclareTopology(conn, JobDispatchTopology()); err != nil {
		t.Fatalf("redeclare the production topology: %v", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("open channel: %v", err)
	}
	defer func() { _ = ch.Close() }()
	if _, err := ch.QueueDeclarePassive(QueueJobs, true, false, false, false, nil); err != nil {
		t.Fatalf("inspect %s: %v", QueueJobs, err)
	}
}

// TestTerminalMessagesDecodeTheOutboxPayloads is the terminal streams' half
// of the same contract TestJobQueuedMessageDecodesTheOutboxPayload closes for
// dispatch: the bytes Repository.Update actually stored, decoded by the
// structs a consumer will read them with, compared field by field against the
// job that produced them.
func TestTerminalMessagesDecodeTheOutboxPayloads(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)

	t.Run("completed", func(t *testing.T) {
		job, epoch := seedProcessingJob(t, repo, ids, "movie.mp4")
		storageKey := domain.ResultStorageKey(job.ID())
		if err := job.Complete(storageKey, 42); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if _, err := repo.Update(ctx, job, epoch); err != nil {
			t.Fatalf("Update: %v", err)
		}

		msg, err := ParseJobCompletedMessage(readTerminalPayload(t, db, postgres.VideoJobCompletedEventType, job))
		if err != nil {
			t.Fatalf("ParseJobCompletedMessage: %v", err)
		}
		if msg.Type != postgres.VideoJobCompletedEventType {
			t.Errorf("Type = %q, want %q", msg.Type, postgres.VideoJobCompletedEventType)
		}
		if msg.JobID != job.ID().String() {
			t.Errorf("JobID = %q, want %q", msg.JobID, job.ID().String())
		}
		if msg.UserID != job.UserID().String() {
			t.Errorf("UserID = %q, want %q", msg.UserID, job.UserID().String())
		}
		if msg.FrameCount != 42 {
			t.Errorf("FrameCount = %d, want 42", msg.FrameCount)
		}
		// The result key is what makes the message actionable: a notifier
		// that cannot name the artifact has nothing to link to.
		if msg.StorageKey != storageKey.String() {
			t.Errorf("StorageKey = %q, want %q", msg.StorageKey, storageKey.String())
		}
		if msg.OccurredAt.IsZero() {
			t.Error("OccurredAt is zero — the timestamp did not survive the round trip")
		}
	})

	t.Run("failed", func(t *testing.T) {
		job, epoch := seedProcessingJob(t, repo, ids, "broken.mp4")
		const reason = "ffmpeg exited with status 1"
		if err := job.Fail(reason); err != nil {
			t.Fatalf("Fail: %v", err)
		}
		if _, err := repo.Update(ctx, job, epoch); err != nil {
			t.Fatalf("Update: %v", err)
		}

		msg, err := ParseJobFailedMessage(readTerminalPayload(t, db, postgres.VideoJobFailedEventType, job))
		if err != nil {
			t.Fatalf("ParseJobFailedMessage: %v", err)
		}
		if msg.Type != postgres.VideoJobFailedEventType {
			t.Errorf("Type = %q, want %q", msg.Type, postgres.VideoJobFailedEventType)
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

// seedProcessingJob creates, enqueues, and claims a job, returning it with the
// epoch its claim won — the state a terminal write starts from.
func seedProcessingJob(t *testing.T, repo *postgres.Repository, ids domain.VideoJobIDGenerator, name string) (*domain.VideoJob, int64) {
	t.Helper()
	ctx := context.Background()

	userID, err := domain.NewUserID("user-1")
	if err != nil {
		t.Fatalf("NewUserID: %v", err)
	}
	filename, err := domain.NewOriginalFilename(name)
	if err != nil {
		t.Fatalf("NewOriginalFilename: %v", err)
	}
	sourceKey, err := domain.NewStorageKey("uploads/upload-1_" + name)
	if err != nil {
		t.Fatalf("NewStorageKey: %v", err)
	}

	job, err := domain.NewVideoJob(ids, userID, filename, sourceKey, "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", time.Now().UTC().Truncate(time.Microsecond))
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

func readTerminalPayload(t *testing.T, db *sql.DB, eventType string, job *domain.VideoJob) []byte {
	t.Helper()

	var payload []byte
	if err := db.QueryRowContext(context.Background(),
		`SELECT payload FROM video_job_outbox WHERE event_type = $1 AND payload->>'job_id' = $2`,
		eventType, job.ID().String(),
	).Scan(&payload); err != nil {
		t.Fatalf("read %s payload: %v", eventType, err)
	}
	return payload
}

// TestTerminalEventsTopology_DeclaresAgainstALiveBroker is the check the
// pinned names cannot make: that the terminal descriptor is one RabbitMQ will
// accept, both bindings and all arguments included. It declares the real
// names and deletes nothing, for the reason the dispatch equivalent does.
func TestTerminalEventsTopology_DeclaresAgainstALiveBroker(t *testing.T) {
	conn := openTestConn(t)
	if err := rabbitmq.DeclareTopology(conn, TerminalEventsTopology()); err != nil {
		t.Fatalf("declare the production terminal topology: %v", err)
	}
	// Declaring twice is what the relay does on every dial.
	if err := rabbitmq.DeclareTopology(conn, TerminalEventsTopology()); err != nil {
		t.Fatalf("redeclare the production terminal topology: %v", err)
	}
}
