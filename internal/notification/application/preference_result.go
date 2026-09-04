package application

import (
	"time"

	"video-processor/internal/notification/domain"
)

// PreferenceResult describes one stored preference to the layer above.
// Both use cases return it, because the write route's response is specified
// as one element of the read route's collection — one type is what keeps
// that true without a handler restating it.
//
// It carries HasSecret and no secret field, which is the non-disclosure
// guarantee made structural: a handler cannot serialize a value the type
// does not hold, and no read path even loads one.
//
// The owner is absent for the same reason it is never accepted from a
// request: every result belongs to the authenticated caller, so echoing it
// would describe the token rather than the preference.
type PreferenceResult struct {
	EventType   string
	Channel     string
	Enabled     bool
	Destination string
	HasSecret   bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// newPreferenceResult renders a stored preference for the layer above.
func newPreferenceResult(view domain.PreferenceView) PreferenceResult {
	return PreferenceResult{
		EventType:   view.EventType.String(),
		Channel:     view.Channel.String(),
		Enabled:     view.Enabled,
		Destination: view.Destination.String(),
		HasSecret:   view.HasSecret,
		CreatedAt:   view.CreatedAt,
		UpdatedAt:   view.UpdatedAt,
	}
}
