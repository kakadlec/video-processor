## ADDED Requirements

### Requirement: An Event Is Delivered Only to Preferences That Existed Before It Occurred

A terminal event SHALL be delivered to a preference only when all of the following hold: the preference belongs to the event's owning user, names the event's type, is `enabled`, and was **created before the event occurred**. A preference failing any of these SHALL receive nothing, and the event SHALL be treated as fully handled rather than as an error.

The creation time is the enrolment boundary, and it is a rule rather than a one-time cutoff. The terminal queue accumulated events for the whole period between `emit-videojob-terminal-events` and this change, so a consumer with no boundary would announce, on its first start, outcomes its users were never subscribed to. `videojob-outbox-relay` answered the same problem with an explicit `occurred_at` cutoff, but a cutoff fixed at a deploy also discards events that are legitimately old — every event that arrived while the consumer was down. A standing instruction that does not announce what happened before it was given needs no constant, keeps holding once the backlog is drained, and cannot be forgotten at the next deploy.

The boundary SHALL be the preference's creation time and SHALL NOT be its last-updated time, so that editing a destination or toggling the enabled flag never moves it.

The two predicates are evaluated at different times, and the difference SHALL be stated rather than left to be inferred. `created_at` is compared against the event's `occurred_at`, so it is **historical**: it asks whether the preference existed when the thing happened. `enabled` is read as it stands **when the event is handled**, so it is a routing switch rather than a record of what was true at the time. The consequence is that an event which occurred while a preference was disabled, and which is still on the queue when its owner re-enables it, SHALL be delivered. This is accepted rather than prevented: the alternative is a persisted enablement history, which is materially more machinery for a case that only arises when the consumer is behind, and `enabled` means "send me these" rather than "and also forget what happened while you were not". What re-enabling SHALL NOT do is re-send anything already resolved — the delivery record refuses a second claim on it.

#### Scenario: An event predating the preference is not delivered

- **GIVEN** a terminal event whose `occurred_at` is earlier than its owner's matching preference's creation time
- **WHEN** the consumer handles it
- **THEN** no request is made to the destination, the event is treated as handled, and no delivery record is created

#### Scenario: An event after enrolment is delivered

- **GIVEN** an enabled webhook preference for an event type, created before a matching event occurred
- **WHEN** the consumer handles that event
- **THEN** a signed request is made to the registered destination

#### Scenario: A disabled preference receives nothing

- **GIVEN** a webhook preference for the event's type whose `enabled` flag is false
- **WHEN** a matching event is handled
- **THEN** no request is made and the event is treated as handled

#### Scenario: Re-enabling before the event is handled delivers it

- **GIVEN** a preference created before an event occurred, disabled while the event was queued, and enabled again before the consumer reached it
- **WHEN** the consumer handles that event
- **THEN** it is delivered, because `enabled` is read when the event is handled and not as of the moment it occurred

#### Scenario: Re-enabling does not re-send a resolved delivery

- **GIVEN** a delivery already resolved for a preference
- **WHEN** its owner disables and re-enables the preference
- **THEN** nothing is re-sent, because the resolved delivery record refuses a second claim

#### Scenario: An event with no matching preference is handled, not failed

- **GIVEN** a terminal event whose owner has registered no preference for its type
- **WHEN** the consumer handles it
- **THEN** nothing is delivered, nothing is recorded, and the event is not dead-lettered or retried

#### Scenario: Re-enabling a preference does not replay the events it missed

- **GIVEN** a preference that was disabled while matching events occurred and has since been re-enabled
- **WHEN** those earlier events are not redelivered by the broker
- **THEN** nothing replays them, and only events occurring after the re-enable are delivered

### Requirement: A Delivery Is Claimed Before It Is Attempted, and a Claim Is Granted Once

Before any outbound request is made, the system SHALL durably claim the delivery identified by the owning user, the event type, the channel, and the job the event names. A claim SHALL be granted at most once for that identity while it holds. When a claim is refused, no request SHALL be made — and what becomes of the message depends on *why* it was refused: a delivery already resolved SHALL be treated as handled, while a claim another consumer still holds SHALL leave the message unhandled and retried later, because the holder may have died before resolving it and nothing else would ever meet that row. `notification-event-consumer` carries the dispositions; what this requirement fixes is that the two refusals are not the same answer.

This is the deduplication obligation `videojob-terminal-events` assigns to whatever consumes its queue, and the claim SHALL precede the attempt rather than record it afterwards. Recording afterwards leaves a read-then-act window that two consumer processes lose: both read "not delivered", and both deliver. The claim SHALL be a single atomic statement in the Notification context's own storage, so the guarantee holds across processes rather than within one.

A claim that was granted but never resolved to an outcome SHALL become reclaimable after a bounded period, so a consumer that dies mid-delivery does not strand the delivery permanently. Reclaiming SHALL preserve the delivery's identifier, so a receiver that deduplicates on it still sees one logical delivery.

That bounded period SHALL be longer than the longest time a single claimant can legitimately hold a claim — its whole attempt budget plus the bounded retries of its resolution — and `notification-persistence` requires the relationship to be enforced at startup. A shorter bound would let a second consumer be granted the claim while the first is still mid-request, and the claim token fences only the database write: it cannot recall a request already on the wire. The bound is therefore sized as a small multiple of that budget, not as a large round number, because the same value bounds how long an abandoned claim delays the rest of the queue.

A refused claim SHALL NOT be reported as a failure. A duplicate is the expected consequence of at-least-once transport, not an incident.

#### Scenario: A redelivered message delivers nothing a second time

- **GIVEN** a terminal event already delivered to a preference and resolved
- **WHEN** the same event is delivered to the consumer again
- **THEN** the claim is refused as already resolved, no request is made to the destination, and the message is treated as handled

#### Scenario: A redelivery meeting a live claim is not treated as handled

- **GIVEN** a terminal event whose claim is held by another consumer and is not yet past the reclaim bound
- **WHEN** the same event is delivered to this consumer
- **THEN** the claim is refused as held, no request is made, and the message is left for a later attempt rather than treated as handled

#### Scenario: Two consumers racing on one event produce one request

- **GIVEN** two consumer processes handling copies of the same terminal event for the same preference at the same time
- **WHEN** both attempt to claim it
- **THEN** exactly one claim is granted and exactly one outbound request is made

#### Scenario: A claim abandoned mid-delivery is reclaimable

- **GIVEN** a delivery whose claim was granted and whose consumer stopped before recording an outcome
- **WHEN** the same event is redelivered after the reclaim period
- **THEN** the claim is granted again, under the same delivery identifier, and the delivery is attempted

#### Scenario: One preference per triple means one request per event

- **GIVEN** a user with preferences for both terminal event types on the webhook channel
- **WHEN** one job completes
- **THEN** exactly one request is made, for the completion event only

### Requirement: The Delivered Payload Is the Notification Context's Own and Carries a Generation

The request body SHALL be a payload defined by the Notification context, carrying **its own version**, the delivery's identifier, the event type, the time the outcome occurred, and the outcome's data. It SHALL NOT be the Video Processing wire payload forwarded verbatim.

Forwarding would make an internal contract public: every subsequent change to what the outbox writes would become a breaking change for every registered endpoint, and the generation suffix `videojob-terminal-events` uses to isolate producer changes would be leaking out of the system it isolates. The Notification payload SHALL therefore be versioned in its own right, independently of the event type's generation.

That version SHALL appear **on the wire**, as a top-level field of the body, and SHALL NOT be left implicit in the event type's own `.v1` suffix. A version a receiver cannot read is not a version: the whole purpose of separating the two generations is that the payload can change while the event type does not, and a receiver that has only the event type to look at cannot tell the two shapes apart. It is a body field rather than a header or a media type so that it falls inside what the signature covers, and so a receiver that logs the body has logged the version with it. The two generations SHALL be free to advance independently, and neither SHALL be derived from the other.

The payload SHALL NOT carry the owning user's identifier: the receiver is that user, addressed at a URL only that user registered, so including it adds nothing and widens what a mis-delivered request would disclose. It SHALL NOT carry the signing secret, and it SHALL NOT carry a credential of any kind — the result storage key names an object that is still only retrievable through an authenticated, owner-scoped route.

#### Scenario: A completion delivery names the job and its result

- **WHEN** a completion event is delivered
- **THEN** the body carries the payload version, the delivery identifier, the event type, the time the outcome occurred, the job identifier, the frame count, and the result storage key

#### Scenario: The payload version is readable without parsing the event type

- **WHEN** any delivery body is received
- **THEN** a top-level version field states which payload contract it follows, it is covered by the signature, and its value is not derived from the event type's own generation suffix

#### Scenario: A failure delivery names the job and its reason

- **WHEN** a failure event is delivered
- **THEN** the body carries the delivery identifier, the event type, the time the outcome occurred, the job identifier, and the failure reason

#### Scenario: The delivered body is not the internal wire payload

- **WHEN** a delivery body is compared with the outbox payload for the same event
- **THEN** they are separate contracts, and the delivered body carries no field whose only purpose is internal routing or relay bookkeeping

### Requirement: Every Delivery Is Signed With the Preference's Secret Over a Timestamp and the Body

Each request SHALL carry an HMAC-SHA256 signature computed with the preference's stored secret over a value that binds the request timestamp to the request body, and SHALL carry that timestamp as a header so a receiver can reject a stale one. The event type and the delivery identifier SHALL also be carried as headers, so a receiver can route and deduplicate without parsing the body.

Signing the body alone SHALL NOT be sufficient: a captured request would then be replayable against the receiver forever. Binding the timestamp into the signed value is what lets a receiver bound that window without the attacker being able to move the timestamp.

The secret SHALL be read on the delivery path and nowhere else, and SHALL NOT appear in the request body, in any header other than as the signature's output, in any log line, or in any stored delivery record.

Every attempt for one delivery SHALL carry the same delivery identifier, so a receiver deduplicating on it treats a retried attempt as the same logical delivery rather than as a new one.

#### Scenario: A delivery carries a signature over the timestamp and body

- **GIVEN** a preference with a stored secret
- **WHEN** a delivery is made to its destination
- **THEN** the request carries a timestamp header and an HMAC-SHA256 signature computed with that secret over the timestamp and the exact body sent

#### Scenario: A receiver can verify with the secret it registered

- **GIVEN** the secret a user submitted when registering the preference
- **WHEN** the receiver recomputes the signature over the timestamp header and the received body
- **THEN** it equals the signature header

#### Scenario: Retried attempts share one delivery identifier

- **GIVEN** a delivery whose first attempt did not succeed
- **WHEN** a later attempt is made within the budget
- **THEN** it carries the same delivery identifier as the first

#### Scenario: The secret does not appear in logs or records

- **GIVEN** a completed delivery, successful or failed
- **WHEN** the process's log output and the stored delivery record are examined
- **THEN** neither contains the secret's value

### Requirement: A Destination Is Refused Both Where It Is Registered and Where It Is Dialled

The system SHALL apply one destination policy at two points: when a preference is written, and again when a connection is opened. The policy SHALL require a transport-secure scheme. At dial time it SHALL be evaluated against the **resolved network address**, not against the hostname alone. Redirects SHALL NOT be followed, the response body SHALL be bounded, and every attempt SHALL be bounded in time.

The guarded dial SHALL be the only path to the destination: the delivery transport's proxy SHALL be nil, and a proxy SHALL NOT be taken from the process environment. This is a condition for the address rule meaning anything, not a hardening preference. With a proxy configured, the connection the guarded dial opens is a connection to the *proxy* — whose address is public and duly approved — after which the proxy resolves the user's hostname itself and connects to whatever it resolves to, so the entire enumeration below is bypassed by an environment variable an operator may have set for a reason that has nothing to do with this system.

The address rule SHALL be expressed as a permission rather than a prohibition: **only an address that is globally reachable unicast may be dialled**, and everything else SHALL be refused. A deny-list of the ranges one happens to think of is the wrong shape for this rule, because the failure mode of forgetting a range is a reachable internal host, while the failure mode of an over-broad refusal is a destination a user must re-register.

Refusal SHALL cover, at minimum, in IPv4: loopback, the unspecified address, link-local (which is what covers `169.254.169.254`), multicast, RFC 1918 private space, **shared address space `100.64.0.0/10`**, **benchmarking `198.18.0.0/15`**, IETF protocol assignments `192.0.0.0/24`, the documentation ranges `192.0.2.0/24`, `198.51.100.0/24` and `203.0.113.0/24`, reserved `240.0.0.0/4`, and `0.0.0.0/8`. The three ranges named in bold are called out because a general "is this a global unicast address" predicate answers *yes* for all of them, so a policy built only from such a predicate would leave exactly the gap this requirement exists to close.

The IPv6 enumeration SHALL be its own list rather than "the equivalents", because the same predicate accepts native IPv6 special-use space that has no IPv4 counterpart. Its normative extent SHALL be **every prefix the IANA IPv6 Special-Purpose Address Registry marks as not globally reachable**, so the list is anchored to a maintained source rather than to what an author happened to recall; the enumeration that follows is that set as it stood when this was written, and it SHALL be reconciled against the registry when implemented. It SHALL cover: the unspecified address and loopback, link-local `fe80::/10`, unique-local `fc00::/7`, multicast `ff00::/8`, documentation `2001:db8::/32` and `3fff::/20`, the discard-only prefix `100::/64`, **benchmarking `2001:2::/48`**, **local-use IPv4/IPv6 translation `64:ff9b:1::/48`**, and **SRv6 SIDs `5f00::/16`**. Benchmarking is called out because it is the exact IPv6 counterpart of the `198.18.0.0/15` refused above, and refusing one twin while admitting the other is the asymmetry a separate list exists to prevent. It SHALL also cover the two prefixes that embed an IPv4 address in an IPv6 one — **6to4 `2002::/16`** and **Teredo `2001::/32`** — which SHALL be refused outright rather than merely unwrapped, since an address in either reaches its embedded IPv4 destination through a relay this policy does not control.

An address in an IPv4-mapped (`::ffff:0:0/96`) or NAT64 (`64:ff9b::/96`) form SHALL be unwrapped to the IPv4 address it embeds and evaluated as that address, so a refused address cannot be reached by rewriting it. **Only the well-known NAT64 prefix `64:ff9b::/96` SHALL be unwrapped; the local-use translation prefix `64:ff9b:1::/48` SHALL be refused outright.** The two differ by one field and read as the same thing, but the local-use prefix exists to translate *inside* an operator's network, so unwrapping it would evaluate an embedded address that is reached through a translator this policy does not control — the same reason 6to4 and Teredo are refused by prefix. The whole enumeration, IPv4 and IPv6 alike, SHALL be explicit and SHALL be tested range by range.

Two evaluations are required rather than one, and neither is redundant. A write-time check alone cannot survive a hostname that resolves differently later, nor a policy tightened after the row was stored. A dial-time check alone silently accepts a destination that will never be delivered to, which is the outcome the closed `Channel` set already rejects for `email`: a preference the system stores and never acts on is indistinguishable, to its owner, from one that works.

A single configuration switch MAY relax the policy for environments that have no TLS and no public addressing — local development and the compose stack. It SHALL default to the restrictive behaviour, and it SHALL relax both the scheme rule and the address rule together, because they are wanted in exactly the same situation and separating them invites enabling half of it where neither belongs.

Preferences stored before this policy took effect SHALL NOT be migrated or deleted. One that the policy now refuses SHALL simply not deliver, and the recorded reason SHALL say so.

#### Scenario: A proxy in the environment does not bypass the address rule

- **GIVEN** a proxy is configured in the process environment
- **WHEN** a delivery is attempted
- **THEN** the connection is opened directly to the resolved destination address, the proxy is not used, and the address rule is evaluated against the destination rather than against the proxy

#### Scenario: A native IPv6 benchmarking or local-translation address is refused

- **GIVEN** a destination resolving to an address in `2001:2::/48` or `64:ff9b:1::/48`
- **WHEN** the connection is opened
- **THEN** it is refused before any packet is sent, and the local-translation address is refused as a prefix rather than unwrapped to what it embeds

#### Scenario: A plaintext destination is refused at registration

- **GIVEN** the policy in its default, restrictive configuration
- **WHEN** a user submits a preference whose destination uses `http`
- **THEN** the request is rejected with `400` and no preference is stored

#### Scenario: An internal address is refused at registration

- **WHEN** a user submits a destination naming a loopback, private, or link-local address, or one in shared, benchmarking, documentation, or reserved space
- **THEN** the request is rejected with `400` and no preference is stored

#### Scenario: A hostname resolving to an internal address is refused at dial

- **GIVEN** a stored destination whose hostname resolves to a private or link-local address
- **WHEN** a delivery to it is attempted
- **THEN** no connection is made to that address, and the delivery is recorded as refused by policy

#### Scenario: A globally-unicast but non-public address is refused

- **GIVEN** a destination whose address is in shared address space, benchmarking space, or reserved space — each of which a general global-unicast predicate accepts
- **WHEN** it is registered, and when a delivery to it is attempted
- **THEN** it is refused at both points

#### Scenario: A native IPv6 special-use address is refused

- **GIVEN** a destination whose address is in IPv6 documentation, discard-only, 6to4, or Teredo space — none of which has an IPv4 counterpart and each of which a general global-unicast predicate accepts
- **WHEN** it is registered, and when a delivery to it is attempted
- **THEN** it is refused at both points, and a 6to4 or Teredo address is refused whatever IPv4 address it embeds

#### Scenario: A mapped form of a refused address is still refused

- **GIVEN** a destination resolving to an IPv4-mapped or NAT64 form of an address the policy refuses
- **WHEN** the dial-time check runs
- **THEN** the embedded address is evaluated and the connection is refused

#### Scenario: A redirect is not followed

- **GIVEN** a destination that answers with a redirect to another address
- **WHEN** a delivery is attempted
- **THEN** the redirect is not followed and the attempt does not succeed

#### Scenario: A previously stored destination that the policy now refuses does not deliver

- **GIVEN** a preference stored before the policy took effect whose destination the policy now refuses
- **WHEN** a matching event is handled
- **THEN** nothing is delivered, the preference row is left untouched, and the recorded reason names the policy

#### Scenario: The relaxation is opt-in and covers both rules

- **GIVEN** the relaxation switch is enabled
- **WHEN** a destination using `http` and naming a private address is registered and delivered to
- **THEN** both are accepted, and with the switch absent or disabled both are refused

### Requirement: Delivery Attempts Are Bounded, and the Outcome Is Recorded Rather Than Retried Indefinitely

A delivery SHALL be attempted a bounded number of times with backoff between attempts, each attempt bounded in time, and the whole budget SHALL be small enough that one unreachable destination costs the consumer seconds rather than minutes. A `2xx` response SHALL record the delivery as delivered. Any other outcome — a non-`2xx` status, a timeout, a refused connection, a policy refusal — SHALL exhaust the budget and record the delivery as failed, naming the last observed reason.

The recorded reason and every log line SHALL be built from a classified error value of this system's own, and SHALL NOT be built from a transport error's own text. A Go transport error is wrapped in a `*url.Error`, whose rendering includes the full request URL, so recording or logging one verbatim would write a destination's query string into the database and the logs. A destination may legitimately carry its credential there — many webhook receivers issue exactly that kind of URL — which makes the transport error's text a second, unguarded copy of a secret the rest of this specification is careful never to store or print.

**Both outcomes SHALL result in the message being acknowledged.** A failing endpoint SHALL NOT be dead-lettered and SHALL NOT be requeued. `videojob-worker`'s "dead-letter, never requeue" disposition is calibrated for a claim-fenced job and SHALL NOT be inherited here: a third party's endpoint being down is not a defect in this system, and a dead-letter queue filling with one user's broken URL buries the messages that queue exists to surface.

Attempts SHALL NOT be conditioned on which status code was observed. The budget is small enough that retrying a permanent rejection costs little, whereas a table classifying third-party status codes as retryable or not is a source of confident wrong guesses about endpoints this system does not control.

A failed delivery SHALL NOT change any `VideoJob`, SHALL NOT be visible to the Video Processing context, and SHALL NOT affect what any HTTP route reports about the job.

The attempt count, the backoff between attempts, and the per-attempt timeout SHALL each be configurable, and each SHALL have a documented default, so the budget can be tuned without a code change. Every term SHALL have one, because the maximum time a claimant can hold a claim is computed from all of them and is what the reclaim bound is validated against; a term left undocumented makes that validation unreproducible.

#### Scenario: A successful delivery is recorded and acknowledged

- **GIVEN** a destination answering `2xx`
- **WHEN** the delivery is made
- **THEN** it is recorded as delivered and the message is acknowledged

#### Scenario: A failing endpoint exhausts the budget and is acknowledged

- **GIVEN** a destination answering `500` to every attempt
- **WHEN** the delivery is attempted
- **THEN** the attempts stop at the configured bound, the delivery is recorded as failed with the observed reason, and the message is acknowledged rather than dead-lettered or requeued

#### Scenario: A destination's query credential reaches neither the record nor the logs

- **GIVEN** a destination whose URL carries a credential in its query string, and whose every attempt fails at the transport layer
- **WHEN** the delivery is recorded and the failure is logged
- **THEN** neither the stored reason nor any log line contains that credential or the destination's query string

#### Scenario: A permanent rejection is not treated specially

- **GIVEN** a destination answering `404`
- **WHEN** the delivery is attempted
- **THEN** it follows the same bounded budget as any other failure and is recorded as failed

#### Scenario: A failed delivery leaves the job untouched

- **GIVEN** a `completed` `VideoJob` whose owner's webhook delivery failed
- **WHEN** the owner reads the job through the API
- **THEN** it still reports `completed`, and nothing in its state or its result records the delivery failure

#### Scenario: One slow destination does not stall the consumer indefinitely

- **GIVEN** a destination that accepts connections and never responds
- **WHEN** a delivery to it is attempted
- **THEN** each attempt ends at the per-attempt timeout and the whole delivery ends within the configured budget
