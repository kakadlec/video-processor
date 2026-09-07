package domain

import "errors"

// ErrInvalidChannel is returned when a value is outside the closed set of
// delivery channels this context can honour.
var ErrInvalidChannel = errors.New("notification: invalid channel")

// The delivery channels an adapter actually delivers through.
//
// The set is closed, and stays closed as it grows: a channel is added here
// when something can send on it, never ahead of that. A preference the
// system stores and never acts on is indistinguishable to its owner from
// one that is working, which is why a value outside this set is refused
// rather than accepted and ignored.
const (
	ChannelWebhook = "webhook"
	ChannelEmail   = "email"
)

// Channel is the transport a notification is delivered through.
type Channel struct {
	value string
}

// ParseChannel accepts only a value in the closed set above.
func ParseChannel(raw string) (Channel, error) {
	switch raw {
	case ChannelWebhook, ChannelEmail:
		return Channel{value: raw}, nil
	default:
		return Channel{}, ErrInvalidChannel
	}
}

// String returns the channel's canonical representation.
func (c Channel) String() string {
	return c.value
}

// IsZero reports whether the Channel is the unset zero value.
func (c Channel) IsZero() bool {
	return c.value == ""
}

// Signs reports whether a delivery on this channel is signed with the
// preference's own secret.
//
// It is what the secret's invariants are conditional on, rather than a
// comparison against ChannelWebhook written out at each site: "carries a
// signing secret" is a property of the channel's delivery mechanism, and
// naming it once is what keeps the schema constraint, the create path, the
// aggregate and the delivery read agreeing on the same rule. The zero value
// does not sign, which is the safe answer for a channel that was never
// parsed.
func (c Channel) Signs() bool {
	return c.value == ChannelWebhook
}
