package main

import (
	"context"
	"fmt"

	notificationdomain "video-processor/internal/notification/domain"
)

var _ notificationdomain.Deliverer = (*channelDeliverer)(nil)

// channelDeliverer sends each delivery through the implementation composed
// for its preference's channel.
//
// The routing lives here rather than in the use case deliberately. What
// DeliverNotification owns is the claim/attempt/resolve protocol — the
// claim, the fence, the budget and the disposition — and none of that varies
// by channel. A switch inside it would put a transport concern in the
// application layer and give every future channel a reason to edit the one
// file that holds the fencing logic; composing it out here is what lets that
// file be literally unchanged by a second channel, which is also what makes
// "the new channel inherits the first one's guarantees" checkable rather
// than asserted.
type channelDeliverer struct {
	byChannel map[string]notificationdomain.Deliverer
}

// newChannelDeliverer refuses unless every channel in the closed set has an
// implementation.
//
// Exhaustive at startup, not at delivery: a channel with no deliverer would
// otherwise be discovered by the first user who registered a preference on
// it, as a failed delivery they cannot see the cause of. That is the
// stored-and-never-honoured outcome the closed channel set exists to
// prevent, and refusing to boot is the only answer that keeps it prevented
// when the set grows.
func newChannelDeliverer(byChannel map[string]notificationdomain.Deliverer) (*channelDeliverer, error) {
	for _, channel := range notificationdomain.AllChannels() {
		if byChannel[channel.String()] == nil {
			return nil, fmt.Errorf("notification: notifier: no deliverer composed for channel %q", channel)
		}
	}

	owned := make(map[string]notificationdomain.Deliverer, len(byChannel))
	for name, deliverer := range byChannel {
		owned[name] = deliverer
	}
	return &channelDeliverer{byChannel: owned}, nil
}

// Deliver routes one attempt.
//
// A preference whose channel has no implementation cannot arrive here — the
// constructor refused to build without one, and the channel a stored
// preference carries was parsed from the closed set. The refusal below is
// therefore unreachable rather than defensive, and it is a refusal rather
// than a panic because a delivery path that crashes the process on an
// impossible row would take down the queue for every other user.
func (d *channelDeliverer) Deliver(
	ctx context.Context,
	preference *notificationdomain.NotificationPreference,
	event notificationdomain.TerminalEvent,
	deliveryID notificationdomain.DeliveryID,
) error {
	if preference == nil {
		return notificationdomain.NewPolicyRefusal()
	}

	deliverer, ok := d.byChannel[preference.Channel().String()]
	if !ok {
		return notificationdomain.NewPolicyRefusal()
	}
	return deliverer.Deliver(ctx, preference, event, deliveryID)
}
