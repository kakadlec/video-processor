package application_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"video-processor/internal/notification/application"
	"video-processor/internal/notification/domain"
)

const (
	testUserID      = "user-1"
	otherUserID     = "user-2"
	testDestination = "https://hooks.example.com/video-jobs"
	testSecret      = "0123456789abcdef"
)

var testNow = time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

func newSetPreference(repo *fakePreferenceRepository) *application.SetPreference {
	return application.NewSetPreference(repo, fakeClock{now: testNow})
}

func validSetInput() application.SetPreferenceInput {
	secret := testSecret
	return application.SetPreferenceInput{
		UserID:      testUserID,
		EventType:   domain.EventTypeVideoJobCompleted,
		Channel:     domain.ChannelWebhook,
		Enabled:     true,
		Destination: testDestination,
		Secret:      &secret,
	}
}

func TestSetPreferenceStoresANewPreference(t *testing.T) {
	repo := newFakePreferenceRepository()

	result, err := newSetPreference(repo).Execute(context.Background(), validSetInput())
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}

	if result.EventType != domain.EventTypeVideoJobCompleted {
		t.Errorf("EventType = %q, want %q", result.EventType, domain.EventTypeVideoJobCompleted)
	}
	if result.Channel != domain.ChannelWebhook {
		t.Errorf("Channel = %q, want %q", result.Channel, domain.ChannelWebhook)
	}
	if !result.Enabled {
		t.Error("Enabled = false, want true")
	}
	if result.Destination != testDestination {
		t.Errorf("Destination = %q, want %q", result.Destination, testDestination)
	}
	if !result.HasSecret {
		t.Error("HasSecret = false, want true")
	}
	if !result.CreatedAt.Equal(testNow) || !result.UpdatedAt.Equal(testNow) {
		t.Errorf("timestamps = (%v, %v), want both %v", result.CreatedAt, result.UpdatedAt, testNow)
	}
}

// The repository stamps both timestamps from the time it is handed, so the
// use case owning the Clock is what keeps time out of the adapter.
func TestSetPreferencePassesTheClockTimeToTheRepository(t *testing.T) {
	repo := newFakePreferenceRepository()

	if _, err := newSetPreference(repo).Execute(context.Background(), validSetInput()); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}

	if !repo.lastNow.Equal(testNow) {
		t.Errorf("repository received now = %v, want %v", repo.lastNow, testNow)
	}
}

// An omitted secret must reach the repository as an omission, because that
// is the only signal it branches on to choose the statement that preserves
// the stored value.
func TestSetPreferenceForwardsAnOmittedSecretAsOmitted(t *testing.T) {
	repo := newFakePreferenceRepository()
	uc := newSetPreference(repo)

	if _, err := uc.Execute(context.Background(), validSetInput()); err != nil {
		t.Fatalf("seeding Execute() error = %v, want nil", err)
	}

	update := validSetInput()
	update.Secret = nil
	update.Enabled = false

	result, err := uc.Execute(context.Background(), update)
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}

	if _, submitted := repo.lastIntent.Secret(); submitted {
		t.Error("intent reported a submitted secret, want omitted")
	}
	if result.Enabled {
		t.Error("Enabled = true, want false")
	}
	if !result.HasSecret {
		t.Error("HasSecret = false, want true — the stored secret must be preserved")
	}
}

// A submitted secret must reach the repository as submitted, so the insert
// path replaces the stored one.
func TestSetPreferenceForwardsASubmittedSecretAsSubmitted(t *testing.T) {
	repo := newFakePreferenceRepository()

	if _, err := newSetPreference(repo).Execute(context.Background(), validSetInput()); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}

	secret, submitted := repo.lastIntent.Secret()
	if !submitted {
		t.Fatal("intent reported an omitted secret, want submitted")
	}
	if secret.Reveal() != testSecret {
		t.Error("intent carried a different secret than the one submitted")
	}
}

// The rule is enforced by the adapter's row count; the use case's obligation
// is to surface it unchanged, since errors.Is is what separates it from
// ErrInvalidSecret at the HTTP boundary.
func TestSetPreferenceSurfacesErrSecretRequiredOnCreateWithoutASecret(t *testing.T) {
	repo := newFakePreferenceRepository()
	input := validSetInput()
	input.Secret = nil

	_, err := newSetPreference(repo).Execute(context.Background(), input)
	if !errors.Is(err, domain.ErrSecretRequired) {
		t.Fatalf("Execute() error = %v, want ErrSecretRequired", err)
	}
	if errors.Is(err, domain.ErrInvalidSecret) {
		t.Error("error also matches ErrInvalidSecret; the two sentinels must stay disjoint")
	}
}

func TestSetPreferenceWritesForTheCallerOnly(t *testing.T) {
	repo := newFakePreferenceRepository()
	input := validSetInput()
	input.UserID = otherUserID

	if _, err := newSetPreference(repo).Execute(context.Background(), input); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}

	if got := repo.lastIntent.UserID().String(); got != otherUserID {
		t.Errorf("intent user id = %q, want %q", got, otherUserID)
	}
}

func TestSetPreferenceRejectsInvalidInput(t *testing.T) {
	longSecret := strings.Repeat("a", domain.MinSecretLength-1)
	emptySecret := ""

	tests := []struct {
		name    string
		mutate  func(*application.SetPreferenceInput)
		wantErr error
	}{
		{
			name:    "empty user id",
			mutate:  func(in *application.SetPreferenceInput) { in.UserID = "" },
			wantErr: domain.ErrInvalidUserID,
		},
		{
			name:    "unknown event type",
			mutate:  func(in *application.SetPreferenceInput) { in.EventType = "video_job.archived.v1" },
			wantErr: domain.ErrInvalidEventType,
		},
		{
			name:    "unversioned event type",
			mutate:  func(in *application.SetPreferenceInput) { in.EventType = "video_job.completed" },
			wantErr: domain.ErrInvalidEventType,
		},
		{
			name:    "email channel",
			mutate:  func(in *application.SetPreferenceInput) { in.Channel = "email" },
			wantErr: domain.ErrInvalidChannel,
		},
		{
			name:    "relative destination",
			mutate:  func(in *application.SetPreferenceInput) { in.Destination = "/hooks" },
			wantErr: domain.ErrInvalidDestination,
		},
		{
			name:    "ftp destination",
			mutate:  func(in *application.SetPreferenceInput) { in.Destination = "ftp://example.com/hooks" },
			wantErr: domain.ErrInvalidDestination,
		},
		{
			name:    "short secret",
			mutate:  func(in *application.SetPreferenceInput) { in.Secret = &longSecret },
			wantErr: domain.ErrInvalidSecret,
		},
		{
			name:    "explicitly empty secret",
			mutate:  func(in *application.SetPreferenceInput) { in.Secret = &emptySecret },
			wantErr: domain.ErrInvalidSecret,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakePreferenceRepository()
			input := validSetInput()
			tt.mutate(&input)

			_, err := newSetPreference(repo).Execute(context.Background(), input)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Execute() error = %v, want %v", err, tt.wantErr)
			}
			if repo.setCalls != 0 {
				t.Errorf("repository Set called %d times, want 0 — nothing may be stored", repo.setCalls)
			}
		})
	}
}

func TestSetPreferenceSurfacesARepositoryFailure(t *testing.T) {
	wantErr := errors.New("connection refused")
	repo := newFakePreferenceRepository()
	repo.setErr = wantErr

	_, err := newSetPreference(repo).Execute(context.Background(), validSetInput())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Execute() error = %v, want %v", err, wantErr)
	}
}

// The result type is the whole non-disclosure guarantee at this layer: a
// handler cannot serialize a field that does not exist. HasSecret is the
// only permitted mention of one.
func TestPreferenceResultDeclaresNoSecretField(t *testing.T) {
	typ := reflect.TypeOf(application.PreferenceResult{})
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		if name == "HasSecret" {
			continue
		}
		if strings.Contains(strings.ToLower(name), "secret") {
			t.Errorf("PreferenceResult declares field %q; the secret must not be returnable", name)
		}
	}
}
