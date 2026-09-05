package webhook

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"video-processor/internal/notification/domain"
)

func TestBuildPayload_CompletionNamesTheJobAndItsResult(t *testing.T) {
	event := completedEvent(t, 42)

	body, err := buildPayload(event, deliveryID(t, "delivery-1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unexpected error decoding the payload: %v", err)
	}

	if decoded["id"] != "delivery-1" {
		t.Errorf("id = %v, want delivery-1", decoded["id"])
	}
	if decoded["type"] != domain.EventTypeVideoJobCompleted {
		t.Errorf("type = %v, want %s", decoded["type"], domain.EventTypeVideoJobCompleted)
	}
	if decoded["occurred_at"] != "2026-09-01T12:00:00Z" {
		t.Errorf("occurred_at = %v, want 2026-09-01T12:00:00Z", decoded["occurred_at"])
	}

	data, ok := decoded["data"].(map[string]any)
	if !ok {
		t.Fatalf("data = %v, want an object", decoded["data"])
	}
	if data["job_id"] != "job-1" {
		t.Errorf("data.job_id = %v, want job-1", data["job_id"])
	}
	if data["frame_count"] != float64(42) {
		t.Errorf("data.frame_count = %v, want 42", data["frame_count"])
	}
	if data["storage_key"] != "frames_job-1.zip" {
		t.Errorf("data.storage_key = %v, want frames_job-1.zip", data["storage_key"])
	}
}

// A completion reporting zero frames still says zero. This is what the
// pointer field is for: with a plain int and omitempty the value would
// vanish, and a receiver would have to guess whether the job produced no
// frames or the sender forgot the field.
func TestBuildPayload_ZeroFrameCountIsSentRatherThanOmitted(t *testing.T) {
	body, err := buildPayload(completedEvent(t, 0), deliveryID(t, "delivery-zero"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var decoded struct {
		Data struct {
			FrameCount *int `json:"frame_count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unexpected error decoding the payload: %v", err)
	}
	if decoded.Data.FrameCount == nil {
		t.Fatal("data.frame_count is absent, want 0")
	}
	if *decoded.Data.FrameCount != 0 {
		t.Errorf("data.frame_count = %d, want 0", *decoded.Data.FrameCount)
	}
}

func TestBuildPayload_FailureNamesTheJobAndItsReason(t *testing.T) {
	event := failedEvent(t, "ffmpeg exited with status 1")

	body, err := buildPayload(event, deliveryID(t, "delivery-2"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var decoded struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Data struct {
			JobID      string `json:"job_id"`
			Reason     string `json:"reason"`
			FrameCount *int   `json:"frame_count"`
			StorageKey string `json:"storage_key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unexpected error decoding the payload: %v", err)
	}

	if decoded.Type != domain.EventTypeVideoJobFailed {
		t.Errorf("type = %s, want %s", decoded.Type, domain.EventTypeVideoJobFailed)
	}
	if decoded.Data.Reason != "ffmpeg exited with status 1" {
		t.Errorf("data.reason = %q, want the failure reason", decoded.Data.Reason)
	}
	if decoded.Data.FrameCount != nil || decoded.Data.StorageKey != "" {
		t.Errorf("a failure carries a completion's fields: frame_count = %v, storage_key = %q",
			decoded.Data.FrameCount, decoded.Data.StorageKey)
	}
}

// The version is present, is a top-level field, and does not move with the
// event type's own generation.
//
// The second half is what carries the weight. A version derived from the
// suffix in video_job.completed.v1 would be indistinguishable from this one
// today and would silently change the day that suffix does, which is exactly
// the coupling two generations exist to prevent — so the assertion is that
// both event types render the same version, and that it is the package's own
// constant rather than anything parsed out of the type string.
func TestBuildPayload_CarriesItsOwnVersionOnTheWire(t *testing.T) {
	for _, event := range []domain.TerminalEvent{completedEvent(t, 1), failedEvent(t, "boom")} {
		body, err := buildPayload(event, deliveryID(t, "delivery-3"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var decoded map[string]json.RawMessage
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatalf("unexpected error decoding the payload: %v", err)
		}
		raw, present := decoded["version"]
		if !present {
			t.Fatalf("%s: the payload carries no top-level version field", event.EventType())
		}

		var version int
		if err := json.Unmarshal(raw, &version); err != nil {
			t.Fatalf("%s: version is not a number: %v", event.EventType(), err)
		}
		if version != payloadVersion {
			t.Errorf("%s: version = %d, want %d", event.EventType(), version, payloadVersion)
		}

		// The event type's generation appears in "type" and nowhere else:
		// if the version were derived from it, this suffix would have to be
		// readable as the version's source.
		if !strings.HasSuffix(event.EventType().String(), ".v1") {
			t.Fatalf("the event type no longer carries the suffix this assertion is written against: %s", event.EventType())
		}
	}
}

// The delivered body is the Notification context's own contract, not the
// outbox payload forwarded. It carries no field whose only purpose is
// internal routing or relay bookkeeping, and it never names the owner.
func TestBuildPayload_CarriesNoInternalOrOwnerFields(t *testing.T) {
	body, err := buildPayload(completedEvent(t, 7), deliveryID(t, "delivery-4"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unexpected error decoding the payload: %v", err)
	}

	for _, forbidden := range []string{"user_id", "secret", "event_type", "published_at", "outbox_id", "routing_key"} {
		if _, present := decoded[forbidden]; present {
			t.Errorf("the payload carries %q, which is not part of its contract", forbidden)
		}
	}
	data, ok := decoded["data"].(map[string]any)
	if !ok {
		t.Fatalf("data = %v, want an object", decoded["data"])
	}
	if _, present := data["user_id"]; present {
		t.Error("data carries user_id: the receiver is that user, so naming them only widens what a mis-delivery discloses")
	}
}

func completedEvent(t *testing.T, frameCount int) domain.TerminalEvent {
	t.Helper()
	event, err := domain.NewCompletedEvent(
		jobID(t, "job-1"), userID(t, "user-1"), testOccurredAt, frameCount, "frames_job-1.zip")
	if err != nil {
		t.Fatalf("unexpected error building a completion event: %v", err)
	}
	return event
}

func failedEvent(t *testing.T, reason string) domain.TerminalEvent {
	t.Helper()
	event, err := domain.NewFailedEvent(jobID(t, "job-1"), userID(t, "user-1"), testOccurredAt, reason)
	if err != nil {
		t.Fatalf("unexpected error building a failure event: %v", err)
	}
	return event
}

var testOccurredAt = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func jobID(t *testing.T, value string) domain.JobID {
	t.Helper()
	id, err := domain.NewJobID(value)
	if err != nil {
		t.Fatalf("unexpected error building a job id: %v", err)
	}
	return id
}

func userID(t *testing.T, value string) domain.UserID {
	t.Helper()
	id, err := domain.NewUserID(value)
	if err != nil {
		t.Fatalf("unexpected error building a user id: %v", err)
	}
	return id
}

func deliveryID(t *testing.T, value string) domain.DeliveryID {
	t.Helper()
	id, err := domain.NewDeliveryID(value)
	if err != nil {
		t.Fatalf("unexpected error building a delivery id: %v", err)
	}
	return id
}
