package domain

import (
	"context"
	"time"
)

// ClaimOutcome reports what happened when a delivery was claimed.
//
// It is three-valued rather than a boolean, and collapsing the two refusals
// into one false is the bug it exists to prevent. The two call for opposite
// dispositions: a delivery already resolved is finished, so its message is
// acknowledged, while a claim another consumer still holds must leave the
// message unhandled. A crashed claimant's message is redelivered within
// seconds of its channel closing — far inside the reclaim bound — so the
// redelivery meets a fresh pending row and is refused. Acknowledging there
// would strand that row, which nothing else will ever meet, and drop the
// notification silently.
//
// The zero value is deliberately not a valid outcome, so a repository that
// forgets to set one cannot be read as a grant.
type ClaimOutcome int

const (
	// ClaimGranted means this caller owns the delivery. The returned Delivery
	// carries the stable DeliveryID and the freshly issued ClaimToken.
	ClaimGranted ClaimOutcome = iota + 1

	// ClaimAlreadyResolved means the delivery has ended. Nothing is left to
	// do and no request may be made.
	ClaimAlreadyResolved

	// ClaimHeldByAnother means a claim is live and not yet past the reclaim
	// bound. The holder may be mid-request or may have died a second after
	// claiming; this caller cannot tell, and must not assume the former.
	ClaimHeldByAnother
)

// String renders the outcome for logs.
func (o ClaimOutcome) String() string {
	switch o {
	case ClaimGranted:
		return "granted"
	case ClaimAlreadyResolved:
		return "already_resolved"
	case ClaimHeldByAnother:
		return "held_by_another"
	default:
		return "unknown"
	}
}

// DeliveryRepository is the persistence port for the delivery record. It is
// what discharges the deduplication obligation the terminal-event capability
// assigns to whatever consumes its queue.
type DeliveryRepository interface {
	// ClaimDelivery claims the delivery that identity names, returning the
	// record and which of the three outcomes occurred. now stamps the claim
	// and staleBefore is the reclaim boundary: a pending claim older than it
	// may be taken over. Both are parameters rather than calls to time.Now
	// inside the adapter, so the application layer's Clock stays the single
	// source of time.
	//
	// Two guarantees belong to this contract rather than to any
	// implementation of it.
	//
	// First, granting is one atomic statement that reads no row. It must not
	// be a lookup followed by an insert: the guarantee has to hold across
	// notifier processes, and two of them both reading "not delivered" and
	// both proceeding is exactly the race the record exists to close. A
	// caller must therefore not pre-read to decide whether to claim.
	//
	// Second, a reclaim preserves the DeliveryID and reissues the
	// ClaimToken. The identifier is preserved because a receiver may already
	// have deduplicated on it, so a takeover is the same logical delivery
	// rather than a new one. The token is reissued because it is what fences
	// the prior holder out of resolving: staleBefore proves a claim is old,
	// not that the process holding it stopped, and a claimant blocked on a
	// slow endpoint for longer than the bound is still running and would
	// otherwise write its outcome over its successor's.
	//
	// A refused claim is not a failure. It is the expected consequence of
	// at-least-once transport, and is reported through the outcome rather
	// than through the error.
	ClaimDelivery(ctx context.Context, identity DeliveryIdentity, now, staleBefore time.Time) (Delivery, ClaimOutcome, error)

	// ResolveDelivery records how a claimed delivery ended, reporting whether
	// the write was applied.
	//
	// It is fenced on deliveryID, claimToken and the delivery still being
	// pending, and the fence is not optional for the reason ClaimDelivery
	// documents. A false return therefore does not mean the write failed: it
	// means this claimant was superseded and a successor owns the outcome.
	// The correct response is to log and acknowledge, never to retry — a
	// retry can only be refused again, and the successor's record is the
	// right one.
	ResolveDelivery(ctx context.Context, deliveryID DeliveryID, claimToken ClaimToken, status DeliveryStatus, attempts int, reason string, now time.Time) (bool, error)
}
