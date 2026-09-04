package domain_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"video-processor/internal/notification/domain"
)

type preferenceParts struct {
	userID      domain.UserID
	eventType   domain.EventType
	channel     domain.Channel
	destination domain.Destination
	secret      domain.Secret
	at          time.Time
}

func validParts(t *testing.T) preferenceParts {
	t.Helper()

	userID, err := domain.NewUserID("user-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	eventType, err := domain.ParseEventType(domain.EventTypeVideoJobCompleted)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	channel, err := domain.ParseChannel(domain.ChannelWebhook)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	destination, err := domain.NewDestination("https://example.test/hooks/1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	secret, err := domain.NewSecret(validSecret)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	return preferenceParts{
		userID:      userID,
		eventType:   eventType,
		channel:     channel,
		destination: destination,
		secret:      secret,
		at:          time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC),
	}
}

func TestNewPreferenceIntent(t *testing.T) {
	parts := validParts(t)

	tests := []struct {
		name        string
		userID      domain.UserID
		eventType   domain.EventType
		channel     domain.Channel
		destination domain.Destination
		secret      *domain.Secret
		wantErr     error
	}{
		{"with a secret", parts.userID, parts.eventType, parts.channel, parts.destination, &parts.secret, nil},
		{"without a secret", parts.userID, parts.eventType, parts.channel, parts.destination, nil, nil},
		{"missing user", domain.UserID{}, parts.eventType, parts.channel, parts.destination, nil, domain.ErrPreferenceUserIDRequired},
		{"missing event type", parts.userID, domain.EventType{}, parts.channel, parts.destination, nil, domain.ErrPreferenceEventTypeRequired},
		{"missing channel", parts.userID, parts.eventType, domain.Channel{}, parts.destination, nil, domain.ErrPreferenceChannelRequired},
		{"missing destination", parts.userID, parts.eventType, parts.channel, domain.Destination{}, nil, domain.ErrPreferenceDestinationRequired},
		{"present but zero secret", parts.userID, parts.eventType, parts.channel, parts.destination, &domain.Secret{}, domain.ErrInvalidSecret},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent, err := domain.NewPreferenceIntent(tt.userID, tt.eventType, tt.channel, true, tt.destination, tt.secret)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("NewPreferenceIntent error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}

			secret, ok := intent.Secret()
			if ok != (tt.secret != nil) {
				t.Fatalf("Secret() reported present = %v, want %v", ok, tt.secret != nil)
			}
			if ok && secret.Reveal() != validSecret {
				t.Fatal("Secret() did not return the submitted secret")
			}
			if !intent.UserID().Equal(tt.userID) {
				t.Fatal("UserID() did not round-trip")
			}
			if intent.EventType() != tt.eventType || intent.Channel() != tt.channel || intent.Destination() != tt.destination {
				t.Fatal("the triple or destination did not round-trip")
			}
			if !intent.Enabled() {
				t.Fatal("Enabled() did not round-trip")
			}
		})
	}
}

// TestNewPreferenceIntent_DoesNotRequireASecret pins 1.6a: whether a create
// needs a secret depends on a row this constructor cannot see, so demanding
// one here would reject every legitimate update. The repository enforces it.
func TestNewPreferenceIntent_DoesNotRequireASecret(t *testing.T) {
	parts := validParts(t)

	intent, err := domain.NewPreferenceIntent(parts.userID, parts.eventType, parts.channel, false, parts.destination, nil)
	if err != nil {
		t.Fatalf("an intent omitting a secret must be constructible, got %v", err)
	}
	if _, ok := intent.Secret(); ok {
		t.Fatal("an omitted secret must report absent, not empty")
	}
}

func TestNewNotificationPreference(t *testing.T) {
	parts := validParts(t)

	preference, err := domain.NewNotificationPreference(parts.userID, parts.eventType, parts.channel, true, parts.destination, parts.secret, parts.at)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !preference.CreatedAt().Equal(parts.at) || !preference.UpdatedAt().Equal(parts.at) {
		t.Fatal("a new preference should stamp both timestamps from createdAt")
	}
	if preference.Secret().Reveal() != validSecret {
		t.Fatal("Secret() did not round-trip")
	}
	if !preference.Enabled() {
		t.Fatal("Enabled() did not round-trip")
	}
}

func TestRestoreNotificationPreference(t *testing.T) {
	parts := validParts(t)
	updated := parts.at.Add(time.Hour)

	tests := []struct {
		name        string
		userID      domain.UserID
		eventType   domain.EventType
		channel     domain.Channel
		destination domain.Destination
		secret      domain.Secret
		createdAt   time.Time
		updatedAt   time.Time
		wantErr     error
	}{
		{"complete row", parts.userID, parts.eventType, parts.channel, parts.destination, parts.secret, parts.at, updated, nil},
		{"missing user", domain.UserID{}, parts.eventType, parts.channel, parts.destination, parts.secret, parts.at, updated, domain.ErrPreferenceUserIDRequired},
		{"missing event type", parts.userID, domain.EventType{}, parts.channel, parts.destination, parts.secret, parts.at, updated, domain.ErrPreferenceEventTypeRequired},
		{"missing channel", parts.userID, parts.eventType, domain.Channel{}, parts.destination, parts.secret, parts.at, updated, domain.ErrPreferenceChannelRequired},
		{"missing destination", parts.userID, parts.eventType, parts.channel, domain.Destination{}, parts.secret, parts.at, updated, domain.ErrPreferenceDestinationRequired},
		// A stored preference always carries a secret; that is the whole
		// difference between it and a write intent.
		{"missing secret", parts.userID, parts.eventType, parts.channel, parts.destination, domain.Secret{}, parts.at, updated, domain.ErrInvalidSecret},
		{"missing created at", parts.userID, parts.eventType, parts.channel, parts.destination, parts.secret, time.Time{}, updated, domain.ErrPreferenceTimestampsRequired},
		{"missing updated at", parts.userID, parts.eventType, parts.channel, parts.destination, parts.secret, parts.at, time.Time{}, domain.ErrPreferenceTimestampsRequired},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preference, err := domain.RestoreNotificationPreference(tt.userID, tt.eventType, tt.channel, false, tt.destination, tt.secret, tt.createdAt, tt.updatedAt)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("RestoreNotificationPreference error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				if preference != nil {
					t.Fatal("a rejected restore must not return an aggregate")
				}
				return
			}
			if !preference.UpdatedAt().Equal(updated) {
				t.Fatal("UpdatedAt() did not round-trip")
			}
		})
	}
}

// TestNotificationPreference_IsNeverRenderedWithItsSecret covers the leak
// Secret's own redaction cannot close: fmt reaches an unexported field
// through reflection and cannot call methods on what it finds there, so the
// aggregate has to redact itself.
func TestNotificationPreference_IsNeverRenderedWithItsSecret(t *testing.T) {
	parts := validParts(t)

	preference, err := domain.NewNotificationPreference(parts.userID, parts.eventType, parts.channel, true, parts.destination, parts.secret, parts.at)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d", "%t", "%f", "%p"} {
		t.Run(verb, func(t *testing.T) {
			for _, rendered := range []string{
				fmt.Sprintf(verb, preference),
				fmt.Sprintf(verb, *preference),
			} {
				if strings.Contains(rendered, validSecret) {
					t.Fatalf("fmt %s rendered the secret: %s", verb, rendered)
				}
				// %p is an address by construction: fmt answers it before
				// consulting any rendering hook, so it can carry neither the
				// secret nor the triple.
				if verb == "%p" {
					continue
				}
				if !strings.Contains(rendered, parts.userID.String()) {
					t.Fatalf("fmt %s dropped the identifying triple: %s", verb, rendered)
				}
			}
		})
	}
}

// TestPreferenceIntent_IsNeverRenderedWithItsSecret covers the write intent,
// which is the shape a handler actually holds and so the one most likely to
// reach a log line.
func TestPreferenceIntent_IsNeverRenderedWithItsSecret(t *testing.T) {
	parts := validParts(t)

	intent, err := domain.NewPreferenceIntent(parts.userID, parts.eventType, parts.channel, true, parts.destination, &parts.secret)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d", "%t", "%f", "%p"} {
		t.Run(verb, func(t *testing.T) {
			for _, rendered := range []string{
				fmt.Sprintf(verb, intent),
				fmt.Sprintf(verb, &intent),
			} {
				if strings.Contains(rendered, validSecret) {
					t.Fatalf("fmt %s rendered the secret: %s", verb, rendered)
				}
			}
		})
	}
}

func TestPreferenceView_CarriesNoSecret(t *testing.T) {
	parts := validParts(t)

	view := domain.PreferenceView{
		UserID:      parts.userID,
		EventType:   parts.eventType,
		Channel:     parts.channel,
		Enabled:     true,
		Destination: parts.destination,
		HasSecret:   true,
		CreatedAt:   parts.at,
		UpdatedAt:   parts.at,
	}

	if rendered := fmt.Sprintf("%+v", view); strings.Contains(rendered, validSecret) {
		t.Fatalf("a read view rendered a secret: %s", rendered)
	}
	if !view.HasSecret {
		t.Fatal("HasSecret did not round-trip")
	}
}
