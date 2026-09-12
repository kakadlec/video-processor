package main

import (
	"context"
	"database/sql"
	"sync"
	"time"
)

// The dependencies a readiness verdict may name. A closed set this package
// owns: no part of a readiness answer is ever assembled from a value a
// caller supplied.
const (
	dependencyDatabase = "postgres"
)

// readinessCheckTimeout bounds one dependency check. It has to stay strictly
// below the timeout of whatever probes the readiness endpoint — this
// service's healthcheck timeout in docker-compose.yml is the one that exists
// today. A check allowed to run longer than the prober waits makes the
// prober abandon every request, and then every verdict is a failure whatever
// the dependency is doing: the signal does not degrade, it inverts, and it
// inverts without emitting anything that says so.
const readinessCheckTimeout = 2 * time.Second

// readinessCheck pairs one dependency's name with the probe that answers for
// it. The name is drawn from the closed set above and never from anything a
// caller supplied, because it is what a log record will carry.
type readinessCheck struct {
	name  string
	probe func(context.Context) error
}

// readinessChecker answers whether this process can still serve. Its set of
// checks is this service's readiness matrix, and a dependency missing from
// that set is missing deliberately — see newReadinessChecker.
type readinessChecker struct {
	checks []readinessCheck
}

func newChecker(checks ...readinessCheck) *readinessChecker {
	return &readinessChecker{checks: checks}
}

// failingDependencies runs every check and reports the names of those that
// failed, in declaration order. An empty result means ready.
//
// The checks run concurrently, so the worst case is the slowest single check
// rather than the sum of all of them: running them in sequence would make
// the bound a function of how many dependencies a service happens to hold,
// which is not the quantity the prober's timeout was chosen against.
//
// Each check is bounded by readinessCheckTimeout derived from ctx, which
// does both halves of the obligation at once — a dependency that accepts a
// connection and never answers cannot hold the goroutine, and a caller that
// disconnects releases the work instead of paying out the bound.
//
// Nothing is cached, at any layer. An answer kept from an earlier probe is
// an answer for a different moment than the caller asked about, which is the
// failure the object store's own reachability check was written to avoid.
func (rc *readinessChecker) failingDependencies(ctx context.Context) []string {
	failed := make([]bool, len(rc.checks))

	var wg sync.WaitGroup
	for i, check := range rc.checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			checkCtx, cancel := context.WithTimeout(ctx, readinessCheckTimeout)
			defer cancel()
			failed[i] = check.probe(checkCtx) != nil
		}()
	}
	wg.Wait()

	// Declaration order rather than completion order: these names go into a
	// log record, and an attribute whose value depends on goroutine
	// scheduling is not a fact about the system.
	var names []string
	for i, check := range rc.checks {
		if failed[i] {
			names = append(names, check.name)
		}
	}
	return names
}

// newReadinessChecker builds this process's checker from the handles startup
// already holds. The set of checks is the readiness matrix for this service,
// and it is one entry long.
//
// PostgreSQL is a readiness dependency because both of this service's routes
// are a bcrypt hash and a row: with it unreachable, neither can be answered
// and nothing here is specified to degrade instead.
//
// Nothing else is consulted, and each absence is a decision rather than an
// omission. This process opens no cache client and no broker connection at
// all, and the two services that do treat both as survivable — every feature
// over the cache fails open, and the broker sits behind an outbox — so a
// readiness endpoint that consulted either would take a serving process out
// of rotation for a condition the system is designed to tolerate.
func newReadinessChecker(db *sql.DB) *readinessChecker {
	return newChecker(readinessCheck{name: dependencyDatabase, probe: db.PingContext})
}
