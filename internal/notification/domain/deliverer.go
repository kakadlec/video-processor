package domain

import (
	"context"
	"fmt"
)

// DeliveryFailureKind classifies why one attempt did not deliver. The
// recorded reason has to say which, because the three call for different
// operator responses: a policy refusal will never succeed until the
// destination changes, a transport failure may clear on its own, and a
// non-2xx is the receiver's own answer.
type DeliveryFailureKind int

const (
	// DeliveryRefusedByPolicy means the destination policy refused the URL or
	// the address it resolved to. No request was made.
	DeliveryRefusedByPolicy DeliveryFailureKind = iota + 1

	// DeliveryTransportFailure means the request could not be completed —
	// refused, timed out, TLS rejected, redirected.
	DeliveryTransportFailure

	// DeliveryUnexpectedStatus means the receiver answered something other
	// than 2xx.
	DeliveryUnexpectedStatus
)

// String renders the kind for logs and for the stored reason.
func (k DeliveryFailureKind) String() string {
	switch k {
	case DeliveryRefusedByPolicy:
		return "refused_by_policy"
	case DeliveryTransportFailure:
		return "transport_failure"
	case DeliveryUnexpectedStatus:
		return "unexpected_status"
	default:
		return "unknown"
	}
}

// DeliveryError is the only error a Deliverer reports, and it is built from
// values this system chose rather than from anything a third party or the
// transport produced.
//
// That is the point of the type, not a stylistic preference. Go wraps a
// transport error in a *url.Error, whose Error() renders the full request
// URL — and a webhook destination may legitimately carry its credential in
// the query string, which many receivers issue exactly that way. Recording or
// logging such an error verbatim would write that credential into the
// delivery record and the logs, which is why this type holds no wrapped
// error, offers no Unwrap, and renders nothing it was not given as a
// classification. A response body is excluded for the same reason from the
// other direction: it is a third party's text, not ours.
type DeliveryError struct {
	kind       DeliveryFailureKind
	statusCode int
}

// NewPolicyRefusal builds the refused-by-policy failure.
func NewPolicyRefusal() *DeliveryError {
	return &DeliveryError{kind: DeliveryRefusedByPolicy}
}

// NewTransportFailure builds the transport failure. It deliberately takes no
// underlying error: there is nothing safe to carry out of one.
func NewTransportFailure() *DeliveryError {
	return &DeliveryError{kind: DeliveryTransportFailure}
}

// NewUnexpectedStatus builds the non-2xx failure. The status code is the
// receiver's, and is the one value from the response that is safe to record.
func NewUnexpectedStatus(statusCode int) *DeliveryError {
	return &DeliveryError{kind: DeliveryUnexpectedStatus, statusCode: statusCode}
}

// Kind returns the classification.
func (e *DeliveryError) Kind() DeliveryFailureKind { return e.kind }

// StatusCode returns the response status, and is meaningful only for
// DeliveryUnexpectedStatus.
func (e *DeliveryError) StatusCode() int { return e.statusCode }

// Error renders the failure. Its output is what the delivery record stores as
// the reason, so it names the classification and, for a non-2xx, the status
// code — and nothing else.
func (e *DeliveryError) Error() string {
	if e.kind == DeliveryUnexpectedStatus {
		return fmt.Sprintf("notification: delivery failed (%s: %d)", e.kind, e.statusCode)
	}
	return fmt.Sprintf("notification: delivery failed (%s)", e.kind)
}

// Is lets errors.Is match two DeliveryErrors by classification, so a caller
// can test for a policy refusal without reaching for the concrete type.
func (e *DeliveryError) Is(target error) bool {
	other, ok := target.(*DeliveryError)
	return ok && other.kind == e.kind && other.statusCode == e.statusCode
}

// Deliverer is the outbound port: one attempt at one delivery. The
// application layer holds this interface and therefore never names net/http,
// a TLS configuration, or a dialer.
//
// The preference is passed whole because the implementation needs both the
// destination and the secret to sign with; it is the aggregate rather than a
// PreferenceView for exactly that reason, and this is the only port that
// takes one. deliveryID is what the request announces to the receiver as its
// deduplication key, so every attempt within a budget passes the same value.
//
// Every failure is reported as a *DeliveryError. Retrying, counting attempts
// and recording the outcome belong to the caller, not here.
type Deliverer interface {
	Deliver(ctx context.Context, preference *NotificationPreference, event TerminalEvent, deliveryID DeliveryID) error
}
