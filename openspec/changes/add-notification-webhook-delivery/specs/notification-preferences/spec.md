## ADDED Requirements

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

## MODIFIED Requirements

### Requirement: A Webhook Preference Carries a Destination and a Signing Secret

A preference on the `webhook` channel SHALL carry an absolute destination URL that satisfies the destination policy `notification-webhook-delivery` defines, and a signing secret. Both SHALL be present when the preference is first created; a request that omits either SHALL be rejected with `400`. A destination that is not an absolute URL, or that the destination policy refuses, SHALL be rejected with `400`. A secret shorter than the required minimum length SHALL be rejected, and so SHALL one containing a NUL byte.

The destination rule is no longer "absolute `http` or `https`". `http` was accepted while nothing dialled a destination, and this capability's own record named the delivery change as the one that would restrict it; that change has arrived. The policy — a transport-secure scheme, and an address that is not loopback, private, link-local, or an instance-metadata address — SHALL be applied here, at registration, and again at dial time. Applying it here is what turns an undeliverable destination into an error its owner can see, rather than into a preference that is stored and silently never acted on: the same argument the closed `Channel` set makes for rejecting `email` outright.

The policy's single relaxation switch, defaulting to restrictive, SHALL govern this route exactly as it governs the dial, so a local development stack that has no TLS can still register a destination it can actually reach.

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

#### Scenario: A non-absolute destination is rejected

- **GIVEN** an authenticated user
- **WHEN** they submit a destination that is relative, empty, or carries a scheme other than `https`
- **THEN** the request is rejected with `400` and no preference is stored

#### Scenario: A plaintext destination is rejected under the default policy

- **GIVEN** an authenticated user and the destination policy in its default configuration
- **WHEN** they submit an absolute `http` destination
- **THEN** the request is rejected with `400` and no preference is stored

#### Scenario: An internal address is rejected under the default policy

- **GIVEN** an authenticated user and the destination policy in its default configuration
- **WHEN** they submit a destination naming a loopback, private, link-local, or instance-metadata address
- **THEN** the request is rejected with `400` and no preference is stored

### Requirement: The Signing Secret Is Never Disclosed

The signing secret SHALL be treated as a credential. No response body SHALL contain it, on any route, for any caller — including the owner who set it. It SHALL NOT appear in any log line or error message. A read that feeds a response SHALL instead report only whether a secret is present.

The secret cannot be stored as a one-way hash the way a password is, because signing a delivery requires the original bytes. Non-disclosure is therefore the whole of its protection, and it SHALL hold on every path rather than on the read route alone.

Exactly one path SHALL load the value: the delivery path, through the single named repository operation `notification-persistence` requires, whose only consumer computes a signature with it. This is a narrowing of the rule rather than an exception carved out of it — the value has to be loadable somewhere for a signature to exist at all, and what keeps non-disclosure true is that the somewhere is singular, named, and provably absent from the HTTP composition root. The routes' own read SHALL remain unable to load it.

#### Scenario: Reading a preference reports only that a secret exists

- **GIVEN** a stored webhook preference carrying a secret
- **WHEN** its owner reads their preferences
- **THEN** the response describes the preference — event type, channel, enabled flag, destination — and reports the presence of a secret as a boolean, and the secret's value appears nowhere in the response

#### Scenario: Writing a preference does not echo the secret back

- **GIVEN** an authenticated user
- **WHEN** they submit a preference carrying a secret and the write succeeds
- **THEN** the response has the same shape a read produces, and the submitted secret is not echoed

#### Scenario: The delivery path is the only one that loads it

- **WHEN** the sources of the HTTP composition root are inspected
- **THEN** none of them calls the operation that loads the secret, and the operation the routes do call selects no secret column

#### Scenario: A delivery does not log the secret it signed with

- **GIVEN** a delivery signed with a stored secret
- **WHEN** the consumer's log output is examined
- **THEN** it names the preference's triple and the delivery identifier, and contains no secret value
