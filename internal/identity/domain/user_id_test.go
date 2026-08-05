package domain_test

import (
	"errors"
	"testing"

	"video-processor/internal/identity/domain"
)

func TestNewUserID(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr error
	}{
		{"valid v4 uuid", "3fa85f64-5717-4562-b3fc-2c963f66afa6", "3fa85f64-5717-4562-b3fc-2c963f66afa6", nil},
		{"uppercase normalized to lowercase", "3FA85F64-5717-4562-B3FC-2C963F66AFA6", "3fa85f64-5717-4562-b3fc-2c963f66afa6", nil},
		{"empty string", "", "", domain.ErrInvalidUserID},
		{"not a uuid", "not-a-uuid", "", domain.ErrInvalidUserID},
		{"wrong version (v1, not v4)", "3fa85f64-5717-1562-b3fc-2c963f66afa6", "", domain.ErrInvalidUserID},
		{"wrong variant", "3fa85f64-5717-4562-0000-2c963f66afa6", "", domain.ErrInvalidUserID},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := domain.NewUserID(tt.value)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("NewUserID(%q) error = %v, want %v", tt.value, err, tt.wantErr)
			}
			if tt.wantErr == nil && id.String() != tt.want {
				t.Fatalf("NewUserID(%q).String() = %q, want %q", tt.value, id.String(), tt.want)
			}
		})
	}
}

func TestUserID_IsZero(t *testing.T) {
	var zero domain.UserID
	if !zero.IsZero() {
		t.Fatal("zero-value UserID should report IsZero() == true")
	}

	id, err := domain.NewUserID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id.IsZero() {
		t.Fatal("valid UserID should report IsZero() == false")
	}
}

func TestUserID_Equal(t *testing.T) {
	a, _ := domain.NewUserID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	b, _ := domain.NewUserID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	c, _ := domain.NewUserID("4fa85f64-5717-4562-b3fc-2c963f66afa6")

	if !a.Equal(b) {
		t.Fatal("expected equal UserIDs built from the same value to be Equal")
	}
	if a.Equal(c) {
		t.Fatal("expected different UserIDs to not be Equal")
	}
}

type stubUserIDGenerator struct {
	id domain.UserID
}

func (s stubUserIDGenerator) NewUserID() domain.UserID {
	return s.id
}

func TestUserIDGenerator_Port(t *testing.T) {
	want, _ := domain.NewUserID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	var gen domain.UserIDGenerator = stubUserIDGenerator{id: want}

	got := gen.NewUserID()
	if !got.Equal(want) {
		t.Fatalf("generator returned %v, want %v", got, want)
	}
}
