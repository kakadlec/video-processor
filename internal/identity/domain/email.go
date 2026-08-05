package domain

import (
	"net/mail"
	"strings"
)

// maxEmailLength mirrors the RFC 5321 total address length limit.
const maxEmailLength = 254

// Email is a validated email address, trimmed and normalized so that
// lookups are deterministic. The local part preserves its original case;
// only the domain is lower-cased, per RFC 5321 (the local part's case
// significance is up to the receiving host, so it must not be altered).
type Email struct {
	value string
}

// NewEmail validates raw and returns its normalized form.
func NewEmail(raw string) (Email, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) > maxEmailLength {
		return Email{}, ErrInvalidEmail
	}

	addr, err := mail.ParseAddress(trimmed)
	if err != nil || addr.Name != "" || addr.Address != trimmed {
		return Email{}, ErrInvalidEmail
	}

	at := strings.LastIndexByte(trimmed, '@')
	local, domain := trimmed[:at], trimmed[at+1:]
	normalized := local + "@" + strings.ToLower(domain)

	return Email{value: normalized}, nil
}

// String returns the normalized email value.
func (e Email) String() string {
	return e.value
}

// IsZero reports whether e is the zero value (never validated).
func (e Email) IsZero() bool {
	return e.value == ""
}

// Equals reports whether e and other are the same normalized email.
func (e Email) Equals(other Email) bool {
	return e.value == other.value
}
