package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"video-processor/internal/identity/infrastructure/jwtauth"
	notificationapplication "video-processor/internal/notification/application"
	notificationdomain "video-processor/internal/notification/domain"
	notificationpostgres "video-processor/internal/notification/infrastructure/postgres"
	videopostgres "video-processor/internal/video/infrastructure/postgres"
)

// inMemoryPreferenceRepository is a fake notificationdomain.PreferenceRepository
// so these HTTP tests don't need a live PostgreSQL instance, mirroring
// identity_test.go's inMemoryUserRepository and video_test.go's
// inMemoryVideoJobRepository.
//
// This is why NOTIFICATION_POSTGRES_DSN is deliberately *not* in
// main_test.go's TestMain gate (tasks.md 5.12 left that as a decision and
// leaned the other way). Two reasons decided it. First, the precedent: no
// test in this package calls setupIdentity, setupVideo, or setupNotification
// — every one builds modules by hand and hands them to setupRouter — which is
// why CI's Test step sets neither IDENTITY_POSTGRES_DSN nor
// VIDEO_POSTGRES_DSN either. Real MinIO is the sole exception, and for a
// reason that does not apply here: presigning and Stat are route behaviour,
// whereas the adapter's two-statement create/update branch is invisible to
// these handlers, which only forward ErrSecretRequired as a 400. Second, a
// real pool would be a flake: internal/notification/infrastructure/postgres's
// own testDB TRUNCATEs notification_preferences on every call, `go test ./...`
// runs packages in parallel, and CI points both DSNs at the same database, so
// that TRUNCATE would wipe rows out from under a route test mid-assertion.
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

func (r *inMemoryPreferenceRepository) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byKey)
}

const notificationPreferencesPath = "/api/notification-preferences"

const testWebhookSecret = "test-only-signing-secret"

func newTestNotificationModule(repo notificationdomain.PreferenceRepository) *notificationModule {
	return newNotificationModule(
		notificationapplication.NewSetPreference(repo, systemClock{}),
		notificationapplication.NewListPreferences(repo),
	)
}

// newNoopNotificationModule builds a module over an empty in-memory
// repository, for the tests that need setupRouter's full argument list but
// exercise none of its preference routes.
func newNoopNotificationModule() *notificationModule {
	return newTestNotificationModule(newInMemoryPreferenceRepository())
}

// newNotificationTestServer serves the real router so these tests exercise
// the route's real middleware chain, not a hand-built one.
//
// The video module is nil deliberately: setupRouter only takes method values
// off it to register routes, none of which any test here calls, and building
// a real one creates and tears down a MinIO bucket per test for no coverage.
func newNotificationTestServer(t *testing.T, limiter videoRateLimiter) (*httptest.Server, jwtauth.Adapter, *inMemoryPreferenceRepository) {
	t.Helper()

	identity, tokens := newTestIdentityModuleWithTokens(t)
	repo := newInMemoryPreferenceRepository()
	srv := httptest.NewServer(setupRouter(identity, nil, newTestNotificationModule(repo), limiter))
	t.Cleanup(srv.Close)
	return srv, tokens, repo
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
func TestNotificationPreferences_RegisterLoginThenUseBothRoutes(t *testing.T) {
	srv, _, _ := newNotificationTestServer(t, alwaysAllowRateLimiter{})

	registerTestAccount(t, srv.URL, "prefs-flow@example.com", "correct-horse-battery")

	loginResp := postJSON(t, srv.URL+"/api/auth/login", authenticateUserRequest{
		Email:    "prefs-flow@example.com",
		Password: "correct-horse-battery",
	})
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d, want %d", loginResp.StatusCode, http.StatusOK)
	}
	var login authenticateUserResponse
	if err := json.Unmarshal(readBody(t, loginResp), &login); err != nil {
		t.Fatalf("unexpected error decoding login response: %v", err)
	}

	writeResp := putPreference(t, srv.URL, login.AccessToken, validPreferenceBody())
	if writeResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d (body %s)", writeResp.StatusCode, http.StatusOK, readBody(t, writeResp))
	}
	writeResp.Body.Close()

	status, body := listPreferences(t, srv.URL, login.AccessToken)
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
	limiter := &fakeVideoRateLimiter{allow: false, retryAfter: 42 * time.Second}
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

// 5.11 pins the literals internal/notification/domain deliberately declares
// itself rather than importing, the dependency rules having forbidden the
// import. cmd/api is the composition root and the only package that
// legitimately sees both contexts, so this is where the two can be compared.
// Without it a rename or a generation bump on one side would drift silently:
// a delivery consumer would resolve every terminal event against an event
// type no stored preference names. In the spirit of
// TestRoutingKeyMatchesTheOutboxEventType.
func TestNotificationEventTypesMatchTheEmittedTerminalEventTypes(t *testing.T) {
	tests := []struct {
		name         string
		notification string
		emitted      string
	}{
		{"completed", notificationdomain.EventTypeVideoJobCompleted, videopostgres.VideoJobCompletedEventType},
		{"failed", notificationdomain.EventTypeVideoJobFailed, videopostgres.VideoJobFailedEventType},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.notification != tt.emitted {
				t.Fatalf("notification event type %q does not match the emitted event type %q", tt.notification, tt.emitted)
			}
		})
	}
}

// The composition path itself, which no route test reaches: every one of
// them builds the module by hand. A regression in the startup gate would
// otherwise leave the suite green while cmd/api refused to boot — or, worse,
// booted without persistence.
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
