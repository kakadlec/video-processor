package domain

import "strings"

// NormalizeEmail defines Identity's application-level case-insensitive lookup policy.
// This is a product decision, not a claim that every mail provider treats the
// local part of an address as case-insensitive.
func NormalizeEmail(email string) (string, error) {
	trimmedEmail := strings.TrimSpace(email)
	if strings.Count(trimmedEmail, "@") != 1 {
		return "", ErrInvalidEmail
	}
	atIndex := strings.IndexByte(trimmedEmail, '@')
	if atIndex <= 0 || atIndex == len(trimmedEmail)-1 || strings.ContainsAny(trimmedEmail, " 	\r\n") {
		return "", ErrInvalidEmail
	}
	normalizedEmail := strings.ToLower(trimmedEmail)
	if !strings.Contains(normalizedEmail[atIndex+1:], ".") {
		return "", ErrInvalidEmail
	}
	return normalizedEmail, nil
}
