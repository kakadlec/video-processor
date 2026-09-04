package application_test

import (
	"context"
	"sort"
	"sync"
	"time"

	"video-processor/internal/notification/domain"
)

// preferenceKey is the triple that identifies a preference, flattened into a
// comparable map key.
type preferenceKey struct {
	userID    string
	eventType string
	channel   string
}

// fakePreferenceRepository is an in-memory domain.PreferenceRepository used
// to unit test use cases without a database.
//
// It reproduces the port's contract closely enough to exercise the use
// cases and no further: it records what it was handed, because what the use
// case passes to the port is the only thing these tests can honestly assert.
// The create-with-no-secret rule really lives in a row count against
// PostgreSQL, and the adapter suite is where it is tested; the mirror of it
// here exists so the use case has something to surface, not as evidence the
// rule holds.
type fakePreferenceRepository struct {
	mu        sync.Mutex
	stored    map[preferenceKey]domain.PreferenceView
	setErr    error
	listErr   error
	setCalls  int
	listCalls int

	lastIntent    domain.PreferenceIntent
	lastIntentSet bool
	lastNow       time.Time
	lastListedFor domain.UserID
}

func newFakePreferenceRepository() *fakePreferenceRepository {
	return &fakePreferenceRepository{stored: make(map[preferenceKey]domain.PreferenceView)}
}

func (r *fakePreferenceRepository) Set(_ context.Context, intent domain.PreferenceIntent, now time.Time) (domain.PreferenceView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.setCalls++
	r.lastIntent = intent
	r.lastIntentSet = true
	r.lastNow = now

	if r.setErr != nil {
		return domain.PreferenceView{}, r.setErr
	}

	key := preferenceKey{
		userID:    intent.UserID().String(),
		eventType: intent.EventType().String(),
		channel:   intent.Channel().String(),
	}
	_, submitted := intent.Secret()
	existing, exists := r.stored[key]
	if !submitted && !exists {
		return domain.PreferenceView{}, domain.ErrSecretRequired
	}

	view := domain.PreferenceView{
		UserID:      intent.UserID(),
		EventType:   intent.EventType(),
		Channel:     intent.Channel(),
		Enabled:     intent.Enabled(),
		Destination: intent.Destination(),
		HasSecret:   submitted || existing.HasSecret,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if exists {
		view.CreatedAt = existing.CreatedAt
	}
	r.stored[key] = view
	return view, nil
}

func (r *fakePreferenceRepository) ListByUser(_ context.Context, userID domain.UserID) ([]domain.PreferenceView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.listCalls++
	r.lastListedFor = userID

	if r.listErr != nil {
		return nil, r.listErr
	}

	views := make([]domain.PreferenceView, 0, len(r.stored))
	for key, view := range r.stored {
		if key.userID == userID.String() {
			views = append(views, view)
		}
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].EventType.String() != views[j].EventType.String() {
			return views[i].EventType.String() < views[j].EventType.String()
		}
		return views[i].Channel.String() < views[j].Channel.String()
	})
	return views, nil
}

// fakeClock always returns the same pre-set time, for deterministic assertions.
type fakeClock struct {
	now time.Time
}

func (c fakeClock) Now() time.Time { return c.now }
