package domain

import "errors"

// ErrInvalidUserID is returned when a user identifier is empty.
var ErrInvalidUserID = errors.New("video: invalid user id")

// UserID is Video Processing's local representation of an authenticated user.
type UserID struct {
	value string
}

// NewUserID wraps an already-verified user identifier.
func NewUserID(value string) (UserID, error) {
	if value == "" {
		return UserID{}, ErrInvalidUserID
	}
	return UserID{value: value}, nil
}

// String returns the identifier's opaque representation.
func (id UserID) String() string { return id.value }

// IsZero reports whether the identifier is unset.
func (id UserID) IsZero() bool { return id.value == "" }

// Equal reports whether two identifiers represent the same user.
func (id UserID) Equal(other UserID) bool { return id.value == other.value }
