package domain

import (
	"errors"

	"github.com/google/uuid"
)

// ErrInvalidUserID is returned when a value is not a syntactically valid UUID v4.
var ErrInvalidUserID = errors.New("identity: invalid user id")

// UserID is an opaque, validated identifier for a User. Parsing/format
// validation is a pure function, so the domain delegates it directly to a
// well-tested UUID library rather than hand-rolling it. Generation is the
// impure part — it needs a random source — so it stays inverted behind the
// UserIDGenerator port, keeping the domain deterministic and testable.
type UserID struct {
	value string
}

// NewUserID validates and wraps an existing UUID v4 string, e.g. one loaded from storage.
// The canonical (lowercase, hyphenated) form is stored regardless of input casing.
func NewUserID(value string) (UserID, error) {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.Version() != 4 || parsed.Variant() != uuid.RFC4122 {
		return UserID{}, ErrInvalidUserID
	}
	return UserID{value: parsed.String()}, nil
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
