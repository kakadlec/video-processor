// Package idgen implements domain.UserIDGenerator and domain.UserIDParser
// using UUID v4, keeping the concrete ID-format library out of the domain
// and application layers.
package idgen

import (
	"github.com/google/uuid"

	"video-processor/internal/identity/domain"
)

// Adapter implements domain.UserIDGenerator and domain.UserIDParser using UUID v4.
type Adapter struct{}

// New returns a ready-to-use UUID v4 adapter.
func New() Adapter {
	return Adapter{}
}

// NewUserID mints a new, unique UUID v4 UserID.
func (Adapter) NewUserID() domain.UserID {
	id, err := domain.NewUserID(uuid.NewString())
	if err != nil {
		// uuid.NewString() always returns a non-empty value, the only thing
		// domain.NewUserID validates, so this branch is unreachable in practice.
		panic("identity: idgen produced an invalid UserID: " + err.Error())
	}
	return id
}

// ParseUserID validates value as a UUID v4 and, if valid, wraps its
// canonical form in a UserID.
func (Adapter) ParseUserID(value string) (domain.UserID, error) {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.Version() != 4 || parsed.Variant() != uuid.RFC4122 {
		return domain.UserID{}, domain.ErrInvalidUserID
	}
	return domain.NewUserID(parsed.String())
}
