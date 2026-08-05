package domain_test

import (
	"errors"
	"testing"

	"video-processor/internal/identity/domain"
)

// Scheme-specific format validation (UUID v4 parsing) lives behind
// UserIDParser and is exercised where it's implemented — the infrastructure
// adapter — not here. This file only covers the one invariant the domain
// itself owns: a UserID must be non-empty.
func TestNewUserID(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr error
	}{
		{"non-empty value accepted", "3fa85f64-5717-4562-b3fc-2c963f66afa6", nil},
		{"empty string rejected", "", domain.ErrInvalidUserID},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := domain.NewUserID(tt.value)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("NewUserID(%q) error = %v, want %v", tt.value, err, tt.wantErr)
			}
			if tt.wantErr == nil && id.String() != tt.value {
				t.Fatalf("NewUserID(%q).String() = %q, want %q", tt.value, id.String(), tt.value)
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

type stubUserIDParser struct {
	id  domain.UserID
	err error
}

func (s stubUserIDParser) ParseUserID(value string) (domain.UserID, error) {
	if s.err != nil {
		return domain.UserID{}, s.err
	}
	return s.id, nil
}

func TestUserIDParser_Port(t *testing.T) {
	want, _ := domain.NewUserID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	var parser domain.UserIDParser = stubUserIDParser{id: want}

	got, err := parser.ParseUserID("anything")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("parser returned %v, want %v", got, want)
	}

	var failing domain.UserIDParser = stubUserIDParser{err: domain.ErrInvalidUserID}
	if _, err := failing.ParseUserID("garbage"); !errors.Is(err, domain.ErrInvalidUserID) {
		t.Fatalf("error = %v, want %v", err, domain.ErrInvalidUserID)
	}
}
