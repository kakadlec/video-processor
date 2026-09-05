package webhook

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"

	"video-processor/internal/notification/domain"
)

const (
	// userAgent identifies this sender to a receiver's logs. Its own suffix
	// is the sender's generation and is unrelated to payloadVersion.
	userAgent = "fiapx-notifier/1"

	// maxResponseBytes bounds what is read from a response. The body is
	// discarded — nothing a third party writes enters this system, which is
	// the same reason a delivery reason is never built from one — but it is
	// read rather than abandoned so the connection can be reused, and it is
	// read under a cap so a receiver answering with an endless body cannot
	// hold memory or the attempt open.
	maxResponseBytes = 4 << 10

	headerEvent     = "X-FiapX-Event"
	headerDelivery  = "X-FiapX-Delivery"
	headerTimestamp = "X-FiapX-Timestamp"
	headerSignature = "X-FiapX-Signature"
)

// errRedirectRefused is what CheckRedirect answers with. Refusing rather
// than returning the redirect response is deliberate: a 3xx recorded as an
// unexpected status would read as the receiver's own answer, when what
// happened is that this system declined to follow a destination its owner
// never registered and the policy never judged.
var errRedirectRefused = errors.New("notification: redirects are not followed")

var _ domain.Deliverer = (*Client)(nil)

// Client is domain.Deliverer over HTTP: one attempt at one delivery, bounded
// in time and in bytes, to an address the policy has approved.
//
// Retrying, counting attempts and recording an outcome belong to the caller.
// Every failure leaves here as a *domain.DeliveryError, which carries a
// classification this system chose and nothing else — in particular not the
// transport's own error, whose *url.Error rendering would carry the
// destination's query string, and with it any credential a receiver issued
// there.
type Client struct {
	policy domain.DestinationPolicy
	http   *http.Client

	// now supplies the instant a request is signed at. It is a field rather
	// than a call to time.Now so a test can pin the timestamp a receiver
	// verifies against; production wiring never sets it.
	now func() time.Time
}

// NewClient builds the deliverer. timeout bounds one whole attempt —
// connect, TLS, request and the capped response read.
func NewClient(policy domain.DestinationPolicy, timeout time.Duration) *Client {
	client := &Client{policy: policy, now: time.Now}

	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	// Control runs after resolution with the address about to be connected,
	// which is the only place a hostname resolving to 169.254.169.254 is
	// actually caught.
	dialer.Control = client.controlDial

	client.http = &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			// Explicitly nil, and never http.ProxyFromEnvironment — which is
			// what http.DefaultTransport sets, and therefore what a
			// hand-rolled transport is most likely to be copied from. With a
			// proxy configured, the connection the guarded dial opens is a
			// connection to the *proxy*, whose address is public and duly
			// approved; the proxy then resolves the user's hostname itself
			// and connects to whatever it finds. The entire address rule
			// would be bypassed by an environment variable set for an
			// unrelated reason, and nothing in the code would look wrong.
			Proxy:                 nil,
			DialContext:           dialer.DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          10,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   timeout,
			ExpectContinueTimeout: time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return errRedirectRefused },
	}
	return client
}

// controlDial is the dial-time half of the destination policy.
//
// It judges the resolved address rather than the hostname, which is what the
// netip.Addr in the policy's own signature is there to force.
func (c *Client) controlDial(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return domain.ErrDestinationRefused
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		// Control is called with an already-resolved address, so a value
		// that does not parse as one is something this policy cannot judge.
		// Refusing is the only safe answer.
		return domain.ErrDestinationRefused
	}
	return c.policy.CheckAddr(addr)
}

// Deliver makes one attempt.
func (c *Client) Deliver(ctx context.Context, preference *domain.NotificationPreference, event domain.TerminalEvent, deliveryID domain.DeliveryID) error {
	if preference == nil {
		return domain.NewPolicyRefusal()
	}

	// The scheme and host rules are applied here as well as at registration.
	// A row stored before this policy existed — or before it was tightened —
	// is refused at delivery rather than dialled, which is what makes the
	// recorded reason for such a row name the policy.
	if err := c.policy.CheckDestination(preference.Destination()); err != nil {
		return domain.NewPolicyRefusal()
	}

	body, err := buildPayload(event, deliveryID)
	if err != nil {
		// Unreachable for the closed set of event types this envelope is
		// built from, and classified rather than wrapped anyway: the port
		// admits three kinds, and inventing a fourth to describe our own
		// encoder is worth less than keeping that set closed.
		return domain.NewTransportFailure()
	}

	timestamp := renderTimestamp(c.now())

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, preference.Destination().String(), bytes.NewReader(body))
	if err != nil {
		// This error renders the URL it failed to parse, so it is
		// classified and dropped rather than wrapped.
		return domain.NewTransportFailure()
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", userAgent)
	request.Header.Set(headerEvent, event.EventType().String())
	request.Header.Set(headerDelivery, deliveryID.String())
	request.Header.Set(headerTimestamp, timestamp)
	// The signature covers the exact bytes sent, and is the only place the
	// secret appears in the request.
	request.Header.Set(headerSignature, sign(preference.Secret(), timestamp, body))

	response, err := c.http.Do(request)
	if err != nil {
		// A Control refusal reaches here wrapped in a *net.OpError inside a
		// *url.Error; errors.Is matches through both. Distinguishing it
		// matters because a policy refusal will never succeed until the
		// destination changes, while a transport failure may clear on its
		// own — and because the recorded reason has to say which.
		if errors.Is(err, domain.ErrDestinationRefused) {
			return domain.NewPolicyRefusal()
		}
		return domain.NewTransportFailure()
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))

	if response.StatusCode < http.StatusOK || response.StatusCode > 299 {
		return domain.NewUnexpectedStatus(response.StatusCode)
	}
	return nil
}
