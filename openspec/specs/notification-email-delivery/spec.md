# notification-email-delivery Specification

## Purpose

Defines what a delivered e-mail notification is: the address the preference carries and the rule that validates it, why the destination policy does not judge it, the plain-text unsigned message and what it names, the relay it is sent through and the configuration that must be present for it, and what a recorded delivery on this channel does and does not mean. The claim, the fence, the attempt budget and the enrolment boundary are `notification-webhook-delivery`'s and are shared unchanged; the process that composes the transports is `notification-event-consumer`'s; the preference being resolved is `notification-preferences`'.

## Requirements

### Requirement: An Email Notification Is Delivered to the Address Its Preference Carries

The system SHALL deliver a terminal event to the address stored as the `destination` of an enabled `email` preference for that event's owner and event type. The address SHALL come from the preference and from nowhere else: the system SHALL NOT read an address from the Identity context, SHALL NOT project one locally, and SHALL NOT derive one from the authenticated subject.

The address is therefore **self-declared and unverified** — a user may register an address they do not control. This SHALL be documented in operations guidance rather than left implicit. What bounds the exposure is that a delivery is only ever triggered by a job the same user owns, so the volume is the registrant's own uploads; the system SHALL NOT be usable to send mail that no job of the registrant's produced.

The enrolment boundary, the claim that precedes any attempt, the claim token that fences resolution, the attempt budget, and the recorded outcome SHALL be exactly those `notification-webhook-delivery` and `notification-persistence` define. This capability SHALL NOT restate or vary them: one event with both an `email` and a `webhook` preference produces two records, two claims, and two independent outcomes, because a delivery record is already identified per channel.

#### Scenario: An email preference receives a terminal event

- **GIVEN** an enabled `email` preference for a user and event type, created before the event occurred
- **WHEN** a terminal event for that user and event type is handled
- **THEN** a message is sent to the address the preference carries, and the outcome is recorded against that user, event type, channel, and job

#### Scenario: Both channels of one user receive the same event independently

- **GIVEN** one user with an enabled `webhook` preference and an enabled `email` preference for the same event type, both created before the event occurred
- **WHEN** one terminal event for that event type is handled
- **THEN** two delivery records exist for that job, each claimed and resolved on its own, and the failure of either SHALL NOT prevent or alter the other

#### Scenario: No address is obtained from Identity

- **WHEN** the sources of the Notification context are inspected
- **THEN** no path reads a user's address from the Identity context, from a locally projected copy of one, or from the authenticated subject

### Requirement: An Email Destination Is Validated as an Address, and the Validation Is a Contract

An `email` preference's destination SHALL be validated as a single addr-spec e-mail address. The validation SHALL reject a value carrying a display name, angle brackets, comment syntax, or any form whose canonical rendering is not byte-identical to what was submitted; it SHALL reject a value containing a carriage return, a line feed, or a NUL byte; and it SHALL reject a value longer than **254 bytes**, the longest reverse-path or forward-path an SMTP server is required to accept.

The address SHALL be **ASCII only**: a value containing a byte outside US-ASCII, in either the local part or the domain, SHALL be rejected. This is a property of the transport rather than a preference. An internationalized address requires the sending client to negotiate the SMTPUTF8 extension, and the client this system sends with does not; accepting one at registration would store an address the adapter can never put in an envelope, which is precisely the stored-and-never-honoured outcome the closed channel set exists to prevent. A deployment that later adopts an SMTPUTF8-capable transport may relax this rule, and SHALL do so in the same change rather than ahead of it.

A rejected address SHALL be answered with `400` at registration and SHALL NOT be stored.

This is a contract rather than an implementation detail because it decides what a caller sees and because the failure it prevents is header injection: an SMTP message is a header block, and a destination containing a line break that reached a header would let a registrant add recipients, headers, or a body of their own. Rejecting at registration is the same argument this specification's sibling makes for applying the destination policy at write time and for rejecting a NUL in a signing secret — a value that can never be delivered SHALL fail where its owner can read the error.

The adapter SHALL NOT interpolate any user-supplied string into a message header that has not passed this validation.

#### Scenario: An address carrying a line break is rejected

- **GIVEN** an authenticated user
- **WHEN** they register an `email` preference whose destination contains a carriage return or line feed
- **THEN** the request is rejected with `400` and no preference is stored

#### Scenario: An address carrying a display name is rejected

- **GIVEN** an authenticated user
- **WHEN** they register an `email` preference whose destination is of the form `Name <user@example.com>`
- **THEN** the request is rejected with `400` and no preference is stored

#### Scenario: A non-ASCII or over-long address is rejected

- **GIVEN** an authenticated user
- **WHEN** they register an `email` preference whose destination contains a byte outside US-ASCII, or whose destination exceeds 254 bytes
- **THEN** the request is rejected with `400` and no preference is stored

#### Scenario: A plain address is accepted

- **GIVEN** an authenticated user
- **WHEN** they register an `email` preference whose destination is a single address with no display name
- **THEN** the preference is stored

### Requirement: The Destination Policy Does Not Apply to an Email Destination

The destination policy SHALL NOT be applied to an `email` preference's destination, at registration or at delivery. An `email` destination is envelope data, not a connection target: the delivery path opens a connection to the relay this deployment configures and never to the address the user registered.

This SHALL NOT be implemented as a skipped check. The branch SHALL be taken on the closed channel set, so that a channel outside the set is refused before either branch is reached and no third path exists that could silently apply neither rule. A channel whose destination *is* a connection target SHALL be judged by the policy at write and at dial, as `notification-webhook-delivery` requires.

#### Scenario: An email destination naming an internal-looking host is stored

- **GIVEN** an authenticated user and the destination policy in its default configuration
- **WHEN** they register an `email` preference whose address's domain part would be refused were it a webhook host
- **THEN** the preference is stored, because the address is never dialled

#### Scenario: A webhook destination is still judged by the policy

- **GIVEN** an authenticated user and the destination policy in its default configuration
- **WHEN** they register a `webhook` preference whose destination the policy refuses
- **THEN** the request is rejected with `400` and no preference is stored

### Requirement: The Message Is the Notification Context's Own, Plain Text, and Unsigned

The delivered message SHALL be built from the Notification context's own representation of the outcome — the event type, the job identifier, when it occurred, and that outcome's own fields — and SHALL NOT be the Video Processing wire payload forwarded. It SHALL be `text/plain`, and SHALL carry no HTML, attachment, or remote reference.

It SHALL carry no signature. HMAC signing is the webhook channel's mechanism, and an `email` preference carries no secret to sign with; the delivery path SHALL NOT require one to exist.

The delivery read SHALL NOT load a stored secret for a preference on a channel that does not sign — not merely decline to use one it loaded. An `email` preference may carry a secret, since a write submitting one stores it, and a value that is selected and scanned has entered the process whether or not anything reads it afterwards. `notification-persistence` states the projection rule this depends on.

The message SHALL carry the delivery identifier in a header a receiver can deduplicate on, giving an e-mail receiver the same stable handle the webhook channel gives in its delivery header. The sender address SHALL come from this deployment's configuration and SHALL NOT be derived from the recipient or from any user-supplied value.

The message SHALL NOT contain a credential of any kind, and SHALL NOT contain the recipient's own address in the body.

#### Scenario: The message names the job and the outcome

- **GIVEN** a completed job with a frame count and a stored result key
- **WHEN** its notification is delivered by e-mail
- **THEN** the message names the event type, the job identifier, when it occurred, and the completion's own fields, and carries no signature header

#### Scenario: No secret is loaded for an email preference

- **GIVEN** an `email` preference that carries a stored secret
- **WHEN** it is loaded for delivery
- **THEN** the query returns no secret value for that row, nothing in the process holds one, and delivery proceeds

#### Scenario: The delivery identifier is carried for deduplication

- **GIVEN** a message the relay accepted
- **WHEN** the same event is redelivered and the claim is found already resolved
- **THEN** no second message is sent, and the message that was sent carried a stable delivery identifier a receiver could have deduplicated on

### Requirement: Sending Is Bounded by the Existing Delivery Budget, With No Channel-Specific Term

An e-mail attempt SHALL be bounded in time by the same per-attempt timeout every channel uses, retried the same bounded number of times with the same backoff, and resolved under the same budget. No term SHALL be added, and no term SHALL be made channel-specific.

This is required rather than preferred. The maximum time a claimant may hold a claim is computed from every term, the reclaim bound is validated against that computation at startup, and the process treats a failing validation as fatal. A channel-specific term would make that maximum a comparison across channels and would require the validator to reason about a combination no single delivery ever exhibits, which is exactly the unreproducible validation the budget's design forbids.

#### Scenario: An unreachable relay exhausts the budget and is acknowledged

- **GIVEN** a relay that cannot be reached
- **WHEN** an e-mail delivery is attempted
- **THEN** the attempts are bounded by the existing budget, the delivery is recorded as failed naming a classified reason, and the message is acknowledged rather than dead-lettered or requeued

#### Scenario: No channel-specific budget term exists

- **WHEN** the delivery configuration is inspected
- **THEN** it holds one attempt count, one per-attempt timeout, and one backoff, each governing every channel

### Requirement: The Recorded Reason Names a Classification of This System's Own, Never a Transport Error's Text

The reason recorded for a failed e-mail delivery, and every log line on the path, SHALL be built from a classified error value of this system's own. A transport or protocol error's own text SHALL NOT be recorded or logged, and the recipient address SHALL NOT appear in either.

The rule is the one the webhook path already carries, and it binds here for its own reason: an SMTP error commonly quotes the envelope recipient back in its text, so recording one verbatim would write a user's address into the delivery table and the logs, where the record is deliberately kept free of the destination.

#### Scenario: A refused recipient is recorded without the address

- **GIVEN** a relay that refuses the recipient
- **WHEN** the failure is recorded
- **THEN** the stored reason names a classification of this system's own and contains neither the address nor the relay's own message text

### Requirement: A Recorded Delivery Means the Relay Accepted the Message

For the `email` channel, a delivery recorded as delivered SHALL mean that the configured relay accepted the message for delivery, and SHALL NOT be read as a statement that it reached the recipient. A bounce is asynchronous, is returned to the envelope sender, and is not observed by this system.

The meaning SHALL be stated in operations guidance, because the same recorded status means something stronger on the webhook channel — a receiver's own `2xx` — and an operator reading one table SHALL NOT have to infer the difference.

#### Scenario: An accepted message is recorded as delivered

- **GIVEN** a relay that accepts the message
- **WHEN** the outcome is recorded
- **THEN** the delivery is recorded as delivered, and that status means the relay accepted it rather than that it arrived

### Requirement: The Relay Is Configured, Required at Startup, and Never Authenticated Over an Unencrypted Connection

`cmd/notifier` SHALL require the relay's address and the envelope sender address at startup and SHALL fail fast with an error naming a missing variable. It SHALL NOT start in a mode where an `email` preference is stored and silently never honoured — the failure mode the closed channel set exists to prevent.

Where relay credentials are configured, the adapter SHALL require an encrypted session before authenticating, and SHALL fail the attempt rather than send the credential over an unencrypted connection. Where no credentials are configured, an unencrypted session SHALL be permitted, so a local stack with no TLS works unchanged. The switch that relaxes the destination policy SHALL NOT govern this: it relaxes rules about user-supplied destinations, while the relay is operator-supplied.

`cmd/notification-api` SHALL NOT require any relay configuration. It validates an address; it sends nothing.

#### Scenario: A missing relay address fails the notifier's startup

- **GIVEN** the relay address is absent
- **WHEN** `cmd/notifier` starts
- **THEN** startup fails with an error naming the variable, and nothing is consumed

#### Scenario: Credentials are never sent over an unencrypted session

- **GIVEN** relay credentials are configured and the relay offers no encrypted session
- **WHEN** a delivery is attempted
- **THEN** no credential is sent, the attempt fails, and the recorded reason names a classification of this system's own

#### Scenario: The preference API needs no relay configuration

- **GIVEN** no relay configuration is set
- **WHEN** `cmd/notification-api` starts
- **THEN** it starts and serves the preference routes, including writes naming the `email` channel
