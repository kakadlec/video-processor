package domain

import (
	"errors"
	"regexp"
)

// ErrInvalidUserID is returned when a value does not match the UUID v4 shape required for a UserID.
var ErrInvalidUserID = errors.New("identity: invalid user id")

var userIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// UserID is an opaque, validated identifier for a User. The domain validates
// its shape but never generates one itself — generation requires randomness,
// which is supplied through the UserIDGenerator port so this package stays
// free of infrastructure dependencies.
type UserID struct {
	value string
}

// NewUserID validates and wraps an existing UUID v4 string, e.g. one loaded from storage.
func NewUserID(value string) (UserID, error) {
	if !userIDPattern.MatchString(value) {
		return UserID{}, ErrInvalidUserID
	}
	return UserID{value: value}, nil
}

// String returns the canonical UUID v4 representation.
func (id UserID) String() string {
	return id.value
}

// IsZero reports whether the UserID is the unset zero value.
func (id UserID) IsZero() bool {
	return id.value == ""
}

// Equal reports whether two UserIDs identify the same user.
func (id UserID) Equal(other UserID) bool {
	return id.value == other.value
}

// UserIDGenerator is the port through which new, unique UserIDs are minted.
// The domain depends on this interface; infrastructure supplies the implementation.
type UserIDGenerator interface {
	NewUserID() UserID
}
