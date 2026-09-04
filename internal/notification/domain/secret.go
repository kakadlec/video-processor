package domain

import (
	"errors"
	"fmt"
	"io"
)

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
// Three layers get it there, each closing what the one before leaves open.
//
// String alone would not: fmt consults Stringer only for %v, %s, %q, %x and
// %X, and reflects over the struct for every other verb, so %d would render
// {%!d(string=<the secret>)}. Format closes that, being consulted for every
// verb — every verb but one. fmt answers %p before it looks for a Formatter,
// and its "bad verb" diagnostic then re-prints the argument with method
// dispatch disabled, so no rendering hook of any kind can intervene.
//
// Which is why the value is held behind a pointer: what reflection reaches
// on that last path is an address rather than the bytes. It also protects
// every type that holds a Secret as a field, where fmt cannot call methods
// at all — see PreferenceIntent and NotificationPreference, which carry one.
// The indirection costs nothing: a Secret is immutable, so copies sharing
// one string are indistinguishable from copies holding their own.
type Secret struct {
	value *string
}

// NewSecret validates raw as at least MinSecretLength bytes long. An
// explicitly empty secret is rejected here rather than read as a removal —
// there is no way to remove one, only to replace it.
func NewSecret(raw string) (Secret, error) {
	if len(raw) < MinSecretLength {
		return Secret{}, ErrInvalidSecret
	}
	return Secret{value: &raw}, nil
}

// Reveal returns the secret's bytes. It is named to make every disclosure a
// deliberate act that reads as one at the call site: the only legitimate
// callers are the persistence adapter storing the value and, later, the
// delivery adapter signing with it.
func (s Secret) Reveal() string {
	if s.value == nil {
		return ""
	}
	return *s.value
}

// IsZero reports whether the Secret is the unset zero value.
func (s Secret) IsZero() bool {
	return s.value == nil || *s.value == ""
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

// Format renders the redacted placeholder for every verb it is consulted
// for, including the numeric and boolean ones String cannot reach. %p is the
// exception fmt gives no hook for; the pointer field above is what covers
// that path.
func (s Secret) Format(f fmt.State, verb rune) {
	_, _ = io.WriteString(f, redactedSecret)
}

// MarshalJSON refuses. A Secret has no place in any response body, so an
// attempt to encode one is a defect, and surfacing it as an error is
// preferable to emitting a placeholder that reads as a successful write.
func (s Secret) MarshalJSON() ([]byte, error) {
	return nil, ErrSecretNotSerializable
}
