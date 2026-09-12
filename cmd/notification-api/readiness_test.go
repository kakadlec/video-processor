package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestTheReadinessCheckerConsultsExactlyItsMatrixRow is the negative half of
// this service's readiness matrix, and it is the half that is a decision
// rather than an obvious consequence. The names are read off the checker main
// builds, so a check added for the cache or for the broker fails here rather
// than shipping as an opinion: what this service does not consult, it cannot
// call while answering a probe, and a cache or broker outage therefore leaves
// readiness reporting ready.
//
// nil is a safe argument because a method value on a nil *sql.DB is formed
// without dereferencing it, and no probe is invoked here.
func TestTheReadinessCheckerConsultsExactlyItsMatrixRow(t *testing.T) {
	checker := newReadinessChecker(nil)

	var names []string
	for _, check := range checker.checks {
		names = append(names, check.name)
	}

	want := []string{dependencyDatabase}
	if len(names) != len(want) {
		t.Fatalf("readiness consults %v, want exactly %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("readiness consults %v, want exactly %v", names, want)
		}
	}
}

// readinessProbeOK answers immediately and successfully.
func readinessProbeOK(context.Context) error { return nil }

// readinessProbeFailing answers immediately with a failure.
func readinessProbeFailing(context.Context) error { return errors.New("the dependency is unreachable") }

// readinessProbeHanging stands in for a dependency that accepts a connection
// and never answers: it returns only when the context it was handed ends.
func readinessProbeHanging(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// readinessProbeSlow answers successfully after d, unless the context ends
// first.
func readinessProbeSlow(d time.Duration) func(context.Context) error {
	return func(ctx context.Context) error {
		select {
		case <-time.After(d):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func TestReadinessIsReadyWhenEveryDependencyAnswers(t *testing.T) {
	checker := newChecker(
		readinessCheck{name: "first", probe: readinessProbeOK},
		readinessCheck{name: "second", probe: readinessProbeOK},
	)

	if failing := checker.failingDependencies(context.Background()); len(failing) != 0 {
		t.Fatalf("expected no failing dependency, got %v", failing)
	}
}

func TestReadinessNamesEveryDependencyThatFailedInDeclarationOrder(t *testing.T) {
	// The first check is deliberately the slower of the two failures, so a
	// result assembled in completion order would come back reversed.
	checker := newChecker(
		readinessCheck{name: "first", probe: func(ctx context.Context) error {
			time.Sleep(20 * time.Millisecond)
			return errors.New("first is unreachable")
		}},
		readinessCheck{name: "second", probe: readinessProbeOK},
		readinessCheck{name: "third", probe: readinessProbeFailing},
	)

	failing := checker.failingDependencies(context.Background())
	if len(failing) != 2 || failing[0] != "first" || failing[1] != "third" {
		t.Fatalf(`expected ["first" "third"] in declaration order, got %v`, failing)
	}
}

// TestReadinessBoundsADependencyThatNeverAnswers pins the timeout half of the
// bound. The probe returns only when its context ends, so the elapsed time is
// the bound itself.
func TestReadinessBoundsADependencyThatNeverAnswers(t *testing.T) {
	checker := newChecker(readinessCheck{name: "hanging", probe: readinessProbeHanging})

	start := time.Now()
	failing := checker.failingDependencies(context.Background())
	elapsed := time.Since(start)

	if len(failing) != 1 || failing[0] != "hanging" {
		t.Fatalf(`expected ["hanging"], got %v`, failing)
	}
	if elapsed < readinessCheckTimeout {
		t.Fatalf("returned after %v, before its own bound of %v — the bound is not what ended it", elapsed, readinessCheckTimeout)
	}
	if elapsed > 2*readinessCheckTimeout {
		t.Fatalf("returned after %v, well past its bound of %v", elapsed, readinessCheckTimeout)
	}
}

// TestReadinessReleasesTheWorkWhenTheCallerDisconnects pins the other half,
// and it is the half that discriminates: a bound derived from
// context.Background() rather than from the request's would pass the timeout
// test above and fail this one, because the caller going away would buy
// nothing.
func TestReadinessReleasesTheWorkWhenTheCallerDisconnects(t *testing.T) {
	checker := newChecker(readinessCheck{name: "hanging", probe: readinessProbeHanging})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	defer cancel()

	start := time.Now()
	failing := checker.failingDependencies(ctx)
	elapsed := time.Since(start)

	if len(failing) != 1 || failing[0] != "hanging" {
		t.Fatalf(`expected ["hanging"], got %v`, failing)
	}
	if elapsed >= readinessCheckTimeout/2 {
		t.Fatalf("took %v after the caller disconnected; the check is not derived from the request context", elapsed)
	}
}

// TestReadinessRunsItsChecksConcurrently asserts the endpoint's worst case is
// the slowest single check and not the sum, which is what keeps the bound
// independent of how many dependencies a service happens to hold.
func TestReadinessRunsItsChecksConcurrently(t *testing.T) {
	const each = 200 * time.Millisecond

	checker := newChecker(
		readinessCheck{name: "first", probe: readinessProbeSlow(each)},
		readinessCheck{name: "second", probe: readinessProbeSlow(each)},
		readinessCheck{name: "third", probe: readinessProbeSlow(each)},
	)

	start := time.Now()
	failing := checker.failingDependencies(context.Background())
	elapsed := time.Since(start)

	if len(failing) != 0 {
		t.Fatalf("expected no failing dependency, got %v", failing)
	}
	if elapsed >= 2*each {
		t.Fatalf("three %v checks took %v; they ran in sequence rather than concurrently", each, elapsed)
	}
}

// TestReadinessOfANoDependencyCheckerIsReady covers the degenerate set
// directly rather than by inference: a checker holding nothing to consult
// reports ready, which is what every service would report if its matrix row
// were ever emptied.
func TestReadinessOfANoDependencyCheckerIsReady(t *testing.T) {
	if failing := newChecker().failingDependencies(context.Background()); len(failing) != 0 {
		t.Fatalf("expected no failing dependency, got %v", failing)
	}
}
