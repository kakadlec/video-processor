package application

import (
	"context"

	"video-processor/internal/notification/domain"
)

// ListPreferences returns every preference one user has registered.
//
// The owner is a parameter rather than anything ambient, so the only way to
// list a user's preferences is to name them — the composition root passes
// the authenticated subject and nothing else can reach this use case.
//
// It has no Clock: nothing here is stamped.
type ListPreferences struct {
	preferences domain.PreferenceRepository
}

// NewListPreferences wires the ListPreferences use case to its port.
func NewListPreferences(preferences domain.PreferenceRepository) *ListPreferences {
	return &ListPreferences{preferences: preferences}
}

// Execute runs the preference listing use case. A user who has registered
// nothing yields an empty collection and no error: an absent preference
// means not subscribed, which is an ordinary state rather than a lookup
// failure. The repository's ordering is preserved as it arrives, so the
// route stays deterministic across the two layers.
func (uc *ListPreferences) Execute(ctx context.Context, rawUserID string) ([]PreferenceResult, error) {
	userID, err := domain.NewUserID(rawUserID)
	if err != nil {
		return nil, err
	}

	views, err := uc.preferences.ListByUser(ctx, userID)
	if err != nil {
		return nil, err
	}

	results := make([]PreferenceResult, len(views))
	for i, view := range views {
		results[i] = newPreferenceResult(view)
	}
	return results, nil
}
