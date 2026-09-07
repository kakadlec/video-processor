## MODIFIED Requirements

### Requirement: A Preference Is Identified By User, Event Type, and Channel

A notification preference SHALL be identified by exactly three values: the owning user, the event type it reacts to, and the channel it delivers through. At most one preference SHALL exist for a given triple. Both the event type and the channel SHALL be drawn from a closed set; a value outside either set SHALL be rejected as a client error and SHALL NOT be stored.

The accepted event types SHALL be `video_job.completed.v1` and `video_job.failed.v1`. The accepted channels SHALL be `webhook` and `email`.

The channel set SHALL remain closed at exactly the channels an adapter delivers through. `email` was refused while none existed, on the grounds that a preference the system stores and never honours is indistinguishable to the user from one that is working; that adapter now exists, and the same grounds are what keep the set closed rather than open. A channel value outside the set SHALL be rejected as a client error and SHALL NOT be stored.

#### Scenario: A preference is stored under a recognized event type and channel

- **GIVEN** an authenticated user
- **WHEN** they register a preference for event type `video_job.completed.v1` on channel `webhook`
- **THEN** the preference is stored for that user, that event type, and that channel

#### Scenario: An unrecognized event type is rejected

- **GIVEN** an authenticated user
- **WHEN** they submit a preference naming an event type outside the accepted set — including an unversioned `video_job.completed`, a future generation, or an arbitrary string
- **THEN** the request is rejected with `400` and no preference is stored

#### Scenario: A preference is stored on the email channel

- **GIVEN** an authenticated user
- **WHEN** they register a preference for event type `video_job.completed.v1` on channel `email`
- **THEN** the preference is stored for that user, that event type, and that channel

#### Scenario: An unrecognized channel is rejected

- **GIVEN** an authenticated user
- **WHEN** they submit a preference naming a channel outside the accepted set — including `sms`, `slack`, or an arbitrary string
- **THEN** the request is rejected with `400` and no preference is stored

## ADDED Requirements

### Requirement: An Email Preference Carries an Address and No Signing Secret

A preference on the `email` channel SHALL carry a destination that is a valid e-mail address, validated as `notification-email-delivery` requires. It SHALL NOT require a signing secret, and a request creating one that submits no secret SHALL succeed rather than being rejected.

This is a narrowing of the create-requires-a-secret rule to the channel that signs, not a relaxation of it. The rule exists because a webhook destination with no secret describes an endpoint that cannot be signed, and a user who registered one would have no way to learn it will never be called. An e-mail message is not signed with a per-preference secret, so the same rule applied to it would demand a value that is stored, never read, and never usable — a credential-shaped field with no credential in it.

A create on the `webhook` channel that submits no secret SHALL continue to be rejected with `400`, unchanged.

Where a secret is nonetheless submitted for an `email` preference, it SHALL be accepted and stored under the same validation every secret receives, and SHALL be disclosed by no read, exactly as a webhook secret is. Nothing on the delivery path SHALL read it.

#### Scenario: Creating an email preference without a secret succeeds

- **GIVEN** an authenticated user with no preference stored for a triple naming channel `email`
- **WHEN** they submit that preference carrying a valid address and no secret
- **THEN** the preference is stored and the response reports that no secret is set

#### Scenario: Creating a webhook preference without a secret is still rejected

- **GIVEN** an authenticated user with no preference stored for a triple naming channel `webhook`
- **WHEN** they submit that preference carrying a destination but no secret
- **THEN** the request is rejected with `400` and no preference is stored

#### Scenario: An email preference reads back without a secret

- **GIVEN** a stored `email` preference created without a secret
- **WHEN** its owner reads their preferences
- **THEN** it is returned with its address and with no secret, and the response reports that none is set
