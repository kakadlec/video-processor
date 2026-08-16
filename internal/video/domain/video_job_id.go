package domain

import "errors"

// ErrInvalidVideoJobID is returned when a value fails VideoJobID construction
// — either the domain's own non-empty invariant, or, via VideoJobIDParser
// implementations, format validation of the underlying ID scheme.
var ErrInvalidVideoJobID = errors.New("video: invalid video job id")

// VideoJobID is an opaque identifier for a VideoJob. The domain enforces
// only the one invariant it owns — non-zero — and never imports a concrete
// ID library itself. Both minting new IDs and parsing/validating existing
// ones require a concrete scheme (UUID v4, via infrastructure), so both are
// inverted behind ports: VideoJobIDGenerator and VideoJobIDParser.
type VideoJobID struct {
	value string
}

// NewVideoJobID wraps an already-known identifier value, e.g. one produced
// by a VideoJobIDGenerator or VideoJobIDParser implementation. It enforces
// only the domain's own invariant (non-empty); scheme-specific format
// validation is the responsibility of whichever port produced the value.
func NewVideoJobID(value string) (VideoJobID, error) {
	if value == "" {
		return VideoJobID{}, ErrInvalidVideoJobID
	}
	return VideoJobID{value: value}, nil
}

// String returns the identifier's canonical representation.
func (id VideoJobID) String() string {
	return id.value
}

// IsZero reports whether the VideoJobID is the unset zero value.
func (id VideoJobID) IsZero() bool {
	return id.value == ""
}

// Equal reports whether two VideoJobIDs identify the same job.
func (id VideoJobID) Equal(other VideoJobID) bool {
	return id.value == other.value
}

// VideoJobIDGenerator is the port through which new, unique VideoJobIDs are
// minted. The domain depends on this interface; infrastructure supplies the
// concrete implementation (UUID v4 generation).
type VideoJobIDGenerator interface {
	NewVideoJobID() VideoJobID
}

// VideoJobIDParser is the port through which a raw, externally-supplied
// identifier string (e.g. a path parameter) is validated and converted into
// a VideoJobID. The domain depends on this interface; infrastructure
// supplies the concrete implementation (UUID v4 parsing) so no ID-format
// library is imported here.
type VideoJobIDParser interface {
	ParseVideoJobID(value string) (VideoJobID, error)
}
