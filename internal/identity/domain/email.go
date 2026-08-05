package domain

import (
	"errors"
	"net/mail"
	"strings"
)

// ErrInvalidEmail is returned when a value is not a syntactically valid email address.
var ErrInvalidEmail = errors.New("identity: invalid email address")

// Email is a validated email address. The local part keeps the casing it was
// supplied with (some mail systems treat it as case-sensitive); only the
// domain is lowercased for the stored/display form. Lookups must still be
// case-insensitive end-to-end, so NormalizedForLookup lowercases the whole
// address for use as a repository key — it is distinct from String() on purpose.
type Email struct {
	value string
}

// NewEmail parses and validates raw as an email address.
func NewEmail(raw string) (Email, error) {
	trimmed := strings.TrimSpace(raw)
	addr, err := mail.ParseAddress(trimmed)
	if err != nil || addr.Address == "" {
		return Email{}, ErrInvalidEmail
	}

	at := strings.LastIndex(addr.Address, "@")
	if at < 0 {
		return Email{}, ErrInvalidEmail
	}

	local := addr.Address[:at]
	domain := strings.ToLower(addr.Address[at+1:])
	return Email{value: local + "@" + domain}, nil
}

// String returns the stored/display form: local part as supplied, domain lowercased.
func (e Email) String() string {
	return e.value
}

// NormalizedForLookup returns a fully lowercased form suitable as a case-insensitive repository key.
func (e Email) NormalizedForLookup() string {
	return strings.ToLower(e.value)
}

// Equal reports whether two Emails are the same address for lookup purposes.
func (e Email) Equal(other Email) bool {
	return e.NormalizedForLookup() == other.NormalizedForLookup()
}
