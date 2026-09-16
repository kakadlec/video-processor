package postgres_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"video-processor/internal/video/infrastructure/idgen"
	"video-processor/internal/video/infrastructure/postgres"
)

// gauge is one gathered sample, flattened so an assertion can be written
// about a label without naming the client library's wire types.
type gauge struct {
	name  string
	label string
	value float64
}

// collect registers the collector into a registry of its own and gathers it
// once. A pedantic registry rather than the process-wide one: what is under
// test is this collector's output, and a shared registry would make every
// assertion here depend on what the rest of the binary recorded.
func collect(t *testing.T, db *sql.DB) ([]gauge, error) {
	t.Helper()

	registry := prometheus.NewPedanticRegistry()
	registry.MustRegister(postgres.NewPipelineCollector(postgres.NewRepository(db, idgen.New())))

	families, err := registry.Gather()
	if err != nil {
		return nil, err
	}

	var gathered []gauge
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			point := gauge{name: family.GetName(), value: metric.GetGauge().GetValue()}
			for _, pair := range metric.GetLabel() {
				point.label = pair.GetValue()
			}
			gathered = append(gathered, point)
		}
	}
	return gathered, nil
}

func mustCollect(t *testing.T, db *sql.DB) []gauge {
	t.Helper()

	gathered, err := collect(t, db)
	if err != nil {
		t.Fatalf("gathering failed: %v", err)
	}
	return gathered
}

func find(t *testing.T, gathered []gauge, name, label string) (float64, bool) {
	t.Helper()

	for _, point := range gathered {
		if point.name == name && point.label == label {
			return point.value, true
		}
	}
	return 0, false
}

func requireGauge(t *testing.T, gathered []gauge, name, label string, want float64) {
	t.Helper()

	got, ok := find(t, gathered, name, label)
	if !ok {
		t.Fatalf("%s{%q} is absent; a successful collection reports every member of its closed set", name, label)
	}
	if got != want {
		t.Fatalf("%s{%q} = %g, want %g", name, label, got, want)
	}
}

func seed(t *testing.T, db *sql.DB, statement string, args ...any) {
	t.Helper()

	if _, err := db.ExecContext(context.Background(), statement, args...); err != nil {
		t.Fatalf("seeding failed: %v", err)
	}
}

const insertJobs = `INSERT INTO video_jobs (id, user_id, original_filename, status, created_at)
	SELECT gen_random_uuid(), 'u', 'f.mp4', $1, now() - make_interval(secs => $2::int)
	FROM generate_series(1, $3) AS n`

// TestOnlyTheInFlightStatesAreReported holds the set the gauge carries.
// pending is excluded on a stronger footing than cost — a job created through
// the job-lifecycle API has no processing trigger and stays pending
// permanently by design, so a count of them climbs monotonically and
// describes nothing — and the terminal states are excluded because the
// interesting quantity for a state a job enters once is a rate.
func TestOnlyTheInFlightStatesAreReported(t *testing.T) {
	db := testDB(t)
	for _, status := range []string{"pending", "queued", "processing", "completed", "failed"} {
		seed(t, db, insertJobs, status, 10, 1)
	}

	gathered := mustCollect(t, db)
	requireGauge(t, gathered, "fiapx_video_jobs_in_state", "queued", 1)
	requireGauge(t, gathered, "fiapx_video_jobs_in_state", "processing", 1)
	for _, excluded := range []string{"pending", "completed", "failed"} {
		if _, ok := find(t, gathered, "fiapx_video_jobs_in_state", excluded); ok {
			t.Errorf("the gauge reports %q, which is not an in-flight state", excluded)
		}
	}
}

// TestAnEmptyStateIsAZeroAndNotASilence holds the half of the collector's
// rule that is easy to get wrong in the direction that looks tidy. A bare
// GROUP BY returns no row for a state with no jobs, so a drained queue would
// vanish from the exposition by the very mechanism a dead database uses — and
// the distinction between nothing is waiting and the value could not be
// computed would be unmakeable at the moment it matters.
func TestAnEmptyStateIsAZeroAndNotASilence(t *testing.T) {
	db := testDB(t)
	seed(t, db, insertJobs, "processing", 30, 1)

	gathered := mustCollect(t, db)

	requireGauge(t, gathered, "fiapx_video_jobs_in_state", "queued", 0)
	if _, ok := find(t, gathered, "fiapx_video_jobs_oldest_in_state_age_seconds", "queued"); ok {
		t.Fatal("an empty state reports an age; there is no oldest row for it to describe")
	}
	if age, ok := find(t, gathered, "fiapx_video_jobs_oldest_in_state_age_seconds", "processing"); !ok || age < 25 {
		t.Fatalf("the processing age is %g (present=%t), want roughly 30s", age, ok)
	}
}

// TestTheOldestQueuedAgeClimbsWhileNothingConsumes is the gauge this change
// is bought for: it distinguishes uploads are being accepted and nothing is
// consuming them from the system is idle, which is the condition currently
// undetectable from outside.
func TestTheOldestQueuedAgeClimbsWhileNothingConsumes(t *testing.T) {
	db := testDB(t)
	seed(t, db, insertJobs, "queued", 60, 3)

	first, ok := find(t, mustCollect(t, db), "fiapx_video_jobs_oldest_in_state_age_seconds", "queued")
	if !ok {
		t.Fatal("the queued age is absent while three jobs are queued")
	}
	time.Sleep(1100 * time.Millisecond)
	second, _ := find(t, mustCollect(t, db), "fiapx_video_jobs_oldest_in_state_age_seconds", "queued")

	requireGauge(t, mustCollect(t, db), "fiapx_video_jobs_in_state", "queued", 3)
	if second <= first {
		t.Fatalf("the queued age went from %g to %g; it must climb while nothing consumes", first, second)
	}
}

// TestTheCountSaturatesAndTheAgeDoesNot holds the asymmetry. A saturated
// count still reports at least this many, and past the bound it is the age
// that carries how bad it is — which is the number an operator acts on.
func TestTheCountSaturatesAndTheAgeDoesNot(t *testing.T) {
	db := testDB(t)
	seed(t, db, insertJobs, "queued", 1, 10000)
	seed(t, db, insertJobs, "queued", 7200, 1)

	gathered := mustCollect(t, db)

	requireGauge(t, gathered, "fiapx_video_jobs_in_state", "queued", float64(postgres.InFlightCountBound))
	age, ok := find(t, gathered, "fiapx_video_jobs_oldest_in_state_age_seconds", "queued")
	if !ok {
		t.Fatal("the age is absent past the count's bound")
	}
	if age < 7000 {
		t.Fatalf("the oldest-queued age is %g, want roughly 7200 — the age must not saturate with the count", age)
	}
}

// TestCreationEventsAreExcludedFromTheOutboxAggregate holds the restriction
// that is the difference between a useful number and one that climbs forever:
// creation events are written to the same table, are claimed by no relay, and
// keep published_at NULL permanently and by design.
func TestCreationEventsAreExcludedFromTheOutboxAggregate(t *testing.T) {
	db := testDB(t)
	const insertEvents = `INSERT INTO video_job_outbox (id, event_type, payload, occurred_at, published_at)
		SELECT gen_random_uuid(), $1, '{}'::jsonb, now() - make_interval(secs => $2::int), NULL
		FROM generate_series(1, $3) AS n`

	seed(t, db, insertEvents, "video_job.created", 600, 5)
	seed(t, db, insertEvents, "video_job.queued.v2", 45, 2)

	gathered := mustCollect(t, db)

	requireGauge(t, gathered, "fiapx_video_job_outbox_unpublished", "video_job.queued.v2", 2)
	for _, quiet := range []string{"video_job.completed.v1", "video_job.failed.v1"} {
		requireGauge(t, gathered, "fiapx_video_job_outbox_unpublished", quiet, 0)
		if _, ok := find(t, gathered, "fiapx_video_job_outbox_oldest_unpublished_age_seconds", quiet); ok {
			t.Errorf("%q reports an age with nothing unpublished", quiet)
		}
	}
	if _, ok := find(t, gathered, "fiapx_video_job_outbox_unpublished", "video_job.created"); ok {
		t.Fatal("the aggregate reports creation events, whose rows are a record rather than a pending dispatch")
	}
	if age, ok := find(t, gathered, "fiapx_video_job_outbox_oldest_unpublished_age_seconds", "video_job.queued.v2"); !ok || age > 300 {
		t.Fatalf("the dispatch backlog's age is %g (present=%t); the permanent creation backlog has leaked into it", age, ok)
	}
}

// TestAFailedCollectionReportsNoValueAtAll is the single most misleading
// thing this change could have shipped, asserted against rather than argued
// about. A queued count reading zero means nothing is waiting, which is the
// most reassuring statement this endpoint can make; emitting it because a
// query failed would turn a database outage into a green dashboard.
func TestAFailedCollectionReportsNoValueAtAll(t *testing.T) {
	db := testDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("closing the pool failed: %v", err)
	}

	gathered, err := collect(t, db)
	if err == nil {
		t.Fatal("a collection against a closed pool reported success; the scrape must be seen to have failed")
	}
	for _, point := range gathered {
		if point.name == "fiapx_video_jobs_in_state" {
			t.Fatalf("a failed collection emitted %s{%q} = %g", point.name, point.label, point.value)
		}
	}
}
