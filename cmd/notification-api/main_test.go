package main

import (
	"fmt"
	"os"
	"testing"
)

// TestMain fixes the working directory and gates nothing. go test sets the
// working directory to this package's own, while the source scans in
// notification_test.go name paths from the repository root — the same reason
// cmd/api chdirs.
//
// There is deliberately no prerequisite gate. No test in this package opens a
// PostgreSQL pool or reaches Redis: the routes are exercised over an
// in-memory repository and a fake limiter, so a NOTIFICATION_POSTGRES_TEST_DSN
// or REDIS_ADDR check would refuse to run a suite that needs neither. The
// adapters those variables configure are covered by their own packages'
// suites, and development-workflow's non-zero-exit requirement is about a
// hard runtime prerequisite of the integration tests — which for this package
// is nothing at all.
func TestMain(m *testing.M) {
	if err := os.Chdir("../.."); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: failed to chdir to repo root: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}
