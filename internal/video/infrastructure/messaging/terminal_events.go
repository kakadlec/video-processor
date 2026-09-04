package messaging

import (
	"encoding/json"
	"fmt"
	"time"
)

// JobCompletedMessage and JobFailedMessage are the consumer halves of the two
// terminal wire contracts, whose producer halves are
// postgres.videoJobCompletedPayload and postgres.videoJobFailedPayload.
//
// They exist ahead of the consumer that will read them, and deliberately so:
// they are what pins the payloads the terminal relay is already publishing.
// Without them the producer's field names would be unconstrained until a
// consumer shipped, and by then every stored row would carry whatever they
// had drifted into.
//
// As with JobQueuedMessage, nothing in the compiler relates these to the
// producer's structs — infrastructure adapters do not import one another — so
// a renamed field would decode as its zero value here.
// TestTerminalMessagesDecodeTheOutboxPayloads is what pins them.
type JobCompletedMessage struct {
	Type       string    `json:"type"`
	JobID      string    `json:"job_id"`
	UserID     string    `json:"user_id"`
	FrameCount int       `json:"frame_count"`
	StorageKey string    `json:"storage_key"`
	OccurredAt time.Time `json:"occurred_at"`
}

type JobFailedMessage struct {
	Type        string    `json:"type"`
	JobID       string    `json:"job_id"`
	UserID      string    `json:"user_id"`
	ErrorReason string    `json:"error_reason"`
	OccurredAt  time.Time `json:"occurred_at"`
}

// ParseJobCompletedMessage decodes a completion delivery body.
func ParseJobCompletedMessage(body []byte) (JobCompletedMessage, error) {
	var msg JobCompletedMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return JobCompletedMessage{}, fmt.Errorf("video: consumer: decode job completed message: %w", err)
	}
	return msg, nil
}

// ParseJobFailedMessage decodes a failure delivery body.
func ParseJobFailedMessage(body []byte) (JobFailedMessage, error) {
	var msg JobFailedMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return JobFailedMessage{}, fmt.Errorf("video: consumer: decode job failed message: %w", err)
	}
	return msg, nil
}
