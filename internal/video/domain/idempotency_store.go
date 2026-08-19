package domain

import "context"

// IdempotencyStore is the port through which POST /upload's idempotency
// mechanism reserves, finalizes, looks up, and clears an IdempotencyKey's
// mapping to a VideoJobID. The domain depends on this interface;
// infrastructure supplies the concrete implementation (Redis-backed).
//
// Reserve/Finalize/Clear are token-based: Reserve returns a token that
// proves ownership of the reservation it just made, and Finalize/Clear only
// take effect if the caller presents that same token — so a request whose
// reservation has already expired and been reclaimed by someone else can
// never overwrite or delete the newer owner's mapping.
type IdempotencyStore interface {
	// Reserve atomically claims key for a new, in-flight submission. It
	// returns false (with no error) if key is already reserved or
	// finalized by someone else; otherwise it returns true and a token
	// that must be presented to Finalize or Clear to affect this
	// reservation.
	Reserve(ctx context.Context, key IdempotencyKey) (token string, reserved bool, err error)

	// Finalize replaces a reservation held under token with the given
	// jobID, extending the key's lifetime to the full idempotency window.
	// It returns false (with no error) if token no longer matches key's
	// current owner — the caller should treat this as a no-op, not a
	// failure.
	Finalize(ctx context.Context, key IdempotencyKey, token string, jobID VideoJobID) (finalized bool, err error)

	// Lookup returns the VideoJobID a key has been finalized to, if any.
	// found is false both when key is absent and when it still holds an
	// in-flight reservation (not yet a real job to return).
	Lookup(ctx context.Context, key IdempotencyKey) (jobID VideoJobID, found bool, err error)

	// Clear removes key's reservation or finalized mapping, but only if
	// token matches its current owner. It returns false (with no error)
	// if token no longer matches — the caller should treat this as a
	// no-op, not a failure.
	Clear(ctx context.Context, key IdempotencyKey, token string) (cleared bool, err error)
}
