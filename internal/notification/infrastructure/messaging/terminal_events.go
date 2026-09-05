package messaging

import (
	"encoding/json"
	"fmt"
	"time"
)

// JobCompletedMessage and JobFailedMessage decode the two terminal payloads
// Video Processing's outbox stores, whose producer halves are that context's
// unexported videoJobCompletedPayload and videoJobFailedPayload.
//
// They are this context's own copy for the reason the package comment gives,
// and nothing in the compiler relates them to the producer's structs — not
// even the loose relation Video Processing's own consumer types have, which
// at least live in the same module tree as an acknowledged pair. A field
// renamed on the producer's side decodes here as its zero value, so a
// notification would be sent naming job "" or carrying no artifact key at
// all, and the decode would return no error.
// TestNotificationTerminalMessagesDecodeTheEmittedPayloads in cmd/api is what
// pins them, against the bytes the producer actually stores rather than
// against a fixture written from this file.
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

// ParseJobCompletedMessage decodes a completion delivery body. A body that
// will not decode is a permanent defect rather than a transient one, which is
// why the consumer's disposition table dead-letters it instead of requeueing:
// redelivering it produces the same failure forever.
func ParseJobCompletedMessage(body []byte) (JobCompletedMessage, error) {
	var msg JobCompletedMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return JobCompletedMessage{}, fmt.Errorf("notification: consumer: decode job completed message: %w", err)
	}
	return msg, nil
}

// ParseJobFailedMessage decodes a failure delivery body.
func ParseJobFailedMessage(body []byte) (JobFailedMessage, error) {
	var msg JobFailedMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return JobFailedMessage{}, fmt.Errorf("notification: consumer: decode job failed message: %w", err)
	}
	return msg, nil
}
