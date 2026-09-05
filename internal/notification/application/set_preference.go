package application

import (
	"context"

	"video-processor/internal/notification/domain"
)

// SetPreferenceInput carries the caller-supplied preference fields.
//
// Secret is a pointer because omitting it and sending it empty are
// different requests: an omission preserves the stored secret, while an
// explicit empty value is invalid. A plain string collapses the two before
// the domain can tell them apart, and the preserve-on-omission rule would
// then be unimplementable.
//
// UserID is the authenticated caller, supplied by the composition root and
// never read from a request body.
type SetPreferenceInput struct {
	UserID      string
	EventType   string
	Channel     string
	Enabled     bool
	Destination string
	Secret      *string
}

// SetPreference registers or replaces one user's preference for one event
// type on one channel.
//
// It performs no read before writing. Whether the write creates or updates
// is decided by the repository in a single atomic statement, and the
// statement it picks follows from whether the caller submitted a secret —
// which is knowable here, without consulting any row. Refusing a create
// that carries no secret is therefore the repository's answer, surfaced
// unchanged as domain.ErrSecretRequired, rather than a check this use case
// could make honestly.
type SetPreference struct {
	preferences domain.PreferenceRepository
	clock       Clock
	policy      domain.DestinationPolicy
}

// NewSetPreference wires the SetPreference use case to its ports.
//
// The policy is a value rather than a port because its zero value is the
// restrictive posture: a composition root that forgets to pass one refuses
// insecure destinations instead of accepting everything.
func NewSetPreference(preferences domain.PreferenceRepository, clock Clock, policy domain.DestinationPolicy) *SetPreference {
	return &SetPreference{preferences: preferences, clock: clock, policy: policy}
}

// Execute runs the preference write use case.
func (uc *SetPreference) Execute(ctx context.Context, input SetPreferenceInput) (PreferenceResult, error) {
	userID, err := domain.NewUserID(input.UserID)
	if err != nil {
		return PreferenceResult{}, err
	}

	eventType, err := domain.ParseEventType(input.EventType)
	if err != nil {
		return PreferenceResult{}, err
	}

	channel, err := domain.ParseChannel(input.Channel)
	if err != nil {
		return PreferenceResult{}, err
	}

	destination, err := domain.NewDestination(input.Destination)
	if err != nil {
		return PreferenceResult{}, err
	}

	// Judged before anything is written, and judged whatever Enabled says: a
	// disabled preference still stores a destination, and enabling it later
	// goes through no validation of its own. The dial-time check in the
	// delivery client is not made redundant by this one — a name resolves
	// somewhere else later, and a policy tightened after the row was stored
	// never revisits it — but without this one a caller can register a
	// destination that is refused at every delivery, which from the outside
	// is indistinguishable from one that simply never fires.
	if err := uc.policy.CheckDestination(destination); err != nil {
		return PreferenceResult{}, err
	}

	// Parsed only when submitted: NewSecret rejects the empty string, so an
	// unconditional parse would turn every legitimate omission into a
	// validation failure.
	var secret *domain.Secret
	if input.Secret != nil {
		parsed, parseErr := domain.NewSecret(*input.Secret)
		if parseErr != nil {
			return PreferenceResult{}, parseErr
		}
		secret = &parsed
	}

	intent, err := domain.NewPreferenceIntent(userID, eventType, channel, input.Enabled, destination, secret)
	if err != nil {
		return PreferenceResult{}, err
	}

	// The clock is read once and handed to the port, which stamps both
	// columns. Time enters the system here rather than in the adapter, so
	// created_at and updated_at cannot disagree with each other across a
	// statement boundary.
	view, err := uc.preferences.Set(ctx, intent, uc.clock.Now())
	if err != nil {
		return PreferenceResult{}, err
	}

	return newPreferenceResult(view), nil
}
