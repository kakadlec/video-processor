# notification-preferences Specification

## Purpose

Defines how a user tells the system where and how it should announce the end of one of their video jobs: what identifies a notification preference, which event types and channels are accepted, how the webhook destination and its signing secret are registered and protected, and what an absent preference means to the consumer that will read these rows.
## Requirements
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
### Requirement: The Recognized Event Types Equal the Emitted Terminal Event Types

The event-type values this capability accepts SHALL be exactly the values the Video Processing context publishes for a completed and a failed job. The Notification context SHALL NOT import any Video Processing package to obtain them — it declares its own constants — so the equality SHALL be asserted by an automated test in `internal/contracts`, the test-only package that exists to see both contexts.

That package replaces the composition root the assertion used to live in. Since the HTTP tier was split by bounded context, no process imports both contexts, so there is no root left to host it; the drift it guards against is unchanged, and `ddd-architecture` grants that package the cross-context import on the condition that it declares nothing outside its test files.

Without that assertion the two independently-declared literals can drift with nothing failing, and a consumer would then resolve every delivered event against an event type no stored preference names — a silent total delivery failure rather than a build error.

#### Scenario: The constants are pinned across the two contexts

- **GIVEN** the Notification context declares its accepted event types and the Video Processing context declares the event types it writes to its outbox
- **WHEN** the test suite runs
- **THEN** a test in `internal/contracts` asserts each Notification event-type constant is equal to the corresponding Video Processing constant, and fails if either is renamed or re-versioned alone

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
### Requirement: The Signing Secret Is Never Disclosed

The signing secret SHALL be treated as a credential. No response body SHALL contain it, on any route, for any caller — including the owner who set it. It SHALL NOT appear in any log line or error message. A read that feeds a response SHALL instead report only whether a secret is present.

The secret cannot be stored as a one-way hash the way a password is, because signing a delivery requires the original bytes. Non-disclosure is therefore the whole of its protection, and it SHALL hold on every path rather than on the read route alone.

Exactly one path SHALL load the value: the delivery path, through the single named repository operation `notification-persistence` requires, whose only consumer computes a signature with it. This is a narrowing of the rule rather than an exception carved out of it — the value has to be loadable somewhere for a signature to exist at all, and what keeps non-disclosure true is that the somewhere is singular, named, and provably absent from the Notification context's HTTP service. The routes' own read SHALL remain unable to load it.

The claim is now stronger than it was when one process served every context: the other HTTP services do not link the Notification context's repository at all, so for them the property is enforced by the build rather than by a test. The test SHALL be retained and re-targeted at the one HTTP service that could reach the operation.

#### Scenario: Reading a preference reports only that a secret exists

- **GIVEN** a stored webhook preference carrying a secret
- **WHEN** its owner reads their preferences
- **THEN** the response describes the preference — event type, channel, enabled flag, destination — and reports the presence of a secret as a boolean, and the secret's value appears nowhere in the response

#### Scenario: Writing a preference does not echo the secret back

- **GIVEN** an authenticated user
- **WHEN** they submit a preference carrying a secret and the write succeeds
- **THEN** the response has the same shape a read produces, and the submitted secret is not echoed

#### Scenario: The delivery path is the only one that loads it

- **WHEN** the sources of the Notification context's HTTP service are inspected
- **THEN** none of them calls the operation that loads the secret, and the operation the routes do call selects no secret column

#### Scenario: No other HTTP service can reach the operation at all

- **WHEN** the build graphs of the Identity and Video Processing HTTP services are inspected
- **THEN** neither links the Notification context's repository package, so neither can call the secret-loading operation

#### Scenario: A delivery does not log the secret it signed with

- **GIVEN** a delivery signed with a stored secret
- **WHEN** the consumer's log output is examined
- **THEN** it names the preference's triple and the delivery identifier, and contains no secret value

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

### Requirement: A Preference's Creation Time Is Stable and Is the Enrolment Boundary

A preference's creation time SHALL be stamped when it is first stored and SHALL NOT be changed by any later write to the same triple. Updating the enabled flag, the destination, or the secret SHALL leave it as it was.

It is no longer only an audit field. `notification-webhook-delivery` evaluates every event against it — a preference receives an event only when the event occurred after the preference existed — so a write that reset it would silently re-open the window over outcomes its owner was not subscribed to when they happened. The stability of this value is therefore a requirement of the preference, not an incidental property of the statement that writes it.

#### Scenario: An update leaves the creation time untouched

- **GIVEN** a stored preference
- **WHEN** its owner submits the same triple with a different destination and enabled flag
- **THEN** the stored creation time is unchanged and only the updated-at time advances

#### Scenario: A first write stamps both times

- **GIVEN** no preference stored for a triple
- **WHEN** its owner creates one
- **THEN** the creation time and the updated-at time are both stamped from the same instant

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
