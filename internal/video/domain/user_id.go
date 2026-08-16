package domain

import "errors"

// ErrInvalidUserID is returned when a value fails UserID construction.
var ErrInvalidUserID = errors.New("video: invalid user id")

// UserID is Video Processing's own opaque identifier for the owner of a
// VideoJob — a distinct Go type from internal/identity/domain.UserID, per
// the "No direct cross-context domain imports" dependency rule. It always
// arrives pre-verified from the Identity context's bearer-auth middleware,
// so unlike VideoJobID it has neither a generator nor a parser port: this
// context never mints one and never parses one from untrusted input.
type UserID struct {
	value string
}

// NewUserID wraps an already-verified identifier value. It enforces only
// this context's own invariant (non-empty).
func NewUserID(value string) (UserID, error) {
	if value == "" {
		return UserID{}, ErrInvalidUserID
	}
	return UserID{value: value}, nil
}

// String returns the identifier's canonical representation.
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
