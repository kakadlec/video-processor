package domain_test

import (
	"errors"
	"testing"
	"time"

	"video-processor/internal/notification/domain"
)

func mustDeliveryIdentity(t *testing.T) domain.DeliveryIdentity {
	t.Helper()
	eventType, err := domain.ParseEventType(domain.EventTypeVideoJobCompleted)
	if err != nil {
		t.Fatalf("ParseEventType error = %v", err)
	}
	channel, err := domain.ParseChannel(domain.ChannelWebhook)
	if err != nil {
		t.Fatalf("ParseChannel error = %v", err)
	}
	identity, err := domain.NewDeliveryIdentity(mustUserID(t, "user-1"), eventType, channel, mustJobID(t, "job-1"))
	if err != nil {
		t.Fatalf("NewDeliveryIdentity error = %v", err)
	}
	return identity
}

func TestNewDeliveryIdentity_RequiresAllFour(t *testing.T) {
	eventType, _ := domain.ParseEventType(domain.EventTypeVideoJobCompleted)
	channel, _ := domain.ParseChannel(domain.ChannelWebhook)
	userID := mustUserID(t, "user-1")
	jobID := mustJobID(t, "job-1")

	tests := []struct {
		name      string
		userID    domain.UserID
		eventType domain.EventType
		channel   domain.Channel
		jobID     domain.JobID
	}{
		{"no user id", domain.UserID{}, eventType, channel, jobID},
		{"no event type", userID, domain.EventType{}, channel, jobID},
		{"no channel", userID, eventType, domain.Channel{}, jobID},
		{"no job id", userID, eventType, channel, domain.JobID{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := domain.NewDeliveryIdentity(tt.userID, tt.eventType, tt.channel, tt.jobID)
			if !errors.Is(err, domain.ErrDeliveryIdentityIncomplete) {
				t.Fatalf("error = %v, want %v", err, domain.ErrDeliveryIdentityIncomplete)
			}
		})
	}

	identity, err := domain.NewDeliveryIdentity(userID, eventType, channel, jobID)
	if err != nil {
		t.Fatalf("NewDeliveryIdentity error = %v", err)
	}
	if identity.IsZero() {
		t.Fatal("IsZero() = true for a built identity")
	}
	if identity.UserID() != userID || identity.EventType() != eventType || identity.Channel() != channel || identity.JobID() != jobID {
		t.Fatal("accessors do not return what was supplied")
	}
}

func TestNewClaimedDelivery(t *testing.T) {
	claimedAt := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	id, err := domain.NewDeliveryID("delivery-1")
	if err != nil {
		t.Fatalf("NewDeliveryID error = %v", err)
	}
	token, err := domain.NewClaimToken("token-1")
	if err != nil {
		t.Fatalf("NewClaimToken error = %v", err)
	}

	delivery, err := domain.NewClaimedDelivery(id, mustDeliveryIdentity(t), token, claimedAt)
	if err != nil {
		t.Fatalf("NewClaimedDelivery error = %v", err)
	}
	if delivery.Status().String() != domain.DeliveryStatusPending {
		t.Fatalf("Status() = %q, want %q", delivery.Status(), domain.DeliveryStatusPending)
	}
	if delivery.Status().IsResolved() {
		t.Fatal("a fresh claim reports IsResolved() = true")
	}
	if delivery.Attempts() != 0 {
		t.Fatalf("Attempts() = %d, want 0", delivery.Attempts())
	}
	if _, resolved := delivery.ResolvedAt(); resolved {
		t.Fatal("a fresh claim reports a resolution time")
	}
	if delivery.Reason() != "" {
		t.Fatalf("Reason() = %q, want empty", delivery.Reason())
	}
	if !delivery.ID().Equal(id) || !delivery.ClaimToken().Equal(token) {
		t.Fatal("the claim does not carry the id and token it was built with")
	}
	if delivery.IsZero() {
		t.Fatal("IsZero() = true for a built delivery")
	}
}

func TestRestoreDelivery_RejectsCorruptedRows(t *testing.T) {
	claimedAt := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	id, _ := domain.NewDeliveryID("delivery-1")
	token, _ := domain.NewClaimToken("token-1")
	pending, _ := domain.ParseDeliveryStatus(domain.DeliveryStatusPending)
	identity := mustDeliveryIdentity(t)

	tests := []struct {
		name      string
		id        domain.DeliveryID
		identity  domain.DeliveryIdentity
		token     domain.ClaimToken
		status    domain.DeliveryStatus
		attempts  int
		claimedAt time.Time
		want      error
	}{
		{"no id", domain.DeliveryID{}, identity, token, pending, 0, claimedAt, domain.ErrInvalidDeliveryID},
		{"no identity", id, domain.DeliveryIdentity{}, token, pending, 0, claimedAt, domain.ErrDeliveryIdentityIncomplete},
		{"no claim token", id, identity, domain.ClaimToken{}, pending, 0, claimedAt, domain.ErrInvalidClaimToken},
		{"no status", id, identity, token, domain.DeliveryStatus{}, 0, claimedAt, domain.ErrInvalidDeliveryStatus},
		{"negative attempts", id, identity, token, pending, -1, claimedAt, domain.ErrDeliveryAttemptsInvalid},
		{"no claimed at", id, identity, token, pending, 0, time.Time{}, domain.ErrDeliveryClaimedAtRequired},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := domain.RestoreDelivery(tt.id, tt.identity, tt.token, tt.status, tt.attempts, tt.claimedAt, time.Time{}, "")
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

// TestRestoreDelivery_RejectsAStatusThatDisagreesWithResolvedAt covers both
// directions, because a row is read through two different accessors: the
// disposition table branches on Status().IsResolved() and the reclaim bound
// on the timestamps, so a row where they disagree is answered differently
// depending on which one a caller consults.
func TestRestoreDelivery_RejectsAStatusThatDisagreesWithResolvedAt(t *testing.T) {
	claimedAt := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	resolvedAt := claimedAt.Add(3 * time.Second)
	id, _ := domain.NewDeliveryID("delivery-1")
	token, _ := domain.NewClaimToken("token-1")
	identity := mustDeliveryIdentity(t)
	pending, _ := domain.ParseDeliveryStatus(domain.DeliveryStatusPending)
	delivered, _ := domain.ParseDeliveryStatus(domain.DeliveryStatusDelivered)
	failed, _ := domain.ParseDeliveryStatus(domain.DeliveryStatusFailed)

	tests := []struct {
		name       string
		status     domain.DeliveryStatus
		resolvedAt time.Time
	}{
		{"delivered with no resolved at", delivered, time.Time{}},
		{"failed with no resolved at", failed, time.Time{}},
		{"pending carrying a resolved at", pending, resolvedAt},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := domain.RestoreDelivery(id, identity, token, tt.status, 1, claimedAt, tt.resolvedAt, "")
			if !errors.Is(err, domain.ErrDeliveryResolvedAtMismatch) {
				t.Fatalf("error = %v, want %v", err, domain.ErrDeliveryResolvedAtMismatch)
			}
		})
	}
}

func TestRestoreDelivery_CarriesAResolvedOutcome(t *testing.T) {
	claimedAt := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	resolvedAt := claimedAt.Add(3 * time.Second)
	id, _ := domain.NewDeliveryID("delivery-1")
	token, _ := domain.NewClaimToken("token-1")
	failed, _ := domain.ParseDeliveryStatus(domain.DeliveryStatusFailed)

	delivery, err := domain.RestoreDelivery(id, mustDeliveryIdentity(t), token, failed, 3, claimedAt, resolvedAt, "notification: delivery failed (unexpected_status: 500)")
	if err != nil {
		t.Fatalf("RestoreDelivery error = %v", err)
	}
	if !delivery.Status().IsResolved() {
		t.Fatal("a failed delivery reports IsResolved() = false")
	}
	got, resolved := delivery.ResolvedAt()
	if !resolved || !got.Equal(resolvedAt) {
		t.Fatalf("ResolvedAt() = %v/%v, want %v/true", got, resolved, resolvedAt)
	}
	if delivery.Attempts() != 3 {
		t.Fatalf("Attempts() = %d, want 3", delivery.Attempts())
	}
}

func TestParseDeliveryStatus(t *testing.T) {
	for _, raw := range []string{domain.DeliveryStatusPending, domain.DeliveryStatusDelivered, domain.DeliveryStatusFailed} {
		status, err := domain.ParseDeliveryStatus(raw)
		if err != nil {
			t.Fatalf("ParseDeliveryStatus(%q) error = %v", raw, err)
		}
		if status.String() != raw {
			t.Fatalf("String() = %q, want %q", status, raw)
		}
	}
	for _, raw := range []string{"", "PENDING", "sent", "queued"} {
		if _, err := domain.ParseDeliveryStatus(raw); !errors.Is(err, domain.ErrInvalidDeliveryStatus) {
			t.Fatalf("ParseDeliveryStatus(%q) error = %v, want %v", raw, err, domain.ErrInvalidDeliveryStatus)
		}
	}
	if (domain.DeliveryStatus{}).IsResolved() {
		t.Fatal("the zero status reports IsResolved() = true")
	}
}

func TestDeliveryIDAndClaimToken(t *testing.T) {
	if _, err := domain.NewDeliveryID(""); !errors.Is(err, domain.ErrInvalidDeliveryID) {
		t.Fatalf("NewDeliveryID(\"\") error = %v, want %v", err, domain.ErrInvalidDeliveryID)
	}
	if _, err := domain.NewClaimToken(""); !errors.Is(err, domain.ErrInvalidClaimToken) {
		t.Fatalf("NewClaimToken(\"\") error = %v, want %v", err, domain.ErrInvalidClaimToken)
	}

	first, _ := domain.NewDeliveryID("delivery-1")
	second, _ := domain.NewDeliveryID("delivery-2")
	if !first.Equal(first) || first.Equal(second) {
		t.Fatal("DeliveryID.Equal does not compare by value")
	}
	if !(domain.DeliveryID{}).IsZero() || first.IsZero() {
		t.Fatal("DeliveryID.IsZero is wrong")
	}

	tokenA, _ := domain.NewClaimToken("token-a")
	tokenB, _ := domain.NewClaimToken("token-b")
	if !tokenA.Equal(tokenA) || tokenA.Equal(tokenB) {
		t.Fatal("ClaimToken.Equal does not compare by value")
	}
	if !(domain.ClaimToken{}).IsZero() || tokenA.IsZero() {
		t.Fatal("ClaimToken.IsZero is wrong")
	}
}

// TestClaimOutcome_ZeroValueIsNotAGrant pins that a repository which returns
// an outcome it forgot to set cannot be read as permission to deliver.
func TestClaimOutcome_ZeroValueIsNotAGrant(t *testing.T) {
	var outcome domain.ClaimOutcome

	if outcome == domain.ClaimGranted {
		t.Fatal("the zero ClaimOutcome equals ClaimGranted")
	}
	if outcome.String() != "unknown" {
		t.Fatalf("String() = %q, want %q", outcome, "unknown")
	}
	for _, tt := range []struct {
		outcome domain.ClaimOutcome
		want    string
	}{
		{domain.ClaimGranted, "granted"},
		{domain.ClaimAlreadyResolved, "already_resolved"},
		{domain.ClaimHeldByAnother, "held_by_another"},
	} {
		if got := tt.outcome.String(); got != tt.want {
			t.Fatalf("String() = %q, want %q", got, tt.want)
		}
	}
	if domain.ClaimAlreadyResolved == domain.ClaimHeldByAnother {
		t.Fatal("the two refusals are the same value; they call for opposite dispositions")
	}
}
