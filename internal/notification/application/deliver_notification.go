package application

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"video-processor/internal/notification/domain"
)

// DeliveryDisposition is what handling one event asks the composition root
// to tell the broker.
//
// It is the application layer's own type rather than the messaging package's
// Disposition, which lives in infrastructure and which this layer may not
// name. The split is cmd/worker's: the use case decides *what happened*, the
// composition root decides *what to tell the broker*.
//
// There are two values rather than three because this use case is handed a
// domain.TerminalEvent. An undecodable body and an unrecognized event type
// are decided before it is reached, so Reject belongs entirely to the
// composition root.
type DeliveryDisposition int

// Numbered from one, so the zero value is no disposition at all rather than
// a valid one — the same reason domain.ClaimGranted is.
const (
	// DeliveryHandled means nothing more is owed for this event: it was
	// delivered, or its budget was exhausted, or there was nothing to
	// deliver, or its outcome could not be recorded and no redelivery can
	// help. The composition root acknowledges.
	DeliveryHandled DeliveryDisposition = iota + 1

	// DeliveryDeferred means this handler attempted nothing and the
	// situation clears on its own. The composition root requeues after a
	// pause.
	DeliveryDeferred
)

// String renders the disposition for logs.
func (d DeliveryDisposition) String() string {
	switch d {
	case DeliveryHandled:
		return "handled"
	case DeliveryDeferred:
		return "deferred"
	default:
		return "unknown"
	}
}

// DeliverNotification turns one terminal event into the requests its owner's
// preferences call for, or into a recorded reason why it did not.
type DeliverNotification struct {
	preferences domain.PreferenceRepository
	deliveries  domain.DeliveryRepository
	deliverer   domain.Deliverer
	clock       Clock
	config      DeliveryConfig
	logger      *log.Logger
}

// NewDeliverNotification wires the use case to its ports. A nil logger means
// the standard one.
func NewDeliverNotification(
	preferences domain.PreferenceRepository,
	deliveries domain.DeliveryRepository,
	deliverer domain.Deliverer,
	clock Clock,
	config DeliveryConfig,
	logger *log.Logger,
) *DeliverNotification {
	if logger == nil {
		logger = log.Default()
	}
	return &DeliverNotification{
		preferences: preferences,
		deliveries:  deliveries,
		deliverer:   deliverer,
		clock:       clock,
		config:      config,
		logger:      logger,
	}
}

// Execute handles one event.
//
// The returned error accompanies DeliveryDeferred as the reason the work was
// put back, and is nil for the one deferral that is not a failure — a claim
// another consumer holds. A DeliveryHandled never carries one: by then the
// outcome is recorded, or the accounting loss is logged, and there is
// nothing left for a caller to do with an error.
func (uc *DeliverNotification) Execute(ctx context.Context, event domain.TerminalEvent) (DeliveryDisposition, error) {
	preferences, err := uc.preferences.FindDeliverable(ctx, event.UserID(), event.EventType())
	if err != nil {
		// Before anything could be claimed, let alone attempted. The
		// boundary is "this handler attempted nothing", not "the claim
		// failed", and a read of the preferences sits on the same side of it
		// as the claim does.
		return DeliveryDeferred, fmt.Errorf("notification: find deliverable preferences: %w", err)
	}

	for _, preference := range preferences {
		if preference == nil {
			continue
		}

		// The enrolment boundary: a standing instruction does not announce
		// what happened before it was given. Not-before rather than
		// not-after, so an event occurring in the same instant a preference
		// was created is not delivered.
		if !preference.CreatedAt().Before(event.OccurredAt()) {
			continue
		}

		disposition, deferErr := uc.deliverOne(ctx, preference, event)
		if disposition == DeliveryDeferred {
			// Safe even after an earlier preference in this loop was
			// delivered: the redelivery this asks for meets that
			// preference's row already resolved, so its claim is refused as
			// resolved and nothing is sent twice.
			return DeliveryDeferred, deferErr
		}
	}

	return DeliveryHandled, nil
}

// deliverOne claims, attempts and resolves one preference's delivery.
func (uc *DeliverNotification) deliverOne(ctx context.Context, preference *domain.NotificationPreference, event domain.TerminalEvent) (DeliveryDisposition, error) {
	identity, err := domain.NewDeliveryIdentity(event.UserID(), event.EventType(), preference.Channel(), event.JobID())
	if err != nil {
		// A preference and an event that together cannot name a delivery is
		// a defect in what is stored, and no redelivery repairs it.
		uc.logger.Printf("notification: cannot identify a delivery for job %s on channel %s: %v",
			event.JobID(), preference.Channel(), err)
		return DeliveryHandled, nil
	}

	now := uc.clock.Now()
	delivery, outcome, err := uc.deliveries.ClaimDelivery(ctx, identity, now, now.Add(-uc.config.ReclaimBound))
	if err != nil {
		return DeliveryDeferred, fmt.Errorf("notification: claim delivery: %w", err)
	}

	switch outcome {
	case domain.ClaimGranted:
	case domain.ClaimAlreadyResolved:
		return DeliveryHandled, nil
	case domain.ClaimHeldByAnother:
		// Acknowledging here looks obviously right and is the bug. RabbitMQ
		// redelivers an unacked message as soon as a crashed consumer's
		// channel closes — seconds later, far inside the reclaim bound — so
		// the redelivery meets a *fresh* pending row, is refused, and
		// acknowledging would strand that row and drop the notification.
		//
		// The loop terminates either way: the holder resolves it, and the
		// next refusal is ClaimAlreadyResolved; or it does not, and the
		// bound expires and the claim is granted. The accepted cost, at
		// prefetch 1, is that an abandoned claim stalls the queue for the
		// reclaim bound — which is why DeliveryConfig sizes that bound
		// tightly rather than generously.
		uc.logger.Printf("notification: delivery for job %s on channel %s is claimed by another consumer; deferring",
			event.JobID(), preference.Channel())
		return DeliveryDeferred, nil
	default:
		// Not a claim outcome at all. Nothing was attempted, so deferring is
		// the fail-safe answer: it costs one more pass, where acknowledging
		// would drop the notification.
		return DeliveryDeferred, fmt.Errorf("notification: unknown claim outcome %d", outcome)
	}

	status, attempts, reason := uc.attempt(ctx, preference, event, delivery)
	uc.record(ctx, delivery, preference, event, status, attempts, reason)
	return DeliveryHandled, nil
}

// attempt runs the delivery budget and reports the outcome to record: the
// status, how many requests were actually made, and the reason for a
// failure.
func (uc *DeliverNotification) attempt(ctx context.Context, preference *domain.NotificationPreference, event domain.TerminalEvent, delivery domain.Delivery) (string, int, string) {
	var lastErr error
	attempts := 0
	wait := uc.config.InitialBackoff

	for attempt := 0; attempt < uc.config.MaxAttempts; attempt++ {
		if attempt > 0 {
			if !pause(ctx, wait) {
				break
			}
			wait *= 2
		}

		// Derived from the handler's context, and used for this attempt
		// only. The resolve below derives its own from the same parent
		// rather than from this one, which would hand it a deadline that has
		// already expired.
		attemptCtx, cancel := context.WithTimeout(ctx, uc.config.Timeout)
		err := uc.deliverer.Deliver(attemptCtx, preference, event, delivery.ID())
		cancel()
		attempts++

		if err == nil {
			return domain.DeliveryStatusDelivered, attempts, ""
		}
		lastErr = err

		// Not conditioned on which failure it was. The budget is small
		// enough that retrying a permanent rejection costs little, whereas a
		// table classifying a third party's status codes as retryable is a
		// source of confident wrong guesses about endpoints this system does
		// not control.
	}

	return domain.DeliveryStatusFailed, attempts, reasonFor(lastErr)
}

// record writes the outcome, retrying a repository failure a bounded number
// of times.
//
// A failure here cannot be requeued. This handler holds the claim and has
// already sent a request, so a redelivery would meet its own live claim,
// defer forever, and — worse — send the webhook again first. So the retries
// run and, if the outcome still will not commit, it is logged and the
// message is acknowledged. The retries are what make the composition root's
// shutdown-detached context load-bearing: on a cancellable one they would
// all fail immediately, which is the same reason cmd/worker detaches its own
// CompleteJob retry. That leaves a row pending that nothing will ever
// resolve: an accounting loss, accepted because every alternative is worse,
// and visible in the table rather than silent.
func (uc *DeliverNotification) record(ctx context.Context, delivery domain.Delivery, preference *domain.NotificationPreference, event domain.TerminalEvent, status string, attempts int, reason string) {
	parsed, err := domain.ParseDeliveryStatus(status)
	if err != nil {
		uc.logger.Printf("notification: delivery %s carries an unrecognized status %q and cannot be recorded", delivery.ID(), status)
		return
	}

	var lastErr error
	wait := uc.config.ResolveInitialBackoff

	for attempt := 0; attempt < uc.config.ResolveMaxAttempts; attempt++ {
		if attempt > 0 {
			if !pause(ctx, wait) {
				break
			}
			wait *= 2
		}

		resolveCtx, cancel := context.WithTimeout(ctx, uc.config.ResolveTimeout)
		applied, resolveErr := uc.deliveries.ResolveDelivery(
			resolveCtx, delivery.ID(), delivery.ClaimToken(), parsed, attempts, reason, uc.clock.Now())
		cancel()

		if resolveErr != nil {
			lastErr = resolveErr
			continue
		}
		if !applied {
			// The fence refused the write, so a successor owns this
			// delivery and its outcome is the one that counts. Retrying
			// would only lose again.
			uc.logger.Printf("notification: delivery %s for %s/%s/%s was superseded before its outcome could be recorded",
				delivery.ID(), preference.UserID(), event.EventType(), preference.Channel())
			return
		}

		uc.logger.Printf("notification: delivery %s for %s/%s/%s on job %s recorded as %s after %d attempt(s)%s",
			delivery.ID(), preference.UserID(), event.EventType(), preference.Channel(), event.JobID(), status, attempts, suffixFor(reason))
		return
	}

	uc.logger.Printf("notification: delivery %s for %s/%s/%s on job %s ended as %s but could not be recorded; the outcome is lost: %v",
		delivery.ID(), preference.UserID(), event.EventType(), preference.Channel(), event.JobID(), status, lastErr)
}

// reasonFor renders what is stored and logged about a failure.
//
// Built from a classified error of ours and never from the transport's own
// text: Go wraps a transport error in a *url.Error, whose Error() renders
// the full request URL, so recording one verbatim would write a
// destination's query string — and any credential a receiver issued there —
// into the database and the logs.
func reasonFor(err error) string {
	if err == nil {
		return ""
	}
	var deliveryErr *domain.DeliveryError
	if errors.As(err, &deliveryErr) {
		return deliveryErr.Error()
	}
	// The port admits only *DeliveryError. Anything else could be carrying
	// anything, so its text is dropped rather than stored.
	return "notification: delivery failed (unclassified)"
}

// suffixFor appends a failure's reason to a log line, and nothing to a
// success's.
func suffixFor(reason string) string {
	if reason == "" {
		return ""
	}
	return ": " + reason
}

// pause waits, and reports whether the wait completed. It is the one place
// this use case sleeps, so a cancelled context stops the budget rather than
// running it to the end against a context that is already done.
func pause(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
