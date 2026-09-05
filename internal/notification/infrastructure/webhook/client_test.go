package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"video-processor/internal/notification/domain"
)

const testSecret = "a-signing-secret-long-enough"

const testTimeout = 2 * time.Second

// testSignedAt is the instant every client test signs at, so a receiver's
// verification is reproducible.
var testSignedAt = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func TestClient_DeliversASignedRequest(t *testing.T) {
	var (
		mu       sync.Mutex
		received *http.Request
		body     []byte
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		read, _ := io.ReadAll(r.Body)
		mu.Lock()
		received = r.Clone(context.Background())
		body = read
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := newTestClient(t, domain.NewDestinationPolicy(true))
	preference := newTestPreference(t, server.URL+"/hook", testSecret)

	if err := client.Deliver(context.Background(), preference, completedEvent(t, 42), deliveryID(t, "delivery-1")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if received == nil {
		t.Fatal("the destination received no request")
	}
	if received.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", received.Method)
	}
	for header, want := range map[string]string{
		"Content-Type":  "application/json",
		"User-Agent":    userAgent,
		headerEvent:     domain.EventTypeVideoJobCompleted,
		headerDelivery:  "delivery-1",
		headerTimestamp: "1788264000",
	} {
		if got := received.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}

	// What a receiver does: recompute the signature over the timestamp
	// header and the bytes it received, using the secret it registered.
	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write([]byte(received.Header.Get(headerTimestamp) + "."))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if got := received.Header.Get(headerSignature); got != want {
		t.Errorf("%s = %q, want %q — a receiver cannot verify what it was sent", headerSignature, got, want)
	}

	var decoded struct {
		Version int    `json:"version"`
		ID      string `json:"id"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("the delivered body is not JSON: %v", err)
	}
	if decoded.Version != payloadVersion || decoded.ID != "delivery-1" {
		t.Errorf("body = %s, want version %d and id delivery-1", body, payloadVersion)
	}

	for header, values := range received.Header {
		for _, value := range values {
			if strings.Contains(value, testSecret) {
				t.Errorf("header %s carries the secret", header)
			}
		}
	}
	if strings.Contains(string(body), testSecret) {
		t.Error("the body carries the secret")
	}
}

func TestClient_ReportsANon2xxAsAnUnexpectedStatus(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusNotFound, http.StatusUnauthorized} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))

		client := newTestClient(t, domain.NewDestinationPolicy(true))
		err := client.Deliver(context.Background(), newTestPreference(t, server.URL, testSecret), completedEvent(t, 1), deliveryID(t, "d"))
		server.Close()

		if !errors.Is(err, domain.NewUnexpectedStatus(status)) {
			t.Errorf("status %d: err = %v, want an unexpected-status failure naming it", status, err)
		}
	}
}

// A 2xx other than 200 is still a success: receivers commonly answer 202 to
// a webhook they have queued rather than processed.
func TestClient_AcceptsEvery2xx(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusAccepted, http.StatusNoContent, 299} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))

		client := newTestClient(t, domain.NewDestinationPolicy(true))
		err := client.Deliver(context.Background(), newTestPreference(t, server.URL, testSecret), completedEvent(t, 1), deliveryID(t, "d"))
		server.Close()

		if err != nil {
			t.Errorf("status %d: unexpected error: %v", status, err)
		}
	}
}

func TestClient_DoesNotFollowARedirect(t *testing.T) {
	var followed atomicCounter
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		followed.add()
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	client := newTestClient(t, domain.NewDestinationPolicy(true))
	err := client.Deliver(context.Background(), newTestPreference(t, redirector.URL, testSecret), completedEvent(t, 1), deliveryID(t, "d"))

	if !errors.Is(err, domain.NewTransportFailure()) {
		t.Errorf("err = %v, want a transport failure", err)
	}
	if got := followed.value(); got != 0 {
		t.Errorf("the redirect target received %d requests, want 0", got)
	}
}

// A destination stored before the policy existed, or before it was
// tightened, is refused at delivery rather than dialled — which is what
// makes the recorded reason for such a row name the policy.
func TestClient_RefusesAStoredDestinationThePolicyNowRefuses(t *testing.T) {
	var reached atomicCounter
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.add()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// The restrictive policy: the plaintext scheme is refused before any
	// connection is opened.
	client := newTestClient(t, domain.NewDestinationPolicy(false))
	err := client.Deliver(context.Background(), newTestPreference(t, server.URL, testSecret), completedEvent(t, 1), deliveryID(t, "d"))

	if !errors.Is(err, domain.NewPolicyRefusal()) {
		t.Errorf("err = %v, want a policy refusal", err)
	}
	if got := reached.value(); got != 0 {
		t.Errorf("the destination received %d requests, want 0", got)
	}
}

// The dial guard itself, over the address string net.Dialer.Control is
// called with. This one never skips, so the property is covered whatever the
// environment's name resolution looks like; the test below it covers the
// wiring end to end.
func TestClient_ControlRefusesAnAddressThePolicyRefuses(t *testing.T) {
	client := newTestClient(t, domain.NewDestinationPolicy(false))

	refused := []string{
		"127.0.0.1:443",          // loopback
		"10.1.2.3:443",           // RFC 1918
		"169.254.169.254:80",     // link-local, the metadata endpoint
		"100.64.0.1:443",         // shared address space, which IsGlobalUnicast accepts
		"198.18.0.1:443",         // benchmarking, likewise
		"[::1]:443",              // IPv6 loopback
		"[2001:db8::1]:443",      // IPv6 documentation
		"[64:ff9b::a01:203]:443", // NAT64 form of 10.1.2.3
		"not-an-address",         // no port at all
		"example.com:443",        // a hostname, which Control is never called with
	}
	for _, address := range refused {
		if err := client.controlDial("tcp", address, nil); !errors.Is(err, domain.ErrDestinationRefused) {
			t.Errorf("controlDial(%q) = %v, want a refusal", address, err)
		}
	}

	for _, address := range []string{"93.184.216.34:443", "[2606:2800:220:1:248:1893:25c8:1946]:443"} {
		if err := client.controlDial("tcp", address, nil); err != nil {
			t.Errorf("controlDial(%q) = %v, want it permitted", address, err)
		}
	}

	// The relaxation reaches the dial guard too, which is what makes the
	// compose stack's private-address receiver deliverable.
	relaxed := newTestClient(t, domain.NewDestinationPolicy(true))
	if err := relaxed.controlDial("tcp", "127.0.0.1:443", nil); err != nil {
		t.Errorf("controlDial under the relaxation = %v, want it permitted", err)
	}
}

// The dial-side policy, reached through a hostname rather than a literal
// address: the write-time check passes because the name is not one it can
// judge, and the refusal comes from the address it resolves to.
//
// The assertion is on the typed policy error rather than on the request
// having failed. A test that passes because the request merely failed proves
// nothing — an unreachable port would satisfy it.
func TestClient_RefusesAHostnameResolvingToARefusedAddress(t *testing.T) {
	policy := domain.NewDestinationPolicy(false)
	name := hostnameResolvingToARefusedAddress(t, policy)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("unexpected error opening a listener: %v", err)
	}
	defer func() { _ = listener.Close() }()

	var accepted atomicCounter
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			accepted.add()
			_ = conn.Close()
		}
	}()

	port := listener.Addr().(*net.TCPAddr).Port
	client := newTestClient(t, policy)
	preference := newTestPreference(t, fmt.Sprintf("https://%s:%d/hook", name, port), testSecret)

	err = client.Deliver(context.Background(), preference, completedEvent(t, 1), deliveryID(t, "d"))
	if !errors.Is(err, domain.NewPolicyRefusal()) {
		t.Errorf("err = %v, want a policy refusal for %s", err, name)
	}
	if got := accepted.value(); got != 0 {
		t.Errorf("the listener accepted %d connections, want 0", got)
	}
}

// hostnameResolvingToARefusedAddress finds a name whose every resolved
// address the policy refuses. Candidates rather than a fixed name because
// the suite runs both inside the Debian-based test image and directly on a
// CI runner, and the two do not carry the same /etc/hosts.
func hostnameResolvingToARefusedAddress(t *testing.T, policy domain.DestinationPolicy) string {
	t.Helper()

	candidates := []string{"ip6-localhost", "ip6-loopback"}
	if host, err := os.Hostname(); err == nil && host != "" && host != "localhost" {
		candidates = append(candidates, host)
	}

	for _, name := range candidates {
		addrs, err := net.DefaultResolver.LookupNetIP(context.Background(), "ip", name)
		if err != nil || len(addrs) == 0 {
			continue
		}
		refused := true
		for _, addr := range addrs {
			if policy.CheckAddr(addr) == nil {
				refused = false
				break
			}
		}
		if refused {
			return name
		}
	}

	t.Skipf("no candidate of %v resolves entirely to addresses the policy refuses; "+
		"TestClient_ControlRefusesAnAddressThePolicyRefuses still covers the guard itself", candidates)
	return ""
}

// The transport's Proxy field is pinned directly, not only behaviourally.
// net/http caches the environment it read for ProxyFromEnvironment in a
// sync.Once, so a behavioural test alone could pass green against the very
// regression it exists to catch.
func TestClient_TransportUsesNoProxy(t *testing.T) {
	client := newTestClient(t, domain.NewDestinationPolicy(true))

	transport, ok := client.http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", client.http.Transport)
	}
	if transport.Proxy != nil {
		t.Error("the transport carries a proxy function: with a proxy in the environment the entire address rule is bypassed")
	}
}

// And behaviourally. The destination is a non-loopback address on purpose:
// ProxyFromEnvironment declines to proxy loopback destinations, so a test
// against an httptest server would pass just as green with the proxy wired
// up and would catch nothing. TEST-NET-3 is unroutable, so with the proxy
// nil the attempt fails at the dial and the proxy listener stays untouched;
// with a proxy configured the connection would go to the proxy instead, and
// that is what this counts.
func TestClient_IgnoresAProxyInTheEnvironment(t *testing.T) {
	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("unexpected error opening the proxy listener: %v", err)
	}
	defer func() { _ = proxy.Close() }()

	var proxied atomicCounter
	go func() {
		for {
			conn, acceptErr := proxy.Accept()
			if acceptErr != nil {
				return
			}
			proxied.add()
			_ = conn.Close()
		}
	}()

	proxyURL := "http://" + proxy.Addr().String()
	for _, variable := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(variable, proxyURL)
	}

	client := NewClient(domain.NewDestinationPolicy(true), 300*time.Millisecond)
	client.now = func() time.Time { return testSignedAt }
	preference := newTestPreference(t, "http://203.0.113.7/hook", testSecret)

	if err := client.Deliver(context.Background(), preference, completedEvent(t, 1), deliveryID(t, "d")); !errors.Is(err, domain.NewTransportFailure()) {
		t.Errorf("err = %v, want a transport failure from the direct dial", err)
	}
	if got := proxied.value(); got != 0 {
		t.Errorf("the proxy accepted %d connections, want 0 — the guarded dial was bypassed", got)
	}
}

// An attempt is bounded in time by the client itself, so a destination that
// accepts a connection and never answers cannot hold the consumer.
func TestClient_BoundsAnAttemptThatNeverAnswers(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	}))
	defer func() {
		close(release)
		server.Close()
	}()

	client := NewClient(domain.NewDestinationPolicy(true), 150*time.Millisecond)
	client.now = func() time.Time { return testSignedAt }

	started := time.Now()
	err := client.Deliver(context.Background(), newTestPreference(t, server.URL, testSecret), completedEvent(t, 1), deliveryID(t, "d"))
	elapsed := time.Since(started)

	if !errors.Is(err, domain.NewTransportFailure()) {
		t.Errorf("err = %v, want a transport failure", err)
	}
	if elapsed > time.Second {
		t.Errorf("the attempt took %s, want it bounded by the configured timeout", elapsed)
	}
}

// A refusal carries a classification and nothing else — in particular not
// the *url.Error the transport produced, which renders the full request URL
// and with it any credential the destination carries in its query string.
func TestClient_ReportsNoTransportTextForACredentialBearingDestination(t *testing.T) {
	const credential = "tok_live_do_not_leak"

	// A closed port on a permitted-looking address: the connection fails at
	// the transport, which is the path that produces a *url.Error.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("unexpected error opening a listener: %v", err)
	}
	address := listener.Addr().String()
	_ = listener.Close()

	client := newTestClient(t, domain.NewDestinationPolicy(true))
	preference := newTestPreference(t, "http://"+address+"/hook?token="+credential, testSecret)

	deliverErr := client.Deliver(context.Background(), preference, completedEvent(t, 1), deliveryID(t, "d"))
	if deliverErr == nil {
		t.Fatal("expected the delivery to fail")
	}
	if strings.Contains(deliverErr.Error(), credential) || strings.Contains(deliverErr.Error(), "token=") {
		t.Errorf("the returned error renders the destination's query string: %v", deliverErr)
	}
	if !errors.Is(deliverErr, domain.NewTransportFailure()) {
		t.Errorf("err = %v, want a transport failure", deliverErr)
	}
}

func TestClient_RefusesANilPreference(t *testing.T) {
	client := newTestClient(t, domain.NewDestinationPolicy(true))
	if err := client.Deliver(context.Background(), nil, completedEvent(t, 1), deliveryID(t, "d")); !errors.Is(err, domain.NewPolicyRefusal()) {
		t.Errorf("err = %v, want a policy refusal", err)
	}
}

// The response body is bounded, which the spec requires of every attempt.
//
// The teeth are in the elapsed time rather than in the verdict: this
// destination sends more than the cap and then holds the response open
// forever. Bounded, the read ends at the cap and the attempt returns at
// once; unbounded, io.Copy waits for an EOF that never comes and only the
// client's own timeout ends it — returning the same nil, seconds later,
// with a connection held the whole time.
func TestClient_BoundsTheResponseBody(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, maxResponseBytes+1))
		if flusher, flushable := w.(http.Flusher); flushable {
			flusher.Flush()
		}
		<-release
	}))
	// Close after the handler is released: the server waits for outstanding
	// requests, and one blocked on a channel would never finish.
	defer server.Close()
	defer close(release)

	client := newTestClient(t, domain.NewDestinationPolicy(true))
	preference := newTestPreference(t, server.URL+"/hook", testSecret)

	start := time.Now()
	err := client.Deliver(context.Background(), preference, completedEvent(t, 1), deliveryID(t, "d"))
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed >= testTimeout/2 {
		t.Errorf("Deliver took %s against a destination still holding the response open, with a %s timeout: the response body is not bounded",
			elapsed, testTimeout)
	}
}

func newTestClient(t *testing.T, policy domain.DestinationPolicy) *Client {
	t.Helper()
	client := NewClient(policy, testTimeout)
	client.now = func() time.Time { return testSignedAt }
	return client
}

func newTestPreference(t *testing.T, destinationURL, rawSecret string) *domain.NotificationPreference {
	t.Helper()

	destination, err := domain.NewDestination(destinationURL)
	if err != nil {
		t.Fatalf("unexpected error building a destination: %v", err)
	}
	eventType, err := domain.ParseEventType(domain.EventTypeVideoJobCompleted)
	if err != nil {
		t.Fatalf("unexpected error parsing the event type: %v", err)
	}
	channel, err := domain.ParseChannel(domain.ChannelWebhook)
	if err != nil {
		t.Fatalf("unexpected error parsing the channel: %v", err)
	}

	preference, err := domain.NewNotificationPreference(
		userID(t, "user-1"), eventType, channel, true, destination, secret(t, rawSecret), testOccurredAt.Add(-time.Hour))
	if err != nil {
		t.Fatalf("unexpected error building a preference: %v", err)
	}
	return preference
}

// atomicCounter is a mutex-guarded counter, so an assertion made on the test
// goroutine reads what an accept loop or a handler wrote.
type atomicCounter struct {
	mu    sync.Mutex
	count int
}

func (c *atomicCounter) add() {
	c.mu.Lock()
	c.count++
	c.mu.Unlock()
}

func (c *atomicCounter) value() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}
