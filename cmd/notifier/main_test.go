package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	notificationapplication "video-processor/internal/notification/application"
	notificationdomain "video-processor/internal/notification/domain"
	notificationmessaging "video-processor/internal/notification/infrastructure/messaging"
	notificationwebhook "video-processor/internal/notification/infrastructure/webhook"
)

// A missing variable is fatal and the error names it, for each of the two
// this process requires. Nothing is opened on the way: the configuration is
// all read before any I/O, so an operator who forgot a variable is told
// that, not that something was unreachable.
func TestSetupNotifier_RequiresItsOwnConfiguration(t *testing.T) {
	cases := map[string]struct {
		broker string
		dsn    string
		names  string
	}{
		"no broker url": {broker: "", dsn: "postgres://user:pass@localhost:5432/db?sslmode=disable", names: "RABBITMQ_URL"},
		"no dsn":        {broker: "amqp://guest:guest@localhost:5672/", dsn: "", names: "NOTIFICATION_POSTGRES_DSN"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			clearDeliveryBudgetEnv(t)
			t.Setenv("RABBITMQ_URL", testCase.broker)
			t.Setenv("NOTIFICATION_POSTGRES_DSN", testCase.dsn)

			deps, err := setupNotifier(context.Background())
			if err == nil {
				closeDB(deps.db)
				t.Fatalf("startup succeeded with %s unset", testCase.names)
			}
			if !strings.Contains(err.Error(), testCase.names) {
				t.Errorf("error %q does not name %s", err, testCase.names)
			}
		})
	}
}

// The destination policy is configuration too, and it is read in the same
// pass: a value that is not a boolean fails startup before a connection is
// opened, rather than leaving the process to guess at a security posture.
func TestSetupNotifier_RefusesAnUnparseableDestinationPolicy(t *testing.T) {
	clearDeliveryBudgetEnv(t)
	t.Setenv("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/")
	t.Setenv("NOTIFICATION_POSTGRES_DSN", "postgres://user:pass@localhost:5432/db?sslmode=disable")
	t.Setenv(notificationwebhook.EnvAllowInsecureDestinations, "yes")

	deps, err := setupNotifier(context.Background())
	if err == nil {
		closeDB(deps.db)
		t.Fatal("startup succeeded with an unparseable destination policy")
	}
	if !strings.Contains(err.Error(), notificationwebhook.EnvAllowInsecureDestinations) {
		t.Errorf("error %q does not name the variable", err)
	}
}

// The delivery budget is operator-tunable, and that is what gives the
// startup validator something to refuse: a budget that could only ever be
// the compile-time default is one no configured value could ever break.
func TestLoadDeliveryConfig_AppliesTheEnvironmentOverrides(t *testing.T) {
	clearDeliveryBudgetEnv(t)
	t.Setenv(envWebhookMaxAttempts, "5")
	t.Setenv(envWebhookTimeoutSeconds, "10")
	t.Setenv(envDeliveryReclaimSeconds, "300")

	config, err := loadDeliveryConfig()
	if err != nil {
		t.Fatalf("loadDeliveryConfig() = %v", err)
	}
	if config.MaxAttempts != 5 {
		t.Errorf("MaxAttempts = %d, want 5", config.MaxAttempts)
	}
	if config.Timeout != 10*time.Second {
		t.Errorf("Timeout = %s, want 10s", config.Timeout)
	}
	if config.ReclaimBound != 300*time.Second {
		t.Errorf("ReclaimBound = %s, want 5m0s", config.ReclaimBound)
	}
	// A term this process does not expose keeps its documented default; the
	// overrides above are three variables, not a way in for every term.
	if config.ResolveMaxAttempts != notificationapplication.DefaultResolveMaxAttempts {
		t.Errorf("ResolveMaxAttempts = %d, want the default %d",
			config.ResolveMaxAttempts, notificationapplication.DefaultResolveMaxAttempts)
	}
}

// The refusal the validator exists for, now reachable: attempt terms raised
// past what the default bound covers is a startup failure naming both
// values, not a warning. A second consumer would otherwise be granted the
// claim while this one still has a request on the wire.
func TestLoadDeliveryConfig_RefusesABoundBelowTheConfiguredBudget(t *testing.T) {
	clearDeliveryBudgetEnv(t)
	t.Setenv(envWebhookMaxAttempts, "5")
	t.Setenv(envWebhookTimeoutSeconds, "60")

	_, err := loadDeliveryConfig()
	if err == nil {
		t.Fatal("a budget far exceeding the default reclaim bound was accepted")
	}
	// Computed the way the loader computes it rather than transcribed, so
	// changing a default moves this assertion with it.
	configured := notificationapplication.DefaultDeliveryConfig()
	configured.MaxAttempts = 5
	configured.Timeout = 60 * time.Second
	for _, want := range []string{configured.ReclaimBound.String(), configured.MaxClaimHold().String()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}

// A value that is not a positive, representable count is refused rather than
// defaulted: silently restoring the default would leave an operator who
// meant to change a term running the budget they thought they had replaced.
func TestLoadDeliveryConfig_RefusesAValueThatIsNotAPositiveCount(t *testing.T) {
	cases := map[string]struct {
		name  string
		value string
	}{
		"not a number":                 {envWebhookMaxAttempts, "many"},
		"zero attempts":                {envWebhookMaxAttempts, "0"},
		"a negative timeout":           {envWebhookTimeoutSeconds, "-5"},
		"a bound no duration can hold": {envDeliveryReclaimSeconds, "9999999999999"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			clearDeliveryBudgetEnv(t)
			t.Setenv(testCase.name, testCase.value)

			config, err := loadDeliveryConfig()
			if err == nil {
				t.Fatalf("%s=%q was accepted, yielding %+v", testCase.name, testCase.value, config)
			}
			if !strings.Contains(err.Error(), testCase.name) {
				t.Errorf("error %q does not name %s", err, testCase.name)
			}
		})
	}
}

// clearDeliveryBudgetEnv unsets every budget override, so a value in the
// ambient environment cannot decide what a case exercises.
func clearDeliveryBudgetEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{envWebhookMaxAttempts, envWebhookTimeoutSeconds, envDeliveryReclaimSeconds} {
		t.Setenv(name, "")
	}
}

// The whole disposition table, over the handler's own inputs.
//
// Read it through the rule that produces it rather than as a list: what a
// situation is entitled to depends first on whether this handler has
// attempted anything yet, and second on whether what stopped it clears on
// its own. Every case therefore also asserts how many requests were made,
// because "requeue" and "acknowledge" differ in what they are allowed to
// have done first.
func TestHandle_DispositionTable(t *testing.T) {
	cases := map[string]struct {
		eventType    string
		body         []byte
		preferences  []*notificationdomain.NotificationPreference
		findErr      error
		claimOutcome notificationdomain.ClaimOutcome
		claimErr     error
		deliverErr   error
		resolveApply bool
		resolveErr   error
		want         notificationmessaging.Disposition
		wantAttempts int
	}{
		"an undecodable body is dead-lettered": {
			eventType: notificationdomain.EventTypeVideoJobCompleted,
			body:      []byte("{not json"),
			want:      notificationmessaging.Reject,
		},
		"an unrecognized event type is dead-lettered": {
			eventType: "video_job.completed.v99",
			body:      completedBody(t, testJobID, testUserID),
			want:      notificationmessaging.Reject,
		},
		// The routing key chooses the parser, and the two payload shapes
		// decode from each other's bytes: read as a failure, a completion
		// yields an empty reason the domain accepts, so a mismatch left
		// unchecked would announce a failure that never happened.
		"a completion routed as a failure is dead-lettered": {
			eventType: notificationdomain.EventTypeVideoJobFailed,
			body:      completedBody(t, testJobID, testUserID),
			want:      notificationmessaging.Reject,
		},
		"a payload declaring no type at all is dead-lettered": {
			eventType: notificationdomain.EventTypeVideoJobCompleted,
			body:      completedBodyWithoutType(t),
			want:      notificationmessaging.Reject,
		},
		"a message naming no user is dead-lettered": {
			eventType: notificationdomain.EventTypeVideoJobCompleted,
			body:      completedBody(t, testJobID, ""),
			want:      notificationmessaging.Reject,
		},
		"a message naming no job is dead-lettered": {
			eventType: notificationdomain.EventTypeVideoJobFailed,
			body:      failedBody(t, "", testUserID),
			want:      notificationmessaging.Reject,
		},
		"a completion carrying no artifact key is dead-lettered": {
			eventType: notificationdomain.EventTypeVideoJobCompleted,
			body:      completedBodyWithoutStorageKey(t),
			want:      notificationmessaging.Reject,
		},
		"preferences that cannot be read are requeued": {
			eventType: notificationdomain.EventTypeVideoJobCompleted,
			body:      completedBody(t, testJobID, testUserID),
			findErr:   errors.New("connection refused"),
			want:      notificationmessaging.Requeue,
		},
		"a claim that cannot be written is requeued": {
			eventType:   notificationdomain.EventTypeVideoJobCompleted,
			body:        completedBody(t, testJobID, testUserID),
			preferences: onePreference(t),
			claimErr:    errors.New("connection refused"),
			want:        notificationmessaging.Requeue,
		},
		"a claim held by another consumer is requeued": {
			eventType:    notificationdomain.EventTypeVideoJobCompleted,
			body:         completedBody(t, testJobID, testUserID),
			preferences:  onePreference(t),
			claimOutcome: notificationdomain.ClaimHeldByAnother,
			want:         notificationmessaging.Requeue,
		},
		"a claim refused as already resolved is acknowledged": {
			eventType:    notificationdomain.EventTypeVideoJobCompleted,
			body:         completedBody(t, testJobID, testUserID),
			preferences:  onePreference(t),
			claimOutcome: notificationdomain.ClaimAlreadyResolved,
			want:         notificationmessaging.Ack,
		},
		"nothing to deliver is acknowledged": {
			eventType: notificationdomain.EventTypeVideoJobCompleted,
			body:      completedBody(t, testJobID, testUserID),
			want:      notificationmessaging.Ack,
		},
		"a delivered event is acknowledged": {
			eventType:    notificationdomain.EventTypeVideoJobCompleted,
			body:         completedBody(t, testJobID, testUserID),
			preferences:  onePreference(t),
			claimOutcome: notificationdomain.ClaimGranted,
			resolveApply: true,
			want:         notificationmessaging.Ack,
			wantAttempts: 1,
		},
		"a failure event is delivered and acknowledged": {
			eventType:    notificationdomain.EventTypeVideoJobFailed,
			body:         failedBody(t, testJobID, testUserID),
			preferences:  onePreference(t),
			claimOutcome: notificationdomain.ClaimGranted,
			resolveApply: true,
			want:         notificationmessaging.Ack,
			wantAttempts: 1,
		},
		"an exhausted budget is acknowledged": {
			eventType:    notificationdomain.EventTypeVideoJobCompleted,
			body:         completedBody(t, testJobID, testUserID),
			preferences:  onePreference(t),
			claimOutcome: notificationdomain.ClaimGranted,
			deliverErr:   notificationdomain.NewTransportFailure(),
			resolveApply: true,
			want:         notificationmessaging.Ack,
			wantAttempts: testMaxAttempts,
		},
		"an outcome that cannot be recorded is acknowledged": {
			eventType:    notificationdomain.EventTypeVideoJobCompleted,
			body:         completedBody(t, testJobID, testUserID),
			preferences:  onePreference(t),
			claimOutcome: notificationdomain.ClaimGranted,
			resolveErr:   errors.New("connection refused"),
			want:         notificationmessaging.Ack,
			wantAttempts: 1,
		},
		"a resolution refused by the fence is acknowledged": {
			eventType:    notificationdomain.EventTypeVideoJobCompleted,
			body:         completedBody(t, testJobID, testUserID),
			preferences:  onePreference(t),
			claimOutcome: notificationdomain.ClaimGranted,
			resolveApply: false,
			want:         notificationmessaging.Ack,
			wantAttempts: 1,
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			deliverer := &stubDeliverer{err: testCase.deliverErr}
			deps := newTestDeps(t,
				&stubPreferences{preferences: testCase.preferences, err: testCase.findErr},
				&stubDeliveries{outcome: testCase.claimOutcome, claimErr: testCase.claimErr, applied: testCase.resolveApply, resolveErr: testCase.resolveErr},
				deliverer)

			got := deps.handle(context.Background(), testCase.eventType, testCase.body)

			if got != testCase.want {
				t.Errorf("disposition = %d, want %d", got, testCase.want)
			}
			if deliverer.attempts != testCase.wantAttempts {
				t.Errorf("%d requests were made, want %d", deliverer.attempts, testCase.wantAttempts)
			}
		})
	}
}

// The ordering shutdown exists to hold: the pool the delivery in hand
// borrows is closed only once that delivery has reached a disposition.
//
// The pause before the ping is what gives this teeth — without it a run that
// closed the pool the moment the signal arrived would still pass, because
// nothing else would have looked at the pool in between.
func TestRun_ClosesThePoolOnlyAfterTheConsumerHasJoined(t *testing.T) {
	deps := &notifierDeps{db: openStubPool(t)}
	ctx, cancel := context.WithCancel(context.Background())

	var openWhileConsuming error
	consuming := make(chan struct{})
	consume := func(consumeCtx context.Context) error {
		close(consuming)
		<-consumeCtx.Done()
		time.Sleep(50 * time.Millisecond)
		openWhileConsuming = deps.db.PingContext(context.Background())
		return nil
	}

	done := make(chan drainOutcome, 1)
	go func() { done <- run(ctx, deps, consume, time.Minute) }()

	<-consuming
	cancel()

	if outcome := <-done; outcome != drainJoined {
		t.Fatalf("outcome = %d, want drainJoined", outcome)
	}
	if openWhileConsuming != nil {
		t.Errorf("the pool was closed while the delivery in hand was still running: %v", openWhileConsuming)
	}
	if err := deps.db.PingContext(context.Background()); err == nil {
		t.Error("the pool is still open after a clean join; nothing else closes it")
	}
}

// The named exception: when the drain expires the handler is still running
// on its detached context and can never be joined, so the bound wins and the
// orderly close is skipped rather than waited on. Closing anyway would abort
// the write recording an outcome that has already been earned.
func TestRun_LeavesThePoolOpenWhenTheDrainExpires(t *testing.T) {
	deps := &notifierDeps{db: openStubPool(t)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	blocked := make(chan struct{})
	defer close(blocked)
	consume := func(context.Context) error {
		<-blocked
		return nil
	}

	outcome := run(ctx, deps, consume, 50*time.Millisecond)

	if outcome != drainExpired {
		t.Fatalf("outcome = %d, want drainExpired", outcome)
	}
	if err := deps.db.PingContext(context.Background()); err != nil {
		t.Errorf("the pool was closed under a delivery still in flight: %v", err)
	}
}

// The drain is arithmetic over the configured budget rather than a constant,
// so lowering a delivery term shortens the wait with it — and it is longer
// than the budget it exists to cover, or it would abandon deliveries that
// were still within it.
func TestDrainTimeout_FollowsTheDeliveryBudget(t *testing.T) {
	deps := &notifierDeps{config: notificationapplication.DefaultDeliveryConfig()}
	hold := deps.config.MaxClaimHold()

	if got := deps.drainTimeout(); got <= hold {
		t.Errorf("drainTimeout() = %s, which does not exceed the %s a claimant may hold a claim", got, hold)
	}

	shorter := *deps
	shorter.config.Timeout = deps.config.Timeout / 2
	if shorter.drainTimeout() >= deps.drainTimeout() {
		t.Errorf("halving the per-attempt timeout did not shorten the drain: %s vs %s",
			shorter.drainTimeout(), deps.drainTimeout())
	}
}

const (
	testJobID       = "550e8400-e29b-41d4-a716-446655440000"
	testUserID      = "user-1"
	testMaxAttempts = 2
)

// newTestDeps wires the real use case to stub ports, under a budget small
// enough to run in a test: the production one waits seconds between attempts
// and again between resolve retries.
func newTestDeps(t *testing.T, preferences notificationdomain.PreferenceRepository, deliveries notificationdomain.DeliveryRepository, deliverer notificationdomain.Deliverer) *notifierDeps {
	t.Helper()

	config := notificationapplication.DeliveryConfig{
		MaxAttempts:           testMaxAttempts,
		Timeout:               time.Second,
		InitialBackoff:        time.Millisecond,
		ResolveMaxAttempts:    testMaxAttempts,
		ResolveTimeout:        time.Second,
		ResolveInitialBackoff: time.Millisecond,
		ReclaimBound:          time.Minute,
	}
	return &notifierDeps{
		config: config,
		deliver: notificationapplication.NewDeliverNotification(
			preferences, deliveries, deliverer, systemClock{}, config, nil),
	}
}

func completedBody(t *testing.T, jobID, userID string) []byte {
	t.Helper()
	return marshal(t, notificationmessaging.JobCompletedMessage{
		Type:       notificationdomain.EventTypeVideoJobCompleted,
		JobID:      jobID,
		UserID:     userID,
		FrameCount: 12,
		StorageKey: "frames_" + jobID + ".zip",
		OccurredAt: time.Now(),
	})
}

// A completion the domain refuses for a reason the transport cannot see: the
// body decodes, and the event still cannot be built.
func completedBodyWithoutStorageKey(t *testing.T) []byte {
	t.Helper()
	return marshal(t, notificationmessaging.JobCompletedMessage{
		Type:       notificationdomain.EventTypeVideoJobCompleted,
		JobID:      testJobID,
		UserID:     testUserID,
		FrameCount: 12,
		OccurredAt: time.Now(),
	})
}

// A completion carrying no type field. The routing key says completion, so
// the parser is the right one and every field the domain checks is valid —
// only the two halves of the message disagree.
func completedBodyWithoutType(t *testing.T) []byte {
	t.Helper()
	return marshal(t, notificationmessaging.JobCompletedMessage{
		JobID:      testJobID,
		UserID:     testUserID,
		FrameCount: 12,
		StorageKey: "frames_" + testJobID + ".zip",
		OccurredAt: time.Now(),
	})
}

func failedBody(t *testing.T, jobID, userID string) []byte {
	t.Helper()
	return marshal(t, notificationmessaging.JobFailedMessage{
		Type:        notificationdomain.EventTypeVideoJobFailed,
		JobID:       jobID,
		UserID:      userID,
		ErrorReason: "ffmpeg exited 1",
		OccurredAt:  time.Now(),
	})
}

func marshal(t *testing.T, message any) []byte {
	t.Helper()
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("marshal test message: %v", err)
	}
	return encoded
}

// onePreference is a webhook preference created before any event in these
// tests occurs, so the enrolment boundary never silently makes a case pass
// by delivering nothing.
func onePreference(t *testing.T) []*notificationdomain.NotificationPreference {
	t.Helper()

	userID, err := notificationdomain.NewUserID(testUserID)
	if err != nil {
		t.Fatalf("new user id: %v", err)
	}
	channel, err := notificationdomain.ParseChannel(notificationdomain.ChannelWebhook)
	if err != nil {
		t.Fatalf("parse channel: %v", err)
	}
	destination, err := notificationdomain.NewDestination("https://receiver.example.com/hook")
	if err != nil {
		t.Fatalf("new destination: %v", err)
	}
	secret, err := notificationdomain.NewSecret("a-secret-long-enough-to-sign-with")
	if err != nil {
		t.Fatalf("new secret: %v", err)
	}

	preferences := make([]*notificationdomain.NotificationPreference, 0, 2)
	for _, raw := range []string{notificationdomain.EventTypeVideoJobCompleted, notificationdomain.EventTypeVideoJobFailed} {
		eventType, err := notificationdomain.ParseEventType(raw)
		if err != nil {
			t.Fatalf("parse event type: %v", err)
		}
		preference, err := notificationdomain.NewNotificationPreference(
			userID, eventType, channel, true, destination, secret, time.Now().Add(-time.Hour))
		if err != nil {
			t.Fatalf("new preference: %v", err)
		}
		preferences = append(preferences, preference)
	}
	return preferences
}

type stubPreferences struct {
	preferences []*notificationdomain.NotificationPreference
	err         error
}

func (s *stubPreferences) Set(context.Context, notificationdomain.PreferenceIntent, time.Time) (notificationdomain.PreferenceView, error) {
	return notificationdomain.PreferenceView{}, errors.New("unused by the notifier")
}

func (s *stubPreferences) ListByUser(context.Context, notificationdomain.UserID) ([]notificationdomain.PreferenceView, error) {
	return nil, errors.New("unused by the notifier")
}

func (s *stubPreferences) FindDeliverable(_ context.Context, _ notificationdomain.UserID, eventType notificationdomain.EventType) ([]*notificationdomain.NotificationPreference, error) {
	if s.err != nil {
		return nil, s.err
	}
	matching := make([]*notificationdomain.NotificationPreference, 0, len(s.preferences))
	for _, preference := range s.preferences {
		if preference.EventType() == eventType {
			matching = append(matching, preference)
		}
	}
	return matching, nil
}

type stubDeliveries struct {
	outcome    notificationdomain.ClaimOutcome
	claimErr   error
	applied    bool
	resolveErr error
}

func (s *stubDeliveries) ClaimDelivery(_ context.Context, identity notificationdomain.DeliveryIdentity, now, _ time.Time) (notificationdomain.Delivery, notificationdomain.ClaimOutcome, error) {
	if s.claimErr != nil {
		return notificationdomain.Delivery{}, 0, s.claimErr
	}
	if s.outcome != notificationdomain.ClaimGranted {
		return notificationdomain.Delivery{}, s.outcome, nil
	}

	deliveryID, err := notificationdomain.NewDeliveryID("11111111-2222-3333-4444-555555555555")
	if err != nil {
		return notificationdomain.Delivery{}, 0, err
	}
	claimToken, err := notificationdomain.NewClaimToken("66666666-7777-8888-9999-000000000000")
	if err != nil {
		return notificationdomain.Delivery{}, 0, err
	}
	delivery, err := notificationdomain.NewClaimedDelivery(deliveryID, identity, claimToken, now)
	if err != nil {
		return notificationdomain.Delivery{}, 0, err
	}
	return delivery, notificationdomain.ClaimGranted, nil
}

func (s *stubDeliveries) ResolveDelivery(context.Context, notificationdomain.DeliveryID, notificationdomain.ClaimToken, notificationdomain.DeliveryStatus, int, string, time.Time) (bool, error) {
	if s.resolveErr != nil {
		return false, s.resolveErr
	}
	return s.applied, nil
}

type stubDeliverer struct {
	err      error
	attempts int
}

func (s *stubDeliverer) Deliver(context.Context, *notificationdomain.NotificationPreference, notificationdomain.TerminalEvent, notificationdomain.DeliveryID) error {
	s.attempts++
	return s.err
}

// openStubPool is a *sql.DB over a driver that connects to nothing, so a
// test can ask whether the pool is open without a database behind it: a
// query on a closed *sql.DB fails whatever the driver is, and this one
// succeeds while it is open.
func openStubPool(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open(stubDriverName, "")
	if err != nil {
		t.Fatalf("open the stub pool: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("the stub pool is not usable before the test starts: %v", err)
	}
	return db
}

const stubDriverName = "notifier-test-stub"

func init() { sql.Register(stubDriverName, stubDriver{}) }

type stubDriver struct{}

func (stubDriver) Open(string) (driver.Conn, error) { return stubConn{}, nil }

type stubConn struct{}

func (stubConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (stubConn) Close() error                        { return nil }
func (stubConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }
