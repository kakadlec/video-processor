package domain

import "regexp"

var userIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// UserID is an opaque, validated identifier for a User aggregate. It only
// validates that a supplied value is well-formed; generating new values is
// an infrastructure concern reached through the UserIDGenerator port.
type UserID struct {
	value string
}

// NewUserID validates and wraps a caller-supplied identifier value.
func NewUserID(value string) (UserID, error) {
	if !userIDPattern.MatchString(value) {
		return UserID{}, ErrInvalidUserID
	}
	return UserID{value: value}, nil
}

// String returns the underlying identifier value.
func (id UserID) String() string {
	return id.value
}

// IsZero reports whether id is the zero value (never validated).
func (id UserID) IsZero() bool {
	return id.value == ""
}

// Equals reports whether id and other identify the same user.
func (id UserID) Equals(other UserID) bool {
	return id.value == other.value
}
