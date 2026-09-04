package application_test

import (
	"context"
	"errors"
	"testing"

	"video-processor/internal/notification/application"
	"video-processor/internal/notification/domain"
)

func seedPreference(t *testing.T, repo *fakePreferenceRepository, userID, eventType string) {
	t.Helper()

	input := validSetInput()
	input.UserID = userID
	input.EventType = eventType

	if _, err := newSetPreference(repo).Execute(context.Background(), input); err != nil {
		t.Fatalf("seeding %s/%s: %v", userID, eventType, err)
	}
}

func TestListPreferencesReturnsOnlyTheCallersPreferences(t *testing.T) {
	repo := newFakePreferenceRepository()
	seedPreference(t, repo, testUserID, domain.EventTypeVideoJobCompleted)
	seedPreference(t, repo, otherUserID, domain.EventTypeVideoJobFailed)

	results, err := application.NewListPreferences(repo).Execute(context.Background(), testUserID)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}

	if len(results) != 1 {
		t.Fatalf("got %d preferences, want 1", len(results))
	}
	if results[0].EventType != domain.EventTypeVideoJobCompleted {
		t.Errorf("EventType = %q, want %q", results[0].EventType, domain.EventTypeVideoJobCompleted)
	}
	if !results[0].HasSecret {
		t.Error("HasSecret = false, want true")
	}

	// The other direction too: a filter that is merely inverted would pass a
	// one-sided assertion on this seeding.
	otherResults, err := application.NewListPreferences(repo).Execute(context.Background(), otherUserID)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if len(otherResults) != 1 {
		t.Fatalf("got %d preferences for the other user, want 1", len(otherResults))
	}
	if otherResults[0].EventType != domain.EventTypeVideoJobFailed {
		t.Errorf("EventType = %q, want %q", otherResults[0].EventType, domain.EventTypeVideoJobFailed)
	}
}

func TestListPreferencesQueriesTheGivenUser(t *testing.T) {
	repo := newFakePreferenceRepository()

	if _, err := application.NewListPreferences(repo).Execute(context.Background(), otherUserID); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}

	if got := repo.lastListedFor.String(); got != otherUserID {
		t.Errorf("repository queried for %q, want %q", got, otherUserID)
	}
}

// An absent preference means not subscribed, which is an ordinary state
// rather than a lookup failure.
func TestListPreferencesReturnsAnEmptyCollectionForAUserWithNone(t *testing.T) {
	repo := newFakePreferenceRepository()
	seedPreference(t, repo, otherUserID, domain.EventTypeVideoJobCompleted)

	results, err := application.NewListPreferences(repo).Execute(context.Background(), testUserID)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if results == nil {
		t.Fatal("results = nil, want an empty non-nil collection")
	}
	if len(results) != 0 {
		t.Fatalf("got %d preferences, want 0", len(results))
	}
}

// The port orders by event type then channel; the use case must not reorder
// what it was handed, or the route's determinism is lost between them.
func TestListPreferencesPreservesTheRepositoryOrdering(t *testing.T) {
	repo := newFakePreferenceRepository()
	seedPreference(t, repo, testUserID, domain.EventTypeVideoJobFailed)
	seedPreference(t, repo, testUserID, domain.EventTypeVideoJobCompleted)

	results, err := application.NewListPreferences(repo).Execute(context.Background(), testUserID)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}

	stored, err := repo.ListByUser(context.Background(), mustUserID(t, testUserID))
	if err != nil {
		t.Fatalf("ListByUser() error = %v, want nil", err)
	}
	if len(results) != len(stored) {
		t.Fatalf("got %d preferences, want %d", len(results), len(stored))
	}
	for i := range results {
		if results[i].EventType != stored[i].EventType.String() {
			t.Errorf("preference %d = %q, want %q", i, results[i].EventType, stored[i].EventType)
		}
	}
}

func TestListPreferencesRejectsAnEmptyUserID(t *testing.T) {
	repo := newFakePreferenceRepository()

	_, err := application.NewListPreferences(repo).Execute(context.Background(), "")
	if !errors.Is(err, domain.ErrInvalidUserID) {
		t.Fatalf("Execute() error = %v, want ErrInvalidUserID", err)
	}
	if repo.listCalls != 0 {
		t.Errorf("repository ListByUser called %d times, want 0", repo.listCalls)
	}
}

func TestListPreferencesSurfacesARepositoryFailure(t *testing.T) {
	wantErr := errors.New("connection refused")
	repo := newFakePreferenceRepository()
	repo.listErr = wantErr

	_, err := application.NewListPreferences(repo).Execute(context.Background(), testUserID)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Execute() error = %v, want %v", err, wantErr)
	}
}

func mustUserID(t *testing.T, value string) domain.UserID {
	t.Helper()

	userID, err := domain.NewUserID(value)
	if err != nil {
		t.Fatalf("NewUserID(%q) error = %v", value, err)
	}
	return userID
}
