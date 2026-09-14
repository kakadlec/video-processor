package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"video-processor/internal/video/domain"
)

// TestUnavailabilityIsDecidedByTheEnumeratedSQLStates pins the decision the
// classifier makes for a server-answered error, state by state. It is the
// test a later simplification to a class prefix — SQLState()[:2] == "57" —
// would fail: 57014 and 57P04 share class 57 with the two admitted states and
// are refused by name.
func TestUnavailabilityIsDecidedByTheEnumeratedSQLStates(t *testing.T) {
	admitted := []string{
		"57P01", // admin_shutdown
		"57P03", // cannot_connect_now
		"53300", // too_many_connections
	}
	refused := []string{
		"57014", // query_canceled, class 57 and not an outage
		"57P04", // database_dropped, class 57 and permanent
		"42703", // undefined_column
		"42P01", // undefined_table
		"42501", // insufficient_privilege
		"23505", // unique_violation
		"28P01", // invalid_password
		"3D000", // invalid_catalog_name
		"08006", // class 08 was never observed reaching a caller
		"58030", // class 58 was never observed reaching a caller
		"53200", // out_of_memory: class 53, not the admitted state
	}

	for _, state := range admitted {
		err := &pgconn.PgError{Code: state, Message: "provoked"}
		if !isUnavailable(err) {
			t.Errorf("SQLSTATE %s: expected unavailability, got none", state)
		}
		if !errors.Is(markUnavailable(err), domain.ErrRepositoryUnavailable) {
			t.Errorf("SQLSTATE %s: marked error does not carry the sentinel", state)
		}
	}
	for _, state := range refused {
		err := &pgconn.PgError{Code: state, Message: "provoked"}
		if isUnavailable(err) {
			t.Errorf("SQLSTATE %s: expected no unavailability", state)
		}
		if errors.Is(markUnavailable(err), domain.ErrRepositoryUnavailable) {
			t.Errorf("SQLSTATE %s: unmarked error carries the sentinel", state)
		}
	}
}

// timeoutError satisfies net.Error with Timeout() true while wrapping a
// context deadline, which is the shape pgx reports a context deadline in. It
// exists to pin the first rule ahead of the last one: judged as a net.Error,
// a deadline this caller set would be read as the server being unavailable.
type timeoutError struct{ error }

func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
func (e timeoutError) Unwrap() error { return e.error }

// TestAContextErrorIsNotUnavailabilityEvenWhenItIsAlsoANetError is what pins
// the first rule. The hazard is not hypothetical and not confined to pgx:
// context.DeadlineExceeded is itself a net.Error with Timeout() true, and
// database/sql returns it directly when the context is already done, so
// deleting the first rule admits it through the last one.
func TestAContextErrorIsNotUnavailabilityEvenWhenItIsAlsoANetError(t *testing.T) {
	cases := map[string]error{
		"canceled":           context.Canceled,
		"deadline exceeded":  context.DeadlineExceeded,
		"wrapped canceled":   fmt.Errorf("video: claim: %w", context.Canceled),
		"net.Error deadline": timeoutError{context.DeadlineExceeded},
	}
	for name, err := range cases {
		var netErr net.Error
		if name == "net.Error deadline" && !errors.As(err, &netErr) {
			t.Fatalf("%s: test fixture does not satisfy net.Error, so it pins nothing", name)
		}
		if isUnavailable(err) {
			t.Errorf("%s: expected no unavailability", name)
		}
	}
}

func TestATransportFailureIsUnavailability(t *testing.T) {
	cases := map[string]error{
		"unexpected EOF":  io.ErrUnexpectedEOF,
		"wrapped net op":  fmt.Errorf("video: dial: %w", &net.OpError{Op: "dial", Err: errors.New("connection refused")}),
		"net.Error alone": &net.OpError{Op: "read", Err: errors.New("connection reset by peer")},
	}
	for name, err := range cases {
		if !isUnavailable(err) {
			t.Errorf("%s: expected unavailability", name)
		}
	}
}

// TestAFailureInterpretingAReceivedRowIsNotUnavailability covers the half of
// the classification a DDL test cannot reach: the server answered with a row
// and database/sql could not convert it. Both errors are produced by
// database/sql itself rather than written out as literals.
func TestAFailureInterpretingAReceivedRowIsNotUnavailability(t *testing.T) {
	var nullTime sql.NullTime
	conversion := nullTime.Scan("not a time")
	if conversion == nil {
		t.Fatal("expected a conversion error from database/sql")
	}
	var nullInt sql.NullInt64
	syntax := nullInt.Scan("abc")
	if syntax == nil {
		t.Fatal("expected a conversion error from database/sql")
	}

	for _, err := range []error{conversion, syntax} {
		if isUnavailable(err) {
			t.Errorf("%v: expected no unavailability", err)
		}
	}

	// The same error reaching a caller through the adapter's own scan path.
	repo := NewRepository(nil, nil)
	if _, err := repo.scanJobRow(stubScanner{err: conversion}); err == nil {
		t.Fatal("expected scanJobRow to fail")
	} else if errors.Is(err, domain.ErrRepositoryUnavailable) {
		t.Errorf("scanJobRow marked a received-row failure as unavailability: %v", err)
	}
}

// TestScanJobRowLeavesErrNoRowsUnwrapped pins the ordering the marking depends
// on. scanJob maps ErrNoRows to ErrVideoJobNotFound, and it can only do that
// while this branch returns the sentinel untouched — a not-found row carrying
// the unavailability marker would be retried forever instead of dead-lettered.
func TestScanJobRowLeavesErrNoRowsUnwrapped(t *testing.T) {
	repo := NewRepository(nil, nil)
	_, err := repo.scanJobRow(stubScanner{err: sql.ErrNoRows})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows, got %v", err)
	}
	if errors.Is(err, domain.ErrRepositoryUnavailable) {
		t.Error("sql.ErrNoRows carries the unavailability sentinel")
	}
}

func TestMarkUnavailablePreservesTheCause(t *testing.T) {
	cause := &pgconn.PgError{Code: "57P01", Message: "terminating connection due to administrator command"}
	marked := markUnavailable(cause)
	if !errors.Is(marked, domain.ErrRepositoryUnavailable) {
		t.Fatal("marked error does not carry the sentinel")
	}
	var pgErr *pgconn.PgError
	if !errors.As(marked, &pgErr) || pgErr.Code != "57P01" {
		t.Fatalf("original error was not preserved as the cause: %v", marked)
	}
	if markUnavailable(nil) != nil {
		t.Error("markUnavailable(nil) should stay nil")
	}
	plain := errors.New("something else")
	if got := markUnavailable(plain); got != plain {
		t.Errorf("an unadmitted error was rewritten: %v", got)
	}
}

type stubScanner struct{ err error }

func (s stubScanner) Scan(_ ...any) error { return s.err }

// TestADeadlineExpiringDuringAStatementIsNotUnavailability is the live half of
// the first rule: a context that expires while the server is busy is reported
// by pgx as an error satisfying net.Error with Timeout() true, which is the
// shape timeoutError above models. It is in-package because the classifier it
// questions is unexported, and it needs a real server because the type pgx
// wraps the context error in is unexported too.
func TestADeadlineExpiringDuringAStatementIsNotUnavailability(t *testing.T) {
	dsn := os.Getenv("VIDEO_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("VIDEO_POSTGRES_TEST_DSN not set; skipping PostgreSQL integration test")
	}
	db, err := Open(Config{DSN: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	var ignored int
	queryErr := db.QueryRowContext(ctx, "SELECT pg_sleep(2)").Scan(&ignored)
	if queryErr == nil {
		t.Fatal("expected the deadline to interrupt the statement")
	}
	var netErr net.Error
	if !errors.As(queryErr, &netErr) || !netErr.Timeout() {
		t.Fatalf("expected a timing-out net.Error, got %T: %v", queryErr, queryErr)
	}
	if isUnavailable(queryErr) {
		t.Errorf("an expired deadline was classified as unavailability: %v", queryErr)
	}
}
