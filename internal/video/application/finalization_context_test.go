package application_test

import (
	"testing"
	"time"

	"video-processor/internal/video/application"
)

func TestNewFinalizationContext_NotAlreadyDone(t *testing.T) {
	ctx, cancel := application.NewFinalizationContext()
	defer cancel()

	if err := ctx.Err(); err != nil {
		t.Fatalf("ctx.Err() = %v, want nil", err)
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatalf("expected a deadline to be set")
	}
	if time.Until(deadline) <= 0 {
		t.Fatalf("expected the deadline to be in the future")
	}
}
