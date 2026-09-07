package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	notificationapplication "video-processor/internal/notification/application"
	notificationdomain "video-processor/internal/notification/domain"
	notificationpostgres "video-processor/internal/notification/infrastructure/postgres"
)

// inMemoryPreferenceRepository is a fake notificationdomain.PreferenceRepository
// so these HTTP tests don't need a live PostgreSQL instance.
//
// This is why main_test.go's TestMain gates on nothing: no test in this
// package opens a pool or reaches Redis, so a NOTIFICATION_POSTGRES_TEST_DSN
// or REDIS_ADDR gate would guard a prerequisite nothing here uses. Two
// reasons decided it. First, the precedent: no test here calls
// setupNotification against a live database — every one builds the module by
// hand and hands it to setupRouter. Second, a real pool would be a flake:
// internal/notification/infrastructure/postgres's own testDB TRUNCATEs
// notification_preferences on every call, `go test ./...` runs packages in
// parallel, and both would point at the same database, so that TRUNCATE
// would wipe rows out from under a route test mid-assertion.
type inMemoryPreferenceRepository struct {
	mu    sync.Mutex
	byKey map[string]notificationdomain.PreferenceView
}

func newInMemoryPreferenceRepository() *inMemoryPreferenceRepository {
	return &inMemoryPreferenceRepository{byKey: make(map[string]notificationdomain.PreferenceView)}
}

func preferenceKey(userID notificationdomain.UserID, eventType notificationdomain.EventType, channel notificationdomain.Channel) string {
	return userID.String() + "|" + eventType.String() + "|" + channel.String()
}

func (r *inMemoryPreferenceRepository) Set(_ context.Context, intent notificationdomain.PreferenceIntent, now time.Time) (notificationdomain.PreferenceView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := preferenceKey(intent.UserID(), intent.EventType(), intent.Channel())
	existing, found := r.byKey[key]
	_, submittedSecret := intent.Secret()

	// The adapter enforces this by whether its update statement affected a
	// row; the fake reproduces the observable outcome so the handler's
	// mapping of ErrSecretRequired to 400 is exercised end to end.
	if !submittedSecret && !found {
		return notificationdomain.PreferenceView{}, notificationdomain.ErrSecretRequired
	}

	view := notificationdomain.PreferenceView{
		UserID:      intent.UserID(),
		EventType:   intent.EventType(),
		Channel:     intent.Channel(),
		Enabled:     intent.Enabled(),
		Destination: intent.Destination(),
		HasSecret:   submittedSecret || existing.HasSecret,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if found {
		view.CreatedAt = existing.CreatedAt
	}
	r.byKey[key] = view
	return view, nil
}

func (r *inMemoryPreferenceRepository) ListByUser(_ context.Context, userID notificationdomain.UserID) ([]notificationdomain.PreferenceView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	views := make([]notificationdomain.PreferenceView, 0)
	for _, view := range r.byKey {
		if view.UserID.Equal(userID) {
			views = append(views, view)
		}
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].EventType.String() != views[j].EventType.String() {
			return views[i].EventType.String() < views[j].EventType.String()
		}
		return views[i].Channel.String() < views[j].Channel.String()
	})
	return views, nil
}

// FindDeliverable is a deliberate no-op stub: the preference routes never
// call it, and that they cannot is exactly what this composition root is
// supposed to guarantee. A fake returning preferences here would give a
// route test a secret to accidentally render.
func (r *inMemoryPreferenceRepository) FindDeliverable(_ context.Context, _ notificationdomain.UserID, _ notificationdomain.EventType) ([]*notificationdomain.NotificationPreference, error) {
	return nil, nil
}

func (r *inMemoryPreferenceRepository) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byKey)
}

// alwaysAllowRateLimiter is a fake rateLimiter so the tests that are not
// about rate limiting don't need a live Redis instance and are unaffected by
// it. Carried from cmd/api/video_test.go, where the preference suite used to
// borrow it; the limiter's own behaviour is covered by ratelimit_test.go.
type alwaysAllowRateLimiter struct{}

func (alwaysAllowRateLimiter) Allow(context.Context, string) (bool, time.Duration, error) {
	return true, 0, nil
}

const notificationPreferencesPath = "/api/notification-preferences"

const testWebhookSecret = "test-only-signing-secret"

// newTestNotificationModuleWithPolicy takes the policy explicitly, and every
// caller but the destination-policy tests passes the restrictive posture, so
// the rest of this file keeps exercising what production runs under.
func newTestNotificationModuleWithPolicy(repo notificationdomain.PreferenceRepository, policy notificationdomain.DestinationPolicy) *notificationModule {
	return newNotificationModule(
		notificationapplication.NewSetPreference(repo, systemClock{}, policy),
		notificationapplication.NewListPreferences(repo),
	)
}

// newNotificationTestServer serves the real router so these tests exercise
// the route's real middleware chain, not a hand-built one — this service's
// whole router, now, rather than one context's slice of a shared one.
func newNotificationTestServer(t *testing.T, limiter rateLimiter) (*httptest.Server, testTokens, *inMemoryPreferenceRepository) {
	t.Helper()

	auth, tokens := newTestAuthenticatorWithTokens(t)
	repo := newInMemoryPreferenceRepository()
	return newNotificationTestServerOver(t, limiter, auth, repo, notificationdomain.NewDestinationPolicy(false)), tokens, repo
}

// newNotificationTestServerOver serves the real router over a caller-supplied
// authenticator, repository and policy, so two servers under two different
// postures can be stood up over one repository — which is how a row written
// before the policy existed is produced without reaching inside the fake.
func newNotificationTestServerOver(
	t *testing.T,
	limiter rateLimiter,
	auth *authenticator,
	repo notificationdomain.PreferenceRepository,
	policy notificationdomain.DestinationPolicy,
) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(setupRouter(auth, newTestNotificationModuleWithPolicy(repo, policy), limiter))
	t.Cleanup(srv.Close)
	return srv
}

func putPreference(t *testing.T, baseURL, token string, payload any) *http.Response {
	t.Helper()

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error marshaling request: %v", err)
	}
	req, err := http.NewRequest(http.MethodPut, baseURL+notificationPreferencesPath, strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return resp
}

// readBody drains and closes resp, returning the raw bytes so assertions can
// be made on what was actually serialized rather than on a decoded struct.
func readBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("unexpected error reading body: %v", err)
	}
	return body
}

func listPreferences(t *testing.T, baseURL, token string) (int, []byte) {
	t.Helper()
	resp := getWithAuthorization(t, baseURL+notificationPreferencesPath, "Bearer "+token)
	return resp.StatusCode, readBody(t, resp)
}

func decodePreferenceList(t *testing.T, body []byte) listPreferencesResponse {
	t.Helper()
	var decoded listPreferencesResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unexpected error decoding list response: %v", err)
	}
	return decoded
}

func validPreferenceBody() map[string]any {
	return map[string]any{
		"event_type":  notificationdomain.EventTypeVideoJobCompleted,
		"channel":     "webhook",
		"enabled":     true,
		"destination": "https://example.test/hooks/done",
		"secret":      testWebhookSecret,
	}
}

// 5.1
//
// Registering and logging in belong to cmd/identity-api, so the flow this
// test names starts one step later: it mints the token the way a caller
// would hold one after logging in elsewhere. What it still asserts is what
// it always asserted — that both preference routes work for the same
// authenticated subject, one after the other.
func TestNotificationPreferences_TokenHolderUsesBothRoutes(t *testing.T) {
	srv, tokens, _ := newNotificationTestServer(t, alwaysAllowRateLimiter{})

	_, accessToken := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	writeResp := putPreference(t, srv.URL, accessToken, validPreferenceBody())
	if writeResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d (body %s)", writeResp.StatusCode, http.StatusOK, readBody(t, writeResp))
	}
	writeResp.Body.Close()

	status, body := listPreferences(t, srv.URL, accessToken)
	if status != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", status, http.StatusOK)
	}
	if got := decodePreferenceList(t, body); len(got.Preferences) != 1 {
		t.Fatalf("got %d preferences, want 1", len(got.Preferences))
	}
}

// 5.2
func TestNotificationRoutes_RejectMissingMalformedAndExpiredTokens(t *testing.T) {
	srv, tokens, repo := newNotificationTestServer(t, alwaysAllowRateLimiter{})

	userID, _ := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")
	expired, err := tokens.Issue(userID, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, tt := range []struct {
		name   string
		header string
	}{
		{"no token", ""},
		{"malformed token", "Bearer not-a-jwt"},
		{"expired token", "Bearer " + expired},
	} {
		t.Run(tt.name, func(t *testing.T) {
			readResp := getWithAuthorization(t, srv.URL+notificationPreferencesPath, tt.header)
			defer readResp.Body.Close()
			if readResp.StatusCode != http.StatusUnauthorized {
				t.Errorf("GET status = %d, want %d", readResp.StatusCode, http.StatusUnauthorized)
			}

			token := strings.TrimPrefix(tt.header, "Bearer ")
			writeResp := putPreference(t, srv.URL, token, validPreferenceBody())
			defer writeResp.Body.Close()
			if writeResp.StatusCode != http.StatusUnauthorized {
				t.Errorf("PUT status = %d, want %d", writeResp.StatusCode, http.StatusUnauthorized)
			}
		})
	}

	if repo.count() != 0 {
		t.Fatalf("stored %d preferences, want 0", repo.count())
	}
}

// 5.3
func TestListPreferences_EmptyIsAnEmptyCollectionNotANotFound(t *testing.T) {
	srv, tokens, _ := newNotificationTestServer(t, alwaysAllowRateLimiter{})
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	status, body := listPreferences(t, srv.URL, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	// Asserted on the raw bytes: a decoded struct cannot tell a JSON null
	// apart from an absent array, and null is what an unallocated Go slice
	// serializes to.
	if !strings.Contains(string(body), `"preferences":[]`) {
		t.Fatalf("body = %s, want an empty preferences array", body)
	}
}

// 5.4
func TestSetThenListPreference_ReportsHasSecretAndNeverEchoesTheSecret(t *testing.T) {
	srv, tokens, _ := newNotificationTestServer(t, alwaysAllowRateLimiter{})
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	writeBody := readBody(t, putPreference(t, srv.URL, token, validPreferenceBody()))
	status, listBody := listPreferences(t, srv.URL, token)
	if status != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", status, http.StatusOK)
	}

	// The colon matters: has_secret contains the substring "secret", so a
	// bare match on the word would pass against a body that leaked one.
	for _, body := range [][]byte{writeBody, listBody} {
		if strings.Contains(string(body), `"secret":`) {
			t.Errorf("body carries a secret field: %s", body)
		}
		if strings.Contains(string(body), testWebhookSecret) {
			t.Errorf("body carries the secret's value: %s", body)
		}
		if !strings.Contains(string(body), `"has_secret":true`) {
			t.Errorf("body = %s, want has_secret true", body)
		}
	}

	listed := decodePreferenceList(t, listBody)
	if len(listed.Preferences) != 1 {
		t.Fatalf("got %d preferences, want 1", len(listed.Preferences))
	}
	if listed.Preferences[0].Destination != "https://example.test/hooks/done" {
		t.Errorf("Destination = %q, want the stored destination", listed.Preferences[0].Destination)
	}
}

// 5.5
func TestNotificationPreferences_DoNotLeakAcrossTheOwnerBoundary(t *testing.T) {
	srv, tokens, _ := newNotificationTestServer(t, alwaysAllowRateLimiter{})
	_, tokenA := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")
	_, tokenB := issueTestToken(t, tokens, "9f1c1c2e-6d4a-4b3f-8a55-1d2e3f4a5b6c")

	bodyA := validPreferenceBody()
	bodyA["destination"] = "https://a.example.test/hooks"
	writeA := putPreference(t, srv.URL, tokenA, bodyA)
	defer writeA.Body.Close()
	if writeA.StatusCode != http.StatusOK {
		t.Fatalf("user A PUT status = %d, want %d", writeA.StatusCode, http.StatusOK)
	}

	bodyB := validPreferenceBody()
	bodyB["event_type"] = notificationdomain.EventTypeVideoJobFailed
	bodyB["destination"] = "https://b.example.test/hooks"
	writeB := putPreference(t, srv.URL, tokenB, bodyB)
	defer writeB.Body.Close()
	if writeB.StatusCode != http.StatusOK {
		t.Fatalf("user B PUT status = %d, want %d", writeB.StatusCode, http.StatusOK)
	}

	// Both directions: a filter that is merely inverted would pass a
	// one-sided assertion on this seeding.
	for _, tt := range []struct {
		name            string
		token           string
		wantEventType   string
		wantDestination string
	}{
		{"user A", tokenA, notificationdomain.EventTypeVideoJobCompleted, "https://a.example.test/hooks"},
		{"user B", tokenB, notificationdomain.EventTypeVideoJobFailed, "https://b.example.test/hooks"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, body := listPreferences(t, srv.URL, tt.token)
			listed := decodePreferenceList(t, body)
			if len(listed.Preferences) != 1 {
				t.Fatalf("got %d preferences, want 1", len(listed.Preferences))
			}
			if listed.Preferences[0].EventType != tt.wantEventType {
				t.Errorf("EventType = %q, want %q", listed.Preferences[0].EventType, tt.wantEventType)
			}
			if listed.Preferences[0].Destination != tt.wantDestination {
				t.Errorf("Destination = %q, want %q", listed.Preferences[0].Destination, tt.wantDestination)
			}
		})
	}
}

// 5.6
func TestSetPreference_IgnoresAUserIDInTheBody(t *testing.T) {
	srv, tokens, _ := newNotificationTestServer(t, alwaysAllowRateLimiter{})
	victimID, victimToken := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")
	_, attackerToken := issueTestToken(t, tokens, "9f1c1c2e-6d4a-4b3f-8a55-1d2e3f4a5b6c")

	victimBody := validPreferenceBody()
	victimBody["destination"] = "https://victim.example.test/hooks"
	victimWrite := putPreference(t, srv.URL, victimToken, victimBody)
	defer victimWrite.Body.Close()
	if victimWrite.StatusCode != http.StatusOK {
		t.Fatalf("victim PUT status = %d, want %d", victimWrite.StatusCode, http.StatusOK)
	}

	attackerBody := validPreferenceBody()
	attackerBody["user_id"] = victimID.String()
	attackerBody["destination"] = "https://attacker.example.test/hooks"
	attackerWrite := putPreference(t, srv.URL, attackerToken, attackerBody)
	if attackerWrite.StatusCode != http.StatusOK {
		t.Fatalf("attacker PUT status = %d, want %d", attackerWrite.StatusCode, http.StatusOK)
	}
	attackerWrite.Body.Close()

	_, victimList := listPreferences(t, srv.URL, victimToken)
	victimPrefs := decodePreferenceList(t, victimList).Preferences
	if len(victimPrefs) != 1 {
		t.Fatalf("victim has %d preferences, want 1", len(victimPrefs))
	}
	if victimPrefs[0].Destination != "https://victim.example.test/hooks" {
		t.Errorf("victim Destination = %q, want it untouched", victimPrefs[0].Destination)
	}

	_, attackerList := listPreferences(t, srv.URL, attackerToken)
	attackerPrefs := decodePreferenceList(t, attackerList).Preferences
	if len(attackerPrefs) != 1 {
		t.Fatalf("attacker has %d preferences, want 1", len(attackerPrefs))
	}
	if attackerPrefs[0].Destination != "https://attacker.example.test/hooks" {
		t.Errorf("attacker Destination = %q, want the write to have landed on the caller", attackerPrefs[0].Destination)
	}
}

// 5.7
func TestSetPreference_LeavesASecondTriplesPreferenceIntact(t *testing.T) {
	srv, tokens, _ := newNotificationTestServer(t, alwaysAllowRateLimiter{})
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	failedBody := validPreferenceBody()
	failedBody["event_type"] = notificationdomain.EventTypeVideoJobFailed
	failedBody["destination"] = "https://example.test/hooks/failed"
	first := putPreference(t, srv.URL, token, failedBody)
	defer first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first PUT status = %d, want %d", first.StatusCode, http.StatusOK)
	}

	second := putPreference(t, srv.URL, token, validPreferenceBody())
	defer second.Body.Close()
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second PUT status = %d, want %d", second.StatusCode, http.StatusOK)
	}

	_, body := listPreferences(t, srv.URL, token)
	prefs := decodePreferenceList(t, body).Preferences
	if len(prefs) != 2 {
		t.Fatalf("got %d preferences, want 2", len(prefs))
	}
	byEventType := map[string]string{}
	for _, pref := range prefs {
		byEventType[pref.EventType] = pref.Destination
	}
	if got := byEventType[notificationdomain.EventTypeVideoJobFailed]; got != "https://example.test/hooks/failed" {
		t.Errorf("failed-event destination = %q, want it intact", got)
	}
	if got := byEventType[notificationdomain.EventTypeVideoJobCompleted]; got != "https://example.test/hooks/done" {
		t.Errorf("completed-event destination = %q, want the second write", got)
	}
}

// 5.8
func TestSetPreference_OmittedSecretPreservesAndEmptySecretIsRejected(t *testing.T) {
	srv, tokens, _ := newNotificationTestServer(t, alwaysAllowRateLimiter{})
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	created := putPreference(t, srv.URL, token, validPreferenceBody())
	defer created.Body.Close()
	if created.StatusCode != http.StatusOK {
		t.Fatalf("create status = %d, want %d", created.StatusCode, http.StatusOK)
	}

	omitted := validPreferenceBody()
	delete(omitted, "secret")
	omitted["destination"] = "https://example.test/hooks/updated"
	updateBody := readBody(t, putPreference(t, srv.URL, token, omitted))
	if !strings.Contains(string(updateBody), `"has_secret":true`) {
		t.Errorf("update body = %s, want has_secret true", updateBody)
	}
	if !strings.Contains(string(updateBody), "https://example.test/hooks/updated") {
		t.Errorf("update body = %s, want the new destination", updateBody)
	}

	empty := validPreferenceBody()
	empty["secret"] = ""
	rejected := putPreference(t, srv.URL, token, empty)
	defer rejected.Body.Close()
	if rejected.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty-secret status = %d, want %d", rejected.StatusCode, http.StatusBadRequest)
	}
}

// 5.8, the other half of the create rule: an update is what an omitted
// secret licenses, so a create carrying none is refused rather than stored.
func TestSetPreference_CreateWithoutASecretIsRejected(t *testing.T) {
	srv, tokens, repo := newNotificationTestServer(t, alwaysAllowRateLimiter{})
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	body := validPreferenceBody()
	delete(body, "secret")
	resp := putPreference(t, srv.URL, token, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	if repo.count() != 0 {
		t.Fatalf("stored %d preferences, want 0", repo.count())
	}
}

// 5.9
func TestSetPreference_RejectsInvalidValuesAndStoresNothing(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"unknown event type", func(b map[string]any) { b["event_type"] = "video_job.archived.v1" }},
		{"unversioned event type", func(b map[string]any) { b["event_type"] = "video_job.completed" }},
		{"unsupported channel", func(b map[string]any) { b["channel"] = "email" }},
		{"relative destination", func(b map[string]any) { b["destination"] = "/hooks/done" }},
		{"ftp destination", func(b map[string]any) { b["destination"] = "ftp://example.test/hooks" }},
		{"short secret", func(b map[string]any) { b["secret"] = strings.Repeat("s", 15) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, tokens, repo := newNotificationTestServer(t, alwaysAllowRateLimiter{})
			_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

			body := validPreferenceBody()
			tt.mutate(body)

			resp := putPreference(t, srv.URL, token, body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
			}
			if repo.count() != 0 {
				t.Fatalf("stored %d preferences, want 0", repo.count())
			}
		})
	}
}

// 5.9a
func TestNotificationPreferences_PreflightAdvertisesPUT(t *testing.T) {
	srv, _, _ := newNotificationTestServer(t, alwaysAllowRateLimiter{})

	// No bearer token on purpose: the CORS middleware is mounted on the
	// engine and aborts OPTIONS with 204 before the auth group runs, which
	// is exactly what a browser preflight does.
	req, err := http.NewRequest(http.MethodOptions, srv.URL+notificationPreferencesPath, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
	allowed := resp.Header.Get("Access-Control-Allow-Methods")
	if !strings.Contains(allowed, http.MethodPut) {
		t.Fatalf("Access-Control-Allow-Methods = %q, want it to advertise PUT", allowed)
	}
}

// 5.10
func TestNotificationRoutes_AreRateLimited(t *testing.T) {
	limiter := &fakeRateLimiter{allow: false, retryAfter: 42 * time.Second}
	srv, tokens, repo := newNotificationTestServer(t, limiter)
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	writeResp := putPreference(t, srv.URL, token, validPreferenceBody())
	defer writeResp.Body.Close()
	if writeResp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("PUT status = %d, want %d", writeResp.StatusCode, http.StatusTooManyRequests)
	}
	if got := writeResp.Header.Get("Retry-After"); got != "42" {
		t.Errorf("PUT Retry-After = %q, want %q", got, "42")
	}

	readResp := getWithAuthorization(t, srv.URL+notificationPreferencesPath, "Bearer "+token)
	defer readResp.Body.Close()
	if readResp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("GET status = %d, want %d", readResp.StatusCode, http.StatusTooManyRequests)
	}
	if got := readResp.Header.Get("Retry-After"); got != "42" {
		t.Errorf("GET Retry-After = %q, want %q", got, "42")
	}

	if repo.count() != 0 {
		t.Fatalf("stored %d preferences, want 0 — the limiter must reject before the handler runs", repo.count())
	}
}

// The composition path itself, which no route test reaches: every one of
// them builds the module by hand. A regression in the startup gate would
// otherwise leave the suite green while this service refused to boot — or,
// worse, booted without persistence.
func TestSetupNotification_DSNMissing_ReturnsError(t *testing.T) {
	t.Setenv("NOTIFICATION_POSTGRES_DSN", "")

	module, db, err := setupNotification(context.Background())
	if err == nil {
		t.Fatal("expected an error when NOTIFICATION_POSTGRES_DSN is not set")
	}
	if !errors.Is(err, notificationpostgres.ErrDSNRequired) {
		t.Fatalf("expected error to wrap notificationpostgres.ErrDSNRequired, got: %v", err)
	}
	// The variable's own name has to survive into the message, so an
	// operator reading a fatal startup log knows what to set.
	if !strings.Contains(err.Error(), "NOTIFICATION_POSTGRES_DSN") {
		t.Errorf("error %q does not name the missing variable", err)
	}
	if module != nil {
		t.Fatalf("expected a nil module on error, got %+v", module)
	}
	if db != nil {
		t.Fatalf("expected a nil db on error, got %+v", db)
	}
}

func TestSetupNotification_UnreachablePostgres_ReturnsError(t *testing.T) {
	// A loopback address on a port nothing listens on fails fast (connection
	// refused) rather than hanging, so this stays a fast unit-style test.
	t.Setenv("NOTIFICATION_POSTGRES_DSN", "postgres://user:pass@127.0.0.1:1/notification?sslmode=disable&connect_timeout=1")

	module, db, err := setupNotification(context.Background())
	if err == nil {
		t.Fatal("expected an error when configured PostgreSQL is unreachable")
	}
	if strings.TrimSpace(err.Error()) == "" {
		t.Fatal("expected a non-empty error message")
	}
	if module != nil {
		t.Fatalf("expected a nil module on error, got %+v", module)
	}
	if db != nil {
		t.Fatalf("expected a nil db on error, got %+v", db)
	}
}

// secretLoadingMethod is the one read path permitted to load a stored
// signing secret.
const secretLoadingMethod = "FindDeliverable"

// deliveryUseCaseFile is the only file in the Notification application layer
// allowed to name it.
const deliveryUseCaseFile = "deliver_notification.go"

// deliveryUseCaseConstructor is how that file would be reached from a
// composition root. Naming the constructor is not the same check as naming
// the repository method: a wiring line calls the former and never mentions
// the latter, so a scan for the method alone would report a clean composition
// root that in fact builds the use case that loads secrets.
const deliveryUseCaseConstructor = "NewDeliverNotification"

// TestTheHTTPCompositionRootDoesNotLoadTheSecret is the assertion that
// carries the weight of "the secret is read on the delivery path and nowhere
// else".
//
// The invariant it defends used to be absolute — no read path loaded the
// secret at all — and add-notification-webhook-delivery narrows rather than
// drops it: exactly one named method may, and nothing this composition root
// wires may reach it. A source scan rather than a runtime assertion, for the
// same reason internal/notification/infrastructure/postgres scans its own
// queries: a call that is never executed is invisible to a test that runs
// the program.
//
// Re-targeted here from cmd/api, and the claim is strictly stronger for the
// move: the Identity and Video services no longer link the Notification
// context's repository at all, so for them the property is enforced by the
// build rather than by a test. This is the one HTTP service left that could
// reach the operation, which is why the assertion follows the routes rather
// than staying behind.
//
// Two halves, because "no file under cmd/notification-api names the method"
// is not the same as "no path under cmd/notification-api reaches it". The
// first half is the direct call; the second is the indirect one, through the
// use cases this root does wire.
func TestTheHTTPCompositionRootDoesNotLoadTheSecret(t *testing.T) {
	// Paths are relative to the repository root, not to this package:
	// TestMain chdirs there so the tests run with the same working directory
	// the binary does.
	compositionRoot := namingFiles(t, filepath.Join("cmd", "notification-api"), secretLoadingMethod)
	if len(compositionRoot) != 0 {
		t.Errorf("%v under cmd/notification-api name %s: the HTTP composition root must not reach the one read path that loads a stored secret",
			compositionRoot, secretLoadingMethod)
	}

	// The use cases this root wires. Only the delivery use case — which it
	// does not wire, and cmd/notifier does — may name it.
	useCases := namingFiles(t, filepath.Join("internal", "notification", "application"), secretLoadingMethod)
	for _, name := range useCases {
		if name != deliveryUseCaseFile {
			t.Errorf("%s names %s: a use case the HTTP composition root wires would then reach a stored secret through it",
				name, secretLoadingMethod)
		}
	}
	if !slices.Contains(useCases, deliveryUseCaseFile) {
		t.Fatalf("no file in the notification application layer names %s; this scan is passing vacuously", secretLoadingMethod)
	}

	// The wiring itself. cmd/notifier is the composition root that builds the
	// delivery use case; this one must not, whatever the use case's own file
	// happens to call.
	wiring := namingFiles(t, filepath.Join("cmd", "notification-api"), deliveryUseCaseConstructor)
	if len(wiring) != 0 {
		t.Errorf("%v under cmd/notification-api name %s: wiring the delivery use case is what would give this process a path to a stored secret",
			wiring, deliveryUseCaseConstructor)
	}
	if constructed := namingFiles(t, filepath.Join("internal", "notification", "application"), deliveryUseCaseConstructor); len(constructed) == 0 {
		t.Fatalf("no file in the notification application layer names %s; this scan is passing vacuously", deliveryUseCaseConstructor)
	}
}

// 6.3
//
// The write-time half of the destination policy. These are the two shapes it
// exists to keep out: a plaintext scheme, and a destination naming an address
// inside this deployment — 169.254.169.254 being the cloud metadata endpoint
// an SSRF primitive is worth building to reach.
func TestNotificationPreferences_RefuseInsecureDestinationsUnderTheDefaultPolicy(t *testing.T) {
	srv, tokens, repo := newNotificationTestServer(t, alwaysAllowRateLimiter{})
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	for _, tt := range []struct {
		name        string
		destination string
	}{
		{"http scheme", "http://example.test/hooks/done"},
		{"private address", "https://169.254.169.254/latest/meta-data/"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := validPreferenceBody()
			body["destination"] = tt.destination

			resp := putPreference(t, srv.URL, token, body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("PUT status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
			}

			// The refusal says the destination was refused and stops there. It
			// must not name the rule: the rules enumerate this deployment's
			// internal address space, and a caller who could tell "private"
			// from "documentation" apart by resubmitting has a probe.
			var decoded notificationErrorResponse
			if err := json.Unmarshal(readBody(t, resp), &decoded); err != nil {
				t.Fatalf("unexpected error decoding error response: %v", err)
			}
			for _, leak := range []string{"private", "loopback", "link-local", "scheme", "https", "unicast", tt.destination} {
				if strings.Contains(strings.ToLower(decoded.Error), strings.ToLower(leak)) {
					t.Errorf("error %q names %q; the response must not enumerate which rule caught it", decoded.Error, leak)
				}
			}
		})
	}

	if repo.count() != 0 {
		t.Errorf("%d preferences stored, want 0 — a refused destination is refused before the write", repo.count())
	}
}

// The relaxation is what docker-compose.yml sets for the local stack, where
// there is no TLS and a receiver is a container hostname on a private
// network. Both shapes above are accepted under it.
func TestNotificationPreferences_AcceptInsecureDestinationsUnderTheRelaxation(t *testing.T) {
	auth, tokens := newTestAuthenticatorWithTokens(t)
	repo := newInMemoryPreferenceRepository()
	srv := newNotificationTestServerOver(t, alwaysAllowRateLimiter{}, auth, repo, notificationdomain.NewDestinationPolicy(true))
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	for _, tt := range []struct {
		eventType   string
		destination string
	}{
		{notificationdomain.EventTypeVideoJobCompleted, "http://receiver:9000/hooks/done"},
		{notificationdomain.EventTypeVideoJobFailed, "https://10.0.0.7:9000/hooks/failed"},
	} {
		body := validPreferenceBody()
		body["event_type"] = tt.eventType
		body["destination"] = tt.destination

		resp := putPreference(t, srv.URL, token, body)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT %s status = %d, want %d (body %s)", tt.destination, resp.StatusCode, http.StatusOK, readBody(t, resp))
		}
	}

	if repo.count() != 2 {
		t.Errorf("%d preferences stored, want 2", repo.count())
	}
}

// A row stored before the policy existed is neither migrated nor deleted.
// The read path applies no policy at all, so it comes back listed with its
// original destination — the tightening governs what may be written from now
// on, and the dial-time check in the delivery client is what stops an
// already-stored destination from being reached.
func TestNotificationPreferences_StoredBeforeThePolicyAreNeitherMigratedNorDeleted(t *testing.T) {
	auth, tokens := newTestAuthenticatorWithTokens(t)
	repo := newInMemoryPreferenceRepository()
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	const legacyDestination = "http://legacy.internal/hooks/done"

	// Written through a real server running under the relaxation, which is
	// how the row would have been produced before the policy was tightened —
	// rather than by reaching inside the fake, which would prove nothing
	// about the write path.
	relaxed := newNotificationTestServerOver(t, alwaysAllowRateLimiter{}, auth, repo, notificationdomain.NewDestinationPolicy(true))
	body := validPreferenceBody()
	body["destination"] = legacyDestination
	seed := putPreference(t, relaxed.URL, token, body)
	defer seed.Body.Close()
	if seed.StatusCode != http.StatusOK {
		t.Fatalf("seeding PUT status = %d, want %d (body %s)", seed.StatusCode, http.StatusOK, readBody(t, seed))
	}

	// Read back through a second server over the same repository, under the
	// restrictive posture.
	restrictive := newNotificationTestServerOver(t, alwaysAllowRateLimiter{}, auth, repo, notificationdomain.NewDestinationPolicy(false))
	status, listed := listPreferences(t, restrictive.URL, token)
	if status != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", status, http.StatusOK)
	}
	decoded := decodePreferenceList(t, listed)
	if len(decoded.Preferences) != 1 {
		t.Fatalf("got %d preferences, want 1 — the row was deleted or hidden by the read path", len(decoded.Preferences))
	}
	if decoded.Preferences[0].Destination != legacyDestination {
		t.Errorf("Destination = %q, want %q — the row was rewritten", decoded.Preferences[0].Destination, legacyDestination)
	}
	if repo.count() != 1 {
		t.Errorf("%d preferences stored, want 1", repo.count())
	}

	// And rewriting it under the restrictive posture is refused, so the row
	// survives only as it stands: there is no path that quietly re-blesses it.
	rewrite := putPreference(t, restrictive.URL, token, body)
	defer rewrite.Body.Close()
	if rewrite.StatusCode != http.StatusBadRequest {
		t.Errorf("rewriting PUT status = %d, want %d", rewrite.StatusCode, http.StatusBadRequest)
	}
}

// namingFiles returns the base names of the non-test .go files directly
// under dir that contain needle. Test files are excluded deliberately: this
// very file names the method in its own assertions.
func namingFiles(t *testing.T, dir, needle string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("failed to read %s: %v", dir, err)
	}

	naming := make([]string, 0)
	scanned := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		scanned++
		source, err := fs.ReadFile(os.DirFS(dir), entry.Name())
		if err != nil {
			t.Fatalf("failed to read %s: %v", filepath.Join(dir, entry.Name()), err)
		}
		if strings.Contains(string(source), needle) {
			naming = append(naming, entry.Name())
		}
	}
	if scanned == 0 {
		t.Fatalf("no non-test Go file was scanned under %s; the rule this enforces is not being checked", dir)
	}
	return naming
}
