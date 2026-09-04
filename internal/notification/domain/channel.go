package domain

import "errors"

// ErrInvalidChannel is returned when a value is outside the closed set of
// delivery channels this context can honour.
var ErrInvalidChannel = errors.New("notification: invalid channel")

// ChannelWebhook is the only delivery channel currently accepted.
//
// There is deliberately no "email" constant. The channel set is closed at
// what an adapter actually delivers through, so email is refused until
// add-notification-email-delivery ships the adapter that honours it: a
// preference the system stores and never acts on is indistinguishable to the
// user from one that is working, which makes a rejected value the better
// outcome.
const ChannelWebhook = "webhook"

// Channel is the transport a notification is delivered through.
type Channel struct {
	value string
}

// ParseChannel accepts only ChannelWebhook.
func ParseChannel(raw string) (Channel, error) {
	if raw != ChannelWebhook {
		return Channel{}, ErrInvalidChannel
	}
	return Channel{value: raw}, nil
}

// String returns the channel's canonical representation.
func (c Channel) String() string {
	return c.value
}

// IsZero reports whether the Channel is the unset zero value.
func (c Channel) IsZero() bool {
	return c.value == ""
}
