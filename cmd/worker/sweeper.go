package main

import (
	"context"
	"log"
	"time"

	videodomain "video-processor/internal/video/domain"
)

// The recovery sweep's tuning, all constants rather than configuration, on
// the same reasoning as the lease TTL and the status cache's entry TTL: these
// are correctness margins, not deployment preferences.
//
// sweepInterval is how often the scan runs, sweepBatchSize how many
// processing rows one cycle examines, and maxRequeues how many times a single
// job may be re-dispatched before the sweep gives up on it. The bound is what
// keeps an input that reliably kills its worker — one that exhausts memory,
// say — from being re-dispatched forever and taking down each replica in
// turn.
const (
	sweepInterval  = 60 * time.Second
	sweepBatchSize = 50
	maxRequeues    = 3
)

// sweeper returns jobs abandoned by a dead worker to the queue, and fails the
// ones that have exhausted their requeues.
//
// It is the only recovery trigger there is. The broker cannot be one: a
// redelivery arrives while the lease is still live, and the claim predicate
// names queued alone, so it could only be dead-lettered.
type sweeper struct {
	deps *workerDeps

	// cursor is the keyset position the next scan resumes from, carried
	// across cycles and reset to the zero VideoJobID on a short read. Without
	// it, one batch's worth of healthy long-running extractions would sort
	// first every cycle and an abandoned job behind them would never be
	// examined.
	cursor videodomain.VideoJobID

	// marks records jobs observed unleased, by the epoch they were observed
	// at, and the sweep acts on a job only when a later cycle confirms the
	// same mark at the same epoch.
	//
	// This is not belt-and-braces. A claim commits in PostgreSQL and its
	// lease is set in Redis immediately afterwards, so every healthy run is
	// briefly unleased; acting on that single observation would requeue a
	// live job, and at the bound it would fail one and delete its source. A
	// mark is dropped on a contrary observation — the job is leased or at a
	// different epoch — and whenever the lease store cannot answer, because
	// an observation made before an outage is not safe confirmation after
	// recovery. Mere absence from a batch does not drop it, since that would
	// mean no job is ever confirmed once the table holds more processing rows
	// than one batch.
	//
	// Kept worker-local. Sharing or persisting it would add a coordination
	// problem the conditional writes already solve.
	marks map[videodomain.VideoJobID]int64

	// seenThisRotation bounds marks. A job that finishes normally never
	// appears in the scan again, so its mark has nothing to drop it; every
	// id examined since the last wrap is recorded here, and on each wrap the
	// marks nobody saw are discarded.
	seenThisRotation map[videodomain.VideoJobID]struct{}
}

func newSweeper(deps *workerDeps) *sweeper {
	return &sweeper{
		deps:             deps,
		marks:            make(map[videodomain.VideoJobID]int64),
		seenThisRotation: make(map[videodomain.VideoJobID]struct{}),
	}
}

// run sweeps on every tick until ctx is cancelled.
func (s *sweeper) run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweep(ctx)
		}
	}
}

// sweep examines one batch of processing jobs.
func (s *sweeper) sweep(ctx context.Context) {
	jobs, err := s.deps.jobReader.FindProcessing(ctx, s.cursor, sweepBatchSize)
	if err != nil {
		log.Printf("video: worker: sweep: scan processing jobs: %v", err)
		return
	}

	// Logged once per cycle rather than once per job: a Redis outage makes
	// every job in the batch unanswerable, and one line per job would bury
	// the fact that the sweep did nothing.
	unreachable := 0
	for _, job := range jobs {
		epoch := job.LeaseEpoch()
		s.seenThisRotation[job.ID()] = struct{}{}

		held, err := s.deps.leases.Held(ctx, job.ID(), epoch)
		if err != nil {
			// The fail-closed half of the posture. "Cannot reach Redis" is
			// not evidence that a lease expired, and treating it as such
			// would take over every running job at once. It also breaks the
			// confirmation sequence: after connectivity returns, the first
			// successful not-held reading must start a fresh pair rather than
			// confirm a mark made before the outage.
			delete(s.marks, job.ID())
			unreachable++
			continue
		}
		if held {
			delete(s.marks, job.ID())
			continue
		}

		markedEpoch, marked := s.marks[job.ID()]
		if !marked || markedEpoch != epoch {
			s.marks[job.ID()] = epoch
			continue
		}

		s.recover(ctx, job, epoch)
	}
	if unreachable > 0 {
		log.Printf("video: worker: sweep: lease store unreachable for %d job(s), taking over none of them", unreachable)
	}

	// After the loop, never before it: the prune below discards marks for
	// jobs this rotation did not see, and it must see this batch's ids first
	// or it would drop the mark it has just set.
	s.advanceCursor(jobs)
}

// advanceCursor moves the scan past the last row examined, wrapping to the
// start on a short read — and, on that wrap, discarding the marks of jobs
// that no longer appear in the scan at all.
//
// It runs at the end of a cycle, after the loop has recorded what this
// rotation saw. A job that finishes normally never appears in the scan again,
// so without this prune its mark would live for the life of the process; with
// it, the map is bounded by the number of processing jobs seen in one
// rotation.
func (s *sweeper) advanceCursor(jobs []*videodomain.VideoJob) {
	if len(jobs) == sweepBatchSize {
		s.cursor = jobs[len(jobs)-1].ID()
		return
	}
	s.cursor = videodomain.VideoJobID{}
	for id := range s.marks {
		if _, seen := s.seenThisRotation[id]; !seen {
			delete(s.marks, id)
		}
	}
	s.seenThisRotation = make(map[videodomain.VideoJobID]struct{})
}

// recover acts on a job confirmed unleased at epoch: it re-dispatches it, or
// gives up on it.
func (s *sweeper) recover(ctx context.Context, job *videodomain.VideoJob, epoch int64) {
	// A job with no source key is failed rather than requeued. These are the
	// rows predating the source_key column, and requeueing one is a loop with
	// no exit: the aggregate rejects a requeue with no source, so the epoch
	// never advances and the bound is never reached. Filtering them out of
	// the scan instead would leave them stranded, which is the condition this
	// sweep exists to end.
	if job.SourceKey().IsZero() {
		log.Printf("video: worker: sweep: job %s is processing with no source, failing it", job.ID().String())
		s.abandon(ctx, job, epoch)
		return
	}
	if epoch >= maxRequeues {
		log.Printf("video: worker: sweep: job %s has been requeued %d time(s), failing it", job.ID().String(), epoch)
		s.abandon(ctx, job, epoch)
		return
	}

	if err := job.Requeue(); err != nil {
		log.Printf("video: worker: sweep: requeue job %s: %v", job.ID().String(), err)
		return
	}
	requeued, err := s.deps.jobWriter.Requeue(ctx, job, epoch)
	if err != nil {
		log.Printf("video: worker: sweep: requeue job %s: %v", job.ID().String(), err)
		return
	}
	if !requeued {
		// Normal, not an error: another sweeper won, or the job left
		// processing between the scan and this write. Nothing to retry.
		log.Printf("video: worker: sweep: job %s was no longer at epoch %d, leaving it alone", job.ID().String(), epoch)
		return
	}
	delete(s.marks, job.ID())
	log.Printf("video: worker: sweep: requeued job %s at epoch %d", job.ID().String(), job.LeaseEpoch())
}

// abandon fails the job at the epoch the scan observed and, only if that
// write is the one that landed, disposes of what the job still holds.
//
// The applied test is the whole guarantee. Two sweepers at the bound write a
// byte-identical intent — same epoch, same failed status, same fixed reason —
// so matching the stored row proves nothing about who wrote it. Exclusivity
// comes from the terminal statement's own predicate, which requires the row
// to still be processing: whoever gets there first leaves it terminal and
// every other write affects no row.
func (s *sweeper) abandon(ctx context.Context, job *videodomain.VideoJob, epoch int64) {
	result, err := s.deps.fail.Abandon(ctx, job.ID().String(), epoch)
	if err != nil {
		log.Printf("video: worker: sweep: fail abandoned job %s: %v", job.ID().String(), err)
		return
	}
	delete(s.marks, job.ID())
	if !result.Applied {
		return
	}
	if !job.SourceKey().IsZero() {
		s.deps.deleteSource(ctx, job.ID().String(), job.SourceKey())
	}
	s.deps.clearIdempotencyKey(ctx, job.ID().String())
	log.Printf("video: worker: sweep: job %s failed after abandonment at epoch %d", job.ID().String(), epoch)
}
