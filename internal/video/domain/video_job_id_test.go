package domain_test

import (
	"errors"
	"testing"

	"video-processor/internal/video/domain"
)

type stubVideoJobIDGenerator struct{ id domain.VideoJobID }

func (s stubVideoJobIDGenerator) NewVideoJobID() domain.VideoJobID { return s.id }

type stubVideoJobIDParser struct {
	id  domain.VideoJobID
	err error
}

func (s stubVideoJobIDParser) ParseVideoJobID(string) (domain.VideoJobID, error) {
	return s.id, s.err
}

func TestNewVideoJobID(t *testing.T) {
	valid, err := domain.NewVideoJobID("job-1")
	if err != nil || valid.String() != "job-1" || valid.IsZero() {
		t.Fatalf("valid VideoJobID was not preserved: id=%v err=%v", valid, err)
	}
	if _, err := domain.NewVideoJobID(""); !errors.Is(err, domain.ErrInvalidVideoJobID) {
		t.Fatalf("empty ID error = %v, want %v", err, domain.ErrInvalidVideoJobID)
	}
	var zero domain.VideoJobID
	if !zero.IsZero() {
		t.Fatal("zero-value VideoJobID should be zero")
	}
	copy, _ := domain.NewVideoJobID("job-1")
	other, _ := domain.NewVideoJobID("job-2")
	if !valid.Equal(copy) || valid.Equal(other) {
		t.Fatal("VideoJobID equality behavior is incorrect")
	}
}

func TestVideoJobIDGeneratorAndParserPorts(t *testing.T) {
	want, _ := domain.NewVideoJobID("job-1")
	var generator domain.VideoJobIDGenerator = stubVideoJobIDGenerator{id: want}
	if got := generator.NewVideoJobID(); !got.Equal(want) {
		t.Fatalf("generator returned %v, want %v", got, want)
	}

	var parser domain.VideoJobIDParser = stubVideoJobIDParser{id: want}
	got, err := parser.ParseVideoJobID("job-1")
	if err != nil || !got.Equal(want) {
		t.Fatalf("parser returned (%v, %v), want (%v, nil)", got, err, want)
	}
	malformed := errors.New("malformed video job id")
	parser = stubVideoJobIDParser{err: malformed}
	if _, err := parser.ParseVideoJobID("bad"); !errors.Is(err, malformed) {
		t.Fatalf("parser error = %v, want %v", err, malformed)
	}
}
