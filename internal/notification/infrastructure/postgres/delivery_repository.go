package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"video-processor/internal/notification/domain"
)

var _ domain.DeliveryRepository = (*DeliveryRepository)(nil)

const (
	// The whole grant decision, in one statement. It inserts when no record
	// exists and takes over a pending one whose claim is older than the
	// reclaim bound; a record that is resolved, or pending and still fresh,
	// matches neither branch's condition and the statement affects no row.
	//
	// Zero rows is therefore the refusal, and it is not an error: the
	// conflict clause's WHERE is evaluated against the stored row, and when
	// it is false the statement neither updates nor falls back to inserting.
	// TestClaimDelivery covers all four inputs against a real server rather
	// than trusting that reading.
	//
	// Two columns behave differently on the takeover branch, deliberately.
	// delivery_id is not assigned, so a reclaim keeps the identifier a
	// receiver may already have deduplicated on and the takeover stays one
	// logical delivery. claim_token is assigned from the proposed row, which
	// is what fences the previous holder out of resolving — the bound proves
	// its claim is old, not that the process making it stopped.
	//
	// $7 is bound once and read twice, as the status a new claim starts in
	// and as the only status a claim may be taken over from. The two are the
	// same value by necessity rather than coincidence: a delivery that is no
	// longer pending is finished, so there is nothing to reclaim.
	claimDeliveryQuery = `
		INSERT INTO notification_deliveries
		       (user_id, event_type, channel, job_id, delivery_id, claim_token, status, attempts, claimed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 0, $8)
		ON CONFLICT (user_id, event_type, channel, job_id) DO UPDATE
		   SET claim_token = EXCLUDED.claim_token,
		       claimed_at  = EXCLUDED.claimed_at,
		       attempts    = 0
		 WHERE notification_deliveries.status = $7
		   AND notification_deliveries.claimed_at < $9
		RETURNING delivery_id, claim_token, claimed_at
	`

	// Read on the refusal path only, to choose between acknowledging the
	// message and leaving it for a later attempt. It is deliberately not
	// authoritative and deliberately not part of the statement above: the
	// grant decision stays atomic, and this read only decides a disposition.
	// Reading it stale costs one more pass through the handler, never a
	// second outbound request — the claim it would re-run is the thing that
	// gates that.
	classifyRefusedClaimQuery = `
		SELECT status
		  FROM notification_deliveries
		 WHERE user_id = $1 AND event_type = $2 AND channel = $3 AND job_id = $4
	`

	// Fenced on the token as well as the identifier, for the reason the port
	// documents: the reclaim bound proves a claim is old, not that its holder
	// stopped, so a claimant slow enough to be superseded is still running
	// and would otherwise write over the outcome its successor recorded. The
	// pending predicate closes the same gap in the other direction, against a
	// claimant that somehow still holds the current token after the record
	// ended.
	resolveDeliveryQuery = `
		UPDATE notification_deliveries
		   SET status = $3, attempts = $4, reason = $5, resolved_at = $6
		 WHERE delivery_id = $1 AND claim_token = $2 AND status = $7
	`
)

// DeliveryRepository implements domain.DeliveryRepository against PostgreSQL
// using parameterized queries.
type DeliveryRepository struct {
	db *sql.DB
}

// NewDeliveryRepository wires a DeliveryRepository to an already-open
// database handle.
func NewDeliveryRepository(db *sql.DB) *DeliveryRepository {
	return &DeliveryRepository{db: db}
}

// ClaimDelivery claims the delivery identity names, reporting which of the
// three outcomes occurred. A refusal is reported through the outcome and not
// through the error: it is the expected consequence of at-least-once
// transport rather than a failure.
func (r *DeliveryRepository) ClaimDelivery(ctx context.Context, identity domain.DeliveryIdentity, now, staleBefore time.Time) (domain.Delivery, domain.ClaimOutcome, error) {
	if identity.IsZero() {
		return domain.Delivery{}, 0, domain.ErrDeliveryIdentityIncomplete
	}

	// Both identifiers are minted here rather than by the server, because the
	// port hands the adapter no identifier to use. NewRandom rather than New:
	// New panics when the system's entropy source fails, and a claim that
	// cannot be issued is an error to report, not a process to end.
	deliveryID, err := uuid.NewRandom()
	if err != nil {
		return domain.Delivery{}, 0, fmt.Errorf("notification: generate delivery id: %w", err)
	}
	claimToken, err := uuid.NewRandom()
	if err != nil {
		return domain.Delivery{}, 0, fmt.Errorf("notification: generate claim token: %w", err)
	}

	userID := identity.UserID().String()
	eventType := identity.EventType().String()
	channel := identity.Channel().String()
	jobID := identity.JobID().String()

	var (
		grantedID    string
		grantedToken string
		claimedAt    time.Time
	)
	err = r.db.QueryRowContext(ctx, claimDeliveryQuery,
		userID, eventType, channel, jobID,
		deliveryID.String(), claimToken.String(), domain.DeliveryStatusPending, now, staleBefore,
	).Scan(&grantedID, &grantedToken, &claimedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		outcome, err := r.classifyRefusal(ctx, userID, eventType, channel, jobID)
		if err != nil {
			return domain.Delivery{}, 0, err
		}
		return domain.Delivery{}, outcome, nil
	case err != nil:
		return domain.Delivery{}, 0, fmt.Errorf("notification: claim delivery: %w", err)
	}

	id, err := domain.NewDeliveryID(grantedID)
	if err != nil {
		return domain.Delivery{}, 0, fmt.Errorf("notification: stored delivery id is invalid: %w", err)
	}
	token, err := domain.NewClaimToken(grantedToken)
	if err != nil {
		return domain.Delivery{}, 0, fmt.Errorf("notification: stored claim token is invalid: %w", err)
	}

	delivery, err := domain.NewClaimedDelivery(id, identity, token, claimedAt)
	if err != nil {
		return domain.Delivery{}, 0, fmt.Errorf("notification: build claimed delivery: %w", err)
	}
	return delivery, domain.ClaimGranted, nil
}

// classifyRefusal turns a refused claim into the outcome the caller acts on.
//
// A record that vanished between the two statements is reported as held
// rather than as resolved. The two dispositions are not symmetric: reporting
// held costs one re-run of the claim, which is the correct recovery, while
// reporting resolved acknowledges a message no record accounts for.
func (r *DeliveryRepository) classifyRefusal(ctx context.Context, userID, eventType, channel, jobID string) (domain.ClaimOutcome, error) {
	var status string
	err := r.db.QueryRowContext(ctx, classifyRefusedClaimQuery, userID, eventType, channel, jobID).Scan(&status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.ClaimHeldByAnother, nil
	case err != nil:
		return 0, fmt.Errorf("notification: classify refused claim: %w", err)
	}

	if status == domain.DeliveryStatusPending {
		return domain.ClaimHeldByAnother, nil
	}
	return domain.ClaimAlreadyResolved, nil
}

// ResolveDelivery records how a claimed delivery ended, reporting whether the
// fence let the write through. A false return means this claimant was
// superseded, not that the statement failed.
func (r *DeliveryRepository) ResolveDelivery(ctx context.Context, deliveryID domain.DeliveryID, claimToken domain.ClaimToken, status domain.DeliveryStatus, attempts int, reason string, now time.Time) (bool, error) {
	// Refused rather than written, because the table would otherwise hold a
	// row whose status and resolved_at disagree — the shape
	// domain.RestoreDelivery rejects, so it would be unreadable afterwards.
	if !status.IsResolved() {
		return false, fmt.Errorf("notification: resolve delivery: %q is not a resolved status", status)
	}

	// NULL rather than the empty string for an outcome that carries no
	// reason, so the column means "there was one" rather than needing a
	// second convention on top of it.
	storedReason := sql.NullString{String: reason, Valid: reason != ""}

	result, err := r.db.ExecContext(ctx, resolveDeliveryQuery,
		deliveryID.String(), claimToken.String(), status.String(), attempts, storedReason, now, domain.DeliveryStatusPending)
	if err != nil {
		return false, fmt.Errorf("notification: resolve delivery: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("notification: resolve delivery: %w", err)
	}
	return affected > 0, nil
}
