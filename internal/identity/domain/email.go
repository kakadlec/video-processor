package domain

import "strings"

// NormalizeEmail defines the single lookup representation used by Identity.
func NormalizeEmail(email string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if strings.Count(normalized, "@") != 1 {
		return "", ErrInvalidEmail
	}
	at := strings.IndexByte(normalized, '@')
	if at <= 0 || at == len(normalized)-1 || strings.ContainsAny(normalized, " \t\r\n") {
		return "", ErrInvalidEmail
	}
	if !strings.Contains(normalized[at+1:], ".") {
		return "", ErrInvalidEmail
	}
	return normalized, nil
}
