package domain

import (
	"errors"
	"fmt"
	"io"
	"time"
)

// ErrPreferenceUserIDRequired is returned when building a preference without a valid UserID.
var ErrPreferenceUserIDRequired = errors.New("notification: user id is required")

// ErrPreferenceEventTypeRequired is returned when building a preference without a valid EventType.
var ErrPreferenceEventTypeRequired = errors.New("notification: event type is required")

// ErrPreferenceChannelRequired is returned when building a preference without a valid Channel.
var ErrPreferenceChannelRequired = errors.New("notification: channel is required")

// ErrPreferenceDestinationRequired is returned when building a preference without a valid Destination.
var ErrPreferenceDestinationRequired = errors.New("notification: destination is required")

// ErrPreferenceTimestampsRequired is returned when building a preference
// whose created-at or updated-at timestamp is the zero time. One sentinel
// covers both because a caller can do nothing different about them: a
// preference with an unset timestamp is a corrupted row either way, and
// neither is a value any request supplies.
var ErrPreferenceTimestampsRequired = errors.New("notification: created at and updated at are required")

// PreferenceIntent is a request to store one preference: what the caller
// asked for, before anything is known about what is already stored.
//
// It is a distinct type from NotificationPreference, and the distinction is
// load-bearing rather than stylistic. An intent carries an *optional*
// secret, because omitting one is a legitimate request — a client that reads
// a preference back never receives the secret, so demanding one on every
// write would make an ordinary read-modify-write destroy a credential the
// caller could not have known to resend. A stored preference always carries
// one.
//
// The secret is held as a pointer so that "omitted" and "empty" stay
// distinguishable all the way down from the request body.
type PreferenceIntent struct {
	userID      UserID
	eventType   EventType
	channel     Channel
	enabled     bool
	destination Destination
	secret      *Secret
}

// NewPreferenceIntent validates the triple and the destination. A nil secret
// is accepted.
//
// It deliberately does NOT enforce "a create requires a secret". That rule
// depends on whether a row already exists, which this constructor cannot
// know and which the use case declines to look up — deciding
// create-from-update in Go would mean the pre-read the persistence design
// exists to avoid. A constructor demanding a secret would therefore either
// reject valid updates or admit an aggregate violating its own invariant.
// The repository enforces it instead, on the row count of a statement that
// names no secret column, and reports it as ErrSecretRequired.
func NewPreferenceIntent(userID UserID, eventType EventType, channel Channel, enabled bool, destination Destination, secret *Secret) (PreferenceIntent, error) {
	if userID.IsZero() {
		return PreferenceIntent{}, ErrPreferenceUserIDRequired
	}
	if eventType.IsZero() {
		return PreferenceIntent{}, ErrPreferenceEventTypeRequired
	}
	if channel.IsZero() {
		return PreferenceIntent{}, ErrPreferenceChannelRequired
	}
	if destination.IsZero() {
		return PreferenceIntent{}, ErrPreferenceDestinationRequired
	}
	// A present-but-zero secret is a construction bug, not an omission:
	// omission is spelled nil.
	if secret != nil && secret.IsZero() {
		return PreferenceIntent{}, ErrInvalidSecret
	}
	// The submitted Secret is copied rather than aliased. Keeping the
	// caller's pointer would let a later assignment to their variable change
	// what an already-validated intent returns, so the check just above
	// would guarantee nothing. Copying the struct is enough: its bytes are
	// reachable only through this package, and a Go string is immutable.
	var owned *Secret
	if secret != nil {
		copied := *secret
		owned = &copied
	}
	return PreferenceIntent{
		userID:      userID,
		eventType:   eventType,
		channel:     channel,
		enabled:     enabled,
		destination: destination,
		secret:      owned,
	}, nil
}

// String renders the intent by the triple it names and nothing else, for
// the same reason NotificationPreference does; see the note on its String.
func (i PreferenceIntent) String() string {
	return fmt.Sprintf("notification.PreferenceIntent{user:%s event_type:%s channel:%s}",
		i.userID, i.eventType, i.channel)
}

// GoString does the same for %#v.
func (i PreferenceIntent) GoString() string { return i.String() }

// Format routes every verb through String.
func (i PreferenceIntent) Format(f fmt.State, verb rune) {
	_, _ = io.WriteString(f, i.String())
}

// UserID returns the owner the preference is stored for.
func (i PreferenceIntent) UserID() UserID { return i.userID }

// EventType returns the event type the preference reacts to.
func (i PreferenceIntent) EventType() EventType { return i.eventType }

// Channel returns the delivery channel.
func (i PreferenceIntent) Channel() Channel { return i.channel }

// Enabled reports whether the preference is being turned on.
func (i PreferenceIntent) Enabled() bool { return i.enabled }

// Destination returns the delivery destination.
func (i PreferenceIntent) Destination() Destination { return i.destination }

// Secret returns the submitted secret and whether one was submitted at all.
// The boolean is what the repository branches on to choose between its two
// statements, so it reports omission rather than emptiness.
func (i PreferenceIntent) Secret() (Secret, bool) {
	if i.secret == nil {
		return Secret{}, false
	}
	return *i.secret, true
}

// NotificationPreference is the Notification bounded context's aggregate
// root: one user's standing instruction to announce one event type through
// one channel. The triple (UserID, EventType, Channel) is its whole
// identity — there is no surrogate id, because nothing references a
// preference by one.
//
// It always carries a secret, which is the invariant that separates it from
// a PreferenceIntent. Nothing in this change loads one: every read path
// projects whether a secret is set rather than the secret itself, so the
// value is not loadable where a response is built. The type exists for the
// delivery change, which is the one caller that legitimately needs the bytes
// to sign with.
type NotificationPreference struct {
	userID      UserID
	eventType   EventType
	channel     Channel
	enabled     bool
	destination Destination
	secret      Secret
	createdAt   time.Time
	updatedAt   time.Time
}

// NewNotificationPreference builds a preference being stored for the first
// time, stamping both timestamps from createdAt.
func NewNotificationPreference(userID UserID, eventType EventType, channel Channel, enabled bool, destination Destination, secret Secret, createdAt time.Time) (*NotificationPreference, error) {
	return RestoreNotificationPreference(userID, eventType, channel, enabled, destination, secret, createdAt, createdAt)
}

// RestoreNotificationPreference reconstructs a preference from
// already-stored values, re-checking every invariant so a corrupted row
// cannot enter the domain as a valid aggregate. It mirrors how
// video_job.go separates NewVideoJob from RestoreVideoJob.
func RestoreNotificationPreference(userID UserID, eventType EventType, channel Channel, enabled bool, destination Destination, secret Secret, createdAt, updatedAt time.Time) (*NotificationPreference, error) {
	if userID.IsZero() {
		return nil, ErrPreferenceUserIDRequired
	}
	if eventType.IsZero() {
		return nil, ErrPreferenceEventTypeRequired
	}
	if channel.IsZero() {
		return nil, ErrPreferenceChannelRequired
	}
	if destination.IsZero() {
		return nil, ErrPreferenceDestinationRequired
	}
	if secret.IsZero() {
		return nil, ErrInvalidSecret
	}
	if createdAt.IsZero() || updatedAt.IsZero() {
		return nil, ErrPreferenceTimestampsRequired
	}
	return &NotificationPreference{
		userID:      userID,
		eventType:   eventType,
		channel:     channel,
		enabled:     enabled,
		destination: destination,
		secret:      secret,
		createdAt:   createdAt,
		updatedAt:   updatedAt,
	}, nil
}

// UserID returns the preference's owner.
func (p *NotificationPreference) UserID() UserID { return p.userID }

// EventType returns the event type the preference reacts to.
func (p *NotificationPreference) EventType() EventType { return p.eventType }

// Channel returns the delivery channel.
func (p *NotificationPreference) Channel() Channel { return p.channel }

// Enabled reports whether deliveries are currently wanted. A disabled
// preference is retained rather than deleted, so re-enabling it does not
// require re-registering an endpoint whose secret was never disclosed.
func (p *NotificationPreference) Enabled() bool { return p.enabled }

// Destination returns the delivery destination.
func (p *NotificationPreference) Destination() Destination { return p.destination }

// Secret returns the signing secret.
func (p *NotificationPreference) Secret() Secret { return p.secret }

// CreatedAt returns when the preference was first stored.
func (p *NotificationPreference) CreatedAt() time.Time { return p.createdAt }

// UpdatedAt returns when the preference was last written.
func (p *NotificationPreference) UpdatedAt() time.Time { return p.updatedAt }

// String renders the preference by its identifying triple and omits every
// mutable field, so a preference that reaches a log line carries no
// destination and, above all, no secret.
//
// This is not decoration. fmt reads an unexported struct field through
// reflection and cannot call methods on what it finds there, so Secret's own
// redaction does not protect a Secret held as a field: without a String here
// a single %+v on a preference would print the secret in full.
func (p NotificationPreference) String() string {
	return fmt.Sprintf("notification.NotificationPreference{user:%s event_type:%s channel:%s}",
		p.userID, p.eventType, p.channel)
}

// GoString does the same for %#v.
func (p NotificationPreference) GoString() string { return p.String() }

// Format routes every verb through String, including the numeric and boolean
// ones fmt answers by reflecting over the struct — %d on a preference would
// otherwise walk into the unexported secret field and print it inside a
// %!d(string=...) diagnostic, which Secret's own Format cannot prevent for
// the same reflection reason described above. %p is the one verb fmt offers
// no hook for; Secret holding its bytes behind a pointer is what covers it.
func (p NotificationPreference) Format(f fmt.State, verb rune) {
	_, _ = io.WriteString(f, p.String())
}

// PreferenceView is what every read path returns: a preference described
// without its secret, reporting only that one is present. It is a plain
// struct because it holds no invariant to protect — the type carries no
// secret field at all, so no handler can serialize one by accident.
type PreferenceView struct {
	UserID      UserID
	EventType   EventType
	Channel     Channel
	Enabled     bool
	Destination Destination
	HasSecret   bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
