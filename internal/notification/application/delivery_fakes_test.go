package application_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"video-processor/internal/notification/domain"
)

// fakeDeliverablePreferences is a domain.PreferenceRepository whose only
// implemented method is the one the delivery use case calls.
//
// Set and ListByUser panic rather than returning zero values: this fake
// exists to answer one question, and a silent zero would let a future change
// route a write through it and still see a green test.
type fakeDeliverablePreferences struct {
	preferences []*domain.NotificationPreference
	err         error

	calls         int
	lastUserID    domain.UserID
	lastEventType domain.EventType
}

func (r *fakeDeliverablePreferences) Set(context.Context, domain.PreferenceIntent, time.Time) (domain.PreferenceView, error) {
	panic("the delivery use case must not write preferences")
}

func (r *fakeDeliverablePreferences) ListByUser(context.Context, domain.UserID) ([]domain.PreferenceView, error) {
	panic("the delivery use case must not read the projection that hides the secret")
}

func (r *fakeDeliverablePreferences) FindDeliverable(_ context.Context, userID domain.UserID, eventType domain.EventType) ([]*domain.NotificationPreference, error) {
	r.calls++
	r.lastUserID = userID
	r.lastEventType = eventType
	if r.err != nil {
		return nil, r.err
	}
	return r.preferences, nil
}

// resolveCall records one attempt to write an outcome.
type resolveCall struct {
	deliveryID domain.DeliveryID
	claimToken domain.ClaimToken
	status     domain.DeliveryStatus
	attempts   int
	reason     string
	now        time.Time
}

// fakeDeliveryRepository is a scripted domain.DeliveryRepository. It grants
// or refuses a claim as configured and records every resolve, so a test can
// assert both the outcome written and how many times it was attempted.
type fakeDeliveryRepository struct {
	mu sync.Mutex

	outcome  domain.ClaimOutcome
	claimErr error
	delivery domain.Delivery

	claimCalls      int
	lastNow         time.Time
	lastStaleBefore time.Time

	resolveErr     error
	resolveApplied bool
	resolves       []resolveCall
}

func (r *fakeDeliveryRepository) ClaimDelivery(_ context.Context, _ domain.DeliveryIdentity, now, staleBefore time.Time) (domain.Delivery, domain.ClaimOutcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.claimCalls++
	r.lastNow = now
	r.lastStaleBefore = staleBefore
	if r.claimErr != nil {
		return domain.Delivery{}, 0, r.claimErr
	}
	if r.outcome != domain.ClaimGranted {
		return domain.Delivery{}, r.outcome, nil
	}
	return r.delivery, domain.ClaimGranted, nil
}

func (r *fakeDeliveryRepository) ResolveDelivery(_ context.Context, deliveryID domain.DeliveryID, claimToken domain.ClaimToken, status domain.DeliveryStatus, attempts int, reason string, now time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.resolves = append(r.resolves, resolveCall{
		deliveryID: deliveryID,
		claimToken: claimToken,
		status:     status,
		attempts:   attempts,
		reason:     reason,
		now:        now,
	})
	if r.resolveErr != nil {
		return false, r.resolveErr
	}
	return r.resolveApplied, nil
}

func (r *fakeDeliveryRepository) resolveCalls() []resolveCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]resolveCall(nil), r.resolves...)
}

// fakeDeliverer answers each attempt from a script. The last entry repeats,
// so a two-entry script covers "fails, then succeeds" and a one-entry script
// covers "always fails" whatever the configured attempt count is.
type fakeDeliverer struct {
	mu sync.Mutex

	errs        []error
	calls       int
	deliveryIDs []string
	preferences []*domain.NotificationPreference
	events      []domain.TerminalEvent
}

func (d *fakeDeliverer) Deliver(_ context.Context, preference *domain.NotificationPreference, event domain.TerminalEvent, deliveryID domain.DeliveryID) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	index := d.calls
	d.calls++
	d.deliveryIDs = append(d.deliveryIDs, deliveryID.String())
	d.preferences = append(d.preferences, preference)
	d.events = append(d.events, event)

	if len(d.errs) == 0 {
		return nil
	}
	if index >= len(d.errs) {
		index = len(d.errs) - 1
	}
	return d.errs[index]
}

func (d *fakeDeliverer) attempts() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func (d *fakeDeliverer) announcedDeliveryIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.deliveryIDs...)
}

// Domain construction helpers, kept here so the table below reads as the
// cases it enumerates rather than as error handling.

func mustDeliveryUserID(t *testing.T, value string) domain.UserID {
	t.Helper()
	id, err := domain.NewUserID(value)
	if err != nil {
		t.Fatalf("unexpected error building a user id: %v", err)
	}
	return id
}

func mustDeliveryJobID(t *testing.T, value string) domain.JobID {
	t.Helper()
	id, err := domain.NewJobID(value)
	if err != nil {
		t.Fatalf("unexpected error building a job id: %v", err)
	}
	return id
}

func mustEventType(t *testing.T, value string) domain.EventType {
	t.Helper()
	eventType, err := domain.ParseEventType(value)
	if err != nil {
		t.Fatalf("unexpected error parsing an event type: %v", err)
	}
	return eventType
}

func mustWebhookChannel(t *testing.T) domain.Channel {
	t.Helper()
	channel, err := domain.ParseChannel(domain.ChannelWebhook)
	if err != nil {
		t.Fatalf("unexpected error parsing the webhook channel: %v", err)
	}
	return channel
}

func mustSecret(t *testing.T, raw string) domain.Secret {
	t.Helper()
	value, err := domain.NewSecret(raw)
	if err != nil {
		t.Fatalf("unexpected error building a secret: %v", err)
	}
	return value
}

func mustPreference(t *testing.T, destinationURL string, createdAt time.Time) *domain.NotificationPreference {
	t.Helper()
	return mustPreferenceWithSecret(t, destinationURL, deliveryTestSecret, createdAt)
}

func mustPreferenceWithSecret(t *testing.T, destinationURL, rawSecret string, createdAt time.Time) *domain.NotificationPreference {
	t.Helper()

	destination, err := domain.NewDestination(destinationURL)
	if err != nil {
		t.Fatalf("unexpected error building a destination: %v", err)
	}
	preference, err := domain.NewNotificationPreference(
		mustDeliveryUserID(t, deliveryTestUser),
		mustEventType(t, domain.EventTypeVideoJobCompleted),
		mustWebhookChannel(t),
		true,
		destination,
		mustSecret(t, rawSecret),
		createdAt,
	)
	if err != nil {
		t.Fatalf("unexpected error building a preference: %v", err)
	}
	return preference
}

func mustCompletedEvent(t *testing.T, occurredAt time.Time) domain.TerminalEvent {
	t.Helper()
	event, err := domain.NewCompletedEvent(
		mustDeliveryJobID(t, deliveryTestJob), mustDeliveryUserID(t, deliveryTestUser), occurredAt, 42, "frames_"+deliveryTestJob+".zip")
	if err != nil {
		t.Fatalf("unexpected error building a completion event: %v", err)
	}
	return event
}

func mustGrantedDelivery(t *testing.T) domain.Delivery {
	t.Helper()

	id, err := domain.NewDeliveryID(deliveryTestID)
	if err != nil {
		t.Fatalf("unexpected error building a delivery id: %v", err)
	}
	token, err := domain.NewClaimToken(deliveryTestToken)
	if err != nil {
		t.Fatalf("unexpected error building a claim token: %v", err)
	}
	identity, err := domain.NewDeliveryIdentity(
		mustDeliveryUserID(t, deliveryTestUser),
		mustEventType(t, domain.EventTypeVideoJobCompleted),
		mustWebhookChannel(t),
		mustDeliveryJobID(t, deliveryTestJob),
	)
	if err != nil {
		t.Fatalf("unexpected error building a delivery identity: %v", err)
	}
	delivery, err := domain.NewClaimedDelivery(id, identity, token, deliveryTestNow)
	if err != nil {
		t.Fatalf("unexpected error building a claimed delivery: %v", err)
	}
	return delivery
}
