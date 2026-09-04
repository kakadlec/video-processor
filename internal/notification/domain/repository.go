package domain

import (
	"context"
	"errors"
	"time"
)

// ErrSecretRequired reports that a preference could not be created because
// the request carried no signing secret. It is returned only on the create
// path: an update omitting a secret preserves the stored one and succeeds.
//
// It is a distinct sentinel from ErrInvalidSecret, and the distinction is
// the point. ErrInvalidSecret means a value was supplied and rejected;
// ErrSecretRequired means none was supplied and none was already stored.
// Callers separate them with errors.Is, which only works while they stay
// disjoint — both map to 400, but only one of them can be answered by
// resending the same body.
var ErrSecretRequired = errors.New("notification: signing secret is required to create a preference")

// PreferenceRepository is the persistence port for the
// NotificationPreference aggregate.
type PreferenceRepository interface {
	// Set stores the preference the intent names, creating it when none
	// exists for its triple and replacing the mutable fields when one does,
	// and returns what is now stored. It affects no other triple. now
	// stamps updated_at, and created_at as well on the create path; it is a
	// parameter rather than a call to time.Now inside the adapter so the
	// application layer's Clock stays the single source of time.
	//
	// Two guarantees belong to this contract rather than to any
	// implementation of it, because the application layer depends on both.
	//
	// First, Set is atomic on either branch and reads no row. It does not
	// look up whether a preference exists before writing: the branch that
	// decides its behaviour is a property of the intent — whether the
	// caller sent a secret — and is knowable before any statement runs. A
	// caller must therefore not pre-read to decide create-from-update; there
	// is no window in which that read stays true.
	//
	// Second, the create-with-no-secret rule is enforced here, by whether
	// the write affected a row, and reported as ErrSecretRequired. An intent
	// carrying no secret can only ever update, so affecting nothing means
	// there was no preference to update and none may be created without a
	// secret to sign it with. Nothing is stored on that path.
	Set(ctx context.Context, intent PreferenceIntent, now time.Time) (PreferenceView, error)

	// ListByUser returns every preference owned by userID, ordered by event
	// type then channel so the response is deterministic rather than
	// dependent on physical row order. A user who has registered nothing
	// yields an empty slice and no error: an absent preference means not
	// subscribed, which is an ordinary state and not a lookup failure.
	//
	// No implementation may load the secret on this path. Reads project
	// whether one is set, which is what makes PreferenceView's missing
	// secret field a guarantee rather than a convention.
	ListByUser(ctx context.Context, userID UserID) ([]PreferenceView, error)
}
