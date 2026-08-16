package domain_test

import (
	"errors"
	"testing"

	"video-processor/internal/video/domain"
)

func TestNewVideoJobID(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr error
	}{
		{"non-empty value accepted", "3fa85f64-5717-4562-b3fc-2c963f66afa6", nil},
		{"empty string rejected", "", domain.ErrInvalidVideoJobID},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := domain.NewVideoJobID(tt.value)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("NewVideoJobID(%q) error = %v, want %v", tt.value, err, tt.wantErr)
			}
			if tt.wantErr == nil && id.String() != tt.value {
				t.Fatalf("NewVideoJobID(%q).String() = %q, want %q", tt.value, id.String(), tt.value)
			}
		})
	}
}

func TestVideoJobID_IsZero(t *testing.T) {
	var zero domain.VideoJobID
	if !zero.IsZero() {
		t.Fatal("zero-value VideoJobID should report IsZero() == true")
	}

	id, err := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id.IsZero() {
		t.Fatal("valid VideoJobID should report IsZero() == false")
	}
}

func TestVideoJobID_Equal(t *testing.T) {
	a, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	b, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	c, _ := domain.NewVideoJobID("4fa85f64-5717-4562-b3fc-2c963f66afa6")

	if !a.Equal(b) {
		t.Fatal("expected equal VideoJobIDs built from the same value to be Equal")
	}
	if a.Equal(c) {
		t.Fatal("expected different VideoJobIDs to not be Equal")
	}
}

type stubVideoJobIDGenerator struct {
	id domain.VideoJobID
}

func (s stubVideoJobIDGenerator) NewVideoJobID() domain.VideoJobID {
	return s.id
}

func TestVideoJobIDGenerator_Port(t *testing.T) {
	want, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	var gen domain.VideoJobIDGenerator = stubVideoJobIDGenerator{id: want}

	got := gen.NewVideoJobID()
	if !got.Equal(want) {
		t.Fatalf("generator returned %v, want %v", got, want)
	}
}

type stubVideoJobIDParser struct {
	id  domain.VideoJobID
	err error
}

func (s stubVideoJobIDParser) ParseVideoJobID(value string) (domain.VideoJobID, error) {
	if s.err != nil {
		return domain.VideoJobID{}, s.err
	}
	return s.id, nil
}

func TestVideoJobIDParser_Port(t *testing.T) {
	want, _ := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	var parser domain.VideoJobIDParser = stubVideoJobIDParser{id: want}

	got, err := parser.ParseVideoJobID("anything")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("parser returned %v, want %v", got, want)
	}

	var failing domain.VideoJobIDParser = stubVideoJobIDParser{err: domain.ErrInvalidVideoJobID}
	if _, err := failing.ParseVideoJobID("garbage"); !errors.Is(err, domain.ErrInvalidVideoJobID) {
		t.Fatalf("error = %v, want %v", err, domain.ErrInvalidVideoJobID)
	}
}
