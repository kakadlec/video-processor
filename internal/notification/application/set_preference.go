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
}

// NewSetPreference wires the SetPreference use case to its ports.
func NewSetPreference(preferences domain.PreferenceRepository, clock Clock) *SetPreference {
	return &SetPreference{preferences: preferences, clock: clock}
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
