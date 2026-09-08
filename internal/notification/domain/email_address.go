package domain

import "net/mail"

// maxEmailAddressBytes is the longest address this context stores.
//
// 254 is the longest forward-path an SMTP server is required to accept
// (RFC 5321's 256-octet path including the angle brackets), so an address
// beyond it is one no relay is obliged to take. Bounding it here rather than
// at send time makes an unsendable value a 400 its owner can read.
const maxEmailAddressBytes = 254

// parseEmailAddress validates raw as a single addr-spec: no display name, no
// angle brackets, no comment syntax, and no quoted form.
//
// The rule is "canonical rendering is byte-identical to what was submitted",
// which is stricter than mail.ParseAddress on purpose and does most of the
// work here. ParseAddress unquotes a quoted local part and strips brackets
// and comments, so every one of those forms parses successfully and then
// fails the comparison — one check instead of an enumeration of syntaxes
// that would have to keep pace with the RFC.
//
// The byte scan is the half that cannot be delegated, and it is the security
// half. A message is a header block: a destination carrying CR or LF that
// reached a header would let its registrant add recipients or a body of
// their own. Restricting to printable ASCII refuses CR, LF and NUL as a
// consequence rather than as three special cases, and refuses non-ASCII for
// a separate reason — an internationalized address needs the sending client
// to negotiate SMTPUTF8, which net/smtp does not, so storing one would store
// an address the adapter can never put in an envelope.
func parseEmailAddress(raw string) (string, error) {
	if raw == "" || len(raw) > maxEmailAddressBytes {
		return "", ErrInvalidDestination
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] < '!' || raw[i] > '~' {
			return "", ErrInvalidDestination
		}
	}

	parsed, err := mail.ParseAddress(raw)
	if err != nil {
		return "", ErrInvalidDestination
	}
	if parsed.Name != "" || parsed.Address != raw {
		return "", ErrInvalidDestination
	}

	return raw, nil
}
