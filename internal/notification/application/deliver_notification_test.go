package application_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"video-processor/internal/notification/application"
	"video-processor/internal/notification/domain"
	"video-processor/internal/notification/infrastructure/webhook"
)

const (
	deliveryTestUser   = "user-delivery"
	deliveryTestJob    = "job-delivery"
	deliveryTestID     = "11111111-1111-1111-1111-111111111111"
	deliveryTestToken  = "22222222-2222-2222-2222-222222222222"
	deliveryTestSecret = "a-signing-secret-long-enough"
)

var (
	deliveryTestNow        = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	deliveryTestOccurredAt = deliveryTestNow.Add(-time.Minute)
	deliveryTestEnrolledAt = deliveryTestOccurredAt.Add(-time.Hour)
)

// fastDeliveryConfig keeps the shape of the real budget — three attempts,
// three resolve attempts, a doubling wait — while making the waits
// immaterial, so the table below exercises the loops rather than the clock.
func fastDeliveryConfig() application.DeliveryConfig {
	config := application.DefaultDeliveryConfig()
	config.Timeout = 200 * time.Millisecond
	config.InitialBackoff = time.Millisecond
	config.ResolveTimeout = 200 * time.Millisecond
	config.ResolveInitialBackoff = time.Millisecond
	return config
}

var errRepository = errors.New("connection refused")

func TestDeliverNotification_Execute(t *testing.T) {
	cases := []struct {
		name string

		// enrolledAt is when the user's preference was created. The zero
		// value means FindDeliverable returns nothing — either the user
		// registered none, or the one they registered is disabled and the
		// query's own filter excluded it.
		enrolledAt time.Time
		findErr    error

		outcome  domain.ClaimOutcome
		claimErr error

		deliverErrs    []error
		resolveErr     error
		resolveApplied bool

		wantDisposition application.DeliveryDisposition
		wantAttempts    int
		wantResolves    int
		wantStatus      string
		wantReason      string
		wantErr         bool
	}{
		{
			// The enrolment boundary, and its edge: a preference created at
			// the same instant the event occurred is not before it.
			name:            "an event predating the preference is not delivered",
			enrolledAt:      deliveryTestOccurredAt.Add(time.Minute),
			wantDisposition: application.DeliveryHandled,
		},
		{
			name:            "a preference created at the instant the event occurred is not delivered",
			enrolledAt:      deliveryTestOccurredAt,
			wantDisposition: application.DeliveryHandled,
		},
		{
			// The enabled filter lives in FindDeliverable's SQL, so what this
			// case documents is the use case's side of it: a disabled
			// preference simply does not come back. The rule itself is
			// enforced by the adapter suite against a real database, not
			// here.
			name:            "a disabled preference is never returned to deliver to",
			wantDisposition: application.DeliveryHandled,
		},
		{
			name:            "no preference at all is handled rather than failed",
			wantDisposition: application.DeliveryHandled,
		},
		{
			name:            "a claim refused as already resolved is handled",
			enrolledAt:      deliveryTestEnrolledAt,
			outcome:         domain.ClaimAlreadyResolved,
			wantDisposition: application.DeliveryHandled,
		},
		{
			name:            "a claim held by another consumer is deferred",
			enrolledAt:      deliveryTestEnrolledAt,
			outcome:         domain.ClaimHeldByAnother,
			wantDisposition: application.DeliveryDeferred,
		},
		{
			name:            "a 2xx on the first attempt is recorded as delivered",
			enrolledAt:      deliveryTestEnrolledAt,
			outcome:         domain.ClaimGranted,
			deliverErrs:     nil,
			resolveApplied:  true,
			wantDisposition: application.DeliveryHandled,
			wantAttempts:    1,
			wantResolves:    1,
			wantStatus:      domain.DeliveryStatusDelivered,
		},
		{
			name:            "a failing endpoint exhausts the budget and is recorded as failed",
			enrolledAt:      deliveryTestEnrolledAt,
			outcome:         domain.ClaimGranted,
			deliverErrs:     []error{domain.NewUnexpectedStatus(500)},
			resolveApplied:  true,
			wantDisposition: application.DeliveryHandled,
			wantAttempts:    application.DefaultDeliveryMaxAttempts,
			wantResolves:    1,
			wantStatus:      domain.DeliveryStatusFailed,
			wantReason:      "unexpected_status: 500",
		},
		{
			name:            "a recovering endpoint stops the budget early",
			enrolledAt:      deliveryTestEnrolledAt,
			outcome:         domain.ClaimGranted,
			deliverErrs:     []error{domain.NewTransportFailure(), nil},
			resolveApplied:  true,
			wantDisposition: application.DeliveryHandled,
			wantAttempts:    2,
			wantResolves:    1,
			wantStatus:      domain.DeliveryStatusDelivered,
		},
		{
			name:            "a policy refusal is recorded with its own reason",
			enrolledAt:      deliveryTestEnrolledAt,
			outcome:         domain.ClaimGranted,
			deliverErrs:     []error{domain.NewPolicyRefusal()},
			resolveApplied:  true,
			wantDisposition: application.DeliveryHandled,
			wantAttempts:    application.DefaultDeliveryMaxAttempts,
			wantResolves:    1,
			wantStatus:      domain.DeliveryStatusFailed,
			wantReason:      "refused_by_policy",
		},
		{
			// FindDeliverable runs before anything can be claimed, so it is
			// on the same side of the boundary the claim is.
			name:            "a failure reading preferences defers",
			findErr:         errRepository,
			wantDisposition: application.DeliveryDeferred,
			wantErr:         true,
		},
		{
			name:            "a failure claiming defers",
			enrolledAt:      deliveryTestEnrolledAt,
			claimErr:        errRepository,
			wantDisposition: application.DeliveryDeferred,
			wantErr:         true,
		},
		{
			// The claim is already held and a request has already been sent,
			// so a redelivery would defer forever and re-send first.
			name:            "a repository failure while resolving is handled after its bounded retries",
			enrolledAt:      deliveryTestEnrolledAt,
			outcome:         domain.ClaimGranted,
			resolveErr:      errRepository,
			wantDisposition: application.DeliveryHandled,
			wantAttempts:    1,
			wantResolves:    application.DefaultResolveMaxAttempts,
			wantStatus:      domain.DeliveryStatusDelivered,
		},
		{
			name:            "a resolve refused by the fence is handled without a retry",
			enrolledAt:      deliveryTestEnrolledAt,
			outcome:         domain.ClaimGranted,
			resolveApplied:  false,
			wantDisposition: application.DeliveryHandled,
			wantAttempts:    1,
			wantResolves:    1,
			wantStatus:      domain.DeliveryStatusDelivered,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var preferences []*domain.NotificationPreference
			if !testCase.enrolledAt.IsZero() {
				preferences = []*domain.NotificationPreference{
					mustPreference(t, "https://receiver.example/hook", testCase.enrolledAt),
				}
			}

			repository := &fakeDeliverablePreferences{preferences: preferences, err: testCase.findErr}
			deliveries := &fakeDeliveryRepository{
				outcome:        testCase.outcome,
				claimErr:       testCase.claimErr,
				delivery:       mustGrantedDelivery(t),
				resolveErr:     testCase.resolveErr,
				resolveApplied: testCase.resolveApplied,
			}
			deliverer := &fakeDeliverer{errs: testCase.deliverErrs}

			useCase := application.NewDeliverNotification(
				repository, deliveries, deliverer, fakeClock{now: deliveryTestNow}, fastDeliveryConfig(), discardLogger())

			disposition, err := useCase.Execute(context.Background(), mustCompletedEvent(t, deliveryTestOccurredAt))

			if disposition != testCase.wantDisposition {
				t.Errorf("disposition = %v, want %v", disposition, testCase.wantDisposition)
			}
			if testCase.wantErr != (err != nil) {
				t.Errorf("err = %v, want an error: %t", err, testCase.wantErr)
			}
			if got := deliverer.attempts(); got != testCase.wantAttempts {
				t.Errorf("delivery attempts = %d, want %d", got, testCase.wantAttempts)
			}

			resolves := deliveries.resolveCalls()
			if len(resolves) != testCase.wantResolves {
				t.Fatalf("resolve calls = %d, want %d", len(resolves), testCase.wantResolves)
			}
			if testCase.wantStatus != "" {
				last := resolves[len(resolves)-1]
				if last.status.String() != testCase.wantStatus {
					t.Errorf("recorded status = %s, want %s", last.status, testCase.wantStatus)
				}
				if last.attempts != testCase.wantAttempts {
					t.Errorf("recorded attempts = %d, want %d", last.attempts, testCase.wantAttempts)
				}
				if last.deliveryID.String() != deliveryTestID || last.claimToken.String() != deliveryTestToken {
					t.Errorf("the outcome was written against %s/%s, want the claimed pair", last.deliveryID, last.claimToken)
				}
				if testCase.wantReason == "" && last.reason != "" {
					t.Errorf("recorded reason = %q, want it empty for a success", last.reason)
				}
				if testCase.wantReason != "" && !strings.Contains(last.reason, testCase.wantReason) {
					t.Errorf("recorded reason = %q, want it to name %q", last.reason, testCase.wantReason)
				}
			}

			// Every attempt announces the same delivery, so a receiver
			// deduplicating on the identifier treats a retry as the same
			// logical delivery rather than as a new one.
			for _, announced := range deliverer.announcedDeliveryIDs() {
				if announced != deliveryTestID {
					t.Errorf("an attempt announced delivery %s, want %s", announced, deliveryTestID)
				}
			}
		})
	}
}

// A preference disabled and re-enabled before the event is handled is
// delivered to: enabled is a routing switch read at handling time, not a
// record of what was true when the outcome occurred.
func TestDeliverNotification_DeliversToAPreferenceReEnabledBeforeHandling(t *testing.T) {
	preference := mustPreference(t, "https://receiver.example/hook", deliveryTestEnrolledAt)
	repository := &fakeDeliverablePreferences{preferences: []*domain.NotificationPreference{preference}}
	deliveries := &fakeDeliveryRepository{outcome: domain.ClaimGranted, delivery: mustGrantedDelivery(t), resolveApplied: true}
	deliverer := &fakeDeliverer{}

	useCase := application.NewDeliverNotification(
		repository, deliveries, deliverer, fakeClock{now: deliveryTestNow}, fastDeliveryConfig(), discardLogger())

	disposition, err := useCase.Execute(context.Background(), mustCompletedEvent(t, deliveryTestOccurredAt))
	if err != nil || disposition != application.DeliveryHandled {
		t.Fatalf("disposition = %v, err = %v", disposition, err)
	}
	if deliverer.attempts() != 1 {
		t.Errorf("delivery attempts = %d, want 1", deliverer.attempts())
	}
	if !preference.Enabled() {
		t.Fatal("the fixture is disabled; this test is written against an enabled preference")
	}
}

// The claim is asked for against the configured reclaim bound, measured back
// from the clock the use case reads. Getting this wrong in either direction
// is what the startup validator exists to prevent, so it is pinned here too.
func TestDeliverNotification_ClaimsAgainstTheConfiguredReclaimBound(t *testing.T) {
	repository := &fakeDeliverablePreferences{
		preferences: []*domain.NotificationPreference{mustPreference(t, "https://receiver.example/hook", deliveryTestEnrolledAt)},
	}
	deliveries := &fakeDeliveryRepository{outcome: domain.ClaimGranted, delivery: mustGrantedDelivery(t), resolveApplied: true}

	config := fastDeliveryConfig()
	useCase := application.NewDeliverNotification(
		repository, deliveries, &fakeDeliverer{}, fakeClock{now: deliveryTestNow}, config, discardLogger())

	if _, err := useCase.Execute(context.Background(), mustCompletedEvent(t, deliveryTestOccurredAt)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !deliveries.lastNow.Equal(deliveryTestNow) {
		t.Errorf("claim now = %s, want %s", deliveries.lastNow, deliveryTestNow)
	}
	if want := deliveryTestNow.Add(-config.ReclaimBound); !deliveries.lastStaleBefore.Equal(want) {
		t.Errorf("claim staleBefore = %s, want %s", deliveries.lastStaleBefore, want)
	}
	if repository.lastUserID.String() != deliveryTestUser {
		t.Errorf("preferences were read for %s, want %s", repository.lastUserID, deliveryTestUser)
	}
	if repository.lastEventType.String() != domain.EventTypeVideoJobCompleted {
		t.Errorf("preferences were read for %s, want %s", repository.lastEventType, domain.EventTypeVideoJobCompleted)
	}
}

// A full delivery through the real signer and the real transport: the log
// output names the triple and the delivery, and carries neither the secret
// it signed with nor the body it sent.
func TestDeliverNotification_LogsNeitherTheSecretNorTheBody(t *testing.T) {
	// Buffered by one and read after Execute returns: a single Read can
	// stop short of the whole body, which would leave the assertions below
	// searching the log for a fragment and passing for the wrong reason.
	received := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		read, _ := io.ReadAll(r.Body)
		received <- read
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	logs := &bytes.Buffer{}
	useCase := application.NewDeliverNotification(
		&fakeDeliverablePreferences{
			preferences: []*domain.NotificationPreference{mustPreference(t, server.URL+"/hook", deliveryTestEnrolledAt)},
		},
		&fakeDeliveryRepository{outcome: domain.ClaimGranted, delivery: mustGrantedDelivery(t), resolveApplied: true},
		webhook.NewClient(domain.NewDestinationPolicy(true), time.Second),
		fakeClock{now: deliveryTestNow},
		fastDeliveryConfig(),
		log.New(logs, "", 0),
	)

	if _, err := useCase.Execute(context.Background(), mustCompletedEvent(t, deliveryTestOccurredAt)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var body []byte
	select {
	case body = <-received:
	default:
	}
	if len(body) == 0 {
		t.Fatal("the destination received no body; this test would then be asserting on nothing")
	}

	output := logs.String()
	if !strings.Contains(output, deliveryTestID) || !strings.Contains(output, domain.DeliveryStatusDelivered) {
		t.Errorf("log output does not record the delivery: %q", output)
	}
	if strings.Contains(output, deliveryTestSecret) {
		t.Error("log output contains the signing secret")
	}
	if strings.Contains(output, string(body)) || strings.Contains(output, `"data"`) || strings.Contains(output, "version") {
		t.Errorf("log output contains the request body: %q", output)
	}
	if strings.Contains(output, server.URL) {
		t.Errorf("log output contains the destination: %q", output)
	}
}

// A destination carrying a credential in its query string, failing at the
// transport on every attempt. Go wraps such a failure in a *url.Error, whose
// Error() renders the full request URL — so the credential reaches neither
// the stored reason nor the log only because both are built from a
// classified error of ours.
func TestDeliverNotification_ACredentialInTheQueryReachesNeitherTheRecordNorTheLog(t *testing.T) {
	const credential = "tok_live_do_not_leak"

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("unexpected error opening a listener: %v", err)
	}
	address := listener.Addr().String()
	_ = listener.Close()

	destination := "http://" + address + "/hook?token=" + credential
	deliveries := &fakeDeliveryRepository{outcome: domain.ClaimGranted, delivery: mustGrantedDelivery(t), resolveApplied: true}

	logs := &bytes.Buffer{}
	useCase := application.NewDeliverNotification(
		&fakeDeliverablePreferences{
			preferences: []*domain.NotificationPreference{mustPreference(t, destination, deliveryTestEnrolledAt)},
		},
		deliveries,
		webhook.NewClient(domain.NewDestinationPolicy(true), 200*time.Millisecond),
		fakeClock{now: deliveryTestNow},
		fastDeliveryConfig(),
		log.New(logs, "", 0),
	)

	disposition, err := useCase.Execute(context.Background(), mustCompletedEvent(t, deliveryTestOccurredAt))
	if err != nil || disposition != application.DeliveryHandled {
		t.Fatalf("disposition = %v, err = %v", disposition, err)
	}

	resolves := deliveries.resolveCalls()
	if len(resolves) != 1 {
		t.Fatalf("resolve calls = %d, want 1", len(resolves))
	}
	recorded := resolves[0]
	if recorded.status.String() != domain.DeliveryStatusFailed {
		t.Errorf("recorded status = %s, want failed", recorded.status)
	}
	if recorded.reason != "notification: delivery failed (transport_failure)" {
		t.Errorf("recorded reason = %q, want the classified transport failure", recorded.reason)
	}

	output := logs.String()
	for name, text := range map[string]string{"the stored reason": recorded.reason, "the log output": output} {
		if strings.Contains(text, credential) {
			t.Errorf("%s contains the credential: %q", name, text)
		}
		if strings.Contains(text, "token=") || strings.Contains(text, address) {
			t.Errorf("%s contains the destination: %q", name, text)
		}
	}
}

func discardLogger() *log.Logger {
	return log.New(&bytes.Buffer{}, "", 0)
}
