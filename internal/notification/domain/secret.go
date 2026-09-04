package domain

import "errors"

// ErrInvalidSecret is returned when a value fails Secret construction —
// empty, or shorter than MinSecretLength.
var ErrInvalidSecret = errors.New("notification: invalid signing secret")

// ErrSecretNotSerializable is returned by Secret.MarshalJSON, which refuses
// rather than encoding. Nothing should ever serialize a Secret; failing
// loudly turns a mistake into a 500 instead of a disclosure.
var ErrSecretNotSerializable = errors.New("notification: signing secret must not be serialized")

// MinSecretLength is the shortest accepted signing secret, in bytes. Stated
// as a decision rather than derived (design.md): the shortest length that is
// not obviously weak for HMAC-SHA256 without pushing a user toward
// generating a key with tooling they may not have.
const MinSecretLength = 16

// redactedSecret is what every accidental rendering of a Secret produces.
const redactedSecret = "notification.Secret{REDACTED}"

// Secret is the per-preference key a webhook delivery is signed with.
//
// It cannot be hashed the way a password is — HMAC signing needs the
// original bytes — so non-disclosure is the whole of its protection, and it
// has to hold on every path rather than on the read route alone. Hence the
// shape of this type: the value is unexported, String/GoString render a
// redacted placeholder, MarshalJSON refuses outright, and the bytes come out
// of exactly one deliberately-named accessor.
//
// Omitting String entirely would be worse than useless: fmt reaches an
// unexported field through reflection, so a Secret with no String would
// print its contents under %v and its field name and contents under %+v.
// See NotificationPreference, which carries a Secret and redacts for the
// same reason.
type Secret struct {
	value string
}

// NewSecret validates raw as at least MinSecretLength bytes long. An
// explicitly empty secret is rejected here rather than read as a removal —
// there is no way to remove one, only to replace it.
func NewSecret(raw string) (Secret, error) {
	if len(raw) < MinSecretLength {
		return Secret{}, ErrInvalidSecret
	}
	return Secret{value: raw}, nil
}

// Reveal returns the secret's bytes. It is named to make every disclosure a
// deliberate act that reads as one at the call site: the only legitimate
// callers are the persistence adapter storing the value and, later, the
// delivery adapter signing with it.
func (s Secret) Reveal() string {
	return s.value
}

// IsZero reports whether the Secret is the unset zero value.
func (s Secret) IsZero() bool {
	return s.value == ""
}

// String renders a redacted placeholder so an accidental log line or error
// message cannot carry the value.
func (s Secret) String() string {
	return redactedSecret
}

// GoString does the same for %#v.
func (s Secret) GoString() string {
	return redactedSecret
}

// MarshalJSON refuses. A Secret has no place in any response body, so an
// attempt to encode one is a defect, and surfacing it as an error is
// preferable to emitting a placeholder that reads as a successful write.
func (s Secret) MarshalJSON() ([]byte, error) {
	return nil, ErrSecretNotSerializable
}
