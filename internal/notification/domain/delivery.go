package domain

import (
	"errors"
	"time"
)

// ErrInvalidDeliveryID is returned when a value fails DeliveryID construction.
var ErrInvalidDeliveryID = errors.New("notification: invalid delivery id")

// ErrInvalidClaimToken is returned when a value fails ClaimToken construction.
var ErrInvalidClaimToken = errors.New("notification: invalid claim token")

// ErrInvalidDeliveryStatus is returned when a value is outside the closed set
// of delivery statuses.
var ErrInvalidDeliveryStatus = errors.New("notification: invalid delivery status")

// ErrDeliveryIdentityIncomplete is returned when a delivery identity is missing
// one of the four values that name it.
var ErrDeliveryIdentityIncomplete = errors.New("notification: delivery identity is incomplete")

// ErrDeliveryAttemptsInvalid is returned when a delivery is built with a
// negative attempt count.
var ErrDeliveryAttemptsInvalid = errors.New("notification: delivery attempts must not be negative")

// ErrDeliveryClaimedAtRequired is returned when a delivery is built with a zero
// claimed-at. The value is what the reclaim bound is measured from, so an
// unset one would make the claim either permanent or instantly stale.
var ErrDeliveryClaimedAtRequired = errors.New("notification: delivery claimed at is required")

// ErrDeliveryResolvedAtMismatch is returned when a delivery's status and its
// resolution timestamp disagree: a resolved status with no timestamp, or a
// pending one carrying it. The two are read by different callers — the
// disposition table branches on the status, the reclaim bound on the
// timestamps — so a row where they disagree would be answered differently
// depending on which one was consulted.
var ErrDeliveryResolvedAtMismatch = errors.New("notification: delivery resolved at does not match the status")

// DeliveryID is the identifier a receiver deduplicates on. It is minted once,
// when a delivery is first claimed, and is preserved across a reclaim — every
// attempt within the budget and every attempt made after a takeover carry the
// same value, because they are all the same logical delivery.
type DeliveryID struct {
	value string
}

// NewDeliveryID wraps an already-minted identifier, enforcing only that it is
// not empty. Minting belongs to infrastructure, which is where a UUID library
// may be imported.
func NewDeliveryID(value string) (DeliveryID, error) {
	if value == "" {
		return DeliveryID{}, ErrInvalidDeliveryID
	}
	return DeliveryID{value: value}, nil
}

// String returns the identifier's canonical representation.
func (id DeliveryID) String() string { return id.value }

// IsZero reports whether the DeliveryID is the unset zero value.
func (id DeliveryID) IsZero() bool { return id.value == "" }

// Equal reports whether two DeliveryIDs name the same delivery.
func (id DeliveryID) Equal(other DeliveryID) bool { return id.value == other.value }

// ClaimToken fences a resolution. Unlike DeliveryID it is reissued on every
// grant, so a claimant superseded by a reclaim cannot write an outcome over
// its successor's: the reclaim bound proves a claim is old, not that the
// process holding it stopped.
type ClaimToken struct {
	value string
}

// NewClaimToken wraps an already-minted token, enforcing only that it is not
// empty.
func NewClaimToken(value string) (ClaimToken, error) {
	if value == "" {
		return ClaimToken{}, ErrInvalidClaimToken
	}
	return ClaimToken{value: value}, nil
}

// String returns the token's canonical representation.
func (t ClaimToken) String() string { return t.value }

// IsZero reports whether the ClaimToken is the unset zero value.
func (t ClaimToken) IsZero() bool { return t.value == "" }

// Equal reports whether two ClaimTokens are the same grant.
func (t ClaimToken) Equal(other ClaimToken) bool { return t.value == other.value }

// The states a delivery record can be in. A claim starts pending; the
// claimant resolves it to exactly one of the other two and it never moves
// again, which is what makes a second claim on a resolved row refusable.
const (
	DeliveryStatusPending   = "pending"
	DeliveryStatusDelivered = "delivered"
	DeliveryStatusFailed    = "failed"
)

// DeliveryStatus is how a delivery ended, or that it has not ended.
type DeliveryStatus struct {
	value string
}

// ParseDeliveryStatus accepts exactly the three recognized statuses.
func ParseDeliveryStatus(raw string) (DeliveryStatus, error) {
	switch raw {
	case DeliveryStatusPending, DeliveryStatusDelivered, DeliveryStatusFailed:
		return DeliveryStatus{value: raw}, nil
	default:
		return DeliveryStatus{}, ErrInvalidDeliveryStatus
	}
}

// String returns the status's canonical representation.
func (s DeliveryStatus) String() string { return s.value }

// IsZero reports whether the DeliveryStatus is the unset zero value.
func (s DeliveryStatus) IsZero() bool { return s.value == "" }

// IsResolved reports whether the delivery has reached an outcome. A resolved
// delivery is never attempted again, however many times its event is
// redelivered.
func (s DeliveryStatus) IsResolved() bool {
	return s.value == DeliveryStatusDelivered || s.value == DeliveryStatusFailed
}

// DeliveryIdentity is what a delivery is, before anything is known about
// whether it happened: one user, one event type, one channel, one job. It is
// the record's whole identity — there is no surrogate key, because the point
// of the record is that this combination is claimed exactly once.
//
// The job identifier is part of it because the same user, event type and
// channel produce one delivery per job; leaving it out would make a second
// job's completion look like a duplicate of the first's.
type DeliveryIdentity struct {
	userID    UserID
	eventType EventType
	channel   Channel
	jobID     JobID
}

// NewDeliveryIdentity requires all four values.
func NewDeliveryIdentity(userID UserID, eventType EventType, channel Channel, jobID JobID) (DeliveryIdentity, error) {
	if userID.IsZero() || eventType.IsZero() || channel.IsZero() || jobID.IsZero() {
		return DeliveryIdentity{}, ErrDeliveryIdentityIncomplete
	}
	return DeliveryIdentity{userID: userID, eventType: eventType, channel: channel, jobID: jobID}, nil
}

// UserID returns the owner the delivery is addressed to.
func (i DeliveryIdentity) UserID() UserID { return i.userID }

// EventType returns the event type being announced.
func (i DeliveryIdentity) EventType() EventType { return i.eventType }

// Channel returns the transport the delivery goes through.
func (i DeliveryIdentity) Channel() Channel { return i.channel }

// JobID returns the job the announced outcome belongs to.
func (i DeliveryIdentity) JobID() JobID { return i.jobID }

// IsZero reports whether the DeliveryIdentity is the unset zero value.
func (i DeliveryIdentity) IsZero() bool { return i.userID.IsZero() }

// Delivery is the durable record of one logical delivery: claimed before any
// request is made, resolved after one has been. It carries no secret and no
// destination, so a record that reaches a log line discloses neither.
type Delivery struct {
	id         DeliveryID
	identity   DeliveryIdentity
	claimToken ClaimToken
	status     DeliveryStatus
	attempts   int
	claimedAt  time.Time
	resolvedAt time.Time
	reason     string
}

// NewClaimedDelivery builds the record a granted claim returns: pending, no
// attempts yet, no outcome.
func NewClaimedDelivery(id DeliveryID, identity DeliveryIdentity, claimToken ClaimToken, claimedAt time.Time) (Delivery, error) {
	pending, err := ParseDeliveryStatus(DeliveryStatusPending)
	if err != nil {
		return Delivery{}, err
	}
	return RestoreDelivery(id, identity, claimToken, pending, 0, claimedAt, time.Time{}, "")
}

// RestoreDelivery reconstructs a record from stored values, re-checking every
// invariant so a corrupted row cannot enter the domain as a valid one. It
// mirrors how RestoreNotificationPreference separates restoration from
// creation.
func RestoreDelivery(id DeliveryID, identity DeliveryIdentity, claimToken ClaimToken, status DeliveryStatus, attempts int, claimedAt, resolvedAt time.Time, reason string) (Delivery, error) {
	if id.IsZero() {
		return Delivery{}, ErrInvalidDeliveryID
	}
	if identity.IsZero() {
		return Delivery{}, ErrDeliveryIdentityIncomplete
	}
	if claimToken.IsZero() {
		return Delivery{}, ErrInvalidClaimToken
	}
	if status.IsZero() {
		return Delivery{}, ErrInvalidDeliveryStatus
	}
	if attempts < 0 {
		return Delivery{}, ErrDeliveryAttemptsInvalid
	}
	if claimedAt.IsZero() {
		return Delivery{}, ErrDeliveryClaimedAtRequired
	}
	if status.IsResolved() != !resolvedAt.IsZero() {
		return Delivery{}, ErrDeliveryResolvedAtMismatch
	}
	return Delivery{
		id:         id,
		identity:   identity,
		claimToken: claimToken,
		status:     status,
		attempts:   attempts,
		claimedAt:  claimedAt,
		resolvedAt: resolvedAt,
		reason:     reason,
	}, nil
}

// ID returns the identifier every attempt for this delivery carries.
func (d Delivery) ID() DeliveryID { return d.id }

// Identity returns the four values the record is keyed on.
func (d Delivery) Identity() DeliveryIdentity { return d.identity }

// ClaimToken returns the token that fences this claimant's resolution.
func (d Delivery) ClaimToken() ClaimToken { return d.claimToken }

// Status returns whether and how the delivery ended.
func (d Delivery) Status() DeliveryStatus { return d.status }

// Attempts returns how many requests have been made for this delivery.
func (d Delivery) Attempts() int { return d.attempts }

// ClaimedAt returns when the current claim was granted. The reclaim bound is
// measured from it.
func (d Delivery) ClaimedAt() time.Time { return d.claimedAt }

// ResolvedAt returns when the delivery ended, and whether it has.
func (d Delivery) ResolvedAt() (time.Time, bool) {
	return d.resolvedAt, !d.resolvedAt.IsZero()
}

// Reason returns the last observed reason. It is free text this system wrote
// about its own attempt — a classified failure of ours, never a third party's
// response body and never a transport error's own text, which renders the
// request URL and with it any credential the destination carries in its
// query string.
func (d Delivery) Reason() string { return d.reason }

// IsZero reports whether the Delivery is the unset zero value.
func (d Delivery) IsZero() bool { return d.id.IsZero() }
