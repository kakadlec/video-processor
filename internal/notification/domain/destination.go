package domain

import (
	"errors"
	"net/url"
)

// ErrInvalidDestination is returned when a value fails Destination
// construction, whichever channel's rule judged it.
//
// One sentinel across both rules rather than one per channel: the caller
// that maps it answers 400 either way, and a client that could tell "not a
// URL" from "not an address" apart has learned only which channel it named.
var ErrInvalidDestination = errors.New("notification: invalid destination")

// Destination is where a notification is delivered — the absolute URL a
// webhook is posted to, or the address an e-mail is sent to.
//
// Which of the two a value must be is decided by the channel, so it is
// built through NewDestinationFor rather than by a single rule applied
// everywhere. One string either way, stored in one column: nothing queries
// inside it, and a sum type would push a type switch into three packages to
// buy nothing.
//
// http is accepted alongside https because local development and the compose
// stack have no TLS, and refusing it would make the feature untestable in
// the only environment this project runs in (design.md records the
// trade-off: restricting production destinations to https belongs to
// add-notification-webhook-delivery, the change that opens the connection).
//
// Nothing here verifies that the URL exists or that the caller controls it.
// That, and the SSRF-shaped question of a destination pointing at an
// internal address, become real only when something dials it.
type Destination struct {
	value string
}

// NewDestinationFor validates raw under the rule the channel's delivery
// mechanism needs: a dialable URL for a webhook, an address for an e-mail.
//
// The switch is over the closed channel set and its default refuses, which
// is what keeps the pairing exhaustive: a Channel that was never parsed —
// the zero value — matches neither case and is rejected, rather than
// falling through to whichever rule happens to be written first.
func NewDestinationFor(channel Channel, raw string) (Destination, error) {
	switch channel.String() {
	case ChannelWebhook:
		return NewDestination(raw)
	case ChannelEmail:
		address, err := parseEmailAddress(raw)
		if err != nil {
			return Destination{}, err
		}
		return Destination{value: address}, nil
	default:
		return Destination{}, ErrInvalidDestination
	}
}

// NewDestination validates raw as an absolute http or https URL with a host,
// which is the webhook channel's rule. It is kept as its own constructor so
// the sites that are webhook-only by construction say so.
func NewDestination(raw string) (Destination, error) {
	if raw == "" {
		return Destination{}, ErrInvalidDestination
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return Destination{}, ErrInvalidDestination
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return Destination{}, ErrInvalidDestination
	}
	// A scheme alone does not make a URL absolute in the sense that matters
	// here: url.Parse("http:///path") reports scheme http and no host, which
	// is not an address anything could be delivered to. Hostname() rather
	// than Host, because a port-only authority is the same failure wearing a
	// disguise — url.Parse("http://:8080/hooks") reports Host ":8080" and no
	// hostname at all.
	if parsed.Hostname() == "" {
		return Destination{}, ErrInvalidDestination
	}

	return Destination{value: raw}, nil
}

// String returns the destination's canonical representation.
func (d Destination) String() string {
	return d.value
}

// IsZero reports whether the Destination is the unset zero value.
func (d Destination) IsZero() bool {
	return d.value == ""
}
