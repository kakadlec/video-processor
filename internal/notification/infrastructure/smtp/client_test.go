package smtp

import (
	"context"
	"encoding/base64"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"video-processor/internal/notification/domain"
)

const (
	testTimeout   = 2 * time.Second
	testFrom      = "notifier@fiapx.test"
	testRecipient = "user@example.test"
	testSecret    = "a-signing-secret-long-enough"
)

var (
	testSentAt     = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	testOccurredAt = time.Date(2026, 9, 1, 11, 30, 0, 0, time.UTC)
)

func newTestClient(t *testing.T, addr string, credentials bool) *Client {
	t.Helper()

	config := Config{Addr: addr, From: testFrom}
	if credentials {
		config.Username = "relay-user"
		config.Password = "relay-password"
	}

	client := NewClient(config, testTimeout)
	client.now = func() time.Time { return testSentAt }
	return client
}

func newTestPreference(t *testing.T, address string, rawSecret string) *domain.NotificationPreference {
	t.Helper()

	channel, err := domain.ParseChannel(domain.ChannelEmail)
	if err != nil {
		t.Fatalf("unexpected error parsing the channel: %v", err)
	}
	destination, err := domain.NewDestinationFor(channel, address)
	if err != nil {
		t.Fatalf("unexpected error building a destination: %v", err)
	}
	eventType, err := domain.ParseEventType(domain.EventTypeVideoJobCompleted)
	if err != nil {
		t.Fatalf("unexpected error parsing the event type: %v", err)
	}

	var secret domain.Secret
	if rawSecret != "" {
		secret, err = domain.NewSecret(rawSecret)
		if err != nil {
			t.Fatalf("unexpected error building a secret: %v", err)
		}
	}

	preference, err := domain.NewNotificationPreference(
		userID(t, "user-1"), eventType, channel, true, destination, secret, testOccurredAt.Add(-time.Hour))
	if err != nil {
		t.Fatalf("unexpected error building a preference: %v", err)
	}
	return preference
}

func userID(t *testing.T, value string) domain.UserID {
	t.Helper()
	id, err := domain.NewUserID(value)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return id
}

func jobID(t *testing.T, value string) domain.JobID {
	t.Helper()
	id, err := domain.NewJobID(value)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return id
}

func deliveryID(t *testing.T, value string) domain.DeliveryID {
	t.Helper()
	id, err := domain.NewDeliveryID(value)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return id
}

func completedEvent(t *testing.T, frames int) domain.TerminalEvent {
	t.Helper()
	event, err := domain.NewCompletedEvent(
		jobID(t, "job-1"), userID(t, "user-1"), testOccurredAt, frames, "frames_job-1.zip")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return event
}

func failedEvent(t *testing.T, reason string) domain.TerminalEvent {
	t.Helper()
	event, err := domain.NewFailedEvent(jobID(t, "job-1"), userID(t, "user-1"), testOccurredAt, reason)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return event
}

// decodedBody returns the base64 body of the message the relay accepted.
func decodedBody(t *testing.T, message string) string {
	t.Helper()

	_, encoded, found := strings.Cut(message, "\r\n\r\n")
	if !found {
		t.Fatalf("the message has no header/body separator:\n%s", message)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(strings.TrimSpace(encoded), "\r\n", ""))
	if err != nil {
		t.Fatalf("the body is not decodable base64: %v", err)
	}
	return string(decoded)
}

func TestClient_DeliversAMessage(t *testing.T) {
	relay := startFakeRelay(t, &fakeRelay{})
	client := newTestClient(t, relay.addr, false)

	err := client.Deliver(context.Background(),
		newTestPreference(t, testRecipient, ""), completedEvent(t, 42), deliveryID(t, "delivery-1"))
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}

	commands := relay.commands()
	if !strings.Contains(commands, "MAIL FROM:<"+testFrom+">") {
		t.Errorf("the envelope sender was not %q:\n%s", testFrom, commands)
	}
	if !strings.Contains(commands, "RCPT TO:<"+testRecipient+">") {
		t.Errorf("the envelope recipient was not %q:\n%s", testRecipient, commands)
	}

	message := relay.message()
	if message == "" {
		t.Fatal("the relay accepted no message")
	}
	for _, header := range []string{
		"From: " + testFrom,
		"To: " + testRecipient,
		"Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: base64",
		"Message-ID: <delivery-1@fiapx.test>",
		"Date: " + testSentAt.Format(time.RFC1123Z),
	} {
		if !strings.Contains(message, header) {
			t.Errorf("the message is missing %q:\n%s", header, message)
		}
	}

	body := decodedBody(t, message)
	for _, want := range []string{domain.EventTypeVideoJobCompleted, "job-1", "42", "frames_job-1.zip"} {
		if !strings.Contains(body, want) {
			t.Errorf("the body does not name %q:\n%s", want, body)
		}
	}
}

// The message carries no signature, because signing is the other channel's
// mechanism, and it does not repeat the recipient's own address into the
// body — they know it, and the delivery record deliberately does not store
// one either.
func TestClient_TheMessageIsUnsignedAndCarriesNoCredentialOrRecipient(t *testing.T) {
	relay := startFakeRelay(t, &fakeRelay{})
	client := newTestClient(t, relay.addr, false)

	// A preference that does carry a secret, so this shows the client
	// declined to use one rather than that none was available.
	err := client.Deliver(context.Background(),
		newTestPreference(t, testRecipient, testSecret), completedEvent(t, 1), deliveryID(t, "delivery-1"))
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}

	message := relay.message()
	if strings.Contains(strings.ToLower(message), "signature") {
		t.Errorf("the message carries a signature header:\n%s", message)
	}
	if strings.Contains(message, testSecret) {
		t.Fatal("the message carries the preference's secret")
	}
	if strings.Contains(decodedBody(t, message), testRecipient) {
		t.Error("the body repeats the recipient's own address")
	}
}

func TestClient_TheFailureMessageNamesTheReason(t *testing.T) {
	relay := startFakeRelay(t, &fakeRelay{})
	client := newTestClient(t, relay.addr, false)

	err := client.Deliver(context.Background(),
		newTestPreference(t, testRecipient, ""), failedEvent(t, "ffmpeg exited with status 1"), deliveryID(t, "delivery-1"))
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}

	body := decodedBody(t, relay.message())
	if !strings.Contains(body, "ffmpeg exited with status 1") {
		t.Errorf("the body does not name the failure reason:\n%s", body)
	}
	if !strings.Contains(body, domain.EventTypeVideoJobFailed) {
		t.Errorf("the body does not name the event type:\n%s", body)
	}
}

// A reason carrying a byte outside US-ASCII, or a line longer than SMTP's
// 1000-octet limit, is text this system generated but did not constrain.
// Encoding the body is what makes either deliverable rather than a message
// the relay may reject or mangle.
func TestClient_ABodyWithAwkwardTextSurvivesEncoding(t *testing.T) {
	relay := startFakeRelay(t, &fakeRelay{})
	client := newTestClient(t, relay.addr, false)

	reason := "codificação falhou: " + strings.Repeat("x", 2000)
	err := client.Deliver(context.Background(),
		newTestPreference(t, testRecipient, ""), failedEvent(t, reason), deliveryID(t, "delivery-1"))
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}

	message := relay.message()
	for _, line := range strings.Split(message, "\r\n") {
		if len(line) > 998 {
			t.Fatalf("a message line is %d octets, beyond what SMTP requires a relay to accept", len(line))
		}
	}
	if !strings.Contains(decodedBody(t, message), reason) {
		t.Error("the reason did not survive the round trip")
	}
}

func TestClient_ClassifiesARefusedRecipientByItsReplyCode(t *testing.T) {
	// The message text quotes the recipient back, which is exactly what a
	// real relay does and exactly what must not be recorded.
	relay := startFakeRelay(t, &fakeRelay{rcpt: "550 5.1.1 <" + testRecipient + ">: recipient rejected"})
	client := newTestClient(t, relay.addr, false)

	err := client.Deliver(context.Background(),
		newTestPreference(t, testRecipient, ""), completedEvent(t, 1), deliveryID(t, "delivery-1"))
	if err == nil {
		t.Fatal("Deliver() succeeded against a relay that refused the recipient")
	}

	var deliveryErr *domain.DeliveryError
	if !errors.As(err, &deliveryErr) {
		t.Fatalf("Deliver() error = %T, want *domain.DeliveryError", err)
	}
	if deliveryErr.Kind() != domain.DeliveryUnexpectedStatus {
		t.Errorf("kind = %v, want unexpected_status", deliveryErr.Kind())
	}
	if deliveryErr.StatusCode() != 550 {
		t.Errorf("status code = %d, want 550", deliveryErr.StatusCode())
	}

	// The recorded reason is the error's own rendering, and this is the rule
	// that keeps a user's address out of the delivery table and the logs.
	if strings.Contains(err.Error(), testRecipient) {
		t.Errorf("the recorded reason names the recipient: %q", err.Error())
	}
	if strings.Contains(err.Error(), "recipient rejected") {
		t.Errorf("the recorded reason carries the relay's own text: %q", err.Error())
	}
}

func TestClient_ClassifiesARefusedMessageByItsReplyCode(t *testing.T) {
	relay := startFakeRelay(t, &fakeRelay{data: "552 5.3.4 message too large"})
	client := newTestClient(t, relay.addr, false)

	err := client.Deliver(context.Background(),
		newTestPreference(t, testRecipient, ""), completedEvent(t, 1), deliveryID(t, "delivery-1"))

	var deliveryErr *domain.DeliveryError
	if !errors.As(err, &deliveryErr) {
		t.Fatalf("Deliver() error = %v (%T), want *domain.DeliveryError", err, err)
	}
	if deliveryErr.StatusCode() != 552 {
		t.Errorf("status code = %d, want 552", deliveryErr.StatusCode())
	}
	if strings.Contains(err.Error(), "too large") {
		t.Errorf("the recorded reason carries the relay's own text: %q", err.Error())
	}
}

func TestClient_ReportsAnUnreachableRelayAsATransportFailure(t *testing.T) {
	// Bound to a port nothing listens on: the listener is closed before the
	// attempt, so the address is valid and the connection is refused.
	relay := startFakeRelay(t, &fakeRelay{})
	_ = relay.listener.Close()

	client := newTestClient(t, relay.addr, false)
	err := client.Deliver(context.Background(),
		newTestPreference(t, testRecipient, ""), completedEvent(t, 1), deliveryID(t, "delivery-1"))

	var deliveryErr *domain.DeliveryError
	if !errors.As(err, &deliveryErr) {
		t.Fatalf("Deliver() error = %v (%T), want *domain.DeliveryError", err, err)
	}
	if deliveryErr.Kind() != domain.DeliveryTransportFailure {
		t.Errorf("kind = %v, want transport_failure", deliveryErr.Kind())
	}
}

// The case the connection deadline exists for, and the one a dialer timeout
// alone does not cover: the handshake completes, so the dial has already
// returned, and then nothing is ever sent. Without a deadline on the
// connection this attempt would block past the per-attempt timeout, past
// MaxClaimHold, and past the reclaim bound validated against it.
func TestClient_BoundsAConversationThatNeverAnswers(t *testing.T) {
	relay := startFakeRelay(t, &fakeRelay{silent: true})
	client := newTestClient(t, relay.addr, false)

	started := time.Now()
	err := client.Deliver(context.Background(),
		newTestPreference(t, testRecipient, ""), completedEvent(t, 1), deliveryID(t, "delivery-1"))
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("Deliver() succeeded against a relay that never answered")
	}
	var deliveryErr *domain.DeliveryError
	if !errors.As(err, &deliveryErr) {
		t.Fatalf("Deliver() error = %v (%T), want *domain.DeliveryError", err, err)
	}
	if elapsed > 2*testTimeout {
		t.Fatalf("the attempt took %s, beyond the %s bound — the conversation is not deadlined", elapsed, testTimeout)
	}
}

// Cancellation has to reach a blocked read too, and the only thing that
// reaches one is closing the descriptor.
func TestClient_ACancelledContextEndsABlockedConversation(t *testing.T) {
	relay := startFakeRelay(t, &fakeRelay{silent: true})
	client := newTestClient(t, relay.addr, false)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	started := time.Now()
	err := client.Deliver(ctx,
		newTestPreference(t, testRecipient, ""), completedEvent(t, 1), deliveryID(t, "delivery-1"))
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("Deliver() succeeded after its context was cancelled")
	}
	if elapsed >= testTimeout {
		t.Fatalf("the attempt took %s; cancellation did not end the blocked read", elapsed)
	}
}

// A relay offering no encryption is one this process declines to
// authenticate to. The credential is this deployment's, not a user's, so the
// rule is not the destination policy's and the switch that relaxes that
// policy deliberately does not reach it.
func TestClient_RefusesToAuthenticateOverAnUnencryptedSession(t *testing.T) {
	relay := startFakeRelay(t, &fakeRelay{})
	client := newTestClient(t, relay.addr, true)

	err := client.Deliver(context.Background(),
		newTestPreference(t, testRecipient, ""), completedEvent(t, 1), deliveryID(t, "delivery-1"))

	var deliveryErr *domain.DeliveryError
	if !errors.As(err, &deliveryErr) {
		t.Fatalf("Deliver() error = %v (%T), want *domain.DeliveryError", err, err)
	}
	if deliveryErr.Kind() != domain.DeliveryRefusedByPolicy {
		t.Fatalf("kind = %v, want refused_by_policy", deliveryErr.Kind())
	}
	if strings.Contains(relay.commands(), "AUTH") {
		t.Fatal("the client sent AUTH over an unencrypted session")
	}
	if strings.Contains(relay.commands(), "relay-password") {
		t.Fatal("the client sent its credential over an unencrypted session")
	}
}

// Without credentials an unencrypted session is fine, which is what makes
// the local compose stack work with no configuration of its own.
func TestClient_SendsOverAnUnencryptedSessionWithoutCredentials(t *testing.T) {
	relay := startFakeRelay(t, &fakeRelay{})
	client := newTestClient(t, relay.addr, false)

	if err := client.Deliver(context.Background(),
		newTestPreference(t, testRecipient, ""), completedEvent(t, 1), deliveryID(t, "delivery-1")); err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	if strings.Contains(relay.commands(), "AUTH") {
		t.Error("the client authenticated with no credentials configured")
	}
}

// STARTTLS is attempted whenever the relay advertises it, credentials or
// not. The fake abandons the connection immediately afterwards, so what this
// shows is that the client asked.
func TestClient_UpgradesWhenTheRelayAdvertisesSTARTTLS(t *testing.T) {
	relay := startFakeRelay(t, &fakeRelay{advertiseSTARTTLS: true})
	client := newTestClient(t, relay.addr, false)

	err := client.Deliver(context.Background(),
		newTestPreference(t, testRecipient, ""), completedEvent(t, 1), deliveryID(t, "delivery-1"))
	if err == nil {
		t.Fatal("Deliver() succeeded against a relay that abandoned the TLS handshake")
	}
	if !strings.Contains(relay.commands(), "STARTTLS") {
		t.Fatalf("the client did not attempt STARTTLS:\n%s", relay.commands())
	}
}

func TestClient_RefusesANilPreference(t *testing.T) {
	relay := startFakeRelay(t, &fakeRelay{})
	client := newTestClient(t, relay.addr, false)

	err := client.Deliver(context.Background(), nil, completedEvent(t, 1), deliveryID(t, "delivery-1"))

	var deliveryErr *domain.DeliveryError
	if !errors.As(err, &deliveryErr) {
		t.Fatalf("Deliver() error = %v (%T), want *domain.DeliveryError", err, err)
	}
	if deliveryErr.Kind() != domain.DeliveryRefusedByPolicy {
		t.Errorf("kind = %v, want refused_by_policy", deliveryErr.Kind())
	}
}

// A source-level assertion, and it has to be. A behavioural test can observe
// what a message contained; it cannot observe that a value was never read,
// and "this adapter never reaches for the signing secret" is a claim about
// exactly that. The webhook package makes the mirror-image claim about its
// signer being the only caller that does.
func TestNothingInThisPackageReadsTheSigningSecret(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("unexpected error reading package directory: %v", err)
	}

	fset := token.NewFileSet()
	var checked int
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if _, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution); err != nil {
			t.Fatalf("unexpected error parsing %s: %v", name, err)
		}

		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("unexpected error reading %s: %v", name, err)
		}
		checked++
		for _, forbidden := range []string{".Secret()", ".Reveal()"} {
			if strings.Contains(string(source), forbidden) {
				t.Errorf("%s calls %s; this adapter signs nothing and must not read a secret", name, forbidden)
			}
		}
	}

	if checked == 0 {
		t.Fatal("no sources were checked; the scan is not reaching this package")
	}
}

// The credentialed path, end to end and over a verified session: the client
// upgrades, authenticates only after the upgrade, and the message is
// accepted. Without this the suite covered only the refusal, so a regression
// that broke authentication after a successful STARTTLS would have stayed
// green.
func TestClient_AuthenticatesOnlyAfterUpgradingAndDelivers(t *testing.T) {
	relay, roots := newTLSRelay(t)
	client := newTestClient(t, relay.addr, true)
	client.rootCAs = roots

	err := client.Deliver(context.Background(),
		newTestPreference(t, testRecipient, ""), completedEvent(t, 7), deliveryID(t, "delivery-1"))
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}

	if !relay.sawUpgrade() {
		t.Fatal("the conversation did not run over TLS")
	}
	if !relay.sawAuthAfterUpgrade() {
		t.Fatal("AUTH did not arrive after the upgrade")
	}
	if relay.message() == "" {
		t.Fatal("the relay accepted no message")
	}
	if !strings.Contains(decodedBody(t, relay.message()), "job-1") {
		t.Error("the delivered body does not name the job")
	}
}

// The upgrade is verified, not merely attempted: a relay whose certificate
// this client does not trust fails the attempt rather than falling back to
// plaintext.
func TestClient_RefusesARelayItCannotVerify(t *testing.T) {
	relay, _ := newTLSRelay(t)
	client := newTestClient(t, relay.addr, true)
	// No roots configured, so the generated certificate is untrusted.

	err := client.Deliver(context.Background(),
		newTestPreference(t, testRecipient, ""), completedEvent(t, 1), deliveryID(t, "delivery-1"))
	if err == nil {
		t.Fatal("Deliver() succeeded against a relay it could not verify")
	}
	if relay.message() != "" {
		t.Fatal("a message was delivered over an unverified session")
	}
}

// A job identifier arrives inside a consumed message and its value object
// enforces only non-emptiness, deliberately. A payload carrying CRLF there
// would otherwise end the header block and let its author write headers of
// their own, so the message builder refuses before a byte is sent.
func TestClient_RefusesAJobIdentifierThatCannotGoInAHeader(t *testing.T) {
	relay := startFakeRelay(t, &fakeRelay{})
	client := newTestClient(t, relay.addr, false)

	jobID, err := domain.NewJobID("job-1\r\nBcc: someone@elsewhere.test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	event, err := domain.NewCompletedEvent(jobID, userID(t, "user-1"), testOccurredAt, 1, "frames.zip")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = client.Deliver(context.Background(), newTestPreference(t, testRecipient, ""), event, deliveryID(t, "delivery-1"))

	var deliveryErr *domain.DeliveryError
	if !errors.As(err, &deliveryErr) {
		t.Fatalf("Deliver() error = %v (%T), want *domain.DeliveryError", err, err)
	}
	if deliveryErr.Kind() != domain.DeliveryRefusedByPolicy {
		t.Errorf("kind = %v, want refused_by_policy", deliveryErr.Kind())
	}
	if relay.message() != "" {
		t.Fatalf("a message was sent with an injected header:\n%s", relay.message())
	}
	if strings.Contains(err.Error(), "Bcc") {
		t.Errorf("the recorded reason echoes the offending value: %q", err.Error())
	}
}

// One deadline for the whole attempt, taken before the dial. Timing the dial
// and then starting a fresh timeout would let an attempt run to nearly twice
// the configured bound, and this type's contract — the one MaxClaimHold is
// arithmetic over — is a bound on the attempt, not on each of its halves.
func TestClient_TheAttemptBoundCoversTheDialAndTheConversationTogether(t *testing.T) {
	relay := startFakeRelay(t, &fakeRelay{silent: true})
	client := newTestClient(t, relay.addr, false)

	started := time.Now()
	if err := client.Deliver(context.Background(),
		newTestPreference(t, testRecipient, ""), completedEvent(t, 1), deliveryID(t, "delivery-1")); err == nil {
		t.Fatal("Deliver() succeeded against a relay that never answered")
	}

	// A generous ceiling, because what this rules out is the doubling: a
	// per-half timeout would allow up to 2x here.
	if elapsed := time.Since(started); elapsed > testTimeout+testTimeout/2 {
		t.Fatalf("the attempt took %s, beyond the %s whole-attempt bound", elapsed, testTimeout)
	}
}
