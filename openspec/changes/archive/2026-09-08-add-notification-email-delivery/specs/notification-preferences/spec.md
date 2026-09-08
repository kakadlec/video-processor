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

### Requirement: A Webhook Preference Carries a Destination and a Signing Secret

A preference on the `webhook` channel SHALL carry an absolute destination URL that satisfies the destination policy `notification-webhook-delivery` defines, and a signing secret. This requirement governs the `webhook` channel alone; `notification-email-delivery` and this capability's e-mail requirement govern the other. Both SHALL be present when the preference is first created; a request that omits either SHALL be rejected with `400`. A destination that is not an absolute URL, or that the destination policy refuses, SHALL be rejected with `400`. A secret shorter than the required minimum length SHALL be rejected, and so SHALL one containing a NUL byte.

The destination rule is no longer "absolute `http` or `https`". `http` was accepted while nothing dialled a destination, and this capability's own record named the delivery change as the one that would restrict it; that change has arrived. The policy — a transport-secure scheme, and an address that is not loopback, private, link-local, or an instance-metadata address — SHALL be applied here, at registration, and again at dial time. Applying it here is what turns an undeliverable destination into an error its owner can see, rather than into a preference that is stored and silently never acted on: the same argument that keeps the `Channel` set closed at the channels an adapter actually delivers through.

The policy's single relaxation switch, defaulting to restrictive, SHALL govern this route exactly as it governs the dial, so a local development stack that has no TLS can still register a destination it can actually reach. The two rules are therefore separate rather than one list of accepted schemes: a destination that is not an absolute `http` or `https` URL SHALL be rejected whatever the switch is set to, because no configuration of the policy makes it deliverable, while `http` and internal addresses SHALL be rejected under the default configuration and accepted under the relaxation.

The secret is registered here rather than by the delivery capability because a destination with no secret describes an endpoint that cannot be signed, and a user who registered one would have no way to learn that it will never be called.

The NUL rule is a contract rather than a storage detail because it decides the status code a caller sees. JSON encodes `\u0000` as a real NUL byte, so a request body can carry one; the column the secret is stored in cannot hold it. Rejecting the value at validation is what makes a malformed request a `400` instead of a write that fails with a driver error and surfaces as a `500` the caller can do nothing about.

#### Scenario: Creating a webhook preference without a secret is rejected

- **GIVEN** an authenticated user with no preference stored for a triple
- **WHEN** they submit a preference for that triple carrying a destination but no secret
- **THEN** the request is rejected with `400` and no preference is stored

#### Scenario: A secret carrying a NUL byte is rejected

- **GIVEN** an authenticated user
- **WHEN** they submit a secret of otherwise sufficient length whose bytes include a NUL
- **THEN** the request is rejected with `400` and no preference is stored

#### Scenario: A malformed destination is rejected however the policy is configured

- **GIVEN** an authenticated user, regardless of how the destination policy is configured
- **WHEN** they submit a destination that is relative, empty, or carries a scheme that is neither `http` nor `https`
- **THEN** the request is rejected with `400` and no preference is stored

#### Scenario: A plaintext destination is rejected under the default policy

- **GIVEN** an authenticated user and the destination policy in its default configuration
- **WHEN** they submit an absolute `http` destination
- **THEN** the request is rejected with `400` and no preference is stored

#### Scenario: The relaxation accepts a plaintext destination

- **GIVEN** an authenticated user and the destination policy's relaxation switch enabled
- **WHEN** they submit an absolute `http` destination naming a host the relaxed policy permits
- **THEN** the preference is stored

#### Scenario: An internal address is rejected under the default policy

- **GIVEN** an authenticated user and the destination policy in its default configuration
- **WHEN** they submit a destination naming a loopback, private, link-local, or instance-metadata address
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
