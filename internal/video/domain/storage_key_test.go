package domain_test

import (
	"errors"
	"testing"

	"video-processor/internal/video/domain"
)

func TestNewStorageKey(t *testing.T) {
	key, err := domain.NewStorageKey("results/job-001.zip")
	if err != nil {
		t.Fatalf("NewStorageKey() error = %v", err)
	}
	if key.String() != "results/job-001.zip" || key.IsZero() {
		t.Fatalf("key = %q, IsZero = %v", key.String(), key.IsZero())
	}

	zero, err := domain.NewStorageKey("")
	if !errors.Is(err, domain.ErrInvalidStorageKey) {
		t.Fatalf("NewStorageKey(\"\") error = %v, want %v", err, domain.ErrInvalidStorageKey)
	}
	if !zero.IsZero() {
		t.Fatal("failed construction must return a zero StorageKey")
	}
}

func TestStorageKey_Equal(t *testing.T) {
	a, _ := domain.NewStorageKey("results/job.zip")
	same, _ := domain.NewStorageKey("results/job.zip")
	b, _ := domain.NewStorageKey("results/other.zip")
	if !a.Equal(same) || a.Equal(b) {
		t.Fatal("Equal() returned unexpected values")
	}
}
