package domain_test

import (
	"errors"
	"testing"

	"video-processor/internal/video/domain"
)

type stubVideoJobIDGenerator struct {
	id domain.VideoJobID
}

func (stub stubVideoJobIDGenerator) NewVideoJobID() domain.VideoJobID {
	return stub.id
}

type stubVideoJobIDParser struct {
	id  domain.VideoJobID
	err error
}

func (stub stubVideoJobIDParser) ParseVideoJobID(string) (domain.VideoJobID, error) {
	return stub.id, stub.err
}

func TestNewVideoJobID(t *testing.T) {
	id, err := domain.NewVideoJobID("job-001")
	if err != nil {
		t.Fatalf("NewVideoJobID() error = %v", err)
	}
	if id.String() != "job-001" || id.IsZero() {
		t.Fatalf("id = %q, IsZero = %v", id.String(), id.IsZero())
	}
	if _, err := domain.NewVideoJobID(""); !errors.Is(err, domain.ErrInvalidVideoJobID) {
		t.Fatalf("NewVideoJobID(\"\") error = %v, want %v", err, domain.ErrInvalidVideoJobID)
	}
}

func TestVideoJobID_Equal(t *testing.T) {
	a, _ := domain.NewVideoJobID("job-a")
	same, _ := domain.NewVideoJobID("job-a")
	b, _ := domain.NewVideoJobID("job-b")
	if !a.Equal(same) || a.Equal(b) {
		t.Fatalf("Equal() returned unexpected values")
	}
}

func TestVideoJobIDGenerator_Port(t *testing.T) {
	want, _ := domain.NewVideoJobID("generated")
	var generator domain.VideoJobIDGenerator = stubVideoJobIDGenerator{id: want}
	if got := generator.NewVideoJobID(); !got.Equal(want) {
		t.Fatalf("NewVideoJobID() = %q, want %q", got, want)
	}
}

func TestVideoJobIDParser_Port(t *testing.T) {
	want, _ := domain.NewVideoJobID("parsed")
	var parser domain.VideoJobIDParser = stubVideoJobIDParser{id: want}
	got, err := parser.ParseVideoJobID("external")
	if err != nil || !got.Equal(want) {
		t.Fatalf("ParseVideoJobID() = (%q, %v), want (%q, nil)", got, err, want)
	}

	wantErr := errors.New("malformed")
	parser = stubVideoJobIDParser{err: wantErr}
	if _, err := parser.ParseVideoJobID("bad"); !errors.Is(err, wantErr) {
		t.Fatalf("ParseVideoJobID() error = %v, want %v", err, wantErr)
	}
}
