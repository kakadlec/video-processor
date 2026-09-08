package smtp

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"video-processor/internal/notification/domain"
)

// errUnsafeHeaderValue reports that a value bound for a header could not be
// written into one.
//
// It never escapes this package: Deliver classifies it before returning, so
// the port's closed set of three failure kinds stays closed.
var errUnsafeHeaderValue = errors.New("notification: value cannot be written into a message header")

// base64LineBytes is the line length the encoded body is wrapped at, which
// is what RFC 2045 requires of a base64 body.
const base64LineBytes = 76

// buildMessage renders one delivery as an RFC 5322 message.
//
// The body is the Notification context's own account of the outcome, not the
// Video Processing wire payload forwarded and not the webhook envelope's
// JSON pasted into a body. It names the event, the job, when it happened,
// and that outcome's own fields, and nothing else: no credential, no link,
// and not the recipient's own address, which they already know and which the
// delivery record deliberately does not store either.
//
// The delivery identifier travels as the Message-ID, which gives an e-mail
// receiver the same stable handle a webhook receiver gets from the delivery
// header — the deduplication obligation the terminal-event contract assigns
// to a consumer is discharged by the claim, but a receiver deduplicating on
// its own needs something to key on.
//
// The body is base64-encoded rather than sent as-is. A failure's reason is
// text this system generated but did not constrain: it may carry a byte
// outside US-ASCII or a line long enough to exceed the 1000-octet line limit
// SMTP imposes, and either would be a message a relay may reject or mangle.
// Encoding removes both questions, and removes any possibility of a body
// line that looks like the end-of-data marker.
func buildMessage(config Config, preference *domain.NotificationPreference, event domain.TerminalEvent, deliveryID domain.DeliveryID, now time.Time) ([]byte, error) {
	// Checked here rather than trusted from upstream, because upstream is not
	// one place. The two addresses reached this function through the domain's
	// address rule, which already refuses CR and LF — but the job and
	// delivery identifiers did not: JobID and DeliveryID deliberately enforce
	// only non-emptiness, JobID because the value's shape is Video
	// Processing's business and duplicating its UUID rule here would be a
	// second place to keep in sync, DeliveryID because minting belongs to
	// infrastructure. Both of those are the right calls for those types and
	// neither makes a value safe to write into a header block.
	//
	// A job identifier arrives inside a consumed message, so "malformed
	// payload on the terminal queue" is the shape of the attack: a job_id
	// carrying CRLF would end the header block and let its author write
	// headers, or a body, of their own. Refusing here is what makes that
	// impossible without tightening two value objects whose looseness is
	// documented and deliberate.
	for _, value := range []string{
		config.From,
		preference.Destination().String(),
		event.JobID().String(),
		event.EventType().String(),
		deliveryID.String(),
	} {
		if !headerSafe(value) {
			return nil, errUnsafeHeaderValue
		}
	}

	body := renderBody(event)

	var message strings.Builder
	fmt.Fprintf(&message, "From: %s\r\n", config.From)
	fmt.Fprintf(&message, "To: %s\r\n", preference.Destination().String())
	fmt.Fprintf(&message, "Subject: %s\r\n", subjectFor(event))
	fmt.Fprintf(&message, "Date: %s\r\n", now.Format(time.RFC1123Z))
	fmt.Fprintf(&message, "Message-ID: <%s@%s>\r\n", deliveryID.String(), messageIDDomain(config))
	message.WriteString("MIME-Version: 1.0\r\n")
	message.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	message.WriteString("Content-Transfer-Encoding: base64\r\n")
	message.WriteString("\r\n")
	message.WriteString(wrapBase64(body))

	return []byte(message.String()), nil
}

// headerSafe reports whether a value may be written into a header verbatim.
//
// Printable ASCII only, which is the same rule the domain applies to an
// e-mail address and for the same reason: it refuses CR, LF and NUL as a
// consequence rather than as three special cases, so a byte nobody thought
// of cannot slip between them. The body is not subject to it — that is
// base64-encoded, which is why a failure reason may contain anything.
func headerSafe(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < ' ' || value[i] > '~' {
			return false
		}
	}
	return true
}

// subjectFor names the outcome without quoting anything a third party or a
// failing job wrote. A reason belongs in the body, where it is encoded; a
// subject carrying one would be a header built from unconstrained text.
func subjectFor(event domain.TerminalEvent) string {
	if event.EventType().String() == domain.EventTypeVideoJobCompleted {
		return "Your video job finished: " + event.JobID().String()
	}
	return "Your video job failed: " + event.JobID().String()
}

// messageIDDomain is the sender's domain, so the identifier is globally
// unique in the form a receiver expects. The sender was validated as a
// single address, so it has exactly one "@".
func messageIDDomain(config Config) string {
	if at := strings.LastIndex(config.From, "@"); at >= 0 {
		return config.From[at+1:]
	}
	return "localhost"
}

func renderBody(event domain.TerminalEvent) string {
	var body strings.Builder

	if event.EventType().String() == domain.EventTypeVideoJobCompleted {
		body.WriteString("A video job you subscribed to has finished.\r\n\r\n")
	} else {
		body.WriteString("A video job you subscribed to has failed.\r\n\r\n")
	}

	fmt.Fprintf(&body, "Event:    %s\r\n", event.EventType())
	fmt.Fprintf(&body, "Job:      %s\r\n", event.JobID())
	fmt.Fprintf(&body, "Occurred: %s\r\n", event.OccurredAt().UTC().Format(time.RFC3339))

	switch event.EventType().String() {
	case domain.EventTypeVideoJobCompleted:
		fmt.Fprintf(&body, "Frames:   %d\r\n", event.FrameCount())
		if key := event.StorageKey(); key != "" {
			fmt.Fprintf(&body, "Result:   %s\r\n", key)
		}
	case domain.EventTypeVideoJobFailed:
		if reason := event.Reason(); reason != "" {
			fmt.Fprintf(&body, "Reason:   %s\r\n", reason)
		}
	}

	return body.String()
}

func wrapBase64(body string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(body))

	var wrapped strings.Builder
	for len(encoded) > base64LineBytes {
		wrapped.WriteString(encoded[:base64LineBytes])
		wrapped.WriteString("\r\n")
		encoded = encoded[base64LineBytes:]
	}
	wrapped.WriteString(encoded)
	wrapped.WriteString("\r\n")
	return wrapped.String()
}
