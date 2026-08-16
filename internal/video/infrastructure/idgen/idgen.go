// Package idgen implements domain.VideoJobIDGenerator and
// domain.VideoJobIDParser using UUID v4, keeping the concrete ID-format
// library out of the domain and application layers.
package idgen

import (
	"github.com/google/uuid"

	"video-processor/internal/video/domain"
)

// Adapter implements domain.VideoJobIDGenerator and domain.VideoJobIDParser using UUID v4.
type Adapter struct{}

// New returns a ready-to-use UUID v4 adapter.
func New() Adapter {
	return Adapter{}
}

// NewVideoJobID mints a new, unique UUID v4 VideoJobID.
func (Adapter) NewVideoJobID() domain.VideoJobID {
	id, err := domain.NewVideoJobID(uuid.NewString())
	if err != nil {
		// uuid.NewString() always returns a non-empty value, the only thing
		// domain.NewVideoJobID validates, so this branch is unreachable in practice.
		panic("video: idgen produced an invalid VideoJobID: " + err.Error())
	}
	return id
}

// ParseVideoJobID validates value as a UUID v4 and, if valid, wraps its
// canonical form in a VideoJobID.
func (Adapter) ParseVideoJobID(value string) (domain.VideoJobID, error) {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.Version() != 4 || parsed.Variant() != uuid.RFC4122 {
		return domain.VideoJobID{}, domain.ErrInvalidVideoJobID
	}
	return domain.NewVideoJobID(parsed.String())
}
