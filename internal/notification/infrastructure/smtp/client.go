package smtp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/smtp"
	"net/textproto"
	"time"

	"video-processor/internal/notification/domain"
)

var _ domain.Deliverer = (*Client)(nil)

// Client is domain.Deliverer over SMTP: one attempt at one delivery, bounded
// in time, through the relay this deployment configures.
//
// Retrying, counting attempts and recording an outcome belong to the caller,
// exactly as they do for the webhook client. Every failure leaves here as a
// *domain.DeliveryError carrying a classification this system chose — never
// the relay's own text, which conventionally quotes the envelope recipient
// back and would write a user's address into the delivery record and the
// logs, where neither the record nor this package's log lines carry one.
//
// The standard library's client is used rather than a module of its own.
// What is needed — EHLO, STARTTLS, PLAIN, one recipient, one body — is
// exactly what it covers, and every added module is a govulncheck surface
// this repository gates every pull request on. It is frozen to new features,
// which is why the Deliverer port matters: replacing it later is a change
// confined to this package.
type Client struct {
	config  Config
	timeout time.Duration
	dialer  *net.Dialer

	// now supplies the instant the message is dated — a business timestamp,
	// and nothing else. A field rather than a call to time.Now so a test can
	// pin the Date header; production wiring never sets it. The attempt's
	// deadline deliberately does not come from here; see Deliver.
	now func() time.Time

	// rootCAs overrides the roots the STARTTLS upgrade verifies against. Nil
	// means the system pool, which is what production uses; a test sets it
	// so a fake relay presenting its own certificate can be reached without
	// weakening verification, which InsecureSkipVerify would.
	rootCAs *x509.CertPool
}

// NewClient builds the deliverer. timeout bounds one whole attempt — the
// dial and the entire SMTP conversation after it.
func NewClient(config Config, timeout time.Duration) *Client {
	return &Client{
		config:  config,
		timeout: timeout,
		dialer:  &net.Dialer{Timeout: timeout},
		now:     time.Now,
	}
}

// Deliver makes one attempt.
//
// The bound on that attempt is an absolute deadline set on the connection,
// not the dialer's timeout, and the distinction is the whole reason this
// function is shaped the way it is. net.Dialer bounds only establishment;
// once smtp.NewClient owns the connection, the greeting, EHLO, STARTTLS,
// AUTH, MAIL, RCPT, DATA and QUIT are blocking reads and writes that observe
// no context. A relay that completes the TCP handshake and then stops
// answering would otherwise hold this attempt open indefinitely — past the
// per-attempt timeout, past the application layer's MaxClaimHold, and past
// the reclaim bound that is validated against it at startup, which would
// hand a second consumer the claim while this request was still on the wire.
//
// The deadline covers the conversation; closing the connection when the
// context ends covers cancellation, since a blocked read returns only when
// the descriptor does.
func (c *Client) Deliver(ctx context.Context, preference *domain.NotificationPreference, event domain.TerminalEvent, deliveryID domain.DeliveryID) error {
	if preference == nil {
		return domain.NewPolicyRefusal()
	}

	// Rendered before anything is dialled. A message this system cannot
	// build is one no relay could accept, so opening a connection and a DATA
	// transaction first would spend a round trip to reach the same refusal —
	// and would leave the relay reading a body that never arrives.
	//
	// A refusal here is reported as a policy refusal: no request was made,
	// and none will succeed until the offending value changes, which is the
	// same thing this port says about a destination that can never be
	// dialled. Classified rather than wrapped, so the port's three kinds
	// stay closed and nothing of the value reaches a log.
	message, err := buildMessage(c.config, preference, event, deliveryID, c.now())
	if err != nil {
		return domain.NewPolicyRefusal()
	}

	// One deadline for the whole attempt, taken before the dial and reused
	// for the conversation. Timing the dial and then starting a fresh
	// timeout would let an attempt run to nearly twice the configured bound
	// when establishment consumed most of it — and this type's contract is a
	// bound on the attempt, not on each of its halves. That contract is what
	// MaxClaimHold is arithmetic over.
	//
	// time.Now, deliberately, and not c.now. The injectable clock supplies a
	// business timestamp — the instant the message is dated — which a test
	// pins to a fixed date so the Date header is reproducible. A deadline is
	// not that: it is a real-time bound, measured from the monotonic clock
	// this reading carries, and deriving it from a pinned wall-clock value
	// would set it in the past and fail every read before a byte was sent.
	deadline := time.Now().Add(c.timeout)

	dialCtx, cancelDial := context.WithDeadline(ctx, deadline)
	defer cancelDial()

	conn, err := c.dialer.DialContext(dialCtx, "tcp", c.config.Addr)
	if err != nil {
		return domain.NewTransportFailure()
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetDeadline(deadline); err != nil {
		return domain.NewTransportFailure()
	}

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()

	return c.converse(conn, preference, message)
}

// converse runs the SMTP exchange over an already-bounded connection.
//
// Split from Deliver so the deadline and the cancellation watcher are set up
// exactly once, in one place, ahead of every path that can block.
func (c *Client) converse(conn net.Conn, preference *domain.NotificationPreference, message []byte) error {
	host := c.config.Host()

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return classify(err)
	}
	// Quit is the polite close; the deferred conn.Close in Deliver is what
	// actually guarantees the descriptor is released, so a Quit that fails
	// changes nothing and is not worth classifying.
	defer func() { _ = client.Quit() }()

	if ok, _ := client.Extension("STARTTLS"); ok {
		// MinVersion is set explicitly: the zero value would let the
		// negotiation fall back further than this deployment intends, and
		// leaving it out is the shape gosec flags.
		if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12, RootCAs: c.rootCAs}); err != nil {
			return classify(err)
		}
	}

	if c.config.Authenticates() {
		// Credentials are never offered over an unencrypted session. The
		// relay is operator-supplied and trusted, so this is not the
		// destination policy's question and the switch that relaxes that
		// policy deliberately does not reach it: what is protected here is
		// this deployment's own credential, and a relay that offers no
		// encryption is one this process declines to authenticate to.
		if _, encrypted := client.TLSConnectionState(); !encrypted {
			return domain.NewPolicyRefusal()
		}
		if err := client.Auth(smtp.PlainAuth("", c.config.Username, c.config.Password, host)); err != nil {
			return classify(err)
		}
	}

	if err := client.Mail(c.config.From); err != nil {
		return classify(err)
	}
	if err := client.Rcpt(preference.Destination().String()); err != nil {
		return classify(err)
	}

	writer, err := client.Data()
	if err != nil {
		return classify(err)
	}
	if _, err := writer.Write(message); err != nil {
		return classify(err)
	}
	// Close is what writes the end-of-data marker and reads the relay's
	// verdict, so its error — not Write's — is the one that says whether the
	// message was accepted.
	if err := writer.Close(); err != nil {
		return classify(err)
	}

	return nil
}

// classify turns any failure into one of ours.
//
// An SMTP reply is a numeric code and a human-readable message. The code is
// the one value from a relay's answer that is safe to record — it is a
// number this system did not compose — and the message is not: relays
// conventionally quote the envelope recipient back in it, which for this
// channel is a user's own address.
func classify(err error) error {
	var reply *textproto.Error
	if errors.As(err, &reply) {
		return domain.NewUnexpectedStatus(reply.Code)
	}
	return domain.NewTransportFailure()
}
