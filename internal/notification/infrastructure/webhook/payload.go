// Package webhook delivers a terminal event to the endpoint its owner
// registered, as one signed HTTP request. It is the Notification context's
// only outbound adapter, and the only place on the delivery path that names
// net/http, a dialer, or a hash.
package webhook

import (
	"encoding/json"
	"fmt"
	"time"

	"video-processor/internal/notification/domain"
)

// payloadVersion is the generation of the envelope this package sends, and
// it is the Notification context's own.
//
// It is deliberately not derived from the event type's ".v1" suffix and does
// not move with it. Forwarding Video Processing's payload — or its
// generation — would publish an internal contract: every later change to
// what the outbox writes would become a breaking change for every registered
// endpoint, and the suffix that isolates that context's producers from its
// consumers would be leaking out of the system it isolates.
//
// It travels in the body rather than in a header so that the signature
// covers it, and so a receiver that logs the body has logged the version
// with it. A version a receiver cannot read is not a version: the whole
// point of two generations is that the envelope can change while
// video_job.completed.v1 does not, and the receiver is the party that has to
// tell those apart.
const payloadVersion = 1

// envelope is the delivered body. Field names are the wire contract; they
// are answered to by every registered endpoint and are not renamed without a
// payloadVersion bump.
type envelope struct {
	Version    int          `json:"version"`
	ID         string       `json:"id"`
	Type       string       `json:"type"`
	OccurredAt time.Time    `json:"occurred_at"`
	Data       envelopeData `json:"data"`
}

// envelopeData carries the outcome's own fields, and only those.
//
// There is no user_id: the receiver is that user, addressed at a URL only
// that user registered, so naming them adds nothing and widens what a
// mis-delivered request would disclose. There is no secret and no credential
// of any kind — the storage key names an object that is still only
// retrievable through an authenticated, owner-scoped route — and no relay
// bookkeeping, which is internal routing and not the receiver's business.
//
// FrameCount is a pointer so a completion reporting zero frames is still
// sent as zero: with a plain int, omitempty would drop exactly the value a
// receiver most needs to see.
type envelopeData struct {
	JobID      string `json:"job_id"`
	FrameCount *int   `json:"frame_count,omitempty"`
	StorageKey string `json:"storage_key,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// buildPayload renders the body for one delivery. The bytes it returns are
// both what is sent and what is signed, so the two cannot disagree.
func buildPayload(event domain.TerminalEvent, deliveryID domain.DeliveryID) ([]byte, error) {
	body := envelope{
		Version:    payloadVersion,
		ID:         deliveryID.String(),
		Type:       event.EventType().String(),
		OccurredAt: event.OccurredAt().UTC(),
		Data:       envelopeData{JobID: event.JobID().String()},
	}

	switch event.EventType().String() {
	case domain.EventTypeVideoJobCompleted:
		frameCount := event.FrameCount()
		body.Data.FrameCount = &frameCount
		body.Data.StorageKey = event.StorageKey()
	case domain.EventTypeVideoJobFailed:
		body.Data.Reason = event.Reason()
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("notification: encode delivery payload: %w", err)
	}
	return encoded, nil
}
