// Command notifier delivers a video job's terminal outcome to the webhook
// destinations its owner registered.
//
// It is the third entrypoint of the same image, and it holds the narrowest
// configuration surface of the three: the Notification context's own
// PostgreSQL DSN and the broker URL, and nothing else. It authenticates no
// caller, so it needs no signing key; it stores no artifact, so it needs no
// bucket; it holds no lease and runs no extraction, so it needs neither
// Redis nor ffmpeg. Requiring any of them would misrepresent what this
// process does.
//
// A separate process rather than a goroutine in cmd/api or cmd/worker: an
// outbound request to a third party must share a lifecycle with neither
// serving HTTP nor the worker's one-extraction-at-a-time shape. The three
// scale on different axes.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	notificationapplication "video-processor/internal/notification/application"
	notificationdomain "video-processor/internal/notification/domain"
	notificationmessaging "video-processor/internal/notification/infrastructure/messaging"
	notificationpostgres "video-processor/internal/notification/infrastructure/postgres"
	notificationwebhook "video-processor/internal/notification/infrastructure/webhook"
	platformrabbitmq "video-processor/internal/platform/rabbitmq"
)

// consumerTag names this process to the broker, for management listings.
const consumerTag = "notification-notifier"

// drainGrace is what the shutdown drain allows on top of the longest a
// claimant may legitimately hold a claim.
//
// The drain is that hold plus this rather than a round constant, so that
// lowering a delivery term shortens the wait with it. The grace covers what
// the budget does not: the broker round trip that acknowledges the delivery
// once the outcome is recorded.
const drainGrace = 30 * time.Second

// The delivery budget's operator-tunable terms.
//
// The defaults, the arithmetic they feed, and the validator that refuses an
// unsafe combination all live with DeliveryConfig in the application layer;
// only the translation from the environment lives here. A use case that read
// os.Getenv itself would be one no test could configure.
//
// Three variables and not seven: these are the terms design.md exposes. The
// backoff intervals and the resolve-retry terms feed MaxClaimHold too, but
// they keep their documented defaults — every variable added here is another
// way to reach a configuration the validator has to refuse at startup.
const (
	envWebhookMaxAttempts     = "NOTIFICATION_WEBHOOK_MAX_ATTEMPTS"
	envWebhookTimeoutSeconds  = "NOTIFICATION_WEBHOOK_TIMEOUT_SECONDS"
	envDeliveryReclaimSeconds = "NOTIFICATION_DELIVERY_RECLAIM_SECONDS"
)

// drainOutcome reports how the wait for the in-flight delivery ended. It is
// a named result rather than a bare bool because what main does with it is a
// decision the spec makes — see run's select and main's close below.
type drainOutcome int

const (
	// drainJoined means the delivery in hand reached a disposition and
	// nothing is borrowing the pool any more.
	drainJoined drainOutcome = iota + 1
	// drainExpired means it did not, and is still running.
	drainExpired
)

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func main() {
	ctx := context.Background()

	deps, err := setupNotifier(ctx)
	if err != nil {
		log.Fatal(err)
	}

	signalCtx, stopSignals := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	consumer := notificationmessaging.NewConsumer(
		deps.rabbit,
		notificationmessaging.TerminalEventsTopology(),
		consumerTag,
		notificationmessaging.DefaultRequeuePause,
		deps.handle,
	)

	run(signalCtx, deps, consumer.Run, deps.drainTimeout())
}

// notifierDeps is the composition root's product: everything one delivery
// needs, already wired.
type notifierDeps struct {
	rabbit platformrabbitmq.Config
	db     *sql.DB
	config notificationapplication.DeliveryConfig

	deliver *notificationapplication.DeliverNotification
}

// drainTimeout is how long shutdown waits for the delivery in hand.
//
// The sum cannot overflow, and that is an invariant setupNotifier holds
// rather than a property of this struct: it validates the configuration
// before any notifierDeps exists, and Validate refuses any configuration
// whose hold does not leave room for twice itself — so the hold this adds to
// is at most half the representable range.
func (d *notifierDeps) drainTimeout() time.Duration {
	return d.config.MaxClaimHold() + drainGrace
}

// setupNotifier builds the Notification context's dependencies.
//
// Configuration first, all of it, before any I/O: a missing variable is
// reported as a missing variable rather than as a failure to reach something
// the process was never configured to reach. The delivery budget is
// validated here for the same reason — the reclaim bound is what makes an
// abandoned claim recoverable, and a bound below twice one claimant's
// maximum hold hands a second consumer the claim mid-flight, which is a
// startup failure and not a warning.
//
// PostgreSQL is confirmed reachable; the broker deliberately is not. The
// consumer dials with backoff and redials, so broker reachability is not a
// startup gate in either direction — neither is any other process's having
// started first, which is why this process migrates its own schema.
func setupNotifier(ctx context.Context) (*notifierDeps, error) {
	rabbitConfig, err := platformrabbitmq.LoadConfigFromEnv()
	if err != nil {
		return nil, err
	}
	postgresConfig, err := notificationpostgres.LoadConfigFromEnv()
	if err != nil {
		return nil, err
	}
	policy, err := notificationwebhook.LoadDestinationPolicyFromEnv()
	if err != nil {
		return nil, err
	}

	config, err := loadDeliveryConfig()
	if err != nil {
		return nil, err
	}

	db, err := notificationpostgres.Open(postgresConfig)
	if err != nil {
		return nil, fmt.Errorf("notification: notifier: open postgres: %w", err)
	}
	if err := notificationpostgres.Migrate(ctx, db); err != nil {
		closeDB(db)
		return nil, fmt.Errorf("notification: notifier: migrate: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		closeDB(db)
		return nil, fmt.Errorf("notification: notifier: ping postgres: %w", err)
	}

	log.Printf("notification: notifier: destination policy: insecure destinations allowed=%t", policy.AllowsInsecure())

	// The transport's timeout is the configured one, not a constant of its
	// own. MaxClaimHold — and with it the reclaim bound the whole claim
	// protocol is sized against — is arithmetic over config.Timeout, so a
	// client bounded by anything else makes that arithmetic a lie.
	deliverer := notificationwebhook.NewClient(policy, config.Timeout)

	return &notifierDeps{
		rabbit: rabbitConfig,
		db:     db,
		config: config,
		deliver: notificationapplication.NewDeliverNotification(
			notificationpostgres.NewPreferenceRepository(db),
			notificationpostgres.NewDeliveryRepository(db),
			deliverer,
			systemClock{},
			config,
			nil,
		),
	}, nil
}

// loadDeliveryConfig starts from the documented defaults, applies whatever
// the environment overrides, and validates the result.
//
// Validating here is what makes the check meaningful at all: its own
// documentation calls it a startup check rather than a comment "because
// these are separately tunable variables", and a budget that could only ever
// be the compile-time default would be one the validator could never refuse.
func loadDeliveryConfig() (notificationapplication.DeliveryConfig, error) {
	config := notificationapplication.DefaultDeliveryConfig()

	attempts, err := positiveIntFromEnv(envWebhookMaxAttempts, config.MaxAttempts)
	if err != nil {
		return notificationapplication.DeliveryConfig{}, err
	}
	config.MaxAttempts = attempts

	timeout, err := positiveSecondsFromEnv(envWebhookTimeoutSeconds, config.Timeout)
	if err != nil {
		return notificationapplication.DeliveryConfig{}, err
	}
	config.Timeout = timeout

	reclaim, err := positiveSecondsFromEnv(envDeliveryReclaimSeconds, config.ReclaimBound)
	if err != nil {
		return notificationapplication.DeliveryConfig{}, err
	}
	config.ReclaimBound = reclaim

	if err := config.Validate(); err != nil {
		return notificationapplication.DeliveryConfig{}, err
	}
	return config, nil
}

// positiveIntFromEnv reads an optional whole-number override, keeping the
// default when the variable is unset or empty.
//
// A value that is not a positive integer is refused rather than defaulted,
// for the reason LoadDestinationPolicyFromEnv gives about a non-boolean:
// both ways of guessing at a typo are wrong, and here one of them silently
// restores a budget the operator meant to change — which is the budget the
// reclaim bound is validated against.
func positiveIntFromEnv(name string, fallback int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("notification: %s must be a positive integer, got %q", name, raw)
	}
	return value, nil
}

// maxConfigurableSeconds is the largest second count a time.Duration can
// hold. Refused here rather than left to wrap: a wrapped product can land
// back on a small positive duration, which Validate would then accept as a
// deliberately tiny timeout.
const maxConfigurableSeconds = int(math.MaxInt64 / int64(time.Second))

func positiveSecondsFromEnv(name string, fallback time.Duration) (time.Duration, error) {
	seconds, err := positiveIntFromEnv(name, int(fallback/time.Second))
	if err != nil {
		return 0, err
	}
	if seconds > maxConfigurableSeconds {
		return 0, fmt.Errorf("notification: %s must be at most %d seconds, got %d", name, maxConfigurableSeconds, seconds)
	}
	return time.Duration(seconds) * time.Second, nil
}

// run consumes until ctx is cancelled, waits up to drain for the delivery in
// flight to reach a disposition, and closes the database pool only if that
// wait succeeded.
//
// The close lives here rather than in main, which is where cmd/api and
// cmd/worker put theirs, because here it is conditional on how the wait
// ended and this is the only place that is known. What it consumes with is a
// parameter for the same reason the drain is one: a test cannot wait out the
// production drain, and the branch below that matters most is reached only
// with a delivery that is still running when it expires.
func run(ctx context.Context, deps *notifierDeps, consume func(context.Context) error, drain time.Duration) drainOutcome {
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		if err := consume(ctx); err != nil {
			log.Printf("notification: notifier: consumer: %v", err)
		}
	}()

	<-ctx.Done()
	log.Print("notification: notifier: shutdown signal received")

	select {
	case <-consumerDone:
		// After the join, never before it: the delivery in hand borrows this
		// pool for the whole of a claim, and closing underneath one would
		// turn a resolvable outcome into an aborted one.
		closeDB(deps.db)
		return drainJoined
	case <-time.After(drain):
		// The bound wins, and skipping the close is its consequence rather
		// than an oversight. The handler runs on a context this signal does
		// not cancel, so at this point it is still running and can never be
		// joined: "close only after the join" and "exit at the bound" cannot
		// both be honoured. Closing the pool anyway would abort the write
		// recording an outcome already earned, and waiting longer would let
		// one hung destination keep the process alive indefinitely. So the
		// process exits without an orderly close and lets exit release the
		// connections — nothing is lost that the reclaim bound does not
		// already cover, since an unresolved claim is exactly what a later
		// consumer reclaims.
		log.Printf("notification: notifier: drain deadline of %s expired with a delivery still in flight; exiting without closing the pool", drain)
		return drainExpired
	}
}

// handle carries the whole disposition table, and it turns on one question
// asked first: has this handler attempted anything yet?
//
// Nothing before the use case has, so a message this process cannot make
// sense of is dead-lettered — redelivering it reproduces the same failure
// forever — and everything the use case reports as deferred is requeued,
// because at that point no request has been made and no claim is held. Once
// an attempt has been made, Ack is the only honest verdict: a requeue would
// discard an outcome already earned and could send the webhook a second
// time.
func (d *notifierDeps) handle(ctx context.Context, eventType string, body []byte) notificationmessaging.Disposition {
	event, err := decodeEvent(eventType, body)
	if err != nil {
		log.Printf("notification: notifier: %v; dead-lettering", err)
		return notificationmessaging.Reject
	}

	disposition, err := d.deliver.Execute(ctx, event)
	switch disposition {
	case notificationapplication.DeliveryHandled:
		return notificationmessaging.Ack
	case notificationapplication.DeliveryDeferred:
		if err != nil {
			log.Printf("notification: notifier: %s job %s: deferred: %v", eventType, event.JobID(), err)
		} else {
			// The one deferral that is not a failure: another consumer holds
			// the claim and has not yet reached the reclaim bound.
			log.Printf("notification: notifier: %s job %s: deferred: the claim is held by another consumer", eventType, event.JobID())
		}
		return notificationmessaging.Requeue
	default:
		// Unreachable: every path out of Execute returns one of the two
		// above. Dead-lettered rather than acknowledged because Ack asserts
		// a settled outcome, and a disposition that arrived unset asserts
		// nothing — the dead-letter queue is where it can be seen.
		log.Printf("notification: notifier: %s job %s: unknown disposition %d; dead-lettering", eventType, event.JobID(), disposition)
		return notificationmessaging.Reject
	}
}

// decodeEvent turns a delivery into this context's own model of the outcome.
//
// This is the sanctioned crossing: the message's user_id is Identity's
// identifier as Video Processing published it, and it becomes a
// notification.UserID here, in the composition root, rather than anywhere a
// dependency rule would have to allow it.
//
// Every failure here is a permanent defect in the message — a body that will
// not decode, an event type this context does not recognize, a field the
// domain refuses. The error names which one, because the dead-letter queue
// is the only place anyone will read it, and it never carries the body.
func decodeEvent(eventType string, body []byte) (notificationdomain.TerminalEvent, error) {
	switch eventType {
	case notificationdomain.EventTypeVideoJobCompleted:
		message, err := notificationmessaging.ParseJobCompletedMessage(body)
		if err != nil {
			return notificationdomain.TerminalEvent{}, err
		}
		if err := matchesRoutingKey(eventType, message.Type); err != nil {
			return notificationdomain.TerminalEvent{}, err
		}
		jobID, userID, err := identifiers(message.JobID, message.UserID)
		if err != nil {
			return notificationdomain.TerminalEvent{}, fmt.Errorf("notification: notifier: %s: %w", eventType, err)
		}
		event, err := notificationdomain.NewCompletedEvent(jobID, userID, message.OccurredAt, message.FrameCount, message.StorageKey)
		if err != nil {
			return notificationdomain.TerminalEvent{}, fmt.Errorf("notification: notifier: %s job %s: %w", eventType, message.JobID, err)
		}
		return event, nil

	case notificationdomain.EventTypeVideoJobFailed:
		message, err := notificationmessaging.ParseJobFailedMessage(body)
		if err != nil {
			return notificationdomain.TerminalEvent{}, err
		}
		if err := matchesRoutingKey(eventType, message.Type); err != nil {
			return notificationdomain.TerminalEvent{}, err
		}
		jobID, userID, err := identifiers(message.JobID, message.UserID)
		if err != nil {
			return notificationdomain.TerminalEvent{}, fmt.Errorf("notification: notifier: %s: %w", eventType, err)
		}
		event, err := notificationdomain.NewFailedEvent(jobID, userID, message.OccurredAt, message.ErrorReason)
		if err != nil {
			return notificationdomain.TerminalEvent{}, fmt.Errorf("notification: notifier: %s job %s: %w", eventType, message.JobID, err)
		}
		return event, nil

	default:
		return notificationdomain.TerminalEvent{}, fmt.Errorf("notification: notifier: unrecognized event type %q", eventType)
	}
}

// matchesRoutingKey refuses a payload whose own type field disagrees with
// the routing key it arrived under.
//
// The routing key is what chooses the parser, and the two payload shapes
// decode from each other's bytes without error: a completion read as a
// failure yields an empty reason, which the domain accepts, so the mismatch
// would be announced to a subscriber as a failure that never happened.
// Neither the parser nor the domain can catch it — only the disagreement
// between the two halves of the message can, and the producer sets both from
// one event type, so a disagreement is a defect no redelivery repairs.
func matchesRoutingKey(eventType, payloadType string) error {
	if payloadType != eventType {
		return fmt.Errorf("notification: notifier: %s: the payload declares type %q", eventType, payloadType)
	}
	return nil
}

// identifiers builds the two value objects both message shapes carry, naming
// which of them was refused.
func identifiers(rawJobID, rawUserID string) (notificationdomain.JobID, notificationdomain.UserID, error) {
	jobID, err := notificationdomain.NewJobID(rawJobID)
	if err != nil {
		return notificationdomain.JobID{}, notificationdomain.UserID{}, fmt.Errorf("job id: %w", err)
	}
	userID, err := notificationdomain.NewUserID(rawUserID)
	if err != nil {
		return notificationdomain.JobID{}, notificationdomain.UserID{}, fmt.Errorf("job %s: user id: %w", rawJobID, err)
	}
	return jobID, userID, nil
}

func closeDB(db *sql.DB) {
	if err := db.Close(); err != nil {
		log.Printf("notification: notifier: close postgres: %v", err)
	}
}
