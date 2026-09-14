package postgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/jackc/pgx/v5/pgconn"

	"video-processor/internal/video/domain"
)

// unavailableSQLStates is the permission list of SQLSTATEs that establish the
// server answered only that it cannot serve the statement now.
//
// It is an enumeration of individual states drawn from observation — each one
// was provoked against the pinned pgx v5.10.0 through database/sql and read
// off the error that actually reached the caller — and it is deliberately not
// a class prefix. Class 57 also carries 57014 query_canceled, an ordinary
// statement cancellation, and 57P04 database_dropped, which is permanent;
// both are excluded by name. Classes 08 and 58 are absent because no
// provocation produced one: every transport failure arrived as a raw net/io
// error or as 57P01. Widening this map means repeating that empirical pass,
// not reading the SQLSTATE registry.
var unavailableSQLStates = map[string]bool{
	"57P01": true, // admin_shutdown: the server terminated the connection
	"57P03": true, // cannot_connect_now: the server is starting up
	"53300": true, // too_many_connections
}

// isUnavailable reports whether err's own evidence establishes that the server
// did not, or could not, answer the statement. It is a permission list: an
// error carrying none of the enumerated evidence is refused the marker, so a
// failure mode nobody has classified keeps behaving as it does today. It
// never asks which driver method returned err — each of Scan, Query and Exec
// reports permanent and transient failures through the same value.
//
// The rule order is load-bearing in two places.
//
// The context rule precedes the net.Error rule because pgx reports a context
// deadline as an error satisfying net.Error with Timeout() true; judged there
// it would be read as an outage.
//
// The PgError rule is terminal and precedes the ConnectError rule because a
// rejected password (28P01) and a nonexistent database (3D000) both arrive as
// *pgconn.ConnectError with an embedded *pgconn.PgError. A ConnectError-first
// predicate would report a rotated credential as an outage and retry it
// forever.
func isUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return unavailableSQLStates[pgErr.SQLState()]
	}
	// Reached only when the server answered nothing at all: the connection
	// could not be established.
	var connErr *pgconn.ConnectError
	if errors.As(err, &connErr) {
		return true
	}
	// The connection was lost while the statement was in flight. Bare io.EOF
	// is deliberately absent: it was never observed reaching a caller.
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// markUnavailable returns err wrapped so errors.Is reaches
// domain.ErrRepositoryUnavailable when isUnavailable admits it, with err
// preserved as the cause, and returns err untouched otherwise.
//
// It is applied to the argument of an existing %w and never in place of one,
// which is what keeps every sql.ErrNoRows branch above it: those branches
// already return before their method's wrap site, so the not-found sentinel
// can never pick up this marker.
func markUnavailable(err error) error {
	if !isUnavailable(err) {
		return err
	}
	return fmt.Errorf("%w: %w", domain.ErrRepositoryUnavailable, err)
}
