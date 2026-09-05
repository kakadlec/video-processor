package domain_test

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"video-processor/internal/notification/domain"
)

func TestDeliveryError_ClassifiesAndRenders(t *testing.T) {
	tests := []struct {
		name string
		err  *domain.DeliveryError
		kind domain.DeliveryFailureKind
		want string
	}{
		{"policy refusal", domain.NewPolicyRefusal(), domain.DeliveryRefusedByPolicy, "notification: delivery failed (refused_by_policy)"},
		{"transport failure", domain.NewTransportFailure(), domain.DeliveryTransportFailure, "notification: delivery failed (transport_failure)"},
		{"non-2xx", domain.NewUnexpectedStatus(503), domain.DeliveryUnexpectedStatus, "notification: delivery failed (unexpected_status: 503)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.Kind() != tt.kind {
				t.Fatalf("Kind() = %v, want %v", tt.err.Kind(), tt.kind)
			}
			if got := tt.err.Error(); got != tt.want {
				t.Fatalf("Error() = %q, want %q", got, tt.want)
			}
		})
	}

	if code := domain.NewUnexpectedStatus(503).StatusCode(); code != 503 {
		t.Fatalf("StatusCode() = %d, want 503", code)
	}
	if code := domain.NewTransportFailure().StatusCode(); code != 0 {
		t.Fatalf("StatusCode() = %d, want 0 on a transport failure", code)
	}
	if got := domain.DeliveryFailureKind(0).String(); got != "unknown" {
		t.Fatalf("String() = %q, want %q", got, "unknown")
	}
}

// TestDeliveryError_CannotCarryATransportErrorsText is the assertion the type
// exists for. A Go transport error is wrapped in a *url.Error whose Error()
// renders the full request URL, and a webhook destination may legitimately
// carry its credential in the query string. The recorded reason is built from
// this error's own text, so the type takes no underlying error at all — the
// guarantee is structural rather than a convention someone has to remember.
func TestDeliveryError_CannotCarryATransportErrorsText(t *testing.T) {
	const credential = "s3cr3t-token"
	transportErr := &url.Error{
		Op:  "Post",
		URL: "https://hooks.example.com/notify?token=" + credential,
		Err: errors.New("dial tcp 203.0.113.7:443: connect: connection refused"),
	}
	if !strings.Contains(transportErr.Error(), credential) {
		t.Fatal("the *url.Error under test does not render its URL; the premise of this test is wrong")
	}

	for _, err := range []*domain.DeliveryError{
		domain.NewPolicyRefusal(),
		domain.NewTransportFailure(),
		domain.NewUnexpectedStatus(500),
	} {
		rendered := fmt.Sprintf("%v %s %q %+v %#v", err, err, err, err, err)
		if strings.Contains(rendered, credential) || strings.Contains(rendered, "hooks.example.com") {
			t.Fatalf("rendering = %q, want it to disclose no destination or credential", rendered)
		}
		if errors.Is(err, transportErr) || errors.Unwrap(err) != nil {
			t.Fatal("a DeliveryError wraps another error; it must carry nothing a transport produced")
		}
	}
}

func TestDeliveryError_IsMatchesByClassification(t *testing.T) {
	if !errors.Is(domain.NewPolicyRefusal(), domain.NewPolicyRefusal()) {
		t.Fatal("two policy refusals do not match under errors.Is")
	}
	if errors.Is(domain.NewPolicyRefusal(), domain.NewTransportFailure()) {
		t.Fatal("a policy refusal matches a transport failure under errors.Is")
	}
	if !errors.Is(domain.NewUnexpectedStatus(500), domain.NewUnexpectedStatus(500)) {
		t.Fatal("two identical status failures do not match under errors.Is")
	}
	if errors.Is(domain.NewUnexpectedStatus(500), domain.NewUnexpectedStatus(404)) {
		t.Fatal("two different status failures match under errors.Is")
	}
}
