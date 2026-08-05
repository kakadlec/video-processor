package domain

import (
	"net/mail"
	"strings"
)

// NormalizeEmail parses a bare mailbox and normalizes only its domain. The
// local part is preserved because the email specification permits providers to
// treat its casing as significant; Identity does not assume otherwise.
func NormalizeEmail(email string) (string, error) {
	trimmedEmail := strings.TrimSpace(email)
	parsedAddress, err := mail.ParseAddress(trimmedEmail)
	if err != nil || parsedAddress.Address != trimmedEmail {
		return "", ErrInvalidEmail
	}

	atIndex := strings.LastIndexByte(parsedAddress.Address, '@')
	if atIndex <= 0 || atIndex == len(parsedAddress.Address)-1 {
		return "", ErrInvalidEmail
	}

	localPart := parsedAddress.Address[:atIndex]
	domainPart := strings.ToLower(parsedAddress.Address[atIndex+1:])
	return localPart + "@" + domainPart, nil
}
