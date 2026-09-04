package domain

import (
	"errors"
	"net/url"
)

// ErrInvalidDestination is returned when a value fails Destination
// construction — unparseable, relative, empty, hostless, or carrying a
// scheme other than http or https.
var ErrInvalidDestination = errors.New("notification: invalid destination")

// Destination is the absolute URL a webhook notification is delivered to.
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

// NewDestination validates raw as an absolute http or https URL with a host.
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
