# notification-preferences Specification

## Purpose

Defines how a user tells the system where and how it should announce the end of one of their video jobs: what identifies a notification preference, which event types and channels are accepted, how the webhook destination and its signing secret are registered and protected, and what an absent preference means to the consumer that will read these rows.

## Requirements

### Requirement: A Preference Is Identified By User, Event Type, and Channel

A notification preference SHALL be identified by exactly three values: the owning user, the event type it reacts to, and the channel it delivers through. At most one preference SHALL exist for a given triple. Both the event type and the channel SHALL be drawn from a closed set; a value outside either set SHALL be rejected as a client error and SHALL NOT be stored.

The accepted event types SHALL be `video_job.completed.v1` and `video_job.failed.v1`. The accepted channels SHALL be `webhook` only. `email` SHALL NOT be accepted until an adapter exists that delivers through it, because a preference the system stores and never honours is indistinguishable to the user from one that is working.

#### Scenario: A preference is stored under a recognized event type and channel

- **GIVEN** an authenticated user
- **WHEN** they register a preference for event type `video_job.completed.v1` on channel `webhook`
- **THEN** the preference is stored for that user, that event type, and that channel

#### Scenario: An unrecognized event type is rejected

- **GIVEN** an authenticated user
- **WHEN** they submit a preference naming an event type outside the accepted set — including an unversioned `video_job.completed`, a future generation, or an arbitrary string
- **THEN** the request is rejected with `400` and no preference is stored

#### Scenario: The email channel is not yet accepted

- **GIVEN** an authenticated user
- **WHEN** they submit a preference naming channel `email`
- **THEN** the request is rejected with `400` and no preference is stored

### Requirement: The Recognized Event Types Equal the Emitted Terminal Event Types

The event-type values this capability accepts SHALL be exactly the values the Video Processing context publishes for a completed and a failed job. The Notification context SHALL NOT import any Video Processing package to obtain them — it declares its own constants — so the equality SHALL be asserted by an automated test in the composition root, which legitimately sees both contexts.

Without that assertion the two independently-declared literals can drift with nothing failing, and a consumer would then resolve every delivered event against an event type no stored preference names — a silent total delivery failure rather than a build error.

#### Scenario: The constants are pinned across the two contexts

- **GIVEN** the Notification context declares its accepted event types and the Video Processing context declares the event types it writes to its outbox
- **WHEN** the test suite runs
- **THEN** a test in the composition root asserts each Notification event-type constant is equal to the corresponding Video Processing constant, and fails if either is renamed or re-versioned alone

### Requirement: A Webhook Preference Carries a Destination and a Signing Secret

A preference on the `webhook` channel SHALL carry an absolute `http` or `https` destination URL and a signing secret. Both SHALL be present when the preference is first created; a request that omits either SHALL be rejected with `400`. A destination that is not an absolute URL with one of those two schemes SHALL be rejected. A secret shorter than the required minimum length SHALL be rejected.

The secret is registered here rather than by the delivery capability because a destination with no secret describes an endpoint that cannot be signed, and a user who registered one would have no way to learn that it will never be called.

#### Scenario: Creating a webhook preference without a secret is rejected

- **GIVEN** an authenticated user with no preference stored for a triple
- **WHEN** they submit a preference for that triple carrying a destination but no secret
- **THEN** the request is rejected with `400` and no preference is stored

#### Scenario: A non-absolute or non-HTTP destination is rejected

- **GIVEN** an authenticated user
- **WHEN** they submit a destination that is relative, empty, or carries a scheme other than `http` or `https`
- **THEN** the request is rejected with `400` and no preference is stored

### Requirement: The Signing Secret Is Never Disclosed

The signing secret SHALL be treated as a credential. No response body SHALL contain it, on any route, for any caller — including the owner who set it. It SHALL NOT appear in any log line or error message. A read SHALL instead report only whether a secret is present.

The secret cannot be stored as a one-way hash the way a password is, because signing a delivery requires the original bytes. Non-disclosure is therefore the whole of its protection, and it SHALL hold on every path rather than on the read route alone.

#### Scenario: Reading a preference reports only that a secret exists

- **GIVEN** a stored webhook preference carrying a secret
- **WHEN** its owner reads their preferences
- **THEN** the response describes the preference — event type, channel, enabled flag, destination — and reports the presence of a secret as a boolean, and the secret's value appears nowhere in the response

#### Scenario: Writing a preference does not echo the secret back

- **GIVEN** an authenticated user
- **WHEN** they submit a preference carrying a secret and the write succeeds
- **THEN** the response has the same shape a read produces, and the submitted secret is not echoed

### Requirement: A Write Omitting the Secret Preserves the Stored One

An update to an existing preference MAY omit the secret. When it is omitted, the stored secret SHALL be preserved unchanged. When it is present, it SHALL replace the stored one. An explicitly empty secret SHALL be rejected as invalid rather than interpreted as a removal.

This follows from non-disclosure: a client that reads a preference back never receives the secret, so requiring one on every write would make an ordinary read-modify-write — toggling the enabled flag, correcting the URL — destroy the credential it did not know.

#### Scenario: Toggling a preference keeps its secret

- **GIVEN** a stored webhook preference carrying a secret
- **WHEN** its owner submits the same triple with `enabled` changed and no secret field
- **THEN** the enabled flag is updated, the stored secret is unchanged, and the preference still reports that a secret is present

#### Scenario: Submitting a new secret replaces the stored one

- **GIVEN** a stored webhook preference carrying a secret
- **WHEN** its owner submits the same triple with a different valid secret
- **THEN** the stored secret is replaced

### Requirement: Preferences Are Read and Written Only By Their Owner

Both preference routes SHALL require a valid bearer token and SHALL be subject to the same per-user rate limiting every other authenticated route carries. A read SHALL return only the calling user's own preferences. A write SHALL apply only to the calling user's own preferences. The owning user SHALL be taken from the authenticated token and SHALL NOT be accepted from the request body, the path, or a query parameter; a caller-supplied user identifier SHALL be ignored rather than honoured.

#### Scenario: An unauthenticated request is rejected

- **GIVEN** a request to either preference route
- **WHEN** it carries no bearer token, or a malformed, expired, or invalid one
- **THEN** it is rejected with `401` before any preference is read or written

#### Scenario: A read returns only the caller's preferences

- **GIVEN** two users each with stored preferences
- **WHEN** one of them reads their preferences
- **THEN** the response contains that user's preferences and none belonging to the other

#### Scenario: A caller cannot write another user's preference

- **GIVEN** an authenticated user
- **WHEN** they submit a preference whose body names a different user identifier
- **THEN** the preference is written for the authenticated caller, and the other user's preferences are unchanged

### Requirement: A Write Upserts Exactly One Preference

The write route SHALL accept exactly one preference per request, naming its event type and channel in the request body. It SHALL create the preference when none exists for that triple and replace the stored one's mutable fields when it does. It SHALL NOT affect any other triple, and SHALL NOT be a full replacement of the caller's preference set — a user who has registered two event types SHALL keep both after writing one.

#### Scenario: Writing one preference leaves the others untouched

- **GIVEN** a user with stored preferences for both accepted event types
- **WHEN** they write a preference for one of them
- **THEN** the other preference is unchanged, and the read route still returns both

#### Scenario: Writing the same triple twice stores one preference

- **GIVEN** an authenticated user
- **WHEN** they write the same event type and channel twice with different destinations
- **THEN** exactly one preference exists for that triple and it carries the destination from the second write

### Requirement: An Absent Preference Means Not Subscribed

The absence of a stored preference for a triple SHALL mean the user has not subscribed, and SHALL NOT be interpreted as a default subscription by any consumer. No preference SHALL be created implicitly — not at user registration, not at job creation, and not by a backfill over existing users.

A webhook has no defensible default value: the system was never given a destination, so there is nothing it could deliver to. The read route SHALL therefore be able to return an empty set, and that SHALL be a successful response rather than an error.

#### Scenario: A user who has registered nothing reads an empty set

- **GIVEN** an authenticated user who has never written a preference
- **WHEN** they read their preferences
- **THEN** the response succeeds with an empty collection

#### Scenario: A disabled preference is retained, not deleted

- **GIVEN** a stored preference
- **WHEN** its owner writes the same triple with `enabled` set to false
- **THEN** the preference is retained with its destination and secret and reported as disabled, so re-enabling it does not require re-registering the endpoint
