package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"video-processor/internal/identity/infrastructure/jwtauth"
	notificationapplication "video-processor/internal/notification/application"
	notificationdomain "video-processor/internal/notification/domain"
	notificationmessaging "video-processor/internal/notification/infrastructure/messaging"
	notificationpostgres "video-processor/internal/notification/infrastructure/postgres"
	videodomain "video-processor/internal/video/domain"
	"video-processor/internal/video/infrastructure/idgen"
	videomessaging "video-processor/internal/video/infrastructure/messaging"
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

// The topology half of the same pin, for the copy
// internal/notification/infrastructure/messaging carries of the terminal
// names. It needs nothing running: the descriptors are values, so this one
// can never be skipped, and it is a separate test from the payload pin below
// for exactly that reason.
//
// reflect.DeepEqual over the whole descriptor rather than a field-by-field
// comparison, because "equal in every field" is what DeepEqual actually
// asserts — including a field added to rabbitmq.Topology later that only one
// of the two copies is taught to set.
//
// The bounds matter as much as the names, and less obviously. RabbitMQ
// refuses to redeclare an existing queue whose arguments differ, so a drift
// in WorkMaxLength or either dead-letter bound does not produce a consumer
// reading a slightly different queue — it produces a notifier whose every
// dial fails at the declaration, while a pin comparing only names stays
// green.
func TestNotificationTerminalTopologyMatchesTheEmittedTopology(t *testing.T) {
	copied := notificationmessaging.TerminalEventsTopology()
	emitted := videomessaging.TerminalEventsTopology()

	if !reflect.DeepEqual(copied, emitted) {
		t.Fatalf("the copied terminal topology has drifted from the emitted one:\n copied = %+v\nemitted = %+v", copied, emitted)
	}
}

// The payload half: the bytes internal/video/infrastructure/postgres actually
// stores, decoded by the Notification context's own message structs.
//
// Not a fixture written from those structs, which would pass whatever the
// producer does, and not a comparison of struct tags either. The producer's
// payload types are unexported, so the only honest way to see what it writes
// is to write one — the assertion that matters is that every field arrives
// populated, since a renamed field decodes as its zero value and returns no
// error at all. A notifier reading such a message would announce job "" and
// link to no artifact.
//
// Skipped rather than failed without VIDEO_POSTGRES_TEST_DSN: cmd/api's
// TestMain gates ffmpeg, MinIO, and the broker URL, and this is the one test
// in the package that needs a database.
func TestNotificationTerminalMessagesDecodeTheEmittedPayloads(t *testing.T) {
	db := videoPinTestDB(t)
	ctx := context.Background()

	ids := idgen.New()
	repo := videopostgres.NewRepository(db, ids)

	t.Run("completed", func(t *testing.T) {
		job, epoch := seedProcessingVideoJob(t, repo, ids, "movie.mp4")
		storageKey := videodomain.ResultStorageKey(job.ID())
		if err := job.Complete(storageKey, 42); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if _, err := repo.Update(ctx, job, epoch); err != nil {
			t.Fatalf("Update: %v", err)
		}

		msg, err := notificationmessaging.ParseJobCompletedMessage(
			readEmittedTerminalPayload(t, db, videopostgres.VideoJobCompletedEventType, job.ID().String()))
		if err != nil {
			t.Fatalf("ParseJobCompletedMessage: %v", err)
		}
		if msg.Type != notificationdomain.EventTypeVideoJobCompleted {
			t.Errorf("Type = %q, want %q", msg.Type, notificationdomain.EventTypeVideoJobCompleted)
		}
		if msg.JobID != job.ID().String() {
			t.Errorf("JobID = %q, want %q", msg.JobID, job.ID().String())
		}
		// The owner is what the whole delivery resolves against: preferences
		// are read by user, and a zero value here would resolve every event
		// to nobody.
		if msg.UserID != job.UserID().String() {
			t.Errorf("UserID = %q, want %q", msg.UserID, job.UserID().String())
		}
		if msg.FrameCount != 42 {
			t.Errorf("FrameCount = %d, want 42", msg.FrameCount)
		}
		if msg.StorageKey != storageKey.String() {
			t.Errorf("StorageKey = %q, want %q", msg.StorageKey, storageKey.String())
		}
		// The enrolment boundary compares this against the preference's
		// created_at, so a zero value would deliver a job's outcome to a
		// preference registered long after it.
		if msg.OccurredAt.IsZero() {
			t.Error("OccurredAt is zero — the timestamp did not survive the round trip")
		}
	})

	t.Run("failed", func(t *testing.T) {
		job, epoch := seedProcessingVideoJob(t, repo, ids, "broken.mp4")
		const reason = "ffmpeg exited with status 1"
		if err := job.Fail(reason); err != nil {
			t.Fatalf("Fail: %v", err)
		}
		if _, err := repo.Update(ctx, job, epoch); err != nil {
			t.Fatalf("Update: %v", err)
		}

		msg, err := notificationmessaging.ParseJobFailedMessage(
			readEmittedTerminalPayload(t, db, videopostgres.VideoJobFailedEventType, job.ID().String()))
		if err != nil {
			t.Fatalf("ParseJobFailedMessage: %v", err)
		}
		if msg.Type != notificationdomain.EventTypeVideoJobFailed {
			t.Errorf("Type = %q, want %q", msg.Type, notificationdomain.EventTypeVideoJobFailed)
		}
		if msg.JobID != job.ID().String() {
			t.Errorf("JobID = %q, want %q", msg.JobID, job.ID().String())
		}
		if msg.UserID != job.UserID().String() {
			t.Errorf("UserID = %q, want %q", msg.UserID, job.UserID().String())
		}
		if msg.ErrorReason != reason {
			t.Errorf("ErrorReason = %q, want %q", msg.ErrorReason, reason)
		}
		if msg.OccurredAt.IsZero() {
			t.Error("OccurredAt is zero — the timestamp did not survive the round trip")
		}
	})
}

// videoPinTestDatabase is this test's own database, created beside the one
// VIDEO_POSTGRES_TEST_DSN names, for the reason
// internal/video/infrastructure/messaging keeps one: `go test ./...` runs
// packages in parallel and internal/video/infrastructure/postgres truncates
// video_jobs and video_job_outbox before each of its own tests. Sharing would
// let it wipe the row this test just wrote, as an unrelated flake.
const videoPinTestDatabase = "notification_wire_pin"

func videoPinTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("VIDEO_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("VIDEO_POSTGRES_TEST_DSN not set; skipping the terminal payload pin")
	}

	admin, err := videopostgres.Open(videopostgres.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	ctx := context.Background()
	// The name is a compile-time constant, not caller input, and CREATE
	// DATABASE takes no parameters. Already-exists is the normal case after
	// the first run.
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+videoPinTestDatabase); err != nil && !strings.Contains(err.Error(), "already exists") {
		_ = admin.Close()
		t.Fatalf("create %s: %v", videoPinTestDatabase, err)
	}
	_ = admin.Close()

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse VIDEO_POSTGRES_TEST_DSN: %v", err)
	}
	parsed.Path = "/" + videoPinTestDatabase

	db, err := videopostgres.Open(videopostgres.Config{DSN: parsed.String()})
	if err != nil {
		t.Fatalf("open %s: %v", videoPinTestDatabase, err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := videopostgres.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, "TRUNCATE TABLE video_jobs, video_job_outbox"); err != nil {
		t.Fatalf("truncate tables: %v", err)
	}
	return db
}

// seedProcessingVideoJob creates, enqueues, and claims a job, returning it
// with the epoch its claim won — the state a terminal write starts from, and
// the only state from which Repository.Update emits a terminal outbox row.
func seedProcessingVideoJob(t *testing.T, repo *videopostgres.Repository, ids videodomain.VideoJobIDGenerator, name string) (*videodomain.VideoJob, int64) {
	t.Helper()
	ctx := context.Background()

	userID, err := videodomain.NewUserID("user-1")
	if err != nil {
		t.Fatalf("NewUserID: %v", err)
	}
	filename, err := videodomain.NewOriginalFilename(name)
	if err != nil {
		t.Fatalf("NewOriginalFilename: %v", err)
	}
	sourceKey, err := videodomain.NewStorageKey("uploads/upload-1_" + name)
	if err != nil {
		t.Fatalf("NewStorageKey: %v", err)
	}

	job, err := videodomain.NewVideoJob(ids, userID, filename, sourceKey,
		"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", time.Now().UTC().Truncate(time.Microsecond))
	if err != nil {
		t.Fatalf("NewVideoJob: %v", err)
	}
	if err := repo.Create(ctx, job); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := job.Enqueue(); err != nil {
		t.Fatalf("job.Enqueue: %v", err)
	}
	if err := repo.Enqueue(ctx, job); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := job.StartProcessing(); err != nil {
		t.Fatalf("job.StartProcessing: %v", err)
	}
	claimed, epoch, err := repo.ClaimForProcessing(ctx, job)
	if err != nil {
		t.Fatalf("ClaimForProcessing: %v", err)
	}
	if !claimed {
		t.Fatal("ClaimForProcessing did not claim a freshly queued job")
	}
	return job, epoch
}

func readEmittedTerminalPayload(t *testing.T, db *sql.DB, eventType, jobID string) []byte {
	t.Helper()

	var payload []byte
	if err := db.QueryRowContext(context.Background(),
		`SELECT payload FROM video_job_outbox WHERE event_type = $1 AND payload->>'job_id' = $2`,
		eventType, jobID,
	).Scan(&payload); err != nil {
		t.Fatalf("read %s payload: %v", eventType, err)
	}
	return payload
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

// secretLoadingMethod is the one read path permitted to load a stored
// signing secret.
const secretLoadingMethod = "FindDeliverable"

// deliveryUseCaseFile is the only file in the Notification application layer
// allowed to name it.
const deliveryUseCaseFile = "deliver_notification.go"

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
// Two halves, because "no path under cmd/api reaches it" is not the same as
// "no file under cmd/api names it". The first half is the direct call; the
// second is the indirect one, through the two use cases this root does wire.
func TestTheHTTPCompositionRootDoesNotLoadTheSecret(t *testing.T) {
	// Paths are relative to the repository root, not to this package:
	// TestMain chdirs there so the tests run with the same working directory
	// the binary does.
	compositionRoot := namingFiles(t, filepath.Join("cmd", "api"), secretLoadingMethod)
	if len(compositionRoot) != 0 {
		t.Errorf("%v under cmd/api name %s: the HTTP composition root must not reach the one read path that loads a stored secret",
			compositionRoot, secretLoadingMethod)
	}

	// The use cases cmd/api wires. Only the delivery use case — which this
	// root does not wire, and cmd/notifier does — may name it.
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
