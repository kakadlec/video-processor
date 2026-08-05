package domain

import "errors"

// ErrInvalidUserID is returned when a value fails UserID construction —
// either the domain's own non-empty invariant, or, via UserIDParser
// implementations, format validation of the underlying ID scheme.
var ErrInvalidUserID = errors.New("identity: invalid user id")

// UserID is an opaque identifier for a User. The domain enforces only the
// one invariant it owns — non-zero — and never imports a concrete ID
// library itself. Both minting new IDs and parsing/validating existing ones
// require a concrete scheme (UUID v4, via infrastructure), so both are
// inverted behind ports: UserIDGenerator and UserIDParser.
type UserID struct {
	value string
}

// NewUserID wraps an already-known identifier value, e.g. one produced by a
// UserIDGenerator or UserIDParser implementation. It enforces only the
// domain's own invariant (non-empty); scheme-specific format validation is
// the responsibility of whichever port produced the value.
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

// UserIDGenerator is the port through which new, unique UserIDs are minted.
// The domain depends on this interface; infrastructure supplies the
// concrete implementation (UUID v4 generation).
type UserIDGenerator interface {
	NewUserID() UserID
}

// UserIDParser is the port through which a raw, externally-supplied
// identifier string (e.g. loaded from storage, or a path parameter) is
// validated and converted into a UserID. The domain depends on this
// interface; infrastructure supplies the concrete implementation (UUID v4
// parsing) so no ID-format library is imported here.
type UserIDParser interface {
	ParseUserID(value string) (UserID, error)
}
