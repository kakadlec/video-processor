package domain_test

import (
	"errors"
	"testing"

	"video-processor/internal/video/domain"
)

func TestNewUserID(t *testing.T) {
	id, err := domain.NewUserID("verified-user")
	if err != nil {
		t.Fatalf("NewUserID() error = %v", err)
	}
	if id.String() != "verified-user" || id.IsZero() {
		t.Fatalf("id = %q, IsZero = %v", id.String(), id.IsZero())
	}

	zero, err := domain.NewUserID("")
	if !errors.Is(err, domain.ErrInvalidUserID) {
		t.Fatalf("NewUserID(\"\") error = %v, want %v", err, domain.ErrInvalidUserID)
	}
	if !zero.IsZero() {
		t.Fatal("failed construction must return a zero UserID")
	}
}

func TestUserID_Equal(t *testing.T) {
	a, _ := domain.NewUserID("user-a")
	same, _ := domain.NewUserID("user-a")
	b, _ := domain.NewUserID("user-b")
	if !a.Equal(same) {
		t.Fatal("same user ID values should be equal")
	}
	if a.Equal(b) {
		t.Fatal("different user ID values should not be equal")
	}
}
