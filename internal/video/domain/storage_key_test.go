package domain_test

import (
	"errors"
	"testing"

	"video-processor/internal/video/domain"
)

func TestNewStorageKey(t *testing.T) {
	key, err := domain.NewStorageKey("results/job-1.zip")
	if err != nil || key.String() != "results/job-1.zip" || key.IsZero() {
		t.Fatalf("valid StorageKey was not preserved: key=%v err=%v", key, err)
	}
	if _, err := domain.NewStorageKey(""); !errors.Is(err, domain.ErrInvalidStorageKey) {
		t.Fatalf("empty key error = %v, want %v", err, domain.ErrInvalidStorageKey)
	}
	var zero domain.StorageKey
	if !zero.IsZero() {
		t.Fatal("zero-value StorageKey should be zero")
	}
	copy, _ := domain.NewStorageKey("results/job-1.zip")
	other, _ := domain.NewStorageKey("results/job-2.zip")
	if !key.Equal(copy) || key.Equal(other) {
		t.Fatal("StorageKey equality behavior is incorrect")
	}
}
