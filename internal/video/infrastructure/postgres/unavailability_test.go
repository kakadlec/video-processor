package postgres_test

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"video-processor/internal/video/domain"
	"video-processor/internal/video/infrastructure/idgen"
	"video-processor/internal/video/infrastructure/postgres"
)

// unavailabilityTestDatabase is this file's own database. Test (b) below
// drops a column from video_jobs, which every other test in this package
// reads; doing that to the shared test database and restoring afterwards
// would fail all of them if the restore did not run.
const unavailabilityTestDatabase = "video_unavailability_test"

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("VIDEO_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("VIDEO_POSTGRES_TEST_DSN not set; skipping PostgreSQL integration test")
	}
	return dsn
}

// rewriteDSN returns dsn with its database name replaced, keeping host,
// credentials and parameters. Parsed rather than string-substituted: the
// database name can appear in a parameter too.
func rewriteDSN(t *testing.T, dsn, database string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse VIDEO_POSTGRES_TEST_DSN: %v", err)
	}
	parsed.Path = "/" + database
	return parsed.String()
}

// TestAnUnreachableDatabaseCarriesTheUnavailabilitySentinel is discrimination
// (a). The DSN names a port nothing listens on, so no statement reaches a
// server at all.
//
// It deliberately does not close an open pool instead: "sql: database is
// closed" is an unexported errors.errorString matching nothing, which is
// exactly what the permission list is supposed to leave unmarked.
func TestAnUnreachableDatabaseCarriesTheUnavailabilitySentinel(t *testing.T) {
	dsn := rewriteDSN(t, testDSN(t), "video_test")
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	parsed.Host = "127.0.0.1:1"
	db, err := postgres.Open(postgres.Config{DSN: parsed.String()})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()
	job := newTestJob(t, ids, "user-1", "video.mp4")

	if _, err := repo.FindByID(ctx, job.ID()); !errors.Is(err, domain.ErrRepositoryUnavailable) {
		t.Errorf("FindByID: expected the unavailability sentinel, got %v", err)
	}
	if _, _, err := repo.ClaimForProcessing(ctx, job); !errors.Is(err, domain.ErrRepositoryUnavailable) {
		t.Errorf("ClaimForProcessing: expected the unavailability sentinel, got %v", err)
	}
	if err := repo.Create(ctx, job); !errors.Is(err, domain.ErrRepositoryUnavailable) {
		t.Errorf("Create: expected the unavailability sentinel, got %v", err)
	}
	if _, err := repo.FindProcessing(ctx, domain.VideoJobID{}, 10); !errors.Is(err, domain.ErrRepositoryUnavailable) {
		t.Errorf("FindProcessing: expected the unavailability sentinel, got %v", err)
	}
	if _, err := repo.FindByUserID(ctx, job.UserID(), 0, 10); !errors.Is(err, domain.ErrRepositoryUnavailable) {
		t.Errorf("FindByUserID: expected the unavailability sentinel, got %v", err)
	}
}

// TestAConnectionRefusedByTheServerIsNotUnavailability is the test that pins
// the classifier's rule order, and it cannot be built from fabricated values:
// pgconn.ConnectError's inner error is unexported, so the only way to obtain
// one carrying an embedded *pgconn.PgError is to provoke it against a live
// server. A rejected password (28P01) and a nonexistent database (3D000) both
// arrive that way — and a ConnectError-first predicate would report a rotated
// credential as an outage and retry it forever across every replica.
func TestAConnectionRefusedByTheServerIsNotUnavailability(t *testing.T) {
	base := testDSN(t)

	parsed, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	username := parsed.User.Username()
	badPassword := *parsed
	badPassword.User = url.UserPassword(username, "definitely-not-the-password")

	cases := map[string]struct {
		dsn   string
		state string
	}{
		"rejected password":    {dsn: badPassword.String(), state: "28P01"},
		"nonexistent database": {dsn: rewriteDSN(t, base, "video_no_such_database"), state: "3D000"},
	}

	ids := idgen.New()
	job := newTestJob(t, ids, "user-1", "video.mp4")

	for name, tc := range cases {
		db, err := postgres.Open(postgres.Config{DSN: tc.dsn})
		if err != nil {
			t.Fatalf("%s: open: %v", name, err)
		}
		_, findErr := postgres.NewRepository(db, ids).FindByID(context.Background(), job.ID())
		_ = db.Close()

		if findErr == nil {
			t.Fatalf("%s: expected the connection to be refused", name)
		}
		// The fixture proves what it claims only if the driver really did
		// deliver a ConnectError carrying the server's own SQLSTATE.
		var connErr *pgconn.ConnectError
		if !errors.As(findErr, &connErr) {
			t.Fatalf("%s: expected a *pgconn.ConnectError, got %T: %v", name, findErr, findErr)
		}
		var pgErr *pgconn.PgError
		if !errors.As(findErr, &pgErr) {
			t.Fatalf("%s: expected an embedded *pgconn.PgError, got %v", name, findErr)
		}
		if pgErr.SQLState() != tc.state {
			t.Fatalf("%s: expected SQLSTATE %s, got %s", name, tc.state, pgErr.SQLState())
		}
		if errors.Is(findErr, domain.ErrRepositoryUnavailable) {
			t.Errorf("%s: a refusal the server answered was marked as unavailability: %v", name, findErr)
		}
	}
}

// TestACancelledContextIsNotUnavailability covers a caller's own cancellation
// reaching the repository. It pins the shape, not the rule order: a context
// already done is intercepted by database/sql before the driver is touched, so
// the bare context.Canceled it yields matches nothing in the permission list
// and this test would pass with the first rule deleted. The rule order is
// pinned in errors_test.go, by the fixture and by the live expiring-deadline
// case.
func TestACancelledContextIsNotUnavailability(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	job := newTestJob(t, ids, "user-1", "video.mp4")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := repo.FindByID(ctx, job.ID()); err == nil {
		t.Fatal("expected the cancelled context to fail the query")
	} else if errors.Is(err, domain.ErrRepositoryUnavailable) {
		t.Errorf("a cancelled context was marked as unavailability: %v", err)
	}
}

// TestAServerAnsweredRefusalIsNotUnavailability is discrimination (b): the
// server is reachable and answers 42703 undefined_column, which no retry can
// change. Under the call-site rule this change rejects, a half-applied
// migration would mark every call unavailable and spin every replica forever.
//
// It runs against its own database, created and dropped here.
func TestAServerAnsweredRefusalIsNotUnavailability(t *testing.T) {
	base := testDSN(t)
	ctx := context.Background()

	admin, err := postgres.Open(postgres.Config{DSN: base})
	if err != nil {
		t.Fatalf("open admin database: %v", err)
	}
	// Registered before the database cleanup below so it runs after it:
	// t.Cleanup is LIFO, and DROP DATABASE needs this pool alive.
	t.Cleanup(func() { _ = admin.Close() })

	// The name is a compile-time constant, not caller input, and CREATE
	// DATABASE takes no parameters.
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+unavailabilityTestDatabase); err != nil && !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("create %s: %v", unavailabilityTestDatabase, err)
	}

	db, err := postgres.Open(postgres.Config{DSN: rewriteDSN(t, base, unavailabilityTestDatabase)})
	if err != nil {
		t.Fatalf("open %s: %v", unavailabilityTestDatabase, err)
	}
	// DROP DATABASE is refused while a connection to it is open, and it must
	// run from a connection to a different database — hence the admin pool
	// above, kept alive for exactly this.
	t.Cleanup(func() {
		_ = db.Close()
		if _, err := admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+unavailabilityTestDatabase); err != nil {
			t.Errorf("drop %s: %v", unavailabilityTestDatabase, err)
		}
	})

	if err := postgres.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.ExecContext(ctx, "ALTER TABLE video_jobs DROP COLUMN content_hash"); err != nil {
		t.Fatalf("drop column: %v", err)
	}

	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	job := newTestJob(t, ids, "user-1", "video.mp4")

	_, findErr := repo.FindByID(ctx, job.ID())
	if findErr == nil {
		t.Fatal("expected FindByID to fail against a table missing a selected column")
	}
	var pgErr *pgconn.PgError
	if !errors.As(findErr, &pgErr) {
		t.Fatalf("expected a *pgconn.PgError, got %T: %v", findErr, findErr)
	}
	if pgErr.SQLState() != "42703" {
		t.Fatalf("expected SQLSTATE 42703, got %s", pgErr.SQLState())
	}
	if errors.Is(findErr, domain.ErrRepositoryUnavailable) {
		t.Errorf("a server-answered refusal was marked as unavailability: %v", findErr)
	}
	if errors.Is(findErr, domain.ErrVideoJobNotFound) {
		t.Errorf("a server-answered refusal was reported as a missing job: %v", findErr)
	}
}

// TestARowThatWillNotReconstructIsNotUnavailability is discrimination (c).
// The row is written directly through SQL because no code path can produce a
// status outside the closed set.
func TestARowThatWillNotReconstructIsNotUnavailability(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	rawID := uuid.NewString()
	const insert = `
		INSERT INTO video_jobs (id, user_id, original_filename, status, frame_count, error_reason, source_key, content_hash, storage_key, created_at, lease_epoch)
		VALUES ($1, $2, $3, $4, 0, '', '', '', '', $5, 0)
	`
	if _, err := db.ExecContext(ctx, insert, rawID, "user-1", "video.mp4", "teleported", time.Now().UTC().Truncate(time.Microsecond)); err != nil {
		t.Fatalf("seed unreconstructible row: %v", err)
	}

	id, err := domain.NewVideoJobID(rawID)
	if err != nil {
		t.Fatalf("parse id: %v", err)
	}

	_, findErr := repo.FindByID(ctx, id)
	if findErr == nil {
		t.Fatal("expected FindByID to refuse a status outside the closed set")
	}
	if errors.Is(findErr, domain.ErrRepositoryUnavailable) {
		t.Errorf("an unreconstructible row was marked as unavailability: %v", findErr)
	}
}

// TestAnUnknownIDIsNotUnavailability is discrimination (d). It is the
// ordering the whole marking depends on: sql.ErrNoRows must reach
// ErrVideoJobNotFound before anything is marked, or the worker would retry an
// unknown job forever instead of dead-lettering it.
func TestAnUnknownIDIsNotUnavailability(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	id, err := domain.NewVideoJobID(uuid.NewString())
	if err != nil {
		t.Fatalf("parse id: %v", err)
	}

	_, findErr := repo.FindByID(ctx, id)
	if !errors.Is(findErr, domain.ErrVideoJobNotFound) {
		t.Fatalf("expected ErrVideoJobNotFound, got %v", findErr)
	}
	if errors.Is(findErr, domain.ErrRepositoryUnavailable) {
		t.Errorf("a missing row was marked as unavailability: %v", findErr)
	}

	// The same ordering on the claim step's existence probe, which is its
	// own statement and its own wrap site.
	job := newTestJob(t, ids, "user-1", "video.mp4")
	_, _, claimErr := repo.ClaimForProcessing(ctx, job)
	if !errors.Is(claimErr, domain.ErrVideoJobNotFound) {
		t.Fatalf("expected ErrVideoJobNotFound from the claim probe, got %v", claimErr)
	}
	if errors.Is(claimErr, domain.ErrRepositoryUnavailable) {
		t.Errorf("a missing row was marked as unavailability by the claim probe: %v", claimErr)
	}
}
