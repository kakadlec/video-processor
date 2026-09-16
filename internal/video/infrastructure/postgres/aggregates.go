package postgres

import (
	"context"
	"database/sql"
	"fmt"
)

// InFlightCountBound is where a count saturates, and it is normative here
// because the statement, the gauge that reports it and the test that asserts
// the saturation all read the same number — three artifacts choosing it
// independently is how they come to disagree.
//
// The asymmetry with the age, which does not saturate, follows from the
// costs. A count over a partial index costs work proportional to the number
// of matching rows, in exactly the case that repeats on every collection
// interval for as long as the backlog lasts, while the oldest-age lookup is a
// single ordered row from the same index. 10,000 sits about three orders of
// magnitude above any healthy value — three workers at prefetch 1 make a
// healthy processing count three and a healthy queued count single digits —
// and a bounded index read of at most that many entries stays well under a
// millisecond. A saturated count still reports at least this many, and past
// the bound it is the age that says how bad it is.
const InFlightCountBound = 10000

// The in-flight states, and no others. They are exported so the collector
// composes over the same closed set the statements below enumerate, rather
// than over a second copy of it.
const (
	InFlightStateQueued     = "queued"
	InFlightStateProcessing = "processing"
)

// InFlightStates returns the closed set, in the order a job passes through
// it.
func InFlightStates() []string {
	return []string{InFlightStateQueued, InFlightStateProcessing}
}

// RelayedEventTypes returns the event types the two relays claim on. The
// aggregate below is restricted to these rather than computed over every
// unpublished row: creation events are written to the same table, are claimed
// by no relay, and keep published_at NULL permanently and by design, so an
// unrestricted aggregate would report a backlog that grows for the life of
// the deployment while describing nothing that is pending.
func RelayedEventTypes() []string {
	return []string{videoJobQueuedEventType, videoJobCompletedEventType, videoJobFailedEventType}
}

// Aggregate is one entry of a bounded aggregate: a count, and the age of the
// oldest row behind it.
//
// The age is seconds computed by the statement, subtracting created_at or
// occurred_at from this statement's own now(). Both of those columns are now
// minted by PostgreSQL itself — Repository.Create, Enqueue, Requeue, and
// Update all read the writing transaction's own now() rather than an
// application clock (see transactionNow in repository.go) — so this
// subtraction compares PostgreSQL against PostgreSQL, not a writer's clock
// against a reader's. An earlier version of this comment recorded the
// opposite; that was the two-clock problem docs/roadmap.md's
// mint-videojob-timestamps-in-database entry closed.
//
// The GREATEST(…, 0) clamp below is kept anyway, now as a defensive bound
// rather than a load-bearing one: nothing in ordinary operation should ever
// produce a negative age once both sides of the subtraction are PostgreSQL's
// own clock, but a negative age is still a value no age can take, and a
// clamped zero reads as "the oldest is brand new" rather than surfacing an
// impossible number to whatever reads the gauge.
//
// OldestAgeValid is carried rather than inferred from a zero age, because the
// absence of an age is load-bearing. The collector reading these
// distinguishes nothing is waiting from the value could not be computed by
// emitting a sample in the first case and none in the second, so an empty set
// has to arrive as a count of zero with no age — not as a missing entry, and
// not as an age of zero, which is what a row written this instant has.
type Aggregate struct {
	Key            string
	Count          int
	OldestAge      float64
	OldestAgeValid bool
}

// Each in-flight state's predicate is written as a literal, one UNION ALL
// branch per state, rather than supplied as a parameter. A partial index
// whose predicate is a literal is matched only when the planner can prove the
// query's predicate implies it, which it cannot do for a value it does not
// yet have — so the tidier WHERE status = $1 would quietly forfeit both
// partial indexes this change adds.
//
// The oldest-age lookup is written as an explicitly ordered single row rather
// than as min(created_at), so what the index is for is visible at the call
// site rather than resting on a planner transformation. Its plan is asserted
// rather than assumed — see the EXPLAIN test beside this file.
//
// The count is taken over a bounded subquery, which is what makes it
// saturate. The bound is a parameter because it bounds the read rather than
// selecting rows, so it costs the index match nothing and keeps the number in
// exactly one place.
const inFlightAggregateQuery = `
SELECT 'queued' AS key,
       (SELECT count(*) FROM (SELECT 1 FROM video_jobs WHERE status = 'queued' LIMIT $1) AS bounded) AS total,
       (SELECT GREATEST(EXTRACT(EPOCH FROM now() - created_at), 0) FROM video_jobs WHERE status = 'queued' ORDER BY created_at ASC LIMIT 1) AS oldest_age
UNION ALL
SELECT 'processing' AS key,
       (SELECT count(*) FROM (SELECT 1 FROM video_jobs WHERE status = 'processing' LIMIT $1) AS bounded) AS total,
       (SELECT GREATEST(EXTRACT(EPOCH FROM now() - created_at), 0) FROM video_jobs WHERE status = 'processing' ORDER BY created_at ASC LIMIT 1) AS oldest_age`

// The outbox aggregate, restricted to the relayed event types by the same
// device and for the same reason: one literal branch per type, so each is
// served by video_job_outbox_unpublished_idx, whose leading column is
// event_type.
const unpublishedOutboxAggregateQuery = `
SELECT 'video_job.queued.v2' AS key,
       (SELECT count(*) FROM (SELECT 1 FROM video_job_outbox WHERE event_type = 'video_job.queued.v2' AND published_at IS NULL LIMIT $1) AS bounded) AS total,
       (SELECT GREATEST(EXTRACT(EPOCH FROM now() - occurred_at), 0) FROM video_job_outbox WHERE event_type = 'video_job.queued.v2' AND published_at IS NULL ORDER BY occurred_at ASC LIMIT 1) AS oldest_age
UNION ALL
SELECT 'video_job.completed.v1' AS key,
       (SELECT count(*) FROM (SELECT 1 FROM video_job_outbox WHERE event_type = 'video_job.completed.v1' AND published_at IS NULL LIMIT $1) AS bounded) AS total,
       (SELECT GREATEST(EXTRACT(EPOCH FROM now() - occurred_at), 0) FROM video_job_outbox WHERE event_type = 'video_job.completed.v1' AND published_at IS NULL ORDER BY occurred_at ASC LIMIT 1) AS oldest_age
UNION ALL
SELECT 'video_job.failed.v1' AS key,
       (SELECT count(*) FROM (SELECT 1 FROM video_job_outbox WHERE event_type = 'video_job.failed.v1' AND published_at IS NULL LIMIT $1) AS bounded) AS total,
       (SELECT GREATEST(EXTRACT(EPOCH FROM now() - occurred_at), 0) FROM video_job_outbox WHERE event_type = 'video_job.failed.v1' AND published_at IS NULL ORDER BY occurred_at ASC LIMIT 1) AS oldest_age`

// InFlightJobAggregate reports how many jobs are in each in-flight state and
// how old the oldest of each is.
//
// It is a method on this concrete repository and deliberately not on
// domain.VideoJobRepository: widening the port would oblige the cache
// decorator and every application-layer test double to carry two methods that
// exist for one collector in one process, and it would make it possible to
// wire the collector through the decorator — which must not happen, because a
// cached count is a count from a different moment.
func (r *Repository) InFlightJobAggregate(ctx context.Context) ([]Aggregate, error) {
	return r.aggregate(ctx, inFlightAggregateQuery, "in-flight job aggregate")
}

// UnpublishedOutboxAggregate reports how many unpublished events of each
// relayed type are waiting and how old the oldest of each is.
//
// This is the aggregate the whole shape exists for. Both relays claim from
// one table filtered on event_type, and the relay carrying terminal outcomes
// runs inside the worker — a process that serves nothing and may not. Reading
// this from the HTTP service's own pool is how the state of that relay
// becomes observable at all.
func (r *Repository) UnpublishedOutboxAggregate(ctx context.Context) ([]Aggregate, error) {
	return r.aggregate(ctx, unpublishedOutboxAggregateQuery, "unpublished outbox aggregate")
}

func (r *Repository) aggregate(ctx context.Context, query, what string) ([]Aggregate, error) {
	rows, err := r.db.QueryContext(ctx, query, InFlightCountBound)
	if err != nil {
		return nil, fmt.Errorf("video: read the %s: %w", what, markUnavailable(err))
	}
	defer rows.Close()

	var aggregates []Aggregate
	for rows.Next() {
		var (
			entry Aggregate
			age   sql.NullFloat64
		)
		if err := rows.Scan(&entry.Key, &entry.Count, &age); err != nil {
			return nil, fmt.Errorf("video: scan the %s: %w", what, markUnavailable(err))
		}
		entry.OldestAge, entry.OldestAgeValid = age.Float64, age.Valid
		aggregates = append(aggregates, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("video: read the %s: %w", what, markUnavailable(err))
	}
	return aggregates, nil
}
